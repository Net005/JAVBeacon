// Package silo is the Silo media-server integration's own, independent
// backend - it does not share internal/jellyfin.Service, and Silo's
// /api/v1/integrations/silo/* routes do not call into the Jellyfin
// integration at all (see internal/web/silo.go).
//
// This split was requested explicitly after several bugs this project fixed
// turned out to be cross-contamination from the two integrations sharing one
// service: a nil-slice-to-JSON-null mismatch found via Jellyfin crashed
// Jellyfin's collection sync, a reversed-performer-name fix and a
// Search()-per-row Stash enrichment slowdown both had to be reasoned about
// for two very different clients (Jellyfin's C# plugin scanning a whole
// library vs. Silo's Go plugin matching one item at a time) at once, and a
// Watchlist/collection-tag feature request for Silo alone required touching
// the shared DTO anyway. Silo's plugin SDK is also simply easier to extend
// than Jellyfin's provider model, so this package is free to diverge from
// internal/jellyfin's DTO shape where that divergence is useful (see
// Metadata's own doc comment) without worrying about a second consumer.
//
// Playback and the cover-crop endpoint were briefly left calling into
// internal/jellyfin (see git history around v1.0.241) on the reasoning that
// both were provider-agnostic enough not to be worth duplicating. That was
// overruled: this package now owns its own Playback engine (below, keyed by
// domain.SiloPlaybackSession, its own table) and its own cover-crop route
// (/covers/{id}/silo-primary, registered in internal/web/server.go), so
// internal/web/silo.go never calls into internal/jellyfin.Service at all.
// The two engines share only the deterministic image-crop transform
// function itself (covers.ConformForServing - pure pixel logic, no
// integration-specific behavior) and the stash package's StashApp client,
// same as any two independent callers of a shared library would.
//
// The saved-filter-set ("collection") resolution logic - the one place a
// silent behavioral drift between the two integrations would be most visible
// to a user (a release in one integration's collection but not the other's)
// - is not duplicated either: both this package and internal/jellyfin call
// into internal/filterpreset for it.
package silo

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Net005/JAVBeacon/internal/domain"
	"github.com/Net005/JAVBeacon/internal/filterpreset"
	"github.com/Net005/JAVBeacon/internal/screenshots"
	"github.com/Net005/JAVBeacon/internal/stash"
	"github.com/Net005/JAVBeacon/internal/store"
)

const (
	ProviderIDRelease = "JAVBeacon"
	ProviderIDStash   = "Stash"
)

// stashBridge no longer omits playback writeback now that Playback lives on
// this package too (see the package doc comment) - it needs the checkpoint
// and play-count methods internal/jellyfin's own stashBridge declares.
// stash.Service's own method names are provider-neutral (SaveActivity/AddPlay/
// AddO/Activity - renamed off their original "Jellyfin"-prefixed names once
// Silo started calling them too, since they write to StashApp's per-scene
// play counters, which are shared physical state regardless of which
// integration drove playback). Silo has no "+1 O"/activity-readback route of
// its own (unlike Jellyfin's releases/{id}/activity and releases/{id}/o), so
// AddO/Activity aren't declared here; add them if that ever changes.
// GetPersonDetail still always returns empty (confirmed against the plugin's
// own main.go), so no performer-bio method is declared either.
type stashBridge interface {
	StashSceneMetadata(context.Context, string) (stash.StashSceneMetadata, error)
	SearchSiloScenes(context.Context, string) ([]stash.SiloScene, error)
	SiloSceneByID(context.Context, string) (stash.SiloScene, error)
	SiloWatchlistScenes(context.Context) (map[string]time.Time, bool, error)
	SiloWatchedScenes(context.Context) (map[string]stash.SiloWatchedScene, bool, error)
	SiloChangedScenes(context.Context, time.Time) ([]stash.SiloChangedScene, error)
	SaveActivity(context.Context, string, float64, float64) error
	AddPlay(context.Context, string, time.Time) (int, error)
}

// repository is satisfied by *store.SQLite via a type assertion in
// newService, mirroring internal/jellyfin's own repository interface -
// separate from the wide store.Store interface so Playback's persistence
// contract is explicit and independently testable.
type repository interface {
	SiloPlaybackSession(context.Context, string) (domain.SiloPlaybackSession, error)
	SaveSiloPlaybackSession(context.Context, domain.SiloPlaybackSession) error
}

type Service struct {
	store store.Store
	repo  repository
	stash stashBridge
	shots *screenshots.Cache
	mu    sync.Mutex

	// collMu/collRevision/collIndex/collPresets cache
	// collectionNamesForRelease's per-preset membership computation - see its
	// own doc comment for why. This cache is independent of
	// internal/jellyfin.Service's own (each service instance keeps its own
	// copy), which costs one extra revision-gated rebuild pass on the rare
	// occasion both happen to miss at once - a fine trade for not sharing
	// mutable state between two otherwise-independent services.
	collMu       sync.RWMutex
	collBuildMu  sync.Mutex
	collAsyncMu  sync.Mutex
	collBuilding bool
	collRetryAt  time.Time
	collRevision string
	collIndex    map[int64][]string
	collPresets  []FilterPresetCollection
}

func New(st store.Store, stashService *stash.Service, screenshotCaches ...*screenshots.Cache) *Service {
	return newService(st, stashService, screenshotCaches...)
}

func newService(st store.Store, stashService stashBridge, screenshotCaches ...*screenshots.Cache) *Service {
	repo, _ := st.(repository)
	service := &Service{store: st, repo: repo, stash: stashService}
	if len(screenshotCaches) > 0 {
		service.shots = screenshotCaches[0]
	}
	return service
}

// Metadata mirrors internal/jellyfin.Metadata's JSON shape wherever Silo
// actually consumes the equivalent field (see
// silo-plugin-metadata-javbeacon's provider/types.go), but is free to differ
// where Silo's plugin SDK genuinely has no use for something Jellyfin needs:
//
//   - No PerformerIDs: Jellyfin attaches this as a Person's own provider id so
//     JAVBeaconPersonProvider can serve a bio page for it; Silo's
//     GetPersonDetail has no such routing and always returns empty.
//   - No Tags: confirmed unused by the Silo plugin's own metadataItemFromResult
//     (Genres already carries everything it surfaces).
//   - StashScreenshotURL is new here (Jellyfin has had this gap-fill fallback
//     for a while; Silo never did) - see its own doc comment below.
type Metadata struct {
	ReleaseID      int64    `json:"release_id"`
	ProviderID     string   `json:"provider_id,omitempty"`
	StashSceneID   string   `json:"stash_scene_id,omitempty"`
	Code           string   `json:"code"`
	Title          string   `json:"title"`
	OriginalTitle  string   `json:"original_title,omitempty"`
	Overview       string   `json:"overview,omitempty"`
	PremiereDate   string   `json:"premiere_date,omitempty"`
	ProductionYear int      `json:"production_year,omitempty"`
	Studio         string   `json:"studio,omitempty"`
	Label          string   `json:"label,omitempty"`
	Performers     []string `json:"performers,omitempty"`
	Directors      []string `json:"directors,omitempty"`
	Genres         []string `json:"genres,omitempty"`
	RuntimeSeconds int64    `json:"runtime_seconds,omitempty"`
	// CoverPath points at this integration's own /covers/{id}/silo-primary
	// crop endpoint (see internal/web/server.go's coverSiloPrimary) - not
	// Jellyfin's /covers/{id}/jellyfin-primary. Both routes apply the exact
	// same deterministic crop transform (covers.ConformForServing); only the
	// URL is integration-specific, so a change to one route's auth/caching
	// behavior can never accidentally affect the other's.
	CoverPath         string   `json:"cover_path,omitempty"`
	CoverBackdropPath string   `json:"cover_backdrop_path,omitempty"`
	BackdropURLs      []string `json:"backdrop_urls,omitempty"`
	// StashScreenshotURL is a JAVBeacon-proxied StashApp scene screenshot,
	// populated only when this release has no JAVBeacon-scraped cover of its
	// own (see applyStashScene) - a fallback image source Silo never had
	// before this package existed (Silo's plugin previously had no
	// Stash-screenshot gap-fill at all, unlike Jellyfin). Points at this
	// package's own dedicated /api/v1/integrations/silo/releases/{id}/
	// stash-cover endpoint, not Jellyfin's.
	StashScreenshotURL string `json:"stash_screenshot_url,omitempty"`
	StashPosterURL     string `json:"stash_poster_url,omitempty"`
	SourceURL          string `json:"source_url,omitempty"`
	// CollectionNames lists every saved filter set (see FilterPresetCollection)
	// this release currently matches. Only populated by Metadata (a
	// single-release fetch) - never by Search, to avoid running the
	// filter-preset engine once per bulk search result (see Search's own doc
	// comment for why that cost matters here in particular).
	CollectionNames []string `json:"collection_names,omitempty"`
	// Watchlist mirrors domain.Release.Watchlist - cheap (already loaded on
	// every release row), so unlike CollectionNames/PerformerImages it is
	// populated everywhere, including Search.
	Watchlist bool `json:"watchlist"`
	// PerformerImages maps a performer's display name (as it appears in
	// Performers) to a JAVBeacon-proxied StashApp portrait URL. Only
	// populated by Metadata; never by Search, for the same bulk-cost reason
	// as CollectionNames.
	PerformerImages  map[string]string          `json:"performer_images,omitempty"`
	PerformerDetails map[string]PerformerDetail `json:"performer_details,omitempty"`
	ProviderIDs      map[string]string          `json:"provider_ids"`
}

// PerformerDetail uses only StashApp fields already returned with the scene.
// No extra per-performer GraphQL round trip is needed during a scan.
type PerformerDetail struct {
	StashID   string `json:"stash_id"`
	Birthdate string `json:"birthdate,omitempty"`
}

type LibrarySyncItem struct {
	ReleaseID     int64     `json:"release_id"`
	StashSceneID  string    `json:"stash_scene_id"`
	Title         string    `json:"title,omitempty"`
	Path          string    `json:"path,omitempty"`
	WatchlistedAt time.Time `json:"watchlisted_at,omitempty"`
	WatchedAt     time.Time `json:"watched_at,omitempty"`
	PlayCount     int       `json:"play_count,omitempty"`
}

type LibrarySyncSnapshot struct {
	Revision      string                   `json:"revision"`
	Watchlist     []LibrarySyncItem        `json:"watchlist"`
	Watched       []LibrarySyncItem        `json:"watched"`
	FilterPresets []FilterPresetCollection `json:"filter_presets"`
	ReleaseCodes  map[int64]string         `json:"release_codes,omitempty"`
}

// FilterPresetCollection is one saved filter set resolved to its current,
// ordered membership.
type FilterPresetCollection struct {
	ID         int64   `json:"id"`
	Name       string  `json:"name"`
	ReleaseIDs []int64 `json:"release_ids"`
}

// StashSavedFilters keeps Stash scene saved filters separate from JAVBeacon's
// Release Library presets. A Stash outage fails the requested collection sync
// instead of silently clearing its collections.
func (s *Service) StashSavedFilters(ctx context.Context, selection string) ([]stash.SiloSavedFilter, error) {
	source, ok := s.stash.(interface {
		SiloSavedFilters(context.Context, string) ([]stash.SiloSavedFilter, error)
	})
	if !ok {
		return nil, fmt.Errorf("Stash saved filter source is unavailable")
	}
	return source.SiloSavedFilters(ctx, selection)
}

// Search is deliberately DB-only - no per-result Stash enrichment (text/
// image gap-fill or performer images). See internal/jellyfin.Service.Search's
// doc comment for the full history: this exact policy is what fixed Silo's
// "days to match a library" bug, since Silo's own scan-time matcher and this
// package's own match-unmatched scheduled task (in the Silo plugin repo) both
// call this once per unmatched library item.
func (s *Service) Search(ctx context.Context, query string, limit int) ([]Metadata, error) {
	if limit < 1 || limit > 100 {
		limit = 25
	}
	needle := strings.TrimSpace(query)
	// Scan titles are usually exact JAV release codes. Use the dedicated
	// video_id predicate first; fuzzy search joins many text fields and is
	// substantially slower on large libraries.
	var rows []domain.Release
	var err error
	if likelyReleaseCode(needle) {
		// Scan filenames commonly contain a release code. An exact miss is
		// definitive for Silo's matcher: a broad text search cannot turn a
		// different code into a safe match, and costs seconds on large catalogs.
		variants := releaseCodeVariants(needle)
		for _, variant := range variants {
			rows, err = s.store.Releases(ctx, domain.ReleaseFilter{VideoID: variant, Limit: limit, StashLinked: true})
			if err != nil || len(rows) > 0 {
				break
			}
		}
	} else {
		rows, err = s.store.Releases(ctx, domain.ReleaseFilter{Search: needle, Limit: limit, StashLinked: true})
	}
	if err != nil {
		return nil, err
	}
	out := make([]Metadata, 0, len(rows))
	for _, r := range rows {
		out = append(out, s.metadata(r))
	}
	return out, nil
}

// SearchWithStashFallback prefers an exact local JAV release. A broad local
// text hit must not hide an exact Stash filename match when JAVBeacon has no
// release for that scene.
func (s *Service) SearchWithStashFallback(ctx context.Context, query string, limit int) ([]Metadata, error) {
	// The manual match dialog can search a known Stash scene ID directly.
	// This bypasses Stash's text index, which may omit a scene whose code
	// and local filename use different separators.
	if sceneID, explicit := stashSceneIDQuery(query); sceneID != "" && s.stash != nil {
		item, err := s.StashMetadata(ctx, sceneID)
		if err == nil {
			return []Metadata{item}, nil
		}
		if explicit {
			return nil, err
		}
	}
	// Ordinary filename stems can trigger a costly broad JAV text search.
	// Ask Stash's scene index first; an exact local filename wins over any
	// unrelated title text in JAVBeacon's wider catalog.
	if !likelyReleaseCode(strings.TrimSpace(query)) {
		stashRows, err := s.SearchStashScenes(ctx, query)
		if err != nil {
			return nil, err
		}
		if len(stashRows) > 0 {
			return stashRows, nil
		}
	}
	rows, err := s.Search(ctx, query, limit)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		if sameReleaseCode(row.Code, query) {
			return rows, nil
		}
	}
	if !likelyReleaseCode(strings.TrimSpace(query)) {
		return rows, nil // Stash was already queried above.
	}
	stashRows, err := s.SearchStashScenes(ctx, query)
	if err != nil {
		return nil, err
	}
	if len(stashRows) > 0 {
		return stashRows, nil
	}
	return rows, nil
}

// stashSceneIDQuery recognizes a numeric Stash scene ID, stash:<id>, or a
// Stash scene URL pasted into the manual match search box.
func stashSceneIDQuery(query string) (string, bool) {
	value := strings.TrimSpace(query)
	explicit := false
	if id, ok := strings.CutPrefix(strings.ToLower(value), "stash:"); ok {
		value, explicit = id, true
	} else if parsed, err := url.Parse(value); err == nil && parsed.Host != "" {
		parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
		if len(parts) == 2 && parts[0] == "scenes" {
			value, explicit = parts[1], true
		}
	}
	if value == "" {
		return "", explicit
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return "", explicit
		}
	}
	return value, explicit
}

// SearchStashScenes uses StashApp's scene index when no JAVBeacon release
// matches the filename-derived query. Only exact code/file stems are returned.
func (s *Service) SearchStashScenes(ctx context.Context, query string) ([]Metadata, error) {
	if s.stash == nil {
		return nil, nil
	}
	boundedCtx, cancel := context.WithTimeout(ctx, stashLookupTimeout)
	defer cancel()
	scenes, err := s.stash.SearchSiloScenes(boundedCtx, query)
	if err != nil {
		// A Stash outage must not fail Silo's entire scan of an unmatched file.
		return nil, nil
	}
	if len(scenes) > 1 {
		best, _ := bestStashScene(scenes)
		scenes = []stash.SiloScene{best}
	}
	out := make([]Metadata, 0, len(scenes))
	for _, scene := range scenes {
		// The file may already be linked to a JAVBeacon release under a
		// different code. Prefer its richer metadata, but only while local.
		linked, err := s.store.Releases(ctx, domain.ReleaseFilter{StashSceneID: scene.ID, StashLinked: true, Limit: 2})
		if err != nil {
			return nil, err
		}
		if len(linked) == 1 {
			item := s.metadata(linked[0])
			item.Code = scene.Code // Keep the filename Silo actually matched.
			out = append(out, item)
			continue
		}
		if len(linked) == 0 {
			out = append(out, s.stashOnlyMetadata(ctx, scene))
		}
	}
	return out, nil
}

// bestStashScene chooses one exact-code/file-stem candidate using metadata
// completeness and playback evidence. Exact ties use the Stash scene ID so
// manual and automatic matching choose the same scene regardless of result order.
func bestStashScene(scenes []stash.SiloScene) (stash.SiloScene, bool) {
	if len(scenes) == 0 {
		return stash.SiloScene{}, false
	}
	best := scenes[0]
	bestTotal, bestPlayback, bestMetadata := stashSceneScore(best)
	for _, scene := range scenes[1:] {
		total, playback, metadata := stashSceneScore(scene)
		if total > bestTotal ||
			(total == bestTotal && playback > bestPlayback) ||
			(total == bestTotal && playback == bestPlayback && metadata > bestMetadata) ||
			(total == bestTotal && playback == bestPlayback && metadata == bestMetadata &&
				(stashPlayedAfter(scene.LastPlayedAt, best.LastPlayedAt) ||
					(!stashPlayedAfter(best.LastPlayedAt, scene.LastPlayedAt) && stashSceneIDAfter(scene.ID, best.ID)))) {
			best, bestTotal, bestPlayback, bestMetadata = scene, total, playback, metadata
		}
	}
	return best, true
}

func stashSceneIDAfter(a, b string) bool {
	first, errA := strconv.ParseUint(a, 10, 64)
	second, errB := strconv.ParseUint(b, 10, 64)
	if errA == nil && errB == nil {
		return first > second
	}
	return a > b
}

func stashPlayedAfter(a, b string) bool {
	first, errA := time.Parse(time.RFC3339Nano, a)
	second, errB := time.Parse(time.RFC3339Nano, b)
	return errA == nil && (errB != nil || first.After(second))
}

func stashSceneScore(scene stash.SiloScene) (total, playback, metadata int) {
	if scene.Title != "" {
		metadata++
	}
	if scene.Details != "" {
		metadata += 3
	}
	if scene.Date != "" {
		metadata += 2
	}
	if scene.Studio != "" {
		metadata += 2
	}
	if len(scene.Performers) > 0 {
		metadata += 2 + min(len(scene.Performers), 3)
	}
	if len(scene.Tags) > 0 {
		metadata += 1 + min(len(scene.Tags), 2)
	}
	if scene.ScreenshotURL != "" {
		metadata += 2
	}
	if scene.PlayCount > 0 {
		playback += 6 + min(scene.PlayCount, 5)
	}
	if scene.OCounter > 0 {
		playback += 3 + min(scene.OCounter, 3)
	}
	if scene.LastPlayedAt != "" {
		playback += 2
	}
	if scene.PlayDuration > 0 {
		playback++
	}
	return metadata + playback, playback, metadata
}

func (s *Service) StashMetadata(ctx context.Context, sceneID string) (Metadata, error) {
	scene, err := s.stash.SiloSceneByID(ctx, sceneID)
	if err != nil {
		return Metadata{}, err
	}
	return s.stashOnlyMetadata(ctx, scene), nil
}

func (s *Service) stashOnlyMetadata(ctx context.Context, scene stash.SiloScene) Metadata {
	m := Metadata{ProviderID: "stash:" + scene.ID, StashSceneID: scene.ID, Code: scene.Code, Title: scene.Title, OriginalTitle: scene.Title, Overview: scene.Details, PremiereDate: scene.Date, Studio: scene.Studio, Genres: append([]string(nil), scene.Tags...), ProviderIDs: map[string]string{"Stash": scene.ID}}
	if tagID := s.watchlistTagID(ctx); tagID != "" {
		m.Watchlist = containsTagID(scene.TagIDs, tagID)
	}
	if len(scene.Date) >= 4 {
		m.ProductionYear, _ = strconv.Atoi(scene.Date[:4])
	}
	if scene.ScreenshotURL != "" {
		m.StashScreenshotURL = "/api/v1/integrations/silo/stash/scenes/" + url.PathEscape(scene.ID) + "/cover"
		m.StashPosterURL = m.StashScreenshotURL + "?variant=poster"
		m.CoverPath = m.StashPosterURL
	}
	m.PerformerImages = map[string]string{}
	m.PerformerDetails = map[string]PerformerDetail{}
	for _, p := range scene.Performers {
		m.Performers = append(m.Performers, p.Name)
		if p.ImagePath != "" {
			m.PerformerImages[p.Name] = "/api/v1/integrations/performers/" + url.PathEscape(p.ID) + "/image"
		}
		m.PerformerDetails[p.Name] = PerformerDetail{StashID: p.ID, Birthdate: p.Birthdate}
	}
	return m
}

func likelyReleaseCode(query string) bool {
	query = strings.TrimSpace(query)
	if query == "" || len(query) > 32 || strings.ContainsAny(query, " \t\n") {
		return false
	}
	letters, digits := 0, 0
	for _, r := range query {
		switch {
		case r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z':
			letters++
		case r >= '0' && r <= '9':
			digits++
		case r == '-' || r == '_':
		default:
			return false
		}
	}
	return letters >= 2 && digits >= 1
}

// releaseCodeVariants handles filename separators without falling through to
// an expensive broad text search. Only exact video_id predicates are used.
func sameReleaseCode(a, b string) bool {
	normalize := func(value string) string {
		var out strings.Builder
		for _, r := range strings.ToUpper(strings.TrimSpace(value)) {
			if r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
				out.WriteRune(r)
			}
		}
		return out.String()
	}
	return normalize(a) == normalize(b)
}

func releaseCodeVariants(query string) []string {
	out := []string{query}
	seen := map[string]bool{strings.ToUpper(query): true}
	add := func(value string) {
		key := strings.ToUpper(value)
		if value != "" && !seen[key] {
			out = append(out, value)
			seen[key] = true
		}
	}
	compact := strings.NewReplacer("-", "", "_", "").Replace(query)
	add(compact)
	firstDigit := strings.IndexFunc(compact, func(r rune) bool { return r >= '0' && r <= '9' })
	if firstDigit > 0 && firstDigit < len(compact) {
		add(compact[:firstDigit] + "-" + compact[firstDigit:])
	}
	return out
}

// stashLookupTimeout bounds every individual Stash round trip made while
// building Metadata/Search results, independent of whatever deadline (if
// any) the caller's own context carries - see
// internal/jellyfin.stashLookupTimeout's doc comment for the incident this
// guards against.
const stashLookupTimeout = 5 * time.Second

// collectionIndexTimeout bounds collectionMembershipIndex's rebuild pass.
const collectionIndexTimeout = 20 * time.Second

func (s *Service) watchlistTagID(ctx context.Context) string {
	settings, err := s.store.Settings(ctx)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(settings["stash_watchlist_tag_id"])
}

func containsTagID(tags []string, tagID string) bool {
	for _, id := range tags {
		if id == tagID {
			return true
		}
	}
	return false
}

func (s *Service) stashSceneMetadata(ctx context.Context, sceneID string) (stash.StashSceneMetadata, bool) {
	if s.stash == nil || sceneID == "" {
		return stash.StashSceneMetadata{}, false
	}
	boundedCtx, cancel := context.WithTimeout(ctx, stashLookupTimeout)
	defer cancel()
	scene, err := s.stash.StashSceneMetadata(boundedCtx, sceneID)
	if err != nil {
		return stash.StashSceneMetadata{}, false
	}
	return scene, true
}

func (s *Service) Metadata(ctx context.Context, releaseID int64) (Metadata, error) {
	r, err := s.store.Release(ctx, releaseID)
	if err != nil {
		return Metadata{}, err
	}
	m := s.metadataForRelease(ctx, r)
	m.CollectionNames = s.collectionNamesForRelease(ctx, releaseID)
	return m, nil
}

// metadataForRelease builds r's Metadata with the same single-Stash-fetch
// enrichment (text/image gap-fill plus performer images) internal/jellyfin
// uses.
func (s *Service) metadataForRelease(ctx context.Context, r domain.Release) Metadata {
	m := s.metadata(r)
	if scene, ok := s.stashSceneMetadata(ctx, r.StashSceneID); ok {
		m = applyStashScene(r, m, scene)
		applyPerformerImages(scene, &m)
		applyPerformerDetails(scene, &m)
		if tagID := s.watchlistTagID(ctx); tagID != "" {
			m.Watchlist = containsTagID(scene.TagIDs, tagID)
		}
	}
	return m
}

// applyPerformerImages attaches a JAVBeacon-proxied StashApp portrait URL to
// every performer name already on m that StashApp has a photo for, matching
// by name - including a collision-safe reversed-word-order fallback, since
// JAVBeacon and StashApp surprisingly often scrape the same performer in
// opposite word order (e.g. "Hamasaki Mao" vs. StashApp's own "Mao
// Hamasaki"). This is a direct, deliberate duplicate of
// internal/jellyfin.applyPerformerImages - see this package's own doc
// comment for why that duplication is the accepted tradeoff here, and keep
// the two in sync if this algorithm ever changes.
func applyPerformerImages(scene stash.StashSceneMetadata, m *Metadata) {
	if len(scene.Performers) == 0 {
		return
	}
	imagePathFor := func(id string) string {
		return fmt.Sprintf("/api/v1/integrations/performers/%s/image", url.PathEscape(id))
	}
	type performer struct{ id, imagePath string }
	byExactName := make(map[string]performer, len(scene.Performers))
	for _, p := range scene.Performers {
		name := strings.TrimSpace(p.Name)
		if name == "" || p.ID == "" {
			continue
		}
		byExactName[name] = performer{id: p.ID, imagePath: p.ImagePath}
	}
	if len(byExactName) == 0 {
		return
	}
	images := make(map[string]string, len(byExactName))
	for name, p := range byExactName {
		if p.imagePath != "" {
			images[name] = imagePathFor(p.id)
		}
	}
	for name, p := range byExactName {
		fields := strings.Fields(name)
		if len(fields) != 2 {
			continue
		}
		reversed := fields[1] + " " + fields[0]
		if strings.EqualFold(reversed, name) {
			continue
		}
		if _, isSomeoneElsesRealName := byExactName[reversed]; isSomeoneElsesRealName {
			continue
		}
		if p.imagePath != "" {
			if _, exists := images[reversed]; !exists {
				images[reversed] = imagePathFor(p.id)
			}
		}
	}
	if len(images) > 0 {
		m.PerformerImages = images
	}
}

func applyPerformerDetails(scene stash.StashSceneMetadata, m *Metadata) {
	if len(scene.Performers) == 0 {
		return
	}
	details := make(map[string]PerformerDetail)
	for _, p := range scene.Performers {
		if p.ID == "" || p.Name == "" {
			continue
		}
		detail := PerformerDetail{StashID: p.ID, Birthdate: p.Birthdate}
		details[p.Name] = detail
		fields := strings.Fields(p.Name)
		if len(fields) == 2 {
			reversed := fields[1] + " " + fields[0]
			if reversed != p.Name {
				if _, exists := details[reversed]; !exists {
					details[reversed] = detail
				}
			}
		}
	}
	if len(details) > 0 {
		m.PerformerDetails = details
	}
}

// collectionNamesForRelease resolves every saved filter set that currently
// matches releaseID. Best-effort: any storage error yields no names rather
// than failing the whole metadata request.
func (s *Service) collectionNamesForRelease(_ context.Context, releaseID int64) []string {
	// A preset-index rebuild can run for ten seconds. Never make the scan's
	// per-release metadata RPC wait for it; return the last complete snapshot.
	s.collMu.RLock()
	names := append([]string(nil), s.collIndex[releaseID]...)
	s.collMu.RUnlock()
	s.collAsyncMu.Lock()
	if !s.collBuilding && time.Now().After(s.collRetryAt) {
		s.collBuilding = true
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), collectionIndexTimeout+time.Second)
			defer cancel()
			_, _, _ = s.collectionIndexAndPresets(ctx)
			s.collAsyncMu.Lock()
			s.collBuilding = false
			s.collRetryAt = time.Now().Add(time.Minute)
			s.collAsyncMu.Unlock()
		}()
	}
	s.collAsyncMu.Unlock()
	return names
}

// collectionMembershipIndex returns a releaseID -> matching-preset-names map,
// rebuilding it only when jellyfin_library_revision has moved since the last
// build (the same staleness signal internal/jellyfin and the Silo plugin's
// collection-sync task already key off of).
func (s *Service) collectionMembershipIndex(ctx context.Context) (map[int64][]string, error) {
	index, _, err := s.collectionIndexAndPresets(ctx)
	return index, err
}

// collectionPresets returns the same per-preset release-ID lists LibrarySync
// needs, sharing collectionMembershipIndex's revision-gated cache.
func (s *Service) collectionPresets(ctx context.Context) ([]FilterPresetCollection, error) {
	_, presets, err := s.collectionIndexAndPresets(ctx)
	return presets, err
}

func (s *Service) collectionIndexAndPresets(ctx context.Context) (map[int64][]string, []FilterPresetCollection, error) {
	settings, err := s.store.Settings(ctx)
	if err != nil {
		return nil, nil, err
	}
	revision := settings["jellyfin_library_revision"]

	s.collBuildMu.Lock()
	defer s.collBuildMu.Unlock()
	s.collMu.RLock()
	if s.collIndex != nil && s.collRevision == revision {
		index, presets := s.collIndex, s.collPresets
		s.collMu.RUnlock()
		return index, presets, nil
	}
	s.collMu.RUnlock()

	boundedCtx, cancel := context.WithTimeout(ctx, collectionIndexTimeout)
	defer cancel()

	presets, err := s.store.FilterPresets(boundedCtx)
	if err != nil {
		return nil, nil, err
	}
	index := map[int64][]string{}
	// Never left nil - see internal/jellyfin's identical comment for the
	// exact NullReferenceException this avoids on the Jellyfin side; kept
	// here for the same not-null-by-convention guarantee even though Silo's
	// own plugin decodes this into a Go slice, which tolerates a JSON null
	// fine - consistency between the two integrations' wire shape is the
	// point.
	collections := []FilterPresetCollection{}
	for _, preset := range presets {
		filter, ok := filterpreset.FromState(preset.State, settings)
		if !ok {
			continue
		}
		ids, err := filterpreset.ResolveReleaseIDs(boundedCtx, s.store, filter)
		if err != nil {
			// Never cache a partial index as a complete revision: otherwise
			// tags stay absent until an unrelated library change arrives.
			return nil, nil, fmt.Errorf("resolve saved filter %q: %w", preset.Name, err)
		}
		if tagName := filterpreset.SanitizeTagName(preset.Name); tagName != "" {
			for _, id := range ids {
				index[id] = append(index[id], tagName)
			}
		}
		collections = append(collections, FilterPresetCollection{ID: preset.ID, Name: preset.Name, ReleaseIDs: ids})
	}
	s.collMu.Lock()
	s.collRevision = revision
	s.collIndex = index
	s.collPresets = collections
	s.collMu.Unlock()
	return index, collections, nil
}

// applyStashScene fills gaps in JAVBeacon's own scraped metadata directly
// from an already-fetched StashApp scene. It only ever fills a field that is
// currently EMPTY - StashApp data never overrides anything JAVBeacon itself
// already has. Direct duplicate of internal/jellyfin.applyStashScene except
// for the stash-cover path, which points at this package's own dedicated
// Silo route rather than Jellyfin's.
func applyStashScene(r domain.Release, m Metadata, scene stash.StashSceneMetadata) Metadata {
	missingImage := r.ImageURL == ""
	if r.Title == "" {
		if scene.Details != "" {
			m.Overview = scene.Details
		} else if scene.Title != "" {
			m.Overview = scene.Title
		}
	}
	if r.Studio == "" && scene.Studio != "" {
		m.Studio = scene.Studio
	}
	if len(r.Actresses) == 0 && len(scene.Performers) > 0 {
		m.Performers = scene.PerformerNames()
	}
	if len(r.Genres) == 0 && len(scene.Tags) > 0 {
		m.Genres = scene.Tags
	}
	if missingImage && scene.ScreenshotURL != "" {
		m.StashScreenshotURL = fmt.Sprintf("/api/v1/integrations/silo/releases/%d/stash-cover", r.ID)
		m.StashPosterURL = m.StashScreenshotURL + "?variant=poster"
		m.CoverPath = m.StashPosterURL
		m.CoverBackdropPath = m.StashScreenshotURL
	}
	return m
}

func (s *Service) LibrarySync(ctx context.Context) (LibrarySyncSnapshot, error) {
	settings, err := s.store.Settings(ctx)
	if err != nil {
		return LibrarySyncSnapshot{}, err
	}
	out := LibrarySyncSnapshot{Revision: settings["jellyfin_library_revision"], Watchlist: []LibrarySyncItem{}, Watched: []LibrarySyncItem{}}
	linkedByScene := make(map[string]domain.Release)
	for offset := 0; ; offset += 500 {
		rows, err := s.store.Releases(ctx, domain.ReleaseFilter{StashLinked: true, Limit: 500, Offset: offset})
		if err != nil {
			return LibrarySyncSnapshot{}, err
		}
		for _, r := range rows {
			linkedByScene[r.StashSceneID] = r
		}
		if len(rows) < 500 {
			break
		}
	}
	var stashWatchlist map[string]time.Time
	var configured bool
	var stashErr error
	if s.stash != nil {
		stashWatchlist, configured, stashErr = s.stash.SiloWatchlistScenes(ctx)
	}
	if configured && stashErr == nil {
		// Include Stash-only scenes too: no JAVBeacon release row is needed for
		// a local file or for Silo's Stash provider ID.
		sceneIDs := make([]string, 0, len(stashWatchlist))
		for sceneID := range stashWatchlist {
			sceneIDs = append(sceneIDs, sceneID)
		}
		sort.Strings(sceneIDs)
		hash := sha256.New()
		for _, sceneID := range sceneIDs {
			updated := stashWatchlist[sceneID]
			fmt.Fprintf(hash, "%s:%s\n", sceneID, updated.UTC().Format(time.RFC3339Nano))
			item := LibrarySyncItem{StashSceneID: sceneID, WatchlistedAt: updated}
			if r, ok := linkedByScene[sceneID]; ok {
				item.ReleaseID = r.ID
				item.Path = r.StashFilePath
			}
			out.Watchlist = append(out.Watchlist, item)
		}
		out.Revision += fmt.Sprintf("/stash-watchlist:%x", hash.Sum(nil))
	} else {
		// Stash is unavailable or no tag is configured. Keep the last
		// JAVBeacon mirror as a degraded fallback rather than dropping tags.
		for offset := 0; ; offset += 500 {
			rows, err := s.store.Releases(ctx, domain.ReleaseFilter{Watchlist: true, Limit: 500, Offset: offset})
			if err != nil {
				return LibrarySyncSnapshot{}, err
			}
			for _, r := range rows {
				if r.Local && r.StashSceneID != "" {
					out.Watchlist = append(out.Watchlist, LibrarySyncItem{ReleaseID: r.ID, StashSceneID: r.StashSceneID, Path: r.StashFilePath, WatchlistedAt: r.WatchlistAt})
				}
			}
			if len(rows) < 500 {
				break
			}
		}
	}
	sort.Slice(out.Watchlist, func(i, j int) bool {
		a, b := out.Watchlist[i], out.Watchlist[j]
		if !a.WatchlistedAt.Equal(b.WatchlistedAt) {
			return a.WatchlistedAt.After(b.WatchlistedAt)
		}
		return a.StashSceneID < b.StashSceneID
	})

	var stashWatched map[string]stash.SiloWatchedScene
	var watchedConfigured bool
	var watchedErr error
	if s.stash != nil {
		stashWatched, watchedConfigured, watchedErr = s.stash.SiloWatchedScenes(ctx)
	}
	if watchedConfigured && watchedErr == nil {
		sceneIDs := make([]string, 0, len(stashWatched))
		for id := range stashWatched {
			sceneIDs = append(sceneIDs, id)
		}
		sort.Strings(sceneIDs)
		for _, sceneID := range sceneIDs {
			state := stashWatched[sceneID]
			item := LibrarySyncItem{StashSceneID: sceneID, Title: state.Title, Path: state.Path, WatchedAt: state.LastPlayedAt, PlayCount: state.PlayCount}
			if r, ok := linkedByScene[sceneID]; ok {
				item.ReleaseID = r.ID
				item.Path = r.StashFilePath
			}
			out.Watched = append(out.Watched, item)
		}
	} else {
		// Preserve the mirrored release history if Stash is temporarily unavailable.
		for offset := 0; ; offset += 500 {
			rows, err := s.store.Releases(ctx, domain.ReleaseFilter{StashWatched: true, Limit: 500, Offset: offset})
			if err != nil {
				return LibrarySyncSnapshot{}, err
			}
			for _, r := range rows {
				watchedAt, _ := time.Parse(time.RFC3339, r.LastPlayedAt)
				out.Watched = append(out.Watched, LibrarySyncItem{ReleaseID: r.ID, StashSceneID: r.StashSceneID, Path: r.StashFilePath, WatchedAt: watchedAt, PlayCount: r.PlayCount})
			}
			if len(rows) < 500 {
				break
			}
		}
	}

	presetCollections, err := s.collectionPresets(ctx)
	if err != nil {
		return LibrarySyncSnapshot{}, err
	}
	out.FilterPresets = presetCollections
	seen := map[int64]bool{}
	ids := []int64{}
	for _, item := range out.Watchlist {
		if item.ReleaseID > 0 && !seen[item.ReleaseID] {
			ids = append(ids, item.ReleaseID)
			seen[item.ReleaseID] = true
		}
	}
	for _, preset := range presetCollections {
		for _, id := range preset.ReleaseIDs {
			if id > 0 && !seen[id] {
				ids = append(ids, id)
				seen[id] = true
			}
		}
	}
	if len(ids) > 0 {
		if bulk, ok := s.store.(interface {
			ReleaseCodes(context.Context, []int64) (map[int64]string, error)
		}); ok {
			out.ReleaseCodes, err = bulk.ReleaseCodes(ctx, ids)
			if err != nil {
				return LibrarySyncSnapshot{}, err
			}
		} else {
			out.ReleaseCodes = map[int64]string{}
			for _, id := range ids {
				r, err := s.store.Release(ctx, id)
				if err == nil {
					out.ReleaseCodes[id] = r.VideoID
				} else if !errors.Is(err, sql.ErrNoRows) {
					return LibrarySyncSnapshot{}, err
				}
			}
		}
	}
	return out, nil
}

func (s *Service) metadata(r domain.Release) Metadata {
	year := 0
	if len(r.ReleaseDate) >= 4 {
		year, _ = strconv.Atoi(r.ReleaseDate[:4])
	}
	directors := []string{}
	if strings.TrimSpace(r.Director) != "" {
		directors = append(directors, strings.TrimSpace(r.Director))
	}
	ids := map[string]string{ProviderIDRelease: strconv.FormatInt(r.ID, 10)}
	if r.StashSceneID != "" {
		ids[ProviderIDStash] = r.StashSceneID
	}
	backdrops := make([]string, 0, len(r.Screenshots))
	if s.shots != nil {
		for _, index := range s.shots.Available(r.VideoID, r.Screenshots) {
			backdrops = append(backdrops, fmt.Sprintf("/screenshots/%d/%d", r.ID, index))
		}
	}
	return Metadata{
		ReleaseID: r.ID, StashSceneID: r.StashSceneID, Code: r.VideoID, Title: r.VideoID,
		OriginalTitle: r.VideoID, Overview: releaseTitle(r.VideoID, r.Title), PremiereDate: r.ReleaseDate,
		ProductionYear: year, Studio: r.Studio, Label: r.Label, Performers: append([]string(nil), r.Actresses...),
		Directors: directors, Genres: append([]string(nil), r.Genres...), RuntimeSeconds: parseRuntime(r.Duration),
		CoverPath: fmt.Sprintf("/covers/%d/silo-primary", r.ID), CoverBackdropPath: fmt.Sprintf("/covers/%d/original", r.ID),
		BackdropURLs: backdrops, SourceURL: r.ProductURL, ProviderIDs: ids, Watchlist: r.Watchlist,
	}
}

// releaseTitle strips a leading release-code prefix from a title, careful
// not to cut a different word/number (direct duplicate of
// internal/jellyfin.releaseTitle).
func releaseTitle(releaseID, title string) string {
	title = strings.TrimSpace(title)
	releaseID = strings.TrimSpace(releaseID)
	if releaseID == "" || len(title) < len(releaseID) || !strings.EqualFold(title[:len(releaseID)], releaseID) {
		return title
	}
	if len(title) > len(releaseID) {
		next := rune(title[len(releaseID)])
		if (next >= 'A' && next <= 'Z') || (next >= 'a' && next <= 'z') || (next >= '0' && next <= '9') {
			return title
		}
	}
	return strings.TrimSpace(strings.TrimLeft(title[len(releaseID):], "-_:|–— \t"))
}

// PlaybackEvent/PlaybackResult/Playback are Silo's own independent twin of
// internal/jellyfin.Service's checkpoint/resume/completion-threshold engine
// - same arithmetic, deliberately duplicated rather than shared (see the
// package doc comment for why) so this integration never depends on
// internal/jellyfin.Service. Persistence is keyed by
// domain.SiloPlaybackSession/silo_playback_sessions, a separate table from
// Jellyfin's own session store.
type PlaybackEvent struct {
	Event     string `json:"event"`
	SessionID string `json:"session_id"`
	ReleaseID int64  `json:"release_id"`
	// StashSceneID lets a playback event be reported for a StashApp scene
	// JAVBeacon never scraped into a release row at all (no ReleaseID exists
	// yet). Either ReleaseID or StashSceneID is required; when both are given,
	// ReleaseID wins and is trusted to already be linked to that scene.
	StashSceneID    string    `json:"stash_scene_id,omitempty"`
	SiloItemID      string    `json:"silo_item_id"`
	SiloUserID      string    `json:"silo_user_id"`
	PositionSeconds float64   `json:"position_seconds"`
	RuntimeSeconds  float64   `json:"runtime_seconds"`
	IsPaused        bool      `json:"is_paused"`
	IsPlayed        bool      `json:"is_played"`
	OccurredAt      time.Time `json:"occurred_at,omitempty"`
}

type PlaybackResult struct {
	SessionID         string  `json:"session_id"`
	Accumulated       float64 `json:"accumulated_seconds"`
	Forwarded         float64 `json:"forwarded_seconds"`
	ResumeTime        float64 `json:"resume_time_seconds"`
	PlayCounted       bool    `json:"play_counted"`
	CheckpointWritten bool    `json:"checkpoint_written"`
}

type playbackPolicy struct{ checkpoint, maxGap, percent, remaining float64 }

// policy reads the same jellyfin_checkpoint_seconds/jellyfin_max_checkpoint_
// gap_seconds/jellyfin_completion_percent/jellyfin_completion_remaining_
// seconds settings internal/jellyfin.Service.policy reads. These are
// generic playback-completion thresholds, not integration-specific
// configuration or a call into internal/jellyfin.Service - there is only one
// admin-configured completion policy, and both integrations' playback
// engines apply it identically. The setting keys keep their pre-split names
// since no admin UI change was requested alongside this split.
func (s *Service) policy(ctx context.Context) playbackPolicy {
	p := playbackPolicy{checkpoint: 30, maxGap: 120, percent: 80, remaining: 600}
	settings, err := s.store.Settings(ctx)
	if err != nil {
		return p
	}
	for key, target := range map[string]*float64{"jellyfin_checkpoint_seconds": &p.checkpoint, "jellyfin_max_checkpoint_gap_seconds": &p.maxGap, "jellyfin_completion_percent": &p.percent, "jellyfin_completion_remaining_seconds": &p.remaining} {
		if n, e := strconv.ParseFloat(settings[key], 64); e == nil && n >= 0 {
			*target = n
		}
	}
	return p
}

func (s *Service) Playback(ctx context.Context, event PlaybackEvent) (PlaybackResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.repo == nil {
		return PlaybackResult{}, errors.New("Silo integration storage is unavailable")
	}
	event.Event = strings.ToLower(strings.TrimSpace(event.Event))
	if event.Event != "start" && event.Event != "progress" && event.Event != "stop" {
		return PlaybackResult{}, errors.New("event must be start, progress, or stop")
	}
	stashSceneID := strings.TrimSpace(event.StashSceneID)
	if strings.TrimSpace(event.SessionID) == "" || (event.ReleaseID < 1 && stashSceneID == "") {
		return PlaybackResult{}, errors.New("session_id and either release_id or stash_scene_id are required")
	}
	// releaseID stays 0 for a Stash-only scene JAVBeacon never scraped into a
	// release row - the session and every Stash write below are keyed by
	// stashSceneID alone in that case, bypassing store.Release entirely.
	releaseID := int64(0)
	if event.ReleaseID > 0 {
		r, err := s.store.Release(ctx, event.ReleaseID)
		if err != nil {
			return PlaybackResult{}, err
		}
		if r.StashSceneID == "" {
			return PlaybackResult{}, errors.New("release is not mapped to a StashApp scene")
		}
		releaseID, stashSceneID = r.ID, r.StashSceneID
	}
	now := event.OccurredAt.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	p := s.policy(ctx)
	x, err := s.repo.SiloPlaybackSession(ctx, event.SessionID)
	if errors.Is(err, sql.ErrNoRows) {
		x = domain.SiloPlaybackSession{SessionID: event.SessionID, ReleaseID: releaseID, StashSceneID: stashSceneID, SiloItemID: event.SiloItemID, SiloUserID: event.SiloUserID, StartedAt: now, LastEventAt: now, RuntimeSeconds: event.RuntimeSeconds, Status: "active"}
	} else if err != nil {
		return PlaybackResult{}, err
	} else if x.StashSceneID != stashSceneID {
		return PlaybackResult{}, errors.New("session_id is already bound to another release")
	} else if now.Before(x.LastEventAt) {
		// Playback event callbacks are forwarded asynchronously and may arrive
		// out of order. A stale callback must never move the durable clock or
		// resume position backwards, or cause a second external mutation.
		return playbackResult(x, false), nil
	} else if now.After(x.LastEventAt) && !x.WasPaused {
		delta := now.Sub(x.LastEventAt).Seconds()
		if delta > p.maxGap {
			delta = p.maxGap
		}
		if delta > 0 {
			x.Accumulated += delta
		}
	}
	if event.RuntimeSeconds > 0 {
		x.RuntimeSeconds = event.RuntimeSeconds
	}
	x.LastEventAt, x.LastPosition, x.WasPaused, x.UpdatedAt = now, math.Max(event.PositionSeconds, 0), event.IsPaused, time.Now().UTC()
	if event.Event == "stop" {
		x.Status = "stopped"
	} else {
		x.Status = "active"
	}
	if err = s.repo.SaveSiloPlaybackSession(ctx, x); err != nil {
		return PlaybackResult{}, err
	}
	pending := math.Max(x.Accumulated-x.Forwarded, 0)
	write := event.Event == "start" || event.Event == "stop" || pending >= p.checkpoint
	if write {
		if err = s.stash.SaveActivity(ctx, x.StashSceneID, x.LastPosition, pending); err != nil {
			return playbackResult(x, false), err
		}
		x.Forwarded = x.Accumulated
		// Persist the successful external write before attempting the separate
		// play-count mutation. If that second Stash call fails, the retry must
		// not add this duration delta a second time.
		if err = s.repo.SaveSiloPlaybackSession(ctx, x); err != nil {
			return PlaybackResult{}, err
		}
	}
	complete := event.IsPlayed
	if x.RuntimeSeconds > 0 {
		complete = complete || x.Accumulated/x.RuntimeSeconds*100 >= p.percent || (x.RuntimeSeconds > p.remaining && x.RuntimeSeconds-x.LastPosition <= p.remaining)
	}
	if complete && !x.PlayCounted {
		if _, err = s.stash.AddPlay(ctx, x.StashSceneID, now); err != nil {
			return playbackResult(x, write), err
		}
		x.PlayCounted = true
	}
	if x.PlayCounted {
		if err = s.repo.SaveSiloPlaybackSession(ctx, x); err != nil {
			return PlaybackResult{}, err
		}
	}
	return playbackResult(x, write), nil
}

func playbackResult(x domain.SiloPlaybackSession, written bool) PlaybackResult {
	return PlaybackResult{SessionID: x.SessionID, Accumulated: x.Accumulated, Forwarded: x.Forwarded, ResumeTime: x.LastPosition, PlayCounted: x.PlayCounted, CheckpointWritten: written}
}

// parseRuntime parses a duration string in whatever shape JAVBeacon scraped
// it in (direct duplicate of internal/jellyfin.parseRuntime).
func parseRuntime(raw string) int64 {
	v := strings.TrimSpace(strings.ToLower(raw))
	if v == "" {
		return 0
	}
	if d, err := time.ParseDuration(strings.ReplaceAll(v, "minutes", "m")); err == nil {
		return int64(d.Seconds())
	}
	parts := strings.Split(v, ":")
	if len(parts) == 2 || len(parts) == 3 {
		var seconds int64
		for _, part := range parts {
			n, err := strconv.ParseInt(strings.TrimSpace(part), 10, 64)
			if err != nil {
				return 0
			}
			seconds = seconds*60 + n
		}
		return seconds
	}
	n, _ := strconv.ParseFloat(strings.Fields(v)[0], 64)
	if strings.Contains(v, "hour") || strings.HasSuffix(v, "h") {
		return int64(n * 3600)
	}
	if strings.Contains(v, "min") {
		return int64(n * 60)
	}
	return int64(n * 60)
}

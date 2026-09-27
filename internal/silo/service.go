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
// Two pieces are deliberately still shared with internal/jellyfin, not
// duplicated here:
//
//   - Playback (internal/jellyfin.Service.Playback) - the checkpoint/resume/
//     completion-threshold engine and its StashApp play-count/O-count
//     writeback. It is keyed by domain.JellyfinPlaybackSession (a
//     Jellyfin-named persistence type predating this split) but is otherwise
//     already fully provider-agnostic - JellyfinItemID/JellyfinUserID are
//     optional labels, not required fields - and reimplementing ~150 lines of
//     delicate checkpoint/gap/completion arithmetic a second time would be
//     pure duplication risk for zero behavioral benefit. internal/web/silo.go
//     still calls s.jellyfin.Playback directly for this reason.
//   - The /covers/{id}/jellyfin-primary image-crop endpoint - a pure,
//     deterministic image transform (portrait pad/crop of a source cover)
//     with no business logic or per-integration behavior at all. Both
//     integrations' Metadata.CoverPath point at the same URL on purpose.
//
// The saved-filter-set ("collection") resolution logic - the one place a
// silent behavioral drift between the two integrations would be most visible
// to a user (a release in one integration's collection but not the other's)
// - is not duplicated either: both this package and internal/jellyfin call
// into internal/filterpreset for it.
package silo

import (
	"context"
	"fmt"
	"net/url"
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

// stashBridge is deliberately much narrower than internal/jellyfin's own
// stashBridge interface: this package never needs playback/O-count writeback
// (Playback stays on internal/jellyfin.Service, see the package doc comment)
// or performer bio lookups (Silo's GetPersonDetail always returns empty -
// confirmed against the plugin's own main.go), only the one Stash gap-fill
// lookup Metadata/Search's enrichment step uses.
type stashBridge interface {
	StashSceneMetadata(context.Context, string) (stash.StashSceneMetadata, error)
}

type Service struct {
	store store.Store
	stash stashBridge
	shots *screenshots.Cache

	// collMu/collRevision/collIndex/collPresets cache
	// collectionNamesForRelease's per-preset membership computation - see its
	// own doc comment for why. This cache is independent of
	// internal/jellyfin.Service's own (each service instance keeps its own
	// copy), which costs one extra revision-gated rebuild pass on the rare
	// occasion both happen to miss at once - a fine trade for not sharing
	// mutable state between two otherwise-independent services.
	collMu       sync.Mutex
	collRevision string
	collIndex    map[int64][]string
	collPresets  []FilterPresetCollection
}

func New(st store.Store, stashService *stash.Service, screenshotCaches ...*screenshots.Cache) *Service {
	return newService(st, stashService, screenshotCaches...)
}

func newService(st store.Store, stashService stashBridge, screenshotCaches ...*screenshots.Cache) *Service {
	service := &Service{store: st, stash: stashService}
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
	// CoverPath deliberately points at the same
	// /covers/{id}/jellyfin-primary endpoint the Jellyfin integration uses -
	// see the package doc comment for why that one endpoint stays shared.
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
	PerformerImages map[string]string `json:"performer_images,omitempty"`
	ProviderIDs     map[string]string `json:"provider_ids"`
}

type LibrarySyncItem struct {
	ReleaseID     int64     `json:"release_id"`
	StashSceneID  string    `json:"stash_scene_id"`
	Path          string    `json:"path,omitempty"`
	WatchlistedAt time.Time `json:"watchlisted_at,omitempty"`
	WatchedAt     time.Time `json:"watched_at,omitempty"`
}

type LibrarySyncSnapshot struct {
	Revision      string                   `json:"revision"`
	Watchlist     []LibrarySyncItem        `json:"watchlist"`
	Watched       []LibrarySyncItem        `json:"watched"`
	FilterPresets []FilterPresetCollection `json:"filter_presets"`
}

// FilterPresetCollection is one saved filter set resolved to its current,
// ordered membership.
type FilterPresetCollection struct {
	ID         int64   `json:"id"`
	Name       string  `json:"name"`
	ReleaseIDs []int64 `json:"release_ids"`
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
	rows, err := s.store.Releases(ctx, domain.ReleaseFilter{Search: strings.TrimSpace(query), Limit: limit})
	if err != nil {
		return nil, err
	}
	out := make([]Metadata, 0, len(rows))
	for _, r := range rows {
		out = append(out, s.metadata(r))
	}
	return out, nil
}

// stashLookupTimeout bounds every individual Stash round trip made while
// building Metadata/Search results, independent of whatever deadline (if
// any) the caller's own context carries - see
// internal/jellyfin.stashLookupTimeout's doc comment for the incident this
// guards against.
const stashLookupTimeout = 5 * time.Second

// collectionIndexTimeout bounds collectionMembershipIndex's rebuild pass.
const collectionIndexTimeout = 10 * time.Second

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

// collectionNamesForRelease resolves every saved filter set that currently
// matches releaseID. Best-effort: any storage error yields no names rather
// than failing the whole metadata request.
func (s *Service) collectionNamesForRelease(ctx context.Context, releaseID int64) []string {
	index, err := s.collectionMembershipIndex(ctx)
	if err != nil {
		return nil
	}
	return index[releaseID]
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

	s.collMu.Lock()
	defer s.collMu.Unlock()
	if s.collIndex != nil && s.collRevision == revision {
		return s.collIndex, s.collPresets, nil
	}

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
			continue
		}
		if tagName := filterpreset.SanitizeTagName(preset.Name); tagName != "" {
			for _, id := range ids {
				index[id] = append(index[id], tagName)
			}
		}
		collections = append(collections, FilterPresetCollection{ID: preset.ID, Name: preset.Name, ReleaseIDs: ids})
	}
	s.collRevision = revision
	s.collIndex = index
	s.collPresets = collections
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
	}
	return m
}

func (s *Service) LibrarySync(ctx context.Context) (LibrarySyncSnapshot, error) {
	settings, err := s.store.Settings(ctx)
	if err != nil {
		return LibrarySyncSnapshot{}, err
	}
	out := LibrarySyncSnapshot{Revision: settings["jellyfin_library_revision"], Watchlist: []LibrarySyncItem{}, Watched: []LibrarySyncItem{}}
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
	for offset := 0; ; offset += 500 {
		rows, err := s.store.Releases(ctx, domain.ReleaseFilter{StashWatched: true, Limit: 500, Offset: offset})
		if err != nil {
			return LibrarySyncSnapshot{}, err
		}
		for _, r := range rows {
			watchedAt, _ := time.Parse(time.RFC3339, r.LastPlayedAt)
			out.Watched = append(out.Watched, LibrarySyncItem{ReleaseID: r.ID, StashSceneID: r.StashSceneID, Path: r.StashFilePath, WatchedAt: watchedAt})
		}
		if len(rows) < 500 {
			break
		}
	}
	presetCollections, err := s.collectionPresets(ctx)
	if err != nil {
		return LibrarySyncSnapshot{}, err
	}
	out.FilterPresets = presetCollections
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
		CoverPath: fmt.Sprintf("/covers/%d/jellyfin-primary", r.ID), CoverBackdropPath: fmt.Sprintf("/covers/%d/original", r.ID),
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

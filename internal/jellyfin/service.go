package jellyfin

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Net005/JAVBeacon/internal/domain"
	"github.com/Net005/JAVBeacon/internal/screenshots"
	"github.com/Net005/JAVBeacon/internal/stash"
	"github.com/Net005/JAVBeacon/internal/store"
)

const (
	ProviderIDRelease = "JAVBeacon"
	ProviderIDStash   = "Stash"
)

type repository interface {
	JellyfinReleaseByPath(context.Context, string) (domain.Release, error)
	JellyfinPlaybackSession(context.Context, string) (domain.JellyfinPlaybackSession, error)
	SaveJellyfinPlaybackSession(context.Context, domain.JellyfinPlaybackSession) error
}

type stashBridge interface {
	SaveJellyfinActivity(context.Context, string, float64, float64) error
	AddJellyfinPlay(context.Context, string, time.Time) (int, error)
	AddJellyfinO(context.Context, string, time.Time) (int, error)
	JellyfinActivity(context.Context, string) (stash.JellyfinActivity, error)
	StashSceneMetadata(context.Context, string) (stash.StashSceneMetadata, error)
}

type Service struct {
	store store.Store
	repo  repository
	stash stashBridge
	shots *screenshots.Cache
	mu    sync.Mutex
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
	Tags           []string `json:"tags,omitempty"`
	RuntimeSeconds int64    `json:"runtime_seconds,omitempty"`
	// CoverPath is dedicated to Jellyfin's own Primary/Poster/Cover image
	// fetch (/covers/{id}/jellyfin-primary) - it slices/pads a JavLibrary
	// or GIGA two-panel spread cover down to Jellyfin's 1000x1500 shape,
	// entirely in memory, without ever touching JAVBeacon's own cached
	// cover file. JAVBeacon's own web UI never uses this path; it always
	// requests the plain /covers/{id} endpoint, which is never conformed.
	CoverPath string `json:"cover_path,omitempty"`
	// CoverBackdropPath points at the non-cropped, non-padded original
	// cover - a cropped poster makes a poor background, so Jellyfin's
	// Backdrop image should use this path instead of CoverPath.
	CoverBackdropPath string   `json:"cover_backdrop_path,omitempty"`
	BackdropURLs      []string `json:"backdrop_urls,omitempty"`
	// StashScreenshotURL is a JAVBeacon-proxied StashApp scene screenshot,
	// populated only when this release has no JAVBeacon-scraped cover of its
	// own (r.ImageURL is empty) - see enrichFromStash. Jellyfin's image
	// provider offers it alongside (never instead of) JAVBeacon's own cover/
	// backdrop candidates, so a release JAVBeacon never fully scraped but
	// that is linked to a StashApp scene still gets an image.
	StashScreenshotURL string            `json:"stash_screenshot_url,omitempty"`
	SourceURL          string            `json:"source_url,omitempty"`
	ProviderIDs        map[string]string `json:"provider_ids"`
}

type MatchResult struct {
	Matched     bool      `json:"matched"`
	MatchMethod string    `json:"match_method,omitempty"`
	Release     *Metadata `json:"release,omitempty"`
}

type LibrarySyncItem struct {
	ReleaseID     int64     `json:"release_id"`
	StashSceneID  string    `json:"stash_scene_id"`
	Path          string    `json:"path,omitempty"`
	WatchlistedAt time.Time `json:"watchlisted_at,omitempty"`
	// WatchedAt is only populated on LibrarySyncSnapshot.Watched entries: the
	// most recent time StashApp recorded this scene as played
	// (domain.Release.LastPlayedAt), used as Jellyfin's per-user
	// LastPlayedDate when the plugin's optional "sync watched status from
	// StashApp" setting marks the matching item played.
	WatchedAt time.Time `json:"watched_at,omitempty"`
}

type LibrarySyncSnapshot struct {
	Revision  string            `json:"revision"`
	Watchlist []LibrarySyncItem `json:"watchlist"`
	// Watched lists every local, StashApp-linked release StashApp reports as
	// played at least once (play_count>0) - independent of Watchlist, since
	// a release can be watched without ever having been on the Watchlist.
	// The Jellyfin plugin only acts on this when its own "sync watched
	// status from StashApp" setting is enabled; JAVBeacon always includes it
	// in the snapshot so enabling that setting later needs no backend change.
	Watched []LibrarySyncItem `json:"watched"`
	// FilterPresets mirrors every saved filter set from the Release Library
	// (see domain.FilterPreset), each resolved to the exact, already-sorted
	// list of local/Stash-linked release IDs it currently matches - using
	// the very same filter+sort engine (store.Releases) the web UI itself
	// uses, so a Jellyfin collection built from this list always matches
	// what the Release Library would show for that saved filter set. The
	// Jellyfin plugin creates/updates one collection per entry and removes
	// collections for presets that no longer exist.
	FilterPresets []FilterPresetCollection `json:"filter_presets"`
}

// FilterPresetCollection is one saved filter set resolved to its current,
// ordered membership for the Jellyfin plugin's collection sync.
type FilterPresetCollection struct {
	ID         int64   `json:"id"`
	Name       string  `json:"name"`
	ReleaseIDs []int64 `json:"release_ids"`
}

var releaseCode = regexp.MustCompile(`(?i)[a-z]{2,}(?:[-_ ]?\d){2,7}`)

func canonical(raw string) string {
	raw = strings.ToUpper(raw)
	letters := regexp.MustCompile(`[^A-Z]`).ReplaceAllString(raw, "")
	numbers := strings.TrimLeft(regexp.MustCompile(`[^0-9]`).ReplaceAllString(raw, ""), "0")
	if numbers == "" && strings.ContainsAny(raw, "0123456789") {
		numbers = "0"
	}
	if letters == "" || numbers == "" {
		return ""
	}
	return letters + numbers
}

func (s *Service) Match(ctx context.Context, path, query string) (MatchResult, error) {
	if s.repo == nil {
		return MatchResult{}, errors.New("Jellyfin integration storage is unavailable")
	}
	path = strings.TrimSpace(path)
	if path != "" {
		for _, candidate := range s.matchPaths(ctx, path) {
			r, err := s.repo.JellyfinReleaseByPath(ctx, candidate)
			if err == nil {
				m := s.enrichFromStash(ctx, r, s.metadata(r))
				method := "path"
				if candidate != path {
					method = "remapped_path"
				}
				return MatchResult{Matched: true, MatchMethod: method, Release: &m}, nil
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return MatchResult{}, err
			}
		}
	}
	needle := strings.TrimSpace(query)
	if needle == "" && path != "" {
		needle = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
		if match := releaseCode.FindString(needle); match != "" {
			needle = match
		}
	}
	want := canonical(needle)
	if want == "" {
		return MatchResult{Matched: false}, nil
	}
	rows, err := s.store.Releases(ctx, domain.ReleaseFilter{Search: needle, Limit: 100})
	if err != nil {
		return MatchResult{}, err
	}
	for _, r := range rows {
		if canonical(r.VideoID) == want {
			m := s.enrichFromStash(ctx, r, s.metadata(r))
			return MatchResult{Matched: true, MatchMethod: "release_code", Release: &m}, nil
		}
	}
	return MatchResult{Matched: false}, nil
}

type pathRemap struct {
	From string `json:"from"`
	To   string `json:"to"`
}

func (s *Service) matchPaths(ctx context.Context, path string) []string {
	paths := []string{path}
	settings, err := s.store.Settings(ctx)
	if err != nil {
		return paths
	}
	var remaps []pathRemap
	if json.Unmarshal([]byte(settings["jellyfin_path_remaps"]), &remaps) != nil {
		return paths
	}
	normalized := strings.ReplaceAll(path, `\`, "/")
	for _, remap := range remaps {
		from := strings.TrimRight(strings.ReplaceAll(strings.TrimSpace(remap.From), `\`, "/"), "/")
		to := strings.TrimRight(strings.ReplaceAll(strings.TrimSpace(remap.To), `\`, "/"), "/")
		if from == "" || to == "" || (!strings.EqualFold(normalized, from) && !strings.HasPrefix(strings.ToLower(normalized), strings.ToLower(from)+"/")) {
			continue
		}
		candidate := to + normalized[len(from):]
		if !slicesContainsFold(paths, candidate) {
			paths = append(paths, candidate)
		}
	}
	return paths
}

func slicesContainsFold(values []string, want string) bool {
	for _, value := range values {
		if strings.EqualFold(value, want) {
			return true
		}
	}
	return false
}

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
		out = append(out, s.enrichFromStash(ctx, r, s.metadata(r)))
	}
	return out, nil
}

func (s *Service) Metadata(ctx context.Context, releaseID int64) (Metadata, error) {
	r, err := s.store.Release(ctx, releaseID)
	if err != nil {
		return Metadata{}, err
	}
	return s.enrichFromStash(ctx, r, s.metadata(r)), nil
}

// enrichFromStash fills gaps in JAVBeacon's own scraped metadata directly
// from the linked StashApp scene, so a release JAVBeacon only partially
// scraped (or never finished scraping) still shows complete information in
// Jellyfin. It only ever fills a field that is currently EMPTY - StashApp
// data never overrides anything JAVBeacon itself already has - and it is
// best-effort: a StashApp lookup failure (unreachable server, scene since
// deleted, etc.) leaves m unchanged rather than failing the whole request.
func (s *Service) enrichFromStash(ctx context.Context, r domain.Release, m Metadata) Metadata {
	if s.stash == nil || r.StashSceneID == "" {
		return m
	}
	missingText := r.Title == "" || r.Studio == "" || len(r.Actresses) == 0 || len(r.Genres) == 0
	missingImage := r.ImageURL == ""
	if !missingText && !missingImage {
		return m
	}
	scene, err := s.stash.StashSceneMetadata(ctx, r.StashSceneID)
	if err != nil {
		return m
	}
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
		m.Performers = scene.Performers
	}
	if len(r.Genres) == 0 && len(scene.Tags) > 0 {
		m.Genres = scene.Tags
		m.Tags = append(append([]string(nil), scene.Tags...), m.Tags...)
	}
	if missingImage && scene.ScreenshotURL != "" {
		m.StashScreenshotURL = fmt.Sprintf("/api/v1/integrations/jellyfin/releases/%d/stash-cover", r.ID)
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
	presets, err := s.store.FilterPresets(ctx)
	if err != nil {
		return LibrarySyncSnapshot{}, err
	}
	for _, preset := range presets {
		filter, ok := filterFromPresetState(preset.State, settings)
		if !ok {
			continue
		}
		ids, err := s.resolveFilterReleaseIDs(ctx, filter)
		if err != nil {
			// A single malformed/legacy saved filter must never break sync for
			// every other collection - skip it and keep going.
			continue
		}
		out.FilterPresets = append(out.FilterPresets, FilterPresetCollection{ID: preset.ID, Name: preset.Name, ReleaseIDs: ids})
	}
	return out, nil
}

// resolveFilterReleaseIDs runs filter through the exact same store.Releases
// engine the Release Library itself uses, paging through every match and
// keeping only releases Jellyfin could actually have (local, Stash-linked),
// in the order store.Releases returns them (i.e. filter.Sort/Direction).
func (s *Service) resolveFilterReleaseIDs(ctx context.Context, filter domain.ReleaseFilter) ([]int64, error) {
	ids := []int64{}
	filter.Limit = 500
	for offset := 0; ; offset += 500 {
		filter.Offset = offset
		rows, err := s.store.Releases(ctx, filter)
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			if r.Local && r.StashSceneID != "" {
				ids = append(ids, r.ID)
			}
		}
		if len(rows) < 500 {
			break
		}
	}
	return ids, nil
}

// filterPresetState mirrors the shape the web UI saves as a FilterPreset's
// State (see app.js currentReleaseFilterState) - only the fields relevant to
// selecting/ordering releases are decoded; unknown fields are ignored.
type filterPresetState struct {
	ActiveTab        string          `json:"activeTab"`
	Category         string          `json:"category"`
	Entries          json.RawMessage `json:"entries"`
	Search           string          `json:"search"`
	WildcardLogic    string          `json:"wildcardLogic"`
	SearchExpression json.RawMessage `json:"searchExpression"`
	SortField        string          `json:"sortField"`
	SortDirection    string          `json:"sortDirection"`
	HideLocal        bool            `json:"hideLocal"`
	HideMonitored    bool            `json:"hideMonitored"`
	ShowNonPreferred bool            `json:"showNonPreferred"`
	Watchlist        bool            `json:"watchlist"`
	ReleasedMinDays  string          `json:"releasedMinDays"`
	ReleasedMaxDays  string          `json:"releasedMaxDays"`
	UpcomingMaxDays  string          `json:"upcomingMaxDays"`
}

// filterFromPresetState reproduces app.js's releaseQuery()/releaseFilterFromQuery
// pairing in Go, so a saved filter set resolves to precisely the same
// domain.ReleaseFilter the Release Library itself would send for it - same
// search, category/entries, structured conditions, wildcard logic, sort, and
// (for the Released/Upcoming tabs) the same "days ago/days from now" window,
// anchored on today since the UI never persists an explicit start date.
func filterFromPresetState(raw json.RawMessage, settings map[string]string) (domain.ReleaseFilter, bool) {
	var state filterPresetState
	if len(raw) == 0 || json.Unmarshal(raw, &state) != nil {
		return domain.ReleaseFilter{}, false
	}
	f := domain.ReleaseFilter{
		Search:           state.Search,
		SearchWildcards:  true,
		Status:           state.ActiveTab,
		Category:         state.Category,
		WildcardLogic:    state.WildcardLogic,
		Watchlist:        state.Watchlist,
		HideLocal:        state.HideLocal,
		HideMonitored:    state.HideMonitored,
		ShowNonPreferred: state.ShowNonPreferred,
		Sort:             state.SortField,
		Direction:        state.SortDirection,
	}
	if len(state.Entries) > 0 && string(state.Entries) != "null" {
		f.Entries = string(state.Entries)
	}
	if len(state.SearchExpression) > 0 && string(state.SearchExpression) != "null" {
		f.SearchExpression = string(state.SearchExpression)
	}
	if !f.ShowNonPreferred {
		f.IgnoreTags = domain.ParseIgnoreList(settings["ignore_tags"])
		f.IgnoreTitles = domain.ParseIgnoreList(settings["ignore_titles"])
		f.UsePreferred = len(f.IgnoreTags) > 0 || len(f.IgnoreTitles) > 0
	}
	today := time.Now().UTC()
	switch state.ActiveTab {
	case "released":
		if minDays, err := strconv.Atoi(strings.TrimSpace(state.ReleasedMinDays)); err == nil {
			f.MaxReleaseDate = today.AddDate(0, 0, -minDays).Format("2006-01-02")
		}
		if maxDays, err := strconv.Atoi(strings.TrimSpace(state.ReleasedMaxDays)); err == nil {
			f.MinReleaseDate = today.AddDate(0, 0, -maxDays).Format("2006-01-02")
		}
	case "upcoming":
		if maxDays, err := strconv.Atoi(strings.TrimSpace(state.UpcomingMaxDays)); err == nil {
			f.MaxReleaseDate = today.AddDate(0, 0, maxDays).Format("2006-01-02")
		}
	}
	return f, true
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
	tags := append([]string(nil), r.Genres...)
	if r.Label != "" {
		tags = append(tags, r.Label)
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
	return Metadata{ReleaseID: r.ID, StashSceneID: r.StashSceneID, Code: r.VideoID, Title: r.VideoID, OriginalTitle: r.VideoID, Overview: releaseTitle(r.VideoID, r.Title), PremiereDate: r.ReleaseDate, ProductionYear: year, Studio: r.Studio, Label: r.Label, Performers: append([]string(nil), r.Actresses...), Directors: directors, Genres: append([]string(nil), r.Genres...), Tags: tags, RuntimeSeconds: parseRuntime(r.Duration), CoverPath: fmt.Sprintf("/covers/%d/jellyfin-primary", r.ID), CoverBackdropPath: fmt.Sprintf("/covers/%d/original", r.ID), BackdropURLs: backdrops, SourceURL: r.ProductURL, ProviderIDs: ids}
}

func releaseTitle(releaseID, title string) string {
	title = strings.TrimSpace(title)
	releaseID = strings.TrimSpace(releaseID)
	if releaseID == "" || len(title) < len(releaseID) || !strings.EqualFold(title[:len(releaseID)], releaseID) {
		return title
	}
	// Only remove a complete leading release ID, never an ID-like prefix of a
	// different word or number (for example ABC-12 from ABC-123 title).
	if len(title) > len(releaseID) {
		next := rune(title[len(releaseID)])
		if (next >= 'A' && next <= 'Z') || (next >= 'a' && next <= 'z') || (next >= '0' && next <= '9') {
			return title
		}
	}
	return strings.TrimSpace(strings.TrimLeft(title[len(releaseID):], "-_:|–— \t"))
}

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

type PlaybackEvent struct {
	Event           string    `json:"event"`
	SessionID       string    `json:"session_id"`
	ReleaseID       int64     `json:"release_id"`
	JellyfinItemID  string    `json:"jellyfin_item_id"`
	JellyfinUserID  string    `json:"jellyfin_user_id"`
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
		return PlaybackResult{}, errors.New("Jellyfin integration storage is unavailable")
	}
	event.Event = strings.ToLower(strings.TrimSpace(event.Event))
	if event.Event != "start" && event.Event != "progress" && event.Event != "stop" {
		return PlaybackResult{}, errors.New("event must be start, progress, or stop")
	}
	if strings.TrimSpace(event.SessionID) == "" || event.ReleaseID < 1 {
		return PlaybackResult{}, errors.New("session_id and release_id are required")
	}
	r, err := s.store.Release(ctx, event.ReleaseID)
	if err != nil {
		return PlaybackResult{}, err
	}
	if r.StashSceneID == "" {
		return PlaybackResult{}, errors.New("release is not mapped to a StashApp scene")
	}
	now := event.OccurredAt.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	p := s.policy(ctx)
	x, err := s.repo.JellyfinPlaybackSession(ctx, event.SessionID)
	if errors.Is(err, sql.ErrNoRows) {
		x = domain.JellyfinPlaybackSession{SessionID: event.SessionID, ReleaseID: r.ID, StashSceneID: r.StashSceneID, JellyfinItemID: event.JellyfinItemID, JellyfinUserID: event.JellyfinUserID, StartedAt: now, LastEventAt: now, RuntimeSeconds: event.RuntimeSeconds, Status: "active"}
	} else if err != nil {
		return PlaybackResult{}, err
	} else if x.ReleaseID != event.ReleaseID {
		return PlaybackResult{}, errors.New("session_id is already bound to another release")
	} else if now.Before(x.LastEventAt) {
		// Jellyfin event callbacks are forwarded asynchronously and may arrive
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
	if err = s.repo.SaveJellyfinPlaybackSession(ctx, x); err != nil {
		return PlaybackResult{}, err
	}
	pending := math.Max(x.Accumulated-x.Forwarded, 0)
	write := event.Event == "start" || event.Event == "stop" || pending >= p.checkpoint
	if write {
		if err = s.stash.SaveJellyfinActivity(ctx, x.StashSceneID, x.LastPosition, pending); err != nil {
			return playbackResult(x, false), err
		}
		x.Forwarded = x.Accumulated
		// Persist the successful external write before attempting the separate
		// play-count mutation. If that second Stash call fails, the retry must
		// not add this duration delta a second time.
		if err = s.repo.SaveJellyfinPlaybackSession(ctx, x); err != nil {
			return PlaybackResult{}, err
		}
	}
	complete := event.IsPlayed
	if x.RuntimeSeconds > 0 {
		complete = complete || x.Accumulated/x.RuntimeSeconds*100 >= p.percent || (x.RuntimeSeconds > p.remaining && x.RuntimeSeconds-x.LastPosition <= p.remaining)
	}
	if complete && !x.PlayCounted {
		if _, err = s.stash.AddJellyfinPlay(ctx, x.StashSceneID, now); err != nil {
			return playbackResult(x, write), err
		}
		x.PlayCounted = true
	}
	if x.PlayCounted {
		if err = s.repo.SaveJellyfinPlaybackSession(ctx, x); err != nil {
			return PlaybackResult{}, err
		}
	}
	return playbackResult(x, write), nil
}

func playbackResult(x domain.JellyfinPlaybackSession, written bool) PlaybackResult {
	return PlaybackResult{SessionID: x.SessionID, Accumulated: x.Accumulated, Forwarded: x.Forwarded, ResumeTime: x.LastPosition, PlayCounted: x.PlayCounted, CheckpointWritten: written}
}

func (s *Service) Activity(ctx context.Context, releaseID int64) (stash.JellyfinActivity, error) {
	r, err := s.store.Release(ctx, releaseID)
	if err != nil {
		return stash.JellyfinActivity{}, err
	}
	if r.StashSceneID == "" {
		return stash.JellyfinActivity{}, errors.New("release is not mapped to a StashApp scene")
	}
	return s.stash.JellyfinActivity(ctx, r.StashSceneID)
}

func (s *Service) AddO(ctx context.Context, releaseID int64, at time.Time) (stash.JellyfinActivity, error) {
	r, err := s.store.Release(ctx, releaseID)
	if err != nil {
		return stash.JellyfinActivity{}, err
	}
	if r.StashSceneID == "" {
		return stash.JellyfinActivity{}, errors.New("release is not mapped to a StashApp scene")
	}
	if _, err = s.stash.AddJellyfinO(ctx, r.StashSceneID, at); err != nil {
		return stash.JellyfinActivity{}, err
	}
	return s.stash.JellyfinActivity(ctx, r.StashSceneID)
}

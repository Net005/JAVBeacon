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
	ReleaseID      int64             `json:"release_id"`
	StashSceneID   string            `json:"stash_scene_id,omitempty"`
	Code           string            `json:"code"`
	Title          string            `json:"title"`
	OriginalTitle  string            `json:"original_title,omitempty"`
	Overview       string            `json:"overview,omitempty"`
	PremiereDate   string            `json:"premiere_date,omitempty"`
	ProductionYear int               `json:"production_year,omitempty"`
	Studio         string            `json:"studio,omitempty"`
	Label          string            `json:"label,omitempty"`
	Performers     []string          `json:"performers,omitempty"`
	Directors      []string          `json:"directors,omitempty"`
	Genres         []string          `json:"genres,omitempty"`
	Tags           []string          `json:"tags,omitempty"`
	RuntimeSeconds int64             `json:"runtime_seconds,omitempty"`
	CoverPath      string            `json:"cover_path,omitempty"`
	BackdropURLs   []string          `json:"backdrop_urls,omitempty"`
	SourceURL      string            `json:"source_url,omitempty"`
	ProviderIDs    map[string]string `json:"provider_ids"`
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
}

type LibrarySyncSnapshot struct {
	Revision  string            `json:"revision"`
	Watchlist []LibrarySyncItem `json:"watchlist"`
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
				m := s.metadata(r)
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
			m := s.metadata(r)
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
		out = append(out, s.metadata(r))
	}
	return out, nil
}

func (s *Service) Metadata(ctx context.Context, releaseID int64) (Metadata, error) {
	r, err := s.store.Release(ctx, releaseID)
	if err != nil {
		return Metadata{}, err
	}
	return s.metadata(r), nil
}

func (s *Service) LibrarySync(ctx context.Context) (LibrarySyncSnapshot, error) {
	settings, err := s.store.Settings(ctx)
	if err != nil {
		return LibrarySyncSnapshot{}, err
	}
	out := LibrarySyncSnapshot{Revision: settings["jellyfin_library_revision"], Watchlist: []LibrarySyncItem{}}
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
	return Metadata{ReleaseID: r.ID, StashSceneID: r.StashSceneID, Code: r.VideoID, Title: r.VideoID, OriginalTitle: r.VideoID, Overview: releaseTitle(r.VideoID, r.Title), PremiereDate: r.ReleaseDate, ProductionYear: year, Studio: r.Studio, Label: r.Label, Performers: append([]string(nil), r.Actresses...), Directors: directors, Genres: append([]string(nil), r.Genres...), Tags: tags, RuntimeSeconds: parseRuntime(r.Duration), CoverPath: fmt.Sprintf("/covers/%d", r.ID), BackdropURLs: backdrops, SourceURL: r.ProductURL, ProviderIDs: ids}
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

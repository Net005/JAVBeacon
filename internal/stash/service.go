package stash

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/Net005/JAVBeacon/internal/domain"
	"github.com/Net005/JAVBeacon/internal/download"
	"github.com/Net005/JAVBeacon/internal/scraper"
	"github.com/Net005/JAVBeacon/internal/store"
)

const DefaultQuery = `query JAVBeaconLocalScenes { findScenes(filter: { per_page: -1 }) { scenes { id title code date tags { id } } } }`

// playbackStatsQueryWithHistory and playbackStatsQueryBasic back the
// Release Library Conditions dialog's "O Count"/"Last O Count Date"/"Last
// Played" fields (TODO-2.0 task 38): a separate, optional GraphQL query run
// after the main DefaultQuery/custom-query local-state sync, keyed directly
// by StashSceneID (already known for every locally-matched release) rather
// than the canonical title/code matching fetch() uses, since these releases
// are already matched.
//
// This is deliberately its own query rather than added fields on
// DefaultQuery/the user's custom stash_graphql_query: DefaultQuery is what
// the safety-critical local-availability matching in run() depends on, and
// a GraphQL schema validation error (a whole request fails if it asks for
// any field the server's schema doesn't have) from a field a given StashApp
// version lacks must not be able to take that down. o_history in particular
// is newer than o_counter/play_count/last_played_at, hence the two-tier
// fallback in fetchPlaybackStats: try the full query first, and if the
// whole request errors, retry without o_history so the O-Counter/Play
// Count/Last Played figures still sync even when Last O Count Date can't.
const playbackStatsQueryWithHistory = `query JAVBeaconPlaybackStats { findScenes(filter: { per_page: -1 }) { scenes { id o_counter play_count last_played_at o_history } } }`
const playbackStatsQueryBasic = `query JAVBeaconPlaybackStats { findScenes(filter: { per_page: -1 }) { scenes { id o_counter play_count last_played_at } } }`
const sceneCreatedAtQuery = `query JAVBeaconSceneCreatedAt { findScenes(filter: { per_page: -1 }) { scenes { id created_at } } }`

// playbackStats is one scene's parsed O-Counter/Play Count/Last Played/Last
// O Count Date figures, keyed by StashApp scene ID in fetchPlaybackStats's
// result. LastOCountAt is "" when it could not be determined this round
// (the basic-tier query was used, or the scene's o_history was empty) - see
// SetStashPlaybackStats's doc comment for how run() treats that.
type playbackStats struct {
	OCounter     int
	PlayCount    int
	LastPlayedAt string
	LastOCountAt string
}

var idInText = regexp.MustCompile(`(?i)[a-z]{2,}[\s_-]*0*[0-9]{2,7}`)

type Status struct {
	Running     bool      `json:"running"`
	StartedAt   time.Time `json:"started_at,omitempty"`
	FinishedAt  time.Time `json:"finished_at,omitempty"`
	Phase       string    `json:"phase,omitempty"`
	Scenes      int       `json:"scenes"`
	Total       int       `json:"total"`
	Processed   int       `json:"processed"`
	Matched     int       `json:"matched"`
	Updated     int       `json:"updated"`
	CurrentItem string    `json:"current_item,omitempty"`
	Error       string    `json:"error,omitempty"`
}

type DesiredStatus struct {
	Checked int    `json:"checked"`
	Updated int    `json:"updated"`
	Skipped int    `json:"skipped"`
	Error   string `json:"error,omitempty"`
}

type Service struct {
	store     store.Store
	client    *http.Client
	log       *slog.Logger
	mu        sync.RWMutex
	desiredMu sync.Mutex
	status    Status

	// jav and downloads back the TODO-2.0 Phase 2 "Missing Library Files"
	// recovery flow (missing.go): jav scrapes a matched-but-unretrieved
	// scene's JavLibrary URL into a full release, and downloads drives the
	// "Monitor + Download + search" bulk action. Both may be nil in tests
	// that only exercise the pre-existing local/desired sync behavior above.
	jav       *scraper.JavLibrary
	downloads *download.Service

	missingMu     sync.RWMutex
	missingStatus MissingStatus

	retrieveMu     sync.RWMutex
	retrieveStatus RetrieveStatus

	applyMu     sync.RWMutex
	applyStatus ApplyStatus

	// scheduleNextAttempt tracks, per schedule loop below ("sync",
	// "desired_sync"), the wall-clock time that loop will next actually
	// check whether it's due to run - kept live (updated every loop
	// iteration, not just when the schedule fires) so ScheduleForecast can
	// report an accurate "next run" without re-deriving the loop's own
	// timing separately. Guarded by mu like the rest of this struct's
	// mutable fields.
	scheduleNextAttempt map[string]time.Time
}

// scheduleMaxSleepChunk bounds how long Schedule/DesiredSchedule ever sleep
// in one time.NewTimer wait, so a settings change (interval edited, or a
// schedule enabled/disabled) is picked up within this long at worst instead
// of only after whatever stale interval the loop last computed - mirrors
// monitor.scheduleMaxSleepChunk and download.scheduleMaxSleepChunk, which
// document the same "otherwise a changed schedule is indistinguishable from
// requires a restart" problem.
var scheduleMaxSleepChunk = 30 * time.Second

func New(st store.Store, timeout time.Duration, log *slog.Logger, jav *scraper.JavLibrary, downloads *download.Service) *Service {
	svc := &Service{store: st, client: &http.Client{Timeout: timeout}, log: log, jav: jav, downloads: downloads, scheduleNextAttempt: map[string]time.Time{}}
	if st != nil {
		svc.restoreMissingScanStatus(context.Background())
	}
	return svc
}

func (s *Service) Status() Status { s.mu.RLock(); defer s.mu.RUnlock(); return s.status }

func (s *Service) publishStatus(status Status) {
	s.mu.Lock()
	s.status = status
	s.mu.Unlock()
}

func (s *Service) Start(ctx context.Context) error {
	s.mu.Lock()
	if s.status.Running {
		s.mu.Unlock()
		return errors.New("Stash sync already running")
	}
	s.status = Status{Running: true, StartedAt: time.Now().UTC(), Phase: "Preparing"}
	s.mu.Unlock()
	go s.run(context.WithoutCancel(ctx))
	return nil
}

func (s *Service) run(ctx context.Context) {
	result := s.Status()
	if result.StartedAt.IsZero() {
		result.Running = true
		result.StartedAt = time.Now().UTC()
	}
	defer func() {
		result.Running = false
		result.FinishedAt = time.Now().UTC()
		result.CurrentItem = ""
		if result.Error == "" {
			result.Phase = "Complete"
		} else {
			result.Phase = "Failed"
		}
		s.publishStatus(result)
		fields := []any{"scenes", result.Scenes, "total", result.Total, "processed", result.Processed, "matched", result.Matched, "updated", result.Updated, "duration", result.FinishedAt.Sub(result.StartedAt).Round(time.Millisecond)}
		if result.Error != "" {
			s.log.Error("Stash local release sync finished with an error", append(fields, "error", result.Error)...)
		} else {
			s.log.Info("Stash local release sync completed", fields...)
		}
	}()
	result.Phase = "Loading settings"
	s.publishStatus(result)
	settings, err := s.store.Settings(ctx)
	if err != nil {
		result.Error = err.Error()
		return
	}
	baseURL, query := strings.TrimRight(strings.TrimSpace(settings["stash_base_url"]), "/"), strings.TrimSpace(settings["stash_graphql_query"])
	url := baseURL + "/graphql"
	apiKey := strings.TrimSpace(settings["stash_api_key"])
	if baseURL == "" {
		result.Error = "StashApp Base URL is not configured"
		return
	}
	if query == "" {
		query = DefaultQuery
	}
	s.log.Info("Stash local release sync started", "url", url)
	result.Phase = "Fetching StashApp scenes"
	s.publishStatus(result)
	ids, dates, scenes, err := s.fetch(ctx, url, query, apiKey)
	result.Scenes = scenes
	if err != nil {
		result.Error = err.Error()
		return
	}
	result.Phase = "Fetching StashApp details"
	s.publishStatus(result)
	createdAtByScene, createdAtErr := s.fetchSceneCreatedAt(ctx, url, apiKey)
	if createdAtErr != nil {
		result.Error = "fetch StashApp scene creation dates: " + createdAtErr.Error()
		return
	}
	// Playback stats are a separate, best-effort pass (see the doc comment
	// on playbackStatsQueryWithHistory): a failure here - an older StashApp
	// without o_counter/play_count/last_played_at at all, a network hiccup
	// on this second request - is logged but never turns into result.Error,
	// since it must not make an otherwise-successful local-availability sync
	// report as failed.
	stats, statsErr := s.fetchPlaybackStats(ctx, url, apiKey)
	if statsErr != nil {
		s.log.Warn("Stash playback-stats sync skipped", "url", url, "error", statsErr)
		stats = nil
	}
	total, err := s.store.ReleasesCount(ctx, domain.ReleaseFilter{})
	if err != nil {
		result.Error = err.Error()
		return
	}
	result.Total = total
	result.Phase = "Matching releases"
	s.publishStatus(result)
	s.log.Info("Stash local release sync matching started", "scenes", scenes, "identifiers", len(ids), "releases", total)
	advanceProgress := func() {
		result.Processed++
		if result.Processed%25 == 0 || result.Processed == result.Total {
			s.publishStatus(result)
		}
		if result.Processed%500 == 0 || result.Processed == result.Total {
			s.log.Info("Stash local release sync progress", "processed", result.Processed, "total", result.Total, "matched", result.Matched, "updated", result.Updated, "current_item", result.CurrentItem)
		}
	}
	for offset := 0; ; offset += 500 {
		releases, e := s.store.Releases(ctx, domain.ReleaseFilter{Limit: 500, Offset: offset})
		if e != nil {
			result.Error = e.Error()
			return
		}
		for _, release := range releases {
			result.CurrentItem = release.VideoID
			releaseUpdated := false
			key := canonical(release.VideoID)
			sceneID, local := ids[key]
			if local {
				result.Matched++
			}
			if local != release.Local || sceneID != release.StashSceneID {
				if e := s.store.SetStashState(ctx, release.ID, local, sceneID); e != nil {
					result.Error = e.Error()
					advanceProgress()
					continue
				}
				releaseUpdated = true
				s.log.Debug("release local state changed from Stash", "video_id", release.VideoID, "local", local)
			}
			// TODO-2.0's "Missing released status display": best-effort, so a
			// scene lookup miss or a custom query lacking `date` just means
			// nothing to store here - it never clears a previously stored
			// value (see SetStashReleaseDate).
			if date := dates[key]; date != "" && date != release.StashReleaseDate {
				if e := s.store.SetStashReleaseDate(ctx, release.ID, date); e != nil {
					result.Error = e.Error()
				} else {
					releaseUpdated = true
				}
			}
			// Playback stats use the scene ID found in this sync, not the ID
			// from the release snapshot loaded before SetStashState above. On a
			// release's first match that snapshot is still blank; using it made
			// O Count, Play Count, Last Played, and Last O Count remain empty
			// until a second sync. Only current local matches are eligible, so a
			// scene removed from Stash cannot refresh stale playback values.
			if local && sceneID != "" {
				// created_at is a required part of a local Stash match and is
				// fetched independently from optional playback statistics. This
				// guarantees the Local tab's Added Locally sort is backed by the
				// same timestamp as StashApp's sortby=created_at.
				if rawCreatedAt := createdAtByScene[sceneID]; rawCreatedAt != "" {
					if createdAt, e := time.Parse(time.RFC3339, rawCreatedAt); e != nil {
						result.Error = fmt.Sprintf("parse StashApp created_at for scene %s: %v", sceneID, e)
					} else if !createdAt.Equal(release.StashCreatedAt) {
						if e := s.store.SetStashCreatedAt(ctx, release.ID, createdAt); e != nil {
							result.Error = e.Error()
						} else {
							releaseUpdated = true
						}
					}
				} else {
					result.Error = fmt.Sprintf("StashApp scene %s did not return created_at", sceneID)
				}
			}
			if stats != nil && local && sceneID != "" {
				if st, ok := stats[sceneID]; ok {
					lastOCountAt := st.LastOCountAt
					if lastOCountAt == "" {
						// This round's query couldn't determine it (basic-tier
						// fallback, or an empty o_history) - keep whatever was
						// already recorded rather than blanking it out.
						lastOCountAt = release.LastOCountAt
					}
					if st.OCounter != release.OCounter || st.PlayCount != release.PlayCount || st.LastPlayedAt != release.LastPlayedAt || lastOCountAt != release.LastOCountAt {
						if e := s.store.SetStashPlaybackStats(ctx, release.ID, st.OCounter, st.PlayCount, st.LastPlayedAt, lastOCountAt); e != nil {
							s.log.Error("Stash playback-stats update failed", "video_id", release.VideoID, "error", e)
						} else {
							releaseUpdated = true
						}
					}
				}
			}
			if releaseUpdated {
				result.Updated++
			}
			advanceProgress()
		}
		if len(releases) < 500 {
			break
		}
	}
}

func (s *Service) fetchSceneCreatedAt(ctx context.Context, url, apiKey string) (map[string]string, error) {
	body, _ := json.Marshal(map[string]string{"query": sceneCreatedAtQuery})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("ApiKey", apiKey)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("Stash returned HTTP %d", resp.StatusCode)
	}
	var payload struct {
		Data struct {
			FindScenes struct {
				Scenes []struct {
					ID        string `json:"id"`
					CreatedAt string `json:"created_at"`
				} `json:"scenes"`
			} `json:"findScenes"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}
	if len(payload.Errors) > 0 {
		return nil, errors.New(payload.Errors[0].Message)
	}
	out := make(map[string]string, len(payload.Data.FindScenes.Scenes))
	for _, scene := range payload.Data.FindScenes.Scenes {
		out[scene.ID] = scene.CreatedAt
	}
	return out, nil
}

func (s *Service) fetch(ctx context.Context, url, query, apiKey string) (map[string]string, map[string]string, int, error) {
	body, _ := json.Marshal(map[string]string{"query": query})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, nil, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("ApiKey", apiKey)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, nil, 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, nil, 0, fmt.Errorf("Stash returned HTTP %d", resp.StatusCode)
	}
	var payload struct {
		Data struct {
			FindScenes struct {
				Scenes []struct{ ID, Title, Code, Date string } `json:"scenes"`
			} `json:"findScenes"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, nil, 0, err
	}
	if len(payload.Errors) > 0 {
		return nil, nil, 0, errors.New(payload.Errors[0].Message)
	}
	ids, dates := map[string]string{}, map[string]string{}
	for _, scene := range payload.Data.FindScenes.Scenes {
		for _, raw := range append([]string{scene.Code}, idInText.FindAllString(scene.Title, -1)...) {
			if id := canonical(raw); id != "" {
				ids[id] = scene.ID
				// scene.Date is absent from a custom stash_graphql_query that
				// does not request it (TODO-2.0's StashApp-sourced release
				// date is best-effort): leave any previously stored date
				// alone rather than blanking it out in that case.
				if scene.Date != "" {
					dates[id] = scene.Date
				}
			}
		}
	}
	return ids, dates, len(payload.Data.FindScenes.Scenes), nil
}

// fetchPlaybackStats tries playbackStatsQueryWithHistory first and falls
// back to playbackStatsQueryBasic (dropping o_history) if that whole
// request fails - see the doc comment on those two consts for why a schema
// validation error from an older StashApp must not take out the rest of
// the playback-stats sync.
func (s *Service) fetchPlaybackStats(ctx context.Context, url, apiKey string) (map[string]playbackStats, error) {
	stats, err := s.fetchPlaybackStatsQuery(ctx, url, apiKey, playbackStatsQueryWithHistory, true)
	if err == nil {
		return stats, nil
	}
	return s.fetchPlaybackStatsQuery(ctx, url, apiKey, playbackStatsQueryBasic, false)
}

func (s *Service) fetchPlaybackStatsQuery(ctx context.Context, url, apiKey, query string, withHistory bool) (map[string]playbackStats, error) {
	body, _ := json.Marshal(map[string]string{"query": query})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("ApiKey", apiKey)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("Stash returned HTTP %d", resp.StatusCode)
	}
	var payload struct {
		Data struct {
			FindScenes struct {
				Scenes []struct {
					ID           string   `json:"id"`
					OCounter     int      `json:"o_counter"`
					PlayCount    int      `json:"play_count"`
					LastPlayedAt string   `json:"last_played_at"`
					OHistory     []string `json:"o_history"`
				} `json:"scenes"`
			} `json:"findScenes"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}
	if len(payload.Errors) > 0 {
		return nil, errors.New(payload.Errors[0].Message)
	}
	out := map[string]playbackStats{}
	for _, scene := range payload.Data.FindScenes.Scenes {
		st := playbackStats{OCounter: scene.OCounter, PlayCount: scene.PlayCount, LastPlayedAt: scene.LastPlayedAt}
		if withHistory {
			for _, t := range scene.OHistory {
				if t > st.LastOCountAt {
					st.LastOCountAt = t
				}
			}
		}
		out[scene.ID] = st
	}
	return out, nil
}

func (s *Service) SyncDesired(ctx context.Context) (DesiredStatus, error) {
	s.desiredMu.Lock()
	defer s.desiredMu.Unlock()
	return s.syncDesired(ctx)
}

func (s *Service) syncDesired(ctx context.Context) (DesiredStatus, error) {
	var out DesiredStatus
	base, key, tagID, e := s.desiredConfig(ctx)
	if e != nil {
		return out, e
	}
	if e = s.verifyDesiredTag(ctx, base, key, tagID); e != nil {
		return out, e
	}
	for offset := 0; ; offset += 500 {
		rows, e := s.store.Releases(ctx, domain.ReleaseFilter{Desired: true, Limit: 500, Offset: offset})
		if e != nil {
			return out, e
		}
		for _, r := range rows {
			out.Checked++
			state, syncErr := s.syncDesiredRelease(ctx, r, base, key, tagID)
			if syncErr != nil {
				return out, syncErr
			}
			if state == "tagged" {
				out.Updated++
			} else {
				out.Skipped++
			}
		}
		if len(rows) < 500 {
			break
		}
	}
	return out, nil
}

func (s *Service) SyncDesiredRelease(ctx context.Context, releaseID int64) (string, error) {
	s.desiredMu.Lock()
	defer s.desiredMu.Unlock()
	r, e := s.store.Release(ctx, releaseID)
	if e != nil {
		return "", e
	}
	if !r.Desired {
		return "not_desired", nil
	}
	base, key, tagID, e := s.desiredConfig(ctx)
	if e != nil {
		return "", e
	}
	if !r.Local || r.StashSceneID == "" {
		return "pending_scene", nil
	}
	if e = s.verifyDesiredTag(ctx, base, key, tagID); e != nil {
		return "", e
	}
	return s.syncDesiredRelease(ctx, r, base, key, tagID)
}

func (s *Service) desiredConfig(ctx context.Context) (base, key, tagID string, err error) {
	settings, err := s.store.Settings(ctx)
	if err != nil {
		return "", "", "", err
	}
	base = strings.TrimRight(strings.TrimSpace(settings["stash_base_url"]), "/")
	key = settings["stash_api_key"]
	tagID = strings.TrimSpace(settings["stash_desired_tag_id"])
	if base == "" || tagID == "" {
		return "", "", "", errors.New("StashApp Base URL and Watchlist tag ID are required")
	}
	return base, key, tagID, nil
}

func (s *Service) verifyDesiredTag(ctx context.Context, base, key, tagID string) error {
	var payload struct {
		Data struct {
			FindTag *struct {
				ID string `json:"id"`
			} `json:"findTag"`
		} `json:"data"`
	}
	if e := s.graphql(ctx, base, key, `query { findTag(id: "`+escapeGraphQL(tagID)+`") { id } }`, &payload); e != nil {
		return e
	}
	if payload.Data.FindTag == nil {
		return errors.New("configured Watchlist tag does not exist in StashApp")
	}
	return nil
}

func (s *Service) syncDesiredRelease(ctx context.Context, r domain.Release, base, key, tagID string) (string, error) {
	if !r.Local || r.StashSceneID == "" {
		return "pending_scene", nil
	}
	if done, _ := s.store.DesiredSynced(ctx, r.ID, r.StashSceneID, tagID); done {
		return "already_synced", nil
	}
	var scene struct {
		Data struct {
			FindScene *struct {
				Tags []struct {
					ID string `json:"id"`
				} `json:"tags"`
			} `json:"findScene"`
		} `json:"data"`
	}
	if e := s.graphql(ctx, base, key, `query { findScene(id: "`+escapeGraphQL(r.StashSceneID)+`") { tags { id } } }`, &scene); e != nil {
		return "", e
	}
	if scene.Data.FindScene == nil {
		return "pending_scene", nil
	}
	tags := []string{}
	for _, tag := range scene.Data.FindScene.Tags {
		tags = append(tags, tag.ID)
		if tag.ID == tagID {
			_ = s.store.SaveDesiredSync(ctx, r.ID, r.StashSceneID, tagID, "tag already present")
			return "already_tagged", nil
		}
	}
	tags = append(tags, tagID)
	quoted := make([]string, 0, len(tags))
	for _, id := range tags {
		quoted = append(quoted, `"`+escapeGraphQL(id)+`"`)
	}
	var mutation any
	if e := s.graphql(ctx, base, key, `mutation { sceneUpdate(input: {id: "`+escapeGraphQL(r.StashSceneID)+`", tag_ids: [`+strings.Join(quoted, ",")+`]}) { id } }`, &mutation); e != nil {
		return "", e
	}
	if e := s.store.SaveDesiredSync(ctx, r.ID, r.StashSceneID, tagID, "tag added"); e != nil {
		return "", e
	}
	return "tagged", nil
}
func (s *Service) graphql(ctx context.Context, base, key, query string, target any) error {
	body, _ := json.Marshal(map[string]string{"query": query})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, base+"/graphql", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("ApiKey", key)
	}
	resp, e := s.client.Do(req)
	if e != nil {
		return e
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("StashApp returned HTTP %d", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(target)
}
func escapeGraphQL(v string) string { return strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(v) }

func canonical(raw string) string {
	raw = strings.ToUpper(raw)
	letters := regexp.MustCompile(`[^A-Z]`).ReplaceAllString(raw, "")
	numbers := regexp.MustCompile(`[^0-9]`).ReplaceAllString(raw, "")
	numbers = strings.TrimLeft(numbers, "0")
	if numbers == "" && strings.ContainsAny(raw, "0123456789") {
		numbers = "0"
	}
	if letters == "" || numbers == "" {
		return ""
	}
	return letters + numbers
}

func (s *Service) Schedule(ctx context.Context) {
	lastAttempt := time.Now()
	for {
		settings, _ := s.store.Settings(ctx)
		interval, err := domain.ParseScheduleDuration(settings["stash_sync_interval"])
		if err != nil || interval < time.Minute {
			interval = 6 * time.Hour
		}
		now := time.Now()
		remaining := interval - now.Sub(lastAttempt)
		if remaining <= 0 {
			lastAttempt = now
			s.startScheduledLocalSync(ctx, settings)
			remaining = interval
		}
		s.mu.Lock()
		if s.scheduleNextAttempt == nil {
			s.scheduleNextAttempt = map[string]time.Time{}
		}
		s.scheduleNextAttempt["sync"] = now.Add(remaining)
		s.mu.Unlock()
		sleep := remaining
		if sleep <= 0 || sleep > scheduleMaxSleepChunk {
			sleep = scheduleMaxSleepChunk
		}
		t := time.NewTimer(sleep)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
	}
}

// startScheduledLocalSync deliberately delegates to Start instead of keeping
// a reduced scheduled-only sync path. This guarantees every regular schedule
// run performs the same required scene created_at fetch, local matching,
// release-date sync, and optional playback-stat sync as the manual Integration
// Sync button.
func (s *Service) startScheduledLocalSync(ctx context.Context, settings map[string]string) bool {
	if settings["stash_local_sync_enabled"] != "true" || strings.TrimSpace(settings["stash_base_url"]) == "" {
		return false
	}
	if err := s.Start(ctx); err != nil {
		s.log.Warn("scheduled Stash local-library sync not started", "error", err)
		return false
	}
	s.log.Info("scheduled Stash local-library sync started")
	return true
}

func (s *Service) DesiredSchedule(ctx context.Context) {
	lastAttempt := time.Now()
	for {
		settings, _ := s.store.Settings(ctx)
		interval, err := domain.ParseScheduleDuration(settings["stash_desired_sync_interval"])
		if err != nil || interval < time.Minute {
			interval = 6 * time.Hour
		}
		now := time.Now()
		remaining := interval - now.Sub(lastAttempt)
		if remaining <= 0 {
			lastAttempt = now
			if settings["stash_desired_sync_enabled"] == "true" && strings.TrimSpace(settings["stash_desired_tag_id"]) != "" {
				result, syncErr := s.SyncDesired(ctx)
				if syncErr != nil {
					s.log.Error("scheduled Stash Watchlist sync failed", "error", syncErr)
				} else {
					s.log.Info("scheduled Stash Watchlist sync completed", "checked", result.Checked, "updated", result.Updated, "skipped", result.Skipped)
				}
			}
			remaining = interval
		}
		s.mu.Lock()
		if s.scheduleNextAttempt == nil {
			s.scheduleNextAttempt = map[string]time.Time{}
		}
		s.scheduleNextAttempt["desired_sync"] = now.Add(remaining)
		s.mu.Unlock()
		sleep := remaining
		if sleep <= 0 || sleep > scheduleMaxSleepChunk {
			sleep = scheduleMaxSleepChunk
		}
		t := time.NewTimer(sleep)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
	}
}

// scheduleForecastRunCount is how many upcoming run times ScheduleForecast
// predicts per schedule - matches monitor.scheduleForecastRunCount and
// download.scheduleForecastRunCount so every panel shows the same count.
const scheduleForecastRunCount = 3

// ScheduleForecast reports the StashApp local-library and Desired-tag sync
// schedules' current enabled/interval state plus their next
// scheduleForecastRunCount predicted run times, reading the live
// scheduleNextAttempt time Schedule/DesiredSchedule last computed so it can
// never drift from what those loops will actually do next.
func (s *Service) ScheduleForecast(ctx context.Context) []domain.ScheduleForecast {
	settings, _ := s.store.Settings(ctx)
	syncEnabled := settings["stash_local_sync_enabled"] == "true" && settings["stash_base_url"] != ""
	desiredEnabled := settings["stash_desired_sync_enabled"] == "true" && strings.TrimSpace(settings["stash_desired_tag_id"]) != ""
	return []domain.ScheduleForecast{
		s.intervalScheduleForecast("sync", "Local library sync", syncEnabled, settings["stash_sync_interval"]),
		s.intervalScheduleForecast("desired_sync", "Watchlist-tag sync", desiredEnabled, settings["stash_desired_sync_interval"]),
	}
}

// intervalScheduleForecast builds one ScheduleForecast entry for a
// live-tracked schedule loop keyed by mode in s.scheduleNextAttempt,
// extrapolating scheduleForecastRunCount future runs by repeatedly adding
// its interval to the loop's live next-check time.
func (s *Service) intervalScheduleForecast(mode, name string, enabled bool, rawInterval string) domain.ScheduleForecast {
	interval, err := domain.ParseScheduleDuration(rawInterval)
	if err != nil || interval < time.Minute {
		interval = 6 * time.Hour
	}
	forecast := domain.ScheduleForecast{Group: "StashApp sync", Name: name, Enabled: enabled, Interval: interval.String()}
	if !enabled {
		return forecast
	}
	s.mu.RLock()
	next, tracked := s.scheduleNextAttempt[mode]
	s.mu.RUnlock()
	if !tracked {
		return forecast
	}
	runs := make([]time.Time, 0, scheduleForecastRunCount)
	for len(runs) < scheduleForecastRunCount {
		runs = append(runs, next)
		next = next.Add(interval)
	}
	forecast.NextRuns = runs
	return forecast
}

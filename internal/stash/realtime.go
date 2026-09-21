package stash

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Net005/JAVBeacon/internal/domain"
)

const realtimeSceneQuery = `query JAVBeaconRealtimeScene($id: ID!) { findScene(id: $id) { id title code date created_at urls o_counter play_count last_played_at play_duration play_history o_history tags { id } files { path } } }`

var errRealtimeSceneUnmatched = errors.New("changed Stash scene does not have a matchable JAVBeacon release yet")

type RealtimeStatus struct {
	Enabled       bool      `json:"enabled"`
	Running       bool      `json:"running"`
	Queued        int       `json:"queued"`
	CurrentScene  string    `json:"current_scene,omitempty"`
	LastScene     string    `json:"last_scene,omitempty"`
	LastEvent     string    `json:"last_event,omitempty"`
	LastMatchedID string    `json:"last_matched_release,omitempty"`
	LastError     string    `json:"last_error,omitempty"`
	LastRunAt     time.Time `json:"last_run_at,omitempty"`
	Synced        int64     `json:"synced"`
}

type realtimeScene struct {
	ID           string   `json:"id"`
	Title        string   `json:"title"`
	Code         string   `json:"code"`
	Date         string   `json:"date"`
	CreatedAt    string   `json:"created_at"`
	URLs         []string `json:"urls"`
	OCounter     int      `json:"o_counter"`
	PlayCount    int      `json:"play_count"`
	LastPlayedAt string   `json:"last_played_at"`
	PlayDuration float64  `json:"play_duration"`
	PlayHistory  []string `json:"play_history"`
	OHistory     []string `json:"o_history"`
	Tags         []struct {
		ID string `json:"id"`
	} `json:"tags"`
	Files []struct {
		Path string `json:"path"`
	} `json:"files"`
}

func (s *Service) RealtimeStatus(ctx context.Context) RealtimeStatus {
	settings, _ := s.store.Settings(ctx)
	s.realtimeMu.RLock()
	status := s.realtimeStatus
	status.Queued = len(s.realtimePending)
	s.realtimeMu.RUnlock()
	status.Enabled = settings["stash_realtime_enabled"] == "true"
	return status
}

// EnqueueRealtimeScene coalesces repeated Stash hooks for the same scene. The
// hook request returns immediately; Stash API traffic and retries happen in a
// single background worker so update bursts cannot overwhelm either app.
func (s *Service) EnqueueRealtimeScene(ctx context.Context, sceneID, event string) error {
	settings, err := s.store.Settings(ctx)
	if err != nil {
		return err
	}
	if settings["stash_realtime_enabled"] != "true" {
		return errors.New("realtime Stash sync is disabled")
	}
	return s.enqueueRealtimeScene(sceneID, event)
}

// EnqueueAuthenticatedRealtimeScene is used by the webhook after it has
// already loaded settings and authenticated the caller. Keeping this path
// database-free lets Stash receive 202 Accepted promptly even while another
// JAVBeacon task is holding the database busy.
func (s *Service) EnqueueAuthenticatedRealtimeScene(sceneID, event string) error {
	return s.enqueueRealtimeScene(sceneID, event)
}

func (s *Service) enqueueRealtimeScene(sceneID, event string) error {
	sceneID = strings.TrimSpace(sceneID)
	if sceneID == "" {
		return errors.New("scene_id is required")
	}
	s.realtimeMu.Lock()
	_, coalesced := s.realtimePending[sceneID]
	s.realtimePending[sceneID] = time.Now().UTC()
	s.realtimeStatus.LastEvent = strings.TrimSpace(event)
	s.realtimeStatus.Queued = len(s.realtimePending)
	s.realtimeMu.Unlock()
	s.log.Debug("realtime Stash scene queued", "scene_id", sceneID, "event", strings.TrimSpace(event), "coalesced", coalesced)
	select {
	case s.realtimeWake <- struct{}{}:
	default:
	}
	return nil
}

func settingInt(settings map[string]string, key string, fallback, minimum int) int {
	v, err := strconv.Atoi(strings.TrimSpace(settings[key]))
	if err != nil || v < minimum {
		return fallback
	}
	return v
}

func (s *Service) realtimeWorker() {
	for range s.realtimeWake {
		for {
			settings, err := s.store.Settings(context.Background())
			if err != nil {
				s.log.Warn("realtime Stash worker could not load settings", "error", err)
				break
			}
			debounce := time.Duration(settingInt(settings, "stash_realtime_debounce_seconds", 3, 0)) * time.Second
			if debounce > 0 {
				time.Sleep(debounce)
			}
			s.realtimeMu.Lock()
			var sceneID string
			for id := range s.realtimePending {
				sceneID = id
				delete(s.realtimePending, id)
				break
			}
			if sceneID == "" {
				s.realtimeStatus.Running = false
				s.realtimeStatus.Queued = 0
				s.realtimeMu.Unlock()
				break
			}
			s.realtimeStatus.Running, s.realtimeStatus.CurrentScene = true, sceneID
			s.realtimeStatus.Queued = len(s.realtimePending)
			s.realtimeMu.Unlock()

			attempts := settingInt(settings, "stash_realtime_retry_attempts", 3, 1)
			delay := time.Duration(settingInt(settings, "stash_realtime_retry_delay_seconds", 2, 1)) * time.Second
			var matched string
			for attempt := 1; attempt <= attempts; attempt++ {
				s.log.Debug("realtime Stash scene sync attempt", "scene_id", sceneID, "attempt", attempt, "max_attempts", attempts)
				matched, err = s.syncRealtimeScene(context.Background(), settings, sceneID)
				if err == nil {
					break
				}
				s.log.Debug("realtime Stash scene sync attempt failed", "scene_id", sceneID, "attempt", attempt, "error", err)
				if attempt < attempts {
					time.Sleep(delay * time.Duration(attempt))
				}
			}
			s.realtimeMu.Lock()
			s.realtimeStatus.CurrentScene = ""
			s.realtimeStatus.LastScene = sceneID
			s.realtimeStatus.LastMatchedID = matched
			s.realtimeStatus.LastRunAt = time.Now().UTC()
			if err != nil {
				s.realtimeStatus.LastError = err.Error()
			} else {
				s.realtimeStatus.LastError = ""
				s.realtimeStatus.Synced++
			}
			s.realtimeMu.Unlock()
			if err != nil {
				s.log.Warn("realtime Stash scene sync failed", "scene_id", sceneID, "error", err)
			} else {
				s.log.Info("realtime Stash scene sync completed", "scene_id", sceneID, "release", matched)
			}
		}
	}
}

func (s *Service) fetchRealtimeScene(ctx context.Context, url, apiKey, sceneID string) (*realtimeScene, error) {
	started := time.Now()
	s.log.Debug("requesting changed scene from Stash", "scene_id", sceneID, "url", url, "api_key_configured", apiKey != "")
	body, _ := json.Marshal(map[string]any{"query": realtimeSceneQuery, "variables": map[string]string{"id": sceneID}})
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
		s.log.Debug("changed-scene Stash request failed", "scene_id", sceneID, "elapsed", time.Since(started), "error", err)
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		s.log.Debug("changed-scene Stash request returned error", "scene_id", sceneID, "status", resp.StatusCode, "elapsed", time.Since(started))
		return nil, fmt.Errorf("Stash returned HTTP %d", resp.StatusCode)
	}
	var payload struct {
		Data struct {
			FindScene *realtimeScene `json:"findScene"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}
	if len(payload.Errors) > 0 {
		s.log.Debug("changed-scene Stash GraphQL error", "scene_id", sceneID, "elapsed", time.Since(started), "error", payload.Errors[0].Message)
		return nil, errors.New(payload.Errors[0].Message)
	}
	s.log.Debug("changed scene received from Stash", "scene_id", sceneID, "found", payload.Data.FindScene != nil, "elapsed", time.Since(started))
	return payload.Data.FindScene, nil
}

func (s *Service) syncRealtimeScene(ctx context.Context, settings map[string]string, sceneID string) (string, error) {
	started := time.Now()
	base := strings.TrimRight(strings.TrimSpace(settings["stash_base_url"]), "/")
	if base == "" {
		return "", errors.New("StashApp Base URL is not configured")
	}
	scene, err := s.fetchRealtimeScene(ctx, base+"/graphql", strings.TrimSpace(settings["stash_api_key"]), sceneID)
	if err != nil {
		return "", err
	}
	var match *domain.Release
	sceneKeys := realtimeSceneMatchKeys(scene)
	for offset := 0; ; offset += 500 {
		releases, e := s.store.Releases(ctx, domain.ReleaseFilter{Limit: 500, Offset: offset})
		if e != nil {
			return "", e
		}
		for i := range releases {
			if releases[i].StashSceneID == sceneID || sceneKeys[canonical(releases[i].VideoID)] {
				r := releases[i]
				match = &r
				break
			}
		}
		if match != nil || len(releases) < 500 {
			break
		}
	}
	if scene == nil {
		if match == nil {
			s.log.Debug("deleted Stash scene has no JAVBeacon match", "scene_id", sceneID, "elapsed", time.Since(started))
			return "", nil
		}
		if err := s.store.SetStashState(ctx, match.ID, false, ""); err != nil {
			return "", err
		}
		if err := s.store.SetStashFilePath(ctx, match.ID, ""); err != nil {
			return "", err
		}
		if strings.TrimSpace(settings["stash_watchlist_tag_id"]) != "" && match.Watchlist {
			watchlist := false
			if err := s.store.PatchRelease(ctx, match.ID, nil, nil, nil, nil, &watchlist, nil, nil, nil); err != nil {
				return "", err
			}
		}
		s.markJellyfinLibraryChanged(ctx)
		s.log.Debug("deleted Stash scene cleared from JAVBeacon release", "scene_id", sceneID, "release", match.VideoID, "elapsed", time.Since(started))
		return match.VideoID, nil
	}
	if match == nil {
		// Scene.Create.Post can arrive before Stash has finished attaching the
		// file or applying scraper metadata. Returning a retryable error lets the
		// existing bounded retry policy fetch the scene again instead of silently
		// treating that incomplete first snapshot as synchronized.
		s.log.Debug("changed Stash scene is not matchable yet", "scene_id", sceneID, "code", scene.Code, "candidate_keys", len(sceneKeys), "elapsed", time.Since(started))
		return "", errRealtimeSceneUnmatched
	}
	if err := s.store.SetStashState(ctx, match.ID, true, scene.ID); err != nil {
		return "", err
	}
	path := ""
	for _, f := range scene.Files {
		if strings.TrimSpace(f.Path) != "" {
			path = f.Path
			break
		}
	}
	if err := s.store.SetStashFilePath(ctx, match.ID, path); err != nil {
		return "", err
	}
	if tagID := strings.TrimSpace(settings["stash_watchlist_tag_id"]); tagID != "" {
		hasTag := false
		for _, tag := range scene.Tags {
			if tag.ID == tagID {
				hasTag = true
				break
			}
		}
		switch {
		case hasTag == match.Watchlist:
			// Already in sync.
		case !hasTag && match.Watchlist:
			// Stash has no tag but JAVBeacon thinks this release is
			// watchlisted. Only trust that as an intentional un-watchlist if
			// we've previously confirmed the tag was applied to this scene;
			// otherwise this release's Watchlist mark was never pushed yet
			// (e.g. it was marked while still downloading, before this scene
			// existed to tag) - push it now instead of silently discarding
			// the mark the instant the release becomes local.
			if synced, _ := s.store.WatchlistSynced(ctx, match.ID, scene.ID, tagID); synced {
				if err := s.store.PatchRelease(ctx, match.ID, nil, nil, nil, nil, &hasTag, nil, nil, nil); err != nil {
					return "", err
				}
			} else {
				withScene := *match
				withScene.StashSceneID = scene.ID
				if _, err := s.setWatchlistTag(ctx, withScene, base, strings.TrimSpace(settings["stash_api_key"]), tagID, true); err != nil {
					s.log.Warn("failed to push pending Watchlist tag for newly-local release", "release_id", match.ID, "video_id", match.VideoID, "error", err)
				}
			}
		default:
			// Stash has the tag but JAVBeacon doesn't know about it yet -
			// adopt it.
			if err := s.store.PatchRelease(ctx, match.ID, nil, nil, nil, nil, &hasTag, nil, nil, nil); err != nil {
				return "", err
			}
		}
	}
	if scene.Date != "" {
		if err := s.store.SetStashReleaseDate(ctx, match.ID, scene.Date); err != nil {
			return "", err
		}
	}
	if scene.CreatedAt != "" {
		created, e := time.Parse(time.RFC3339, scene.CreatedAt)
		if e != nil {
			return "", e
		}
		if e = s.store.SetStashCreatedAt(ctx, match.ID, created); e != nil {
			return "", e
		}
	}
	lastO := match.LastOCountAt
	for _, raw := range scene.OHistory {
		if raw > lastO {
			lastO = raw
		}
	}
	if err := s.store.SetStashPlaybackStats(ctx, match.ID, scene.OCounter, scene.PlayCount, scene.LastPlayedAt, lastO); err != nil {
		return "", err
	}
	if history, ok := s.store.(interface {
		UpsertStashHistory(context.Context, domain.StashHistoryScene, []time.Time, []time.Time) error
	}); ok {
		plays, e := parseHistoryTimes(scene.PlayHistory)
		if e != nil {
			return "", e
		}
		orgasms, e := parseHistoryTimes(scene.OHistory)
		if e != nil {
			return "", e
		}
		h := domain.StashHistoryScene{StashSceneID: scene.ID, ReleaseID: match.ID, VideoID: match.VideoID, Title: scene.Title, JavLibraryURL: firstJavLibraryURL(scene.URLs), FilePath: path, TotalPlaySeconds: scene.PlayDuration, PlayCount: len(plays), OrgasmCount: len(orgasms), ObservedAt: time.Now().UTC()}
		if h.JavLibraryURL == "" && strings.Contains(strings.ToLower(match.ProductURL), "javlibrary") {
			h.JavLibraryURL = match.ProductURL
		}
		if err := history.UpsertStashHistory(ctx, h, plays, orgasms); err != nil {
			return "", err
		}
	}
	s.markJellyfinLibraryChanged(ctx)
	s.log.Debug("changed Stash scene applied to JAVBeacon", "scene_id", sceneID, "release", match.VideoID, "file_path", path, "plays", scene.PlayCount, "orgasms", scene.OCounter, "elapsed", time.Since(started))
	return match.VideoID, nil
}

func realtimeSceneMatchKeys(scene *realtimeScene) map[string]bool {
	keys := map[string]bool{}
	if scene == nil {
		return keys
	}
	addExact := func(raw string) {
		if key := canonical(raw); key != "" {
			keys[key] = true
		}
	}
	addEmbedded := func(raw string) {
		for _, candidate := range idInText.FindAllString(raw, -1) {
			addExact(candidate)
		}
	}

	// Code is the authoritative Stash identifier. Titles, file paths, and
	// source URLs are supporting identifiers for newly imported scenes whose
	// code has not been populated yet.
	addExact(scene.Code)
	addEmbedded(scene.Title)
	for _, file := range scene.Files {
		name := filepath.Base(strings.TrimSpace(file.Path))
		addEmbedded(strings.TrimSuffix(name, filepath.Ext(name)))
	}
	for _, rawURL := range scene.URLs {
		parsed, err := url.Parse(strings.TrimSpace(rawURL))
		if err != nil {
			continue
		}
		name := filepath.Base(parsed.Path)
		addEmbedded(strings.TrimSuffix(name, filepath.Ext(name)))
		for _, values := range parsed.Query() {
			for _, value := range values {
				addEmbedded(value)
			}
		}
	}
	return keys
}

func (s *Service) markJellyfinLibraryChanged(ctx context.Context) {
	if err := s.store.SaveSettings(ctx, map[string]string{"jellyfin_library_revision": time.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
		s.log.Warn("unable to mark Jellyfin library sync pending", "error", err)
	}
}

package stash

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Net005/JAVBeacon/internal/domain"
)

const realtimeSceneQuery = `query JAVBeaconRealtimeScene($id: ID!) { findScene(id: $id) { id title code date created_at urls o_counter play_count last_played_at play_duration play_history o_history tags { id } files { path } } }`

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
	sceneKeys := map[string]bool{}
	if scene != nil {
		if key := canonical(scene.Code); key != "" {
			sceneKeys[key] = true
		}
		for _, raw := range idInText.FindAllString(scene.Title, -1) {
			if key := canonical(raw); key != "" {
				sceneKeys[key] = true
			}
		}
	}
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
			if err := s.store.PatchRelease(ctx, match.ID, nil, nil, nil, nil, &watchlist, nil, nil, nil, nil); err != nil {
				return "", err
			}
		}
		s.markJellyfinLibraryChanged(ctx)
		s.log.Debug("deleted Stash scene cleared from JAVBeacon release", "scene_id", sceneID, "release", match.VideoID, "elapsed", time.Since(started))
		return match.VideoID, nil
	}
	if match == nil {
		// A later full sync may match custom title-based queries. Realtime sync
		// deliberately avoids guessing when Stash has no canonical scene code.
		s.markJellyfinLibraryChanged(ctx)
		s.log.Debug("changed Stash scene has no JAVBeacon release match", "scene_id", sceneID, "code", scene.Code, "candidate_keys", len(sceneKeys), "elapsed", time.Since(started))
		return "", nil
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
		watchlist := false
		for _, tag := range scene.Tags {
			if tag.ID == tagID {
				watchlist = true
				break
			}
		}
		if watchlist != match.Watchlist {
			if err := s.store.PatchRelease(ctx, match.ID, nil, nil, nil, nil, &watchlist, nil, nil, nil, nil); err != nil {
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

func (s *Service) markJellyfinLibraryChanged(ctx context.Context) {
	if err := s.store.SaveSettings(ctx, map[string]string{"jellyfin_library_revision": time.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
		s.log.Warn("unable to mark Jellyfin library sync pending", "error", err)
	}
}

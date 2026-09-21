package web

import (
	"crypto/subtle"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Net005/JAVBeacon/internal/domain"
)

func (s *Server) authenticateStashRealtime(w http.ResponseWriter, r *http.Request) bool {
	settings, err := s.store.Settings(r.Context())
	if err != nil {
		s.log.Error("Stash plugin request could not load settings", "path", r.URL.Path, "error", err)
		s.problem(w, http.StatusInternalServerError, err.Error())
		return false
	}
	if settings["stash_realtime_enabled"] != "true" {
		s.log.Debug("Stash plugin request rejected", "path", r.URL.Path, "reason", "realtime sync disabled", "remote", r.RemoteAddr)
		s.problem(w, http.StatusServiceUnavailable, "realtime Stash sync is disabled")
		return false
	}
	want := strings.TrimSpace(settings["stash_realtime_secret"])
	got := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	if got == "" {
		got = strings.TrimSpace(r.Header.Get("X-JAVBeacon-Webhook-Secret"))
	}
	if want == "" || len(got) != len(want) || subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
		s.log.Warn("Stash plugin authentication failed", "path", r.URL.Path, "remote", r.RemoteAddr)
		s.problem(w, http.StatusUnauthorized, "invalid realtime Stash sync secret")
		return false
	}
	return true
}

func (s *Server) stashRealtimeTest(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	if !s.authenticateStashRealtime(w, r) {
		return
	}
	var payload struct {
		RequestID string `json:"request_id"`
		Event     string `json:"event"`
		// SceneID is accepted but ignored: older and third-party plugin
		// clients send it on connection-test calls even though the test
		// endpoint has no scene to act on.
		SceneID string `json:"scene_id"`
	}
	if !s.decode(w, r, &payload) {
		return
	}
	s.log.Info("Stash plugin connection test passed", "request_id", strings.TrimSpace(payload.RequestID), "event", strings.TrimSpace(payload.Event), "remote", r.RemoteAddr, "elapsed", time.Since(started))
	s.json(w, http.StatusOK, map[string]any{"state": "connected", "request_id": strings.TrimSpace(payload.RequestID), "realtime_enabled": true})
}

func (s *Server) stashRealtimeEvent(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	if !s.authenticateStashRealtime(w, r) {
		return
	}
	var payload struct {
		SceneID   string `json:"scene_id"`
		Event     string `json:"event"`
		RequestID string `json:"request_id"`
	}
	if !s.decode(w, r, &payload) {
		return
	}
	if err := s.stash.EnqueueAuthenticatedRealtimeScene(payload.SceneID, payload.Event); err != nil {
		s.log.Warn("Stash plugin event rejected", "request_id", payload.RequestID, "scene_id", payload.SceneID, "event", payload.Event, "error", err)
		s.problem(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	s.log.Debug("Stash plugin event accepted", "request_id", strings.TrimSpace(payload.RequestID), "scene_id", strings.TrimSpace(payload.SceneID), "event", strings.TrimSpace(payload.Event), "remote", r.RemoteAddr, "elapsed", time.Since(started))
	s.json(w, http.StatusAccepted, map[string]any{"state": "accepted", "request_id": strings.TrimSpace(payload.RequestID), "scene_id": strings.TrimSpace(payload.SceneID)})
}

func (s *Server) stashReleaseLink(w http.ResponseWriter, r *http.Request) {
	if !s.authenticateStashRealtime(w, r) {
		return
	}
	var payload struct {
		SceneID string `json:"scene_id"`
	}
	if !s.decode(w, r, &payload) {
		return
	}
	payload.SceneID = strings.TrimSpace(payload.SceneID)
	if payload.SceneID == "" {
		s.problem(w, http.StatusBadRequest, "scene ID is required")
		return
	}
	releases, err := s.store.Releases(r.Context(), domain.ReleaseFilter{
		StashSceneID: payload.SceneID,
		Limit:        1,
	})
	if err != nil {
		s.problem(w, http.StatusInternalServerError, err.Error())
		return
	}
	if len(releases) == 0 {
		s.problem(w, http.StatusNotFound, "no JAVBeacon release is linked to this Stash scene")
		return
	}
	s.json(w, http.StatusOK, map[string]any{
		"release_id":   releases[0].ID,
		"release_path": "/release/" + strconv.FormatInt(releases[0].ID, 10),
		"video_id":     releases[0].VideoID,
	})
}

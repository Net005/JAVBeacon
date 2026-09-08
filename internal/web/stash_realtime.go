package web

import (
	"crypto/subtle"
	"net/http"
	"strings"
)

func (s *Server) stashRealtimeEvent(w http.ResponseWriter, r *http.Request) {
	settings, err := s.store.Settings(r.Context())
	if err != nil {
		s.problem(w, http.StatusInternalServerError, err.Error())
		return
	}
	if settings["stash_realtime_enabled"] != "true" {
		s.problem(w, http.StatusServiceUnavailable, "realtime Stash sync is disabled")
		return
	}
	want := strings.TrimSpace(settings["stash_realtime_secret"])
	got := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	if got == "" {
		got = strings.TrimSpace(r.Header.Get("X-JAVBeacon-Webhook-Secret"))
	}
	if want == "" || len(got) != len(want) || subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
		s.problem(w, http.StatusUnauthorized, "invalid realtime Stash sync secret")
		return
	}
	var payload struct {
		SceneID string `json:"scene_id"`
		Event   string `json:"event"`
	}
	if !s.decode(w, r, &payload) {
		return
	}
	if err := s.stash.EnqueueRealtimeScene(r.Context(), payload.SceneID, payload.Event); err != nil {
		s.problem(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	s.json(w, http.StatusAccepted, s.stash.RealtimeStatus(r.Context()))
}

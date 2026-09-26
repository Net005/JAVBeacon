package web

import (
	"io"
	"net/http"
)

// performerImage streams a StashApp performer's portrait directly, proxying
// it the same way jellyfinStashCover proxies a scene screenshot - so the
// Stash base URL and API key never reach Jellyfin or Silo. Shared by both
// integrations (not namespaced under /jellyfin/ or /silo/) since a
// performer's photo is provider-agnostic: Metadata.PerformerImages
// (internal/jellyfin) already carries the fully-formed path to this route.
func (s *Server) performerImage(w http.ResponseWriter, r *http.Request) {
	performerID := r.PathValue("performerId")
	if performerID == "" {
		s.problem(w, http.StatusBadRequest, "invalid performer id")
		return
	}
	resp, err := s.stash.FetchPerformerImage(r.Context(), performerID)
	if err != nil {
		s.problem(w, http.StatusBadGateway, err.Error())
		return
	}
	defer resp.Body.Close()
	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "image/jpeg"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "public, max-age=3600")
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, resp.Body)
}

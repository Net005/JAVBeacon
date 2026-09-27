package web

import (
	"io"
	"net/http"
	"net/url"
	"strings"
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

// performerStashRedirect keeps Silo person homepages stable if the StashApp
// base URL changes. This public route contains no credentials or person data.
func (s *Server) performerStashRedirect(w http.ResponseWriter, r *http.Request) {
	performerID := r.PathValue("performerId")
	if performerID == "" || strings.ContainsAny(performerID, "/\\") {
		http.NotFound(w, r)
		return
	}
	settings, err := s.store.Settings(r.Context())
	if err != nil {
		http.Error(w, "settings unavailable", http.StatusServiceUnavailable)
		return
	}
	base := strings.TrimRight(strings.TrimSpace(settings["stash_base_url"]), "/")
	if base == "" {
		http.NotFound(w, r)
		return
	}
	http.Redirect(w, r, base+"/performers/"+url.PathEscape(performerID), http.StatusFound)
}

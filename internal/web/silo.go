package web

import (
	"database/sql"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	siloIntegration "github.com/Net005/JAVBeacon/internal/silo"
)

// siloSearch and siloMetadata expose Silo's own, independent metadata service
// (internal/silo.Service.Search/Metadata) under the Silo integration path,
// for the silo-plugin-metadata-javbeacon plugin (metadata_provider.v1 +
// image_resolver.v1). This deliberately does NOT call into
// internal/jellyfin.Service - the two integrations shared one backend for a
// while, and every cross-contamination bug this project hit (a Jellyfin-only
// collection-sync crash, a Jellyfin-scan-path performer-image gap, a
// Search() enrichment cost that only actually hurt Silo's automated
// matching) traced back to that sharing. internal/silo's own Metadata DTO is
// free to diverge from internal/jellyfin's where Silo's plugin SDK genuinely
// has different needs (see its own doc comment) without either client
// depending on the other's shape.
func (s *Server) siloSearch(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	rows, err := s.silo.Search(r.Context(), r.URL.Query().Get("q"), limit)
	if err != nil {
		s.problem(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.json(w, http.StatusOK, map[string]any{"items": rows, "total": len(rows)})
}

func (s *Server) siloMetadata(w http.ResponseWriter, r *http.Request) {
	n, err := id(r)
	if err != nil {
		s.problem(w, http.StatusBadRequest, "invalid release id")
		return
	}
	value, err := s.silo.Metadata(r.Context(), n)
	if errors.Is(err, sql.ErrNoRows) {
		s.problem(w, http.StatusNotFound, "release not found")
		return
	}
	if err != nil {
		s.problem(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.json(w, http.StatusOK, value)
}

// siloStashCover mirrors jellyfinStashCover under Silo's own dedicated path
// (see internal/silo.Metadata.StashScreenshotURL) rather than pointing the
// Silo plugin at Jellyfin's /api/v1/integrations/jellyfin/releases/{id}/
// stash-cover - a gap-fill image source Silo never had at all before this
// endpoint existed. Both handlers proxy the identical underlying StashApp
// screenshot fetch (s.store.Release + s.stash.FetchSceneScreenshot); kept as
// two small handlers rather than one shared function so each integration's
// route can evolve independently, consistent with the rest of this split.
func (s *Server) siloStashCover(w http.ResponseWriter, r *http.Request) {
	n, err := id(r)
	if err != nil {
		s.problem(w, http.StatusBadRequest, "invalid release id")
		return
	}
	release, err := s.store.Release(r.Context(), n)
	if errors.Is(err, sql.ErrNoRows) {
		s.problem(w, http.StatusNotFound, "release not found")
		return
	}
	if err != nil {
		s.problem(w, http.StatusInternalServerError, err.Error())
		return
	}
	if release.StashSceneID == "" {
		s.problem(w, http.StatusNotFound, "release is not linked to a StashApp scene")
		return
	}
	resp, err := s.stash.FetchSceneScreenshot(r.Context(), release.StashSceneID)
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

// siloPlayback forwards playback/scrobble events reported by the Silo
// watch_sync_provider.v1 capability into internal/silo.Service's own
// checkpoint/resume/completion-threshold engine - a full duplicate of
// internal/jellyfin.Service's Playback, not a call into it. See
// internal/silo's own package doc comment for why this was split out.
func (s *Server) siloPlayback(w http.ResponseWriter, r *http.Request) {
	var event siloIntegration.PlaybackEvent
	if !s.decode(w, r, &event) {
		return
	}
	result, err := s.silo.Playback(r.Context(), event)
	if err != nil {
		status := http.StatusBadGateway
		if strings.Contains(err.Error(), "required") || strings.Contains(err.Error(), "event must") || strings.Contains(err.Error(), "already bound") || strings.Contains(err.Error(), "not mapped") {
			status = http.StatusUnprocessableEntity
		}
		if errors.Is(err, sql.ErrNoRows) {
			status = http.StatusNotFound
		}
		s.problem(w, status, err.Error())
		return
	}
	s.json(w, http.StatusOK, result)
}

// siloLibrarySync exposes internal/silo.Service.LibrarySync's own
// revision/filter-preset-membership snapshot under the Silo integration
// path - independent of internal/jellyfin.Service.LibrarySync, though both
// resolve saved filter sets through the same internal/filterpreset package,
// so membership never disagrees between the two integrations. The Silo
// plugin's scheduled_task.v1 "collection-sync" task polls this to notice
// when a saved filter set's membership changed since its last run, then
// calls Silo's own POST /api/v2/admin/items/{id}/refresh-metadata for every
// affected item it can map to a Silo media ID (see
// watchsync.go/collectionsync.go in the Silo plugin repo) - there is no host
// API for a plugin to push new metadata or invalidate an item directly, so
// this poll-and-refresh loop is the closest available substitute for the
// realtime push Jellyfin's own plugin gets from running in-process against
// ICollectionManager.
func (s *Server) siloLibrarySync(w http.ResponseWriter, r *http.Request) {
	value, err := s.silo.LibrarySync(r.Context())
	if err != nil {
		s.problem(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.json(w, http.StatusOK, value)
}

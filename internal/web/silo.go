package web

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"

	jellyfinintegration "github.com/Net005/JAVBeacon/internal/jellyfin"
)

// siloSearch and siloMetadata expose the same release-metadata shape as the
// Jellyfin integration (internal/jellyfin.Service.Search/Metadata) under a
// Silo-specific path, for the silo-plugin-metadata-javbeacon plugin
// (metadata_provider.v1 + image_resolver.v1). The underlying DTO
// (jellyfin.Metadata) is already provider-agnostic - its ProviderIDs use the
// generic ProviderIDRelease/ProviderIDStash keys, not anything Jellyfin
// specific - so this reuses the exact same service and JSON shape rather than
// duplicating it under a second name. Keeping a distinct URL path (rather
// than pointing the plugin at /api/v1/integrations/jellyfin/*) means the two
// integrations' contracts can diverge later - e.g. Jellyfin-only playback
// sync fields - without either client depending on the other's naming.
func (s *Server) siloSearch(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	rows, err := s.jellyfin.Search(r.Context(), r.URL.Query().Get("q"), limit)
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
	value, err := s.jellyfin.Metadata(r.Context(), n)
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

// siloPlayback forwards playback/scrobble events reported by the Silo
// watch_sync_provider.v1 capability into the exact same JAVBeacon playback
// engine (checkpointing, resume, completion thresholds, play-count/O-count
// writeback to StashApp) the Jellyfin plugin's /api/v1/integrations/jellyfin/
// playback endpoint uses. jellyfin.PlaybackEvent is already provider-agnostic
// (JellyfinItemID/JellyfinUserID are optional labels, not required fields),
// so no separate DTO is needed for Silo.
func (s *Server) siloPlayback(w http.ResponseWriter, r *http.Request) {
	var event jellyfinintegration.PlaybackEvent
	if !s.decode(w, r, &event) {
		return
	}
	result, err := s.jellyfin.Playback(r.Context(), event)
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

// siloLibrarySync exposes the exact same revision/filter-preset-membership
// snapshot the Jellyfin plugin polls (internal/jellyfin.Service.LibrarySync),
// under the Silo integration path. The Silo plugin's scheduled_task.v1
// "collection-sync" task polls this to notice when a saved filter set's
// membership changed since its last run, then calls Silo's own
// POST /api/v2/admin/items/{id}/refresh-metadata for every affected item it
// can map to a Silo media ID (see watchsync.go/collectionsync.go in the Silo
// plugin repo) - there is no host API for a plugin to push new metadata or
// invalidate an item directly, so this poll-and-refresh loop is the closest
// available substitute for the realtime push Jellyfin's own plugin gets from
// running in-process against ICollectionManager.
func (s *Server) siloLibrarySync(w http.ResponseWriter, r *http.Request) {
	value, err := s.jellyfin.LibrarySync(r.Context())
	if err != nil {
		s.problem(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.json(w, http.StatusOK, value)
}

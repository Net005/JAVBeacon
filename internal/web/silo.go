package web

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"
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

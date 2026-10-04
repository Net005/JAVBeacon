package web

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/Net005/JAVBeacon/internal/domain"
)

// stashEnrichment provides release data to Stash.Metadata without routing
// through the retired Silo plugin contract. The normal JAVBeacon API key
// middleware protects this endpoint. Stash remains responsible for choosing
// which missing fields to fill and for applying its own GraphQL updates.
func (s *Server) stashEnrichment(w http.ResponseWriter, r *http.Request) {
	sceneID := strings.TrimSpace(r.PathValue("sceneId"))
	if sceneID == "" {
		s.problem(w, http.StatusBadRequest, "scene ID is required")
		return
	}
	releases, err := s.store.Releases(r.Context(), domain.ReleaseFilter{StashSceneID: sceneID, Limit: 2})
	if err != nil {
		s.problem(w, http.StatusInternalServerError, err.Error())
		return
	}
	if len(releases) == 0 {
		s.problem(w, http.StatusNotFound, "no JAVBeacon release is linked to this Stash scene")
		return
	}
	if len(releases) != 1 {
		s.problem(w, http.StatusConflict, "multiple JAVBeacon releases are linked to this Stash scene")
		return
	}
	release := releases[0]
	posterPath := ""
	if strings.TrimSpace(release.ImageURL) != "" {
		posterPath = "/covers/" + strconv.FormatInt(release.ID, 10) + "/stash-poster"
	}
	s.json(w, http.StatusOK, map[string]any{
		"scene_id":    sceneID,
		"release_id":  release.ID,
		"code":        strings.TrimSpace(release.VideoID),
		"title":       strings.TrimSpace(release.Title),
		"details":     strings.TrimSpace(release.Story),
		"director":    strings.TrimSpace(release.Director),
		"date":        strings.TrimSpace(release.ReleaseDate),
		"studio":      strings.TrimSpace(release.Studio),
		"performers":  release.Actresses,
		"tags":        release.Genres,
		"source_url":  strings.TrimSpace(release.ProductURL),
		"poster_path": posterPath,
	})
}

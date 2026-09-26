package web

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	jellyfinintegration "github.com/Net005/JAVBeacon/internal/jellyfin"
)

// markJellyfinLibraryChanged invalidates the Jellyfin plugin's cached
// LibrarySync revision so its next poll (or the "Force JAVBeacon sync"
// scheduled task) picks up the change instead of skipping it as a no-op.
// internal/stash has its own copy of this (it can't import internal/web),
// used for Stash-originated changes; this one covers changes made directly
// through the JAVBeacon API, such as saved filter set edits.
func (s *Server) markJellyfinLibraryChanged(ctx context.Context) {
	if e := s.store.SaveSettings(ctx, map[string]string{"jellyfin_library_revision": time.Now().UTC().Format(time.RFC3339Nano)}); e != nil {
		s.log.Warn("unable to mark Jellyfin library sync pending", "error", e)
	}
}

func (s *Server) jellyfinMatch(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Path  string `json:"path"`
		Query string `json:"query"`
	}
	if !s.decode(w, r, &input) {
		return
	}
	result, err := s.jellyfin.Match(r.Context(), input.Path, input.Query)
	if err != nil {
		s.problem(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.json(w, http.StatusOK, result)
}

func (s *Server) jellyfinSearch(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	rows, err := s.jellyfin.Search(r.Context(), r.URL.Query().Get("q"), limit)
	if err != nil {
		s.problem(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.json(w, http.StatusOK, map[string]any{"items": rows, "total": len(rows)})
}

func (s *Server) jellyfinMetadata(w http.ResponseWriter, r *http.Request) {
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

func (s *Server) jellyfinLibrarySync(w http.ResponseWriter, r *http.Request) {
	value, err := s.jellyfin.LibrarySync(r.Context())
	if err != nil {
		s.problem(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.json(w, http.StatusOK, value)
}

func (s *Server) jellyfinPlayback(w http.ResponseWriter, r *http.Request) {
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

func (s *Server) jellyfinActivity(w http.ResponseWriter, r *http.Request) {
	n, err := id(r)
	if err != nil {
		s.problem(w, http.StatusBadRequest, "invalid release id")
		return
	}
	value, err := s.jellyfin.Activity(r.Context(), n)
	if errors.Is(err, sql.ErrNoRows) {
		s.problem(w, http.StatusNotFound, "release not found")
		return
	}
	if err != nil {
		s.problem(w, http.StatusBadGateway, err.Error())
		return
	}
	s.json(w, http.StatusOK, value)
}

// jellyfinStashCover streams a release's linked StashApp scene screenshot,
// for use only when JAVBeacon itself has no cover image for that release
// (see internal/jellyfin's enrichFromStash and Metadata.StashScreenshotURL).
// The Stash base URL and API key never reach the caller; only the image
// bytes do.
func (s *Server) jellyfinStashCover(w http.ResponseWriter, r *http.Request) {
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

func (s *Server) jellyfinAddO(w http.ResponseWriter, r *http.Request) {
	n, err := id(r)
	if err != nil {
		s.problem(w, http.StatusBadRequest, "invalid release id")
		return
	}
	var input struct {
		OccurredAt time.Time `json:"occurred_at,omitempty"`
	}
	if r.ContentLength > 0 && !s.decode(w, r, &input) {
		return
	}
	value, err := s.jellyfin.AddO(r.Context(), n, input.OccurredAt)
	if errors.Is(err, sql.ErrNoRows) {
		s.problem(w, http.StatusNotFound, "release not found")
		return
	}
	if err != nil {
		s.problem(w, http.StatusBadGateway, err.Error())
		return
	}
	s.json(w, http.StatusOK, value)
}

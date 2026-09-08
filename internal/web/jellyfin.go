package web

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	jellyfinintegration "github.com/Net005/JAVBeacon/internal/jellyfin"
)

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

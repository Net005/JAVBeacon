package web

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/Net005/JAVBeacon/internal/domain"
)

func (s *Server) blacklistedReleases(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		rows, total, err := s.store.BlacklistedReleases(r.Context(), r.URL.Query().Get("search"), limit, offset)
		if err != nil {
			s.problem(w, 500, "Could not load blacklisted releases")
			return
		}
		s.json(w, 200, map[string]any{"items": rows, "total": total})
		return
	}
	var input struct {
		URL    string `json:"url"`
		Reason string `json:"reason"`
	}
	if !s.decode(w, r, &input) {
		return
	}
	if len(input.URL) > 16384 {
		s.problem(w, 400, "Download URL is too long")
		return
	}
	if _, err := domain.BlacklistURLKey(input.URL); err != nil {
		s.problem(w, 400, err.Error())
		return
	}
	if r.Method == http.MethodDelete {
		if err := s.store.DeleteBlacklistedRelease(r.Context(), input.URL); err != nil {
			s.problem(w, 500, "Could not remove blacklist entry")
			return
		}
		s.json(w, 200, map[string]bool{"removed": true})
		return
	}
	reason := strings.TrimSpace(input.Reason)
	if reason == "" {
		reason = "Manually blacklisted download URL"
	}
	if len(reason) > 2000 {
		s.problem(w, 400, "Blacklist reason is too long")
		return
	}
	transport := "http"
	if strings.HasPrefix(strings.ToLower(input.URL), "magnet:") {
		transport = "torrent"
	}
	warning := ""
	var err error
	if s.downloads != nil {
		warning, err = s.downloads.BlacklistSource(r.Context(), input.URL, reason)
	} else {
		err = s.store.SaveBlacklistedRelease(r.Context(), domain.BlacklistedRelease{URL: input.URL, Reason: reason, Transport: transport})
	}
	if err != nil {
		s.problem(w, 500, "Could not save blacklist entry")
		return
	}
	s.json(w, 201, map[string]any{"blacklisted": true, "warning": warning})
}

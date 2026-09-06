package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/Net005/JAVBeacon/internal/domain"
)

type stashHistoryReader interface {
	StashHistory(context.Context, string, time.Time, time.Time) ([]domain.StashHistoryItem, error)
	StashHistoryExport(context.Context) (domain.StashHistoryExport, error)
}

func parseHistoryBound(raw string) (time.Time, error) {
	if raw == "" {
		return time.Time{}, nil
	}
	if t, e := time.Parse(time.RFC3339, raw); e == nil {
		return t, nil
	}
	return time.Parse("2006-01-02", raw)
}

func (s *Server) stashHistory(w http.ResponseWriter, r *http.Request) {
	store, ok := s.store.(stashHistoryReader)
	if !ok {
		s.problem(w, 500, "history storage is unavailable")
		return
	}
	from, err := parseHistoryBound(r.URL.Query().Get("from"))
	if err != nil {
		s.problem(w, 400, "invalid from date")
		return
	}
	to, err := parseHistoryBound(r.URL.Query().Get("to"))
	if err != nil {
		s.problem(w, 400, "invalid to date")
		return
	}
	if !to.IsZero() && len(r.URL.Query().Get("to")) == 10 {
		to = to.AddDate(0, 0, 1)
	}
	items, err := store.StashHistory(r.Context(), r.URL.Query().Get("type"), from, to)
	if err != nil {
		s.problem(w, 500, err.Error())
		return
	}
	var plays, orgasms int
	var seconds float64
	for _, item := range items {
		plays += item.PlayCount
		orgasms += item.OrgasmCount
		seconds += item.PlaySeconds
	}
	s.json(w, 200, map[string]any{"items": items, "total": len(items), "play_count": plays, "orgasm_count": orgasms, "play_seconds": seconds})
}

func (s *Server) exportStashHistory(w http.ResponseWriter, r *http.Request) {
	store, ok := s.store.(stashHistoryReader)
	if !ok {
		s.problem(w, 500, "history storage is unavailable")
		return
	}
	data, err := store.StashHistoryExport(r.Context())
	if err != nil {
		s.problem(w, 500, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="javbeacon-stash-history-%s.json"`, time.Now().Format("2006-01-02")))
	_ = json.NewEncoder(w).Encode(data)
}

func (s *Server) syncStashHistory(w http.ResponseWriter, r *http.Request) {
	if err := s.stash.Start(r.Context()); err != nil {
		s.problem(w, http.StatusConflict, err.Error())
		return
	}
	s.json(w, http.StatusAccepted, s.stash.Status())
}
func (s *Server) reviewStashHistoryWriteback(w http.ResponseWriter, r *http.Request) {
	review, err := s.stash.ReviewHistoryWriteback(r.Context())
	if err != nil {
		s.problem(w, http.StatusBadGateway, err.Error())
		return
	}
	s.json(w, 200, review)
}
func (s *Server) applyStashHistoryWriteback(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Token string `json:"token"`
	}
	if !s.decode(w, r, &body) {
		return
	}
	if body.Token == "" {
		s.problem(w, 400, "review token is required")
		return
	}
	result, err := s.stash.ApplyHistoryWriteback(r.Context(), body.Token)
	if err != nil {
		s.problem(w, http.StatusConflict, err.Error())
		return
	}
	s.json(w, 200, result)
}

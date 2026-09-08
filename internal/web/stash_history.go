package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
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
	// Fetch only the selected calendar window. The response below paginates
	// its detail ledger, while compact daily totals let the browser render the
	// complete graph without receiving every scene/day record up front.
	items, err := store.StashHistory(r.Context(), "", from, to)
	if err != nil {
		s.problem(w, 500, err.Error())
		return
	}
	type dailyTotal struct {
		Date        string  `json:"date"`
		PlayCount   int     `json:"play_count"`
		OrgasmCount int     `json:"orgasm_count"`
		PlaySeconds float64 `json:"play_seconds"`
	}
	daily := map[string]*dailyTotal{}
	var plays, orgasms int
	var seconds float64
	for _, item := range items {
		plays += item.PlayCount
		orgasms += item.OrgasmCount
		seconds += item.PlaySeconds
		day := daily[item.Date]
		if day == nil {
			day = &dailyTotal{Date: item.Date}
			daily[item.Date] = day
		}
		day.PlayCount += item.PlayCount
		day.OrgasmCount += item.OrgasmCount
		day.PlaySeconds += item.PlaySeconds
	}
	kind := r.URL.Query().Get("type")
	if kind == "play" || kind == "orgasm" {
		filtered := items[:0]
		for _, item := range items {
			if (kind == "play" && item.PlayCount > 0) || (kind == "orgasm" && item.OrgasmCount > 0) {
				filtered = append(filtered, item)
			}
		}
		items = filtered
	}
	limit := 25
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if parsed, parseErr := strconv.Atoi(raw); parseErr != nil || parsed < 1 {
			s.problem(w, 400, "invalid history limit")
			return
		} else if parsed > 100 {
			limit = 100
		} else {
			limit = parsed
		}
	}
	offset := 0
	if raw := r.URL.Query().Get("offset"); raw != "" {
		if parsed, parseErr := strconv.Atoi(raw); parseErr != nil || parsed < 0 {
			s.problem(w, 400, "invalid history offset")
			return
		} else {
			offset = parsed
		}
	}
	total := len(items)
	if offset > total {
		offset = total
	}
	end := min(total, offset+limit)
	page := items[offset:end]
	days := make([]dailyTotal, 0, len(daily))
	for _, day := range daily {
		days = append(days, *day)
	}
	sort.Slice(days, func(i, j int) bool { return days[i].Date < days[j].Date })
	s.json(w, 200, map[string]any{"items": page, "days": days, "total": total, "next_offset": end, "has_more": end < total, "play_count": plays, "orgasm_count": orgasms, "play_seconds": seconds})
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
		Token          string   `json:"token"`
		SourceSceneIDs []string `json:"source_scene_ids"`
	}
	if !s.decode(w, r, &body) {
		return
	}
	if body.Token == "" {
		s.problem(w, 400, "review token is required")
		return
	}
	result, err := s.stash.ApplyHistoryWriteback(r.Context(), body.Token, body.SourceSceneIDs)
	if err != nil {
		s.problem(w, http.StatusConflict, err.Error())
		return
	}
	s.json(w, 200, result)
}

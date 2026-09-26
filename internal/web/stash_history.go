package web

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
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

type stashReleaseHistoryReader interface {
	StashHistoryForRelease(context.Context, int64) ([]domain.StashHistoryScene, []domain.StashHistoryEvent, error)
}

func (s *Server) releaseStashHistory(w http.ResponseWriter, r *http.Request) {
	releaseID, err := id(r)
	if err != nil {
		s.problem(w, http.StatusBadRequest, "invalid release id")
		return
	}
	store, ok := s.store.(stashReleaseHistoryReader)
	if !ok {
		s.problem(w, http.StatusInternalServerError, "history storage is unavailable")
		return
	}
	scenes, events, err := store.StashHistoryForRelease(r.Context(), releaseID)
	if err != nil {
		s.problem(w, http.StatusInternalServerError, err.Error())
		return
	}
	sort.Slice(events, func(i, j int) bool { return events[i].OccurredAt.After(events[j].OccurredAt) })
	var plays, orgasms int
	var seconds float64
	for _, event := range events {
		if event.Type == "play" {
			plays++
			seconds += event.DurationSeconds
		} else if event.Type == "orgasm" {
			orgasms++
		}
	}
	s.json(w, http.StatusOK, map[string]any{"release_id": releaseID, "scenes": scenes, "events": events, "play_count": plays, "orgasm_count": orgasms, "play_seconds": seconds})
}

func applyStashHistoryToRelease(release *domain.Release, events []domain.StashHistoryEvent) {
	if release == nil || len(events) == 0 {
		return
	}
	var plays, orgasms int
	var lastPlayed, lastOrgasm time.Time
	for _, event := range events {
		switch event.Type {
		case "play":
			plays++
			if event.OccurredAt.After(lastPlayed) {
				lastPlayed = event.OccurredAt
			}
		case "orgasm":
			orgasms++
			if event.OccurredAt.After(lastOrgasm) {
				lastOrgasm = event.OccurredAt
			}
		}
	}
	release.PlayCount = plays
	release.OCounter = orgasms
	if !lastPlayed.IsZero() {
		release.LastPlayedAt = lastPlayed.UTC().Format(time.RFC3339)
	}
	if !lastOrgasm.IsZero() {
		release.LastOCountAt = lastOrgasm.UTC().Format(time.RFC3339)
	}
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

// stashHistorySceneCover streams a StashApp scene's screenshot directly for
// Stash History entries that never matched a JAVBeacon release (so
// /covers/{release_id} isn't available) - filling in the "?" placeholder
// cover the history grid otherwise shows for those rows.
func (s *Server) stashHistorySceneCover(w http.ResponseWriter, r *http.Request) {
	sceneID := r.PathValue("sceneId")
	if sceneID == "" {
		s.problem(w, http.StatusBadRequest, "invalid scene id")
		return
	}
	resp, err := s.stash.FetchSceneScreenshot(r.Context(), sceneID)
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
		sort.SliceStable(items, func(i, j int) bool {
			left, right := items[i].LatestPlayAt, items[j].LatestPlayAt
			if kind == "orgasm" {
				left, right = items[i].LatestOrgasmAt, items[j].LatestOrgasmAt
			}
			return left.After(right)
		})
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

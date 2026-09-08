package stash

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Net005/JAVBeacon/internal/domain"
)

type stashHistoryStore interface {
	StashHistoryScenes(context.Context) ([]domain.StashHistoryScene, error)
	StashHistoryEventsForScene(context.Context, string) ([]domain.StashHistoryEvent, error)
}

type HistoryReviewItem struct {
	SourceSceneID      string      `json:"source_scene_id"`
	TargetSceneID      string      `json:"target_scene_id,omitempty"`
	ReleaseID          int64       `json:"release_id,omitempty"`
	VideoID            string      `json:"video_id,omitempty"`
	Title              string      `json:"title"`
	FilePath           string      `json:"file_path,omitempty"`
	MatchMethod        string      `json:"match_method,omitempty"`
	PlayTimes          []time.Time `json:"play_times,omitempty"`
	OrgasmTimes        []time.Time `json:"orgasm_times,omitempty"`
	PlayDurationDelta  float64     `json:"play_duration_delta,omitempty"`
	SourcePlayDuration float64     `json:"source_play_duration,omitempty"`
	Status             string      `json:"status"`
	Reason             string      `json:"reason,omitempty"`
}

type HistoryReview struct {
	Token     string              `json:"token"`
	CreatedAt time.Time           `json:"created_at"`
	Items     []HistoryReviewItem `json:"items"`
	Matched   int                 `json:"matched"`
	Unmatched int                 `json:"unmatched"`
	Changes   int                 `json:"changes"`
}

type HistoryApplyResult struct {
	Updated int      `json:"updated"`
	Plays   int      `json:"plays"`
	Orgasms int      `json:"orgasms"`
	Failed  int      `json:"failed"`
	Errors  []string `json:"errors,omitempty"`
}

func normalizeHistoryURL(raw string) string {
	return strings.TrimRight(strings.ToLower(strings.TrimSpace(raw)), "/")
}
func historyFileKey(raw string) string { return strings.ToLower(strings.TrimSpace(filepath.Base(raw))) }
func historyEventKey(value time.Time) string {
	return value.UTC().Truncate(time.Second).Format(time.RFC3339)
}

func (s *Service) ReviewHistoryWriteback(ctx context.Context) (HistoryReview, error) {
	archive, ok := s.store.(stashHistoryStore)
	if !ok {
		return HistoryReview{}, errors.New("history storage is unavailable")
	}
	settings, err := s.store.Settings(ctx)
	if err != nil {
		return HistoryReview{}, err
	}
	base, key := strings.TrimRight(strings.TrimSpace(settings["stash_base_url"]), "/"), settings["stash_api_key"]
	if base == "" {
		return HistoryReview{}, errors.New("StashApp Base URL is not configured")
	}
	remote, err := s.fetchPlaybackStats(ctx, base+"/graphql", key)
	if err != nil {
		return HistoryReview{}, err
	}
	for _, scene := range remote {
		if !scene.HistoryAvailable {
			return HistoryReview{}, errors.New("this StashApp version does not expose timestamped play_history and o_history; safe write-back review is unavailable")
		}
		break
	}
	scenes, err := archive.StashHistoryScenes(ctx)
	if err != nil {
		return HistoryReview{}, err
	}
	byURL, byID, byFile := map[string]string{}, map[string]string{}, map[string]string{}
	for id, scene := range remote {
		for _, u := range scene.URLs {
			if k := normalizeHistoryURL(u); k != "" && strings.Contains(k, "javlibrary") {
				byURL[k] = id
			}
		}
		if k := canonical(scene.Code); k != "" {
			byID[k] = id
		}
		if k := historyFileKey(scene.FilePath); k != "" {
			byFile[k] = id
		}
	}
	var review HistoryReview
	review.CreatedAt = time.Now().UTC()
	token := make([]byte, 16)
	if _, err = rand.Read(token); err != nil {
		return review, err
	}
	review.Token = hex.EncodeToString(token)
	for _, scene := range scenes {
		item := HistoryReviewItem{SourceSceneID: scene.StashSceneID, ReleaseID: scene.ReleaseID, VideoID: scene.VideoID, Title: scene.Title, FilePath: scene.FilePath, SourcePlayDuration: scene.TotalPlaySeconds, Status: "unmatched"}
		if k := normalizeHistoryURL(scene.JavLibraryURL); k != "" {
			item.TargetSceneID = byURL[k]
			if item.TargetSceneID != "" {
				item.MatchMethod = "JavLibrary URL"
			}
		}
		if item.TargetSceneID == "" {
			if k := canonical(scene.VideoID); k != "" {
				item.TargetSceneID = byID[k]
				if item.TargetSceneID != "" {
					item.MatchMethod = "Release ID"
				}
			}
		}
		if item.TargetSceneID == "" {
			if k := historyFileKey(scene.FilePath); k != "" {
				item.TargetSceneID = byFile[k]
				if item.TargetSceneID != "" {
					item.MatchMethod = "Filename (case-insensitive)"
				}
			}
		}
		if item.TargetSceneID == "" {
			item.Reason = "No StashApp scene matched by JavLibrary URL, release ID, or filename"
			review.Unmatched++
			review.Items = append(review.Items, item)
			continue
		}
		events, e := archive.StashHistoryEventsForScene(ctx, scene.StashSceneID)
		if e != nil {
			return review, e
		}
		target := remote[item.TargetSceneID]
		playSet, oSet := map[string]bool{}, map[string]bool{}
		for _, v := range target.PlayHistory {
			if parsed, e := time.Parse(time.RFC3339, v); e == nil {
				playSet[historyEventKey(parsed)] = true
			}
		}
		for _, v := range target.OHistory {
			if parsed, e := time.Parse(time.RFC3339, v); e == nil {
				oSet[historyEventKey(parsed)] = true
			}
		}
		for _, event := range events {
			raw := historyEventKey(event.OccurredAt)
			if event.Type == "play" && !playSet[raw] {
				item.PlayTimes = append(item.PlayTimes, event.OccurredAt.UTC())
				playSet[raw] = true
			}
			if event.Type == "orgasm" && !oSet[raw] {
				item.OrgasmTimes = append(item.OrgasmTimes, event.OccurredAt.UTC())
				oSet[raw] = true
			}
		}
		if scene.TotalPlaySeconds > target.PlayDuration {
			item.PlayDurationDelta = scene.TotalPlaySeconds - target.PlayDuration
		}
		item.Status = "matched"
		review.Matched++
		if len(item.PlayTimes) > 0 || len(item.OrgasmTimes) > 0 || item.PlayDurationDelta > 0 {
			item.Status = "change"
			parts := make([]string, 0, 3)
			if len(item.PlayTimes) > 0 {
				parts = append(parts, fmt.Sprintf("%d new play event(s)", len(item.PlayTimes)))
			}
			if len(item.OrgasmTimes) > 0 {
				parts = append(parts, fmt.Sprintf("%d new orgasm event(s)", len(item.OrgasmTimes)))
			}
			if item.PlayDurationDelta > 0 {
				parts = append(parts, fmt.Sprintf("%.0f seconds additional playtime", item.PlayDurationDelta))
			}
			item.Reason = strings.Join(parts, " · ")
			review.Changes++
		}
		review.Items = append(review.Items, item)
	}
	sort.SliceStable(review.Items, func(i, j int) bool { return review.Items[i].Title < review.Items[j].Title })
	s.historyReviewMu.Lock()
	s.historyReviews = map[string]HistoryReview{review.Token: review}
	s.historyReviewMu.Unlock()
	s.log.Info("Stash history write-back review created", "matched", review.Matched, "unmatched", review.Unmatched, "changes", review.Changes)
	return review, nil
}

func quotedTimes(values []time.Time) string {
	parts := make([]string, len(values))
	for i, v := range values {
		parts[i] = `"` + v.UTC().Format(time.RFC3339) + `"`
	}
	return strings.Join(parts, ",")
}

func (s *Service) historyMutation(ctx context.Context, base, key, query string) error {
	var payload struct {
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := s.graphql(ctx, base, key, query, &payload); err != nil {
		return err
	}
	if len(payload.Errors) > 0 {
		return errors.New(payload.Errors[0].Message)
	}
	return nil
}

func (s *Service) ApplyHistoryWriteback(ctx context.Context, token string, selectedSceneIDs ...[]string) (HistoryApplyResult, error) {
	s.historyReviewMu.Lock()
	review, ok := s.historyReviews[token]
	if ok {
		delete(s.historyReviews, token)
	}
	s.historyReviewMu.Unlock()
	if !ok {
		return HistoryApplyResult{}, errors.New("history review expired; create a new review")
	}
	if time.Since(review.CreatedAt) > 30*time.Minute {
		return HistoryApplyResult{}, errors.New("history review expired; create a new review")
	}
	settings, err := s.store.Settings(ctx)
	if err != nil {
		return HistoryApplyResult{}, err
	}
	base, key := strings.TrimRight(strings.TrimSpace(settings["stash_base_url"]), "/"), settings["stash_api_key"]
	remote, err := s.fetchPlaybackStats(ctx, base+"/graphql", key)
	if err != nil {
		return HistoryApplyResult{}, fmt.Errorf("refresh Stash history before write-back: %w", err)
	}
	selected := map[string]bool{}
	selectedFilter := len(selectedSceneIDs) > 0
	if len(selectedSceneIDs) > 0 {
		for _, id := range selectedSceneIDs[0] {
			selected[id] = true
		}
	}
	playSets, orgasmSets := map[string]map[string]bool{}, map[string]map[string]bool{}
	playDurations := map[string]float64{}
	for id, scene := range remote {
		playSets[id], orgasmSets[id] = map[string]bool{}, map[string]bool{}
		for _, raw := range scene.PlayHistory {
			if parsed, parseErr := time.Parse(time.RFC3339, raw); parseErr == nil {
				playSets[id][historyEventKey(parsed)] = true
			}
		}
		for _, raw := range scene.OHistory {
			if parsed, parseErr := time.Parse(time.RFC3339, raw); parseErr == nil {
				orgasmSets[id][historyEventKey(parsed)] = true
			}
		}
		playDurations[id] = scene.PlayDuration
	}
	var out HistoryApplyResult
	for _, item := range review.Items {
		if item.Status != "change" || (selectedFilter && !selected[item.SourceSceneID]) {
			continue
		}
		if playSets[item.TargetSceneID] == nil {
			playSets[item.TargetSceneID] = map[string]bool{}
		}
		if orgasmSets[item.TargetSceneID] == nil {
			orgasmSets[item.TargetSceneID] = map[string]bool{}
		}
		plays, orgasms := make([]time.Time, 0, len(item.PlayTimes)), make([]time.Time, 0, len(item.OrgasmTimes))
		for _, value := range item.PlayTimes {
			key := historyEventKey(value)
			if !playSets[item.TargetSceneID][key] {
				playSets[item.TargetSceneID][key] = true
				plays = append(plays, value)
			}
		}
		for _, value := range item.OrgasmTimes {
			key := historyEventKey(value)
			if !orgasmSets[item.TargetSceneID][key] {
				orgasmSets[item.TargetSceneID][key] = true
				orgasms = append(orgasms, value)
			}
		}
		if len(plays) > 0 {
			q := fmt.Sprintf(`mutation { sceneAddPlay(id:"%s",times:[%s]) { count } }`, escapeGraphQL(item.TargetSceneID), quotedTimes(plays))
			if e := s.historyMutation(ctx, base, key, q); e != nil {
				out.Failed++
				out.Errors = append(out.Errors, item.Title+": "+e.Error())
				continue
			}
			out.Plays += len(plays)
		}
		if len(orgasms) > 0 {
			q := fmt.Sprintf(`mutation { sceneAddO(id:"%s",times:[%s]) { count } }`, escapeGraphQL(item.TargetSceneID), quotedTimes(orgasms))
			if e := s.historyMutation(ctx, base, key, q); e != nil {
				out.Failed++
				out.Errors = append(out.Errors, item.Title+": "+e.Error())
				continue
			}
			out.Orgasms += len(orgasms)
		}
		playDurationDelta := item.SourcePlayDuration - playDurations[item.TargetSceneID]
		if playDurationDelta > 0 {
			q := fmt.Sprintf(`mutation { sceneSaveActivity(id:"%s",playDuration:%.3f) }`, escapeGraphQL(item.TargetSceneID), playDurationDelta)
			if e := s.historyMutation(ctx, base, key, q); e != nil {
				out.Failed++
				out.Errors = append(out.Errors, item.Title+": "+e.Error())
				continue
			}
			playDurations[item.TargetSceneID] += playDurationDelta
		}
		if len(plays) > 0 || len(orgasms) > 0 || playDurationDelta > 0 {
			out.Updated++
		}
	}
	s.log.Info("Stash history write-back completed", "updated", out.Updated, "plays", out.Plays, "orgasms", out.Orgasms, "failed", out.Failed)
	return out, nil
}

func (s *Service) HistoryWritebackSchedule(ctx context.Context) {
	last := time.Now()
	for {
		settings, _ := s.store.Settings(ctx)
		interval, err := domain.ParseScheduleDuration(settings["stash_history_writeback_interval"])
		if err != nil || interval < time.Minute {
			interval = 24 * time.Hour
		}
		remaining := interval - time.Since(last)
		if remaining <= 0 {
			last = time.Now()
			if settings["stash_history_writeback_enabled"] == "true" && strings.TrimSpace(settings["stash_base_url"]) != "" {
				review, e := s.ReviewHistoryWriteback(ctx)
				if e != nil {
					s.log.Error("scheduled Stash history write-back review failed", "error", e)
				} else if review.Changes > 0 {
					result, e := s.ApplyHistoryWriteback(ctx, review.Token)
					if e != nil {
						s.log.Error("scheduled Stash history write-back failed", "error", e)
					} else {
						s.log.Info("scheduled Stash history write-back completed", "updated", result.Updated, "failed", result.Failed)
					}
				}
			}
			remaining = interval
		}
		s.mu.Lock()
		if s.scheduleNextAttempt == nil {
			s.scheduleNextAttempt = map[string]time.Time{}
		}
		s.scheduleNextAttempt["history_writeback"] = time.Now().Add(remaining)
		s.mu.Unlock()
		sleep := remaining
		if sleep > scheduleMaxSleepChunk {
			sleep = scheduleMaxSleepChunk
		}
		timer := time.NewTimer(sleep)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

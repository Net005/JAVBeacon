package stash

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"
)

// SiloCompletedPlay identifies one finalized Silo playback attempt.
type SiloCompletedPlay struct {
	SessionID string    `json:"session_id"`
	EndedAt   time.Time `json:"ended_at"`
}

type SiloBackfillResult struct {
	Added          int    `json:"added"`
	AlreadyPresent int    `json:"already_present"`
	Skipped        int    `json:"skipped"`
	Reason         string `json:"reason,omitempty"`
}

// BackfillSiloPlays writes only when all existing Stash plays correspond to
// supplied Silo sessions. An unrelated or manually dated play makes the scene
// ambiguous, so it is skipped. A partial write is retryable because Stash's
// timestamped history is reread before each batch.
func (s *Service) BackfillSiloPlays(ctx context.Context, sceneID string, plays []SiloCompletedPlay) (SiloBackfillResult, error) {
	s.backfillMu.Lock()
	defer s.backfillMu.Unlock()
	s.playMu.Lock()
	defer s.playMu.Unlock()
	if sceneID == "" || len(plays) == 0 || len(plays) > 200 {
		return SiloBackfillResult{}, errors.New("scene_id and 1-200 completed plays are required")
	}
	candidates := make(map[string]time.Time, len(plays))
	seenSessions := make(map[string]bool, len(plays))
	for _, play := range plays {
		if play.SessionID == "" || play.EndedAt.IsZero() || play.EndedAt.After(time.Now().Add(time.Minute)) {
			return SiloBackfillResult{}, errors.New("each play needs a valid session_id and past ended_at")
		}
		if seenSessions[play.SessionID] {
			continue
		}
		seenSessions[play.SessionID] = true
		at := play.EndedAt.UTC().Truncate(time.Second)
		candidates[at.Format(time.RFC3339)] = at
	}
	base, key, err := s.stashConfig(ctx)
	if err != nil {
		return SiloBackfillResult{}, err
	}
	query := fmt.Sprintf(`query { findScene(id: "%s") { id play_count play_history } }`, escapeGraphQL(sceneID))
	var payload struct {
		Data struct {
			Scene *struct {
				ID          string   `json:"id"`
				PlayCount   int      `json:"play_count"`
				PlayHistory []string `json:"play_history"`
			} `json:"findScene"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := s.graphql(ctx, base, key, query, &payload); err != nil {
		return SiloBackfillResult{}, err
	}
	if len(payload.Errors) > 0 {
		return SiloBackfillResult{}, errors.New(payload.Errors[0].Message)
	}
	if payload.Data.Scene == nil || payload.Data.Scene.ID != sceneID {
		return SiloBackfillResult{}, errors.New("Stash scene was not found")
	}
	remote := payload.Data.Scene
	result := SiloBackfillResult{}
	if remote.PlayCount != len(remote.PlayHistory) {
		result.Skipped, result.Reason = len(candidates), "Stash has plays without complete timestamped history"
		return result, nil
	}
	existing := make(map[string]bool, len(remote.PlayHistory))
	for _, raw := range remote.PlayHistory {
		at, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			result.Skipped, result.Reason = len(candidates), "Stash has an invalid play timestamp"
			return result, nil
		}
		stamp := at.UTC().Truncate(time.Second).Format(time.RFC3339)
		if _, known := candidates[stamp]; !known {
			result.Skipped, result.Reason = len(candidates), "Stash already has a play outside these Silo sessions"
			return result, nil
		}
		existing[stamp] = true
	}
	stamps := make([]string, 0, len(candidates))
	for stamp := range candidates {
		stamps = append(stamps, stamp)
	}
	sort.Strings(stamps)
	for _, stamp := range stamps {
		if existing[stamp] {
			result.AlreadyPresent++
			continue
		}
		if _, err := s.addHistory(ctx, "sceneAddPlay", sceneID, candidates[stamp]); err != nil {
			return result, fmt.Errorf("backfill Stash scene %s at %s: %w", sceneID, stamp, err)
		}
		result.Added++
	}
	return result, nil
}

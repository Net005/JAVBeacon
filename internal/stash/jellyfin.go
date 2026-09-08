package stash

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// JellyfinActivity is the small, stable activity contract exposed to the
// Jellyfin integration. It deliberately hides Stash's GraphQL schema.
type JellyfinActivity struct {
	StashSceneID string  `json:"stash_scene_id"`
	OCount       int     `json:"o_count"`
	PlayCount    int     `json:"play_count"`
	PlayDuration float64 `json:"play_duration_seconds"`
	ResumeTime   float64 `json:"resume_time_seconds"`
	LastPlayedAt string  `json:"last_played_at,omitempty"`
	LastOCountAt string  `json:"last_o_count_at,omitempty"`
}

func (s *Service) jellyfinConfig(ctx context.Context) (string, string, error) {
	settings, err := s.store.Settings(ctx)
	if err != nil {
		return "", "", err
	}
	base := strings.TrimRight(strings.TrimSpace(settings["stash_base_url"]), "/")
	if base == "" {
		return "", "", errors.New("StashApp Base URL is not configured")
	}
	return base, strings.TrimSpace(settings["stash_api_key"]), nil
}

// SaveJellyfinActivity forwards a checkpoint to StashApp. playDuration is a
// delta, while resumeTime is the current absolute media position.
func (s *Service) SaveJellyfinActivity(ctx context.Context, sceneID string, resumeTime, playDuration float64) error {
	base, key, err := s.jellyfinConfig(ctx)
	if err != nil {
		return err
	}
	query := fmt.Sprintf(`mutation { sceneSaveActivity(id: "%s", resume_time: %.3f, playDuration: %.3f) }`, escapeGraphQL(sceneID), maxFloat(resumeTime, 0), maxFloat(playDuration, 0))
	var payload struct {
		Data struct {
			Saved bool `json:"sceneSaveActivity"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err = s.graphql(ctx, base, key, query, &payload); err != nil {
		return err
	}
	if len(payload.Errors) > 0 {
		return errors.New(payload.Errors[0].Message)
	}
	if !payload.Data.Saved {
		return errors.New("StashApp rejected scene activity")
	}
	return nil
}

func (s *Service) AddJellyfinPlay(ctx context.Context, sceneID string, at time.Time) (int, error) {
	return s.addJellyfinHistory(ctx, "sceneAddPlay", sceneID, at)
}

func (s *Service) AddJellyfinO(ctx context.Context, sceneID string, at time.Time) (int, error) {
	return s.addJellyfinHistory(ctx, "sceneAddO", sceneID, at)
}

func (s *Service) addJellyfinHistory(ctx context.Context, mutation, sceneID string, at time.Time) (int, error) {
	base, key, err := s.jellyfinConfig(ctx)
	if err != nil {
		return 0, err
	}
	if at.IsZero() {
		at = time.Now().UTC()
	}
	query := fmt.Sprintf(`mutation { %s(id: "%s", times: ["%s"]) { count history } }`, mutation, escapeGraphQL(sceneID), at.UTC().Format(time.RFC3339))
	var payload struct {
		Data map[string]struct {
			Count int `json:"count"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err = s.graphql(ctx, base, key, query, &payload); err != nil {
		return 0, err
	}
	if len(payload.Errors) > 0 {
		return 0, errors.New(payload.Errors[0].Message)
	}
	return payload.Data[mutation].Count, nil
}

func (s *Service) JellyfinActivity(ctx context.Context, sceneID string) (JellyfinActivity, error) {
	base, key, err := s.jellyfinConfig(ctx)
	if err != nil {
		return JellyfinActivity{}, err
	}
	query := fmt.Sprintf(`query { findScene(id: "%s") { id o_counter play_count play_duration resume_time last_played_at o_history } }`, escapeGraphQL(sceneID))
	var payload struct {
		Data struct {
			Scene *struct {
				ID           string   `json:"id"`
				OCount       int      `json:"o_counter"`
				PlayCount    int      `json:"play_count"`
				PlayDuration float64  `json:"play_duration"`
				ResumeTime   float64  `json:"resume_time"`
				LastPlayedAt string   `json:"last_played_at"`
				OHistory     []string `json:"o_history"`
			} `json:"findScene"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err = s.graphql(ctx, base, key, query, &payload); err != nil {
		return JellyfinActivity{}, err
	}
	if len(payload.Errors) > 0 {
		return JellyfinActivity{}, errors.New(payload.Errors[0].Message)
	}
	if payload.Data.Scene == nil {
		return JellyfinActivity{}, errors.New("StashApp scene not found")
	}
	x := payload.Data.Scene
	out := JellyfinActivity{StashSceneID: x.ID, OCount: x.OCount, PlayCount: x.PlayCount, PlayDuration: x.PlayDuration, ResumeTime: x.ResumeTime, LastPlayedAt: x.LastPlayedAt}
	for _, value := range x.OHistory {
		if value > out.LastOCountAt {
			out.LastOCountAt = value
		}
	}
	return out, nil
}

func maxFloat(value, minimum float64) float64 {
	if value < minimum {
		return minimum
	}
	return value
}

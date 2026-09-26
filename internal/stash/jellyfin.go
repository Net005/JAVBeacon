package stash

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
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

// StashSceneMetadata is the small, stable metadata contract exposed to the
// Jellyfin integration for gap-filling a JAVBeacon release's own scraped
// metadata (see internal/jellyfin's metadata()). Every field is left empty
// when StashApp itself doesn't have it, never guessed.
type StashSceneMetadata struct {
	Title         string
	Details       string
	Studio        string
	Performers    []string
	Tags          []string
	ScreenshotURL string
}

// StashSceneMetadata fetches a scene's title/details/studio/performers/tags
// and screenshot directly from StashApp, for use only as a fallback when
// JAVBeacon's own scraped release metadata or images are incomplete - it
// never overwrites data JAVBeacon already has.
func (s *Service) StashSceneMetadata(ctx context.Context, sceneID string) (StashSceneMetadata, error) {
	base, key, err := s.jellyfinConfig(ctx)
	if err != nil {
		return StashSceneMetadata{}, err
	}
	query := fmt.Sprintf(`query { findScene(id: "%s") { title details studio { name } performers { name } tags { name } paths { screenshot } } }`, escapeGraphQL(sceneID))
	var payload struct {
		Data struct {
			Scene *struct {
				Title      string `json:"title"`
				Details    string `json:"details"`
				Studio     *struct {
					Name string `json:"name"`
				} `json:"studio"`
				Performers []struct {
					Name string `json:"name"`
				} `json:"performers"`
				Tags []struct {
					Name string `json:"name"`
				} `json:"tags"`
				Paths struct {
					Screenshot string `json:"screenshot"`
				} `json:"paths"`
			} `json:"findScene"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err = s.graphql(ctx, base, key, query, &payload); err != nil {
		return StashSceneMetadata{}, err
	}
	if len(payload.Errors) > 0 {
		return StashSceneMetadata{}, errors.New(payload.Errors[0].Message)
	}
	if payload.Data.Scene == nil {
		return StashSceneMetadata{}, errors.New("StashApp scene not found")
	}
	x := payload.Data.Scene
	out := StashSceneMetadata{Title: x.Title, Details: x.Details, ScreenshotURL: x.Paths.Screenshot}
	if x.Studio != nil {
		out.Studio = x.Studio.Name
	}
	for _, p := range x.Performers {
		out.Performers = append(out.Performers, p.Name)
	}
	for _, t := range x.Tags {
		out.Tags = append(out.Tags, t.Name)
	}
	return out, nil
}

// FetchSceneScreenshot resolves sceneID's screenshot URL from StashApp and
// fetches it with the same authenticated client used for every other Stash
// request, so a Stash instance that requires its ApiKey header for image
// requests (not only GraphQL) still works. The caller is responsible for
// closing the returned response body.
func (s *Service) FetchSceneScreenshot(ctx context.Context, sceneID string) (*http.Response, error) {
	base, key, err := s.jellyfinConfig(ctx)
	if err != nil {
		return nil, err
	}
	meta, err := s.StashSceneMetadata(ctx, sceneID)
	if err != nil {
		return nil, err
	}
	if meta.ScreenshotURL == "" {
		return nil, errors.New("StashApp scene has no screenshot")
	}
	target := meta.ScreenshotURL
	if parsed, err := url.Parse(target); err == nil && !parsed.IsAbs() {
		target = strings.TrimRight(base, "/") + "/" + strings.TrimLeft(target, "/")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	if key != "" {
		req.Header.Set("ApiKey", key)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		resp.Body.Close()
		return nil, fmt.Errorf("StashApp returned HTTP %d for scene screenshot", resp.StatusCode)
	}
	return resp, nil
}

func maxFloat(value, minimum float64) float64 {
	if value < minimum {
		return minimum
	}
	return value
}

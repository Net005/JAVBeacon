package stash

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// SiloChangedScene identifies local Stash metadata changed after a cursor.
type SiloChangedScene struct {
	ID        string    `json:"stash_scene_id"`
	Code      string    `json:"code,omitempty"`
	Title     string    `json:"title,omitempty"`
	Path      string    `json:"path,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

// SiloChangedScenes pages newest-first so a normal poll reads only the first
// page. Reaching the safety bound returns an error rather than losing changes.
func (s *Service) SiloChangedScenes(ctx context.Context, since time.Time) ([]SiloChangedScene, error) {
	base, key, err := s.stashConfig(ctx)
	if err != nil {
		return nil, err
	}
	out := []SiloChangedScene{}
	for page := 1; page <= 100; page++ {
		gql := fmt.Sprintf(`query JAVBeaconSiloChanged { findScenes(filter: { page: %d, per_page: 100, sort: "updated_at", direction: DESC }) { scenes { id code title updated_at files { path } } } }`, page)
		var payload struct {
			Data struct {
				FindScenes struct {
					Scenes []struct {
						ID        string `json:"id"`
						Code      string `json:"code"`
						Title     string `json:"title"`
						UpdatedAt string `json:"updated_at"`
						Files     []struct {
							Path string `json:"path"`
						} `json:"files"`
					} `json:"scenes"`
				} `json:"findScenes"`
			} `json:"data"`
			Errors []struct {
				Message string `json:"message"`
			} `json:"errors"`
		}
		if err := s.graphql(ctx, base, key, gql, &payload); err != nil {
			return nil, err
		}
		if len(payload.Errors) > 0 {
			return nil, fmt.Errorf("StashApp changed scenes: %s", payload.Errors[0].Message)
		}
		scenes := payload.Data.FindScenes.Scenes
		for _, scene := range scenes {
			updated, err := time.Parse(time.RFC3339Nano, scene.UpdatedAt)
			if err != nil {
				return nil, fmt.Errorf("StashApp scene %s invalid updated_at: %w", scene.ID, err)
			}
			if !updated.After(since) {
				return out, nil
			}
			for _, file := range scene.Files {
				if strings.TrimSpace(file.Path) != "" {
					out = append(out, SiloChangedScene{ID: scene.ID, Code: scene.Code, Title: scene.Title, Path: file.Path, UpdatedAt: updated})
					break
				}
			}
		}
		if len(scenes) < 100 {
			return out, nil
		}
	}
	return nil, fmt.Errorf("StashApp changed-scene feed exceeded 100 pages; cursor retained for retry")
}

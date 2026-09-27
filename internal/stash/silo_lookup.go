package stash

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
)

// SiloScene is the Stash-owned metadata needed when a media file has no
// JAVBeacon release. Search verifies exact code or filename-stem identity.
type SiloScene struct {
	ID            string
	Code          string
	Title         string
	Details       string
	Date          string
	Studio        string
	Performers    []StashPerformer
	Tags          []string
	ScreenshotURL string
}

func (s *Service) SearchSiloScenes(ctx context.Context, query string) ([]SiloScene, error) {
	base, key, err := s.stashConfig(ctx)
	if err != nil {
		return nil, err
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, nil
	}
	gql := fmt.Sprintf(`query { findScenes(filter: { q: %s, per_page: 25 }) { scenes { id title code files { path } } } }`, strconv.Quote(query))
	var payload struct {
		Data struct {
			FindScenes struct {
				Scenes []struct {
					ID, Title, Code string
					Files           []struct{ Path string }
				}
			} `json:"findScenes"`
		} `json:"data"`
		Errors []struct{ Message string } `json:"errors"`
	}
	if err := s.graphql(ctx, base, key, gql, &payload); err != nil {
		return nil, err
	}
	if len(payload.Errors) > 0 {
		return nil, fmt.Errorf("StashApp scene search: %s", payload.Errors[0].Message)
	}
	out := make([]SiloScene, 0)
	for _, scene := range payload.Data.FindScenes.Scenes {
		code := strings.TrimSpace(scene.Code)
		matched := strings.EqualFold(code, query)
		for _, file := range scene.Files {
			stem := strings.TrimSuffix(filepath.Base(file.Path), filepath.Ext(file.Path))
			if strings.EqualFold(stem, query) {
				matched = true
				// Silo scores against its filename-derived title. Keep the
				// exact file stem even if Stash's scene code differs.
				code = stem
				break
			}
		}
		if matched {
			out = append(out, SiloScene{ID: scene.ID, Code: code, Title: scene.Title})
		}
	}
	return out, nil
}

func (s *Service) SiloSceneByID(ctx context.Context, sceneID string) (SiloScene, error) {
	base, key, err := s.stashConfig(ctx)
	if err != nil {
		return SiloScene{}, err
	}
	gql := fmt.Sprintf(`query { findScene(id: %s) { id title code details date studio { name } performers { id name image_path birthdate } tags { name } files { path } paths { screenshot } } }`, strconv.Quote(sceneID))
	var payload struct {
		Data struct {
			Scene *struct {
				ID, Title, Code, Details, Date string
				Studio                         *struct{ Name string }
				Performers                     []struct {
					ID        string `json:"id"`
					Name      string `json:"name"`
					ImagePath string `json:"image_path"`
					Birthdate string `json:"birthdate"`
				} `json:"performers"`
				Tags  []struct{ Name string }
				Files []struct{ Path string }
				Paths struct{ Screenshot string }
			} `json:"findScene"`
		} `json:"data"`
		Errors []struct{ Message string } `json:"errors"`
	}
	if err := s.graphql(ctx, base, key, gql, &payload); err != nil {
		return SiloScene{}, err
	}
	if len(payload.Errors) > 0 {
		return SiloScene{}, fmt.Errorf("StashApp scene metadata: %s", payload.Errors[0].Message)
	}
	x := payload.Data.Scene
	if x == nil {
		return SiloScene{}, fmt.Errorf("StashApp scene %s not found", sceneID)
	}
	code := strings.TrimSpace(x.Code)
	if len(x.Files) > 0 {
		// The Silo item is identified by its media file. Preserve that
		// identity even when Stash's manually edited code differs.
		code = strings.TrimSuffix(filepath.Base(x.Files[0].Path), filepath.Ext(x.Files[0].Path))
	}
	out := SiloScene{ID: x.ID, Code: code, Title: x.Title, Details: x.Details, Date: x.Date, ScreenshotURL: x.Paths.Screenshot}
	if x.Studio != nil {
		out.Studio = x.Studio.Name
	}
	for _, p := range x.Performers {
		out.Performers = append(out.Performers, StashPerformer{ID: p.ID, Name: p.Name, ImagePath: p.ImagePath, Birthdate: p.Birthdate})
	}
	for _, t := range x.Tags {
		out.Tags = append(out.Tags, t.Name)
	}
	return out, nil
}

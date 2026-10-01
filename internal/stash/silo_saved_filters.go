package stash

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// SiloSavedFilter contains ordered local scene files returned by a Stash
// SCENES saved filter. Paths let Silo use exact local identity, including
// scenes with no JAVBeacon release or a nonstandard filename.
type SiloSavedFilter struct {
	ID    string                `json:"id"`
	Name  string                `json:"name"`
	Items []SiloSavedFilterItem `json:"items"`
}
type SiloSavedFilterItem struct {
	SceneID string `json:"stash_scene_id"`
	Path    string `json:"path"`
	Code    string `json:"code,omitempty"`
	Title   string `json:"title,omitempty"`
}

type stashSavedFilter struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	FindFilter struct {
		Q         string `json:"q"`
		Sort      string `json:"sort"`
		Direction string `json:"direction"`
	} `json:"find_filter"`
	ObjectFilter map[string]any `json:"object_filter"`
}

func (s *Service) savedFilterGraphQL(ctx context.Context, base, key, query string, variables any, target any) error {
	body, err := json.Marshal(map[string]any{"query": query, "variables": variables})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/graphql", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("ApiKey", key)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("StashApp returned HTTP %d", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(target)
}

// SiloSavedFilters resolves only selected Stash scene filters. An empty
// selection disables this optional import and avoids a Stash GraphQL scan.
func (s *Service) SiloSavedFilters(ctx context.Context, selection string) ([]SiloSavedFilter, error) {
	if strings.TrimSpace(selection) == "" {
		return []SiloSavedFilter{}, nil
	}
	base, key, err := s.stashConfig(ctx)
	if err != nil {
		return nil, err
	}
	var list struct {
		Data struct {
			Filters []stashSavedFilter `json:"findSavedFilters"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err = s.savedFilterGraphQL(ctx, base, key, `query { findSavedFilters(mode: SCENES) { id name find_filter { q sort direction } object_filter } }`, nil, &list); err != nil {
		return nil, err
	}
	if len(list.Errors) > 0 {
		return nil, fmt.Errorf("StashApp saved filters: %s", list.Errors[0].Message)
	}
	selected := map[string]bool{}
	found := map[string]bool{}
	for _, token := range strings.FieldsFunc(selection, func(r rune) bool { return r == ',' || r == '\n' }) {
		token = strings.ToLower(strings.TrimSpace(token))
		if token != "" {
			selected[token] = true
		}
	}
	out := make([]SiloSavedFilter, 0, len(list.Data.Filters))
	for _, filter := range list.Data.Filters {
		id, name := strings.TrimSpace(filter.ID), strings.TrimSpace(filter.Name)
		if len(selected) > 0 && !selected[strings.ToLower(id)] && !selected[strings.ToLower(name)] {
			continue
		}
		found[strings.ToLower(id)] = true
		found[strings.ToLower(name)] = true
		entry := SiloSavedFilter{ID: id, Name: name, Items: []SiloSavedFilterItem{}}
		seen := map[string]bool{}
		for page := 1; page <= 1000; page++ {
			ff := map[string]any{"page": page, "per_page": 200}
			if filter.FindFilter.Q != "" {
				ff["q"] = filter.FindFilter.Q
			}
			if filter.FindFilter.Sort != "" {
				ff["sort"] = filter.FindFilter.Sort
			}
			if filter.FindFilter.Direction == "ASC" || filter.FindFilter.Direction == "DESC" {
				ff["direction"] = filter.FindFilter.Direction
			}
			var result struct {
				Data struct {
					Scenes struct {
						Count  int `json:"count"`
						Scenes []struct {
							ID    string `json:"id"`
							Code  string `json:"code"`
							Title string `json:"title"`
							Files []struct {
								Path string `json:"path"`
							} `json:"files"`
						} `json:"scenes"`
					} `json:"findScenes"`
				} `json:"data"`
				Errors []struct {
					Message string `json:"message"`
				} `json:"errors"`
			}
			vars := map[string]any{"findFilter": ff, "sceneFilter": filter.ObjectFilter}
			query := `query($findFilter: FindFilterType, $sceneFilter: SceneFilterType) { findScenes(filter: $findFilter, scene_filter: $sceneFilter) { count scenes { id code title files { path } } } }`
			if err = s.savedFilterGraphQL(ctx, base, key, query, vars, &result); err != nil {
				return nil, err
			}
			if len(result.Errors) > 0 {
				return nil, fmt.Errorf("StashApp saved filter %q: %s", name, result.Errors[0].Message)
			}
			for _, scene := range result.Data.Scenes.Scenes {
				if seen[scene.ID] {
					continue
				}
				seen[scene.ID] = true
				for _, file := range scene.Files {
					if file.Path != "" {
						entry.Items = append(entry.Items, SiloSavedFilterItem{SceneID: scene.ID, Path: file.Path, Code: scene.Code, Title: scene.Title})
					}
				}
			}
			if len(result.Data.Scenes.Scenes) < 200 {
				break
			}
			if page == 1000 {
				return nil, fmt.Errorf("StashApp saved filter %q exceeded 1000 pages", name)
			}
		}
		out = append(out, entry)
	}
	for token := range selected {
		if !found[token] {
			return nil, fmt.Errorf("Stash saved filter %q was not found", token)
		}
	}
	return out, nil
}

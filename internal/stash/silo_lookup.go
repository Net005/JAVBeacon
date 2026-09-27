package stash

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"
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
	TagIDs        []string
	ScreenshotURL string
	PlayCount     int
	OCounter      int
	LastPlayedAt  string
	PlayDuration  float64
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
	gql := fmt.Sprintf(`query { findScenes(filter: { q: %s, per_page: 25 }) { scenes { id title code details date studio { name } performers { id name image_path birthdate } tags { id name } files { path } paths { screenshot } play_count o_counter last_played_at play_duration } } }`, strconv.Quote(query))
	var payload struct {
		Data struct {
			FindScenes struct {
				Scenes []struct {
					ID, Title, Code, Details, Date string
					Studio                         *struct{ Name string }
					Performers                     []struct {
						ID        string `json:"id"`
						Name      string `json:"name"`
						ImagePath string `json:"image_path"`
						Birthdate string `json:"birthdate"`
					} `json:"performers"`
					Tags         []struct{ ID, Name string }
					Files        []struct{ Path string }
					Paths        struct{ Screenshot string }
					PlayCount    int     `json:"play_count"`
					OCounter     int     `json:"o_counter"`
					LastPlayedAt string  `json:"last_played_at"`
					PlayDuration float64 `json:"play_duration"`
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
			item := SiloScene{ID: scene.ID, Code: code, Title: scene.Title, Details: scene.Details, Date: scene.Date, ScreenshotURL: scene.Paths.Screenshot, PlayCount: scene.PlayCount, OCounter: scene.OCounter, LastPlayedAt: scene.LastPlayedAt, PlayDuration: scene.PlayDuration}
			if scene.Studio != nil {
				item.Studio = scene.Studio.Name
			}
			for _, p := range scene.Performers {
				item.Performers = append(item.Performers, StashPerformer{ID: p.ID, Name: p.Name, ImagePath: p.ImagePath, Birthdate: p.Birthdate})
			}
			for _, tag := range scene.Tags {
				item.Tags = append(item.Tags, tag.Name)
				item.TagIDs = append(item.TagIDs, tag.ID)
			}
			out = append(out, item)
		}
	}
	return out, nil
}

func (s *Service) SiloSceneByID(ctx context.Context, sceneID string) (SiloScene, error) {
	base, key, err := s.stashConfig(ctx)
	if err != nil {
		return SiloScene{}, err
	}
	gql := fmt.Sprintf(`query { findScene(id: %s) { id title code details date studio { name } performers { id name image_path birthdate } tags { id name } files { path } paths { screenshot } } }`, strconv.Quote(sceneID))
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
				Tags  []struct{ ID, Name string }
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
		out.TagIDs = append(out.TagIDs, t.ID)
	}
	return out, nil
}

// SiloWatchlistScenes reads the configured Watchlist tag directly from
// StashApp. The returned timestamps are Stash scene update times, which are
// the ordering source for Silo's imported Watchlist.
func (s *Service) SiloWatchlistScenes(ctx context.Context) (map[string]time.Time, bool, error) {
	settings, err := s.store.Settings(ctx)
	if err != nil {
		return nil, false, err
	}
	tagID := strings.TrimSpace(settings["stash_watchlist_tag_id"])
	if tagID == "" {
		return nil, false, nil
	}
	base, key, err := s.stashConfig(ctx)
	if err != nil {
		return nil, true, err
	}
	// Filter in Stash before transferring scenes. Fetching every scene just to
	// inspect one tag can exceed Silo's scheduled-task RPC deadline.
	gql := fmt.Sprintf(`query JAVBeaconSiloWatchlist { findScenes(filter: { per_page: -1 }, scene_filter: { tags: { value: [%s], modifier: INCLUDES } }) { scenes { id updated_at tags { id } files { path } } } }`, strconv.Quote(tagID))
	var payload struct {
		Data struct {
			FindScenes struct {
				Scenes []struct {
					ID        string `json:"id"`
					UpdatedAt string `json:"updated_at"`
					Tags      []struct {
						ID string `json:"id"`
					} `json:"tags"`
					Files []struct {
						Path string `json:"path"`
					} `json:"files"`
				} `json:"scenes"`
			} `json:"findScenes"`
		} `json:"data"`
		Errors []struct{ Message string } `json:"errors"`
	}
	if err := s.graphql(ctx, base, key, gql, &payload); err != nil {
		return nil, true, err
	}
	if len(payload.Errors) > 0 {
		return nil, true, fmt.Errorf("StashApp watchlist: %s", payload.Errors[0].Message)
	}
	out := make(map[string]time.Time)
	for _, scene := range payload.Data.FindScenes.Scenes {
		if len(scene.Files) == 0 {
			continue // Only Stash scenes backed by a local media file.
		}
		for _, tag := range scene.Tags {
			if tag.ID == tagID {
				updated, _ := time.Parse(time.RFC3339Nano, scene.UpdatedAt)
				out[scene.ID] = updated
				break
			}
		}
	}
	return out, true, nil
}

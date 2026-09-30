package silo

import (
	"context"
	"fmt"
	"time"

	"github.com/Net005/JAVBeacon/internal/domain"
)

// MetadataChange is an identity hint, not a full metadata payload. Silo uses
// it to refresh only catalog items affected by a JAVBeacon or Stash edit.
type MetadataChange struct {
	ReleaseID    int64  `json:"release_id,omitempty"`
	StashSceneID string `json:"stash_scene_id,omitempty"`
	Code         string `json:"code,omitempty"`
	Title        string `json:"title,omitempty"`
	Path         string `json:"path,omitempty"`
}

type MetadataChangesResult struct {
	CheckedAt time.Time        `json:"checked_at"`
	Items     []MetadataChange `json:"items"`
}

func (s *Service) MetadataChanges(ctx context.Context, since time.Time) (MetadataChangesResult, error) {
	// Return the time at which this scan began, with overlap. Advancing a
	// consumer cursor to completion time could skip an update made mid-query.
	result := MetadataChangesResult{CheckedAt: time.Now().UTC().Add(-10 * time.Second), Items: []MetadataChange{}}
	if since.IsZero() {
		settings, err := s.store.Settings(ctx)
		if err != nil {
			return MetadataChangesResult{}, err
		}
		since, _ = time.Parse(time.RFC3339Nano, settings["silo_metadata_sync_cursor"])
		if since.IsZero() {
			since = result.CheckedAt.Add(-10 * time.Minute)
		}
	}
	if since.After(result.CheckedAt) {
		return MetadataChangesResult{}, fmt.Errorf("metadata change cursor is in the future")
	}
	for offset := 0; offset < 10000; offset += 500 {
		rows, err := s.store.Releases(ctx, domain.ReleaseFilter{Status: "local", UpdatedAfter: since, ShowNonPreferred: true, Sort: "updated", Direction: "desc", Limit: 500, Offset: offset})
		if err != nil {
			return MetadataChangesResult{}, err
		}
		for _, r := range rows {
			result.Items = append(result.Items, MetadataChange{ReleaseID: r.ID, StashSceneID: r.StashSceneID, Code: r.VideoID, Path: r.StashFilePath})
		}
		if len(rows) < 500 {
			break
		}
		if offset == 9500 {
			return MetadataChangesResult{}, fmt.Errorf("JAVBeacon metadata-change feed exceeded 10000 releases; cursor retained")
		}
	}
	settings, err := s.store.Settings(ctx)
	if err != nil {
		return MetadataChangesResult{}, err
	}
	if s.stash != nil && settings["stash_base_url"] != "" {
		scenes, err := s.stash.SiloChangedScenes(ctx, since)
		if err != nil {
			return MetadataChangesResult{}, err
		}
		for _, scene := range scenes {
			result.Items = append(result.Items, MetadataChange{StashSceneID: scene.ID, Code: scene.Code, Title: scene.Title, Path: scene.Path})
		}
	}
	return result, nil
}

// AckMetadataChanges persists an at-least-once cursor only after Silo accepted
// all targeted refresh jobs. A restart resumes from the last acknowledged scan.
func (s *Service) AckMetadataChanges(ctx context.Context, checkedAt time.Time) error {
	if checkedAt.IsZero() || checkedAt.After(time.Now().UTC()) {
		return fmt.Errorf("invalid metadata change acknowledgement")
	}
	settings, err := s.store.Settings(ctx)
	if err != nil {
		return err
	}
	current, _ := time.Parse(time.RFC3339Nano, settings["silo_metadata_sync_cursor"])
	if !checkedAt.After(current) {
		return nil
	}
	return s.store.SaveSettings(ctx, map[string]string{"silo_metadata_sync_cursor": checkedAt.UTC().Format(time.RFC3339Nano)})
}

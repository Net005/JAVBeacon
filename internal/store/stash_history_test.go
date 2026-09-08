package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/Net005/JAVBeacon/internal/domain"
)

func TestStashHistoryPersistsAndMergesSameDayEvents(t *testing.T) {
	ctx := context.Background()
	st, err := OpenSQLite(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	plays := []time.Time{
		time.Date(2026, 9, 1, 8, 0, 0, 0, time.UTC),
		time.Date(2026, 9, 1, 20, 0, 0, 0, time.UTC),
		time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC),
	}
	orgasms := []time.Time{time.Date(2026, 9, 1, 20, 30, 0, 0, time.UTC)}
	scene := domain.StashHistoryScene{StashSceneID: "scene-1", ReleaseID: 42, VideoID: "ATID-803", Title: "Test", JavLibraryURL: "https://www.javlibrary.com/en/?v=x", FilePath: "/media/ATID-803.mp4", TotalPlaySeconds: 3600}
	if err := st.UpsertStashHistory(ctx, scene, plays, orgasms); err != nil {
		t.Fatal(err)
	}
	items, err := st.StashHistory(ctx, "", time.Time{}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("got %d daily items, want 2: %+v", len(items), items)
	}
	if items[1].Date != "2026-09-01" || items[1].PlayCount != 2 || items[1].OrgasmCount != 1 || items[1].PlaySeconds != 2400 || !items[1].DurationEstimated {
		t.Fatalf("unexpected merged day: %+v", items[1])
	}
	if !items[1].LatestPlayAt.Equal(plays[1]) || !items[1].LatestOrgasmAt.Equal(orgasms[0]) || !items[1].LatestEventAt.Equal(orgasms[0]) {
		t.Fatalf("unexpected latest event times: %+v", items[1])
	}
	exported, err := st.StashHistoryExport(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if exported.Format != "javbeacon-stash-history" || len(exported.Scenes) != 1 || len(exported.Events) != 4 {
		t.Fatalf("unexpected export: %+v", exported)
	}
}

func TestStashHistoryConsolidatesRecreatedSceneForSameRelease(t *testing.T) {
	ctx := context.Background()
	st, err := OpenSQLite(filepath.Join(t.TempDir(), "history-recreated.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	oldPlay := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
	newPlay := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)
	old := domain.StashHistoryScene{StashSceneID: "old-scene", ReleaseID: 77, VideoID: "ABC-123", Title: "Old", TotalPlaySeconds: 900}
	if err = st.UpsertStashHistory(ctx, old, []time.Time{oldPlay}, nil); err != nil {
		t.Fatal(err)
	}
	recreated := domain.StashHistoryScene{StashSceneID: "new-scene", ReleaseID: 77, VideoID: "ABC-123", Title: "New", TotalPlaySeconds: 300}
	if err = st.UpsertStashHistory(ctx, recreated, []time.Time{newPlay}, nil); err != nil {
		t.Fatal(err)
	}
	exported, err := st.StashHistoryExport(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(exported.Scenes) != 1 || exported.Scenes[0].StashSceneID != "new-scene" || exported.Scenes[0].TotalPlaySeconds != 1200 {
		t.Fatalf("scene identity was not consolidated: %+v", exported.Scenes)
	}
	if len(exported.Events) != 2 {
		t.Fatalf("got %d events after consolidation, want 2", len(exported.Events))
	}
}

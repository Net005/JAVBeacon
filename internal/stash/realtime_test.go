package stash

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Net005/JAVBeacon/internal/domain"
	"github.com/Net005/JAVBeacon/internal/store"
)

func TestRealtimeSceneSyncUpdatesOnlyMatchedReleaseAndHistory(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "realtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	site, _ := st.SaveSite(ctx, domain.Site{Title: "Test", Type: "Site", Name: "Test", Enabled: true})
	_, _ = st.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "ABC-123", Title: "Matched"})
	_, _ = st.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "OTHER-9", Title: "Untouched"})

	_ = st.SaveSettings(ctx, map[string]string{"stash_watchlist_tag_id": "watchlist"})
	svc := New(st, time.Second, slog.Default(), nil, nil)
	svc.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		var request map[string]any
		_ = json.NewDecoder(r.Body).Decode(&request)
		variables := request["variables"].(map[string]any)
		if variables["id"] != "scene-123" {
			t.Fatalf("scene variable = %#v", variables)
		}
		body := `{"data":{"findScene":{"id":"scene-123","title":"ABC-123 title","code":"ABC-123","date":"2024-01-02","created_at":"2024-01-03T04:05:06Z","urls":[],"o_counter":1,"play_count":1,"last_played_at":"2024-01-04T00:00:00Z","play_duration":120,"play_history":["2024-01-04T00:00:00Z"],"o_history":["2024-01-04T00:01:00Z"],"tags":[{"id":"watchlist"}],"files":[{"path":"/library/ABC-123.mp4"}]}}}`
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})
	matched, err := svc.syncRealtimeScene(ctx, map[string]string{"stash_base_url": "https://stash.example", "stash_watchlist_tag_id": "watchlist"}, "scene-123")
	if err != nil {
		t.Fatal(err)
	}
	if matched != "ABC-123" {
		t.Fatalf("matched = %q", matched)
	}
	releases, _ := st.Releases(ctx, domain.ReleaseFilter{Limit: 10})
	var got domain.Release
	for _, release := range releases {
		if release.VideoID == "ABC-123" {
			got = release
		}
	}
	if !got.Local || !got.Watchlist || got.StashSceneID != "scene-123" || got.StashFilePath != "/library/ABC-123.mp4" || got.PlayCount != 1 || got.OCounter != 1 {
		t.Fatalf("unexpected matched release: %+v", got)
	}
	history, err := st.StashHistoryEventsForScene(ctx, "scene-123")
	if err != nil || len(history) != 2 {
		t.Fatalf("history=%+v err=%v", history, err)
	}
	settings, _ := st.Settings(ctx)
	if settings["jellyfin_library_revision"] == "" {
		t.Fatal("realtime Stash update did not mark Jellyfin library revision")
	}
}

func TestRealtimeSceneSyncClearsWatchlistWhenTagIsRemoved(t *testing.T) {
	ctx := context.Background()
	st, _ := store.OpenSQLite(filepath.Join(t.TempDir(), "watchlist-remove.db"))
	defer st.Close()
	site, _ := st.SaveSite(ctx, domain.Site{Title: "Test", Type: "Site", Name: "Test", Enabled: true})
	_, _ = st.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "ABC-123", Title: "Matched", Watchlist: true})
	releases, _ := st.Releases(ctx, domain.ReleaseFilter{Limit: 10})
	_ = st.SetStashState(ctx, releases[0].ID, true, "scene-123")
	svc := New(st, time.Second, slog.Default(), nil, nil)
	svc.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body := `{"data":{"findScene":{"id":"scene-123","title":"ABC-123","code":"ABC-123","created_at":"2024-01-03T04:05:06Z","tags":[],"files":[{"path":"/library/ABC-123.mp4"}]}}}`
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})
	if _, err := svc.syncRealtimeScene(ctx, map[string]string{"stash_base_url": "https://stash.example", "stash_watchlist_tag_id": "watchlist"}, "scene-123"); err != nil {
		t.Fatal(err)
	}
	got, _ := st.Release(ctx, releases[0].ID)
	if got.Watchlist {
		t.Fatal("Watchlist remained set after the Stash tag was removed")
	}
}

func TestRealtimeSceneDeleteClearsExistingMatch(t *testing.T) {
	ctx := context.Background()
	st, _ := store.OpenSQLite(filepath.Join(t.TempDir(), "delete.db"))
	defer st.Close()
	site, _ := st.SaveSite(ctx, domain.Site{Title: "Test", Type: "Site", Name: "Test", Enabled: true})
	_, _ = st.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "ABC-123", Title: "Matched"})
	releases, _ := st.Releases(ctx, domain.ReleaseFilter{Limit: 10})
	_ = st.SetStashState(ctx, releases[0].ID, true, "scene-gone")
	svc := New(st, time.Second, slog.Default(), nil, nil)
	svc.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"data":{"findScene":null}}`)), Header: make(http.Header)}, nil
	})
	if _, err := svc.syncRealtimeScene(ctx, map[string]string{"stash_base_url": "https://stash.example"}, "scene-gone"); err != nil {
		t.Fatal(err)
	}
	got, _ := st.Release(ctx, releases[0].ID)
	if got.Local || got.StashSceneID != "" {
		t.Fatalf("stale match not cleared: %+v", got)
	}
}

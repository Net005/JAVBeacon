package stash

import (
	"context"
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

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestCanonicalVideoID(t *testing.T) {
	for _, input := range []string{"PRED-888", "pred888", "PRED_0888", " PRED 00888 "} {
		if got := canonical(input); got != "PRED888" {
			t.Fatalf("canonical(%q) = %q, want PRED888", input, got)
		}
	}
}

func TestFetchSendsStashAPIKeyHeader(t *testing.T) {
	s := New(nil, time.Second, slog.Default(), nil, nil)
	s.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if got := r.Header.Get("ApiKey"); got != "secret-key" {
			t.Fatalf("ApiKey header = %q", got)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"data":{"findScenes":{"scenes":[{"title":"PRED-888","code":"","date":"2024-05-01"}]}}}`)), Header: make(http.Header)}, nil
	})
	ids, dates, scenes, err := s.fetch(context.Background(), "https://stash.example/graphql", DefaultQuery, "secret-key")
	if err != nil || scenes != 1 {
		t.Fatalf("fetch: scenes=%d err=%v", scenes, err)
	}
	if _, ok := ids["PRED888"]; !ok {
		t.Fatal("expected normalized scene ID")
	}
	// TODO-2.0's "Missing released status display": the scene's date field
	// should be captured under the same canonical key as its scene ID.
	if dates["PRED888"] != "2024-05-01" {
		t.Fatalf("expected scene date to be captured, got %+v", dates)
	}
}

// TestFetchLeavesDatesEmptyWhenSceneOmitsDate covers a custom
// stash_graphql_query that does not request the scene's `date` field: it
// must not be treated as an error, and simply yields no entry for that
// scene rather than a zero-value date that would later overwrite a
// previously stored one.
func TestFetchLeavesDatesEmptyWhenSceneOmitsDate(t *testing.T) {
	s := New(nil, time.Second, slog.Default(), nil, nil)
	s.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"data":{"findScenes":{"scenes":[{"title":"PRED-888","code":""}]}}}`)), Header: make(http.Header)}, nil
	})
	ids, dates, _, err := s.fetch(context.Background(), "https://stash.example/graphql", `query { findScenes { scenes { id title code } } }`, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := ids["PRED888"]; !ok {
		t.Fatal("expected normalized scene ID")
	}
	if _, ok := dates["PRED888"]; ok {
		t.Fatalf("expected no date entry when the query omits date, got %+v", dates)
	}
}

func TestSyncWatchlistReleaseAddsTagAndPreservesExistingTags(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "stash.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.SaveSettings(ctx, map[string]string{"stash_base_url": "https://stash.example", "stash_watchlist_tag_id": "watchlist", "stash_api_key": "secret"}); err != nil {
		t.Fatal(err)
	}
	site, _ := st.SaveSite(ctx, domain.Site{Title: "GIGA", Type: "Site", Name: "GIGA", Enabled: true})
	_, _ = st.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "TEST-1", Title: "Watchlist", Source: "GIGA", Watchlist: true})
	releases, _ := st.Releases(ctx, domain.ReleaseFilter{Limit: 10})
	if err := st.SetStashState(ctx, releases[0].ID, true, "scene-1"); err != nil {
		t.Fatal(err)
	}
	// A previous successful sync is only historical evidence. If the tag was
	// later removed in StashApp, a manual Watchlist toggle must restore it.
	if err := st.SaveWatchlistSync(ctx, releases[0].ID, "scene-1", "watchlist", "tag added"); err != nil {
		t.Fatal(err)
	}
	var mutation string
	s := New(st, time.Second, slog.Default(), nil, nil)
	s.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(r.Body)
		query := string(body)
		response := `{"data":{"findTag":{"id":"watchlist"}}}`
		if strings.Contains(query, "findScene") {
			response = `{"data":{"findScene":{"tags":[{"id":"keep"}]}}}`
		}
		if strings.Contains(query, "sceneUpdate") {
			mutation = query
			response = `{"data":{"sceneUpdate":{"id":"scene-1"}}}`
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(response)), Header: make(http.Header)}, nil
	})
	state, err := s.SyncWatchlistRelease(ctx, releases[0].ID)
	if err != nil || state != "tagged" {
		t.Fatalf("state=%q err=%v", state, err)
	}
	if !strings.Contains(mutation, `\"keep\"`) || !strings.Contains(mutation, `\"watchlist\"`) {
		t.Fatalf("mutation did not preserve/add tags: %s", mutation)
	}
	if synced, err := st.WatchlistSynced(ctx, releases[0].ID, "scene-1", "watchlist"); err != nil || !synced {
		t.Fatalf("synced=%v err=%v", synced, err)
	}
}

func TestSyncWatchlistScheduleKeepsPreviouslySyncedFastPath(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "stash-scheduled-watchlist.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.SaveSettings(ctx, map[string]string{"stash_base_url": "https://stash.example", "stash_watchlist_tag_id": "watchlist"}); err != nil {
		t.Fatal(err)
	}
	site, _ := st.SaveSite(ctx, domain.Site{Title: "GIGA", Type: "Site", Name: "GIGA", Enabled: true})
	_, _ = st.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "TEST-2", Title: "Scheduled Watchlist", Source: "GIGA", Watchlist: true})
	releases, _ := st.Releases(ctx, domain.ReleaseFilter{Limit: 10})
	if err := st.SetStashState(ctx, releases[0].ID, true, "scene-2"); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveWatchlistSync(ctx, releases[0].ID, "scene-2", "watchlist", "tag added"); err != nil {
		t.Fatal(err)
	}

	mutations := 0
	s := New(st, time.Second, slog.Default(), nil, nil)
	s.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(r.Body)
		query := string(body)
		response := `{"data":{"findTag":{"id":"watchlist"}}}`
		if strings.Contains(query, "findScene") {
			response = `{"data":{"findScene":{"tags":[{"id":"keep"}]}}}`
		}
		if strings.Contains(query, "sceneUpdate") {
			mutations++
			response = `{"data":{"sceneUpdate":{"id":"scene-2"}}}`
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(response)), Header: make(http.Header)}, nil
	})

	status, err := s.SyncWatchlist(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if status.Checked != 1 || status.Updated != 0 || status.Skipped != 1 || mutations != 0 {
		t.Fatalf("status=%+v mutations=%d, want the scheduled sync to retain its cached fast path", status, mutations)
	}
}

func TestSyncWatchlistReleaseRemovesTagAndPreservesExistingTags(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "stash-remove-watchlist.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.SaveSettings(ctx, map[string]string{"stash_base_url": "https://stash.example", "stash_watchlist_tag_id": "watchlist"}); err != nil {
		t.Fatal(err)
	}
	site, _ := st.SaveSite(ctx, domain.Site{Title: "GIGA", Type: "Site", Name: "GIGA", Enabled: true})
	_, _ = st.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "TEST-3", Title: "Remove Watchlist", Source: "GIGA"})
	releases, _ := st.Releases(ctx, domain.ReleaseFilter{Limit: 10})
	if err := st.SetStashState(ctx, releases[0].ID, true, "scene-3"); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveWatchlistSync(ctx, releases[0].ID, "scene-3", "watchlist", "tag added"); err != nil {
		t.Fatal(err)
	}

	var mutation string
	s := New(st, time.Second, slog.Default(), nil, nil)
	s.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(r.Body)
		query := string(body)
		response := `{"data":{"findTag":{"id":"watchlist"}}}`
		if strings.Contains(query, "findScene") {
			response = `{"data":{"findScene":{"tags":[{"id":"keep"},{"id":"watchlist"}]}}}`
		}
		if strings.Contains(query, "sceneUpdate") {
			mutation = query
			response = `{"data":{"sceneUpdate":{"id":"scene-3"}}}`
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(response)), Header: make(http.Header)}, nil
	})

	state, err := s.SyncWatchlistRelease(ctx, releases[0].ID)
	if err != nil || state != "untagged" {
		t.Fatalf("state=%q err=%v", state, err)
	}
	if !strings.Contains(mutation, `\"keep\"`) || strings.Contains(mutation, `\"watchlist\"`) {
		t.Fatalf("mutation did not preserve unrelated tags while removing Watchlist: %s", mutation)
	}
	if synced, err := st.WatchlistSynced(ctx, releases[0].ID, "scene-3", "watchlist"); err != nil || synced {
		t.Fatalf("synced=%v err=%v, want cleared scheduled-sync marker after manual removal", synced, err)
	}
}

func TestLocalSyncDoesNotRunWatchlistTagSync(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "stash.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.SaveSettings(ctx, map[string]string{"stash_base_url": "https://stash.example", "stash_watchlist_tag_id": "watchlist", "stash_watchlist_sync_enabled": "true"}); err != nil {
		t.Fatal(err)
	}
	site, _ := st.SaveSite(ctx, domain.Site{Title: "GIGA", Type: "Site", Name: "GIGA", Enabled: true})
	_, _ = st.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "TEST-1", Title: "Watchlist", Source: "GIGA", Watchlist: true})

	requests := 0
	s := New(st, time.Second, slog.Default(), nil, nil)
	s.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		requests++
		body, _ := io.ReadAll(r.Body)
		response := `{"data":{"findScenes":{"scenes":[{"id":"scene-1","title":"TEST-1","code":"TEST-1"}]}}}`
		if strings.Contains(string(body), "JAVBeaconSceneCreatedAt") {
			response = `{"data":{"findScenes":{"scenes":[{"id":"scene-1","created_at":"2026-08-28T10:00:00Z"}]}}}`
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(response)), Header: make(http.Header)}, nil
	})
	s.run(ctx)
	// Three local-library requests: scene discovery, the required created_at
	// timestamps used by Added Locally, and optional playback statistics. None
	// belong to the Watchlist tag sync, which is what this test guards against.
	if requests != 3 {
		t.Fatalf("local sync made %d Stash requests, want scene discovery + created-at + playback-stat requests", requests)
	}
}

func TestFirstLocalSyncStoresPlaybackStatsForReleaseConditions(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "stash-first-sync.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.SaveSettings(ctx, map[string]string{"stash_base_url": "https://stash.example"}); err != nil {
		t.Fatal(err)
	}
	site, _ := st.SaveSite(ctx, domain.Site{Title: "JavLibrary", Type: "Site", Name: "JavLibrary", Enabled: true})
	_, _ = st.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "SYNC-100", Title: "First sync", Source: "JavLibrary"})

	s := New(st, time.Second, slog.Default(), nil, nil)
	s.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(r.Body)
		response := `{"data":{"findScenes":{"scenes":[{"id":"scene-100","title":"SYNC-100","code":"SYNC-100"}]}}}`
		if strings.Contains(string(body), "JAVBeaconSceneCreatedAt") {
			response = `{"data":{"findScenes":{"scenes":[{"id":"scene-100","created_at":"2026-08-28T10:00:00Z","files":[{"path":"/library/JAV/SYNC-100.mp4"}]}]}}}`
		} else if strings.Contains(string(body), "JAVBeaconPlaybackStats") {
			response = `{"data":{"findScenes":{"scenes":[{"id":"scene-100","o_counter":3,"play_count":5,"last_played_at":"2024-05-10T12:00:00Z","o_history":["2024-04-01T12:00:00Z","2024-05-09T12:00:00Z"]}]}}}`
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(response)), Header: make(http.Header)}, nil
	})
	s.run(ctx)

	releases, err := st.Releases(ctx, domain.ReleaseFilter{Search: "SYNC-100", Limit: 10})
	if err != nil || len(releases) != 1 {
		t.Fatalf("release lookup: releases=%+v err=%v", releases, err)
	}
	got := releases[0]
	if !got.Local || got.StashSceneID != "scene-100" || got.StashFilePath != "/library/JAV/SYNC-100.mp4" || got.StashCreatedAt.IsZero() || got.OCounter != 3 || got.PlayCount != 5 || got.LastPlayedAt != "2024-05-10T12:00:00Z" || got.LastOCountAt != "2024-05-09T12:00:00Z" {
		t.Fatalf("first sync did not persist local state and playback stats together: %+v", got)
	}
	status := s.Status()
	if status.Running || status.Phase != "Complete" || status.Total != 1 || status.Processed != 1 || status.Matched != 1 || status.CurrentItem != "" || status.Error != "" {
		t.Fatalf("completed sync status does not expose final progress: %+v", status)
	}

	conditions := []string{
		`{"field":"local","value":"true"}`,
		`{"field":"o_count","op":"gte","value":"3"}`,
		`{"field":"play_count","op":"gte","value":"5"}`,
		`{"field":"last_played","op":"before","value":"2024-05-11"}`,
		`{"field":"last_o_count","op":"after","value":"2024-05-01"}`,
	}
	for _, condition := range conditions {
		expression := `{"logic":"and","conditions":[` + condition + `]}`
		matches, filterErr := st.Releases(ctx, domain.ReleaseFilter{SearchExpression: expression, Limit: 10})
		if filterErr != nil || len(matches) != 1 || matches[0].VideoID != "SYNC-100" {
			t.Fatalf("condition %s: matches=%+v err=%v", condition, matches, filterErr)
		}
	}
}

func TestLocalSyncClearsStaleStashFilePathWhenSceneIsGone(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "stash-path-cleared.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.SaveSettings(ctx, map[string]string{"stash_base_url": "https://stash.example"}); err != nil {
		t.Fatal(err)
	}
	site, _ := st.SaveSite(ctx, domain.Site{Title: "JavLibrary", Type: "Site", Name: "JavLibrary", Enabled: true})
	if _, err := st.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "GONE-100", Title: "Removed scene", Source: "JavLibrary"}); err != nil {
		t.Fatal(err)
	}
	releases, err := st.Releases(ctx, domain.ReleaseFilter{VideoID: "GONE-100", Limit: 1})
	if err != nil || len(releases) != 1 {
		t.Fatalf("release lookup: releases=%+v err=%v", releases, err)
	}
	release := releases[0]
	if err := st.SetStashState(ctx, release.ID, true, "scene-gone"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetStashFilePath(ctx, release.ID, "/library/JAV/GONE-100.mp4"); err != nil {
		t.Fatal(err)
	}

	s := New(st, time.Second, slog.Default(), nil, nil)
	s.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"data":{"findScenes":{"scenes":[]}}}`)), Header: make(http.Header)}, nil
	})
	s.run(ctx)

	got, err := st.Release(ctx, release.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Local || got.StashSceneID != "" || got.StashFilePath != "" {
		t.Fatalf("stale Stash state and file path were not cleared: %+v", got)
	}
}

func TestScheduledLocalSyncFetchesRequiredSceneCreatedAt(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "stash-scheduled-created-at.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	settings := map[string]string{
		"stash_base_url":           "https://stash.example",
		"stash_local_sync_enabled": "true",
	}
	if err := st.SaveSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	site, _ := st.SaveSite(ctx, domain.Site{Title: "Scheduled", Type: "Site", Name: "GIGA", Enabled: true})
	_, _ = st.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "SCHED-100", Title: "Scheduled sync", Source: "GIGA"})

	s := New(st, time.Second, slog.Default(), nil, nil)
	s.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(r.Body)
		response := `{"data":{"findScenes":{"scenes":[{"id":"scene-scheduled","title":"SCHED-100","code":"SCHED-100"}]}}}`
		if strings.Contains(string(body), "JAVBeaconSceneCreatedAt") {
			response = `{"data":{"findScenes":{"scenes":[{"id":"scene-scheduled","created_at":"2026-08-28T15:00:00Z"}]}}}`
		} else if strings.Contains(string(body), "JAVBeaconPlaybackStats") {
			response = `{"data":{"findScenes":{"scenes":[{"id":"scene-scheduled"}]}}}`
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(response)), Header: make(http.Header)}, nil
	})
	if !s.startScheduledLocalSync(ctx, settings) {
		t.Fatal("enabled scheduled local-library sync was not started")
	}
	deadline := time.Now().Add(2 * time.Second)
	for s.Status().Running && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	status := s.Status()
	if status.Running || status.Error != "" || status.Phase != "Complete" {
		t.Fatalf("scheduled sync did not complete successfully: %+v", status)
	}
	releases, err := st.Releases(ctx, domain.ReleaseFilter{Search: "SCHED-100", Limit: 1})
	if err != nil || len(releases) != 1 {
		t.Fatalf("scheduled release lookup: releases=%+v err=%v", releases, err)
	}
	want := time.Date(2026, 8, 28, 15, 0, 0, 0, time.UTC)
	if !releases[0].Local || releases[0].StashSceneID != "scene-scheduled" || !releases[0].StashCreatedAt.Equal(want) {
		t.Fatalf("scheduled sync did not persist required Stash created_at: %+v", releases[0])
	}
}

// TestLocalSyncStoresStashReleaseDate covers TODO-2.0's "Missing released
// status display": a local-library sync should carry each matched scene's
// `date` field into the release's StashReleaseDate, and a later sync must
// not blank it back out just because a differently-shaped response (or a
// custom query) does not repeat it for every scene.
func TestLocalSyncStoresStashReleaseDate(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "stash-date.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.SaveSettings(ctx, map[string]string{"stash_base_url": "https://stash.example"}); err != nil {
		t.Fatal(err)
	}
	site, _ := st.SaveSite(ctx, domain.Site{Title: "GIGA", Type: "Site", Name: "GIGA", Enabled: true})
	_, _ = st.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "TEST-1", Title: "Dated", Source: "GIGA"})

	s := New(st, time.Second, slog.Default(), nil, nil)
	s.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(r.Body)
		response := `{"data":{"findScenes":{"scenes":[{"id":"scene-1","title":"TEST-1","code":"TEST-1","date":"2024-03-02"}]}}}`
		if strings.Contains(string(body), "JAVBeaconSceneCreatedAt") {
			response = `{"data":{"findScenes":{"scenes":[{"id":"scene-1","created_at":"2024-03-31T12:39:00Z"}]}}}`
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(response)), Header: make(http.Header)}, nil
	})
	s.run(ctx)
	releases, err := st.Releases(ctx, domain.ReleaseFilter{Search: "TEST-1"})
	if err != nil || len(releases) != 1 {
		t.Fatalf("release lookup: items=%d err=%v", len(releases), err)
	}
	if releases[0].StashReleaseDate != "2024-03-02" {
		t.Fatalf("expected stash_release_date to be set, got %+v", releases[0])
	}

	// A later sync whose response omits the date entirely must not clear it.
	s.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(r.Body)
		response := `{"data":{"findScenes":{"scenes":[{"id":"scene-1","title":"TEST-1","code":"TEST-1"}]}}}`
		if strings.Contains(string(body), "JAVBeaconSceneCreatedAt") {
			response = `{"data":{"findScenes":{"scenes":[{"id":"scene-1","created_at":"2024-03-31T12:39:00Z"}]}}}`
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(response)), Header: make(http.Header)}, nil
	})
	s.run(ctx)
	releases, err = st.Releases(ctx, domain.ReleaseFilter{Search: "TEST-1"})
	if err != nil || len(releases) != 1 {
		t.Fatalf("release lookup: items=%d err=%v", len(releases), err)
	}
	if releases[0].StashReleaseDate != "2024-03-02" {
		t.Fatalf("expected stash_release_date to survive a sync response without a date, got %+v", releases[0])
	}
}

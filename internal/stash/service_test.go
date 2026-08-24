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

func TestSyncDesiredReleaseAddsTagAndPreservesExistingTags(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "stash.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.SaveSettings(ctx, map[string]string{"stash_base_url": "https://stash.example", "stash_desired_tag_id": "desired", "stash_api_key": "secret"}); err != nil {
		t.Fatal(err)
	}
	site, _ := st.SaveSite(ctx, domain.Site{Title: "GIGA", Type: "Site", Name: "GIGA", Enabled: true})
	_, _ = st.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "TEST-1", Title: "Desired", Source: "GIGA", Desired: true})
	releases, _ := st.Releases(ctx, domain.ReleaseFilter{Limit: 10})
	if err := st.SetStashState(ctx, releases[0].ID, true, "scene-1"); err != nil {
		t.Fatal(err)
	}
	var mutation string
	s := New(st, time.Second, slog.Default(), nil, nil)
	s.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(r.Body)
		query := string(body)
		response := `{"data":{"findTag":{"id":"desired"}}}`
		if strings.Contains(query, "findScene") {
			response = `{"data":{"findScene":{"tags":[{"id":"keep"}]}}}`
		}
		if strings.Contains(query, "sceneUpdate") {
			mutation = query
			response = `{"data":{"sceneUpdate":{"id":"scene-1"}}}`
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(response)), Header: make(http.Header)}, nil
	})
	state, err := s.SyncDesiredRelease(ctx, releases[0].ID)
	if err != nil || state != "tagged" {
		t.Fatalf("state=%q err=%v", state, err)
	}
	if !strings.Contains(mutation, `\"keep\"`) || !strings.Contains(mutation, `\"desired\"`) {
		t.Fatalf("mutation did not preserve/add tags: %s", mutation)
	}
	if synced, err := st.DesiredSynced(ctx, releases[0].ID, "scene-1", "desired"); err != nil || !synced {
		t.Fatalf("synced=%v err=%v", synced, err)
	}
}

func TestLocalSyncDoesNotRunDesiredTagSync(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "stash.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.SaveSettings(ctx, map[string]string{"stash_base_url": "https://stash.example", "stash_desired_tag_id": "desired", "stash_desired_sync_enabled": "true"}); err != nil {
		t.Fatal(err)
	}
	site, _ := st.SaveSite(ctx, domain.Site{Title: "GIGA", Type: "Site", Name: "GIGA", Enabled: true})
	_, _ = st.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "TEST-1", Title: "Desired", Source: "GIGA", Desired: true})

	requests := 0
	s := New(st, time.Second, slog.Default(), nil, nil)
	s.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		requests++
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"data":{"findScenes":{"scenes":[{"id":"scene-1","title":"TEST-1","code":"TEST-1"}]}}}`)), Header: make(http.Header)}, nil
	})
	s.run(ctx)
	// 2, not 1: the scene discovery request plus the separate best-effort
	// playback-stats request (task 38's O Count/Last O Count Date/Last
	// Played sync) - still not the Desired tag sync's own requests, which is
	// what this test actually guards against.
	if requests != 2 {
		t.Fatalf("local sync made %d Stash requests, want only the scene discovery + playback-stats requests", requests)
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
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"data":{"findScenes":{"scenes":[{"id":"scene-1","title":"TEST-1","code":"TEST-1","date":"2024-03-02"}]}}}`)), Header: make(http.Header)}, nil
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
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"data":{"findScenes":{"scenes":[{"id":"scene-1","title":"TEST-1","code":"TEST-1"}]}}}`)), Header: make(http.Header)}, nil
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

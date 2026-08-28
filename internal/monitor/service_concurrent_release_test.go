package monitor

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Net005/JAVBeacon/internal/covers"
	"github.com/Net005/JAVBeacon/internal/domain"
	"github.com/Net005/JAVBeacon/internal/scraper"
	"github.com/Net005/JAVBeacon/internal/screenshots"
	"github.com/Net005/JAVBeacon/internal/store"
)

// TestStartReleaseRunsMultipleReleasesConcurrently guards against the exact
// regression the user reported: three manual "Update details" clicks
// serialized behind a single worker even though idle Byparr instances were
// available. It seeds two releases whose detail pages each block until both
// requests have arrived at the test server, then starts both refreshes via
// StartRelease (the entry point the /api/jobs/refresh handler now calls for
// release-scoped requests) and requires both to complete - if StartRelease
// still ran them one at a time, the second release's handler would never
// see a request until the first job finished, and this test would time out
// waiting for the "both in flight" gate to open.
func TestStartReleaseRunsMultipleReleasesConcurrently(t *testing.T) {
	const videoA, videoB = "ABC-123", "XYZ-789"
	var mu sync.Mutex
	seen := map[string]bool{}
	bothArrived := make(chan struct{})
	var closeOnce sync.Once

	detailHandler := func(videoID, title string) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) {
			mu.Lock()
			seen[videoID] = true
			ready := len(seen) == 2
			mu.Unlock()
			if ready {
				closeOnce.Do(func() { close(bothArrived) })
			}
			select {
			case <-bothArrived:
			case <-time.After(3 * time.Second):
				t.Errorf("release %s's detail request never overlapped with the other release's - StartRelease appears to still be serializing concurrent release refreshes", videoID)
				return
			}
			_, _ = w.Write([]byte(javLibraryDetailFixture(title, "2024-02-03")))
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/detail-a.html", detailHandler(videoA, "Title A"))
	mux.HandleFunc("/detail-b.html", detailHandler(videoB, "Title B"))
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "concurrent.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	ctx := context.Background()
	site, err := st.SaveSite(ctx, domain.Site{Title: "JavLibrary", Type: "Site", Name: "JavLibrary", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: videoA, Source: "JavLibrary", ProductURL: server.URL + "/detail-a.html"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: videoB, Source: "JavLibrary", ProductURL: server.URL + "/detail-b.html"}); err != nil {
		t.Fatal(err)
	}
	releaseA, err := st.Releases(ctx, domain.ReleaseFilter{Search: videoA})
	if err != nil || len(releaseA) != 1 {
		t.Fatalf("release A lookup: items=%d err=%v", len(releaseA), err)
	}
	releaseB, err := st.Releases(ctx, domain.ReleaseFilter{Search: videoB})
	if err != nil || len(releaseB) != 1 {
		t.Fatalf("release B lookup: items=%d err=%v", len(releaseB), err)
	}

	coverCache, err := covers.New(t.TempDir(), 2*time.Second, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	javlib := scraper.NewJavLibrary(2*time.Second, "", 0, slog.Default())
	akiba := scraper.NewAkiba("", "", 2*time.Second, slog.Default())
	screenshotCache, err := screenshots.New(t.TempDir(), 2*time.Second, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	service := New(st, akiba, javlib, coverCache, 1, slog.Default(), time.Hour, screenshotCache)

	if _, err := service.StartRelease(ctx, releaseA[0].ID, "", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := service.StartRelease(ctx, releaseB[0].ID, "", 0); err != nil {
		t.Fatal(err)
	}

	jobA := waitForReleaseJob(t, service, releaseA[0].ID)
	jobB := waitForReleaseJob(t, service, releaseB[0].ID)
	if jobA.Outcome != "updated" {
		t.Fatalf("release A outcome=%q, want updated (job=%+v)", jobA.Outcome, jobA)
	}
	if jobB.Outcome != "updated" {
		t.Fatalf("release B outcome=%q, want updated (job=%+v)", jobB.Outcome, jobB)
	}
}

// TestStartReleaseDedupesConcurrentCallsForSameRelease covers the other half
// of StartRelease's contract: calling it again for a release that already
// has an in-flight refresh must not start a second, redundant scrape - it
// should just hand back the existing job's live status.
func TestStartReleaseDedupesConcurrentCallsForSameRelease(t *testing.T) {
	requestStarted := make(chan struct{})
	proceed := make(chan struct{})
	var once sync.Once
	var requestCount int32

	mux := http.NewServeMux()
	mux.HandleFunc("/detail.html", func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&requestCount, 1)
		once.Do(func() { close(requestStarted) })
		<-proceed
		_, _ = w.Write([]byte(javLibraryDetailFixture("Title", "2024-02-03")))
	})
	service, release := newRefreshTestService(t, mux)

	first, err := service.StartRelease(context.Background(), release.ID, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-requestStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for the first StartRelease call's HTTP request to start")
	}

	second, err := service.StartRelease(context.Background(), release.ID, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !second.StartedAt.Equal(first.StartedAt) {
		t.Fatalf("second StartRelease call returned a job with a different StartedAt than the first - it looks like it started a duplicate job instead of returning the in-flight one: first=%+v second=%+v", first, second)
	}
	close(proceed)
	job := waitForReleaseJob(t, service, release.ID)
	if job.Outcome != "updated" {
		t.Fatalf("outcome=%q, want updated (job=%+v)", job.Outcome, job)
	}
	// The strongest proof of dedup: the detail page must only ever have been
	// fetched once, however many times StartRelease was called while the
	// refresh was in flight.
	if got := atomic.LoadInt32(&requestCount); got != 1 {
		t.Fatalf("detail page was fetched %d times, want exactly 1 - StartRelease started a duplicate scrape", got)
	}
}

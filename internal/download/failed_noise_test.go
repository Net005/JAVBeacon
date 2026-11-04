package download

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/Net005/JAVBeacon/internal/domain"
	"github.com/Net005/JAVBeacon/internal/store"
)

func noiseStore(t *testing.T) (*store.SQLite, domain.Site) {
	t.Helper()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "noise.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	site, err := st.SaveSite(context.Background(), domain.Site{Title: "Example", Name: "Example", Type: "Site", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	return st, site
}

func noiseRelease(t *testing.T, st *store.SQLite, site domain.Site, id string, monitored bool) domain.Release {
	t.Helper()
	ctx := context.Background()
	if _, err := st.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: id, Title: id, Source: "Example", MonitorDownload: monitored}); err != nil {
		t.Fatal(err)
	}
	rows, err := st.Releases(ctx, domain.ReleaseFilter{VideoID: id, Limit: 1})
	if err != nil || len(rows) != 1 {
		t.Fatalf("release setup: %v", err)
	}
	return rows[0]
}

// A stalled torrent for a release that is no longer wanted must not trigger
// HTTP provider searches (each failed search used to add a failed row).
func TestTorrentHTTPFallbackSkipsReleasesNoLongerWanted(t *testing.T) {
	ctx := context.Background()
	st, site := noiseStore(t)
	if err := st.SaveSettings(ctx, map[string]string{"http_download_directory": t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	svc := New(st, time.Second, slog.Default())
	unmonitored := noiseRelease(t, st, site, "EXAMPLE-201", false)
	d := domain.Download{ID: 1, ReleaseID: unmonitored.ID, Transport: "torrent", Status: "downloading"}
	if svc.tryTorrentHTTPFallback(ctx, nil, &d, "stalled") {
		t.Fatal("fallback acted on an unmonitored release")
	}
	if d.PostStatus != "http_fallback_skipped" {
		t.Fatalf("post status = %q, want http_fallback_skipped", d.PostStatus)
	}
	if !releaseWantsHTTPFallback(domain.Release{MonitorDownload: true}) {
		t.Fatal("monitored release must stay eligible")
	}
	for _, r := range []domain.Release{{MonitorDownload: true, Local: true}, {MonitorDownload: true, StashSceneID: "42"}, {}} {
		if releaseWantsHTTPFallback(r) {
			t.Fatalf("release %+v must not be eligible", r)
		}
	}
	rows, err := st.Downloads(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("skipped fallback recorded %d download rows", len(rows))
	}
}

// Repeated fallback attempts back off instead of retrying every 30 minutes.
func TestHTTPFallbackRetryBacksOff(t *testing.T) {
	want := []time.Duration{30 * time.Minute, time.Hour, 2 * time.Hour, 4 * time.Hour, 8 * time.Hour, 16 * time.Hour, 24 * time.Hour, 24 * time.Hour}
	for i, w := range want {
		if got := httpFallbackRetryDelay(i + 1); got != w {
			t.Fatalf("attempt %d delay = %v, want %v", i+1, got, w)
		}
	}
	svc := &Service{}
	if !svc.httpFallbackDue(7) || svc.httpFallbackDue(7) {
		t.Fatal("first attempt must run once, then wait")
	}
	svc.httpFallbackRetryAt[7] = time.Now().Add(-time.Second)
	if !svc.httpFallbackDue(7) {
		t.Fatal("due attempt did not run")
	}
	if wait := time.Until(svc.httpFallbackRetryAt[7]); wait < 59*time.Minute {
		t.Fatalf("second retry waits %v, want about an hour", wait)
	}
}

// A provider search that errors (timeouts, outages) is search history, not a
// failed download: it must not appear under the Failed filter.
func TestSearchErrorsAreNotFailedDownloads(t *testing.T) {
	ctx := context.Background()
	st, site := noiseStore(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer server.Close()
	if err := st.SaveSettings(ctx, map[string]string{"search_url_template": server.URL + "/feed?q=<release_id>"}); err != nil {
		t.Fatal(err)
	}
	release := noiseRelease(t, st, site, "EXAMPLE-301", true)
	svc := New(st, time.Second, slog.Default())
	for i := 0; i < 3; i++ {
		if _, err := svc.searchNative(ctx, release, "Monitored Search"); err == nil {
			t.Fatal("expected provider error")
		}
	}
	failed, err := st.Downloads(ctx, "failed")
	if err != nil {
		t.Fatal(err)
	}
	if len(failed) != 0 {
		t.Fatalf("search errors recorded %d failed downloads", len(failed))
	}
	history, err := st.Downloads(ctx, "search_failed")
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 3 {
		t.Fatalf("search_failed history = %d, want 3", len(history))
	}
}

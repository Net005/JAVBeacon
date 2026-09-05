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

// TestIsRecentReleaseAndIsOlderRelease covers the day-threshold bucketing
// that splits the "Releases checked by the scheduled job" monitored-search
// job into two independent schedules (task 38): a recent-releases schedule
// that runs often, and an older-releases schedule that runs far less often
// so an old, unlikely-to-suddenly-appear release doesn't keep hitting the
// download provider (e.g. NYAA) on every run.
func TestIsRecentReleaseAndIsOlderRelease(t *testing.T) {
	now := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name        string
		releaseDate string
		recentDays  int
		olderDays   int
		wantRecent  bool
		wantOlder   bool
	}{
		{"blank release date is always recent, never older", "", 30, 30, true, false},
		{"unparsable release date behaves like blank", "not-a-date", 30, 30, true, false},
		{"today is within the recent window", "2026-08-23", 30, 30, true, false},
		{"exactly at the recent boundary still counts as recent", "2026-07-24", 30, 30, true, false},
		{"one day past the recent boundary is not recent (and now counts as older)", "2026-07-23", 30, 30, false, true},
		{"well past both thresholds is older, not recent", "2020-01-01", 30, 30, false, true},
		{"exactly at the older boundary does not count as older yet", "2026-07-24", 30, 30, true, false},
		{"independent thresholds can leave a gap belonging to neither bucket", "2026-08-01", 10, 60, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			release := domain.Release{ReleaseDate: c.releaseDate}
			if got := isRecentRelease(now, release, c.recentDays); got != c.wantRecent {
				t.Errorf("isRecentRelease(%q, recentDays=%d) = %v, want %v", c.releaseDate, c.recentDays, got, c.wantRecent)
			}
			if got := isOlderRelease(now, release, c.olderDays); got != c.wantOlder {
				t.Errorf("isOlderRelease(%q, olderDays=%d) = %v, want %v", c.releaseDate, c.olderDays, got, c.wantOlder)
			}
		})
	}
}

// TestMonitoredDaysSettingFallsBackWhenUnsetOrInvalid covers
// monitor_recent_days/monitor_older_days's blank/unparsable/negative
// handling: the Monitored releases schedule settings must never crash or
// silently apply a nonsensical negative threshold, they should fall back to
// the documented default instead.
func TestMonitoredDaysSettingFallsBackWhenUnsetOrInvalid(t *testing.T) {
	cases := []struct {
		name     string
		settings map[string]string
		key      string
		fallback int
		want     int
	}{
		{"unset falls back", map[string]string{}, "monitor_recent_days", 30, 30},
		{"blank falls back", map[string]string{"monitor_recent_days": "  "}, "monitor_recent_days", 30, 30},
		{"non-numeric falls back", map[string]string{"monitor_older_days": "soon"}, "monitor_older_days", 30, 30},
		{"negative falls back", map[string]string{"monitor_older_days": "-5"}, "monitor_older_days", 30, 30},
		{"valid value is used", map[string]string{"monitor_recent_days": "14"}, "monitor_recent_days", 30, 14},
		{"zero is a valid value", map[string]string{"monitor_recent_days": "0"}, "monitor_recent_days", 30, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := monitoredDaysSetting(c.settings, c.key, c.fallback); got != c.want {
				t.Errorf("monitoredDaysSetting(%v, %q, %d) = %d, want %d", c.settings, c.key, c.fallback, got, c.want)
			}
		})
	}
}

// TestStartSearchOnlyChecksRecentReleases and
// TestStartSearchOlderOnlyChecksOlderReleases are the end-to-end proof for
// the two-schedule split: each schedule's own job (StartSearch/
// StartSearchOlder) must search only its own release-date bucket, counted
// in its own job status (SearchStatus/SearchStatusOlder), and must never
// even reach the search feed for a release outside its bucket - mirroring
// how TestRunSearchSkipsIgnoredMonitoredRelease proves an ignored release is
// skipped before ever searching.
func TestStartSearchOnlyChecksRecentReleases(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "schedule-split-recent.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	feedHits := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/feed", func(w http.ResponseWriter, r *http.Request) {
		feedHits++
		_, _ = w.Write([]byte(`<rss><channel></channel></rss>`))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	if err := st.SaveSettings(ctx, map[string]string{
		"search_url_template": server.URL + "/feed?q=<release_id>",
		"monitor_recent_days": "30",
	}); err != nil {
		t.Fatal(err)
	}
	site, _ := st.SaveSite(ctx, domain.Site{Title: "Test", Type: "Site", Name: "JavLibrary", Enabled: true})
	recentDate := time.Now().UTC().Format("2006-01-02")
	olderDate := time.Now().UTC().AddDate(0, 0, -90).Format("2006-01-02")
	if _, err := st.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "REC-100", Title: "Recent", Source: "JavLibrary", Released: true, MonitorDownload: true, ReleaseDate: recentDate}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "OLD-100", Title: "Old", Source: "JavLibrary", Released: true, MonitorDownload: true, ReleaseDate: olderDate}); err != nil {
		t.Fatal(err)
	}

	service := New(st, 2*time.Second, slog.Default())
	if err := service.StartSearch(ctx); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for service.SearchStatus().Running && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	status := service.SearchStatus()
	if status.Running {
		t.Fatalf("recent-releases search job did not finish in time: %+v", status)
	}
	if status.Checked != 1 {
		t.Fatalf("expected only the recent release to be checked, got %+v", status)
	}
	if feedHits != 1 {
		t.Fatalf("expected the search feed to be hit exactly once (for the recent release only), got %d hits", feedHits)
	}
}

func TestStartSearchOlderOnlyChecksOlderReleases(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "schedule-split-older.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	feedHits := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/feed", func(w http.ResponseWriter, r *http.Request) {
		feedHits++
		_, _ = w.Write([]byte(`<rss><channel></channel></rss>`))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	if err := st.SaveSettings(ctx, map[string]string{
		"search_url_template": server.URL + "/feed?q=<release_id>",
		"monitor_older_days":  "30",
	}); err != nil {
		t.Fatal(err)
	}
	site, _ := st.SaveSite(ctx, domain.Site{Title: "Test", Type: "Site", Name: "JavLibrary", Enabled: true})
	recentDate := time.Now().UTC().Format("2006-01-02")
	olderDate := time.Now().UTC().AddDate(0, 0, -90).Format("2006-01-02")
	if _, err := st.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "REC-200", Title: "Recent", Source: "JavLibrary", Released: true, MonitorDownload: true, ReleaseDate: recentDate}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "OLD-200", Title: "Old", Source: "JavLibrary", Released: true, MonitorDownload: true, ReleaseDate: olderDate}); err != nil {
		t.Fatal(err)
	}
	// A release with no confirmed date yet must also stay out of the older
	// schedule's bucket (isRecentRelease's default), so this run should
	// touch neither it nor the recent-dated release above.
	if _, err := st.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "UNK-200", Title: "Unknown date", Source: "JavLibrary", Released: true, MonitorDownload: true}); err != nil {
		t.Fatal(err)
	}

	service := New(st, 2*time.Second, slog.Default())
	if err := service.StartSearchOlder(ctx); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for service.SearchStatusOlder().Running && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	status := service.SearchStatusOlder()
	if status.Running {
		t.Fatalf("older-releases search job did not finish in time: %+v", status)
	}
	if status.Checked != 1 {
		t.Fatalf("expected only the older release to be checked, got %+v", status)
	}
	if feedHits != 1 {
		t.Fatalf("expected the search feed to be hit exactly once (for the older release only), got %d hits", feedHits)
	}
	// The recent schedule's own job must be untouched by running the older
	// schedule.
	if recent := service.SearchStatus(); recent.Checked != 0 || recent.Running {
		t.Fatalf("expected the recent schedule's job to be untouched, got %+v", recent)
	}
}

func TestScheduledMonitoredSearchAlsoChecksHTTPFallback(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "scheduled-http-fallback.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	feedHits, httpHits := 0, 0
	mux := http.NewServeMux()
	mux.HandleFunc("/feed", func(w http.ResponseWriter, _ *http.Request) {
		feedHits++
		_, _ = w.Write([]byte(`<rss><channel></channel></rss>`))
	})
	mux.HandleFunc("/search", func(w http.ResponseWriter, _ *http.Request) {
		httpHits++
		_, _ = w.Write([]byte(`<html><body></body></html>`))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	if err := st.SaveSettings(ctx, map[string]string{
		"search_url_template":     server.URL + "/feed?q=<release_id>",
		"javdb_url":               server.URL,
		"http_download_directory": t.TempDir(),
		"monitor_recent_days":     "30",
	}); err != nil {
		t.Fatal(err)
	}
	site, _ := st.SaveSite(ctx, domain.Site{Title: "Test", Type: "Site", Name: "JavLibrary", Enabled: true})
	if _, err := st.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "HTTP-101", Title: "Fallback", Source: "JavLibrary", Released: true, MonitorDownload: true, ReleaseDate: time.Now().UTC().Format("2006-01-02")}); err != nil {
		t.Fatal(err)
	}

	service := New(st, 2*time.Second, slog.Default())
	if err := service.StartSearch(ctx); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for service.SearchStatus().Running && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if service.SearchStatus().Running {
		t.Fatal("scheduled monitored search did not finish")
	}
	if feedHits != 1 || httpHits != 1 {
		t.Fatalf("scheduled provider hits: torrent=%d HTTP=%d, want 1 each", feedHits, httpHits)
	}
}

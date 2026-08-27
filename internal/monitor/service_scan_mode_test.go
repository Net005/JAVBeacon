package monitor

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Net005/JAVBeacon/internal/covers"
	"github.com/Net005/JAVBeacon/internal/domain"
	"github.com/Net005/JAVBeacon/internal/scraper"
	"github.com/Net005/JAVBeacon/internal/store"
)

// newSiteScanTestService builds a Service wired to a real SQLite temp store
// and a real scraper.JavLibrary pointed at an httptest server that serves a
// one-page listing (containing a single release, "ABC-123") plus that
// release's detail page - mirroring newRefreshTestService's construction
// pattern, but for a site-level scan (run()'s Mode handling) rather than a
// single release refresh. It seeds one existing release for that video ID
// with a non-blank ReleaseDate (the skip check in run() only ever applies to
// a release that already has one), and returns the service, the site, and
// that seeded release as stored.
func newSiteScanTestService(t *testing.T, detailHandler http.HandlerFunc) (*Service, domain.Site, domain.Release) {
	t.Helper()
	mux := http.NewServeMux()
	// The listing link's href must contain "jav" - that's the substring
	// javlibrary.go's listing-item link matcher (x.Data=="a" &&
	// strings.Contains(attr(x,"href"),"jav")) requires, mirroring every
	// other listing fixture in this codebase (e.g. javlibrary_test.go's
	// "/javabc123.html").
	mux.HandleFunc("/list", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<div class="video"><a href="/javabc123.html" title="Listing title"><img src="/coverps.jpg"></a><div class="id">ABC-123</div></div>`))
	})
	mux.HandleFunc("/javabc123.html", detailHandler)
	mux.HandleFunc("/cover.jpg", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write([]byte("finished release cover"))
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "scan-mode.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	ctx := context.Background()
	site, err := st.SaveSite(ctx, domain.Site{Title: "JavLibrary", Type: "Site", Name: "JavLibrary", Enabled: true, URL: server.URL + "/list"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpsertRelease(ctx, domain.Release{
		SiteID: site.ID, VideoID: "ABC-123", Source: "JavLibrary", Title: "Old Title", ReleaseDate: "2024-01-01",
	}); err != nil {
		t.Fatal(err)
	}
	items, err := st.Releases(ctx, domain.ReleaseFilter{Search: "ABC-123"})
	if err != nil || len(items) != 1 {
		t.Fatalf("release lookup: items=%d err=%v", len(items), err)
	}

	coverCache, err := covers.New(t.TempDir(), 2*time.Second, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	javlib := scraper.NewJavLibrary(2*time.Second, "", 0, slog.Default())
	akiba := scraper.NewAkiba("", "", 2*time.Second, slog.Default())
	service := New(st, akiba, javlib, coverCache, 1, slog.Default(), time.Hour)
	return service, site, items[0]
}

// waitForSiteJob polls Status until the job is no longer running, mirroring
// waitForReleaseJob in service_refresh_test.go but for a site-level scan.
func waitForSiteJob(t *testing.T, service *Service) domain.Job {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		job := service.Status()
		if !job.Running {
			return job
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("timed out waiting for site scan job to finish")
	return domain.Job{}
}

// TestQuickModeNeverUpdatesAnExistingRelease covers TODO-2.0's Quick/Full
// refresh split: Quick refresh (Mode=="quick") must only ever add releases
// it hasn't seen before - a release the listing scan finds that already
// exists (with a release date on file) must be left untouched, no matter
// what the freshly-scraped page now says. This must hold regardless of
// Scheduled, since the old "Refresh complete existing releases" checkbox
// (and its refresh_existing setting) that used to let a *scheduled* Quick
// scan update existing releases has been removed entirely.
func TestQuickModeNeverUpdatesAnExistingRelease(t *testing.T) {
	service, site, release := newSiteScanTestService(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(javLibraryDetailFixture("New Title From Site", "2024-06-06")))
	})
	if err := service.StartOptions(context.Background(), RefreshOptions{SiteID: site.ID, Mode: "quick", Scheduled: true}); err != nil {
		t.Fatal(err)
	}
	job := waitForSiteJob(t, service)
	if job.Added != 0 || job.Updated != 0 || job.Skipped != 1 {
		t.Fatalf("job=%+v, want the existing release skipped (added=0 updated=0 skipped=1)", job)
	}
	stored, err := service.store.Release(context.Background(), release.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Title != "Old Title" || stored.ReleaseDate != "2024-01-01" {
		t.Fatalf("Quick refresh must not touch an existing release, got %+v", stored)
	}
}

// TestQuickModeSkipsExistingReleaseEvenWithoutAScrapedReleaseDate is a
// regression test for the exact bug report that prompted this fix: the
// skip check in run() used to be gated on the freshly scraped page having a
// non-blank ReleaseDate ("if options.Mode == "quick" && r.ReleaseDate !=
// """), so a detail page whose release date couldn't be parsed (or simply
// wasn't present at scrape time) fell straight through to UpsertRelease and
// got "updated" anyway - even though Quick refresh's entire contract is to
// never touch a release it already has on file. The fix replaced that gate
// with an unconditional existence check, so this must skip regardless of
// what the scraped page's release date looks like.
func TestQuickModeSkipsExistingReleaseEvenWithoutAScrapedReleaseDate(t *testing.T) {
	service, site, release := newSiteScanTestService(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(javLibraryDetailFixture("New Title From Site", "")))
	})
	if err := service.StartOptions(context.Background(), RefreshOptions{SiteID: site.ID, Mode: "quick", Scheduled: true}); err != nil {
		t.Fatal(err)
	}
	job := waitForSiteJob(t, service)
	if job.Added != 0 || job.Updated != 0 || job.Skipped != 1 {
		t.Fatalf("job=%+v, want the existing release skipped (added=0 updated=0 skipped=1)", job)
	}
	stored, err := service.store.Release(context.Background(), release.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Title != "Old Title" || stored.ReleaseDate != "2024-01-01" {
		t.Fatalf("Quick refresh must not touch an existing release just because the scraped page had no release date, got %+v", stored)
	}
}

func TestQuickModeRefreshesArtworkForAnExistingRelease(t *testing.T) {
	service, site, release := newSiteScanTestService(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(javLibraryDetailFixture("New Title From Site", "2024-06-06")))
	})
	coverPath := service.covers.Path(release.VideoID)
	if err := os.WriteFile(coverPath, []byte("now printing"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := service.StartOptions(context.Background(), RefreshOptions{SiteID: site.ID, Mode: "quick", Scheduled: true}); err != nil {
		t.Fatal(err)
	}
	job := waitForSiteJob(t, service)
	if job.Updated != 0 || job.Skipped != 1 {
		t.Fatalf("job=%+v, want metadata skipped while artwork is checked", job)
	}
	got, err := os.ReadFile(coverPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "finished release cover" {
		t.Fatalf("cached cover = %q, want refreshed artwork", got)
	}
	stored, err := service.store.Release(context.Background(), release.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Title != "Old Title" || stored.ReleaseDate != "2024-01-01" {
		t.Fatalf("Quick refresh changed metadata while refreshing artwork: %+v", stored)
	}
}

// TestFullModeUpdatesAnExistingReleaseFoundInThePageScan covers the other
// half of the Quick/Full split: Full refresh (Mode=="full") both adds new
// releases and updates every existing release its page scan finds, so the
// same listing+detail scrape that Quick refresh above left untouched must
// land here.
func TestFullModeUpdatesAnExistingReleaseFoundInThePageScan(t *testing.T) {
	service, site, release := newSiteScanTestService(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(javLibraryDetailFixture("New Title From Site", "2024-06-06")))
	})
	if err := service.StartOptions(context.Background(), RefreshOptions{SiteID: site.ID, Mode: "full", Scheduled: true}); err != nil {
		t.Fatal(err)
	}
	job := waitForSiteJob(t, service)
	if job.Updated != 1 || job.Added != 0 {
		t.Fatalf("job=%+v, want the existing release updated (added=0 updated=1)", job)
	}
	stored, err := service.store.Release(context.Background(), release.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Title != "New Title From Site" || stored.ReleaseDate != "2024-06-06" {
		t.Fatalf("Full refresh must update an existing release with the freshly scraped page, got %+v", stored)
	}
}

// TestScheduleFullOnlyStartsAScanWhenEnabled covers ScheduleFull's opt-in
// gate: with "full_refresh_enabled" left unset (its seeded default is
// "false" - see app.New), a fired tick must not start any scan at all, since
// re-scraping every existing release on a schedule is considerably heavier
// than Quick refresh and was deliberately made opt-in.
func TestScheduleFullOnlyStartsAScanWhenEnabled(t *testing.T) {
	service, _, _ := newSiteScanTestService(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(javLibraryDetailFixture("New Title From Site", "2024-06-06")))
	})
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	service.ScheduleFull(ctx, 20*time.Millisecond)
	if job := service.Status(); job.State != "idle" {
		t.Fatalf("expected no scan to have started while full_refresh_enabled is unset, got job=%+v", job)
	}
}

// TestMultiSiteScanTracksSiteIndexAndCount is the regression test for the
// scrape job progress bar feature request ("Job progress needs to display
// all the remaining monitoring sites it still has to scrape"): a job that
// scans every enabled site (RefreshOptions.SiteID == 0) must report
// job.SiteCount as the number of sites it resolved to scan up front, and
// job.SiteIndex as the 1-based position of the site currently (or, once the
// job has finished, most recently) being processed - so by the time the job
// completes, SiteIndex must equal SiteCount. A single-site job (SiteID
// explicitly set) must leave both at zero, since "1 of 1" isn't useful
// progress information and would just clutter a per-site manual refresh.
func TestMultiSiteScanTracksSiteIndexAndCount(t *testing.T) {
	service, site, _ := newSiteScanTestService(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(javLibraryDetailFixture("New Title From Site", "2024-06-06")))
	})
	ctx := context.Background()
	if _, err := service.store.SaveSite(ctx, domain.Site{Title: "Second Site", Type: "Site", Name: "JavLibrary", Enabled: true, URL: site.URL}); err != nil {
		t.Fatal(err)
	}
	if err := service.StartOptions(ctx, RefreshOptions{Mode: "quick", Scheduled: true}); err != nil {
		t.Fatal(err)
	}
	job := waitForSiteJob(t, service)
	if job.SiteCount != 2 {
		t.Fatalf("job.SiteCount = %d, want 2 (both sites enabled)", job.SiteCount)
	}
	if job.SiteIndex != 2 {
		t.Fatalf("job.SiteIndex = %d, want 2 (the last site processed, once the job has finished)", job.SiteIndex)
	}

	// A single-site job must not populate the multi-site progress fields.
	if err := service.StartOptions(ctx, RefreshOptions{SiteID: site.ID, Mode: "quick", Scheduled: true}); err != nil {
		t.Fatal(err)
	}
	single := waitForSiteJob(t, service)
	if single.SiteCount != 0 || single.SiteIndex != 0 {
		t.Fatalf("single-site job=%+v, want SiteCount=0 and SiteIndex=0", single)
	}
}

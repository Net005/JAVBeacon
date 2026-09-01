package monitor

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Net005/JAVBeacon/internal/covers"
	"github.com/Net005/JAVBeacon/internal/domain"
	"github.com/Net005/JAVBeacon/internal/scraper"
	"github.com/Net005/JAVBeacon/internal/screenshots"
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
	mux.HandleFunc("/full-shot.jpg", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write([]byte("full-size screenshot"))
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
	screenshotCache, err := screenshots.New(t.TempDir(), 2*time.Second, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	service := New(st, akiba, javlib, coverCache, 1, slog.Default(), time.Hour, screenshotCache)
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

func TestQuickModeRepairsScreenshotsWithoutUpdatingExistingMetadata(t *testing.T) {
	service, site, release := newSiteScanTestService(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(javLibraryDetailFixture("New Title From Site", "2024-06-06") + `<div class="previewthumbs"><a href="/full-shot.jpg"><img src="/thumb-shot.jpg"></a></div>`))
	})
	if err := service.StartOptions(context.Background(), RefreshOptions{SiteID: site.ID, Mode: "quick", Scheduled: true}); err != nil {
		t.Fatal(err)
	}
	job := waitForSiteJob(t, service)
	if job.Updated != 0 || job.Skipped != 1 {
		t.Fatalf("job=%+v, want metadata skipped while screenshots are repaired", job)
	}
	stored, err := service.store.Release(context.Background(), release.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Title != "Old Title" || stored.ReleaseDate != "2024-01-01" {
		t.Fatalf("Quick refresh changed metadata while repairing screenshots: %+v", stored)
	}
	if !stored.UpdatedAt.Equal(release.UpdatedAt) {
		t.Fatalf("Quick refresh artwork repair changed updated_at: before=%v after=%v", release.UpdatedAt, stored.UpdatedAt)
	}
	if len(stored.Screenshots) != 1 {
		t.Fatalf("stored screenshots=%v, want one screenshot URL", stored.Screenshots)
	}
	if info, err := os.Stat(service.covers.Path(release.VideoID)); err != nil || info.Size() == 0 {
		t.Fatalf("Quick refresh did not cache cover: info=%v err=%v", info, err)
	}
	if info, err := os.Stat(service.screenshots.Path(release.VideoID, 0)); err != nil || info.Size() == 0 {
		t.Fatalf("Quick refresh did not cache screenshot: info=%v err=%v", info, err)
	}
}

// TestQuickModeBackfillsBlankMetadataFieldsFoundOnTheDetailPage is the
// regression test for the bug report that "Quick and Full scan jobs don't
// fill in the previously missing Label field" - Quick mode's existing-
// release branch above intentionally never *overwrites* metadata (see
// TestQuickModeNeverUpdatesAnExistingRelease), but that used to mean it
// never touched Label/Studio/Genres/release-date at all, even when they
// were simply blank (most commonly because the release predates
// JavLibrary's Label parsing and has never been re-scraped since). Quick
// has already fetched the detail page by this point, so it now fills in
// exactly the fields that are still blank, leaving Title (untouched by
// design - see the "never updates an existing release" tests above) and
// any already-populated field exactly as they were.
func TestQuickModeBackfillsBlankMetadataFieldsFoundOnTheDetailPage(t *testing.T) {
	detail := `<html><title>New Title From Site - JAVLibrary</title><img id="video_jacket_img" src="/cover.jpg">` +
		`<table><tr><td class="header">Release Date:</td><td class="text">2024-06-06</td></tr>` +
		`<tr><td class="header">Length:</td><td>90 min(s)</td></tr>` +
		`<tr><td class="header">Maker:</td><td><a>Attackers</a></td></tr>` +
		`<tr><td class="header">Label:</td><td><a>Otona No Drama</a></td></tr></table>` +
		`<div class="director"><a>Director Name</a></div>` +
		`<div id="video_genres"><span class="genre"><a>Drama</a></span></div></html>`
	service, site, release := newSiteScanTestService(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(detail))
	})
	if release.Label != "" || len(release.Genres) != 0 || release.Studio != "" {
		t.Fatalf("seeded release=%+v, want Label/Genres/Studio blank so this test actually exercises the backfill", release)
	}
	if err := service.StartOptions(context.Background(), RefreshOptions{SiteID: site.ID, Mode: "quick", Scheduled: true}); err != nil {
		t.Fatal(err)
	}
	job := waitForSiteJob(t, service)
	if job.Updated != 0 || job.Skipped != 1 {
		t.Fatalf("job=%+v, want the release counted as skipped (backfill is not a metadata \"update\")", job)
	}
	stored, err := service.store.Release(context.Background(), release.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Label != "Otona No Drama" {
		t.Fatalf("stored.Label = %q, want the blank Label backfilled from the detail page", stored.Label)
	}
	if stored.Studio != "Attackers" {
		t.Fatalf("stored.Studio = %q, want the blank Studio backfilled from the detail page", stored.Studio)
	}
	if len(stored.Genres) != 1 || stored.Genres[0] != "Drama" {
		t.Fatalf("stored.Genres = %v, want the blank Genres backfilled from the detail page", stored.Genres)
	}
	// Title is handled separately (populated from the listing page for every
	// mode) and is deliberately left out of the backfill-if-blank set above
	// since it is never blank for an existing release in the first place -
	// Quick's "never touch what a release already has" contract for it is
	// covered by TestQuickModeNeverUpdatesAnExistingRelease.
	if stored.Title != "Old Title" {
		t.Fatalf("stored.Title = %q, want Quick refresh to leave the already-populated Title alone", stored.Title)
	}
	if !stored.UpdatedAt.Equal(release.UpdatedAt) {
		t.Fatalf("Quick refresh metadata backfill changed updated_at: before=%v after=%v", release.UpdatedAt, stored.UpdatedAt)
	}
}

// TestQuickModeNeverOverwritesAnAlreadyPopulatedMetadataField is the
// counterpart to the backfill test above: a field the release already has a
// value for must be left exactly as it was, even though Quick has fetched a
// detail page reporting something different for it - the backfill only ever
// fills in a currently-blank field, it never corrects/replaces one that
// already has a value (that remains Full refresh's job).
func TestQuickModeNeverOverwritesAnAlreadyPopulatedMetadataField(t *testing.T) {
	detail := `<html><title>New Title From Site - JAVLibrary</title><img id="video_jacket_img" src="/cover.jpg">` +
		`<table><tr><td class="header">Release Date:</td><td class="text">2024-06-06</td></tr>` +
		`<tr><td class="header">Label:</td><td><a>New Label From Site</a></td></tr></table></html>`
	service, site, release := newSiteScanTestService(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(detail))
	})
	if _, err := service.store.UpsertRelease(context.Background(), domain.Release{
		SiteID: site.ID, VideoID: release.VideoID, Source: release.Source, Label: "Original Label",
	}); err != nil {
		t.Fatal(err)
	}
	if err := service.StartOptions(context.Background(), RefreshOptions{SiteID: site.ID, Mode: "quick", Scheduled: true}); err != nil {
		t.Fatal(err)
	}
	job := waitForSiteJob(t, service)
	if job.Updated != 0 || job.Skipped != 1 {
		t.Fatalf("job=%+v, want the release counted as skipped", job)
	}
	stored, err := service.store.Release(context.Background(), release.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Label != "Original Label" {
		t.Fatalf("stored.Label = %q, want Quick refresh to leave an already-populated Label untouched", stored.Label)
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

func TestFullModeCachesScreenshotsForAnExistingRelease(t *testing.T) {
	service, site, release := newSiteScanTestService(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(javLibraryDetailFixture("Old Title", "2024-01-01") + `<div class="previewthumbs"><a href="/full-shot.jpg"><img src="/thumb-shot.jpg"></a></div>`))
	})
	productURL := site.URL[:len(site.URL)-len("/list")] + "/javabc123.html"
	if _, err := service.store.UpsertRelease(context.Background(), domain.Release{
		SiteID: site.ID, VideoID: release.VideoID, ScraperID: "javabc123", Title: "Old Title", ReleaseDate: "2024-01-01",
		Source: "JavLibrary", ProductURL: productURL, Director: "Director Name", Duration: "90 min", Released: true,
	}); err != nil {
		t.Fatal(err)
	}
	var err error
	release, err = service.store.Release(context.Background(), release.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.StartOptions(context.Background(), RefreshOptions{SiteID: site.ID, Mode: "full", Scheduled: true}); err != nil {
		t.Fatal(err)
	}
	job := waitForSiteJob(t, service)
	if job.Updated != 1 || job.Error != "" {
		t.Fatalf("job=%+v, want one successful full-refresh update", job)
	}
	stored, err := service.store.Release(context.Background(), release.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.Screenshots) != 1 {
		t.Fatalf("stored screenshots=%v, want one screenshot URL", stored.Screenshots)
	}
	if !stored.UpdatedAt.Equal(release.UpdatedAt) {
		t.Fatalf("Full refresh artwork-only update changed updated_at: before=%v after=%v", release.UpdatedAt, stored.UpdatedAt)
	}
	if info, err := os.Stat(service.covers.Path(release.VideoID)); err != nil || info.Size() == 0 {
		t.Fatalf("Full refresh did not cache cover: info=%v err=%v", info, err)
	}
	if info, err := os.Stat(service.screenshots.Path(release.VideoID, 0)); err != nil || info.Size() == 0 {
		t.Fatalf("Full refresh did not cache screenshot: info=%v err=%v", info, err)
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
	const scheduledTitle = "New releases only · all enabled sites"
	if err := service.StartOptions(ctx, RefreshOptions{Mode: "quick", Title: scheduledTitle, Scheduled: true}); err != nil {
		t.Fatal(err)
	}
	job := waitForSiteJob(t, service)
	if job.SiteCount != 2 {
		t.Fatalf("job.SiteCount = %d, want 2 (both sites enabled)", job.SiteCount)
	}
	if job.SiteIndex != 2 {
		t.Fatalf("job.SiteIndex = %d, want 2 (the last site processed, once the job has finished)", job.SiteIndex)
	}
	history, err := service.store.Jobs(ctx, 1)
	if err != nil || len(history) != 1 {
		t.Fatalf("saved job history=%+v err=%v", history, err)
	}
	if history[0].Title != scheduledTitle || !history[0].Scheduled || history[0].SiteCount != 2 {
		t.Fatalf("saved scheduled aggregate=%+v, want title=%q, scheduled=true, site_count=2", history[0], scheduledTitle)
	}
	if history[0].SiteTitle == history[0].Title {
		t.Fatalf("saved aggregate title was overwritten by live site progress: %+v", history[0])
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

// TestFullModeSurfacesDetailFetchFailuresInsteadOfSilentlySucceeding is the
// regression test for the exact bug report this covers: "Full/Quick scans
// don't fill in Label or fetch missing screenshots, even though a manual
// Update details on the same release works fine." Manual refresh and scan
// share the same detail-page parser, so a real markup gap would break both
// - the actual gap was that scrapeFiltered's concurrent detail-page fetch
// (javlibrary.go) swallowed a failed fetch into just a WARN log line and
// still fell through to add/"update" the release from listing-page data
// alone (title/cover only), so a struggling solver produced a completely
// normal-looking "N updated" job while refreshing nothing. This asserts
// that a failed detail fetch is now visible on the job (job.Error) instead
// of silently reporting success, and that the release's pre-existing
// fields (seeded with a blank Label, mirroring a release that predates the
// Label fix and still hasn't been backfilled) are left untouched rather
// than being overwritten with blanks.
func TestFullModeSurfacesDetailFetchFailuresInsteadOfSilentlySucceeding(t *testing.T) {
	service, site, _ := newSiteScanTestService(t, func(w http.ResponseWriter, _ *http.Request) {
		// Simulate a Byparr/solver failure on the detail-page fetch (a
		// timeout, an overloaded solver, a still-challenged response that
		// documentOnce's own validation rejects, etc.) - any non-2xx status
		// is enough to make javlibrary.go's j.detail() return an error.
		http.Error(w, "solver unavailable", http.StatusBadGateway)
	})
	origFirst, origSecond := scraper.RetryFirstBackoff, scraper.RetrySecondBackoff
	scraper.RetryFirstBackoff, scraper.RetrySecondBackoff = time.Millisecond, time.Millisecond
	t.Cleanup(func() { scraper.RetryFirstBackoff, scraper.RetrySecondBackoff = origFirst, origSecond })

	ctx := context.Background()
	if err := service.StartOptions(ctx, RefreshOptions{SiteID: site.ID, Mode: "full", Scheduled: true}); err != nil {
		t.Fatal(err)
	}
	job := waitForSiteJob(t, service)
	if job.Error == "" {
		t.Fatal("job.Error is empty, want a summary reporting the failed detail-page fetch instead of silent success")
	}
	if !strings.Contains(job.Error, "ABC-123") && !strings.Contains(job.Error, "1 of 1") {
		t.Fatalf("job.Error = %q, want it to identify the failed release/count", job.Error)
	}
	stored, err := service.store.Release(ctx, mustReleaseID(t, service, "ABC-123"))
	if err != nil {
		t.Fatal(err)
	}
	// Title/cover come from the listing page too, so those legitimately
	// still refresh even on a failed detail fetch - Label (like Studio,
	// Genres, ReleaseDate, and Screenshots) only ever comes from the detail
	// page, so it must be left exactly as seeded (blank) rather than
	// silently "succeeding" with nothing actually merged in.
	if stored.Label != "" {
		t.Fatalf("stored.Label = %q, want it left blank since the detail-page fetch that would have populated it failed", stored.Label)
	}
}

func mustReleaseID(t *testing.T, service *Service, videoID string) int64 {
	t.Helper()
	rows, err := service.store.Releases(context.Background(), domain.ReleaseFilter{Search: videoID})
	if err != nil || len(rows) != 1 {
		t.Fatalf("release lookup for %s: items=%d err=%v", videoID, len(rows), err)
	}
	return rows[0].ID
}

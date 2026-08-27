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
	"github.com/Net005/JAVBeacon/internal/screenshots"
	"github.com/Net005/JAVBeacon/internal/store"
)

// javLibraryDetailFixture returns a minimal but structurally valid JavLibrary
// detail page (same shape javlibrary_test.go's fixtures use), so a caller can
// vary the title/date without duplicating the whole page markup. It is
// intentionally free of extraneous whitespace so a stored, store-normalized
// release compares equal to a freshly re-parsed one -- used by the "no
// change" test below to prove a second identical scrape does not report a
// spurious update.
func javLibraryDetailFixture(title, releaseDate string) string {
	return `<html><title>` + title + ` - JAVLibrary</title><img id="video_jacket_img" src="/cover.jpg">` +
		`<table><tr><td class="header">Release Date:</td><td class="text">` + releaseDate + `</td></tr>` +
		`<tr><td class="header">Length:</td><td>90 min(s)</td></tr></table>` +
		`<div class="director"><a>Director Name</a></div></html>`
}

// newRefreshTestService builds a Service wired to a real SQLite temp store
// and a real scraper.JavLibrary pointed at an httptest server, mirroring the
// construction pattern already used for other monitor-package tests
// (store.OpenSQLite in a temp dir) combined with the httptest.Server +
// real-provider pattern from internal/scraper/javlibrary_test.go. It seeds
// one placeholder release (no scraped detail fields set yet) whose
// ProductURL points at the given mux's "/detail.html" route, and returns the
// service plus that release as stored (with its assigned ID).
func newRefreshTestService(t *testing.T, mux *http.ServeMux) (*Service, domain.Release) {
	t.Helper()
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "refresh.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	ctx := context.Background()
	site, err := st.SaveSite(ctx, domain.Site{Title: "JavLibrary", Type: "Site", Name: "JavLibrary", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpsertRelease(ctx, domain.Release{
		SiteID: site.ID, VideoID: "ABC-123", Source: "JavLibrary",
		ProductURL: server.URL + "/detail.html",
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
	return service, items[0]
}

func TestRegularJavLibraryDetailRefreshCachesScreenshots(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/detail.html", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(javLibraryDetailFixture("Screenshot Title", "2024-02-03") + `<div class="previewthumbs"><a href="/full-shot.jpg"><img src="/thumb-shot.jpg"></a></div>`))
	})
	mux.HandleFunc("/full-shot.jpg", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write([]byte("full-size-screenshot"))
	})
	service, release := newRefreshTestService(t, mux)

	if err := service.StartOptions(context.Background(), RefreshOptions{ReleaseID: release.ID}); err != nil {
		t.Fatal(err)
	}
	if job := waitForReleaseJob(t, service, release.ID); job.Error != "" {
		t.Fatalf("refresh failed: %+v", job)
	}
	stored, err := service.store.Release(context.Background(), release.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.Screenshots) != 1 {
		t.Fatalf("stored screenshots=%v, want one full-size screenshot", stored.Screenshots)
	}
	if info, err := os.Stat(service.screenshots.Path(release.VideoID, 0)); err != nil || info.Size() == 0 {
		t.Fatalf("regular detail refresh did not cache screenshot: info=%v err=%v", info, err)
	}
}

// waitForReleaseJob polls StatusForRelease until the job is no longer
// running (StartOptions runs the scrape asynchronously via a goroutine), so
// tests observe the final Stage/Outcome rather than a snapshot mid-flight.
func waitForReleaseJob(t *testing.T, service *Service, releaseID int64) domain.Job {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		job := service.StatusForRelease(releaseID)
		if !job.Running {
			return job
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("timed out waiting for release refresh job to finish")
	return domain.Job{}
}

// TestServiceRefreshReleaseReportsUpdatedOutcome covers Phase 12's "metadata
// updated" outcome: refreshing a release whose stored detail fields are
// still blank against a page with real content must report Outcome=="updated"
// and land the scraped fields in the store, and Stage must reach
// "completed".
func TestServiceRefreshReleaseReportsUpdatedOutcome(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/detail.html", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(javLibraryDetailFixture("Detail Title", "2024-02-03")))
	})
	service, release := newRefreshTestService(t, mux)

	if err := service.StartOptions(context.Background(), RefreshOptions{ReleaseID: release.ID}); err != nil {
		t.Fatal(err)
	}
	job := waitForReleaseJob(t, service, release.ID)
	if job.Outcome != "updated" {
		t.Fatalf("outcome=%q, want updated (job=%+v)", job.Outcome, job)
	}
	if job.Stage != "completed" {
		t.Fatalf("stage=%q, want completed", job.Stage)
	}
	if job.Error != "" {
		t.Fatalf("unexpected error: %q", job.Error)
	}
	stored, err := service.store.Release(context.Background(), release.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Title != "Detail Title" || stored.ReleaseDate != "2024-02-03" || stored.Director != "Director Name" {
		t.Fatalf("scraped fields were not saved: %+v", stored)
	}
}

// TestServiceRefreshReleaseReportsNoChangeOutcome covers Phase 12's "no new
// information found" outcome. The first refresh primes the store with the
// scraped detail (and is not itself asserted on); the second refresh scrapes
// the exact same page again and must report Outcome=="no_change" since
// nothing about the release actually changed.
func TestServiceRefreshReleaseReportsNoChangeOutcome(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/detail.html", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(javLibraryDetailFixture("Stable Title", "2024-02-03")))
	})
	service, release := newRefreshTestService(t, mux)

	if err := service.StartOptions(context.Background(), RefreshOptions{ReleaseID: release.ID}); err != nil {
		t.Fatal(err)
	}
	if job := waitForReleaseJob(t, service, release.ID); job.Outcome != "updated" {
		t.Fatalf("priming refresh outcome=%q, want updated (job=%+v)", job.Outcome, job)
	}

	if err := service.StartOptions(context.Background(), RefreshOptions{ReleaseID: release.ID}); err != nil {
		t.Fatal(err)
	}
	job := waitForReleaseJob(t, service, release.ID)
	if job.Outcome != "no_change" {
		t.Fatalf("outcome=%q, want no_change (job=%+v)", job.Outcome, job)
	}
	if job.Stage != "completed" {
		t.Fatalf("stage=%q, want completed", job.Stage)
	}
}

// TestServiceRefreshReleaseReportsBlockedOutcome covers Phase 12's "scrape
// blocked" outcome: a Cloudflare-interstitial-shaped response (validated by
// scraper.validatePage the same way a real Cloudflare challenge is) must
// surface as Outcome=="blocked" with a populated Error, not a generic
// "failed". A blocked response is retried (TODO-2.0's scraping retry
// requirement), so this shrinks the scraper package's retry backoff to
// near-zero for the duration of the test - otherwise it would need to wait
// out the real several-second backoff before the job finishes.
func TestServiceRefreshReleaseReportsBlockedOutcome(t *testing.T) {
	origFirst, origSecond := scraper.RetryFirstBackoff, scraper.RetrySecondBackoff
	scraper.RetryFirstBackoff, scraper.RetrySecondBackoff = time.Millisecond, time.Millisecond
	t.Cleanup(func() { scraper.RetryFirstBackoff, scraper.RetrySecondBackoff = origFirst, origSecond })

	mux := http.NewServeMux()
	mux.HandleFunc("/detail.html", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<html><head><title>Just a moment...</title></head><body><div id="challenge-form"></div></body></html>`))
	})
	service, release := newRefreshTestService(t, mux)

	if err := service.StartOptions(context.Background(), RefreshOptions{ReleaseID: release.ID}); err != nil {
		t.Fatal(err)
	}
	job := waitForReleaseJob(t, service, release.ID)
	if job.Outcome != "blocked" {
		t.Fatalf("outcome=%q, want blocked (job=%+v)", job.Outcome, job)
	}
	if job.Error == "" {
		t.Fatal("expected a populated error for a blocked outcome")
	}
}

// TestServiceRefreshReleaseReportsInvalidOutcome covers Phase 12's "scrape
// invalid" outcome: a page that is not a Cloudflare challenge but also does
// not contain the structural markers a JavLibrary detail page must have
// (validatePage's pagePresence check) must surface as Outcome=="invalid".
func TestServiceRefreshReleaseReportsInvalidOutcome(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/detail.html", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<html><body><p>This page moved.</p></body></html>`))
	})
	service, release := newRefreshTestService(t, mux)

	if err := service.StartOptions(context.Background(), RefreshOptions{ReleaseID: release.ID}); err != nil {
		t.Fatal(err)
	}
	job := waitForReleaseJob(t, service, release.ID)
	if job.Outcome != "invalid" {
		t.Fatalf("outcome=%q, want invalid (job=%+v)", job.Outcome, job)
	}
	if job.Error == "" {
		t.Fatal("expected a populated error for an invalid outcome")
	}
}

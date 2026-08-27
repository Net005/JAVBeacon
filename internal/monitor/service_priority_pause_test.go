package monitor

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Net005/JAVBeacon/internal/covers"
	"github.com/Net005/JAVBeacon/internal/domain"
	"github.com/Net005/JAVBeacon/internal/scraper"
	"github.com/Net005/JAVBeacon/internal/store"
)

// TestHigherPriorityJobPausesAndResumesLowerPriorityScan is the regression
// test for the job-priority preemption feature: a low-priority, multi-item
// site scan that's already mid-run must pause - after finishing whichever
// release detail page it's currently on, before starting the next one - as
// soon as a higher-priority job (here, a single release's "update details"
// job) is queued behind it, run that job to completion first, and then
// resume the scan exactly where it left off, still completing both of its
// own items with nothing lost or duplicated.
//
// It proves this with a shared, mutex-protected order log that each detail
// handler appends its name to: AAA-001's handler blocks until the test has
// queued the high-priority job, so the test can be sure the scan is
// mid-item when it enqueues; the high-priority release's handler also
// blocks, long enough for the test to observe job.Paused/PausedFor while
// it's in flight. The asserted order - item1, then highpri, then item2 -
// is only possible if checkpoint actually preempted the scan between items
// instead of letting it run straight through.
func TestHigherPriorityJobPausesAndResumesLowerPriorityScan(t *testing.T) {
	var mu sync.Mutex
	var order []string
	record := func(name string) {
		mu.Lock()
		order = append(order, name)
		mu.Unlock()
	}
	hasRecorded := func(name string) bool {
		mu.Lock()
		defer mu.Unlock()
		for _, n := range order {
			if n == name {
				return true
			}
		}
		return false
	}

	item1Block := make(chan struct{})
	highpriBlock := make(chan struct{})

	mux := http.NewServeMux()
	// Two-item listing page for the low-priority site scan.
	mux.HandleFunc("/list", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(
			`<div class="video"><a href="/javaaa001.html" title="Listing title"><img src="/cover.jpg"></a><div class="id">AAA-001</div></div>` +
				`<div class="video"><a href="/javaaa002.html" title="Listing title"><img src="/cover.jpg"></a><div class="id">AAA-002</div></div>`))
	})
	mux.HandleFunc("/javaaa001.html", func(w http.ResponseWriter, _ *http.Request) {
		record("item1")
		<-item1Block
		_, _ = w.Write([]byte(javLibraryDetailFixture("AAA-001 Title", "2024-01-01")))
	})
	mux.HandleFunc("/javaaa002.html", func(w http.ResponseWriter, _ *http.Request) {
		record("item2")
		_, _ = w.Write([]byte(javLibraryDetailFixture("AAA-002 Title", "2024-01-02")))
	})
	// The high-priority job's own release detail page, on the same server.
	mux.HandleFunc("/highpri-detail.html", func(w http.ResponseWriter, _ *http.Request) {
		record("highpri")
		<-highpriBlock
		_, _ = w.Write([]byte(javLibraryDetailFixture("High Priority Updated Title", "2024-03-03")))
	})
	mux.HandleFunc("/cover.jpg", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write([]byte("cover bytes"))
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "priority-pause.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	ctx := context.Background()
	site, err := st.SaveSite(ctx, domain.Site{Title: "JavLibrary", Type: "Site", Name: "JavLibrary", Enabled: true, URL: server.URL + "/list"})
	if err != nil {
		t.Fatal(err)
	}
	// The release the high-priority job will refresh - pre-existing, and
	// unrelated to the two items the site scan is about to discover.
	highpriRelease, err := st.UpsertRelease(ctx, domain.Release{
		SiteID: site.ID, VideoID: "ZZZ-999", Source: "JavLibrary", Title: "Old Priority Title",
		ProductURL: server.URL + "/highpri-detail.html",
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = highpriRelease

	coverCache, err := covers.New(t.TempDir(), 2*time.Second, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	javlib := scraper.NewJavLibrary(2*time.Second, "", 0, slog.Default())
	akiba := scraper.NewAkiba("", "", 2*time.Second, slog.Default())
	service := New(st, akiba, javlib, coverCache, 1, slog.Default(), time.Hour)

	saved, err := st.Releases(ctx, domain.ReleaseFilter{Search: "ZZZ-999"})
	if err != nil || len(saved) != 1 {
		t.Fatalf("release lookup: items=%d err=%v", len(saved), err)
	}
	highpriID := saved[0].ID

	// Start the low-priority multi-site-capable scan (numerically large
	// Priority = low priority, matching the "lower number runs first"
	// scheme already covered by TestRefreshQueueOrdersPriorityAndPreservesFIFO).
	if err := service.StartOptions(ctx, RefreshOptions{SiteID: site.ID, Mode: "quick", Priority: 900, Title: "Low priority site scan"}); err != nil {
		t.Fatal(err)
	}

	// Wait until the scan is confirmed mid-fetch of item 1 before queuing
	// the higher-priority job, so we know for certain the preemption has to
	// happen between items rather than before the job even started.
	deadline := time.Now().Add(5 * time.Second)
	for !hasRecorded("item1") {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for item1 detail fetch to start")
		}
		time.Sleep(2 * time.Millisecond)
	}

	if err := service.StartOptions(ctx, RefreshOptions{ReleaseID: highpriID, Priority: 1, Title: "High priority release update"}); err != nil {
		t.Fatal(err)
	}

	// Let item1's detail fetch complete; the scan's next progress callback
	// (for item 2) is where checkpoint() should notice the queued
	// high-priority job and preempt.
	close(item1Block)

	// While the high-priority job is in flight, the paused scan's job
	// status should say so.
	pausedSeen := false
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		job := service.Status()
		if job.Paused && job.PausedFor != "" {
			pausedSeen = true
			break
		}
		if hasRecorded("item2") {
			// The scan already moved past the checkpoint - too late to
			// observe the pause flag; fall through and let the order-log
			// assertion below catch a real ordering failure.
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	if !pausedSeen {
		t.Fatal("expected job.Paused to be true (with a PausedFor reason) while the higher-priority job was running")
	}

	close(highpriBlock)

	job := waitForSiteJob(t, service)

	if job.Paused {
		t.Fatalf("job.Paused should be cleared again once resumed and finished, got %+v", job)
	}
	if job.Added != 2 {
		t.Fatalf("job.Added = %d, want 2 (both AAA-001 and AAA-002 added despite the pause)", job.Added)
	}

	mu.Lock()
	gotOrder := append([]string{}, order...)
	mu.Unlock()
	wantOrder := []string{"item1", "highpri", "item2"}
	if len(gotOrder) != len(wantOrder) {
		t.Fatalf("order = %v, want %v", gotOrder, wantOrder)
	}
	for i := range wantOrder {
		if gotOrder[i] != wantOrder[i] {
			t.Fatalf("order = %v, want %v (the high-priority job must run strictly between item1 and item2)", gotOrder, wantOrder)
		}
	}

	updated, err := service.store.Release(ctx, highpriID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Title != "High Priority Updated Title" {
		t.Fatalf("high-priority release title = %q, want it refreshed while the scan was paused", updated.Title)
	}
}

// TestHigherPriorityJobPausesDuringCoverAndScreenshotDownloadPhase covers the
// second checkpoint site: run()'s detail-page scrape for a whole site
// finishes in one call (ScrapeFiltered/ScrapeFilteredThroughEnd returns only
// once every item's detail page has been fetched), and only then does run()
// loop back over those items to download each one's cover/screenshots and
// write it to the store - a separate, potentially slow pass with its own
// need to yield between releases. This is a regression test for that pass
// specifically: both AAA-101 and AAA-102's detail pages are served
// immediately (so the scrape-phase checkpoint above never has anything to
// preempt), but AAA-101's cover image download blocks until the test
// queues the higher-priority job - proving the write/download loop's own
// checkpoint (not the scrape-phase one) is what catches it.
func TestHigherPriorityJobPausesDuringCoverAndScreenshotDownloadPhase(t *testing.T) {
	var mu sync.Mutex
	var order []string
	record := func(name string) {
		mu.Lock()
		order = append(order, name)
		mu.Unlock()
	}
	hasRecorded := func(name string) bool {
		mu.Lock()
		defer mu.Unlock()
		for _, n := range order {
			if n == name {
				return true
			}
		}
		return false
	}

	cover1Block := make(chan struct{})

	detailPage := func(title, releaseDate, coverPath string) string {
		return `<html><title>` + title + ` - JAVLibrary</title><img id="video_jacket_img" src="` + coverPath + `">` +
			`<table><tr><td class="header">Release Date:</td><td class="text">` + releaseDate + `</td></tr>` +
			`<tr><td class="header">Length:</td><td>90 min(s)</td></tr></table>` +
			`<div class="director"><a>Director Name</a></div></html>`
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/list", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(
			`<div class="video"><a href="/javaaa101.html" title="Listing title"><img src="/cover1.jpg"></a><div class="id">AAA-101</div></div>` +
				`<div class="video"><a href="/javaaa102.html" title="Listing title"><img src="/cover2.jpg"></a><div class="id">AAA-102</div></div>`))
	})
	mux.HandleFunc("/javaaa101.html", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(detailPage("AAA-101 Title", "2024-01-01", "/cover1.jpg")))
	})
	mux.HandleFunc("/javaaa102.html", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(detailPage("AAA-102 Title", "2024-01-02", "/cover2.jpg")))
	})
	mux.HandleFunc("/highpri-detail.html", func(w http.ResponseWriter, _ *http.Request) {
		record("highpri")
		_, _ = w.Write([]byte(javLibraryDetailFixture("High Priority Updated Title", "2024-03-03")))
	})
	mux.HandleFunc("/cover1.jpg", func(w http.ResponseWriter, _ *http.Request) {
		record("cover1-fetch-start")
		<-cover1Block
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write([]byte("cover1 bytes"))
		record("cover1-fetch-done")
	})
	mux.HandleFunc("/cover2.jpg", func(w http.ResponseWriter, _ *http.Request) {
		record("cover2-fetch")
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write([]byte("cover2 bytes"))
	})
	mux.HandleFunc("/cover.jpg", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write([]byte("highpri cover bytes"))
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "priority-pause-write.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	ctx := context.Background()
	site, err := st.SaveSite(ctx, domain.Site{Title: "JavLibrary", Type: "Site", Name: "JavLibrary", Enabled: true, URL: server.URL + "/list"})
	if err != nil {
		t.Fatal(err)
	}
	highpriRelease, err := st.UpsertRelease(ctx, domain.Release{
		SiteID: site.ID, VideoID: "ZZZ-888", Source: "JavLibrary", Title: "Old Priority Title",
		ProductURL: server.URL + "/highpri-detail.html",
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = highpriRelease

	coverCache, err := covers.New(t.TempDir(), 2*time.Second, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	javlib := scraper.NewJavLibrary(2*time.Second, "", 0, slog.Default())
	akiba := scraper.NewAkiba("", "", 2*time.Second, slog.Default())
	service := New(st, akiba, javlib, coverCache, 1, slog.Default(), time.Hour)

	saved, err := st.Releases(ctx, domain.ReleaseFilter{Search: "ZZZ-888"})
	if err != nil || len(saved) != 1 {
		t.Fatalf("release lookup: items=%d err=%v", len(saved), err)
	}
	highpriID := saved[0].ID

	if err := service.StartOptions(ctx, RefreshOptions{SiteID: site.ID, Mode: "quick", Priority: 900, Title: "Low priority site scan"}); err != nil {
		t.Fatal(err)
	}

	// Wait until AAA-101's cover download is in flight - by this point both
	// detail pages have already been fetched (the scrape phase is over),
	// so any preemption from here on can only come from the write/download
	// loop's own checkpoint.
	deadline := time.Now().Add(5 * time.Second)
	for !hasRecorded("cover1-fetch-start") {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for AAA-101's cover download to start")
		}
		time.Sleep(2 * time.Millisecond)
	}

	if err := service.StartOptions(ctx, RefreshOptions{ReleaseID: highpriID, Priority: 1, Title: "High priority release update"}); err != nil {
		t.Fatal(err)
	}

	close(cover1Block)

	job := waitForSiteJob(t, service)
	if job.Added != 2 {
		t.Fatalf("job.Added = %d, want 2 (both AAA-101 and AAA-102 added despite the pause)", job.Added)
	}

	mu.Lock()
	gotOrder := append([]string{}, order...)
	mu.Unlock()
	wantOrder := []string{"cover1-fetch-start", "cover1-fetch-done", "highpri", "cover2-fetch"}
	if len(gotOrder) != len(wantOrder) {
		t.Fatalf("order = %v, want %v", gotOrder, wantOrder)
	}
	for i := range wantOrder {
		if gotOrder[i] != wantOrder[i] {
			t.Fatalf("order = %v, want %v (the high-priority job must run strictly between AAA-101's and AAA-102's cover downloads)", gotOrder, wantOrder)
		}
	}

	updated, err := service.store.Release(ctx, highpriID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Title != "High Priority Updated Title" {
		t.Fatalf("high-priority release title = %q, want it refreshed while the scan was paused", updated.Title)
	}
}

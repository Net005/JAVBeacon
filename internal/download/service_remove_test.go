package download

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Net005/JAVBeacon/internal/domain"
	"github.com/Net005/JAVBeacon/internal/store"
)

func TestRemoveDownloadRemovesTorrentAndAllReleaseHistory(t *testing.T) {
	var removed atomic.Bool
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v2/auth/login", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /api/v2/torrents/info", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[{"hash":"abc123","name":"PRED-888 trusted release"}]`))
	})
	mux.HandleFunc("POST /api/v2/torrents/delete", func(w http.ResponseWriter, r *http.Request) {
		if r.FormValue("hashes") != "abc123" || r.FormValue("deleteFiles") != "false" {
			t.Errorf("unexpected delete form: hashes=%q deleteFiles=%q", r.FormValue("hashes"), r.FormValue("deleteFiles"))
		}
		removed.Store(true)
		w.WriteHeader(http.StatusNoContent)
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "remove-download.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.SaveSettings(ctx, map[string]string{"qb_url": server.URL}); err != nil {
		t.Fatal(err)
	}
	site, _ := st.SaveSite(ctx, domain.Site{Title: "Test", Type: "Site", Name: "JavLibrary", Enabled: true})
	_, _ = st.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "PRED-888", Title: "Test", Source: "JavLibrary", Released: true})
	releases, _ := st.Releases(ctx, domain.ReleaseFilter{Limit: 10})
	active, _ := st.SaveDownload(ctx, domain.Download{ReleaseID: releases[0].ID, Query: "PRED-888", TorrentHash: "abc123", Status: "downloading"})
	_, _ = st.SaveDownload(ctx, domain.Download{ReleaseID: releases[0].ID, Query: "PRED-888", Status: "search_accepted"})
	_, _ = st.CreateNotification(ctx, releases[0].ID, "new_release", "New release")
	_, _ = st.CreateNotification(ctx, releases[0].ID, "download_started", "Started")
	_, _ = st.CreateNotification(ctx, releases[0].ID, "download_failed", "Failed")

	deleted, err := New(st, time.Second, slog.Default()).RemoveDownload(ctx, active.ID)
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 2 || !removed.Load() {
		t.Fatalf("deleted=%d torrent_removed=%v", deleted, removed.Load())
	}
	rows, err := st.Downloads(ctx, "")
	if err != nil || len(rows) != 0 {
		t.Fatalf("history remained: rows=%+v err=%v", rows, err)
	}
	notifications, err := st.Notifications(ctx, "")
	if err != nil || len(notifications) != 1 || notifications[0].Type != "new_release" {
		t.Fatalf("download notifications were not cleared cleanly: rows=%+v err=%v", notifications, err)
	}
}

func TestRemoveActiveHTTPDownloadCancelsTransferWithoutQBittorrent(t *testing.T) {
	var qbRequests atomic.Int64
	var pikPakDeletes atomic.Int64
	var deletedPikPakFile atomic.Value
	qb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		qbRequests.Add(1)
		http.Error(w, "qBittorrent unavailable", http.StatusBadGateway)
	}))
	defer qb.Close()

	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "remove-active-http.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.SaveSettings(ctx, map[string]string{"qb_url": qb.URL}); err != nil {
		t.Fatal(err)
	}
	site, _ := st.SaveSite(ctx, domain.Site{Title: "Test", Type: "Site", Name: "JavLibrary", Enabled: true})
	_, _ = st.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "HTTP-200", Title: "Test", Source: "JavLibrary", Released: true})
	releases, _ := st.Releases(ctx, domain.ReleaseFilter{Limit: 10})
	active, _ := st.SaveDownload(ctx, domain.Download{ReleaseID: releases[0].ID, Query: "HTTP-200", Transport: "http", Status: "downloading", RestoredFileID: "restored-http-200", RestoredFileOwned: true})
	_, _ = st.SaveDownload(ctx, domain.Download{ReleaseID: releases[0].ID, Query: "HTTP-200", Transport: "http", Status: "search_accepted"})
	retained, _ := st.SaveDownload(ctx, domain.Download{ReleaseID: releases[0].ID, Query: "HTTP-200", Transport: "torrent", Status: "completed", TorrentHash: "keep-torrent-history"})

	runCtx, cancel := context.WithCancel(context.Background())
	run := &httpDownloadRun{cancel: cancel, done: make(chan struct{})}
	go func() {
		<-runCtx.Done()
		close(run.done)
	}()
	service := &Service{store: st, client: qb.Client(), log: slog.Default(), httpRuns: map[int64]*httpDownloadRun{active.ID: run}}
	service.pikPakDeleteFile = func(_ context.Context, fileID string) error {
		pikPakDeletes.Add(1)
		deletedPikPakFile.Store(fileID)
		return nil
	}
	deleted, err := service.RemoveDownload(ctx, active.ID)
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 2 {
		t.Fatalf("deleted=%d, want both HTTP history rows", deleted)
	}
	if qbRequests.Load() != 0 {
		t.Fatalf("HTTP removal contacted qBittorrent %d time(s)", qbRequests.Load())
	}
	if pikPakDeletes.Load() != 1 || deletedPikPakFile.Load() != "restored-http-200" {
		t.Fatalf("restored PikPak cleanup calls=%d file=%v", pikPakDeletes.Load(), deletedPikPakFile.Load())
	}
	rows, err := st.Downloads(ctx, "")
	if err != nil || len(rows) != 1 || rows[0].ID != retained.ID {
		t.Fatalf("only Torrent history should remain: rows=%+v err=%v", rows, err)
	}
}

func TestRemoveHTTPDownloadDoesNotDeletePreExistingPikPakFile(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "remove-preexisting-http.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	site, _ := st.SaveSite(ctx, domain.Site{Title: "Test", Type: "Site", Name: "JavLibrary", Enabled: true})
	_, _ = st.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "HTTP-201", Title: "Test", Source: "JavLibrary", Released: true})
	releases, _ := st.Releases(ctx, domain.ReleaseFilter{Limit: 10})
	download, _ := st.SaveDownload(ctx, domain.Download{ReleaseID: releases[0].ID, Query: "HTTP-201", Transport: "http", Status: "completed", RestoredFileID: "preexisting-http-201", RestoredFileOwned: false})
	service := New(st, time.Second, slog.Default())
	service.pikPakDeleteFile = func(context.Context, string) error {
		t.Fatal("pre-existing PikPak file must not be deleted")
		return nil
	}
	deleted, err := service.RemoveDownload(ctx, download.ID)
	if err != nil || deleted != 1 {
		t.Fatalf("deleted=%d err=%v", deleted, err)
	}
}

func TestBulkRemoveFailedHTTPDeletesOnlySelectedHistoryWithoutQBittorrent(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "remove-failed-http.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.SaveSettings(ctx, map[string]string{"qb_url": "http://127.0.0.1:1"}); err != nil {
		t.Fatal(err)
	}
	site, _ := st.SaveSite(ctx, domain.Site{Title: "Test", Type: "Site", Name: "JavLibrary", Enabled: true})
	_, _ = st.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "HTTP-404", Title: "Test", Source: "JavLibrary", Released: true})
	releases, _ := st.Releases(ctx, domain.ReleaseFilter{Limit: 10})
	failed, _ := st.SaveDownload(ctx, domain.Download{ReleaseID: releases[0].ID, Query: "HTTP-404", Transport: "http", Status: "failed", Error: "provider timeout"})
	retained, _ := st.SaveDownload(ctx, domain.Download{ReleaseID: releases[0].ID, Query: "HTTP-404", Transport: "torrent", Status: "completed", TorrentHash: "retained"})

	service := New(st, 50*time.Millisecond, slog.Default())
	if _, err := service.StartBulkRemoveAndReplace(ctx, []int64{failed.ID}, false); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for service.ReplacementStatus().Running && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	job := service.ReplacementStatus()
	if job.Running || job.Removed != 1 || job.Failed != 0 {
		t.Fatalf("unexpected deletion result: %+v", job)
	}
	rows, err := st.Downloads(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ID != retained.ID {
		t.Fatalf("only selected failed HTTP history should be removed: %+v", rows)
	}
}

func TestBulkRemoveCompletedRemovedTorrentDeletesLocalHistoryOnly(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "remove-completed-removed.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.SaveSettings(ctx, map[string]string{"qb_url": "http://127.0.0.1:1"}); err != nil {
		t.Fatal(err)
	}
	site, _ := st.SaveSite(ctx, domain.Site{Title: "Test", Type: "Site", Name: "JavLibrary", Enabled: true})
	_, _ = st.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "REAL-971", Title: "Test", Source: "JavLibrary", Released: true})
	releases, _ := st.Releases(ctx, domain.ReleaseFilter{Limit: 10})
	stale, _ := st.SaveDownload(ctx, domain.Download{ReleaseID: releases[0].ID, Query: "REAL-971", Transport: "torrent", Status: "completed", PostStatus: postStatusCompletedRemoved, TorrentHash: "already-gone"})
	service := New(st, 50*time.Millisecond, slog.Default())
	if _, err := service.StartBulkRemoveAndReplace(ctx, []int64{stale.ID}, false); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for service.ReplacementStatus().Running && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	rows, err := st.Downloads(ctx, "")
	if err != nil || len(rows) != 0 {
		t.Fatalf("stale completed-removed history remains: rows=%+v err=%v", rows, err)
	}
}

func TestBulkRemoveAlreadyAbsentSelectionIsIdempotent(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "remove-absent.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	job, err := New(st, time.Second, slog.Default()).StartBulkRemoveAndReplace(ctx, []int64{987654}, false)
	if err != nil || job.Running || job.Total != 1 {
		t.Fatalf("stale selection should be accepted: job=%+v err=%v", job, err)
	}
}

func TestBulkRemoveDoesNotRemainRunningWhenHTTPDownloadWillNotStop(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "remove-stuck-http.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	site, _ := st.SaveSite(ctx, domain.Site{Title: "Test", Type: "Site", Name: "JavLibrary", Enabled: true})
	_, _ = st.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "STUCK-001", Title: "Test", Source: "JavLibrary", Released: true})
	releases, _ := st.Releases(ctx, domain.ReleaseFilter{Limit: 10})
	download, err := st.SaveDownload(ctx, domain.Download{ReleaseID: releases[0].ID, Query: "STUCK-001", Transport: "http", Status: "downloading"})
	if err != nil {
		t.Fatal(err)
	}
	service := New(st, time.Second, slog.Default())
	service.replacementCleanupTimeout = 25 * time.Millisecond
	service.httpMu.Lock()
	service.httpRuns[download.ID] = &httpDownloadRun{cancel: func() {}, done: make(chan struct{})}
	service.httpMu.Unlock()
	if _, err := service.StartBulkRemoveAndReplace(ctx, []int64{download.ID}, false); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for service.ReplacementStatus().Running && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	job := service.ReplacementStatus()
	if job.Running || job.Processed != 1 || job.Failed != 1 || !strings.Contains(job.LastError, context.DeadlineExceeded.Error()) {
		t.Fatalf("stuck cancellation did not release bulk job: %+v", job)
	}
	if _, err := service.StartBulkRemoveAndReplace(ctx, []int64{987654}, false); err != nil {
		t.Fatalf("completed timed-out job still blocks later jobs: %v", err)
	}
}

func TestManualReplacementDeletesFilesClearsHistoryAndStartsFreshDownload(t *testing.T) {
	var deletedFiles, added atomic.Bool
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v2/auth/login", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("GET /api/v2/torrents/info", func(w http.ResponseWriter, _ *http.Request) {
		if added.Load() {
			_, _ = w.Write([]byte(`[{"hash":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","name":"PRED-999 replacement"}]`))
			return
		}
		if deletedFiles.Load() {
			_, _ = w.Write([]byte(`[]`))
			return
		}
		_, _ = w.Write([]byte(`[{"hash":"oldhash","name":"PRED-999 incomplete"}]`))
	})
	mux.HandleFunc("POST /api/v2/torrents/delete", func(w http.ResponseWriter, r *http.Request) {
		if r.FormValue("hashes") != "oldhash" || r.FormValue("deleteFiles") != "true" {
			t.Errorf("replacement delete form: hashes=%q deleteFiles=%q", r.FormValue("hashes"), r.FormValue("deleteFiles"))
		}
		deletedFiles.Store(true)
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /api/v2/torrents/add", func(w http.ResponseWriter, _ *http.Request) {
		if !deletedFiles.Load() {
			t.Error("replacement torrent was added before the old torrent and files were deleted")
		}
		added.Store(true)
		_, _ = w.Write([]byte("Ok."))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "replace-download.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.SaveSettings(ctx, map[string]string{"qb_url": server.URL}); err != nil {
		t.Fatal(err)
	}
	site, _ := st.SaveSite(ctx, domain.Site{Title: "Test", Type: "Site", Name: "JavLibrary", Enabled: true})
	_, _ = st.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "PRED-999", Title: "Test", Source: "JavLibrary", Released: true})
	releases, _ := st.Releases(ctx, domain.ReleaseFilter{Limit: 10})
	release := releases[0]
	_, _ = st.SaveDownload(ctx, domain.Download{ReleaseID: release.ID, Query: release.VideoID, TorrentHash: "oldhash", Status: "downloading"})
	_, _ = st.SaveDownload(ctx, domain.Download{ReleaseID: release.ID, Query: release.VideoID, Status: "search_accepted"})
	_, _ = st.CreateNotification(ctx, release.ID, "download_started", "Old download")

	result, err := New(st, time.Second, slog.Default()).Download(ctx, release, domain.SearchResult{
		Provider: "Sukebei/Nyaa", Title: "PRED-999 replacement", Link: "magnet:?xt=urn:btih:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Forced: true, ReplaceExisting: true,
	}, "Manual Search", "replacement")
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "downloading" || !deletedFiles.Load() || !added.Load() {
		t.Fatalf("replacement result=%+v deleted=%v added=%v", result, deletedFiles.Load(), added.Load())
	}
	rows, err := st.Downloads(ctx, "")
	if err != nil || len(rows) != 1 || rows[0].ID != result.ID || rows[0].TorrentHash != "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" {
		t.Fatalf("replacement history was not clean: rows=%+v err=%v", rows, err)
	}
}

func TestBestSeededCandidateOnlyConsidersAcceptedResults(t *testing.T) {
	results := []domain.SearchResult{
		{Title: "accepted-low", Accepted: true, Seeds: 4},
		{Title: "rejected-high", Accepted: false, Seeds: 25},
		{Title: "accepted-mid", Accepted: true, Seeds: 12},
	}
	best, found := bestSeededCandidate(results)
	if !found || best.Title != "accepted-mid" {
		t.Fatalf("best candidate = %+v found=%v", best, found)
	}
	if _, found := bestSeededCandidate([]domain.SearchResult{{Accepted: false, Seeds: 99}}); found {
		t.Fatal("rejected-only results should not be selected")
	}
}

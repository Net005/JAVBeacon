package web

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/Net005/JAVBeacon/internal/domain"
	"github.com/Net005/JAVBeacon/internal/download"
	"github.com/Net005/JAVBeacon/internal/store"
)

func TestUnavailableSearchDoesNotAccumulateDownloadTasks(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "unavailable-queue.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/search" {
			t.Errorf("unexpected provider path: %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`<html><body><div class="item"><a class="box" href="/v/different"><strong>SYNTH-102</strong><div class="meta">2026-09-15</div></a></div></body></html>`))
	}))
	defer provider.Close()
	if err = st.SaveSettings(ctx, map[string]string{"default_download_method": "http_only", "javdb_url": provider.URL, "http_download_directory": t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	site, err := st.SaveSite(ctx, domain.Site{Title: "Synthetic", Type: "Site", Name: "JavLibrary", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = st.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "SYNTH-101", Title: "Synthetic", Source: "JavLibrary", MonitorDownload: true}); err != nil {
		t.Fatal(err)
	}
	releases, err := st.Releases(ctx, domain.ReleaseFilter{Search: "SYNTH-101", Limit: 1})
	if err != nil || len(releases) != 1 {
		t.Fatalf("setup: %v", err)
	}
	release := releases[0]
	keep := map[int64]bool{}
	for _, row := range []domain.Download{
		{ReleaseID: release.ID, Transport: "torrent", Status: "completed", TorrentHash: "owned-torrent"},
		{ReleaseID: release.ID, Transport: "http", Status: "completed"},
		{ReleaseID: release.ID, Transport: "http", Status: "failed", Error: "real transfer failure"},
	} {
		saved, err := st.SaveDownload(ctx, row)
		if err != nil {
			t.Fatal(err)
		}
		keep[saved.ID] = true
	}
	s := &Server{store: st, downloads: download.New(st, time.Second, slog.Default()), log: slog.Default()}
	for attempt := 0; attempt < 2; attempt++ {
		task, err := st.SaveDownload(ctx, domain.Download{ReleaseID: release.ID, Transport: "http", Status: "search_queued"})
		if err != nil {
			t.Fatal(err)
		}
		s.bulkReleaseQueue = []bulkReleaseItem{{Release: release, TaskID: task.ID, SourceType: "Synthetic search"}}
		s.bulkReleaseRunning = true
		s.runBulkReleaseJobs()
		rows, err := st.Downloads(ctx, "")
		if err != nil {
			t.Fatal(err)
		}
		retained := 0
		diagnostics := 0
		for _, row := range rows {
			if keep[row.ID] {
				retained++
			}
			if row.Status == "not_available" || row.Status == "search_queued" || row.Status == "searching" || row.ID == task.ID {
				t.Fatalf("finished miss retained queue row: %+v", row)
			}
			if row.Status == "search_rejected" {
				diagnostics++
			}
		}
		if retained != len(keep) || diagnostics != attempt+1 {
			t.Fatalf("history: kept=%d diagnostics=%d", retained, diagnostics)
		}
	}
	current, err := st.Release(ctx, release.ID)
	if err != nil || !current.MonitorDownload {
		t.Fatalf("monitoring changed: %+v %v", current, err)
	}
}

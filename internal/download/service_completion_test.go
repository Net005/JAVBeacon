package download

import (
	"context"
	"encoding/json"
	"io"
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

func TestTorrentCompletionState(t *testing.T) {
	for _, state := range []string{"uploading", "stalledUP", "queuedUP", "forcedUP", "pausedUP", "stoppedUP"} {
		if !torrentCompletionState(state) {
			t.Errorf("completed state %q rejected", state)
		}
	}
	for _, state := range []string{"checkingUP", "checkingDL", "checkingResumeData", "allocating", "moving", "downloading", "metaDL", "stalledDL", "queuedDL", "forcedDL", "pausedDL", "stoppedDL", "error", "missingFiles", "unknown", "madeUP", "upload", ""} {
		if torrentCompletionState(state) {
			t.Errorf("unsafe state %q accepted as completed", state)
		}
	}
}

func TestPollDownloadWithholdsCompletionWhileChecking(t *testing.T) {
	for _, status := range []string{"downloading", "completed"} {
		for _, state := range []string{"checkingUP", "checkingDL", "checkingResumeData", "allocating", "moving", "downloading"} {
			t.Run(status+"/"+state, func(t *testing.T) {
				ctx := context.Background()
				st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "checking.db"))
				if err != nil {
					t.Fatal(err)
				}
				defer st.Close()
				site, err := st.SaveSite(ctx, domain.Site{Title: "Check", Name: "Test", Type: "Site", Enabled: true})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := st.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "SAME-250", Title: "Check", Source: "Test"}); err != nil {
					t.Fatal(err)
				}
				releases, err := st.Releases(ctx, domain.ReleaseFilter{VideoID: "SAME-250", Limit: 1})
				if err != nil || len(releases) != 1 {
					t.Fatalf("releases=%v err=%v", releases, err)
				}
				d, err := st.SaveDownload(ctx, domain.Download{ReleaseID: releases[0].ID, Query: "SAME-250", Status: status, TorrentHash: "hash", Files: json.RawMessage(`[]`), TransferReference: "magnet:?xt=urn:btih:hash"})
				if err != nil {
					t.Fatal(err)
				}
				if status == "completed" {
					if err := st.SavePipelineRun(ctx, domain.PipelineRun{DownloadID: d.ID, Trigger: pipelineDownloadCompleted, State: "completed"}); err != nil {
						t.Fatal(err)
					}
				}
				var requests atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests.Add(1)
					w.WriteHeader(http.StatusInternalServerError)
				}))
				defer server.Close()
				service := New(st, time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
				var probes atomic.Int32
				service.videoProbe = func(context.Context, string) error { probes.Add(1); return nil }
				// Even 100% progress during a recheck must not authorize cleanup.
				service.pollDownload(ctx, NewQB(server.URL, "", ""), d, []Torrent{{Hash: "hash", Name: "SAME-250", State: state, Progress: 1}}, 0, completedTorrentRemove)
				if probes.Load() != 0 || requests.Load() != 0 {
					t.Fatalf("checking triggered video probes=%d or qBittorrent requests=%d", probes.Load(), requests.Load())
				}
				rows, err := st.Downloads(ctx, status)
				if err != nil || len(rows) != 1 || rows[0].PostStatus != "" {
					t.Fatalf("download changed during checking: rows=%+v err=%v", rows, err)
				}
				if service.isPipelineInFlight(d.ID, pipelineDownloadCompleted) {
					t.Fatal("checking started completion pipeline")
				}
				run, err := st.PipelineRun(ctx, d.ID, pipelineDownloadCompleted)
				if err != nil {
					t.Fatal(err)
				}
				if status == "downloading" && strings.TrimSpace(run.State) != "" {
					t.Fatalf("checking started pipeline: %+v", run)
				}
			})
		}
	}
}

func TestPollDownloadNeverAdoptsManualTorrentByName(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "manual.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	site, err := st.SaveSite(ctx, domain.Site{Title: "Manual", Name: "Test", Type: "Site", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "SAME-250", Title: "Manual", Source: "Test"}); err != nil {
		t.Fatal(err)
	}
	releases, err := st.Releases(ctx, domain.ReleaseFilter{VideoID: "SAME-250", Limit: 1})
	if err != nil || len(releases) != 1 {
		t.Fatalf("releases=%v err=%v", releases, err)
	}
	d, err := st.SaveDownload(ctx, domain.Download{ReleaseID: releases[0].ID, Query: "SAME-250", Status: "downloading", Files: json.RawMessage(`[]`)})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unowned torrent caused API request: %s", r.URL.Path)
		w.WriteHeader(500)
	}))
	defer server.Close()
	service := New(st, time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	service.videoProbe = func(context.Context, string) error { t.Error("unowned torrent triggered video validation"); return nil }
	service.pollDownload(ctx, NewQB(server.URL, "", ""), d, []Torrent{{Hash: "manual", Name: "SAME-250", State: "stalledUP", Progress: 1}}, 0, completedTorrentRemove)
	rows, err := st.Downloads(ctx, "downloading")
	if err != nil || len(rows) != 1 || rows[0].TorrentHash != "" || rows[0].Progress != 0 {
		t.Fatalf("adopted manual torrent: rows=%+v err=%v", rows, err)
	}
	if service.isPipelineInFlight(d.ID, pipelineDownloadCompleted) {
		t.Fatal("unowned torrent started pipeline")
	}
	// A retired history hash must not reclaim a torrent re-added manually.
	d.TorrentHash = "manual"
	d.Status = "completed"
	d.PostStatus = postStatusCompletedRemoved
	if _, err := st.SaveDownload(ctx, d); err != nil {
		t.Fatal(err)
	}
	service.pollDownload(ctx, NewQB(server.URL, "", ""), d, []Torrent{{Hash: "manual", Name: "SAME-250", State: "stalledUP", Progress: 1}}, 0, completedTorrentRemove)
	// Removal of retired history must also leave the manual torrent alone.
	if err := st.SaveSettings(ctx, map[string]string{"qb_url": server.URL}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.removeReleaseDownloads(ctx, releases[0].ID, "SAME-250", true); err != nil {
		t.Fatal(err)
	}
}

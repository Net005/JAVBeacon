package download

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Net005/JAVBeacon/internal/domain"
	"github.com/Net005/JAVBeacon/internal/store"
)

func TestTorrentProgressContinuesDuringBlockedVideoCheck(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "progress.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	server, fake := newFakeQBittorrentServer(t)
	if err := st.SaveSettings(ctx, map[string]string{"qb_url": server.URL, "qb_completed_action": "keep"}); err != nil {
		t.Fatal(err)
	}
	site, err := st.SaveSite(ctx, domain.Site{Title: "Progress", Name: "Test", Type: "Site", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "TEST-123", Title: "Progress", Source: "Test"}); err != nil {
		t.Fatal(err)
	}
	releases, err := st.Releases(ctx, domain.ReleaseFilter{VideoID: "TEST-123", Limit: 1})
	if err != nil || len(releases) != 1 {
		t.Fatalf("release lookup: %v", err)
	}
	save := func(hash string) domain.Download {
		t.Helper()
		d, err := st.SaveDownload(ctx, domain.Download{ReleaseID: releases[0].ID, Query: "TEST-123", TorrentHash: hash, Status: "downloading"})
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	other := save("other-hash")
	unowned := save("")
	slow := save("slow-hash")
	fake.torrents = []Torrent{{Hash: slow.TorrentHash, Name: "TEST-123", State: "uploading", Progress: 1, Ratio: .1}, {Hash: other.TorrentHash, State: "downloading", Progress: .2}, {Hash: "manual-hash", Name: "TEST-123", Progress: 1}}
	service := New(st, time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	started, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	service.videoProbe = func(context.Context, string) error {
		close(started)
		<-release
		return nil
	}
	go func() { defer close(done); service.pollTorrents(ctx) }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("video check never started")
	}
	fake.mu.Lock()
	fake.torrents[0].Ratio = .8
	fake.torrents[1].Progress = .7
	fake.mu.Unlock()
	service.pollTorrentProgress(ctx)
	get := func(id int64, status string) domain.Download {
		t.Helper()
		d, ok := service.downloadByID(ctx, id, status)
		if !ok {
			t.Fatalf("download %d missing from %s", id, status)
		}
		return d
	}
	if d := get(slow.ID, "downloading"); d.Progress != 1 || d.SeedRatio != .8 {
		t.Fatalf("completed transfer telemetry stayed stale during validation: %+v", d)
	}
	if d := get(other.ID, "downloading"); d.Progress != .7 {
		t.Fatalf("another download froze behind validation: %+v", d)
	}
	if d := get(unowned.ID, "downloading"); d.Progress != 0 || d.TorrentHash != "" {
		t.Fatal("progress polling adopted a torrent by name")
	}
	once.Do(func() { close(release) })
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("completion processing did not resume")
	}
	if d := get(slow.ID, "completed"); d.SeedRatio != .8 || d.Progress != 1 {
		t.Fatalf("slow completion overwrote newer telemetry: %+v", d)
	}
	if d := get(other.ID, "downloading"); d.Progress != .7 {
		t.Fatal("maintenance used an obsolete torrent snapshot")
	}
	waitForPipelineRun(t, st, slow.ID, pipelineDownloadCompleted)
}

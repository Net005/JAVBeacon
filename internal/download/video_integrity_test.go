package download

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Net005/JAVBeacon/internal/domain"
	"github.com/Net005/JAVBeacon/internal/store"
)

func TestVideoFilesFindsSupportedVideosAndIgnoresOtherFiles(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"movie.mp4", "feature.MKV", "subtitle.srt", "readme.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("test"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	files, err := videoFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 {
		t.Fatalf("video files = %v, want the MP4 and MKV only", files)
	}
}

func TestFFprobeVideoRejectsCorruptPayloadWithClearReason(t *testing.T) {
	path := filepath.Join(t.TempDir(), "corrupt.mp4")
	if err := os.WriteFile(path, []byte("this is not a video"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := ffprobeVideo(context.Background(), path)
	if err == nil || !strings.Contains(err.Error(), "ffprobe rejected the video") {
		t.Fatalf("ffprobe error = %v, want an explicit rejection", err)
	}
}

func TestVideoProbeSeamReceivesCompletedPath(t *testing.T) {
	want := "/downloads/RELEASE-1.mp4"
	called := ""
	service := &Service{videoProbe: func(_ context.Context, path string) error {
		called = path
		return nil
	}}
	if err := service.verifyDownloadedVideo(context.Background(), want); err != nil {
		t.Fatal(err)
	}
	if called != want {
		t.Fatalf("probe path = %q, want %q", called, want)
	}
}

func TestCorruptTorrentIsRedownloadedOnceThenFailsClearly(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "video-retry.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.SaveSettings(ctx, map[string]string{"qb_url": "placeholder"}); err != nil {
		t.Fatal(err)
	}
	const hash = "0123456789abcdef0123456789abcdef01234567"
	var mu sync.Mutex
	torrents := []Torrent{{Hash: hash, Name: "RETRY-VIDEO-1", State: "stalledUP", Progress: 1, ContentPath: "/downloads/corrupt.mp4"}}
	deleteFiles := ""
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/auth/login", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("Ok.")) })
	mux.HandleFunc("/api/v2/torrents/info", func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		_ = json.NewEncoder(w).Encode(torrents)
	})
	mux.HandleFunc("/api/v2/torrents/delete", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		mu.Lock()
		deleteFiles = r.FormValue("deleteFiles")
		torrents = nil
		mu.Unlock()
	})
	mux.HandleFunc("/api/v2/torrents/add", func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		torrents = []Torrent{{Hash: hash, Name: "RETRY-VIDEO-1", State: "downloading", Progress: 0}}
		mu.Unlock()
		_, _ = w.Write([]byte("Ok."))
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	if err := st.SaveSettings(ctx, map[string]string{"qb_url": server.URL}); err != nil {
		t.Fatal(err)
	}
	service := New(st, time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	service.videoProbe = func(context.Context, string) error { return errors.New("invalid packet at byte 42") }
	site, err := st.SaveSite(ctx, domain.Site{Title: "Video check", Name: "Test", Type: "Site", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "RETRY-VIDEO-1", Title: "Retry video", Source: "Test"})
	if err != nil {
		t.Fatal(err)
	}
	releases, err := st.Releases(ctx, domain.ReleaseFilter{VideoID: "RETRY-VIDEO-1", Limit: 1})
	if err != nil || len(releases) != 1 {
		t.Fatalf("release lookup = %+v, err=%v", releases, err)
	}
	download, err := st.SaveDownload(ctx, domain.Download{ReleaseID: releases[0].ID, Query: "RETRY-VIDEO-1", Transport: "torrent", TorrentHash: hash, TransferReference: "magnet:?xt=urn:btih:" + hash, Status: "downloading"})
	if err != nil {
		t.Fatal(err)
	}
	qb := NewQB(server.URL, "", "")
	if !service.handleTorrentVideoCheck(ctx, qb, &download, torrents[0]) {
		t.Fatal("corrupt video did not stop normal completion processing")
	}
	rows, err := st.Downloads(ctx, "downloading")
	if err != nil || len(rows) != 1 || rows[0].PostStatus != postStatusVideoRetry || !strings.Contains(rows[0].Error, "failed video check") {
		t.Fatalf("retry row = %+v, err=%v", rows, err)
	}
	if deleteFiles != "true" {
		t.Fatalf("qBittorrent deleteFiles=%q, want true before re-download", deleteFiles)
	}

	download = rows[0]
	if !service.handleTorrentVideoCheck(ctx, qb, &download, Torrent{Hash: hash, ContentPath: "/downloads/still-corrupt.mp4"}) {
		t.Fatal("second corrupt video did not stop normal completion processing")
	}
	failed, err := st.Downloads(ctx, "failed")
	if err != nil || len(failed) != 1 || failed[0].PostStatus != postStatusVideoFailed || !strings.Contains(failed[0].Error, "failed again after automatic re-download") {
		t.Fatalf("terminal row = %+v, err=%v", failed, err)
	}
}

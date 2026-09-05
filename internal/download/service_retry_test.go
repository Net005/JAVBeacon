package download

import (
	"context"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/Net005/JAVBeacon/internal/domain"
	"github.com/Net005/JAVBeacon/internal/store"
)

func TestRetryFailedHTTPDownloadsSupportsSelectedAndAll(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "retry-downloads.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.SaveSettings(ctx, map[string]string{"http_download_directory": t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	site, _ := st.SaveSite(ctx, domain.Site{Title: "Test", Type: "Site", Name: "JavLibrary", Enabled: true})
	for _, videoID := range []string{"RETRY-101", "RETRY-102", "RETRY-103", "RETRY-104"} {
		_, _ = st.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: videoID, Title: videoID, Source: "JavLibrary", Released: true})
	}
	releases, _ := st.Releases(ctx, domain.ReleaseFilter{Limit: 10})
	byVideoID := map[string]domain.Release{}
	for _, release := range releases {
		byVideoID[release.VideoID] = release
	}
	_, _ = st.SaveDownload(ctx, domain.Download{ReleaseID: byVideoID["RETRY-101"].ID, Query: "RETRY-101", Name: "RETRY-101.mp4", Provider: "JavDB / Keepshare", SourceReference: "http://127.0.0.1:1/one", Transport: "http", Status: "failed"})
	_, _ = st.SaveDownload(ctx, domain.Download{ReleaseID: byVideoID["RETRY-102"].ID, Query: "RETRY-102", Name: "RETRY-102.mp4", Provider: "JavDB / Keepshare", SourceReference: "http://127.0.0.1:1/two", Transport: "http", Status: "failed"})
	_, _ = st.SaveDownload(ctx, domain.Download{ReleaseID: byVideoID["RETRY-103"].ID, Query: "RETRY-103", Name: "RETRY-103 torrent", Provider: "Nyaa", Transport: "torrent", Status: "failed"})

	service := New(st, time.Second, slog.Default())
	all, err := service.RetryFailedHTTPDownloads(ctx, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if all["matched"] != 2 || all["retried"] != 2 || all["failed"] != 0 {
		t.Fatalf("all retry result = %#v", all)
	}
	selectedRow, _ := st.SaveDownload(ctx, domain.Download{ReleaseID: byVideoID["RETRY-104"].ID, Query: "RETRY-104", Name: "RETRY-104.mp4", Provider: "JavDB / Keepshare", SourceReference: "http://127.0.0.1:1/four", Transport: "http", Status: "failed"})
	selected, err := service.RetryFailedHTTPDownloads(ctx, []int64{selectedRow.ID}, false)
	if err != nil {
		t.Fatal(err)
	}
	if selected["matched"] != 1 || selected["retried"] != 1 || selected["failed"] != 0 {
		t.Fatalf("selected retry result = %#v", selected)
	}
}

func TestRetryHTTPDownloadReusesFailedRowAndBypassesLocalHistory(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "retry-row.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.SaveSettings(ctx, map[string]string{"http_download_directory": t.TempDir(), "http_download_concurrency": "1"}); err != nil {
		t.Fatal(err)
	}
	site, _ := st.SaveSite(ctx, domain.Site{Title: "Test", Type: "Site", Name: "JavLibrary", Enabled: true})
	_, _ = st.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "RETRY-LOCAL-1", Title: "Test", Source: "JavLibrary", Released: true})
	releases, _ := st.Releases(ctx, domain.ReleaseFilter{Search: "RETRY-LOCAL-1", Limit: 1})
	if len(releases) != 1 {
		t.Fatal("release setup failed")
	}
	if err := st.SetStashState(ctx, releases[0].ID, true, "stash-scene"); err != nil {
		t.Fatal(err)
	}
	failed, _ := st.SaveDownload(ctx, domain.Download{ReleaseID: releases[0].ID, Query: releases[0].VideoID, Name: "RETRY-LOCAL-1.mp4", Provider: "JavDB / Keepshare", SourceReference: "http://127.0.0.1:1/share", Transport: "http", Status: "failed", Error: "upstream timeout"})
	service := New(st, time.Second, slog.Default())
	service.httpActive = 1 // Keep the worker queued until the row is inspected.
	queued, err := service.RetryHTTPDownload(ctx, failed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if queued.ID != failed.ID || queued.Status != "queued" || queued.Error != "" || queued.SourceType != "Manual HTTP Retry" {
		t.Fatalf("retry did not transition the failed row in place: %+v", queued)
	}
	service.httpMu.Lock()
	service.httpActive = 0
	service.httpMu.Unlock()
}

package download

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
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

package web

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Net005/JAVBeacon/internal/domain"
	"github.com/Net005/JAVBeacon/internal/download"
	"github.com/Net005/JAVBeacon/internal/stash"
	"github.com/Net005/JAVBeacon/internal/store"
)

// TestStashMissingListAndCountRespectFilters covers the read side of
// TODO-2.0 Phase 2's Missing Library Files section: the handlers must pass
// query parameters through to the same StashMissingFilter the store tests
// already cover, and count must ignore limit/offset like releasesCount.
func TestStashMissingListAndCountRespectFilters(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "web-stash-missing.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.UpsertStashMissingScene(ctx, domain.StashMissingScene{StashSceneID: "1", Title: "A", Code: "AAA-1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpsertStashMissingScene(ctx, domain.StashMissingScene{StashSceneID: "2", Title: "B", Code: "BBB-2"}); err != nil {
		t.Fatal(err)
	}
	s := &Server{store: st, log: slog.Default()}

	rec := httptest.NewRecorder()
	s.stashMissingList(rec, httptest.NewRequest(http.MethodGet, "/api/stash-missing?limit=1", nil))
	var rows []domain.StashMissingScene
	if err := json.NewDecoder(rec.Body).Decode(&rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected limit=1 to be honored, got %d rows", len(rows))
	}

	rec = httptest.NewRecorder()
	s.stashMissingCount(rec, httptest.NewRequest(http.MethodGet, "/api/stash-missing/count?limit=1", nil))
	var body struct {
		Total int `json:"total"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Total != 2 {
		t.Fatalf("count should ignore limit/offset like releasesCount, got %+v", body)
	}
}

// TestStashMissingScanJobStartsAndReportsConflict covers the job-status
// GET/POST pair (mirroring /api/jobs/stash) for the scan action: POST
// starts a scan and a concurrent POST while one is running is rejected
// with 409, matching every other job endpoint's convention.
func TestStashMissingScanJobStartsAndReportsConflict(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "web-scan.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.SaveSettings(ctx, map[string]string{"stash_base_url": "https://stash.invalid"}); err != nil {
		t.Fatal(err)
	}
	stashSvc := stash.New(st, time.Second, slog.Default(), nil, nil)
	s := &Server{store: st, stash: stashSvc, log: slog.Default()}

	rec := httptest.NewRecorder()
	s.stashMissingScanJob(rec, httptest.NewRequest(http.MethodPost, "/api/jobs/stash-missing-scan", nil))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("first scan start: code=%d body=%s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	s.stashMissingScanJob(rec, httptest.NewRequest(http.MethodPost, "/api/jobs/stash-missing-scan", nil))
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409 while a scan is already running, got %d: %s", rec.Code, rec.Body.String())
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !stashSvc.MissingScanStatus().Running {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	rec = httptest.NewRecorder()
	s.stashMissingScanJob(rec, httptest.NewRequest(http.MethodGet, "/api/jobs/stash-missing-scan", nil))
	var status stash.MissingStatus
	if err := json.NewDecoder(rec.Body).Decode(&status); err != nil {
		t.Fatal(err)
	}
	if status.Running {
		t.Fatalf("scan should have finished (and failed, since the StashApp URL is unreachable): %+v", status)
	}
	if status.Error == "" {
		t.Fatalf("expected an error against an unreachable StashApp URL, got %+v", status)
	}
}

// TestStashMissingRetrieveJobRejectsEmptySelection and
// TestStashMissingApplyJobValidatesMode cover the two bulk-action POST
// handlers' request validation, which the frontend's Retrieve/Set actions
// depend on for a clear error rather than a silent no-op.
func TestStashMissingRetrieveJobRejectsEmptySelection(t *testing.T) {
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "web-retrieve.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	stashSvc := stash.New(st, time.Second, slog.Default(), nil, nil)
	s := &Server{store: st, stash: stashSvc, log: slog.Default()}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/jobs/stash-missing-retrieve", bytes.NewReader([]byte(`{"ids":[]}`)))
	s.stashMissingRetrieveJob(rec, req)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 for an empty selection, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestStashMissingApplyJobValidatesMode(t *testing.T) {
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "web-apply.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	stashSvc := stash.New(st, time.Second, slog.Default(), nil, nil)
	s := &Server{store: st, stash: stashSvc, log: slog.Default()}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/jobs/stash-missing-apply", bytes.NewReader([]byte(`{"ids":[1],"mode":"bogus"}`)))
	s.stashMissingApplyJob(rec, req)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 for an invalid mode, got %d: %s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/jobs/stash-missing-apply", bytes.NewReader([]byte(`{"ids":[1]}`)))
	s.stashMissingApplyJob(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected mode to default to monitor_only, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestStashMissingApplyJobPassesAllowNonPreferredFilenamesThrough covers
// TODO-2.0 Task A's "Allow non-preferred filenames" toggle end-to-end
// through the HTTP layer: the apply-job body's
// allow_non_preferred_filenames field must reach
// download.Service.SearchAndDownloadNow via stash.Service.StartApply, so a
// seeded-but-unaccepted torrent is found and downloaded when the toggle is
// set, and reported not_found (the pre-existing, stricter behavior) when
// it's omitted - proving the field isn't silently dropped anywhere between
// the JSON body and the download service.
func TestStashMissingApplyJobPassesAllowNonPreferredFilenamesThrough(t *testing.T) {
	run := func(t *testing.T, allowNonPreferred bool) stash.ApplyStatus {
		ctx := context.Background()
		st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "web-apply-fallback.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer st.Close()

		mux := http.NewServeMux()
		mux.HandleFunc("/feed", func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`<rss xmlns:nyaa="https://nyaa.si/xmlns/nyaa"><channel>` +
				`<item><title>rejected@ WEBFALLBACK-100 seeded</title><link>magnet:?xt=urn:btih:web</link><nyaa:seeders>5</nyaa:seeders></item>` +
				`</channel></rss>`))
		})
		server := httptest.NewServer(mux)
		defer server.Close()

		qbMux := http.NewServeMux()
		qbMux.HandleFunc("POST /api/v2/auth/login", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("Ok.")) })
		qbMux.HandleFunc("GET /api/v2/torrents/categories", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{}`)) })
		// Service.Download now verifies a torrent actually registered in
		// qBittorrent before trusting the "Ok." /add response (it can be
		// returned for input qBittorrent never actually queues), so this
		// stub must echo the torrent back through /torrents/info the same
		// way a real qBittorrent instance would - but only once /add has
		// actually been called, or the up-front duplicate check would see
		// the torrent "already there" before anything was ever added.
		var added bool
		qbMux.HandleFunc("GET /api/v2/torrents/info", func(w http.ResponseWriter, _ *http.Request) {
			if !added {
				_, _ = w.Write([]byte(`[]`))
				return
			}
			_, _ = w.Write([]byte(`[{"hash":"webfallbackhash","name":"rejected@ WEBFALLBACK-100 seeded"}]`))
		})
		qbMux.HandleFunc("POST /api/v2/torrents/add", func(w http.ResponseWriter, _ *http.Request) {
			added = true
			_, _ = w.Write([]byte("Ok."))
		})
		qbServer := httptest.NewServer(qbMux)
		defer qbServer.Close()

		if err := st.SaveSettings(ctx, map[string]string{
			"search_url_template": server.URL + "/feed?q=<release_id>",
			"qb_url":              qbServer.URL,
		}); err != nil {
			t.Fatal(err)
		}
		site, _ := st.SaveSite(ctx, domain.Site{Title: "GIGA", Type: "Site", Name: "GIGA", Enabled: false, Download: false})
		if _, err := st.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "WEBFALLBACK-100", Title: "T", Source: "GIGA"}); err != nil {
			t.Fatal(err)
		}
		releases, _ := st.Releases(ctx, domain.ReleaseFilter{Search: "WEBFALLBACK-100", Limit: 1})
		id, err := st.UpsertStashMissingScene(ctx, domain.StashMissingScene{StashSceneID: "scn-web-fallback", Title: "T", Code: "WEBFALLBACK-100"})
		if err != nil {
			t.Fatal(err)
		}
		if err := st.LinkStashMissingRelease(ctx, id, releases[0].ID); err != nil {
			t.Fatal(err)
		}

		downloads := download.New(st, 2*time.Second, slog.Default())
		stashSvc := stash.New(st, time.Second, slog.Default(), nil, downloads)
		s := &Server{store: st, stash: stashSvc, log: slog.Default()}

		body := map[string]any{"ids": []int64{id}, "mode": "monitor_download", "allow_non_preferred_filenames": allowNonPreferred}
		raw, _ := json.Marshal(body)
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/jobs/stash-missing-apply", bytes.NewReader(raw))
		s.stashMissingApplyJob(rec, req)
		if rec.Code != http.StatusAccepted {
			t.Fatalf("apply start: code=%d body=%s", rec.Code, rec.Body.String())
		}

		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			if !stashSvc.ApplyRunStatus().Running {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		return stashSvc.ApplyRunStatus()
	}

	t.Run("false: reports not_found despite the seeded result", func(t *testing.T) {
		status := run(t, false)
		if status.Found != 0 || status.NotFound != 1 {
			t.Fatalf("expected not_found with the toggle off, got %+v", status)
		}
	})

	t.Run("true: finds and downloads the seeded-but-unaccepted result", func(t *testing.T) {
		status := run(t, true)
		if status.Found != 1 || status.NotFound != 0 {
			t.Fatalf("expected found=1 with the toggle on, got %+v", status)
		}
	})
}

// TestBrowseDirListsSubdirectoriesOnly covers the Settings "Browse" control
// used to fill in a stash_missing_path_remaps row's "to" path: it must list
// only subdirectories (not files) of the given path, sorted, plus a parent
// pointer for navigating back up.
func TestBrowseDirListsSubdirectoriesOnly(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "zeta"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "alpha"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "a-file.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := &Server{log: slog.Default()}

	rec := httptest.NewRecorder()
	s.browseDir(rec, httptest.NewRequest(http.MethodGet, "/api/system/browse-dir?path="+url.QueryEscape(root), nil))
	var body struct {
		Path        string           `json:"path"`
		Parent      string           `json:"parent"`
		Directories []browseDirEntry `json:"directories"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Directories) != 2 || body.Directories[0].Name != "alpha" || body.Directories[1].Name != "zeta" {
		t.Fatalf("expected only [alpha, zeta] sorted, got %+v", body.Directories)
	}
	if body.Parent != filepath.Dir(root) {
		t.Fatalf("parent=%q, want %q", body.Parent, filepath.Dir(root))
	}
}

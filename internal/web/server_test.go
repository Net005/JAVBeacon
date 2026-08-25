package web

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Net005/JAVBeacon/internal/covers"
	"github.com/Net005/JAVBeacon/internal/domain"
	"github.com/Net005/JAVBeacon/internal/download"
	"github.com/Net005/JAVBeacon/internal/logging"
	"github.com/Net005/JAVBeacon/internal/store"
)

func TestVersionEndpointReturnsApplicationVersion(t *testing.T) {
	s := &Server{mux: http.NewServeMux()}
	s.routes()
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/version", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"version":"v1.0.7"`) {
		t.Fatalf("response = %s, want v1.0.7", rec.Body.String())
	}
}

func TestCoverCacheJobCachesMissingAndSkipsExistingCovers(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "covers.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	imageServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write([]byte("test-cover"))
	}))
	defer imageServer.Close()
	site, _ := st.SaveSite(ctx, domain.Site{Title: "Test", Type: "Site", Name: "JavLibrary", Enabled: true})
	_, _ = st.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "TEST-1", Title: "Test", Source: "JavLibrary", ImageURL: imageServer.URL})
	cache, err := covers.New(filepath.Join(t.TempDir(), "cache"), time.Second, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{store: st, covers: cache, log: slog.Default()}

	if err := s.startCoverCache(ctx); err != nil {
		t.Fatal(err)
	}
	first := waitForCoverJob(t, s)
	if first.Total != 1 || first.Checked != 1 || first.Cached != 1 || first.Failed != 0 {
		t.Fatalf("first cover job: %+v", first)
	}
	if _, err := os.Stat(cache.Path("TEST-1")); err != nil {
		t.Fatalf("cached cover: %v", err)
	}

	if err := s.startCoverCache(ctx); err != nil {
		t.Fatal(err)
	}
	second := waitForCoverJob(t, s)
	if second.Checked != 1 || second.Cached != 0 || second.Skipped != 1 || second.Failed != 0 {
		t.Fatalf("second cover job: %+v", second)
	}
}

func TestCoverEndpointServesBrandedPlaceholderWhenArtworkIsUnavailable(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "placeholder.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	site, err := st.SaveSite(ctx, domain.Site{Title: "Test", Type: "Site", Name: "JavLibrary", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "PENDING-1", Title: "Pending", Source: "JavLibrary"}); err != nil {
		t.Fatal(err)
	}
	releases, err := st.Releases(ctx, domain.ReleaseFilter{Search: "PENDING-1"})
	if err != nil || len(releases) != 1 {
		t.Fatalf("release lookup: items=%d err=%v", len(releases), err)
	}

	s := &Server{store: st, log: slog.Default()}
	req := httptest.NewRequest(http.MethodGet, "/covers/1", nil)
	req.SetPathValue("id", strconv.FormatInt(releases[0].ID, 10))
	rec := httptest.NewRecorder()
	s.cover(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if contentType := rec.Header().Get("Content-Type"); !strings.Contains(contentType, "image/svg+xml") {
		t.Fatalf("content type = %q, want SVG", contentType)
	}
	if !strings.Contains(rec.Body.String(), "Cover not yet available") || !strings.Contains(rec.Body.String(), "JAVBEACON") {
		t.Fatal("response did not contain the branded unavailable-cover artwork")
	}
}

// TestDownloadListReturnsPaginatedItemsAndTotal covers Phase 4B: the
// /api/downloads response shape changed from a bare array to {items,total}
// so the Download Activity table can paginate against a true total.
func TestDownloadListReturnsPaginatedItemsAndTotal(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "downloads.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	site, err := st.SaveSite(ctx, domain.Site{Title: "Test", Type: "Site", Name: "JavLibrary", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "DL-1", Title: "DL-1", Source: "JavLibrary"}); err != nil {
		t.Fatal(err)
	}
	all, err := st.Releases(ctx, domain.ReleaseFilter{Search: "DL-1"})
	if err != nil || len(all) != 1 {
		t.Fatalf("seed release lookup: items=%d err=%v", len(all), err)
	}
	releaseID := all[0].ID
	for _, x := range []domain.Download{
		{ReleaseID: releaseID, Query: "Q1", Status: "downloading"},
		{ReleaseID: releaseID, Query: "Q2", Status: "downloading"},
		{ReleaseID: releaseID, Query: "Q3", Status: "completed"},
	} {
		if _, err := st.SaveDownload(ctx, x); err != nil {
			t.Fatal(err)
		}
	}
	s := &Server{store: st, log: slog.Default()}

	rec := httptest.NewRecorder()
	s.downloadList(rec, httptest.NewRequest(http.MethodGet, "/api/downloads?status=downloading&limit=1", nil))
	var body struct {
		Items []domain.Download `json:"items"`
		Total int               `json:"total"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Total != 2 || len(body.Items) != 1 {
		t.Fatalf("body=%+v, want total=2 items=1", body)
	}
}

func TestBulkRemoveDownloadsRunsDestructiveCleanupInBackground(t *testing.T) {
	deleted := make(chan struct{}, 1)
	qbMux := http.NewServeMux()
	qbMux.HandleFunc("POST /api/v2/auth/login", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	qbMux.HandleFunc("GET /api/v2/torrents/info", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[{"hash":"bulkhash","name":"BULK-1 incomplete"}]`))
	})
	qbMux.HandleFunc("POST /api/v2/torrents/delete", func(w http.ResponseWriter, r *http.Request) {
		if r.FormValue("hashes") != "bulkhash" || r.FormValue("deleteFiles") != "true" {
			t.Errorf("bulk delete form hashes=%q deleteFiles=%q", r.FormValue("hashes"), r.FormValue("deleteFiles"))
		}
		deleted <- struct{}{}
		w.WriteHeader(http.StatusNoContent)
	})
	qb := httptest.NewServer(qbMux)
	defer qb.Close()
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "bulk-downloads.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	_ = st.SaveSettings(ctx, map[string]string{"qb_url": qb.URL})
	site, _ := st.SaveSite(ctx, domain.Site{Title: "Test", Type: "Site", Name: "JavLibrary", Enabled: true})
	_, _ = st.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "BULK-1", Title: "BULK-1"})
	releases, _ := st.Releases(ctx, domain.ReleaseFilter{Search: "BULK-1", Limit: 10})
	history, _ := st.SaveDownload(ctx, domain.Download{ReleaseID: releases[0].ID, Query: "BULK-1", TorrentHash: "bulkhash", Status: "downloading"})
	s := &Server{store: st, downloads: download.New(st, time.Second, slog.Default()), log: slog.Default()}
	body, _ := json.Marshal(map[string]any{"ids": []int64{history.ID}, "replace": false})
	rec := httptest.NewRecorder()
	s.bulkRemoveDownloads(rec, httptest.NewRequest(http.MethodPost, "/api/downloads/bulk-remove", bytes.NewReader(body)))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	select {
	case <-deleted:
	case <-time.After(2 * time.Second):
		t.Fatal("background qBittorrent delete did not run")
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		rows, _ := st.Downloads(ctx, "")
		if len(rows) == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("download history was not cleared by background bulk removal")
}

// TestDownloadListFiltersByFilenamePatternExcluded covers TODO-2.0 Task A's
// Download Activity filter: ?filename_pattern_excluded=true must restrict
// the results to downloads flagged by either the manual "Force download"
// override or the Missing Library Files non-preferred-filename fallback
// chain, and omitting the param (or any other value) must not filter at
// all - mirroring downloadList's other query-param-driven filters.
func TestDownloadListFiltersByFilenamePatternExcluded(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "downloads-excluded.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	site, err := st.SaveSite(ctx, domain.Site{Title: "Test", Type: "Site", Name: "JavLibrary", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "DL-2", Title: "DL-2", Source: "JavLibrary"}); err != nil {
		t.Fatal(err)
	}
	all, err := st.Releases(ctx, domain.ReleaseFilter{Search: "DL-2"})
	if err != nil || len(all) != 1 {
		t.Fatalf("seed release lookup: items=%d err=%v", len(all), err)
	}
	releaseID := all[0].ID
	for _, x := range []domain.Download{
		{ReleaseID: releaseID, Query: "NORMAL", Status: "downloading"},
		{ReleaseID: releaseID, Query: "EXCLUDED", Status: "downloading", FilenamePatternExcluded: true},
	} {
		if _, err := st.SaveDownload(ctx, x); err != nil {
			t.Fatal(err)
		}
	}
	s := &Server{store: st, log: slog.Default()}

	rec := httptest.NewRecorder()
	s.downloadList(rec, httptest.NewRequest(http.MethodGet, "/api/downloads?filename_pattern_excluded=true", nil))
	var body struct {
		Items []domain.Download `json:"items"`
		Total int               `json:"total"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Total != 1 || len(body.Items) != 1 || body.Items[0].Query != "EXCLUDED" {
		t.Fatalf("body=%+v, want exactly the EXCLUDED row", body)
	}

	rec = httptest.NewRecorder()
	s.downloadList(rec, httptest.NewRequest(http.MethodGet, "/api/downloads", nil))
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Total != 2 || len(body.Items) != 2 {
		t.Fatalf("body=%+v, want both rows when the filter is omitted", body)
	}
}

// TestPatchReleaseUpdatesLabel covers Phase 6B: PATCH /api/releases/{id}
// accepts a "label" field and persists it via the store.
func TestPatchReleaseUpdatesLabel(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "patch-label.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	site, err := st.SaveSite(ctx, domain.Site{Title: "Test", Type: "Site", Name: "JavLibrary", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "LBL-1", Title: "LBL-1", Source: "JavLibrary"}); err != nil {
		t.Fatal(err)
	}
	all, err := st.Releases(ctx, domain.ReleaseFilter{Search: "LBL-1"})
	if err != nil || len(all) != 1 {
		t.Fatalf("seed release lookup: items=%d err=%v", len(all), err)
	}
	s := &Server{store: st, log: slog.Default()}

	req := httptest.NewRequest(http.MethodPatch, "/api/releases/"+strconv.FormatInt(all[0].ID, 10), strings.NewReader(`{"label":"MOODYZ"}`))
	req.SetPathValue("id", strconv.FormatInt(all[0].ID, 10))
	rec := httptest.NewRecorder()
	s.patchRelease(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body domain.Release
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Label != "MOODYZ" {
		t.Fatalf("response label=%q, want MOODYZ", body.Label)
	}
	got, err := st.Release(ctx, all[0].ID)
	if err != nil || got.Label != "MOODYZ" {
		t.Fatalf("persisted label=%q err=%v", got.Label, err)
	}
}

// TestPatchReleasesBulkAppliesStopMonitoringAndAllowNonPreferredFlag covers
// the "Releases checked by the scheduled job" table's mass-select bulk
// actions: PATCH /api/releases/bulk must apply monitor_download and/or
// allow_non_preferred_filenames to every id in the request in one call.
func TestPatchReleasesBulkAppliesStopMonitoringAndAllowNonPreferredFlag(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "patch-bulk.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	site, err := st.SaveSite(ctx, domain.Site{Title: "Test", Type: "Site", Name: "JavLibrary", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	var ids []int64
	for _, videoID := range []string{"BULKW-1", "BULKW-2"} {
		if _, err := st.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: videoID, Title: videoID, Source: "JavLibrary"}); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := st.Releases(ctx, domain.ReleaseFilter{Limit: 10})
	if err != nil || len(rows) != 2 {
		t.Fatalf("seed release lookup: items=%d err=%v", len(rows), err)
	}
	monitor := true
	for _, r := range rows {
		ids = append(ids, r.ID)
		if err := st.PatchRelease(ctx, r.ID, nil, nil, nil, nil, nil, &monitor, nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	s := &Server{store: st, log: slog.Default()}

	body, _ := json.Marshal(map[string]any{"ids": ids, "monitor_download": false, "allow_non_preferred_filenames": true})
	req := httptest.NewRequest(http.MethodPatch, "/api/releases/bulk", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	s.patchReleasesBulk(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Updated int64 `json:"updated"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp.Updated != 2 {
		t.Fatalf("expected 2 rows updated, got %+v", resp)
	}
	for _, id := range ids {
		got, err := st.Release(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if got.MonitorDownload {
			t.Fatalf("release %d should no longer be monitored: %+v", id, got)
		}
		if !got.AllowNonPreferredFilenames {
			t.Fatalf("release %d should have the allow-non-preferred flag set: %+v", id, got)
		}
	}
}

// TestReleasesCountEndpointMatchesReleasesFilter covers Phase 4A: the new
// /api/releases/count endpoint accepts the same filter params as
// /api/releases and reports the true total, ignoring limit/offset.
func TestReleasesCountEndpointMatchesReleasesFilter(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "releases.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	site, err := st.SaveSite(ctx, domain.Site{Title: "Test", Type: "Site", Name: "JavLibrary", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, videoID := range []string{"MON-1", "MON-2", "MON-3"} {
		if _, err := st.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: videoID, Title: videoID, Source: "JavLibrary", MonitorDownload: true}); err != nil {
			t.Fatal(err)
		}
	}
	s := &Server{store: st, log: slog.Default()}

	rec := httptest.NewRecorder()
	s.releasesCount(rec, httptest.NewRequest(http.MethodGet, "/api/releases/count?monitor_download=true&limit=1", nil))
	var body struct {
		Total int `json:"total"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Total != 3 {
		t.Fatalf("total=%d, want 3 (count must ignore limit)", body.Total)
	}
}

// TestLogEntriesPaginatesWithBeforeAndAfterCursors covers Phase 13's
// incremental log loading over the actual GET /api/logs handler: no cursor
// returns the newest page, `before` pages backward (older entries,
// ascending) for infinite-scroll, and `after` returns only entries strictly
// newer than the cursor for an efficient tail-poll that does not require
// re-fetching the whole visible window on every tick.
func TestLogEntriesPaginatesWithBeforeAndAfterCursors(t *testing.T) {
	ring := logging.NewRing(slog.NewTextHandler(io.Discard, nil), 100)
	log := slog.New(ring)
	for _, msg := range []string{"one", "two", "three", "four", "five"} {
		log.Info(msg)
	}
	s := &Server{logs: ring, log: slog.Default()}

	rec := httptest.NewRecorder()
	s.logEntries(rec, httptest.NewRequest(http.MethodGet, "/api/logs?limit=2", nil))
	var newest []logging.Entry
	if err := json.NewDecoder(rec.Body).Decode(&newest); err != nil {
		t.Fatal(err)
	}
	if len(newest) != 2 || newest[0].Message != "four" || newest[1].Message != "five" {
		t.Fatalf("newest page=%+v, want [four five]", newest)
	}

	rec = httptest.NewRecorder()
	s.logEntries(rec, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/logs?before=%d&limit=10", newest[0].Seq), nil))
	var older []logging.Entry
	if err := json.NewDecoder(rec.Body).Decode(&older); err != nil {
		t.Fatal(err)
	}
	if len(older) != 3 || older[0].Message != "one" || older[1].Message != "two" || older[2].Message != "three" {
		t.Fatalf("older page=%+v, want [one two three]", older)
	}

	rec = httptest.NewRecorder()
	s.logEntries(rec, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/logs?after=%d&limit=10", newest[1].Seq), nil))
	var tail []logging.Entry
	if err := json.NewDecoder(rec.Body).Decode(&tail); err != nil {
		t.Fatal(err)
	}
	if len(tail) != 0 {
		t.Fatalf("tail page before any new entry=%+v, want empty", tail)
	}

	log.Info("six")
	rec = httptest.NewRecorder()
	s.logEntries(rec, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/logs?after=%d&limit=10", newest[1].Seq), nil))
	if err := json.NewDecoder(rec.Body).Decode(&tail); err != nil {
		t.Fatal(err)
	}
	if len(tail) != 1 || tail[0].Message != "six" {
		t.Fatalf("tail page after new entry=%+v, want [six]", tail)
	}
}

func waitForCoverJob(t *testing.T, s *Server) coverCacheStatus {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		status := s.coverCacheStatus()
		if !status.Running {
			return status
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("cover cache job did not finish")
	return coverCacheStatus{}
}

func TestNotificationSortOptionsAndTabDefaults(t *testing.T) {
	raw, err := assets.ReadFile("static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	script := string(raw)
	wantOptions := "notificationSortOptions=[['downloaded','Download date'],['download_started','Download started'],['local_available','Locally available'],['notification','Notification date'],['release','Release date']]"
	if !strings.Contains(script, wantOptions) {
		t.Fatal("notification sort options are missing or not alphabetized")
	}
	wantDefaults := "notificationDefaultSort={new_release:'release',local_available:'local_available',downloaded:'downloaded',download_started:'download_started',download_failed:'notification'}"
	if !strings.Contains(script, wantDefaults) {
		t.Fatal("notification tabs do not have the requested event-date defaults")
	}
}

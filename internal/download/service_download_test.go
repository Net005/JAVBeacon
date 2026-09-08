package download

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Net005/JAVBeacon/internal/domain"
	"github.com/Net005/JAVBeacon/internal/store"
)

func TestDownloadFailureLogIncludesReasonAndContext(t *testing.T) {
	var output bytes.Buffer
	service := &Service{log: slog.New(slog.NewJSONHandler(&output, nil))}
	service.logDownloadFailure(domain.Download{
		ID:              42,
		ReleaseID:       17,
		Query:           "TEST-123",
		Transport:       "http",
		Provider:        "JavDB / Keepshare",
		SourceType:      "Manual Search",
		SourceReference: "https://example.test/share",
		Error:           "HTTP download returned 403: access denied",
	})
	logged := output.String()
	for _, want := range []string{`"msg":"download failed"`, `"download_id":42`, `"release_id":17`, `"video_id":"TEST-123"`, `"transport":"http"`, `"provider":"JavDB / Keepshare"`, `"error":"HTTP download returned 403: access denied"`} {
		if !strings.Contains(logged, want) {
			t.Fatalf("failure log %q does not contain %q", logged, want)
		}
	}
}

func TestHTTPConnectionsDefaultAndBounds(t *testing.T) {
	for name, testCase := range map[string]struct {
		settings map[string]string
		want     int
	}{
		"default": {map[string]string{}, 4},
		"custom":  {map[string]string{"http_download_connections": "3"}, 3},
		"maximum": {map[string]string{"http_download_connections": "99"}, 4},
	} {
		t.Run(name, func(t *testing.T) {
			if got := httpConnections(testCase.settings); got != testCase.want {
				t.Fatalf("connections=%d, want %d", got, testCase.want)
			}
		})
	}
}

func TestHTTPParallelDownloadQueuePromotesFIFO(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "http-fifo.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.SaveSettings(ctx, map[string]string{"http_download_concurrency": "1"}); err != nil {
		t.Fatal(err)
	}
	first := &httpSlotWaiter{downloadID: 101, ready: make(chan struct{})}
	second := &httpSlotWaiter{downloadID: 102, ready: make(chan struct{})}
	service := &Service{store: st, httpActive: 1, httpWaiters: []*httpSlotWaiter{first, second}}

	service.releaseHTTPSlot()
	select {
	case <-first.ready:
	default:
		t.Fatal("oldest queued HTTP download was not promoted first")
	}
	select {
	case <-second.ready:
		t.Fatal("second queued HTTP download was promoted before capacity was available")
	default:
	}

	service.releaseHTTPSlot()
	select {
	case <-second.ready:
	default:
		t.Fatal("second queued HTTP download was not promoted after the first slot was released")
	}
}

func TestVerifyHTTPDownloadFileAgainstPikPakSHA1(t *testing.T) {
	content := []byte("complete downloaded video payload")
	path := filepath.Join(t.TempDir(), "video.part")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha1.Sum(content)
	resolved := resolvedHTTPFile{ChecksumType: "sha1", Checksum: hex.EncodeToString(sum[:])}
	verification, err := verifyHTTPDownloadFile(path, resolved, int64(len(content)))
	if err != nil {
		t.Fatal(err)
	}
	if verification != "verified SHA-1 checksum against PikPak" {
		t.Fatalf("verification=%q", verification)
	}
	resolved.Checksum = strings.Repeat("0", 40)
	if _, err := verifyHTTPDownloadFile(path, resolved, int64(len(content))); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("expected checksum mismatch, got %v", err)
	}
}

func TestVerifyHTTPDownloadFileRejectsOnDiskSizeMismatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "video.part")
	if err := os.WriteFile(path, []byte("short"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := verifyHTTPDownloadFile(path, resolvedHTTPFile{}, 100); err == nil || !strings.Contains(err.Error(), "size mismatch") {
		t.Fatalf("expected size mismatch, got %v", err)
	}
}

func TestHTTPDownloadLifecycleLogIncludesVisibleIdentityAndStatus(t *testing.T) {
	var output bytes.Buffer
	service := &Service{log: slog.New(slog.NewJSONHandler(&output, nil))}
	service.logHTTPDownloadEvent("HTTP download retry queued", domain.Download{ID: 42, ReleaseID: 17, Query: "TEST-123", Provider: "JavDB / Keepshare", Status: "queued", BytesTotal: 12345})
	logged := output.String()
	for _, want := range []string{`"msg":"HTTP download retry queued"`, `"download_id":42`, `"release_id":17`, `"video_id":"TEST-123"`, `"status":"queued"`, `"bytes_total":12345`} {
		if !strings.Contains(logged, want) {
			t.Fatalf("lifecycle log %q does not contain %q", logged, want)
		}
	}
}

func TestOpenHTTPDownloadStreamRetriesTemporaryGatewayFailure(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if r.Header.Get("Referer") != "https://mypikpak.com/" {
			t.Fatalf("required stream headers were not retained on retry")
		}
		if attempts < 3 {
			http.Error(w, "temporary upstream failure", http.StatusBadGateway)
			return
		}
		_, _ = w.Write([]byte("video"))
	}))
	defer server.Close()
	resp, err := openHTTPDownloadStream(context.Background(), server.Client(), resolvedHTTPFile{URL: server.URL, Headers: map[string]string{"Referer": "https://mypikpak.com/"}})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if attempts != 3 {
		t.Fatalf("attempts=%d, want 3", attempts)
	}
}

func TestOpenHTTPDownloadStreamReportsUpstreamFailureDetail(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "origin unavailable", http.StatusBadGateway)
	}))
	defer server.Close()
	_, err := openHTTPDownloadStream(context.Background(), server.Client(), resolvedHTTPFile{URL: server.URL})
	if err == nil || !strings.Contains(err.Error(), "HTTP stream failed after 3 attempt(s): HTTP download returned 502: origin unavailable") {
		t.Fatalf("unexpected error detail: %v", err)
	}
}

func TestDownloadHTTPToFileUsesParallelValidatedRanges(t *testing.T) {
	content := bytes.Repeat([]byte("0123456789abcdef"), 2*1024*1024)
	var rangeRequests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw := strings.TrimPrefix(r.Header.Get("Range"), "bytes=")
		parts := strings.Split(raw, "-")
		if len(parts) != 2 {
			t.Fatalf("missing range request: %q", r.Header.Get("Range"))
		}
		start, _ := strconv.ParseInt(parts[0], 10, 64)
		end, _ := strconv.ParseInt(parts[1], 10, 64)
		rangeRequests.Add(1)
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(content)))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(content[start : end+1])
	}))
	defer server.Close()
	out, err := os.Create(filepath.Join(t.TempDir(), "parallel.part"))
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	var transferred atomic.Int64
	connections, err := downloadHTTPToFile(context.Background(), server.Client(), resolvedHTTPFile{URL: server.URL}, out, int64(len(content)), 4, &transferred, nil)
	if err != nil {
		t.Fatal(err)
	}
	if connections != 2 || rangeRequests.Load() < 3 { // one probe plus two 16 MiB parts
		t.Fatalf("connections=%d range requests=%d", connections, rangeRequests.Load())
	}
	got, _ := os.ReadFile(out.Name())
	if !bytes.Equal(got, content) || transferred.Load() != int64(len(content)) {
		t.Fatalf("parallel result differs: bytes=%d transferred=%d", len(got), transferred.Load())
	}
}

func TestDownloadHTTPToFileCapsOverlongChunkedRangeBodies(t *testing.T) {
	content := bytes.Repeat([]byte("0123456789abcdef"), 2*1024*1024)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw := strings.TrimPrefix(r.Header.Get("Range"), "bytes=")
		parts := strings.Split(raw, "-")
		if len(parts) != 2 {
			t.Fatalf("missing range request: %q", r.Header.Get("Range"))
		}
		start, _ := strconv.ParseInt(parts[0], 10, 64)
		end, _ := strconv.ParseInt(parts[1], 10, 64)
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(content)))
		w.WriteHeader(http.StatusPartialContent)
		if start == 0 && end == 0 {
			_, _ = w.Write(content[:1])
			return
		}
		// Force chunked encoding, then deliberately violate the advertised
		// range by streaming through EOF. The downloader must cap the body at
		// the requested segment boundary so adjacent workers cannot overwrite
		// one another.
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		_, _ = w.Write(content[start:])
	}))
	defer server.Close()
	out, err := os.Create(filepath.Join(t.TempDir(), "overlong-range.part"))
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	var transferred atomic.Int64
	connections, err := downloadHTTPToFile(context.Background(), server.Client(), resolvedHTTPFile{URL: server.URL}, out, int64(len(content)), 4, &transferred, nil)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(out.Name())
	if connections != 2 || !bytes.Equal(got, content) || transferred.Load() != int64(len(content)) {
		t.Fatalf("connections=%d bytes=%d transferred=%d; overlong ranges corrupted the result", connections, len(got), transferred.Load())
	}
}

func TestDownloadHTTPToFileFallsBackWhenRangesUnsupported(t *testing.T) {
	content := []byte("complete original")
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_, _ = w.Write(content)
	}))
	defer server.Close()
	out, err := os.Create(filepath.Join(t.TempDir(), "single.part"))
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	var transferred atomic.Int64
	connections, err := downloadHTTPToFile(context.Background(), server.Client(), resolvedHTTPFile{URL: server.URL}, out, int64(len(content)), 4, &transferred, nil)
	if err != nil {
		t.Fatal(err)
	}
	if connections != 1 || requests.Load() != 2 {
		t.Fatalf("connections=%d requests=%d, want range probe plus single stream", connections, requests.Load())
	}
}

func TestDownloadHTTPToFileReducesConnectionsAfterGatewayFailures(t *testing.T) {
	content := bytes.Repeat([]byte("adaptive"), 7*1024*1024)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") == "bytes=0-0" {
			w.Header().Set("Content-Range", fmt.Sprintf("bytes 0-0/%d", len(content)))
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(content[:1])
			return
		}
		if r.Header.Get("Range") != "" {
			http.Error(w, "too many upstream connections", http.StatusBadGateway)
			return
		}
		_, _ = w.Write(content)
	}))
	defer server.Close()
	out, err := os.Create(filepath.Join(t.TempDir(), "adaptive.part"))
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	oldDelay := httpRangeRetryBaseDelay
	httpRangeRetryBaseDelay = time.Millisecond
	defer func() { httpRangeRetryBaseDelay = oldDelay }()
	var transferred atomic.Int64
	var downgrades []string
	connections, err := downloadHTTPToFile(context.Background(), server.Client(), resolvedHTTPFile{URL: server.URL}, out, int64(len(content)), 8, &transferred, func(from, to int, _ error) {
		downgrades = append(downgrades, fmt.Sprintf("%d->%d", from, to))
	})
	if err != nil {
		t.Fatal(err)
	}
	if connections != 1 || strings.Join(downgrades, ",") != "4->2,2->1" {
		t.Fatalf("connections=%d downgrades=%v", connections, downgrades)
	}
	got, _ := os.ReadFile(out.Name())
	if !bytes.Equal(got, content) || transferred.Load() != int64(len(content)) {
		t.Fatalf("single-stream fallback differs: bytes=%d transferred=%d", len(got), transferred.Load())
	}
}

func TestDownloadHTTPSingleStreamResumesAfterInterruptedBody(t *testing.T) {
	content := bytes.Repeat([]byte("resume"), 1024*1024)
	cut := len(content) / 3
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Header.Get("Range") == "" {
			w.Header().Set("Content-Length", strconv.Itoa(len(content)))
			_, _ = w.Write(content[:cut])
			return
		}
		startRaw := strings.TrimSuffix(strings.TrimPrefix(r.Header.Get("Range"), "bytes="), fmt.Sprintf("-%d", len(content)-1))
		start, _ := strconv.Atoi(startRaw)
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, len(content)-1, len(content)))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(content[start:])
	}))
	defer server.Close()
	out, err := os.Create(filepath.Join(t.TempDir(), "resumed.part"))
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	oldDelay := httpRangeRetryBaseDelay
	httpRangeRetryBaseDelay = time.Millisecond
	defer func() { httpRangeRetryBaseDelay = oldDelay }()
	var transferred atomic.Int64
	if err := downloadHTTPSingleStream(context.Background(), server.Client(), resolvedHTTPFile{URL: server.URL}, out, int64(len(content)), &transferred); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(out.Name())
	if requests.Load() != 2 || !bytes.Equal(got, content) || transferred.Load() != int64(len(content)) {
		t.Fatalf("requests=%d bytes=%d transferred=%d", requests.Load(), len(got), transferred.Load())
	}
}

func TestHTTPDownloadSizeMismatchExplainsAnonymousPreview(t *testing.T) {
	resp := &http.Response{
		StatusCode:    http.StatusPartialContent,
		ContentLength: 2413882923,
		Header:        http.Header{"Content-Range": []string{"bytes 0-2413882922/4331682987"}},
	}
	err := httpDownloadSizeMismatchError(resp, 4331682987, false)
	for _, want := range []string{"partial anonymous stream", "2413882923 bytes", "4331682987-byte original", "complete original is unavailable"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not contain %q", err, want)
		}
	}
}

func TestHTTPDownloadSizeMismatchExplainsAuthenticatedAccountLimit(t *testing.T) {
	resp := &http.Response{
		StatusCode:    http.StatusPartialContent,
		ContentLength: 2413882923,
		Header:        http.Header{"Content-Range": []string{"bytes 0-2413882922/4331682987"}},
	}
	err := httpDownloadSizeMismatchError(resp, 4331682987, true)
	for _, want := range []string{"PikPak account", "partial stream", "storage", "transfer quota", "restore status"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not contain %q", err, want)
		}
	}
}

func TestDownloadRechecksFilenameRulesServerSide(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "downloads.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.SaveSettings(ctx, map[string]string{"accepted_patterns": "trusted@"}); err != nil {
		t.Fatal(err)
	}
	site, _ := st.SaveSite(ctx, domain.Site{Title: "Test", Type: "Site", Name: "JavLibrary", Enabled: true})
	_, _ = st.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "PRED-888", Title: "Test", Source: "JavLibrary", Released: true})
	releases, _ := st.Releases(ctx, domain.ReleaseFilter{Limit: 10})
	service := New(st, time.Second, slog.Default())
	sourceURL := "https://sukebei.nyaa.si/view/4544529"
	result, err := service.Download(ctx, releases[0], domain.SearchResult{Provider: "Sukebei/Nyaa", Title: "PRED-888 untrusted filename", Link: "magnet:?xt=fake", SourceURL: sourceURL, Accepted: true}, "Manual Search", "test")
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "failed" || result.Error != "result rejected by filename rules" {
		t.Fatalf("client-provided acceptance was trusted: %+v", result)
	}
	if result.SourceReference != sourceURL {
		t.Fatalf("download source reference=%q, want torrent detail page %q", result.SourceReference, sourceURL)
	}
}

// TestDownloadProceedsImmediatelyRegardlessOfReleaseDate covers Phase 1 of
// TODO.md: Scheduled Download no longer exists, so a release with an accepted
// torrent match is processed immediately whether its JavLibrary release date
// is in the past, today, or in the future. A match found on the configured
// download source is itself evidence of availability and overrides an
// apparently future JavLibrary date. Without a qb_url configured, an
// immediately processed download reaches the qBittorrent step and fails with
// "qBittorrent URL is not configured" rather than returning early with the
// retired "scheduled" status, so that specific, stable error is what proves
// no date-based deferral happened.
func TestDownloadProceedsImmediatelyRegardlessOfReleaseDate(t *testing.T) {
	today := time.Now().UTC().Format("2006-01-02")
	yesterday := time.Now().UTC().AddDate(0, 0, -1).Format("2006-01-02")
	tomorrow := time.Now().UTC().AddDate(0, 0, 1).Format("2006-01-02")
	cases := []struct {
		name        string
		released    bool
		releaseDate string
	}{
		{"past release date", true, yesterday},
		{"current release date", true, today},
		{"future release date with a download match", false, tomorrow},
		{"future release date with no release date recorded yet", false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "downloads.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			if err := st.SaveSettings(ctx, map[string]string{"accepted_patterns": "trusted@"}); err != nil {
				t.Fatal(err)
			}
			site, _ := st.SaveSite(ctx, domain.Site{Title: "Test", Type: "Site", Name: "JavLibrary", Enabled: true})
			_, _ = st.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "PRED-889", Title: "Test", Source: "JavLibrary", Released: tc.released, ReleaseDate: tc.releaseDate})
			releases, _ := st.Releases(ctx, domain.ReleaseFilter{Search: "PRED-889", Limit: 10})
			if len(releases) != 1 {
				t.Fatalf("release setup failed: %+v", releases)
			}
			service := New(st, time.Second, slog.Default())
			// An error is expected here: qBittorrent is not configured, and
			// reaching that failure (rather than an early "scheduled" return
			// with no error) is exactly what this test verifies.
			result, _ := service.Download(ctx, releases[0], domain.SearchResult{Provider: "Sukebei/Nyaa", Title: "trusted@PRED-889", Link: "magnet:?xt=fake"}, "Manual Search", "test")
			if result.Status == "scheduled" {
				t.Fatalf("download was deferred with the retired scheduled status: %+v", result)
			}
			if result.Status != "failed" || result.Error != "qBittorrent URL is not configured" {
				t.Fatalf("expected the download to proceed immediately to the qBittorrent step, got: %+v", result)
			}
		})
	}
}

// TestDownloadForcedOverrideBypassesFilenameRejection covers Phase 5B:
// forcing a download must bypass the automatic accepted-filename rejection
// for that one result, while still recording in history that it was a
// manual override rather than a normal accepted match.
func TestDownloadForcedOverrideBypassesFilenameRejection(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "downloads.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.SaveSettings(ctx, map[string]string{"accepted_patterns": "trusted@"}); err != nil {
		t.Fatal(err)
	}
	site, _ := st.SaveSite(ctx, domain.Site{Title: "Test", Type: "Site", Name: "JavLibrary", Enabled: true})
	_, _ = st.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "PRED-890", Title: "Test", Source: "JavLibrary", Released: true})
	releases, _ := st.Releases(ctx, domain.ReleaseFilter{Search: "PRED-890", Limit: 10})
	if len(releases) != 1 {
		t.Fatalf("release setup failed: %+v", releases)
	}
	service := New(st, time.Second, slog.Default())

	rejected, err := service.Download(ctx, releases[0], domain.SearchResult{Provider: "Sukebei/Nyaa", Title: "PRED-890 untrusted filename", Link: "magnet:?xt=fake"}, "Manual Search", "test")
	if err != nil {
		t.Fatal(err)
	}
	if rejected.Status != "failed" || rejected.Error != "result rejected by filename rules" {
		t.Fatalf("baseline (non-forced) result was not rejected: %+v", rejected)
	}

	forced, err := service.Download(ctx, releases[0], domain.SearchResult{Provider: "Sukebei/Nyaa", Title: "PRED-890 untrusted filename", Link: "magnet:?xt=fake", Forced: true}, "Manual Search", "test")
	if err != nil && forced.Status == "" {
		t.Fatal(err)
	}
	if forced.Status == "failed" && forced.Error == "result rejected by filename rules" {
		t.Fatalf("forced download was still rejected by filename rules: %+v", forced)
	}
	if forced.Status != "failed" || forced.Error != "qBittorrent URL is not configured" {
		t.Fatalf("expected the forced download to reach the qBittorrent step, got: %+v err=%v", forced, err)
	}
	if !strings.Contains(forced.MatchReason, "manually forced") {
		t.Fatalf("forced download history does not record the manual override: %+v", forced)
	}
}

func TestManualLocalRedownloadRequiresExplicitIgnoreLocalOverride(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "local-redownload.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.SaveSettings(ctx, map[string]string{"accepted_patterns": "trusted@"}); err != nil {
		t.Fatal(err)
	}
	site, _ := st.SaveSite(ctx, domain.Site{Title: "Test", Type: "Site", Name: "JavLibrary", Enabled: true})
	_, _ = st.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "PRED-891", Title: "Test", Source: "JavLibrary", Released: true})
	releases, _ := st.Releases(ctx, domain.ReleaseFilter{Search: "PRED-891", Limit: 1})
	if len(releases) != 1 {
		t.Fatalf("release setup failed: %+v", releases)
	}
	if err := st.SetStashState(ctx, releases[0].ID, true, "stash-scene-891"); err != nil {
		t.Fatal(err)
	}
	release, err := st.Release(ctx, releases[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	service := New(st, time.Second, slog.Default())
	result := domain.SearchResult{Provider: "Sukebei/Nyaa", Title: "trusted@PRED-891", Link: "magnet:?xt=fake"}

	skipped, err := service.Download(ctx, release, result, "Manual Search", "test")
	if err != nil || skipped.Status != "skipped" || skipped.MatchReason != "release already exists in StashApp" {
		t.Fatalf("ordinary manual download should preserve the local guard: %+v err=%v", skipped, err)
	}

	result.IgnoreLocal = true
	forced, err := service.Download(ctx, release, result, "Manual Search", "test")
	if err == nil || forced.Status != "failed" || forced.Error != "qBittorrent URL is not configured" {
		t.Fatalf("explicit local override did not reach the download client: %+v err=%v", forced, err)
	}
	if forced.FilenamePatternExcluded || !strings.Contains(forced.MatchReason, "existing StashApp match") {
		t.Fatalf("local override was not recorded independently from filename matching: %+v", forced)
	}
}

func TestHTTPDownloadDoesNotRequireQBittorrent(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "http-with-qb-down.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	media := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("video"))
	}))
	defer media.Close()
	if err := st.SaveSettings(ctx, map[string]string{
		"qb_url":                  "http://127.0.0.1:1",
		"http_download_directory": t.TempDir(),
	}); err != nil {
		t.Fatal(err)
	}
	site, _ := st.SaveSite(ctx, domain.Site{Title: "Test", Type: "Site", Name: "JavLibrary", Enabled: true})
	_, _ = st.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "HTTP-891", Title: "Test", Source: "JavLibrary", Released: true})
	releases, _ := st.Releases(ctx, domain.ReleaseFilter{Search: "HTTP-891", Limit: 1})
	if len(releases) != 1 {
		t.Fatalf("release setup failed: %+v", releases)
	}
	service := New(st, time.Second, slog.Default())
	queued, err := service.Download(ctx, releases[0], domain.SearchResult{
		Provider:  "JavDB / Keepshare",
		Title:     "HTTP-891.mp4",
		Link:      media.URL,
		Transport: "http",
		Accepted:  true,
	}, "Manual Search", media.URL)
	if err != nil || queued.Status != "queued" || queued.Transport != "http" {
		t.Fatalf("HTTP download incorrectly depended on qBittorrent: %+v err=%v", queued, err)
	}
}

func TestHTTPDownloadSkipsFetchWhenDestinationFileAlreadyExists(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "http-skip-existing.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	var requests atomic.Int64
	media := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_, _ = w.Write([]byte("video"))
	}))
	defer media.Close()

	downloadDir := t.TempDir()
	existingPath := filepath.Join(downloadDir, "HTTPSKIP-1.mp4")
	if err := os.WriteFile(existingPath, []byte("already downloaded"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveSettings(ctx, map[string]string{"http_download_directory": downloadDir}); err != nil {
		t.Fatal(err)
	}
	site, _ := st.SaveSite(ctx, domain.Site{Title: "Test", Type: "Site", Name: "JavLibrary", Enabled: true})
	_, _ = st.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "HTTPSKIP-1", Title: "Test", Source: "JavLibrary", Released: true})
	releases, _ := st.Releases(ctx, domain.ReleaseFilter{Search: "HTTPSKIP-1", Limit: 1})
	if len(releases) != 1 {
		t.Fatalf("release setup failed: %+v", releases)
	}
	service := New(st, time.Second, slog.Default())
	queued, err := service.Download(ctx, releases[0], domain.SearchResult{
		Provider:  "JavDB / Keepshare",
		Title:     "HTTPSKIP-1.mp4",
		Link:      media.URL,
		Transport: "http",
		Accepted:  true,
	}, "Manual Search", media.URL)
	if err != nil {
		t.Fatalf("queue HTTP download: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	var final domain.Download
	for time.Now().Before(deadline) {
		downloads, listErr := st.Downloads(ctx, "")
		if listErr != nil {
			t.Fatal(listErr)
		}
		for _, d := range downloads {
			if d.ID == queued.ID && d.Status != "queued" && d.Status != "downloading" {
				final = d
			}
		}
		if final.ID != 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if final.ID == 0 {
		t.Fatal("HTTP download never reached a terminal status")
	}
	if final.Status != "completed" {
		t.Fatalf("status = %q, want completed: %+v", final.Status, final)
	}
	if final.DestinationPath != existingPath {
		t.Fatalf("destination path = %q, want existing file %q", final.DestinationPath, existingPath)
	}
	if !strings.Contains(final.MatchReason, "already present locally") {
		t.Fatalf("match reason did not explain the skip: %q", final.MatchReason)
	}
	if got := requests.Load(); got != 0 {
		t.Fatalf("expected the HTTP source to never be fetched, got %d request(s)", got)
	}
	// The pre-existing file's content must be left untouched - no
	// "-0"/"-1" duplicate created beside it either.
	entries, err := os.ReadDir(downloadDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected exactly one file in the download directory, got %+v", entries)
	}
	data, err := os.ReadFile(existingPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "already downloaded" {
		t.Fatalf("existing file content was overwritten: %q", data)
	}
}

func TestHTTPVideoCheckRedownloadsOnceThenFailsClearly(t *testing.T) {
	row := domain.Download{Status: "downloading", Progress: 1, BytesDownloaded: 100, BytesPerSecond: 5, ETASeconds: 2}
	retry, shouldRetry := markHTTPVideoCheckFailure(row, errors.New("invalid media packet"))
	if !shouldRetry || retry.Status != "downloading" || retry.PostStatus != postStatusVideoRetry || !strings.Contains(retry.Error, "failed video check") || retry.Progress != 0 || retry.BytesDownloaded != 0 {
		t.Fatalf("first failure = %+v, retry=%v", retry, shouldRetry)
	}
	failed, shouldRetry := markHTTPVideoCheckFailure(retry, errors.New("moov atom not found"))
	if shouldRetry || failed.Status != "failed" || failed.PostStatus != postStatusVideoFailed || !strings.Contains(failed.Error, "failed again after automatic re-download") {
		t.Fatalf("second failure = %+v, retry=%v", failed, shouldRetry)
	}
}

func TestForceRedownloadIgnoresHistoryButNotActiveSameTransport(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "force-history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	site, _ := st.SaveSite(ctx, domain.Site{Title: "Test", Type: "Site", Name: "JavLibrary", Enabled: true})
	_, _ = st.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "FORCE-100", Title: "Test", Source: "JavLibrary", Released: true})
	releases, _ := st.Releases(ctx, domain.ReleaseFilter{Search: "FORCE-100", Limit: 1})
	if len(releases) != 1 {
		t.Fatalf("release setup failed: %+v", releases)
	}
	release := releases[0]
	service := New(st, time.Second, slog.Default())

	_, _ = st.SaveDownload(ctx, domain.Download{ReleaseID: release.ID, Query: release.VideoID, Transport: "torrent", Status: "completed"})
	if reason, _, _, err := service.duplicateStored(ctx, release, true, true, "torrent"); err != nil || reason != "" {
		t.Fatalf("forced torrent was blocked by completed history: reason=%q err=%v", reason, err)
	}

	_, _ = st.SaveDownload(ctx, domain.Download{ReleaseID: release.ID, Query: release.VideoID, Transport: "http", Status: "downloading"})
	if reason, _, _, err := service.duplicateStored(ctx, release, true, true, "torrent"); err != nil || reason != "" {
		t.Fatalf("forced torrent was blocked by active HTTP download: reason=%q err=%v", reason, err)
	}

	activeTorrent, _ := st.SaveDownload(ctx, domain.Download{ReleaseID: release.ID, Query: release.VideoID, Transport: "torrent", Status: "queued"})
	reason, existingID, replaceable, err := service.duplicateStored(ctx, release, true, true, "torrent")
	if err != nil || reason != "release already has an active torrent download in state queued" || existingID != activeTorrent.ID || replaceable {
		t.Fatalf("active same-transport download was not preserved: reason=%q id=%d replaceable=%v err=%v", reason, existingID, replaceable, err)
	}

	if reason, _, _, err := service.duplicateStored(ctx, release, true, true, "http"); err != nil || !strings.Contains(reason, "active http download") {
		t.Fatalf("active HTTP download did not block forced HTTP duplicate: reason=%q err=%v", reason, err)
	}
}

func TestSiteWatchlistRuleDoesNotRetroactivelyChangeExistingRelease(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "site-watchlist.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	site, err := st.SaveSite(ctx, domain.Site{Title: "Future Watchlist", Type: "Site", Name: "JavLibrary", Watchlist: true, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "TEST-100", Title: "Existing", Source: "JavLibrary", Watchlist: false})
	if err != nil {
		t.Fatal(err)
	}
	releases, err := st.Releases(ctx, domain.ReleaseFilter{Search: "TEST-100", Limit: 10})
	if err != nil || len(releases) != 1 {
		t.Fatalf("release setup failed: rows=%+v err=%v", releases, err)
	}

	New(st, time.Second, slog.Default()).Auto(ctx, releases[0])
	unchanged, err := st.Release(ctx, releases[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.Watchlist {
		t.Fatal("site-level Watchlist rule changed an existing release")
	}
}

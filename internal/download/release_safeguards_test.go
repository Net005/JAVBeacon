package download

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/Net005/JAVBeacon/internal/domain"
	"github.com/Net005/JAVBeacon/internal/store"
)

func safeguardTestService(t *testing.T) (*Service, *store.SQLite, domain.Release) {
	t.Helper()
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "safeguards.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	site, err := st.SaveSite(ctx, domain.Site{Title: "Synthetic", Name: "JavLibrary", Type: "Site"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "TEST-123", Title: "Synthetic", Source: "JavLibrary"})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := st.Releases(ctx, domain.ReleaseFilter{Limit: 1})
	if err != nil || len(rows) != 1 {
		t.Fatalf("release: %v", err)
	}
	return &Service{store: st, log: slog.New(slog.NewTextHandler(io.Discard, nil))}, st, rows[0]
}

func TestSafeguardFutureSearchRejectsBlacklistedURL(t *testing.T) {
	s, st, r := safeguardTestService(t)
	ctx := context.Background()
	const link = "https://downloads.example.com/TEST-123.torrent"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `<rss><channel><item><title>TEST-123.mp4</title><link>`+link+`</link><size>1 GiB</size></item></channel></rss>`)
	}))
	defer server.Close()
	s.client = server.Client()
	if err := st.SaveSettings(ctx, map[string]string{"search_url_template": server.URL}); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveBlacklistedRelease(ctx, domain.BlacklistedRelease{URL: link, Reason: "Synthetic rejection"}); err != nil {
		t.Fatal(err)
	}
	rows, err := s.Search(ctx, r)
	if err != nil || len(rows) != 1 || rows[0].Accepted || !rows[0].BlacklistedReleaseMatch {
		t.Fatalf("blacklisted URL matched again: %+v %v", rows, err)
	}
}

func TestSafeguardShortTorrentIsBlacklistedWithoutRetryOrDeletion(t *testing.T) {
	s, st, r := safeguardTestService(t)
	ctx := context.Background()
	dir := t.TempDir()
	probe := filepath.Join(dir, "ffprobe")
	if err := os.WriteFile(probe, []byte("#!/bin/sh\nprintf '%s' '{\"format\":{\"duration\":\"599\"},\"streams\":[{\"codec_type\":\"video\",\"nb_read_packets\":\"100\"}]}'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	path := filepath.Join(dir, "TEST-123.mp4")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	file.Close()
	if err = os.Truncate(path, 200_000_000); err != nil {
		t.Fatal(err)
	}
	d, err := st.SaveDownload(ctx, domain.Download{ReleaseID: r.ID, Query: r.VideoID, Name: "TEST-123.mp4", Transport: "torrent", Status: "downloading", TransferReference: "https://downloads.example.com/synthetic.torrent"})
	if err != nil {
		t.Fatal(err)
	}
	if !s.handleTorrentVideoCheck(ctx, nil, &d, Torrent{ContentPath: path}) {
		t.Fatal("short video was allowed into completion pipeline")
	}
	if d.Status != "failed" || d.PostStatus != postStatusBlacklistedRelease {
		t.Fatalf("short video retried instead of blacklisted: %+v", d)
	}
	if _, blocked, err := st.BlacklistedRelease(ctx, d.TransferReference); err != nil || !blocked {
		t.Fatalf("source not blacklisted: %v", err)
	}
	if _, err = os.Stat(path); err != nil {
		t.Fatal("safeguard removed a file")
	}
}

func TestSafeguardExistingHTTPFileCannotBypassChecks(t *testing.T) {
	s, st, r := safeguardTestService(t)
	ctx := context.Background()
	dir := t.TempDir()
	if err := st.SaveSettings(ctx, map[string]string{"http_download_directory": dir}); err != nil {
		t.Fatal(err)
	}
	path := httpDestinationPath(dir, r.VideoID)
	if err := os.WriteFile(path, []byte("synthetic-small-file"), 0600); err != nil {
		t.Fatal(err)
	}
	d, err := st.SaveDownload(ctx, domain.Download{ReleaseID: r.ID, Query: r.VideoID, Name: r.VideoID, Transport: "http", Status: "queued", SourceReference: "https://keepshare.org/s/synthetic"})
	if err != nil {
		t.Fatal(err)
	}
	s.runHTTPDownload(ctx, d)
	rows, err := st.Downloads(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Status != "failed" || rows[0].PostStatus != "blocked_existing_file" {
		t.Fatalf("existing file bypassed safeguards: %+v", rows)
	}
	if _, blocked, err := st.BlacklistedRelease(ctx, d.SourceReference); err != nil || blocked {
		t.Fatalf("unrelated remote source was blacklisted: %v", err)
	}
	if _, err = os.Stat(path); err != nil {
		t.Fatal("existing file removed")
	}
}

func TestSafeguardBlacklistsShareAliasesTogether(t *testing.T) {
	s, st, r := safeguardTestService(t)
	ctx := context.Background()
	keep := "https://keepshare.org/s/synthetic"
	pik := "https://mypikpak.com/s/synthetic"
	result := domain.SearchResult{Link: keep, SourceAliases: []string{pik}, Title: "TEST-123.mp4", Transport: "http", Accepted: true, SizeBytes: 100}
	d, err := s.Download(ctx, r, result, "Synthetic", keep)
	if err != nil || d.PostStatus != postStatusBlacklistedRelease {
		t.Fatalf("reject: %+v %v", d, err)
	}
	for _, link := range []string{keep, pik} {
		if _, blocked, err := st.BlacklistedRelease(ctx, link); err != nil || !blocked {
			t.Fatalf("alias not blacklisted: %s %v", link, err)
		}
	}
}

func TestSafeguardSmallCandidateNeverAcquiredOrForced(t *testing.T) {
	for _, transport := range []string{"http", "torrent"} {
		t.Run(transport, func(t *testing.T) {
			s, st, r := safeguardTestService(t)
			ctx := context.Background()
			result := domain.SearchResult{Title: "TEST-123.mp4", Link: "https://downloads.example.com/TEST-123", Accepted: true, Forced: true, IgnoreLocal: true, ReplaceExisting: true, Transport: transport, SizeBytes: 199_999_999}
			d, err := s.Download(ctx, r, result, "Synthetic", result.Link)
			if err != nil || d.Status != "failed" || d.PostStatus != "blacklisted_release" {
				t.Fatalf("unsafe candidate not blocked: %+v %v", d, err)
			}
			entries, total, err := st.BlacklistedReleases(ctx, "", 25, 0)
			if err != nil || total != 1 || entries[0].URL != result.Link || entries[0].SizeBytes != result.SizeBytes {
				t.Fatalf("blacklist: %+v %d %v", entries, total, err)
			}
			// A changed threshold or a false size submitted on a later force request
			// cannot resurrect a URL that was already rejected.
			if err = st.SaveSettings(ctx, map[string]string{"download_min_filesize_mb": "0"}); err != nil {
				t.Fatal(err)
			}
			result.SizeBytes = 900_000_000
			d, err = s.Download(ctx, r, result, "Synthetic", result.Link)
			if err != nil || d.PostStatus != "blacklisted_release" {
				t.Fatalf("blacklist bypass: %+v %v", d, err)
			}
		})
	}
}

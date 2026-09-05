package download

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Net005/JAVBeacon/internal/domain"
	"github.com/Net005/JAVBeacon/internal/store"
)

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

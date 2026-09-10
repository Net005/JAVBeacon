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

// TestFallbackSearchCandidate exercises the three-tier fallback chain in
// isolation from search/HTTP/store plumbing: given a display-sorted result
// list and the provider's native order, it must pick the right candidate
// for each tier. Only Accepted, non-blacklisted results are ever eligible -
// preferred filename patterns influence the sort order (see
// TestSortSearchResultsUsesFilenamePriorityBeforeSeedCount) but are not a
// separate accept/reject concept here.
func TestFallbackSearchCandidate(t *testing.T) {
	t.Run("tier1: seeded accepted match wins outright", func(t *testing.T) {
		acceptedSeeded := domain.SearchResult{Title: "accepted-seeded", Accepted: true, Seeds: 4}
		rejectedHighSeed := domain.SearchResult{Title: "rejected-high-seed", Accepted: false, Seeds: 9}
		sorted := sortSearchResults([]domain.SearchResult{rejectedHighSeed, acceptedSeeded})
		got, found := fallbackSearchCandidate(sorted, sorted)
		if !found || got.Title != "accepted-seeded" {
			t.Fatalf("expected the seeded accepted match, got %+v found=%v", got, found)
		}
	})

	t.Run("tier2: falls back to the best-seeded accepted result when the top-priority match has no seeds", func(t *testing.T) {
		bestPriorityNoSeeds := domain.SearchResult{Title: "best-priority-no-seeds", Accepted: true, PreferredFilenameMatch: true, PreferredFilenamePriority: 1, Seeds: 0}
		lowerPrioritySeeded := domain.SearchResult{Title: "lower-priority-seeded", Accepted: true, PreferredFilenameMatch: true, PreferredFilenamePriority: 10, Seeds: 4}
		rejectedHighSeed := domain.SearchResult{Title: "rejected-high-seed", Accepted: false, Seeds: 9}
		native := []domain.SearchResult{bestPriorityNoSeeds, rejectedHighSeed, lowerPrioritySeeded}
		sorted := sortSearchResults(native)
		if sorted[0].Title != bestPriorityNoSeeds.Title {
			t.Fatalf("test setup assumption broken: expected the best-priority match to sort first, got %+v", sorted)
		}
		got, found := fallbackSearchCandidate(sorted, native)
		if !found || got.Title != "lower-priority-seeded" {
			t.Fatalf("expected the best-seeded accepted result, got %+v found=%v", got, found)
		}
	})

	t.Run("tier3: falls back to the most recent (native order) accepted result when nothing has seeds", func(t *testing.T) {
		acceptedNewest := domain.SearchResult{Title: "accepted-newest", Accepted: true, Seeds: 0}
		acceptedOlder := domain.SearchResult{Title: "accepted-older", Accepted: true, Seeds: 0}
		native := []domain.SearchResult{acceptedNewest, acceptedOlder}
		sorted := sortSearchResults(native)
		got, found := fallbackSearchCandidate(sorted, native)
		if !found || got.Title != acceptedNewest.Title {
			t.Fatalf("expected native[0] as the last-resort candidate, got %+v found=%v (native[0]=%+v)", got, found, native[0])
		}
	})

	t.Run("finds nothing when no result is accepted", func(t *testing.T) {
		rejectedHighSeed := domain.SearchResult{Title: "rejected-high-seed", Accepted: false, Seeds: 9}
		rejectedNoSeed := domain.SearchResult{Title: "rejected-no-seed", Accepted: false, Seeds: 0}
		sorted := []domain.SearchResult{rejectedHighSeed, rejectedNoSeed}
		_, found := fallbackSearchCandidate(sorted, sorted)
		if found {
			t.Fatalf("expected no candidate without any accepted match")
		}
	})

	t.Run("finds nothing when there are no results at all", func(t *testing.T) {
		_, found := fallbackSearchCandidate(nil, nil)
		if found {
			t.Fatalf("expected no candidate when the search returned nothing")
		}
	})

	t.Run("blacklisted results are excluded even when accepted elsewhere", func(t *testing.T) {
		blacklisted := domain.SearchResult{Title: "blacklisted-high-seed", Accepted: true, Seeds: 100, BlacklistedFilenameMatch: true}
		clean := domain.SearchResult{Title: "clean-low-seed", Accepted: true, Seeds: 1}
		got, found := fallbackSearchCandidate([]domain.SearchResult{blacklisted, clean}, []domain.SearchResult{blacklisted, clean})
		if !found || got.Title != clean.Title {
			t.Fatalf("expected clean fallback, got %+v found=%v", got, found)
		}
		if _, found := fallbackSearchCandidate([]domain.SearchResult{blacklisted}, []domain.SearchResult{blacklisted}); found {
			t.Fatal("blacklisted-only results must not produce a fallback candidate")
		}
	})
}

func TestSortSearchResultsUsesFilenamePriorityBeforeSeedCount(t *testing.T) {
	rows := sortSearchResults([]domain.SearchResult{
		{Title: "priority-ten", Accepted: true, PreferredFilenameMatch: true, PreferredFilenamePriority: 10, Seeds: 100},
		{Title: "priority-one", Accepted: true, PreferredFilenameMatch: true, PreferredFilenamePriority: 1, Seeds: 1},
	})
	if len(rows) != 2 || rows[0].Title != "priority-one" {
		t.Fatalf("priority order = %+v", rows)
	}
}

func TestEffectiveDownloadMethodHonorsStrictReleaseOverride(t *testing.T) {
	settings := map[string]string{"default_download_method": "torrent_http"}
	if got := effectiveDownloadMethod(settings, domain.Release{DownloadMethodOverride: "HTTP"}); got != downloadHTTPOnly {
		t.Fatalf("HTTP release override = %q, want %q", got, downloadHTTPOnly)
	}
	settings["default_download_method"] = "http_torrent"
	if got := effectiveDownloadMethod(settings, domain.Release{DownloadMethodOverride: "torrent"}); got != downloadTorrentOnly {
		t.Fatalf("Torrent release override = %q, want %q", got, downloadTorrentOnly)
	}
	if got := effectiveDownloadMethod(settings, domain.Release{}); got != downloadHTTPTorrent {
		t.Fatalf("empty override should inherit global method, got %q", got)
	}
}

func TestTorrentHTTPFallbackReasonRequiresStalledUnhealthyTorrent(t *testing.T) {
	now := time.Now().UTC()
	download := domain.Download{Transport: "torrent", Status: "downloading", AddedAt: now.Add(-defaultTorrentHTTPFallbackDelay - time.Second), Progress: .25}

	if got := torrentHTTPFallbackReason(download, Torrent{State: "stalledDL", Progress: .25, Seeds: 0, SeenComplete: 0}, now, defaultTorrentHTTPFallbackDelay); !strings.Contains(got, "no seeders") {
		t.Fatalf("zero-seed stalled torrent fallback reason = %q", got)
	}
	if got := torrentHTTPFallbackReason(download, Torrent{State: "stalledDL", Progress: .25, Seeds: 2, SeenComplete: 0}, now, defaultTorrentHTTPFallbackDelay); !strings.Contains(got, "never been seen complete") {
		t.Fatalf("never-completed stalled torrent fallback reason = %q", got)
	}
	if got := torrentHTTPFallbackReason(download, Torrent{State: "downloading", Progress: .25, Seeds: 0, SeenComplete: 0}, now, defaultTorrentHTTPFallbackDelay); got != "" {
		t.Fatalf("actively downloading torrent must not fall back, got %q", got)
	}
	if got := torrentHTTPFallbackReason(download, Torrent{State: "stalledDL", Progress: .30, Seeds: 0, SeenComplete: 0}, now, defaultTorrentHTTPFallbackDelay); got != "" {
		t.Fatalf("torrent whose progress advanced must not fall back, got %q", got)
	}
	fresh := download
	fresh.AddedAt = now.Add(-defaultTorrentHTTPFallbackDelay + time.Second)
	if got := torrentHTTPFallbackReason(fresh, Torrent{State: "stalledDL", Progress: .25, Seeds: 0}, now, defaultTorrentHTTPFallbackDelay); got != "" {
		t.Fatalf("fresh torrent must receive its grace period, got %q", got)
	}
}

func TestTorrentHTTPFallbackDelayIsPersistentAndDefaultsToEightHours(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "http-fallback-delay.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	service := New(st, time.Second, slog.Default())
	if got := service.torrentHTTPFallbackDelay(ctx); got != 8*time.Hour {
		t.Fatalf("default fallback delay = %s, want 8h", got)
	}
	if err := st.SaveSettings(ctx, map[string]string{"http_fallback_delay": "90m"}); err != nil {
		t.Fatal(err)
	}
	if got := service.torrentHTTPFallbackDelay(ctx); got != 90*time.Minute {
		t.Fatalf("configured fallback delay = %s, want 90m", got)
	}
}

func TestDownloadMethodAndEquivalentPreferredTieBreak(t *testing.T) {
	if got := normalizeDownloadMethod(""); got != downloadTorrentHTTP {
		t.Fatalf("blank method = %q, want torrent_http", got)
	}
	if got := effectiveDownloadMethod(map[string]string{"default_download_method": "http_torrent"}, domain.Release{HTTPDownloadPrimary: true}); got != downloadHTTPTorrent {
		t.Fatalf("configured method = %q, want http_torrent", got)
	}

	torrent := domain.SearchResult{Accepted: true, PreferredFilenameMatch: true, MatchedFile: "folder/HHD800.com@ABC-123.mp4", SizeBytes: 10_000}
	httpResult := domain.SearchResult{Accepted: true, PreferredFilenameMatch: true, MatchedFile: "hhd800.com@abc-123.mp4", SizeBytes: 9_100}
	if !equivalentPreferredMatches(torrent, httpResult) {
		t.Fatal("same preferred filename within 10% should prefer HTTP")
	}
	httpResult.SizeBytes = 8_999
	if equivalentPreferredMatches(torrent, httpResult) {
		t.Fatal("size difference greater than 10% must not trigger HTTP tie-break")
	}
	httpResult.SizeBytes = 9_100
	httpResult.MatchedFile = "different.mp4"
	if equivalentPreferredMatches(torrent, httpResult) {
		t.Fatal("different matched filenames must not trigger HTTP tie-break")
	}
	httpResult.MatchedFile = "hhd800.com@abc-123.mp4"
	httpResult.PreferredFilenameMatch = false
	if equivalentPreferredMatches(torrent, httpResult) {
		t.Fatal("non-preferred HTTP match must not trigger HTTP tie-break")
	}
}

// TestDownloadAcceptsAnyIDMatchedNonBlacklistedTorrent covers the torrent
// accept/reject gate in Service.Download after the preferred-filename-gate
// removal: a torrent is accepted whenever its title contains the release ID
// and is not blacklisted, with no preferred-filename-pattern match required
// at all. A title that does not contain the release ID is still rejected,
// and Forced still bypasses that one rejection (blacklist stays a hard,
// non-bypassable reject).
func TestDownloadAcceptsAnyIDMatchedNonBlacklistedTorrent(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "download-accept-gate.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	site, _ := st.SaveSite(ctx, domain.Site{Title: "Test", Type: "Site", Name: "JavLibrary", Enabled: true})
	_, _ = st.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "PRED-895", Title: "Test", Source: "JavLibrary", Released: true})
	releases, _ := st.Releases(ctx, domain.ReleaseFilter{Search: "PRED-895", Limit: 10})
	if len(releases) != 1 {
		t.Fatalf("release setup failed: %+v", releases)
	}
	service := New(st, time.Second, slog.Default())

	accepted, err := service.Download(ctx, releases[0], domain.SearchResult{Provider: "Sukebei/Nyaa", Title: "untrusted PRED-895 release", Link: "magnet:?xt=a"}, "Manual Search", "test")
	if err != nil && accepted.Status == "" {
		t.Fatal(err)
	}
	if strings.Contains(accepted.Error, "rejected") {
		t.Fatalf("an ID-matched, non-blacklisted torrent must not be rejected regardless of filename pattern: %+v", accepted)
	}

	mismatched, err := service.Download(ctx, releases[0], domain.SearchResult{Provider: "Sukebei/Nyaa", Title: "unrelated title", Link: "magnet:?xt=b"}, "Manual Search", "test")
	if err != nil && mismatched.Status == "" {
		t.Fatal(err)
	}
	if mismatched.Status != "failed" || !strings.Contains(mismatched.Error, "did not contain release ID") {
		t.Fatalf("a torrent whose filename does not contain the release ID must still be rejected: %+v", mismatched)
	}

	forced, err := service.Download(ctx, releases[0], domain.SearchResult{Provider: "Sukebei/Nyaa", Title: "unrelated title again", Link: "magnet:?xt=c", Forced: true}, "Manual Search", "test")
	if err != nil && forced.Status == "" {
		t.Fatal(err)
	}
	if strings.Contains(forced.Error, "did not contain release ID") {
		t.Fatalf("a manually forced download should bypass the ID-match rejection: %+v", forced)
	}
	if !strings.Contains(forced.MatchReason, "manually forced despite automatic match result") {
		t.Fatalf("expected the forced match reason to explain the override, got: %+v", forced)
	}
}

// TestSearchAndDownloadNowFindsAnyAcceptedResultRegardlessOfFilenamePattern
// is an end-to-end exercise proving SearchAndDownloadNow actually wires
// searchNative -> sortSearchResults -> fallbackSearchCandidate -> Download
// together correctly under the unified (no more allowNonPreferred toggle)
// behavior: an ID-matched result is found and downloaded even when it
// matches no preferred filename pattern at all, and a release with no
// ID-matching result at all is correctly reported not found.
func TestSearchAndDownloadNowFindsAnyAcceptedResultRegardlessOfFilenamePattern(t *testing.T) {
	ctx := context.Background()

	newRelease := func(t *testing.T, st store.Store, videoID string) domain.Release {
		t.Helper()
		site, _ := st.SaveSite(ctx, domain.Site{Title: "GIGA", Type: "Site", Name: "GIGA", Enabled: false, Download: false})
		if _, err := st.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: videoID, Title: "T", Source: "GIGA"}); err != nil {
			t.Fatal(err)
		}
		releases, err := st.Releases(ctx, domain.ReleaseFilter{Search: videoID, Limit: 1})
		if err != nil || len(releases) != 1 {
			t.Fatalf("release setup failed for %s: rows=%d err=%v", videoID, len(releases), err)
		}
		return releases[0]
	}

	t.Run("an ID-matched result with no preferred filename pattern match is still found and downloaded", func(t *testing.T) {
		st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "fallback-no-pattern.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer st.Close()
		mux := http.NewServeMux()
		mux.HandleFunc("/feed", func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`<rss xmlns:nyaa="https://nyaa.si/xmlns/nyaa"><channel>` +
				`<item><title>unmatched-pattern NOPATTERN-100 release</title><link>magnet:?xt=np-1</link><nyaa:seeders>7</nyaa:seeders></item>` +
				`</channel></rss>`))
		})
		server := httptest.NewServer(mux)
		defer server.Close()
		if err := st.SaveSettings(ctx, map[string]string{
			"accepted_patterns":   "trusted@",
			"search_url_template": server.URL + "/feed?q=<release_id>",
		}); err != nil {
			t.Fatal(err)
		}
		release := newRelease(t, st, "NOPATTERN-100")
		service := New(st, 2*time.Second, slog.Default())

		found, err := service.SearchAndDownloadNow(ctx, release, "Missing Library Recovery")
		// qBittorrent is unconfigured, so Download itself errors past the
		// accept gate - proving the ID-matched-but-pattern-mismatched result
		// reached Download rather than being skipped as "not found".
		if err == nil {
			t.Fatalf("expected the download to fail at the unconfigured qBittorrent step, got found=%v", found)
		}

		downloads, err := st.Downloads(ctx, "")
		if err != nil {
			t.Fatal(err)
		}
		var queued *domain.Download
		for i := range downloads {
			if downloads[i].SourceType == "Missing Library Recovery" && downloads[i].Status != "searched" && downloads[i].Status != "search_rejected" {
				queued = &downloads[i]
			}
		}
		if queued == nil {
			t.Fatalf("expected a queued/failed download row for the ID-matched result, got %+v", downloads)
		}
		if strings.Contains(queued.Error, "did not contain release ID") || strings.Contains(queued.Error, "blacklist") {
			t.Fatalf("the result should have been accepted, not rejected: %+v", queued)
		}
	})

	t.Run("reports not found when the only result does not contain the release ID", func(t *testing.T) {
		st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "fallback-no-id-match.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer st.Close()
		mux := http.NewServeMux()
		mux.HandleFunc("/feed", func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`<rss xmlns:nyaa="https://nyaa.si/xmlns/nyaa"><channel>` +
				`<item><title>completely unrelated release</title><link>magnet:?xt=nf-1</link><nyaa:seeders>50</nyaa:seeders></item>` +
				`</channel></rss>`))
		})
		server := httptest.NewServer(mux)
		defer server.Close()
		if err := st.SaveSettings(ctx, map[string]string{
			"search_url_template": server.URL + "/feed?q=<release_id>",
		}); err != nil {
			t.Fatal(err)
		}
		release := newRelease(t, st, "NOFALLBACK-100")
		service := New(st, 2*time.Second, slog.Default())

		found, err := service.SearchAndDownloadNow(ctx, release, "Missing Library Recovery")
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
		if found {
			t.Fatalf("expected found=false since the only result does not contain the release ID")
		}
	})
}

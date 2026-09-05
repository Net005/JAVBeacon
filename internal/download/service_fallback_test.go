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

// TestFallbackSearchCandidate exercises TODO-2.0 Task A's three-tier
// fallback chain in isolation from search/HTTP/store plumbing: given a
// display-sorted result list and the provider's native order, it must pick
// the right candidate for each tier and mark FilenamePatternExcluded only
// when a fallback tier (2 or 3), not the plain accepted match, was used.
func TestFallbackSearchCandidate(t *testing.T) {
	accepted := domain.SearchResult{Title: "accepted-no-seeds", Accepted: true, Seeds: 0}
	acceptedSeeded := domain.SearchResult{Title: "accepted-seeded", Accepted: true, Seeds: 4}
	rejectedHighSeed := domain.SearchResult{Title: "rejected-high-seed", Accepted: false, Seeds: 9}
	rejectedNoSeed := domain.SearchResult{Title: "rejected-no-seed", Accepted: false, Seeds: 0}

	t.Run("allowNonPreferred=false uses the best accepted match regardless of seeds", func(t *testing.T) {
		sorted := []domain.SearchResult{accepted, rejectedHighSeed}
		got, found := fallbackSearchCandidate(sorted, sorted, false)
		if !found || got.Title != "accepted-no-seeds" {
			t.Fatalf("expected the accepted match despite 0 seeds, got %+v found=%v", got, found)
		}
		if got.FilenamePatternExcluded {
			t.Fatalf("a normal accepted match must not be marked FilenamePatternExcluded: %+v", got)
		}
	})

	t.Run("allowNonPreferred=false finds nothing when no result is accepted", func(t *testing.T) {
		sorted := []domain.SearchResult{rejectedHighSeed, rejectedNoSeed}
		_, found := fallbackSearchCandidate(sorted, sorted, false)
		if found {
			t.Fatalf("expected no candidate without an accepted match and allowNonPreferred=false")
		}
	})

	t.Run("allowNonPreferred=true tier1: seeded accepted match wins outright", func(t *testing.T) {
		sorted := sortSearchResults([]domain.SearchResult{rejectedHighSeed, acceptedSeeded})
		got, found := fallbackSearchCandidate(sorted, sorted, true)
		if !found || got.Title != "accepted-seeded" {
			t.Fatalf("expected the seeded accepted match, got %+v found=%v", got, found)
		}
		if got.FilenamePatternExcluded {
			t.Fatalf("tier1 (a real accepted+seeded match) must not be marked excluded: %+v", got)
		}
	})

	t.Run("allowNonPreferred=true tier2: falls back to the highest-seeded result when the accepted match has no seeds", func(t *testing.T) {
		native := []domain.SearchResult{accepted, rejectedNoSeed, rejectedHighSeed}
		sorted := sortSearchResults(native)
		got, found := fallbackSearchCandidate(sorted, native, true)
		if !found || got.Title != "rejected-high-seed" {
			t.Fatalf("expected the highest-seeded result across all candidates, got %+v found=%v", got, found)
		}
		if !got.FilenamePatternExcluded {
			t.Fatalf("tier2 pick must be marked FilenamePatternExcluded: %+v", got)
		}
	})

	t.Run("allowNonPreferred=true tier3: falls back to the most recent (native order) result when nothing has seeds", func(t *testing.T) {
		native := []domain.SearchResult{rejectedNoSeed, accepted}
		sorted := sortSearchResults(native)
		got, found := fallbackSearchCandidate(sorted, native, true)
		if !found || got.Title != native[0].Title {
			t.Fatalf("expected native[0] as the last-resort candidate, got %+v found=%v (native[0]=%+v)", got, found, native[0])
		}
		if !got.FilenamePatternExcluded {
			t.Fatalf("tier3 pick must be marked FilenamePatternExcluded: %+v", got)
		}
	})

	t.Run("allowNonPreferred=true finds nothing when there are no results at all", func(t *testing.T) {
		_, found := fallbackSearchCandidate(nil, nil, true)
		if found {
			t.Fatalf("expected no candidate when the search returned nothing")
		}
	})
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

// TestDownloadMarksFilenamePatternExcludedForForcedAndFallbackResults covers
// the unification point in Service.Download: both the pre-existing manual
// "Force download" override (SearchResult.Forced) and the new Missing
// Library Files fallback-chain pick (SearchResult.FilenamePatternExcluded)
// must land on the same structured domain.Download.FilenamePatternExcluded
// column, while a normal accepted match must not set it.
func TestDownloadMarksFilenamePatternExcludedForForcedAndFallbackResults(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "download-excluded-flag.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.SaveSettings(ctx, map[string]string{"accepted_patterns": "trusted@"}); err != nil {
		t.Fatal(err)
	}
	site, _ := st.SaveSite(ctx, domain.Site{Title: "Test", Type: "Site", Name: "JavLibrary", Enabled: true})
	_, _ = st.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "PRED-895", Title: "Test", Source: "JavLibrary", Released: true})
	releases, _ := st.Releases(ctx, domain.ReleaseFilter{Search: "PRED-895", Limit: 10})
	if len(releases) != 1 {
		t.Fatalf("release setup failed: %+v", releases)
	}
	service := New(st, time.Second, slog.Default())

	accepted, err := service.Download(ctx, releases[0], domain.SearchResult{Provider: "Sukebei/Nyaa", Title: "trusted@ PRED-895", Link: "magnet:?xt=a"}, "Manual Search", "test")
	if err != nil && accepted.Status == "" {
		t.Fatal(err)
	}
	if accepted.FilenamePatternExcluded {
		t.Fatalf("a normal accepted match must not set FilenamePatternExcluded: %+v", accepted)
	}

	forced, err := service.Download(ctx, releases[0], domain.SearchResult{Provider: "Sukebei/Nyaa", Title: "PRED-895 untrusted", Link: "magnet:?xt=b", Forced: true}, "Manual Search", "test")
	if err != nil && forced.Status == "" {
		t.Fatal(err)
	}
	if !forced.FilenamePatternExcluded {
		t.Fatalf("a manually forced download must set FilenamePatternExcluded: %+v", forced)
	}

	excluded, err := service.Download(ctx, releases[0], domain.SearchResult{Provider: "Sukebei/Nyaa", Title: "PRED-895 untrusted fallback", Link: "magnet:?xt=c", FilenamePatternExcluded: true}, "Missing Library Recovery", "test")
	if err != nil && excluded.Status == "" {
		t.Fatal(err)
	}
	if !excluded.FilenamePatternExcluded {
		t.Fatalf("a fallback-chain pick must set FilenamePatternExcluded: %+v", excluded)
	}
	if !strings.Contains(excluded.MatchReason, "non-preferred filename allowed by Missing Library Files fallback search") {
		t.Fatalf("expected the fallback-chain match reason to explain the override, got: %+v", excluded)
	}
}

// TestSearchAndDownloadNowAllowNonPreferredFallsBackAcrossTiers is an
// end-to-end exercise of allowNonPreferred=true against a fake RSS feed
// carrying results for all three fallback tiers, proving
// SearchAndDownloadNow actually wires searchNative -> sortSearchResults ->
// fallbackSearchCandidate -> Download together correctly (the unit test
// above only covers fallbackSearchCandidate in isolation).
func TestSearchAndDownloadNowAllowNonPreferredFallsBackAcrossTiers(t *testing.T) {
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

	t.Run("tier2: seeded but unaccepted result is downloaded and marked excluded", func(t *testing.T) {
		st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "fallback-tier2.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer st.Close()
		mux := http.NewServeMux()
		mux.HandleFunc("/feed", func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`<rss xmlns:nyaa="https://nyaa.si/xmlns/nyaa"><channel>` +
				`<item><title>rejected@ TIER2-100 no accepted match</title><link>magnet:?xt=t2-1</link><nyaa:seeders>7</nyaa:seeders></item>` +
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
		release := newRelease(t, st, "TIER2-100")
		service := New(st, 2*time.Second, slog.Default())

		found, err := service.SearchAndDownloadNow(ctx, release, "Missing Library Recovery", true)
		// qBittorrent is unconfigured, so Download itself errors past the
		// filename-rejection step - proving the seeded-but-unaccepted result
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
			t.Fatalf("expected a queued/failed download row from the fallback pick, got %+v", downloads)
		}
		if !queued.FilenamePatternExcluded {
			t.Fatalf("tier2 fallback download must be marked FilenamePatternExcluded: %+v", queued)
		}
	})

	t.Run("tier3: with nothing seeded, falls back to the most recent result", func(t *testing.T) {
		st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "fallback-tier3.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer st.Close()
		mux := http.NewServeMux()
		mux.HandleFunc("/feed", func(w http.ResponseWriter, _ *http.Request) {
			// Nyaa/RSS order is the provider's native (typically newest-first)
			// order; neither item is seeded nor accepted, so tier3 must pick
			// whichever one the provider listed first.
			_, _ = w.Write([]byte(`<rss xmlns:nyaa="https://nyaa.si/xmlns/nyaa"><channel>` +
				`<item><title>rejected@ TIER3-100 most recent</title><link>magnet:?xt=t3-newest</link><nyaa:seeders>0</nyaa:seeders></item>` +
				`<item><title>rejected@ TIER3-100 older</title><link>magnet:?xt=t3-older</link><nyaa:seeders>0</nyaa:seeders></item>` +
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
		release := newRelease(t, st, "TIER3-100")
		service := New(st, 2*time.Second, slog.Default())

		_, err = service.SearchAndDownloadNow(ctx, release, "Missing Library Recovery", true)
		if err == nil {
			t.Fatalf("expected the download to fail at the unconfigured qBittorrent step")
		}

		downloads, err := st.Downloads(ctx, "")
		if err != nil {
			t.Fatal(err)
		}
		var queued *domain.Download
		for i := range downloads {
			d := downloads[i]
			if d.SourceType != "Missing Library Recovery" || d.Status == "searched" || d.Status == "search_rejected" || d.Status == "search_accepted" {
				continue
			}
			queued = &downloads[i]
		}
		if queued == nil {
			t.Fatalf("expected a queued/failed download row from the fallback pick, got %+v", downloads)
		}
		if queued.Name != "rejected@ TIER3-100 most recent" {
			t.Fatalf("expected the most-recent (provider-native-first) result to be downloaded, got %+v", queued)
		}
		if !queued.FilenamePatternExcluded {
			t.Fatalf("tier3 fallback download must be marked FilenamePatternExcluded: %+v", queued)
		}
	})

	t.Run("allowNonPreferred=false still reports not found when nothing is accepted, despite seeded results existing", func(t *testing.T) {
		st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "fallback-disabled.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer st.Close()
		mux := http.NewServeMux()
		mux.HandleFunc("/feed", func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`<rss xmlns:nyaa="https://nyaa.si/xmlns/nyaa"><channel>` +
				`<item><title>rejected@ NOFALLBACK-100</title><link>magnet:?xt=nf-1</link><nyaa:seeders>50</nyaa:seeders></item>` +
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
		release := newRelease(t, st, "NOFALLBACK-100")
		service := New(st, 2*time.Second, slog.Default())

		found, err := service.SearchAndDownloadNow(ctx, release, "Missing Library Recovery", false)
		if err != nil {
			t.Fatalf("expected no error - a legacy allowNonPreferred=false call should simply report not-found, got %v", err)
		}
		if found {
			t.Fatalf("expected found=false since no result is accepted and the fallback chain is disabled")
		}
	})
}

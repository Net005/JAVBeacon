package download

import (
	"context"
	"log/slog"
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
	result, err := service.Download(ctx, releases[0], domain.SearchResult{Provider: "Sukebei/Nyaa", Title: "PRED-888 untrusted filename", Link: "magnet:?xt=fake", Accepted: true}, "Manual Search", "test")
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "failed" || result.Error != "result rejected by filename rules" {
		t.Fatalf("client-provided acceptance was trusted: %+v", result)
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

func TestSiteDesiredRuleDoesNotRetroactivelyChangeExistingRelease(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "site-desired.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	site, err := st.SaveSite(ctx, domain.Site{Title: "Future Desired", Type: "Site", Name: "JavLibrary", Desired: true, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "TEST-100", Title: "Existing", Source: "JavLibrary", Desired: false})
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
	if unchanged.Desired {
		t.Fatal("site-level Desired rule changed an existing release")
	}
}

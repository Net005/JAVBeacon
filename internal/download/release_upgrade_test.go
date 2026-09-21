package download

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Net005/JAVBeacon/internal/domain"
	"github.com/Net005/JAVBeacon/internal/store"
)

// TestDueReleaseUpgradeRun covers the once-per-calendar-day-at-or-after-HH:MM
// firing rule, including the unset/unparseable-time fallback to
// defaultReleaseUpgradeTime.
func TestDueReleaseUpgradeRun(t *testing.T) {
	loc := time.UTC
	day := time.Date(2026, 9, 10, 0, 0, 0, 0, loc)

	cases := []struct {
		name        string
		now         time.Time
		timeOfDay   string
		lastRunDate string
		want        bool
	}{
		{"before scheduled time today", day.Add(2 * time.Hour), "03:00", "", false},
		{"at scheduled time today, never run", day.Add(3 * time.Hour), "03:00", "", true},
		{"after scheduled time today, never run", day.Add(4 * time.Hour), "03:00", "", true},
		{"already ran today", day.Add(4 * time.Hour), "03:00", "2026-09-10", false},
		{"ran yesterday, now due again today", day.Add(4 * time.Hour), "03:00", "2026-09-09", true},
		{"empty time falls back to default and fires", day.Add(4 * time.Hour), "", "", true},
		{"unparseable time falls back to default and fires", day.Add(4 * time.Hour), "not-a-time", "", true},
		{"before default fallback time", day.Add(2 * time.Hour), "", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := dueReleaseUpgradeRun(c.now, c.timeOfDay, c.lastRunDate); got != c.want {
				t.Fatalf("dueReleaseUpgradeRun(%v, %q, %q) = %v, want %v", c.now, c.timeOfDay, c.lastRunDate, got, c.want)
			}
		})
	}
}

// TestHTTPFilenameHasTopPriorityMatch covers that only a match against the
// single highest-priority (lowest Priority number) configured pattern
// counts - a lower-priority accepted match is not "already the best."
func TestHTTPFilenameHasTopPriorityMatch(t *testing.T) {
	patterns := []PreferredFilenamePattern{{Pattern: "hhd800.com@", Priority: 1}, {Pattern: "4k688.com@", Priority: 10}}

	if !httpFilenameHasTopPriorityMatch("hhd800.com@ABC-123.mp4", patterns) {
		t.Fatalf("expected a top-priority pattern match to report true")
	}
	if httpFilenameHasTopPriorityMatch("4k688.com@ABC-123.mp4", patterns) {
		t.Fatalf("expected a lower-priority match to report false")
	}
	if httpFilenameHasTopPriorityMatch("unrelated-file.mp4", patterns) {
		t.Fatalf("expected a non-matching filename to report false")
	}
	if httpFilenameHasTopPriorityMatch("hhd800.com@ABC-123.mp4", nil) {
		t.Fatalf("expected no configured patterns to report false")
	}
}

// TestBestTopPriorityHTTPCandidate covers the accepted/blacklisted/
// non-top-priority filtering and the larger-file tie-break.
func TestBestTopPriorityHTTPCandidate(t *testing.T) {
	patterns := []PreferredFilenamePattern{{Pattern: "hhd800.com@", Priority: 1}, {Pattern: "4k688.com@", Priority: 10}}

	t.Run("no configured patterns yields nothing", func(t *testing.T) {
		_, found := bestTopPriorityHTTPCandidate([]domain.SearchResult{{Accepted: true, PreferredFilenameMatch: true, PreferredFilenamePriority: 1}}, nil)
		if found {
			t.Fatalf("expected no candidate without configured patterns")
		}
	})

	t.Run("filters out non-accepted, blacklisted, and non-top-priority results", func(t *testing.T) {
		notAccepted := domain.SearchResult{Title: "not-accepted", Accepted: false, PreferredFilenameMatch: true, PreferredFilenamePriority: 1}
		blacklisted := domain.SearchResult{Title: "blacklisted", Accepted: true, PreferredFilenameMatch: true, PreferredFilenamePriority: 1, BlacklistedFilenameMatch: true}
		lowerPriority := domain.SearchResult{Title: "lower-priority", Accepted: true, PreferredFilenameMatch: true, PreferredFilenamePriority: 10}
		noMatch := domain.SearchResult{Title: "no-match", Accepted: true, PreferredFilenameMatch: false}
		want := domain.SearchResult{Title: "top-priority", Accepted: true, PreferredFilenameMatch: true, PreferredFilenamePriority: 1, SizeBytes: 100}

		got, found := bestTopPriorityHTTPCandidate([]domain.SearchResult{notAccepted, blacklisted, lowerPriority, noMatch, want}, patterns)
		if !found || got.Title != want.Title {
			t.Fatalf("expected only the top-priority accepted result, got %+v found=%v", got, found)
		}
	})

	t.Run("ties on priority break toward the larger file", func(t *testing.T) {
		smaller := domain.SearchResult{Title: "smaller", Accepted: true, PreferredFilenameMatch: true, PreferredFilenamePriority: 1, SizeBytes: 100}
		larger := domain.SearchResult{Title: "larger", Accepted: true, PreferredFilenameMatch: true, PreferredFilenamePriority: 1, SizeBytes: 200}

		got, found := bestTopPriorityHTTPCandidate([]domain.SearchResult{smaller, larger}, patterns)
		if !found || got.Title != larger.Title {
			t.Fatalf("expected the larger tied-priority result to win, got %+v found=%v", got, found)
		}
	})

	t.Run("finds nothing when nothing matches the top pattern", func(t *testing.T) {
		lowerOnly := domain.SearchResult{Title: "lower-only", Accepted: true, PreferredFilenameMatch: true, PreferredFilenamePriority: 10}
		_, found := bestTopPriorityHTTPCandidate([]domain.SearchResult{lowerOnly}, patterns)
		if found {
			t.Fatalf("expected no candidate when nothing matches the #1 pattern")
		}
	})
}

// TestLatestCompletedHTTPDownload covers that only completed HTTP-transport
// rows for the given release are considered, and that the most recently
// updated one wins.
func TestLatestCompletedHTTPDownload(t *testing.T) {
	now := time.Now().UTC()
	older := domain.Download{ReleaseID: 1, Transport: "http", Status: "completed", Name: "older.mp4", UpdatedAt: now.Add(-time.Hour)}
	newer := domain.Download{ReleaseID: 1, Transport: "http", Status: "completed", Name: "newer.mp4", UpdatedAt: now}
	otherRelease := domain.Download{ReleaseID: 2, Transport: "http", Status: "completed", Name: "other-release.mp4", UpdatedAt: now.Add(time.Hour)}
	torrent := domain.Download{ReleaseID: 1, Transport: "torrent", Status: "completed", Name: "torrent.mp4", UpdatedAt: now.Add(time.Hour)}
	incomplete := domain.Download{ReleaseID: 1, Transport: "http", Status: "downloading", Name: "incomplete.mp4", UpdatedAt: now.Add(time.Hour)}

	got, found := latestCompletedHTTPDownload([]domain.Download{older, newer, otherRelease, torrent, incomplete}, 1)
	if !found || got.Name != "newer.mp4" {
		t.Fatalf("expected the most recently updated completed HTTP download for release 1, got %+v found=%v", got, found)
	}

	_, found = latestCompletedHTTPDownload([]domain.Download{otherRelease, torrent, incomplete}, 1)
	if found {
		t.Fatalf("expected no match when release 1 has no completed HTTP download")
	}
}

// TestDeleteReleaseUpgradeFiles covers that the old video and every stale
// subtitle sibling present on disk are removed, missing ones are silently
// tolerated, and unrelated files are left alone.
func TestDeleteReleaseUpgradeFiles(t *testing.T) {
	dir := t.TempDir()
	videoPath := filepath.Join(dir, "ABC-123.mp4")

	present := []string{
		videoPath,
		filepath.Join(dir, "ABC-123.en.srt"),
		filepath.Join(dir, "ABC-123.ja.srt"),
		filepath.Join(dir, "ABC-123.subtitles.json"),
	}
	for _, p := range present {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	unrelated := filepath.Join(dir, "unrelated.txt")
	if err := os.WriteFile(unrelated, []byte("keep me"), 0o644); err != nil {
		t.Fatal(err)
	}
	// ABC-123.en.srt.json and ABC-123.ja.srt.json are intentionally absent
	// to exercise the "not found" tolerance path.

	svc := &Service{log: slog.Default()}
	svc.deleteReleaseUpgradeFiles(videoPath, domain.Release{ID: 1, VideoID: "ABC-123"})

	for _, p := range present {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("expected %s to be removed, stat err=%v", p, err)
		}
	}
	if _, err := os.Stat(unrelated); err != nil {
		t.Fatalf("expected unrelated file to remain untouched: %v", err)
	}
}

// TestEligibleReleaseUpgrades covers the four gates eligibleReleaseUpgrades
// applies: the release-date window, requiring a completed HTTP download
// history, and skipping releases whose current file already matches the #1
// preferred-filename pattern.
func TestEligibleReleaseUpgrades(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "release-upgrade-eligibility.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	if err := st.SaveSettings(ctx, map[string]string{"accepted_patterns": `[{"pattern":"hhd800.com@","priority":1},{"pattern":"4k688.com@","priority":10}]`}); err != nil {
		t.Fatal(err)
	}
	site, _ := st.SaveSite(ctx, domain.Site{Title: "Test", Type: "Site", Name: "JavLibrary", Enabled: true})

	today := time.Now().UTC()
	mustRelease := func(videoID, releaseDate string) domain.Release {
		if _, err := st.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: videoID, Title: "Test", Source: "JavLibrary", Released: true, ReleaseDate: releaseDate}); err != nil {
			t.Fatal(err)
		}
		rows, err := st.Releases(ctx, domain.ReleaseFilter{Search: videoID, Limit: 1})
		if err != nil || len(rows) != 1 {
			t.Fatalf("release setup for %s failed: rows=%d err=%v", videoID, len(rows), err)
		}
		return rows[0]
	}

	// Eligible: in-window release with a completed HTTP download that does
	// not match the #1 pattern.
	eligible := mustRelease("ELG-001", today.Format("2006-01-02"))
	if _, err := st.SaveDownload(ctx, domain.Download{ReleaseID: eligible.ID, Query: eligible.VideoID, Transport: "http", Status: "completed", Name: "4k688.com@ELG-001.mp4"}); err != nil {
		t.Fatal(err)
	}

	// Excluded: outside the +/-30 day window.
	outOfWindow := mustRelease("OOW-001", today.AddDate(0, 0, -60).Format("2006-01-02"))
	if _, err := st.SaveDownload(ctx, domain.Download{ReleaseID: outOfWindow.ID, Query: outOfWindow.VideoID, Transport: "http", Status: "completed", Name: "4k688.com@OOW-001.mp4"}); err != nil {
		t.Fatal(err)
	}

	// Excluded: no download history at all.
	_ = mustRelease("NDH-001", today.Format("2006-01-02"))

	// Excluded: current download already matches the #1 (top-priority)
	// pattern - nothing left to upgrade.
	alreadyBest := mustRelease("BST-001", today.Format("2006-01-02"))
	if _, err := st.SaveDownload(ctx, domain.Download{ReleaseID: alreadyBest.ID, Query: alreadyBest.VideoID, Transport: "http", Status: "completed", Name: "hhd800.com@BST-001.mp4"}); err != nil {
		t.Fatal(err)
	}

	service := New(st, time.Second, slog.Default())
	candidates, patterns, err := service.eligibleReleaseUpgrades(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(patterns) != 2 {
		t.Fatalf("expected 2 configured patterns, got %d", len(patterns))
	}
	if len(candidates) != 1 || candidates[0].release.VideoID != "ELG-001" {
		t.Fatalf("expected exactly ELG-001 to be eligible, got %+v", candidates)
	}
}

// TestEligibleReleaseUpgradesNoPatternsConfigured covers that the schedule
// finds nothing to do (rather than erroring) when no preferred-filename
// patterns are configured at all - there is no "#1 pattern" to upgrade
// toward.
func TestEligibleReleaseUpgradesNoPatternsConfigured(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "release-upgrade-no-patterns.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	service := New(st, time.Second, slog.Default())
	candidates, patterns, err := service.eligibleReleaseUpgrades(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(patterns) != 0 || candidates != nil {
		t.Fatalf("expected no patterns and no candidates, got patterns=%+v candidates=%+v", patterns, candidates)
	}
}

func TestEligibleReleaseUpgradesLoadsBeyondFirstStorePage(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "release-upgrade-pages.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.SaveSettings(ctx, map[string]string{"accepted_patterns": `[{"pattern":"hhd800.com@","priority":1},{"pattern":"4k688.com@","priority":10}]`}); err != nil {
		t.Fatal(err)
	}
	site, _ := st.SaveSite(ctx, domain.Site{Title: "Test", Type: "Site", Name: "JavLibrary", Enabled: true})
	today := time.Now().UTC().Format("2006-01-02")
	for i := 1; i <= 501; i++ {
		videoID := fmt.Sprintf("UPG-%04d", i)
		if _, err := st.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: videoID, Title: "Upgrade", Source: "JavLibrary", Released: true, ReleaseDate: today}); err != nil {
			t.Fatal(err)
		}
		rows, err := st.Releases(ctx, domain.ReleaseFilter{VideoID: videoID, Limit: 1})
		if err != nil || len(rows) != 1 {
			t.Fatalf("release setup for %s failed: rows=%d err=%v", videoID, len(rows), err)
		}
		if _, err := st.SaveDownload(ctx, domain.Download{ReleaseID: rows[0].ID, Query: videoID, Transport: "http", Status: "completed", Name: "4k688.com@" + videoID + ".mp4"}); err != nil {
			t.Fatal(err)
		}
	}

	service := New(st, time.Second, slog.Default())
	candidates, _, err := service.eligibleReleaseUpgrades(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 501 {
		t.Fatalf("eligible candidates = %d, want all 501", len(candidates))
	}
}

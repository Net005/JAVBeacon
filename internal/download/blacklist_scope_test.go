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

// Bundled ads and junk files must not decide whether a release is rejected;
// only the release's own video file (or the torrent name) is checked.
func TestBlacklistPatternOnlyChecksTheReleaseVideo(t *testing.T) {
	p := &Nyaa{BlacklistedPatterns: []string{"-C.mp4"}}
	cases := []struct {
		name  string
		title string
		files []string
		want  bool
	}{
		{"ad file with pattern is ignored", "[FHD] SYNTH-101 title", []string{"SYNTH-101.mp4", "casino-ad-C.mp4", "forum.url"}, false},
		{"release video with pattern is rejected", "[FHD] SYNTH-101 title", []string{"SYNTH-101-C.mp4", "casino-ad.mp4"}, true},
		{"non-video files never match", "[FHD] SYNTH-101 title", []string{"SYNTH-101.mp4", "SYNTH-101-C.mp4.html"}, false},
		{"without a release video the torrent name decides", "SYNTH-101-C.mp4", []string{"random.mp4"}, true},
		{"without files the torrent name decides", "SYNTH-101 clean", nil, false},
	}
	for _, tc := range cases {
		got, _, _ := p.blacklistMatch("SYNTH-101", tc.title, tc.files)
		if got != tc.want {
			t.Errorf("%s: blacklisted=%v want %v", tc.name, got, tc.want)
		}
	}
}

// A filename-pattern match is a per-search preference, not a permanent
// exclusion: changing the patterns must make the source eligible again.
func TestFilenamePatternRejectionIsNotPersistedAsBlacklist(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	svc := New(st, time.Second, slog.Default())
	link := "https://torrents.example.invalid/view/101"
	result := domain.SearchResult{Link: link, Title: "SYNTH-101", Transport: "torrent", BlacklistedFilenameMatch: true, Reason: "torrent filename matched a blacklisted pattern", Size: "6.0 GiB"}
	reason, err := svc.candidateBlockReason(ctx, domain.Release{ID: 1, VideoID: "SYNTH-101"}, result)
	if err != nil || !strings.Contains(reason, "blacklisted pattern") {
		t.Fatalf("pattern match should still reject this result: reason=%q err=%v", reason, err)
	}
	if _, blocked, err := st.BlacklistedRelease(ctx, link); err != nil || blocked {
		t.Fatalf("pattern rejection was persisted as a permanent blacklist entry: blocked=%v err=%v", blocked, err)
	}
	result.BlacklistedFilenameMatch, result.Reason = false, ""
	if reason, err := svc.candidateBlockReason(ctx, domain.Release{ID: 1, VideoID: "SYNTH-101"}, result); err != nil || reason != "" {
		t.Fatalf("source stayed blocked after the pattern stopped matching: reason=%q err=%v", reason, err)
	}
}

// Real safeguard failures (too small / too short) remain permanent.
func TestSizeSafeguardStillPersistsBlacklist(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	svc := New(st, time.Second, slog.Default())
	link := "https://torrents.example.invalid/view/102"
	reason, err := svc.candidateBlockReason(ctx, domain.Release{ID: 1, VideoID: "SYNTH-102"}, domain.SearchResult{Link: link, Title: "SYNTH-102", Transport: "torrent", Size: "20 MiB"})
	if err != nil || !strings.Contains(reason, "below the minimum") {
		t.Fatalf("reason=%q err=%v", reason, err)
	}
	if _, blocked, err := st.BlacklistedRelease(ctx, link); err != nil || !blocked {
		t.Fatalf("size safeguard should persist: blocked=%v err=%v", blocked, err)
	}
}

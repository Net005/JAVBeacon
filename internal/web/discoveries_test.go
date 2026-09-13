package web

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Net005/JAVBeacon/internal/domain"
)

func TestDiscoveryCategory(t *testing.T) {
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		r    domain.Release
		want string
	}{
		{"remote", domain.Release{}, "new"},
		{"local unwatched", domain.Release{Local: true}, "unwatched"},
		{"rewatch", domain.Release{Local: true, PlayCount: 2, LastPlayedAt: now.Add(-100 * 24 * time.Hour).Format(time.RFC3339)}, "rewatch"},
		{"recently watched", domain.Release{Local: true, PlayCount: 1, LastPlayedAt: now.Add(-2 * 24 * time.Hour).Format(time.RFC3339)}, "watched"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := discoveryCategory(tt.r, 90, now); got != tt.want {
				t.Fatalf("category = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestBuildAffinityWeightsOrgasmHistoryMoreStrongly(t *testing.T) {
	now := time.Now().UTC()
	profile := buildAffinity([]domain.Release{
		{PlayCount: 3, Actresses: []string{"Play preference"}, LastPlayedAt: now.Format(time.RFC3339)},
		{OCounter: 3, Actresses: []string{"Orgasm preference"}, LastPlayedAt: now.Format(time.RFC3339)},
	}, map[string]string{"discoveries_play_weight": "1", "discoveries_orgasm_weight": "3"}, now)
	if profile.actress["orgasm preference"] <= profile.actress["play preference"] {
		t.Fatalf("orgasm preference %v should outweigh play preference %v", profile.actress["orgasm preference"], profile.actress["play preference"])
	}
}

func TestSubtitleSidecarAndCleanExcerpt(t *testing.T) {
	dir := t.TempDir()
	video := filepath.Join(dir, "ABC-123.mp4")
	subtitle := filepath.Join(dir, "ABC-123.en.srt")
	if err := os.WriteFile(subtitle, []byte("1\n00:00:01,000 --> 00:00:03,000\n<i>Hello there</i>\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	release := domain.Release{StashFilePath: video}
	if !hasSubtitleFile(release) {
		t.Fatal("expected subtitle sidecar to be detected")
	}
	if got := cleanedSubtitleExcerpt(release, 100); got != "Hello there\n" {
		t.Fatalf("cleaned excerpt = %q", got)
	}
}

func TestDiscoveryPools(t *testing.T) {
	pools := discoveryPools("Sci-fi | space, android\nInvestigators | detective, mystery\ninvalid")
	if len(pools["Sci-fi"]) != 2 || pools["Investigators"][0] != "detective" {
		t.Fatalf("unexpected pools: %#v", pools)
	}
}

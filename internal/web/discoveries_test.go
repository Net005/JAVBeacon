package web

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Net005/JAVBeacon/internal/domain"
)

type discoveryArchiveStub struct{ archive domain.StashHistoryExport }

func (s discoveryArchiveStub) StashHistoryExport(context.Context) (domain.StashHistoryExport, error) {
	return s.archive, nil
}

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
	pools := discoveryPools("Sci-fi | space, android\nInvestigators | detective, mystery\nBrainwashing")
	if len(pools["Sci-fi"]) != 2 || pools["Investigators"][0] != "detective" {
		t.Fatalf("unexpected pools: %#v", pools)
	}
	if len(pools["Brainwashing"]) != 1 || pools["Brainwashing"][0] != "Brainwashing" {
		t.Fatalf("bare pool was not accepted: %#v", pools)
	}
}

func TestDiscoveryExcludedTagsAreCaseInsensitive(t *testing.T) {
	excluded := discoveryExcludedTags("Drug, Brainwashing\nVR")
	if !discoveryHasExcludedTag(domain.Release{Genres: []string{"BRAINWASHING"}}, excluded) {
		t.Fatal("expected case-insensitive excluded tag match")
	}
	if discoveryHasExcludedTag(domain.Release{Genres: []string{"Drama"}}, excluded) {
		t.Fatal("unrelated tag was excluded")
	}
}

func TestTextAffinityReportsTitleAndStoryMatches(t *testing.T) {
	weights := map[string]float64{"brainwashing": 8, "investigator": 5}
	score, phrase, field := textAffinity(domain.Release{Title: "A Brainwashing Experiment", Story: "A female investigator follows the case."}, weights)
	if score != 8 || phrase != "brainwashing" || field != "Title" {
		t.Fatalf("title affinity = (%v, %q, %q)", score, phrase, field)
	}
	score, phrase, field = textAffinity(domain.Release{Story: "A female investigator follows the case."}, weights)
	if score != 5 || phrase != "investigator" || field != "Story" {
		t.Fatalf("story affinity = (%v, %q, %q)", score, phrase, field)
	}
}

func TestArchivedAffinityUsesDurablePlaybackCountsAndEventRecency(t *testing.T) {
	played := time.Date(2026, 9, 12, 20, 0, 0, 0, time.UTC)
	got, err := archivedAffinityReleases(context.Background(), discoveryArchiveStub{domain.StashHistoryExport{
		Scenes: []domain.StashHistoryScene{{StashSceneID: "scene-1", ReleaseID: 9, PlayCount: 4, OrgasmCount: 7}},
		Events: []domain.StashHistoryEvent{{StashSceneID: "scene-1", Type: "play", OccurredAt: played}},
	}}, []domain.Release{{ID: 9, PlayCount: 1, OCounter: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if got[0].PlayCount != 4 || got[0].OCounter != 7 || got[0].LastPlayedAt != played.Format(time.RFC3339) {
		t.Fatalf("archive was not authoritative: %#v", got[0])
	}
}

func TestDiscoverySubtitlePathRemap(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "SAME-057.ja.srt"), []byte("subtitle"), 0o600); err != nil {
		t.Fatal(err)
	}
	releases := discoveryRemapReleases([]domain.Release{{ID: 57, StashFilePath: "/collections/jav/SAME-057.mp4"}}, `[{"from":"/collections/jav","to":"`+dir+`"}]`)
	if !subtitleAvailability(releases)[57] {
		t.Fatal("subtitle was not found after Stash path remap")
	}
}

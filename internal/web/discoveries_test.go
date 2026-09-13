package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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
	pools := discoveryPools("Sci-fi | space, android\nInvestigators | detective, mystery\nBrainwashing\nDuplicate | drug, Drug")
	if len(pools["Sci-fi"]) != 2 || pools["Investigators"][0] != "detective" {
		t.Fatalf("unexpected pools: %#v", pools)
	}
	if len(pools["Brainwashing"]) != 1 || pools["Brainwashing"][0] != "Brainwashing" {
		t.Fatalf("bare pool was not accepted: %#v", pools)
	}
	if len(pools["Duplicate"]) != 1 {
		t.Fatalf("duplicate pool keywords were not normalized: %#v", pools)
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

func TestDiscoveryAIBatchesBoundInputAndAdaptSubtitleExcerpt(t *testing.T) {
	dir := t.TempDir()
	items := make([]discoveryItem, 12)
	for i := range items {
		path := filepath.Join(dir, "SCENE-"+strconv.Itoa(i)+".mp4")
		if err := os.WriteFile(strings.TrimSuffix(path, ".mp4")+".en.srt", []byte(strings.Repeat("dialogue line\n", 5000)), 0o600); err != nil {
			t.Fatal(err)
		}
		items[i] = discoveryItem{Release: domain.Release{ID: int64(i + 1), VideoID: "SCENE", Title: "Title", Story: strings.Repeat("story ", 300), StashFilePath: path}, HasSubtitle: true}
	}
	settings := map[string]string{"discoveries_subtitle_analysis_enabled": "true", "discoveries_subtitle_max_chars": "16000"}
	batches, payloads := discoveryAIBatches(items, settings, len(items), 5, 50000)
	if len(batches) != 3 || len(batches[0]) != 5 || len(batches[2]) != 2 {
		t.Fatalf("unexpected batches: %#v", []int{len(batches), len(batches[0]), len(batches[2])})
	}
	for i, payload := range payloads {
		if len(payload)+12000 > 50000 {
			t.Fatalf("batch %d exceeds character budget: %d", i+1, len(payload))
		}
	}
	if len(batches[0][0].Subtitle) == 0 || len(batches[0][0].Subtitle) >= 16000 {
		t.Fatalf("subtitle excerpt was not adaptively reduced: %d", len(batches[0][0].Subtitle))
	}
}

func TestOpenAIRankingRequestReportsProviderErrorAndDoesNotRetry400(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("x-request-id", "req_test_123")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": "input is too large", "code": "context_length_exceeded"}})
	}))
	defer server.Close()
	_, err := openAIRankingRequest(map[string]string{"discoveries_openai_api_key": "test", "discoveries_openai_base_url": server.URL, "discoveries_openai_retry_attempts": "3"}, "test", map[string]any{"type": "object"}, 1)
	if err == nil || !strings.Contains(err.Error(), "context_length_exceeded") || !strings.Contains(err.Error(), "req_test_123") {
		t.Fatalf("provider error details missing: %v", err)
	}
	if calls != 1 {
		t.Fatalf("HTTP 400 was retried %d times", calls)
	}
}

func TestDiscoveryAITextFilteringIsPartialAndCaseInsensitive(t *testing.T) {
	for _, tt := range []struct {
		text, entries string
		want          bool
	}{
		{"Strong match for psychological control themes", `["CONTROL"]`, true},
		{"Investigator story with an undercover reporter", "cover rep", true},
		{"Sci-fi heroine", `["brainwashing","drugs"]`, false},
	} {
		if got := discoveryAITextMatches(tt.text, tt.entries); got != tt.want {
			t.Fatalf("discoveryAITextMatches(%q, %q) = %v, want %v", tt.text, tt.entries, got, tt.want)
		}
	}
}

func TestApplyOpenAIRanksRetainsGeneratedTextForFiltering(t *testing.T) {
	items := applyOpenAIRanks([]discoveryItem{{Release: domain.Release{ID: 42}}}, []openAIRank{{ID: 42, Score: 91, Reason: "Matches the title and story"}})
	if len(items) != 1 || !items[0].AIEnhanced || items[0].AIText != "Matches the title and story" {
		t.Fatalf("AI enrichment was not retained: %#v", items)
	}
}

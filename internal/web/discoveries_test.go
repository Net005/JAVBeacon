package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	aidiscovery "github.com/Net005/JAVBeacon/internal/discovery"
	"github.com/Net005/JAVBeacon/internal/domain"
	"github.com/Net005/JAVBeacon/internal/store"
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

func TestSubtitleExcerptRemovesNoiseDuplicatesAndPreservesUTF8(t *testing.T) {
	dir := t.TempDir()
	video := filepath.Join(dir, "UTF-001.mp4")
	subtitle := filepath.Join(dir, "UTF-001.en.srt")
	content := "1\n00:00:01,000 --> 00:00:03,000\nTranslated by Example\nhttps://example.test\nあいうえお\nあいうえお\nMeaningful dialogue line\n"
	if err := os.WriteFile(subtitle, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	got := cleanedSubtitleExcerpt(domain.Release{StashFilePath: video}, 12)
	if !utf8.ValidString(got) || strings.Contains(got, "Translated") || strings.Contains(got, "http") || strings.Count(got, "あいうえお") != 1 {
		t.Fatalf("subtitle cleanup failed: %q", got)
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

func TestDiscoveryAIRequestLimitsKeepSmallModelWorkBounded(t *testing.T) {
	batchSize, inputChars := discoveryAIRequestLimits(map[string]string{
		"discoveries_openai_batch_size":      "50",
		"discoveries_openai_max_input_chars": "500000",
	})
	if batchSize != 5 || inputChars != 60000 {
		t.Fatalf("oversized saved settings were not bounded: batch=%d input=%d", batchSize, inputChars)
	}
	batchSize, inputChars = discoveryAIRequestLimits(nil)
	if batchSize != 5 || inputChars != 50000 {
		t.Fatalf("unexpected defaults: batch=%d input=%d", batchSize, inputChars)
	}
}

func TestDiscoveryOpenAICostEstimate(t *testing.T) {
	if got := discoveryOpenAICostUSD("gpt-5-mini", 1_000_000, 1_000_000); got != 2.25 {
		t.Fatalf("unexpected GPT-5 Mini cost estimate: %f", got)
	}
	if got := discoveryOpenAICostUSD("custom-model", 1_000_000, 1_000_000); got != 0 {
		t.Fatalf("unknown model should not receive a guessed price: %f", got)
	}
}

func TestEstimateDiscoveryOpenAIUsesConfiguredCaps(t *testing.T) {
	estimate := estimateDiscoveryOpenAI("gpt-5-mini", 1000, 5, 50000, true)
	if estimate.Candidates != 1000 || estimate.Batches != 200 {
		t.Fatalf("unexpected estimate scope: %+v", estimate)
	}
	if estimate.EstimatedCostUSD <= 0 || estimate.MaximumCostUSD < estimate.EstimatedCostUSD {
		t.Fatalf("invalid cost range: %+v", estimate)
	}
}

func TestEstimateDiscoveryOpenAIEndpointIsLocalDryRun(t *testing.T) {
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "openai-estimate.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	site, err := st.SaveSite(context.Background(), domain.Site{Title: "Estimate", Type: "Site", Name: "Estimate", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, videoID := range []string{"EST-1", "EST-2", "EST-3"} {
		if _, err := st.UpsertRelease(context.Background(), domain.Release{SiteID: site.ID, VideoID: videoID, Title: videoID}); err != nil {
			t.Fatal(err)
		}
	}
	s := &Server{store: st}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/discoveries/openai/estimate", strings.NewReader(`{"model":"gpt-5-mini","candidate_limit":1000,"batch_size":2,"max_input_chars":20000}`))
	s.estimateDiscoveryOpenAI(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		DryRun       bool                    `json:"dry_run"`
		OpenAICalled bool                    `json:"openai_called"`
		Run          discoveryOpenAIEstimate `json:"run"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if !response.DryRun || response.OpenAICalled || response.Run.Candidates != 3 || response.Run.Batches != 2 {
		t.Fatalf("unexpected dry-run response: %+v", response)
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

type recordingAIRankSaver struct{ saved []domain.DiscoveryAIRank }

func (s *recordingAIRankSaver) SaveDiscoveryAIRanks(_ context.Context, ranks []domain.DiscoveryAIRank) error {
	s.saved = append(s.saved, ranks...)
	return nil
}

func TestValidatedAIRankingPersistenceGuard(t *testing.T) {
	saver := &recordingAIRankSaver{}
	valid := domain.DiscoveryAIRank{ReleaseID: 7, Fingerprint: "v2", Model: "ollama:qwen3:8b", Score: 84, Reason: "Strong story and preferred studio match."}
	if err := saveValidatedDiscoveryAIRanks(context.Background(), saver, []domain.DiscoveryAIRank{valid}, ""); err != nil || len(saver.saved) != 1 {
		t.Fatalf("valid result was not persisted: saved=%d err=%v", len(saver.saved), err)
	}
	invalid := valid
	invalid.Reason = "Please provide more context so I can assist you."
	if err := saveValidatedDiscoveryAIRanks(context.Background(), saver, []domain.DiscoveryAIRank{invalid}, ""); err == nil {
		t.Fatal("invalid result was persisted")
	}
	if len(saver.saved) != 1 {
		t.Fatalf("invalid persistence changed saved rows: %d", len(saver.saved))
	}
}

func TestInvalidQwenResponseLeavesDeterministicRecommendationUntouched(t *testing.T) {
	items := []discoveryItem{{Release: domain.Release{ID: 7}, Score: 64, Reasons: []string{"Preferred studio history"}}}
	result := applyOpenAIRanks(items, nil)
	if len(result) != 1 || result[0].AIEnhanced || result[0].Score != 64 || result[0].Reasons[0] != "Preferred studio history" {
		t.Fatalf("deterministic recommendation changed: %#v", result)
	}
}

func TestDiscoveryFingerprintIncludesPromptSchemaVersion(t *testing.T) {
	fingerprint := discoveryProviderFingerprint(map[string]string{"discoveries_ollama_url": "http://ollama:11434"}, "qwen3:8b")
	if !strings.Contains(fingerprint, "ai-discovery-schema:"+aidiscovery.SchemaVersion) {
		t.Fatalf("schema version missing from provider fingerprint: %q", fingerprint)
	}
}

func TestExistingInvalidAIRankingRepairAgainstSQLite(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "ai-repair.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	site, err := st.SaveSite(ctx, domain.Site{Title: "Repair", Name: "Repair", Type: "Site", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, release := range []domain.Release{{SiteID: site.ID, VideoID: "GOOD-001", Title: "Good"}, {SiteID: site.ID, VideoID: "BAD-001", Title: "Bad"}} {
		if _, err := st.UpsertRelease(ctx, release); err != nil {
			t.Fatal(err)
		}
	}
	releases, err := st.Releases(ctx, domain.ReleaseFilter{Limit: 10, ShowNonPreferred: true})
	if err != nil || len(releases) != 2 {
		t.Fatalf("load releases: %v count=%d", err, len(releases))
	}
	var goodID, badID int64
	for _, release := range releases {
		if release.VideoID == "GOOD-001" {
			goodID = release.ID
		} else {
			badID = release.ID
		}
	}
	badReason := "The content you provided appears to be a mix of unrelated text. There is no clear narrative. Please provide more context."
	if err := st.SaveDiscoveryAIRanks(ctx, []domain.DiscoveryAIRank{{ReleaseID: goodID, Score: 90, Reason: "Strong preferred studio and story match.", Fingerprint: "good"}, {ReleaseID: badID, Score: 50, Reason: badReason, Fingerprint: "bad"}}); err != nil {
		t.Fatal(err)
	}
	removed, err := aidiscovery.RepairStoredRanks(ctx, st, "", nil)
	if err != nil || removed != 1 {
		t.Fatalf("repair removed=%d err=%v", removed, err)
	}
	remaining, err := st.AllDiscoveryAIRanks(ctx)
	if err != nil || len(remaining) != 1 || remaining[0].ReleaseID != goodID {
		t.Fatalf("valid/invalid repair result: %#v err=%v", remaining, err)
	}
}

func TestOllamaTestEndpointReportsAvailableAndMissingModels(t *testing.T) {
	ollama := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"models": []map[string]string{{"name": "qwen3:8b"}}})
	}))
	defer ollama.Close()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "ollama-test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	s := &Server{store: st, discoveryAI: aidiscovery.New(nil)}
	for _, tt := range []struct {
		model     string
		available bool
	}{{"qwen3:8b", true}, {"missing:latest", false}} {
		body := fmt.Sprintf(`{"url":%q,"model":%q,"health_timeout_seconds":2}`, ollama.URL, tt.model)
		rec := httptest.NewRecorder()
		s.testDiscoveryOllama(rec, httptest.NewRequest(http.MethodPost, "/api/discoveries/ollama/test", strings.NewReader(body)))
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
		var status aidiscovery.OllamaStatus
		if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil {
			t.Fatal(err)
		}
		if !status.Reachable || status.ModelAvailable != tt.available {
			t.Fatalf("model %s: %+v", tt.model, status)
		}
	}
}

func TestOllamaTestEndpointReportsUnreachableServer(t *testing.T) {
	offline := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := offline.URL
	offline.Close()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "ollama-offline.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	s := &Server{store: st, discoveryAI: aidiscovery.New(nil)}
	rec := httptest.NewRecorder()
	s.testDiscoveryOllama(rec, httptest.NewRequest(http.MethodPost, "/api/discoveries/ollama/test", strings.NewReader(fmt.Sprintf(`{"url":%q,"model":"qwen3:8b","health_timeout_seconds":1}`, url))))
	var status aidiscovery.OllamaStatus
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &status) != nil || status.Reachable {
		t.Fatalf("unexpected offline response: %d %s", rec.Code, rec.Body.String())
	}
}

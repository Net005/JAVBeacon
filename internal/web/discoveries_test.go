package web

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
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

func TestSubtitleSidecarMatchingIsCaseInsensitiveAndBounded(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"thza-10.en.srt", "THZA-100.en.srt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("subtitle"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	release := domain.Release{ID: 10, StashFilePath: filepath.Join(dir, "THZA-10.mp4")}
	if !hasSubtitleFile(release) {
		t.Fatal("case-insensitive subtitle sidecar was not detected")
	}
	if subtitleSidecarMatches("THZA-10", "THZA-100.en.srt") {
		t.Fatal("a different release ID was accepted as a subtitle sidecar")
	}
}

func TestSubtitleScanReportsLiveFoundCount(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "abc-1.en.srt"), []byte("subtitle"), 0o600); err != nil {
		t.Fatal(err)
	}
	releases := []domain.Release{{ID: 1, StashFilePath: filepath.Join(dir, "ABC-1.mp4")}, {ID: 2, StashFilePath: filepath.Join(dir, "ABC-2.mp4")}}
	lastCompleted, lastFound := 0, 0
	availability, stats := scanSubtitleAvailability(releases, func(completed, found int) {
		lastCompleted, lastFound = completed, found
	})
	if !availability[1] || availability[2] || lastCompleted != 2 || lastFound != 1 || stats.Directories != 1 || stats.UnreadableDirectories != 0 {
		t.Fatalf("unexpected subtitle scan: availability=%v completed=%d found=%d stats=%+v", availability, lastCompleted, lastFound, stats)
	}
}

// TestSubtitleScanCountsMissingFilePathSeparatelyFromUnreadableDirectories
// guards the diagnostic distinction added for the "why is subtitles indexed
// stuck at 0" report: a release with no recorded Stash file path at all
// (never matched/synced yet) must be counted separately from a directory
// that exists but can't be read (a real permission/mount problem), since
// the two point to completely different fixes.
func TestSubtitleScanCountsMissingFilePathSeparatelyFromUnreadableDirectories(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "abc-1.en.srt"), []byte("subtitle"), 0o600); err != nil {
		t.Fatal(err)
	}
	releases := []domain.Release{
		{ID: 1, StashFilePath: filepath.Join(dir, "ABC-1.mp4")},
		{ID: 2, StashFilePath: ""},
		{ID: 3, StashFilePath: filepath.Join(dir, "unreadable-dir", "ABC-3.mp4")},
	}
	availability, stats := scanSubtitleAvailability(releases, nil)
	if !availability[1] || availability[2] || availability[3] {
		t.Fatalf("unexpected availability: %v", availability)
	}
	if stats.MissingFilePath != 1 {
		t.Fatalf("expected 1 release with a missing file path, got %d", stats.MissingFilePath)
	}
	if stats.UnreadableDirectories != 1 {
		t.Fatalf("expected 1 unreadable directory, got %d", stats.UnreadableDirectories)
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

func TestDiscoveryFilterPushesExcludedTagsBeforePaging(t *testing.T) {
	filter, _, _ := discoveryFilterFromQuery(url.Values{}, map[string]string{
		"discoveries_excluded_tags": "Solowork, solo work\nFighters; Fighting Action",
	}, "for_you")
	want := []string{"fighters", "fighting action", "solo work", "solowork"}
	if !slices.Equal(filter.ExcludeTags, want) {
		t.Fatalf("ExcludeTags = %#v, want %#v", filter.ExcludeTags, want)
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
		if len(payload)+discoveryAIPromptOverheadChars > 50000 {
			t.Fatalf("batch %d exceeds character budget: %d", i+1, len(payload))
		}
	}
	if len(batches[0][0].Subtitle) == 0 || len(batches[0][0].Subtitle) >= 16000 {
		t.Fatalf("subtitle excerpt was not adaptively reduced: %d", len(batches[0][0].Subtitle))
	}
}

// TestDiscoveryAIBatchesDoesNotStarveSubtitlesWithModestStories guards
// against an overcautious prompt-overhead reserve zeroing out perSubtitle
// for a whole batch (every candidate's Subtitle left empty, surfaced to the
// user as "CC ready" with no subtitle actually reaching the model) even
// though there is real room once the reserve matches the instructions text.
func TestDiscoveryAIBatchesDoesNotStarveSubtitlesWithModestStories(t *testing.T) {
	dir := t.TempDir()
	items := make([]discoveryItem, 5)
	for i := range items {
		path := filepath.Join(dir, "SCENE-"+strconv.Itoa(i)+".mp4")
		content := strings.Repeat(fmt.Sprintf("unique dialogue line %d\n", i), 40)
		if err := os.WriteFile(strings.TrimSuffix(path, ".mp4")+".en.srt", []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		items[i] = discoveryItem{Release: domain.Release{ID: int64(i + 1), VideoID: "SCENE", Title: "Title", Story: strings.Repeat("story ", 50), StashFilePath: path}, HasSubtitle: true}
	}
	settings := map[string]string{"discoveries_subtitle_analysis_enabled": "true", "discoveries_subtitle_max_chars": "2000"}
	batches, _ := discoveryAIBatches(items, settings, len(items), 5, 25000)
	if len(batches) != 1 || len(batches[0]) != 5 {
		t.Fatalf("unexpected batches: %#v", batches)
	}
	for i, candidate := range batches[0] {
		if candidate.Subtitle == "" {
			t.Fatalf("candidate %d received no subtitle excerpt despite a reasonable budget", i)
		}
	}
}

// TestDiscoveryTextMatchesIsWholeWordNotSubstring guards a real observed
// false positive: a short discovery pool keyword like "AI" or "VR" used to
// match via strings.Contains, which also matches inside completely
// unrelated words. A school-themed, a gangbang, and a married-woman release
// all showed a spurious 2% "Sci-Fi" match from nothing more than "Maid" (in
// the title) or a similar word containing "ai" - none of them had any
// actual sci-fi content. Short keywords must only match as their own whole
// word.
func TestDiscoveryTextMatchesIsWholeWordNotSubstring(t *testing.T) {
	maidRelease := domain.Release{Title: "Famous Maid Past In Akihabara", Story: "A boyish shyness acquaintance."}
	if discoveryTextMatches(maidRelease, "AI") {
		t.Fatal(`"AI" keyword wrongly matched inside "Maid" via substring`)
	}
	trainingRelease := domain.Release{Story: "Rigorous training and certain obedience await her."}
	if discoveryTextMatches(trainingRelease, "AI") {
		t.Fatal(`"AI" keyword wrongly matched inside "training"/"certain" via substring`)
	}
	// A release that genuinely says "AI" as its own word must still match.
	genuineAI := domain.Release{Story: "An AI takes control of the household."}
	if !discoveryTextMatches(genuineAI, "AI") {
		t.Fatal(`"AI" keyword did not match a release that genuinely says "AI"`)
	}
	// Hyphenated keywords must still match against a hyphenated or
	// space-separated occurrence, since both sides now tokenize the same
	// way (word-boundary splitting, not substring).
	scifiGenre := domain.Release{Genres: []string{"Sci-Fi"}}
	if !discoveryTextMatches(scifiGenre, "sci-fi") {
		t.Fatal(`"sci-fi" keyword did not match a "Sci-Fi" genre tag`)
	}
	scifiTitle := domain.Release{Title: "Sci Fi Nurse Squad"}
	if !discoveryTextMatches(scifiTitle, "sci-fi") {
		t.Fatal(`"sci-fi" keyword did not match a space-separated "Sci Fi" title`)
	}
	// Multi-word keyword phrases still require every word present
	// (order-independent, same as before).
	mindControl := domain.Release{Story: "Under his control, her mind slowly gives in."}
	if !discoveryTextMatches(mindControl, "mind control") {
		t.Fatal(`"mind control" keyword did not match both words present in the story`)
	}
	if discoveryTextMatches(domain.Release{Story: "Just her mind, nothing else."}, "mind control") {
		t.Fatal(`"mind control" keyword matched with only one of its two words present`)
	}
}

func TestDiscoveryAttachPoolMatchesScoresKeywordCoverage(t *testing.T) {
	pools := map[string][]string{
		"Brainwashing / Drugs": {"drug", "brainwashing"},
		"Office Lady":          {"office lady"},
	}
	items := []discoveryItem{
		{Release: domain.Release{ID: 1, Genres: []string{"Drug"}}, Pools: []string{"Brainwashing / Drugs", "Brainwashing / Drugs"}},
		{Release: domain.Release{ID: 2, Genres: []string{"Drug", "Brainwashing"}}, Pools: []string{"Brainwashing / Drugs"}},
		{Release: domain.Release{ID: 3, Title: "Office Lady Seduction"}, Pools: []string{"Office Lady"}},
		{Release: domain.Release{ID: 4}},
	}
	discoveryAttachPoolMatches(items, pools)

	if got := items[0].PoolMatches; len(got) != 1 || got[0].Name != "Brainwashing / Drugs" || got[0].MatchPercent != 50 {
		t.Fatalf("partial keyword coverage = %#v, want a single 50%% match (duplicate pool name deduplicated)", got)
	}
	if got := items[1].PoolMatches; len(got) != 1 || got[0].MatchPercent != 100 {
		t.Fatalf("full keyword coverage = %#v, want 100%%", got)
	}
	if got := items[2].PoolMatches; len(got) != 1 || got[0].MatchPercent != 100 {
		t.Fatalf("single-keyword pool match = %#v, want 100%%", got)
	}
	if got := items[3].PoolMatches; got != nil {
		t.Fatalf("item with no pools should have nil PoolMatches, got %#v", got)
	}
}

func TestDiscoveryAIBatchesPrecomputeCandidatePoolEligibility(t *testing.T) {
	items := []discoveryItem{{Release: domain.Release{ID: 1, Title: "A drug-themed investigation", Story: "An investigator uncovers a coercive drug scheme.", Genres: []string{"Drug"}, Studio: "S1", Label: "L1", Director: "D1", ReleaseDate: "2026-09-20", Duration: "120 min"}, Score: 73.5, Category: "unwatched", HasSubtitle: true, Reasons: []string{"Performer preference: A", "Theme preference: Drug", "Studio preference: S1"}}}
	settings := map[string]string{
		"discoveries_pools":                     "Brainwashing / Drugs | drug, brainwashing\nAgent / Ninja / Spy | agent, ninja, spy",
		"discoveries_subtitle_analysis_enabled": "false",
	}
	batches, payloads := discoveryAIBatches(items, settings, 1, 1, 20000)
	if len(batches) != 1 || len(batches[0]) != 1 {
		t.Fatalf("unexpected batches: %#v", batches)
	}
	if !slices.Equal(batches[0][0].EligiblePools, []string{"Brainwashing / Drugs"}) {
		t.Fatalf("candidate pool eligibility=%#v", batches[0][0].EligiblePools)
	}
	candidate := batches[0][0]
	if candidate.BaselineScore != 73.5 || candidate.DiscoveryState != "unwatched" ||
		candidate.Story == "" || candidate.Label != "L1" || candidate.Director != "D1" ||
		candidate.ReleaseDate != "2026-09-20" || candidate.Duration != "120 min" || !candidate.SubtitleAvailable ||
		!slices.Equal(candidate.Taste.PreferredPerformers, []string{"A"}) ||
		!slices.Equal(candidate.Taste.PreferredThemes, []string{"Drug"}) || candidate.Taste.PreferredStudio != "S1" {
		t.Fatalf("structured recommendation context=%#v", candidate)
	}
	payload := string(payloads[0])
	if strings.Contains(payload, "grounding_evidence") || strings.Contains(payload, "Performer preference:") ||
		!strings.Contains(payload, `"taste_match"`) || !strings.Contains(payload, `"director":"D1"`) ||
		!strings.Contains(payload, `"subtitle_available":true`) {
		t.Fatalf("model payload still contains label-heavy evidence: %s", payload)
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
		if got := discoveryAITextMatches(tt.text, tt.entries, "or"); got != tt.want {
			t.Fatalf("discoveryAITextMatches(%q, %q) = %v, want %v", tt.text, tt.entries, got, tt.want)
		}
	}
	if !discoveryAITextMatches("Undercover investigator story", "under*, investigator", "and") {
		t.Fatal("AND AI text filtering should require and accept every partial value")
	}
	if discoveryAITextMatches("Undercover investigator story", "under, missing", "and") {
		t.Fatal("AND AI text filtering accepted a missing value")
	}
}

func TestApplyOpenAIRanksRetainsGeneratedTextForFiltering(t *testing.T) {
	items := applyOpenAIRanks([]discoveryItem{{Release: domain.Release{ID: 42}, Reasons: []string{"Theme preference: drama", "Studio preference: S1"}}}, []openAIRank{{ID: 42, Score: 91, Reason: "Its title and studio make this a strong fit."}})
	if len(items) != 1 || !items[0].AIEnhanced || items[0].AIText != "Its title and studio make this a strong fit." {
		t.Fatalf("AI enrichment was not retained: %#v", items)
	}
	if len(items[0].Reasons) != 1 || items[0].Reasons[0] != "Its title and studio make this a strong fit." {
		t.Fatalf("AI explanation still includes labelled deterministic evidence: %#v", items[0].Reasons)
	}
}

// TestApplyOpenAIRanksDistinguishesSubtitleUsedFromAvailable guards the
// "subtitle used in analysis" signal: a release can have HasSubtitle=true
// (a subtitle file exists) while discoveryAIBatches' shared per-batch
// character budget left no room for its excerpt, so the AI never actually
// saw it. The UI must be able to tell the two states apart instead of
// treating file existence as proof the AI used it.
func TestApplyOpenAIRanksDistinguishesSubtitleUsedFromAvailable(t *testing.T) {
	items := []discoveryItem{
		{Release: domain.Release{ID: 1}, HasSubtitle: true},
		{Release: domain.Release{ID: 2}, HasSubtitle: true},
	}
	ranks := []openAIRank{
		{ID: 1, Score: 80, Reason: "Used subtitle dialogue", SubtitleUsed: true},
		{ID: 2, Score: 60, Reason: "Budget exhausted before this release", SubtitleUsed: false},
	}
	result := applyOpenAIRanks(items, ranks)
	byID := map[int64]discoveryItem{}
	for _, item := range result {
		byID[item.ID] = item
	}
	if !byID[1].HasSubtitle || !byID[1].SubtitleUsed {
		t.Fatalf("release 1 should show both available and used: %#v", byID[1])
	}
	if !byID[2].HasSubtitle || byID[2].SubtitleUsed {
		t.Fatalf("release 2 has a subtitle file but the AI never saw it: %#v", byID[2])
	}
}

type recordingAIRankSaver struct{ saved []domain.DiscoveryAIRank }

func (s *recordingAIRankSaver) SaveDiscoveryAIRanks(_ context.Context, ranks []domain.DiscoveryAIRank) error {
	s.saved = append(s.saved, ranks...)
	return nil
}

func TestValidatedAIRankingPersistenceGuard(t *testing.T) {
	saver := &recordingAIRankSaver{}
	valid := domain.DiscoveryAIRank{ReleaseID: 7, Fingerprint: "v2", Model: "ollama:qwen3:8b", Score: 84, Reason: "Match: Its detailed story and familiar studio provide strong support for this recommendation."}
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
	if err := st.SaveDiscoveryAIRanks(ctx, []domain.DiscoveryAIRank{{ReleaseID: goodID, Score: 90, Reason: "Match: Its detailed story and familiar studio provide strong support for this recommendation.", Fingerprint: "good"}, {ReleaseID: badID, Score: 50, Reason: badReason, Fingerprint: "bad"}}); err != nil {
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

// TestDiscoveryAIRankSubtitleUsedPersistsAcrossReload guards the durable
// storage side of the "subtitle used" signal: it must survive a save/reload
// round trip through the real SQLite store, not just an in-memory struct,
// since discoveries.go relies on DiscoveryAIRanks/AllDiscoveryAIRanks to
// rehydrate it on every page load and by the repair job.
func TestDiscoveryAIRankSubtitleUsedPersistsAcrossReload(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "subtitle-used.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	site, err := st.SaveSite(ctx, domain.Site{Title: "Subtitle Used", Name: "Subtitle Used", Type: "Site", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, release := range []domain.Release{{SiteID: site.ID, VideoID: "USED-001", Title: "Used"}, {SiteID: site.ID, VideoID: "SKIP-001", Title: "Skipped"}} {
		if _, err := st.UpsertRelease(ctx, release); err != nil {
			t.Fatal(err)
		}
	}
	releases, err := st.Releases(ctx, domain.ReleaseFilter{Limit: 10, ShowNonPreferred: true})
	if err != nil || len(releases) != 2 {
		t.Fatalf("load releases: %v count=%d", err, len(releases))
	}
	var usedID, skippedID int64
	for _, release := range releases {
		if release.VideoID == "USED-001" {
			usedID = release.ID
		} else {
			skippedID = release.ID
		}
	}
	if err := st.SaveDiscoveryAIRanks(ctx, []domain.DiscoveryAIRank{
		{ReleaseID: usedID, Fingerprint: "u1", Score: 90, Reason: "Match: subtitle dialogue supports this recommendation.", SubtitleUsed: true},
		{ReleaseID: skippedID, Fingerprint: "s1", Score: 70, Reason: "Match: title and metadata support this recommendation.", SubtitleUsed: false},
	}); err != nil {
		t.Fatal(err)
	}
	byID, err := st.DiscoveryAIRanks(ctx, []int64{usedID, skippedID})
	if err != nil {
		t.Fatal(err)
	}
	if !byID[usedID].SubtitleUsed {
		t.Fatalf("subtitle_used=true did not round-trip: %#v", byID[usedID])
	}
	if byID[skippedID].SubtitleUsed {
		t.Fatalf("subtitle_used=false was incorrectly persisted as true: %#v", byID[skippedID])
	}
	all, err := st.AllDiscoveryAIRanks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	allByID := map[int64]domain.DiscoveryAIRank{}
	for _, rank := range all {
		allByID[rank.ReleaseID] = rank
	}
	if !allByID[usedID].SubtitleUsed || allByID[skippedID].SubtitleUsed {
		t.Fatalf("AllDiscoveryAIRanks did not preserve subtitle_used: %#v", allByID)
	}
	// A later save that flips the flag on the same release must overwrite it,
	// not merely add a second row (release_id is the primary key).
	if err := st.SaveDiscoveryAIRanks(ctx, []domain.DiscoveryAIRank{{ReleaseID: usedID, Fingerprint: "u2", Score: 91, Reason: "Match: refreshed without subtitle dialogue.", SubtitleUsed: false}}); err != nil {
		t.Fatal(err)
	}
	byID, err = st.DiscoveryAIRanks(ctx, []int64{usedID})
	if err != nil {
		t.Fatal(err)
	}
	if byID[usedID].SubtitleUsed {
		t.Fatalf("subtitle_used was not overwritten on update: %#v", byID[usedID])
	}
}

func TestClearDiscoveryAIRankingsEndpoint(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "clear-ai-endpoint.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	site, err := st.SaveSite(ctx, domain.Site{Title: "Clear AI", Name: "Clear AI", Type: "Site", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "CLEAR-001", Title: "Keep me"}); err != nil {
		t.Fatal(err)
	}
	releases, err := st.Releases(ctx, domain.ReleaseFilter{Limit: 1, ShowNonPreferred: true})
	if err != nil || len(releases) != 1 {
		t.Fatalf("release lookup: %#v err=%v", releases, err)
	}
	if err := st.SaveDiscoveryAIRanks(ctx, []domain.DiscoveryAIRank{{ReleaseID: releases[0].ID, Fingerprint: "old", Model: "ollama:qwen3:8b", Score: 85, Reason: "Match: supplied title metadata."}}); err != nil {
		t.Fatal(err)
	}
	s := &Server{store: st, log: slog.Default()}
	rec := httptest.NewRecorder()
	s.clearDiscoveryAIRankings(rec, httptest.NewRequest(http.MethodDelete, "/api/discoveries/ai-rankings", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"removed":1`) {
		t.Fatalf("clear response status=%d body=%s", rec.Code, rec.Body.String())
	}
	ranks, err := st.AllDiscoveryAIRanks(ctx)
	if err != nil || len(ranks) != 0 {
		t.Fatalf("rankings remain: %#v err=%v", ranks, err)
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

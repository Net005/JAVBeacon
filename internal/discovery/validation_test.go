package discovery

import (
	"math"
	"strings"
	"testing"

	"github.com/Net005/JAVBeacon/internal/domain"
)

const actualBadReason = "The content you provided appears to be a mix of unrelated text, possibly including elements from a fictional story or anime, along with some random phrases and possibly corrupted or incomplete data. There is no clear, coherent narrative or meaningful context that can be extracted from the text as it stands. If you are referring to a specific story, character, or plot, please provide more context or clarify your request so I can assist you better."

func validRank() Rank {
	return Rank{ID: 7, Score: 82, Reason: "Match: Its science-fiction theme, preferred performer, and familiar studio create a strong recommendation.", Pools: []string{"Sci-Fi"}}
}

func TestInvalidConversationalReasonRejected(t *testing.T) {
	for _, reason := range []string{
		actualBadReason,
		"Please provide more context so I can assist you.",
		"I cannot determine what this means from the supplied text.",
		"The subtitle text is incoherent and contains random phrases.",
		"Could you clarify which story you mean?",
		"Match: No CUSTOM DISCOVERY POOL tags present.",
		"Match: No relevant pool tags are present.",
		"Match: Performer preference: Hinata Mio, Theme preference: drugs.",
	} {
		rank := validRank()
		rank.Reason = reason
		if err := validateRanks([]Rank{rank}, testCandidates(), "Sci-Fi | space, heroine"); err == nil {
			t.Fatalf("conversational reason accepted: %q", reason)
		}
	}
}

func TestPoolMustBeSupportedByCandidateEvidence(t *testing.T) {
	candidate := Candidate{ID: 7, Title: "Drug-themed story", Story: "A drug-focused plot.", Genres: []string{"Drug"}, EligiblePools: []string{"Brainwashing / Drugs"}}
	valid := Rank{ID: 7, Score: 80, Reason: "Match: Its drug-focused theme aligns with the story and genre signals.", Pools: []string{"Brainwashing / Drugs"}}
	if err := validateRanks([]Rank{valid}, []Candidate{candidate}, "Brainwashing / Drugs | drug\nAgent / Ninja / Spy | agent, ninja, spy"); err != nil {
		t.Fatalf("eligible pool rejected: %v", err)
	}
	valid.Pools = []string{"Agent / Ninja / Spy"}
	if err := validateRanks([]Rank{valid}, []Candidate{candidate}, "Brainwashing / Drugs | drug\nAgent / Ninja / Spy | agent, ninja, spy"); err == nil || !strings.Contains(err.Error(), "pool unsupported") {
		t.Fatalf("unsupported candidate pool was accepted: %v", err)
	}
}

func TestValidConciseRecommendationReasonAccepted(t *testing.T) {
	if err := validateRanks([]Rank{validRank()}, testCandidates(), "Sci-Fi | space, heroine"); err != nil {
		t.Fatalf("valid rank rejected: %v", err)
	}
	rank := validRank()
	rank.Reason = "Its science-fiction theme, preferred performer, and familiar studio make this a strong recommendation."
	if err := validateRanks([]Rank{rank}, testCandidates(), "Sci-Fi | space, heroine"); err != nil {
		t.Fatalf("natural unlabelled reason rejected: %v", err)
	}
}

func TestRankingValidationRejectsUnsafeStructures(t *testing.T) {
	tests := []struct {
		name  string
		ranks []Rank
	}{
		{"empty reason", []Rank{{ID: 7, Score: 50}}},
		{"unknown ID", []Rank{{ID: 999, Score: 50, Reason: "Strong genre match."}}},
		{"duplicate ID", []Rank{validRank(), validRank()}},
		{"negative score", []Rank{{ID: 7, Score: -1, Reason: "Strong genre match."}}},
		{"high score", []Rank{{ID: 7, Score: 101, Reason: "Strong genre match."}}},
		{"fractional score", []Rank{{ID: 7, Score: 0.9, Reason: "Strong genre match."}}},
		{"NaN score", []Rank{{ID: 7, Score: math.NaN(), Reason: "Strong genre match."}}},
		{"unknown pool", []Rank{{ID: 7, Score: 50, Reason: "Strong genre match.", Pools: []string{"Unknown"}}}},
		{"markdown reason", []Rank{{ID: 7, Score: 50, Reason: "```Strong genre match.```"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := validateRanks(tt.ranks, testCandidates(), "Sci-Fi | space"); err == nil {
				t.Fatal("invalid ranking was accepted")
			}
		})
	}
}

func TestCanonicalIntegerScoreAccepted(t *testing.T) {
	rank := validRank()
	rank.Score = 90
	if err := validateRanks([]Rank{rank}, testCandidates(), "Sci-Fi | space"); err != nil {
		t.Fatalf("canonical integer score rejected: %v", err)
	}
}

func TestGemmaGroundedRecommendationAccepted(t *testing.T) {
	candidate := Candidate{ID: 7, Title: "Space investigator", Genres: []string{"Sci-Fi"}, Evidence: []string{"Theme preference: Sci-Fi"}}
	rank := Rank{ID: 7, Score: 88, Reason: "Match: Its science-fiction setting aligns strongly with the established preference for that theme."}
	if err := validateRanks([]Rank{rank}, []Candidate{candidate}, ""); err != nil {
		t.Fatalf("valid Gemma-style grounded result rejected: %v", err)
	}
}

func TestQwenGroundedRecommendationAccepted(t *testing.T) {
	candidate := Candidate{ID: 7, Title: "Detective story", Story: "An undercover investigation.", Played: 2}
	rank := Rank{ID: 7, Score: 81, Reason: "Match: Its undercover story and previous play history make this a compelling rewatch candidate."}
	if err := validateRanks([]Rank{rank}, []Candidate{candidate}, ""); err != nil {
		t.Fatalf("valid Qwen-style grounded result rejected: %v", err)
	}
}

// TestGroundedSubtitleMentionNotTreatedAsQualityComplaint guards against a
// real false-positive rejection observed with small local models such as
// qwen3:8b-q4_K_M, which frequently echo the prompt's own vocabulary
// ("subtitle text"/"subtitle lines") when legitimately citing subtitle
// evidence, rather than complaining about its quality. Only an actual
// quality complaint (corrupt/incoherent/random/unrelated/incomplete)
// should trip the conversational-reason rejection.
func TestGroundedSubtitleMentionNotTreatedAsQualityComplaint(t *testing.T) {
	candidate := Candidate{ID: 7, Title: "Office drama", Story: "A workplace romance.", Subtitle: "A meaningful supplied line", SubtitleAvailable: true}
	for _, reason := range []string{
		"Match: Its subtitle text describes a workplace romance that aligns with the story, giving this release clear thematic relevance.",
		"Match: The subtitle lines reinforce the story's workplace romance theme, supporting a strong recommendation here.",
	} {
		rank := Rank{ID: 7, Score: 78, Reason: reason}
		if err := validateRanks([]Rank{rank}, []Candidate{candidate}, ""); err != nil {
			t.Fatalf("legitimate subtitle-grounded reason rejected (%q): %v", reason, err)
		}
	}
	// An actual quality complaint about subtitles must still be rejected.
	complaint := Rank{ID: 7, Score: 40, Reason: "Match: The subtitle text is incoherent and contains random phrases, but the story theme is a relevant match."}
	if err := validateRanks([]Rank{complaint}, []Candidate{candidate}, ""); err == nil {
		t.Fatal("subtitle quality complaint was accepted")
	}
}

func TestUngroundedHistoricalAndPreferenceClaimsRejected(t *testing.T) {
	candidate := Candidate{ID: 7, Title: "Supplied title", Studio: "S1", Actresses: []string{"A"}, Genres: []string{"Sci-Fi"}}
	for _, reason := range []string{
		"Strong match based on this studio's history.",
		"Strong match with a frequently watched studio.",
		"Matches the preferred performer and related tags.",
		"Fits the user's preferred genre.",
		"Good rewatch candidate based on viewing history.",
	} {
		rank := Rank{ID: 7, Score: 80, Reason: reason}
		if err := validateRanks([]Rank{rank}, []Candidate{candidate}, ""); err == nil {
			t.Fatalf("ungrounded claim accepted: %q", reason)
		}
	}
}

func TestGroundedHistoricalAndPreferenceClaimsAccepted(t *testing.T) {
	candidate := Candidate{
		ID: 7, Title: "Supplied title", Story: "Supplied story", Studio: "S1", Actresses: []string{"A"}, Genres: []string{"Sci-Fi"}, Played: 3,
		Evidence: []string{"Studio preference: S1", "Studio history: watched 4 releases from S1", "Performer preference: A", "Theme preference: Sci-Fi"},
	}
	for _, reason := range []string{
		"Match: Its story and familiar studio align with the established studio preference.",
		"Match: Its story is reinforced by established viewing history for this studio.",
		"Match: Its story and related tags feature a performer established as preferred.",
		"Match: Its story and science-fiction setting align with the user's preferred genre.",
		"Match: Its supplied story and viewing history make this a compelling rewatch candidate.",
	} {
		rank := Rank{ID: 7, Score: 80, Reason: reason}
		if err := validateRanks([]Rank{rank}, []Candidate{candidate}, ""); err != nil {
			t.Fatalf("grounded claim rejected (%q): %v", reason, err)
		}
	}
}

func TestTypedTasteSignalsGroundPreferenceClaims(t *testing.T) {
	candidate := Candidate{
		ID: 7, Title: "Supplied title", Story: "A psychological drug-themed story.", Actresses: []string{"A"}, Genres: []string{"Drug"},
		Taste: TasteSignals{PreferredPerformers: []string{"A"}, PreferredThemes: []string{"Drug"}},
	}
	rank := Rank{ID: 7, Score: 84, Reason: "Match: Its psychological drug theme aligns with established interests, while the familiar performer adds another strong preference signal."}
	if err := validateRanks([]Rank{rank}, []Candidate{candidate}, ""); err != nil {
		t.Fatalf("typed taste evidence rejected: %v", err)
	}
}

func TestRecommendationReasonQualityContract(t *testing.T) {
	candidate := Candidate{ID: 7, Title: "Supplied title"}
	for _, reason := range []string{
		"Strong title match with useful metadata and recommendation relevance.",
		"Match: Title match.",
		"Match: This recommendation explanation contains far too many individual words and continues without focus or useful prioritization until it exceeds the deliberately bounded descriptive sentence length expected from the internal ranking component for a concise interface reason and then keeps adding unnecessary generic filler without providing any additional evidence or value.",
	} {
		if err := validateRanks([]Rank{{ID: 7, Score: 60, Reason: reason}}, []Candidate{candidate}, ""); err == nil {
			t.Fatalf("low-quality reason accepted: %q", reason)
		}
	}
}

func TestNaturalMetadataExplanationDoesNotRequireRankingKeywords(t *testing.T) {
	candidate := Candidate{ID: 7, Title: "Office Temptation", Actresses: []string{"Fukada Yuuri"}, Genres: []string{"Creampie", "Humiliation"}}
	rank := Rank{ID: 7, Score: 78, Reason: "Fukada Yuuri appears alongside the concrete creampie and humiliation elements found in this release."}
	if err := validateRanks([]Rank{rank}, []Candidate{candidate}, ""); err != nil {
		t.Fatalf("natural metadata explanation rejected: %v", err)
	}
}

// TestGenuinePoolContentIsNotRejectedAsMetaCommentary guards against a bare
// "pool"/"pools" word check that used to live in conversationalReason and
// rejected ANY reason mentioning the word at all - including entirely
// legitimate content, since a JAV release can literally be set at, or
// tagged with, a swimming pool. Only meta-commentary about the discovery-
// pool *feature* ("custom discovery pool", "pool tags" phrasing, still
// covered by TestInvalidConversationalReasonRejected) should be rejected.
func TestGenuinePoolContentIsNotRejectedAsMetaCommentary(t *testing.T) {
	candidate := Candidate{ID: 7, Title: "Poolside Encounter", Story: "A steamy afternoon by the pool leads to an unexpected encounter.", Genres: []string{"Pool"}}
	rank := Rank{ID: 7, Score: 74, Reason: "Its poolside story and pool setting align closely with the tags found in this release."}
	if err := validateRanks([]Rank{rank}, []Candidate{candidate}, ""); err != nil {
		t.Fatalf("genuine pool content rejected as meta-commentary: %v", err)
	}
}

// TestValidationErrorSurfacesRejectedReasonDetail guards the diagnostic
// detail attached to a rejected reason. Rejections used to log only a bare
// category like "conversational/non-ranking reason" with no way to see what
// the model actually wrote, making repeat false-positive rejections
// impossible to root-cause from logs alone.
func TestValidationErrorSurfacesRejectedReasonDetail(t *testing.T) {
	candidate := Candidate{ID: 7, Title: "Supplied title"}
	rank := Rank{ID: 7, Score: 60, Reason: "I'm sorry, I cannot determine a meaningful recommendation from this data."}
	err := validateRanks([]Rank{rank}, []Candidate{candidate}, "")
	if err == nil {
		t.Fatal("expected a conversational reason rejection")
	}
	if !strings.Contains(err.Error(), "I'm sorry") {
		t.Fatalf("rejected-reason detail missing from error: %v", err)
	}
}

func TestPreferenceForAnotherSignalDoesNotBecomePerformerClaim(t *testing.T) {
	candidate := Candidate{ID: 7, Title: "Supplied title", Story: "Supplied story", Actresses: []string{"A"}, Genres: []string{"Sci-Fi"}, Evidence: []string{"Theme preference: Sci-Fi"}}
	for _, reason := range []string{
		"Match: Preferred genre with a performer listed in the metadata.",
		"Match: The preferred theme aligns with the story while the listed cast provides additional context.",
	} {
		rank := Rank{ID: 7, Score: 80, Reason: reason}
		if err := validateRanks([]Rank{rank}, []Candidate{candidate}, ""); err != nil {
			t.Fatalf("unrelated performer preference false positive (%q): %v", reason, err)
		}
	}
}

func TestSubtitleClaimRequiresSubtitleEvidence(t *testing.T) {
	rank := Rank{ID: 7, Score: 70, Reason: "Match: Subtitle availability provides useful dialogue context supporting this grounded recommendation."}
	candidate := Candidate{ID: 7, Title: "Supplied title"}
	if err := validateRanks([]Rank{rank}, []Candidate{candidate}, ""); err == nil {
		t.Fatal("subtitle claim without subtitle evidence was accepted")
	}
	candidate.Subtitle = "A meaningful supplied line"
	if err := validateRanks([]Rank{rank}, []Candidate{candidate}, ""); err != nil {
		t.Fatalf("grounded subtitle claim rejected: %v", err)
	}
}

func TestStrictRankingJSONRejectsInvalidJSONMarkdownAndUnknownFields(t *testing.T) {
	for _, content := range []string{
		`not json`,
		"```json\n{\"rankings\":[]}\n```",
		`{"rankings":[{"id":7,"score":80,"reason":"Strong story match.","pools":[],"extra":true}]}`,
		`{"rankings":[{"id":7,"score":80,"reason":"Strong story match.","pools":[]}],"extra":true}`,
	} {
		if _, err := parseRankingJSON(content, testCandidates(), ""); err == nil {
			t.Fatalf("unsafe JSON accepted: %s", content)
		}
	}
}

func coverageCandidates() []Candidate {
	return []Candidate{{ID: 41, VideoID: "777", Title: "First supplied title"}, {ID: 907, VideoID: "TWO-907", Title: "Second supplied title"}}
}

func TestRankingMustCoverEveryCandidateExactlyOnce(t *testing.T) {
	valid := []Rank{
		{ID: 41, Score: 80, Reason: "Match: The supplied title provides a clear and specific thematic recommendation signal.", Pools: []string{}},
		{ID: 907, Score: 72, Reason: "Match: The supplied title provides another clear and specific thematic recommendation signal.", Pools: []string{}},
	}
	if err := validateRanks(valid, coverageCandidates(), ""); err != nil {
		t.Fatalf("complete one-to-one coverage rejected: %v", err)
	}
	for name, ranks := range map[string][]Rank{
		"missing candidate": valid[:1],
		"extra ranking":     append(append([]Rank{}, valid...), Rank{ID: 999, Score: 60, Reason: "Title match."}),
	} {
		t.Run(name, func(t *testing.T) {
			err := validateRanks(ranks, coverageCandidates(), "")
			if err == nil || !strings.Contains(err.Error(), "incomplete candidate coverage") {
				t.Fatalf("coverage violation not rejected correctly: %v", err)
			}
		})
	}
	duplicate := []Rank{valid[0], valid[0]}
	if err := validateRanks(duplicate, coverageCandidates(), ""); err == nil || !strings.Contains(err.Error(), "duplicate candidate ID") {
		t.Fatalf("duplicate IDs not rejected correctly: %v", err)
	}
}

func TestUnknownCandidateIDRejected(t *testing.T) {
	for _, id := range []int64{1, 2, 3, 777} {
		ranks := []Rank{{ID: id, Score: 80, Reason: "Strong title match."}, {ID: 907, Score: 70, Reason: "Relevant title match."}}
		if err := validateRanks(ranks, coverageCandidates(), ""); err == nil || !strings.Contains(err.Error(), "unknown candidate ID") {
			t.Fatalf("invented/positional/video ID %d not rejected correctly: %v", id, err)
		}
	}
}

func TestActualBadStoredReasonRejected(t *testing.T) {
	err := ValidateStoredRank(domain.DiscoveryAIRank{ReleaseID: 7, Score: 60, Reason: actualBadReason}, "")
	if err == nil || !strings.Contains(err.Error(), "conversational") {
		t.Fatalf("actual bad reason was not classified correctly: %v", err)
	}
}

func TestReasonLengthLimit(t *testing.T) {
	rank := validRank()
	rank.Reason = strings.Repeat("Strong recommendation match. ", 40)
	if err := validateRanks([]Rank{rank}, testCandidates(), "Sci-Fi | space"); err == nil {
		t.Fatal("overlong reason accepted")
	}
}

// TestPromptInstructsCitingSubtitleContentWhenUsable guards against the
// prompt framing subtitle excerpts as such a low priority that the model
// never actually cites them in a reason even when a usable excerpt is
// supplied - "Subtitle used" on a card should be reflected in "Why it
// fits", not just mean the excerpt was included in the request payload.
func TestPromptInstructsCitingSubtitleContentWhenUsable(t *testing.T) {
	prompt := rankingPrompt([]Candidate{{ID: 7, Subtitle: "fragmented line"}}, "Sci-Fi | space", defaultSubtitleWeights())
	for _, required := range []string{
		"cite that concrete detail rather than falling",
		"a usable one should not be ignored either",
		"Never open a reason with a fixed template phrase",
	} {
		if !strings.Contains(prompt, required) {
			t.Fatalf("prompt missing %q", required)
		}
	}
}

// TestPromptTreatsSubtitleAsPrimaryNarrativeForStoryEmptyCandidates guards
// the instruction that a usable subtitle excerpt is the primary narrative
// evidence (on par with a populated story field) for a candidate with no
// story at all - the common case for JAVLibrary-sourced releases, which
// only supply a title and a short tag list.
func TestPromptTreatsSubtitleAsPrimaryNarrativeForStoryEmptyCandidates(t *testing.T) {
	prompt := rankingPrompt([]Candidate{{ID: 7, Subtitle: "fragmented line"}}, "", defaultSubtitleWeights())
	for _, required := range []string{
		"only a title and a short tag list and no",
		"story field at all (for example JAVLibrary-sourced releases)",
		"treat it as primary narrative evidence on the",
		"same footing as a populated story field",
		"stepmother growing closer to her husband's son",
	} {
		if !strings.Contains(prompt, required) {
			t.Fatalf("prompt missing %q", required)
		}
	}
}

// TestCorruptionThemeNotFlaggedAsQualityComplaint guards against a real
// observed false-positive rejection: "corrupt" was matched with plain
// strings.Contains, which also matches inside "corruption" - an entirely
// ordinary JAV theme word (an "NTR/corruption premise") that has nothing to
// do with subtitle data quality.
func TestCorruptionThemeNotFlaggedAsQualityComplaint(t *testing.T) {
	reason := "Aozora Hikari, drama, and SOD Create each match established interests, while the title's indoctrination and NTR premise adds a clear corruption angle."
	if conversationalReason(reason) {
		t.Fatalf("corruption-themed reason wrongly flagged as a quality complaint: %q", reason)
	}
	// An actual "corrupt"/"corrupted" quality complaint must still be caught.
	for _, complaint := range []string{
		"The subtitle text is corrupt and unreadable, but the story theme is a relevant match.",
		"The subtitle data appears corrupted, so this recommendation relies on tags and performer alone.",
	} {
		if !conversationalReason(complaint) {
			t.Fatalf("genuine corruption complaint not flagged: %q", complaint)
		}
	}
}

// TestSanitizeRanksRepairsInsteadOfDiscardingWholeBatch guards the lenient
// per-candidate salvage path added to parseRankingJSON. Individual reason-
// text mistakes (an ungrounded claim, a stray markdown fence) used to
// discard the ENTIRE batch's rankings - including every other candidate's
// genuinely good rank - after a wasted repair attempt, and could ultimately
// hard-fail the whole AI Discovery request. Now only a candidate whose own
// rank is unusable gets a safe, deterministic fallback; every other
// candidate's valid rank is returned untouched, and the call never errors
// for content reasons alone.
func TestSanitizeRanksRepairsInsteadOfDiscardingWholeBatch(t *testing.T) {
	candidates := []Candidate{
		{ID: 1, Title: "Valid candidate"},
		{ID: 2, Title: "Ungrounded studio claim", Studio: ""},
		{ID: 3, Title: "Markdown fenced reason"},
	}
	content := `{"rankings":[` +
		`{"id":1,"score":80,"reason":"Fukada Yuuri appears alongside the concrete creampie and humiliation elements found here.","pools":[]},` +
		`{"id":2,"score":70,"reason":"Strong match based on this studio's history and preference.","pools":[]},` +
		`{"id":3,"score":60,"reason":"` + "```" + `Strong genre match.` + "```" + `","pools":[]}` +
		`]}`
	ranks, err := parseRankingJSON(content, candidates, "")
	if err != nil {
		t.Fatalf("expected lenient acceptance, got error: %v", err)
	}
	if len(ranks) != 3 {
		t.Fatalf("expected exactly 3 sanitized ranks, got %d", len(ranks))
	}
	byID := map[int64]Rank{}
	for _, rank := range ranks {
		byID[rank.ID] = rank
	}
	if byID[1].Reason != "Fukada Yuuri appears alongside the concrete creampie and humiliation elements found here." {
		t.Fatalf("valid candidate's rank was altered: %+v", byID[1])
	}
	for _, id := range []int64{2, 3} {
		rank := byID[id]
		if rank.Reason != fallbackReason {
			t.Fatalf("candidate %d with unusable reason was not sanitized to the fallback: %+v", id, rank)
		}
		if rank.Score < 0 || rank.Score > 100 {
			t.Fatalf("candidate %d has an out-of-range sanitized score: %v", id, rank.Score)
		}
	}
	// The sanitized fallback reason must itself always pass validation
	// regardless of candidate metadata, or a bad batch could re-trigger the
	// very rejection it exists to avoid.
	if err := validateGrounding(Rank{Reason: fallbackReason}, Candidate{}); err != nil {
		t.Fatalf("fallback reason is not self-grounding: %v", err)
	}
}

// TestSanitizeRanksStillRetriesOnStructuralMismatch guards that only
// content-level problems are sanitized leniently - a genuinely broken batch
// (wrong candidate set) still returns an error so the caller retries with a
// repair prompt, since sanitizing per-candidate cannot invent a rank for a
// candidate ID the model never returned at all... except it now can (via the
// per-candidate fallback), so completeness itself must remain a hard,
// retry-worthy failure to guarantee the model actually attempts every
// candidate rather than silently relying on fallbacks for ones it skipped.
func TestSanitizeRanksStillRetriesOnStructuralMismatch(t *testing.T) {
	candidates := []Candidate{{ID: 1, Title: "First"}, {ID: 2, Title: "Second"}}
	content := `{"rankings":[{"id":1,"score":80,"reason":"Strong story and genre alignment here for this one.","pools":[]}]}`
	if _, err := parseRankingJSON(content, candidates, ""); err == nil {
		t.Fatal("incomplete candidate coverage was leniently accepted instead of triggering a retry")
	}
	duplicate := `{"rankings":[` +
		`{"id":1,"score":80,"reason":"Strong story and genre alignment here for this one.","pools":[]},` +
		`{"id":1,"score":70,"reason":"Another strong story and genre alignment for this one.","pools":[]}` +
		`]}`
	if _, err := parseRankingJSON(duplicate, candidates, ""); err == nil {
		t.Fatal("duplicate candidate ID was leniently accepted instead of triggering a retry")
	}
}

// TestSubtitleWeightIsConfigurablePerStoryPresence guards the two
// independent, user-configurable emphasis knobs added for how much
// narrative weight the ranking prompt gives subtitle_excerpt: one for
// candidates with no story field at all (mostly JAVLibrary), one for
// candidates that already have a story field (mostly Akiba/GIGA, where
// subtitles are already AI-translated and can carry real narrative detail).
func TestSubtitleWeightIsConfigurablePerStoryPresence(t *testing.T) {
	candidate := Candidate{ID: 7, Subtitle: "fragmented line"}

	// Defaults: no-story candidates treat subtitles as fully primary
	// (100), story-present candidates get a fair, co-equal blend (50).
	def := rankingPrompt([]Candidate{candidate}, "", defaultSubtitleWeights())
	for _, required := range []string{
		"treat it as primary narrative evidence on the",
		"same footing as a populated story field",
		"give the subtitle detail genuinely equal weight to the story",
	} {
		if !strings.Contains(def, required) {
			t.Fatalf("default-weight prompt missing %q", required)
		}
	}

	// Weight 0 for both cases must instruct the model not to use subtitle
	// content in the reason at all for that case.
	zero := rankingPrompt([]Candidate{candidate}, "", subtitleWeights{NoStory: 0, WithStory: 0})
	for _, required := range []string{
		"do not use subtitle_excerpt content in the reason at all",
		"ignore it in the reason and rely on the story alone",
	} {
		if !strings.Contains(zero, required) {
			t.Fatalf("zero-weight prompt missing %q", required)
		}
	}

	// Weight 100 for the with-story case must let subtitle content take
	// precedence over the story, distinct from the fair-blend default.
	high := rankingPrompt([]Candidate{candidate}, "", subtitleWeights{NoStory: 100, WithStory: 100})
	if !strings.Contains(high, "let the subtitle detail take precedence over the story") {
		t.Fatalf("high with-story-weight prompt missing precedence instruction")
	}

	// resolveSubtitleWeights clamps out-of-range Config values.
	weights := resolveSubtitleWeights(Config{SubtitleWeightNoStory: 250, SubtitleWeightWithStory: -10})
	if weights.NoStory != 100 || weights.WithStory != 0 {
		t.Fatalf("resolveSubtitleWeights did not clamp: %+v", weights)
	}
}

func TestHardenedPromptSeparatesSubtitleFromUserRequest(t *testing.T) {
	prompt := rankingPrompt([]Candidate{{ID: 7, Subtitle: "fragmented line"}}, "Sci-Fi | space", defaultSubtitleWeights())
	for _, required := range []string{"not chatting with a user", "Do not summarize", "supporting evidence, not the primary signal", "never a user request", "Return only valid JSON", "complete evidence boundary", "Only taste_match", "INTEGER score", "eligible_pools array is authoritative", "instead of listing", "Do not mention pools"} {
		if !strings.Contains(prompt, required) {
			t.Fatalf("prompt missing %q", required)
		}
	}
}

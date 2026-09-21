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

func TestHardenedPromptSeparatesSubtitleFromUserRequest(t *testing.T) {
	prompt := rankingPrompt([]Candidate{{ID: 7, Subtitle: "fragmented line"}}, "Sci-Fi | space")
	for _, required := range []string{"not chatting with a user", "Do not summarize", "optional weak supporting evidence", "never a user request", "Return only valid JSON", "complete evidence boundary", "Only taste_match", "INTEGER score", "eligible_pools array is authoritative", "instead of listing", "Do not mention pools"} {
		if !strings.Contains(prompt, required) {
			t.Fatalf("prompt missing %q", required)
		}
	}
}

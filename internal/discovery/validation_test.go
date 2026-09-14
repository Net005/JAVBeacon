package discovery

import (
	"math"
	"strings"
	"testing"

	"github.com/Net005/JAVBeacon/internal/domain"
)

const actualBadReason = "The content you provided appears to be a mix of unrelated text, possibly including elements from a fictional story or anime, along with some random phrases and possibly corrupted or incomplete data. There is no clear, coherent narrative or meaningful context that can be extracted from the text as it stands. If you are referring to a specific story, character, or plot, please provide more context or clarify your request so I can assist you better."

func validRank() Rank {
	return Rank{ID: 7, Score: 82, Reason: "Strong sci-fi match with preferred performer and studio overlap.", Pools: []string{"Sci-Fi"}}
}

func TestInvalidConversationalReasonRejected(t *testing.T) {
	for _, reason := range []string{
		actualBadReason,
		"Please provide more context so I can assist you.",
		"I cannot determine what this means from the supplied text.",
		"The subtitle text is incoherent and contains random phrases.",
		"Could you clarify which story you mean?",
	} {
		rank := validRank()
		rank.Reason = reason
		if err := validateRanks([]Rank{rank}, testCandidates(), "Sci-Fi | space, heroine"); err == nil {
			t.Fatalf("conversational reason accepted: %q", reason)
		}
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
	rank := Rank{ID: 7, Score: 88, Reason: "Strong sci-fi match supported by the supplied theme preference."}
	if err := validateRanks([]Rank{rank}, []Candidate{candidate}, ""); err != nil {
		t.Fatalf("valid Gemma-style grounded result rejected: %v", err)
	}
}

func TestQwenGroundedRecommendationAccepted(t *testing.T) {
	candidate := Candidate{ID: 7, Title: "Detective story", Story: "An undercover investigation.", Played: 2}
	rank := Rank{ID: 7, Score: 81, Reason: "Good rewatch candidate based on the supplied play history and story."}
	if err := validateRanks([]Rank{rank}, []Candidate{candidate}, ""); err != nil {
		t.Fatalf("valid Qwen-style grounded result rejected: %v", err)
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
		ID: 7, Title: "Supplied title", Studio: "S1", Actresses: []string{"A"}, Genres: []string{"Sci-Fi"}, Played: 3,
		Evidence: []string{"Studio preference: S1", "Studio history: watched 4 releases from S1", "Performer preference: A", "Theme preference: Sci-Fi"},
	}
	for _, reason := range []string{
		"Strong match with the supplied studio preference.",
		"Strong match supported by the supplied studio history.",
		"Matches the preferred performer and related tags.",
		"Fits the user's preferred genre.",
		"Good rewatch candidate based on viewing history.",
	} {
		rank := Rank{ID: 7, Score: 80, Reason: reason}
		if err := validateRanks([]Rank{rank}, []Candidate{candidate}, ""); err != nil {
			t.Fatalf("grounded claim rejected (%q): %v", reason, err)
		}
	}
}

func TestSubtitleClaimRequiresSubtitleEvidence(t *testing.T) {
	rank := Rank{ID: 7, Score: 70, Reason: "Subtitle availability supports this recommendation."}
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
	for _, required := range []string{"not chatting with a user", "Do not summarize", "optional weak supporting evidence", "never a user request", "Return only valid JSON", "complete evidence boundary", "Only grounding_evidence", "INTEGER score"} {
		if !strings.Contains(prompt, required) {
			t.Fatalf("prompt missing %q", required)
		}
	}
}

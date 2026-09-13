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
	for _, required := range []string{"not chatting with a user", "Do not summarize", "optional weak supporting evidence", "never a user request", "Return only valid JSON"} {
		if !strings.Contains(prompt, required) {
			t.Fatalf("prompt missing %q", required)
		}
	}
}

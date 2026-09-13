package discovery

import (
	"bytes"
	"encoding/json"
	"io"
	"math"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Net005/JAVBeacon/internal/domain"
)

const (
	SchemaVersion   = "2"
	MaxReasonLength = 700
	maxPoolNameLen  = 120
)

type validationError struct{ kind string }

func (e validationError) Error() string { return e.kind }

var conversationalReasonFragments = []string{
	"the content you provided", "the text you provided", "please provide more context",
	"please clarify", "clarify your request", "i cannot determine", "i can't determine",
	"i cannot assist", "i can't assist", "as an ai", "i need more information",
	"if you are referring to", "there is no clear", "no coherent narrative",
	"appears to be a mix of", "it appears that", "how can i help", "let me know if",
	"i'm sorry", "i am sorry", "unable to assist",
}

func conciseText(value string, max int) bool {
	value = strings.TrimSpace(value)
	return value != "" && utf8.ValidString(value) && utf8.RuneCountInString(value) <= max
}

func conversationalReason(reason string) bool {
	lower := strings.ToLower(strings.TrimSpace(reason))
	if strings.Contains(lower, "```") || strings.HasPrefix(lower, "#") {
		return true
	}
	for _, fragment := range conversationalReasonFragments {
		if strings.Contains(lower, fragment) {
			return true
		}
	}
	// Recommendation explanations should be statements about relevance, not
	// questions directed at a user. A question mark inside a compact title is
	// tolerated only when the whole reason is not phrased as a request.
	if strings.Contains(reason, "?") {
		for _, lead := range []string{"can you", "could you", "would you", "do you", "what ", "which ", "why ", "how ", "please "} {
			if strings.Contains(lower, lead) {
				return true
			}
		}
	}
	// Reject long generic subtitle-quality commentary even if it avoids the
	// common assistant phrases above. Valid reasons describe match/relevance.
	qualityTerms := []string{"corrupt", "incoherent", "unrelated text", "random phrases", "incomplete data", "subtitle text", "subtitle lines"}
	relevanceTerms := []string{"match", "preference", "preferred", "history", "rewatch", "performer", "studio", "genre", "tag", "pool", "story", "recommend", "affinity", "overlap", "signal", " fit", "similar"}
	quality, relevance := false, false
	for _, term := range qualityTerms {
		quality = quality || strings.Contains(lower, term)
	}
	for _, term := range relevanceTerms {
		relevance = relevance || strings.Contains(lower, term)
	}
	return !relevance || quality
}

func poolNames(raw string) map[string]bool {
	result := map[string]bool{}
	for _, line := range strings.Split(raw, "\n") {
		name := strings.TrimSpace(strings.SplitN(line, "|", 2)[0])
		if name != "" {
			result[strings.ToLower(name)] = true
		}
	}
	return result
}

func validateRank(rank Rank, allowedIDs map[int64]bool, allowedPools map[string]bool) error {
	if allowedIDs != nil && !allowedIDs[rank.ID] {
		return validationError{"unknown candidate ID"}
	}
	if math.IsNaN(rank.Score) || math.IsInf(rank.Score, 0) || rank.Score < 0 || rank.Score > 100 {
		return validationError{"score outside accepted range"}
	}
	if !conciseText(rank.Reason, MaxReasonLength) {
		return validationError{"empty, invalid, or overly long reason"}
	}
	if conversationalReason(rank.Reason) {
		return validationError{"conversational/non-ranking reason"}
	}
	if len(rank.Pools) > 20 {
		return validationError{"too many pools"}
	}
	seenPools := map[string]bool{}
	for _, pool := range rank.Pools {
		key := strings.ToLower(strings.TrimSpace(pool))
		if !conciseText(pool, maxPoolNameLen) || strings.ContainsAny(pool, "\r\n`<>") || seenPools[key] {
			return validationError{"structurally invalid pool"}
		}
		if allowedPools != nil && !allowedPools[key] {
			return validationError{"unknown discovery pool"}
		}
		seenPools[key] = true
	}
	return nil
}

func validateRanks(ranks []Rank, candidates []Candidate, pools string) error {
	if len(ranks) == 0 {
		return validationError{"empty AI result"}
	}
	allowed := make(map[int64]bool, len(candidates))
	for _, candidate := range candidates {
		allowed[candidate.ID] = true
	}
	seen := map[int64]bool{}
	for _, rank := range ranks {
		if seen[rank.ID] {
			return validationError{"duplicate candidate ID"}
		}
		if err := validateRank(rank, allowed, poolNames(pools)); err != nil {
			return err
		}
		seen[rank.ID] = true
	}
	return nil
}

func parseRankingJSON(content string, candidates []Candidate, pools string) ([]Rank, error) {
	if strings.TrimSpace(content) == "" {
		return nil, validationError{"empty AI result"}
	}
	var envelope struct {
		Rankings []Rank `json:"rankings"`
	}
	decoder := json.NewDecoder(bytes.NewBufferString(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil {
		return nil, validationError{"invalid structured output"}
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, validationError{"unexpected trailing output"}
		}
		return nil, validationError{"invalid structured output"}
	}
	if err := validateRanks(envelope.Rankings, candidates, pools); err != nil {
		return nil, err
	}
	return envelope.Rankings, nil
}

// ValidateStoredRank applies the same semantic rules used before persistence.
// Candidate membership cannot be reconstructed for an old row, but its
// foreign-key release ID, score, reason and pools are validated.
func ValidateStoredRank(rank domain.DiscoveryAIRank, pools string) error {
	if rank.ReleaseID <= 0 {
		return validationError{"invalid release ID"}
	}
	return validateRank(Rank{ID: rank.ReleaseID, Score: rank.Score, Reason: rank.Reason, Pools: rank.Pools}, nil, poolNames(pools))
}

func TruncateUTF8(value string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= maxRunes {
		return value
	}
	return strings.TrimSpace(string(runes[:maxRunes]))
}

func MeaningfulSubtitleLine(line string) bool {
	line = strings.TrimSpace(line)
	if utf8.RuneCountInString(line) < 3 || strings.Contains(strings.ToLower(line), "http://") || strings.Contains(strings.ToLower(line), "https://") || strings.Contains(strings.ToLower(line), "www.") {
		return false
	}
	lower := strings.ToLower(line)
	for _, marker := range []string{"subtitle by", "subtitles by", "translated by", "translation by", "timing by", "encoded by"} {
		if strings.Contains(lower, marker) {
			return false
		}
	}
	for _, r := range line {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			return true
		}
	}
	return false
}

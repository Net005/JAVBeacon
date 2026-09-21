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
	SchemaVersion   = "5"
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
	if containsWord(lower, "pool", "pools") || strings.Contains(lower, "custom discovery pool") || strings.Contains(lower, "pool tags") ||
		(strings.HasPrefix(lower, "match: no ") && strings.Contains(lower, "present")) ||
		containsAny(lower, "performer preference:", "theme preference:", "studio preference:", "studio history:") {
		return true
	}
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
	if math.IsNaN(rank.Score) || math.IsInf(rank.Score, 0) || rank.Score < 0 || rank.Score > 100 || math.Trunc(rank.Score) != rank.Score {
		return validationError{"score outside accepted range"}
	}
	if !conciseText(rank.Reason, MaxReasonLength) {
		return validationError{"empty, invalid, or overly long reason"}
	}
	if conversationalReason(rank.Reason) {
		return validationError{"conversational/non-ranking reason"}
	}
	if !strings.HasPrefix(strings.TrimSpace(rank.Reason), "Match:") {
		return validationError{"recommendation reason must begin with Match:"}
	}
	wordCount := len(strings.Fields(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(rank.Reason), "Match:"))))
	if wordCount < 8 || wordCount > 36 {
		return validationError{"recommendation reason is not sufficiently descriptive"}
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

func containsAny(value string, terms ...string) bool {
	for _, term := range terms {
		if strings.Contains(value, term) {
			return true
		}
	}
	return false
}

func containsWord(value string, words ...string) bool {
	fields := strings.FieldsFunc(strings.ToLower(value), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) })
	for _, field := range fields {
		for _, word := range words {
			if field == word {
				return true
			}
		}
	}
	return false
}

func containsGroundedClaim(value string, subjects, signals []string) bool {
	value = strings.ToLower(value)
	for _, subject := range subjects {
		for _, signal := range signals {
			// Match the relationship in either natural word order instead of
			// treating two unrelated words anywhere in a sentence as a claim.
			if strings.Contains(value, signal+" "+subject) || strings.Contains(value, subject+" "+signal) ||
				strings.Contains(value, signal+" for "+subject) || strings.Contains(value, subject+" with "+signal) ||
				strings.Contains(value, signal+" with "+subject) || strings.Contains(value, subject+" of "+signal) ||
				strings.Contains(value, subject+"'s "+signal) {
				return true
			}
		}
	}
	return false
}

func groundedEvidence(candidate Candidate, subjects, signals []string) bool {
	for _, subject := range subjects {
		switch subject {
		case "performer", "actress":
			if len(candidate.Taste.PreferredPerformers) > 0 {
				return true
			}
		case "studio":
			if strings.TrimSpace(candidate.Taste.PreferredStudio) != "" {
				return true
			}
		case "theme", "genre", "tag":
			if len(candidate.Taste.PreferredThemes) > 0 || len(candidate.Taste.WatchedTextThemes) > 0 {
				return true
			}
		case "history", "play", "rewatch", "watch":
			if len(candidate.Taste.PreferredPerformers) > 0 || len(candidate.Taste.PreferredThemes) > 0 ||
				candidate.Taste.PreferredStudio != "" || candidate.Taste.PreferredLabel != "" || len(candidate.Taste.WatchedTextThemes) > 0 {
				return true
			}
		}
	}
	for _, evidence := range candidate.Evidence {
		lower := strings.ToLower(evidence)
		if containsAny(lower, subjects...) && containsAny(lower, signals...) {
			return true
		}
	}
	return false
}

// validateGrounding prevents a model from turning a present metadata value
// into an invented preference or history claim. Historical and preference
// language requires an explicit deterministic signal supplied with the same
// candidate; plain metadata claims only require that metadata to exist.
func validateGrounding(rank Rank, candidate Candidate) error {
	reason := strings.ToLower(rank.Reason)
	preferenceSignals := []string{"preference", "preferred", "affinity", "watched theme"}
	historySignals := []string{"history", "frequently watched", "previous play", "rewatch"}

	studioHistoryClaim := containsGroundedClaim(reason, []string{"studio", "studios"}, []string{"history", "frequently watched"})
	studioPreferenceClaim := containsGroundedClaim(reason, []string{"studio", "studios"}, []string{"preference", "preferred", "affinity"})
	if (studioHistoryClaim && !groundedEvidence(candidate, []string{"studio"}, historySignals)) ||
		(studioPreferenceClaim && !groundedEvidence(candidate, []string{"studio"}, preferenceSignals)) {
		return validationError{"unsupported studio preference/history claim"}
	}
	performerSubjects := []string{"performer", "performers", "actress", "actresses", "cast"}
	performerHistoryClaim := containsGroundedClaim(reason, performerSubjects, []string{"history", "frequently watched"})
	performerPreferenceClaim := containsGroundedClaim(reason, performerSubjects, []string{"preference", "preferred", "affinity"})
	if (performerHistoryClaim && !groundedEvidence(candidate, []string{"performer", "actress"}, historySignals)) ||
		(performerPreferenceClaim && !groundedEvidence(candidate, []string{"performer", "actress"}, preferenceSignals)) {
		return validationError{"unsupported performer preference/history claim"}
	}
	themePreferenceClaim := containsGroundedClaim(reason, []string{"genre", "genres", "theme", "themes", "tag", "tags"}, []string{"user preference", "user's preference", "preferred", "preference", "affinity"})
	if themePreferenceClaim &&
		!groundedEvidence(candidate, []string{"theme", "genre", "tag"}, preferenceSignals) {
		return validationError{"unsupported user preference claim"}
	}
	if containsAny(reason, "viewing history", "watch history", "play history", "previous play", "rewatch candidate") && candidate.Played <= 0 && candidate.Orgasms <= 0 &&
		!groundedEvidence(candidate, []string{"history", "play", "rewatch", "watch"}, historySignals) {
		return validationError{"unsupported viewing history claim"}
	}
	if containsAny(reason, "orgasm count", "orgasm history") && candidate.Orgasms <= 0 {
		return validationError{"unsupported orgasm history claim"}
	}
	if containsAny(reason, "play count") && candidate.Played <= 0 {
		return validationError{"unsupported play count claim"}
	}
	if containsWord(reason, "subtitle", "subtitles") && !candidate.SubtitleAvailable && strings.TrimSpace(candidate.Subtitle) == "" {
		return validationError{"unsupported subtitle claim"}
	}
	if containsWord(reason, "story", "stories") && strings.TrimSpace(candidate.Story) == "" {
		return validationError{"unsupported story claim"}
	}
	if strings.Contains(reason, "studio") && strings.TrimSpace(candidate.Studio) == "" {
		return validationError{"unsupported studio claim"}
	}
	if containsAny(reason, "performer", "actress", "cast") && len(candidate.Actresses) == 0 {
		return validationError{"unsupported performer claim"}
	}
	if containsAny(reason, "genre", " tag", "tags") && len(candidate.Genres) == 0 {
		return validationError{"unsupported tag/genre claim"}
	}
	return nil
}

func validateRanks(ranks []Rank, candidates []Candidate, pools string) error {
	if len(ranks) == 0 {
		return validationError{"empty AI result"}
	}
	if len(ranks) != len(candidates) {
		return validationError{"incomplete candidate coverage"}
	}
	allowed := make(map[int64]bool, len(candidates))
	byID := make(map[int64]Candidate, len(candidates))
	for _, candidate := range candidates {
		allowed[candidate.ID] = true
		byID[candidate.ID] = candidate
	}
	seen := map[int64]bool{}
	for _, rank := range ranks {
		if seen[rank.ID] {
			return validationError{"duplicate candidate ID"}
		}
		if err := validateRank(rank, allowed, poolNames(pools)); err != nil {
			return err
		}
		if err := validateGrounding(rank, byID[rank.ID]); err != nil {
			return err
		}
		candidate := byID[rank.ID]
		if candidate.EligiblePools != nil {
			eligible := make(map[string]bool, len(candidate.EligiblePools))
			for _, pool := range candidate.EligiblePools {
				eligible[strings.ToLower(strings.TrimSpace(pool))] = true
			}
			for _, pool := range rank.Pools {
				if !eligible[strings.ToLower(strings.TrimSpace(pool))] {
					return validationError{"pool unsupported by candidate evidence"}
				}
			}
		}
		seen[rank.ID] = true
	}
	for _, candidate := range candidates {
		if !seen[candidate.ID] {
			return validationError{"incomplete candidate coverage"}
		}
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

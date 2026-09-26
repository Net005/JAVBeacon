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
	SchemaVersion   = "7"
	MaxReasonLength = 700
	maxPoolNameLen  = 120
)

// validationError optionally carries detail: a short, truncated snippet of
// the actual model output that triggered the rejection. Earlier rejections
// were only ever logged as a bare kind string (e.g. "conversational/non-
// ranking reason"), which made repeat false-positive rejections impossible
// to root-cause from logs alone - this is surfaced through Error() so every
// existing "error", err log call site gets it for free.
type validationError struct {
	kind   string
	detail string
}

func (e validationError) Error() string {
	if e.detail == "" {
		return e.kind
	}
	return e.kind + ": " + e.detail
}

func reasonRejection(kind, reason string) validationError {
	return validationError{kind: kind, detail: TruncateUTF8(strings.TrimSpace(reason), 160)}
}

var conversationalReasonFragments = []string{
	"the content you provided", "the text you provided", "please provide more context",
	"please clarify", "clarify your request", "i cannot determine", "i can't determine",
	"i cannot assist", "i can't assist", "as an ai", "i need more information",
	"if you are referring to", "there is no clear", "no coherent narrative",
	"appears to be a mix of", "how can i help", "let me know if",
	"i'm sorry", "i am sorry", "unable to assist", "strong title match", "recommendation relevance",
}

func conciseText(value string, max int) bool {
	value = strings.TrimSpace(value)
	return value != "" && utf8.ValidString(value) && utf8.RuneCountInString(value) <= max
}

func conversationalReason(reason string) bool {
	lower := strings.ToLower(strings.TrimSpace(reason))
	// A bare "pool"/"pools" word check used to live here and rejected any
	// reason mentioning the word at all - including entirely legitimate
	// content, since JAV releases can literally be set at or tagged with a
	// "pool" (e.g. a swimming-pool scene). The specific phrases below
	// ("custom discovery pool", "pool tags", the "no ... present" prefix)
	// already catch genuine meta-commentary about the discovery-pool
	// *feature* without that false-positive risk.
	if strings.Contains(lower, "custom discovery pool") || strings.Contains(lower, "pool tags") ||
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
	// Reject data-quality commentary even if it avoids the common assistant
	// phrases above. Do not require a fixed ranking vocabulary here: natural
	// explanations can cite concrete titles, names and metadata without using
	// words such as "match", "preference" or "recommendation". Grounding,
	// length and structural validation are enforced separately.
	// "subtitle text"/"subtitle lines" were deliberately removed from this
	// list: the prompt explicitly allows subtitle excerpts as supporting
	// evidence, so a grounded reason that legitimately cites "subtitle
	// text" or "subtitle lines" (a phrasing smaller/local models produce
	// often, since it echoes the prompt's own vocabulary) must not be
	// rejected just for using those words. An actual quality complaint
	// about subtitles is still caught below via corrupt/incoherent/random
	// phrases/unrelated text/incomplete data.
	//
	// "corrupt" and "incoherent" are matched as whole words (containsWord),
	// not substrings: a plain strings.Contains(lower, "corrupt") also matches
	// "corruption" - an entirely ordinary JAV theme word (an "NTR/corruption
	// premise") that has nothing to do with data quality, and was a real
	// observed false-positive rejection.
	if containsWord(lower, "corrupt", "corrupted", "incoherent") {
		return true
	}
	for _, phrase := range []string{"unrelated text", "random phrases", "incomplete data"} {
		if strings.Contains(lower, phrase) {
			return true
		}
	}
	return false
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
		return validationError{kind: "unknown candidate ID"}
	}
	if math.IsNaN(rank.Score) || math.IsInf(rank.Score, 0) || rank.Score < 0 || rank.Score > 100 || math.Trunc(rank.Score) != rank.Score {
		return validationError{kind: "score outside accepted range"}
	}
	if !conciseText(rank.Reason, MaxReasonLength) {
		return reasonRejection("empty, invalid, or overly long reason", rank.Reason)
	}
	if conversationalReason(rank.Reason) {
		return reasonRejection("conversational/non-ranking reason", rank.Reason)
	}
	naturalReason := strings.TrimSpace(rank.Reason)
	// Accept the old prefix while schema-version-5 rows age out, but no longer
	// require or generate it. Explanations should read as ordinary prose.
	if strings.HasPrefix(strings.ToLower(naturalReason), "match:") {
		naturalReason = strings.TrimSpace(naturalReason[len("Match:"):])
	}
	wordCount := len(strings.Fields(naturalReason))
	if wordCount < 8 || wordCount > 36 {
		return reasonRejection("recommendation reason is not sufficiently descriptive", rank.Reason)
	}
	if len(rank.Pools) > 20 {
		return validationError{kind: "too many pools"}
	}
	seenPools := map[string]bool{}
	for _, pool := range rank.Pools {
		key := strings.ToLower(strings.TrimSpace(pool))
		if !conciseText(pool, maxPoolNameLen) || strings.ContainsAny(pool, "\r\n`<>") || seenPools[key] {
			return validationError{kind: "structurally invalid pool"}
		}
		if allowedPools != nil && !allowedPools[key] {
			return validationError{kind: "unknown discovery pool"}
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
		return reasonRejection("unsupported studio preference/history claim", rank.Reason)
	}
	performerSubjects := []string{"performer", "performers", "actress", "actresses", "cast"}
	performerHistoryClaim := containsGroundedClaim(reason, performerSubjects, []string{"history", "frequently watched"})
	performerPreferenceClaim := containsGroundedClaim(reason, performerSubjects, []string{"preference", "preferred", "affinity"})
	if (performerHistoryClaim && !groundedEvidence(candidate, []string{"performer", "actress"}, historySignals)) ||
		(performerPreferenceClaim && !groundedEvidence(candidate, []string{"performer", "actress"}, preferenceSignals)) {
		return reasonRejection("unsupported performer preference/history claim", rank.Reason)
	}
	themePreferenceClaim := containsGroundedClaim(reason, []string{"genre", "genres", "theme", "themes", "tag", "tags"}, []string{"user preference", "user's preference", "preferred", "preference", "affinity"})
	if themePreferenceClaim &&
		!groundedEvidence(candidate, []string{"theme", "genre", "tag"}, preferenceSignals) {
		return reasonRejection("unsupported user preference claim", rank.Reason)
	}
	if containsAny(reason, "viewing history", "watch history", "play history", "previous play", "rewatch candidate") && candidate.Played <= 0 && candidate.Orgasms <= 0 &&
		!groundedEvidence(candidate, []string{"history", "play", "rewatch", "watch"}, historySignals) {
		return reasonRejection("unsupported viewing history claim", rank.Reason)
	}
	if containsAny(reason, "orgasm count", "orgasm history") && candidate.Orgasms <= 0 {
		return reasonRejection("unsupported orgasm history claim", rank.Reason)
	}
	if containsAny(reason, "play count") && candidate.Played <= 0 {
		return reasonRejection("unsupported play count claim", rank.Reason)
	}
	if containsWord(reason, "subtitle", "subtitles") && !candidate.SubtitleAvailable && strings.TrimSpace(candidate.Subtitle) == "" {
		return reasonRejection("unsupported subtitle claim", rank.Reason)
	}
	if containsWord(reason, "story", "stories") && strings.TrimSpace(candidate.Story) == "" {
		return reasonRejection("unsupported story claim", rank.Reason)
	}
	if strings.Contains(reason, "studio") && strings.TrimSpace(candidate.Studio) == "" {
		return reasonRejection("unsupported studio claim", rank.Reason)
	}
	if containsAny(reason, "performer", "actress", "cast") && len(candidate.Actresses) == 0 {
		return reasonRejection("unsupported performer claim", rank.Reason)
	}
	if containsAny(reason, "genre", " tag", "tags") && len(candidate.Genres) == 0 {
		return reasonRejection("unsupported tag/genre claim", rank.Reason)
	}
	return nil
}

func validateRanks(ranks []Rank, candidates []Candidate, pools string) error {
	if len(ranks) == 0 {
		return validationError{kind: "empty AI result"}
	}
	if len(ranks) != len(candidates) {
		return validationError{kind: "incomplete candidate coverage"}
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
			return validationError{kind: "duplicate candidate ID"}
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
					return validationError{kind: "pool unsupported by candidate evidence"}
				}
			}
		}
		seen[rank.ID] = true
	}
	for _, candidate := range candidates {
		if !seen[candidate.ID] {
			return validationError{kind: "incomplete candidate coverage"}
		}
	}
	return nil
}

func parseRankingJSON(content string, candidates []Candidate, pools string) ([]Rank, error) {
	if strings.TrimSpace(content) == "" {
		return nil, validationError{kind: "empty AI result"}
	}
	var envelope struct {
		Rankings []Rank `json:"rankings"`
	}
	decoder := json.NewDecoder(bytes.NewBufferString(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil {
		return nil, validationError{kind: "invalid structured output"}
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, validationError{kind: "unexpected trailing output"}
		}
		return nil, validationError{kind: "invalid structured output"}
	}
	// Only structurally unusable output (wrong candidate set, missing or
	// duplicated IDs) is treated as a hard failure worth a repair retry.
	// Everything else - an over-eager preference claim, a borderline word
	// count, a stray markdown fence in one candidate's reason - is sanitized
	// per-candidate below instead: a handful of stubborn reason-text mistakes
	// should not discard every other candidate's genuinely good rank in the
	// same batch, and should not repeatedly sink the whole AI Discovery
	// request after a wasted repair attempt.
	if err := validateRankCoverage(envelope.Rankings, candidates); err != nil {
		return nil, err
	}
	sanitized, usable, firstErr := sanitizeRanks(envelope.Rankings, candidates, pools)
	if usable == 0 {
		// Every candidate in the batch needed sanitizing: the model did not
		// produce a single usable rank, which is the "genuinely broken
		// response" case a repair retry (or, for Ollama, provider fallback)
		// exists for - silently returning an all-fallback batch here would
		// mean a completely non-functional model response is still reported
		// as a successful ranking. This is also what keeps a single-candidate
		// batch's one bad rank retried rather than immediately papered over.
		if firstErr != nil {
			return nil, firstErr
		}
		return nil, validationError{kind: "invalid structured output"}
	}
	return sanitized, nil
}

// validateRankCoverage checks only that the response has exactly one ranking
// per supplied candidate ID, with no unknown or duplicate IDs. This is the
// one thing a repair retry can actually fix (the model needs to try again
// with the full candidate list); reason/score/pool content problems cannot
// be fixed more reliably by a second model call than by sanitizeRanks below,
// so they no longer trigger a retry.
func validateRankCoverage(ranks []Rank, candidates []Candidate) error {
	if len(ranks) == 0 {
		return validationError{kind: "empty AI result"}
	}
	if len(ranks) != len(candidates) {
		return validationError{kind: "incomplete candidate coverage"}
	}
	allowed := make(map[int64]bool, len(candidates))
	for _, candidate := range candidates {
		allowed[candidate.ID] = true
	}
	seen := map[int64]bool{}
	for _, rank := range ranks {
		if !allowed[rank.ID] {
			return validationError{kind: "unknown candidate ID"}
		}
		if seen[rank.ID] {
			return validationError{kind: "duplicate candidate ID"}
		}
		seen[rank.ID] = true
	}
	return nil
}

// sanitizeRanks returns exactly one valid Rank per candidate, plus how many
// of those ranks were the model's own usable output (as opposed to a
// substituted fallback) and the first validation error encountered, for the
// all-fallback case in parseRankingJSON. Any rank whose score, reason or
// pools fail validation is repaired in place rather than causing the whole
// batch to be discarded: an unsupported score is clamped to a neutral
// default, an invalid/ungrounded/conversational reason is replaced with a
// deterministic, always-grounded fallback, and pool names are filtered down
// to the ones that are actually valid and eligible for that candidate
// instead of rejecting the whole rank over one bad entry.
func sanitizeRanks(ranks []Rank, candidates []Candidate, pools string) ([]Rank, int, error) {
	byID := make(map[int64]Rank, len(ranks))
	for _, rank := range ranks {
		byID[rank.ID] = rank
	}
	allowedPools := poolNames(pools)
	result := make([]Rank, 0, len(candidates))
	usable := 0
	var firstErr error
	for _, candidate := range candidates {
		rank := byID[candidate.ID]
		rank.ID = candidate.ID
		sanitized, ok, err := sanitizeRank(rank, candidate, allowedPools)
		if ok {
			usable++
		} else if firstErr == nil {
			firstErr = err
		}
		result = append(result, sanitized)
	}
	return result, usable, firstErr
}

func sanitizeRank(rank Rank, candidate Candidate, allowedPools map[string]bool) (Rank, bool, error) {
	score := rank.Score
	scoreErr := error(nil)
	if math.IsNaN(score) || math.IsInf(score, 0) || score < 0 || score > 100 || math.Trunc(score) != score {
		scoreErr = validationError{kind: "score outside accepted range"}
		score = 50
	}
	reason := strings.TrimSpace(rank.Reason)
	if strings.HasPrefix(strings.ToLower(reason), "match:") {
		reason = strings.TrimSpace(reason[len("match:"):])
	}
	reasonErr := reasonValidationError(reason, candidate)
	sanitized := Rank{ID: candidate.ID, Score: score, Reason: reason, Pools: sanitizePools(rank.Pools, candidate, allowedPools)}
	if reasonErr != nil {
		sanitized.Reason = fallbackReason
	}
	if scoreErr == nil && reasonErr == nil {
		return sanitized, true, nil
	}
	if reasonErr != nil {
		return sanitized, false, reasonErr
	}
	return sanitized, false, scoreErr
}

func reasonValidationError(reason string, candidate Candidate) error {
	if !conciseText(reason, MaxReasonLength) {
		return reasonRejection("empty, invalid, or overly long reason", reason)
	}
	if conversationalReason(reason) {
		return reasonRejection("conversational/non-ranking reason", reason)
	}
	wordCount := len(strings.Fields(reason))
	if wordCount < 8 || wordCount > 36 {
		return reasonRejection("recommendation reason is not sufficiently descriptive", reason)
	}
	return validateGrounding(Rank{Reason: reason}, candidate)
}

// fallbackReason is used only when a model's own explanation could not be
// safely accepted after validation. It is deliberately generic and avoids
// every word validateGrounding treats as a claim trigger (story, subtitle,
// performer/actress/cast, studio, genre/tag, preference/history/play/
// orgasm), so it always passes validation itself regardless of candidate.
const fallbackReason = "This release was still included based on the ranking model's overall relevance signal for the batch."

func sanitizePools(raw []string, candidate Candidate, allowedPools map[string]bool) []string {
	if len(raw) == 0 {
		return nil
	}
	var eligible map[string]bool
	if candidate.EligiblePools != nil {
		eligible = make(map[string]bool, len(candidate.EligiblePools))
		for _, pool := range candidate.EligiblePools {
			eligible[strings.ToLower(strings.TrimSpace(pool))] = true
		}
	}
	seen := map[string]bool{}
	pools := make([]string, 0, len(raw))
	for _, pool := range raw {
		key := strings.ToLower(strings.TrimSpace(pool))
		if key == "" || seen[key] || !conciseText(pool, maxPoolNameLen) || strings.ContainsAny(pool, "\r\n`<>") {
			continue
		}
		if allowedPools != nil && !allowedPools[key] {
			continue
		}
		if eligible != nil && !eligible[key] {
			continue
		}
		seen[key] = true
		pools = append(pools, pool)
		if len(pools) >= 20 {
			break
		}
	}
	return pools
}

// ValidateStoredRank applies the same semantic rules used before persistence.
// Candidate membership cannot be reconstructed for an old row, but its
// foreign-key release ID, score, reason and pools are validated.
func ValidateStoredRank(rank domain.DiscoveryAIRank, pools string) error {
	if rank.ReleaseID <= 0 {
		return validationError{kind: "invalid release ID"}
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

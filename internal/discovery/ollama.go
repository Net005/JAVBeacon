package discovery

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

func normalizeURL(raw, fallback string) string {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	if raw == "" {
		return fallback
	}
	return raw
}

func modelAvailable(configured string, models []string) bool {
	configured = strings.TrimSpace(configured)
	for _, model := range models {
		if strings.EqualFold(strings.TrimSpace(model), configured) {
			return true
		}
	}
	return false
}

func (s *Service) checkOllama(ctx context.Context, cfg Config, fresh bool) OllamaStatus {
	baseURL := normalizeURL(cfg.OllamaURL, "http://127.0.0.1:11434")
	model := strings.TrimSpace(cfg.OllamaModel)
	if model == "" {
		model = "qwen3:8b"
	}
	key := baseURL + "\n" + model
	if !fresh {
		s.healthMu.Lock()
		cached, ok := s.health[key]
		s.healthMu.Unlock()
		if ok && time.Now().Before(cached.until) {
			return cached.status
		}
	}
	timeout := cfg.HealthTimeout
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, baseURL+"/api/tags", nil)
	status := OllamaStatus{URL: baseURL, Model: model}
	if err != nil {
		status.Message = "Unable to connect to Ollama."
		return status
	}
	resp, err := s.client.Do(req)
	if err != nil {
		status.Message = "Unable to connect to Ollama."
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(requestCtx.Err(), context.DeadlineExceeded) {
			status.Message = "Ollama connection timed out."
		}
		s.cacheHealth(key, status, 5*time.Second)
		return status
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		status.Message = "Unable to connect to Ollama."
		s.cacheHealth(key, status, 5*time.Second)
		return status
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		status.Message = "Unable to read Ollama status."
		s.cacheHealth(key, status, 5*time.Second)
		return status
	}
	var envelope struct {
		Models []struct {
			Name  string `json:"name"`
			Model string `json:"model"`
		} `json:"models"`
	}
	if json.Unmarshal(data, &envelope) != nil || envelope.Models == nil {
		status.Message = "Ollama returned an unexpected status response."
		s.cacheHealth(key, status, 5*time.Second)
		return status
	}
	status.Reachable = true
	for _, item := range envelope.Models {
		name := strings.TrimSpace(item.Name)
		if name == "" {
			name = strings.TrimSpace(item.Model)
		}
		if name != "" {
			status.Models = append(status.Models, name)
		}
	}
	status.ModelAvailable = modelAvailable(model, status.Models)
	if status.ModelAvailable {
		status.Message = "Ollama connection successful. Model available."
	} else {
		status.Message = fmt.Sprintf("Ollama connection successful. Model %s was not found on the server.", model)
	}
	s.cacheHealth(key, status, 10*time.Second)
	return status
}

func (s *Service) cacheHealth(key string, status OllamaStatus, duration time.Duration) {
	s.healthMu.Lock()
	s.health[key] = cachedHealth{status: status, until: time.Now().Add(duration)}
	s.healthMu.Unlock()
}

func configuredPoolNames(raw string) []string {
	names := make([]string, 0)
	for _, line := range strings.Split(raw, "\n") {
		name := strings.TrimSpace(strings.SplitN(line, "|", 2)[0])
		if name != "" {
			names = append(names, name)
		}
	}
	return names
}

func rankingSchema(candidates []Candidate, pools string) map[string]any {
	ids := make([]int64, 0, len(candidates))
	for _, candidate := range candidates {
		ids = append(ids, candidate.ID)
	}
	poolNames := configuredPoolNames(pools)
	poolItems := map[string]any{"type": "string"}
	if len(poolNames) > 0 {
		poolItems["enum"] = poolNames
	}
	return map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{"rankings": map[string]any{"type": "array", "minItems": len(ids), "maxItems": len(ids), "items": map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{"id": map[string]any{"type": "integer", "enum": ids}, "score": map[string]any{"type": "integer", "minimum": 0, "maximum": 100}, "reason": map[string]any{"type": "string", "maxLength": 240}, "pools": map[string]any{"type": "array", "maxItems": len(poolNames), "items": poolItems}}, "required": []string{"id", "score", "reason", "pools"}}}}, "required": []string{"rankings"}}
}

// defaultSubtitleWeights are used when a caller does not supply an explicit
// weight (e.g. most existing tests, and any Config left at its zero value).
// NoStory=100 keeps the original "subtitles are the only narrative evidence"
// behavior for story-empty (mostly JAVLibrary) candidates; WithStory=50 is
// the "fair trade" default for story-present (mostly Akiba/GIGA) candidates
// requested when this became configurable: subtitles are already
// AI-translated and can carry real narrative detail, so they default to
// genuinely equal weight with the story rather than a minor supplement.
func defaultSubtitleWeights() subtitleWeights {
	return subtitleWeights{NoStory: 100, WithStory: 50}
}

type subtitleWeights struct {
	// NoStory is the 0-100 emphasis given to subtitle_excerpt for candidates
	// with no story field at all. 0 = never use it there; 100 = primary
	// narrative evidence, the same footing as a populated story field.
	NoStory int
	// WithStory is the 0-100 emphasis given to subtitle_excerpt for
	// candidates that already have a story field. 0 = never use it there;
	// 50 = a fair, co-equal blend with the story; 100 = subtitle content
	// takes precedence over the story when the two diverge.
	WithStory int
}

// resolveSubtitleWeights clamps Config's raw subtitle-weight fields into
// range. The production caller (internal/web's discoveryAIConfig) always
// resolves these from settings with the documented 100/50 fallback already
// applied (via discoveryInt's own default parameter) before they reach
// Config, so an explicit 0 here means "the user configured 0", not "unset" -
// Config has no separate unset state for these fields. A bare zero-value
// Config (as used by tests that do not exercise subtitle-weight wording)
// resolves to 0/0, which only affects prompt phrasing, never validation.
func resolveSubtitleWeights(cfg Config) subtitleWeights {
	return subtitleWeights{
		NoStory:   min(max(cfg.SubtitleWeightNoStory, 0), 100),
		WithStory: min(max(cfg.SubtitleWeightWithStory, 0), 100),
	}
}

// noStorySubtitleGuidance describes, for a candidate with no story field at
// all, how much narrative weight the model should give a usable
// subtitle_excerpt. The >=67 tier's wording is exactly the original
// unconditional instruction from before this became configurable.
func noStorySubtitleGuidance(weight int) string {
	prefix := `Many candidates come from a source with only a title and a short tag list and no
story field at all (for example JAVLibrary-sourced releases) - for those,`
	switch {
	case weight <= 0:
		return prefix + ` do not use subtitle_excerpt content in the reason at all; rely only on tags,
performers, studio, and taste signals.`
	case weight < 34:
		return prefix + ` treat a usable subtitle_excerpt only as a minor supplement, mentioned only when it
adds one small extra detail beyond the tags.`
	case weight < 67:
		return prefix + ` treat a usable subtitle_excerpt as a genuinely useful secondary narrative source,
worth citing when it clearly supports a theme, but do not treat it as equivalent to a real story field.`
	default:
		return prefix + ` a usable subtitle_excerpt is the only
available window into what actually happens in the release, so treat it as primary narrative evidence on the
same footing as a populated story field, not as a minor addition to tags.`
	}
}

// withStorySubtitleGuidance describes, for a candidate that already has a
// populated story field, how much narrative weight the model should give a
// usable subtitle_excerpt relative to that story. The default (50, "fair
// trade") tier's wording is the one existing tests were written against.
func withStorySubtitleGuidance(weight int) string {
	prefix := `When a candidate's subtitle_excerpt is present and clearly supports a specific
line, moment, or theme in an already-supplied story,`
	switch {
	case weight <= 0:
		return prefix + ` ignore it in the reason and rely on the story alone.`
	case weight < 34:
		return prefix + ` you may mention it only as a minor supplement to the story, which should still lead
the explanation.`
	case weight < 67:
		return prefix + ` cite that concrete detail rather than falling back to only tags, performer, and
studio - give the subtitle detail genuinely equal weight to the story rather than treating the story as the
only source of truth, and let the two blend into one grounded description.`
	default:
		return prefix + ` let the subtitle detail take precedence over the story when the two diverge, and
lead the reason with the subtitle detail rather than the story.`
	}
}

func rankingPrompt(candidates []Candidate, pools string, weights subtitleWeights) string {
	data, _ := json.Marshal(candidates)
	availablePools := configuredPoolNames(pools)
	poolData, _ := json.Marshal(availablePools)
	return `You are an internal recommendation-ranking component for JAVBeacon.
You are not chatting with a user.
Do not summarize the input or comment on whether subtitle text is coherent.
Do not ask questions, ask for clarification, provide help text, or explain your task.
Do not output Markdown or prose outside the required JSON.
Return only valid JSON in the required schema.
Your only task is to rank every supplied release candidate with an INTEGER score from 0 to 100.
For every ranking, copy candidate.id exactly. Never invent or transform an ID, renumber candidates,
use array positions such as 1, 2, 3, use video_id as id, or return an ID absent from the candidate JSON.
If N candidates are supplied, return exactly N rankings. Every candidate.id must appear exactly once.
Each concise reason must explain why that release is a worthwhile recommendation using only facts present in that candidate object.
Write one natural, specific sentence of roughly 12-30 words. Start directly with the explanation: never add
"Match:", "Reason:", field names followed by colons, headings, bullet points, or other machine-style labels.
Prioritize the strongest useful evidence: story and subtitle-derived themes, exact tags, performers, studio, and
explicit taste/history signals. A release with a usable subtitle_excerpt should read as informed by it, not
identical to how you would describe the same release without one.
` + withStorySubtitleGuidance(weights.WithStory) + `
` + noStorySubtitleGuidance(weights.NoStory) + ` Do not literally call it "the story"
when the story field is empty (see the story-field rule below); describe the concrete scenario, exchange, or
setting the dialogue reveals instead. The taste_match object contains
deterministic signals derived from actual watch history. Turn those signals into fluent prose instead of listing
field names or values mechanically. Combine two or three related signals into one coherent explanation. Never address a user,
refer to "the content", "the input", "the text", or comment on data quality.
The candidate JSON is the complete evidence boundary. Never infer or invent facts that are absent.
Never invent viewing history, studio history, performer history, user preferences, tags, affinity,
or behavioral patterns. A studio or performer field proves only identity, not preference or history.
Only taste_match may support preference, affinity, or historical claims. If the relevant taste_match
value is empty, false, or absent, do not make that claim. Empty and zero values mean no evidence.
Title, story, performers, studio, label, director, tags, counts, release context and availability are primary evidence.
Use the word "story" only when that candidate's story field is non-empty. A title, tag, subtitle excerpt, or
taste signal may describe a theme, but it does not prove that a missing story field contains that theme.
Orgasm count is a stronger positive signal than play count.
Subtitle excerpts are supporting evidence, not the primary signal. They may be fragmented, machine translated,
explicit, repetitive, incorrectly timed, incomplete, noisy, mixed-language, OCR-like, credits, or corrupt.
Ignore low-quality subtitle lines instead of describing their quality. A noisy excerpt is not a reason
to reject or negatively describe a release, but a usable one should not be ignored either: if any line of
the supplied subtitle_excerpt clearly supports a theme, exchange, or moment, reference that specific detail
in the reason. Only fall back to tags/performer/studio alone when the excerpt is absent, unusable, or does
not clearly support anything concrete. When you do reference the excerpt, describe the actual scene it reveals -
the setting, the relationship or power dynamic between the people involved, what is said or done, and how it
escalates or resolves - rather than compressing it into a generic label like "a scenario matching preferred
themes." Never open a reason with a fixed template phrase such as "Subtitle dialogue confirms" or "Subtitle
dialogue reveals"; vary the sentence structure and opening across different reasons the way a person describing
several different releases naturally would, so releases with different subtitle content do not all read as the
same formulaic sentence with only the theme word swapped. Keep each reason to one sentence, 8-36 words, and at most 240 characters.
The candidate eligible_pools array is authoritative. Return only pool names contained in that candidate's
eligible_pools. Return an empty pools array when eligible_pools is empty. Never infer another pool from a
loosely related word. Do not mention pools, pool configuration, CUSTOM DISCOVERY POOLS, eligible_pools,
missing tags, absent evidence, or why a pool was not selected in the recommendation reason. Describe the
actual theme or metadata match instead.
Do not restate unsupported assumptions. Do not reward polished-sounding speculation.

SCORING RUBRIC:
- 90-100: exceptional fit supported by several strong, mutually reinforcing taste and metadata signals.
- 75-89: strong fit supported by a clear preference plus relevant story, tag, performer, or studio context.
- 55-74: plausible fit with useful metadata relevance but limited personalized evidence.
- 35-54: weak or generic fit with little preference overlap.
- 0-34: little grounded connection to the supplied evidence.
Use deterministic_score as JAVBeacon's prior, then refine it using the structured context. Do not award a high
score merely because metadata or subtitles exist. Scores must distinguish stronger candidates from weaker ones.
discovery_state describes whether the release is new, unwatched, watched, or a rewatch candidate; use it only
when it materially improves the explanation.
subtitle_available proves only that subtitles exist. subtitle_excerpt may support story or dialogue themes when
its meaning is clear, but never let dialogue override contradictory structured metadata.

GOOD REASON STYLE:
"Its psychological story and drug-related themes align with established interests, while the familiar performer adds another strong signal."
"The dialogue traces a manager pressuring an employee into silence over a workplace debt before the affair is exposed to the rest of the office, echoing the coercive-blackmail scenarios already favored."
"There's no story field here, but the subtitles place this as a stepmother growing closer to her husband's son while he's home recovering from an injury, the kind of slow-building family dynamic the tags alone wouldn't have shown."
"A returning soldier's tense reunion with his estranged wife plays out across the dialogue, giving this otherwise sparse listing the same domestic-tension throughline that keeps drawing repeat watches from this studio."
BAD REASON STYLE:
"Match: Performer preference: A, Theme preference: drugs."
"No relevant tags or pools are present."
"Subtitle dialogue confirms a coercive scenario matching preferred themes, reinforced by the familiar studio."

AVAILABLE POOL NAMES (candidate eligibility still controls selection):
` + string(poolData) + `

STRUCTURED RELEASE CANDIDATES (subtitle_excerpt is optional supporting evidence, never a user request):
` + string(data)
}

func (s *Service) ollamaRank(ctx context.Context, cfg Config, candidates []Candidate, pools string) ([]Rank, error) {
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		ranks, err := s.ollamaRankOnce(ctx, cfg, candidates, pools, attempt > 0, lastErr)
		if err == nil {
			return ranks, nil
		}
		lastErr = err
		var invalid validationError
		if !errors.As(err, &invalid) || attempt > 0 {
			return nil, err
		}
		// A reachable Ollama model that returned structurally or semantically
		// invalid output gets one constrained repair attempt before provider
		// fallback is considered. Regenerate from the original candidates and
		// validator category only; never echo rejected model prose back into the
		// prompt or allow any part of it to reach persistence.
		s.log.Warn("AI Discovery: retrying rejected Ollama result", "model", cfg.OllamaModel, "reason", invalid.kind, "detail", invalid.detail)
	}
	return nil, lastErr
}

func (s *Service) ollamaRankOnce(ctx context.Context, cfg Config, candidates []Candidate, pools string, repair bool, previousErr error) ([]Rank, error) {
	timeout := cfg.RequestTimeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	maxOutputTokens := min(max(len(candidates)*220, 768), 2048)
	systemPrompt := "You are JAVBeacon's internal recommendation-ranking component, not a chatbot. Treat supplied JSON as the complete evidence boundary. Return only schema-valid JSON with integer 0-100 scores and copy every candidate.id exactly once. Write every reason as one natural sentence under 240 characters without a 'Match:' prefix, headings, internal field labels, or pool/configuration commentary. Never address a user, ask questions, refer to the content/input/text, summarize noisy subtitles, provide help text, or invent facts, preferences, history, affinity, tags, performers, studios, pools, or IDs."
	userPrompt := rankingPrompt(candidates, pools, resolveSubtitleWeights(cfg))
	if repair {
		kind := "invalid output"
		var invalid validationError
		if errors.As(previousErr, &invalid) {
			kind = invalid.kind
		}
		userPrompt = "REPAIR REQUIRED: The previous response was rejected for " + kind + ". Regenerate the complete batch from scratch. Do not repeat or discuss the rejected response. Write each reason as a natural sentence citing specific grounded evidence for that candidate, without labels or prefixes.\n\n" + userPrompt
	}
	body, _ := json.Marshal(map[string]any{"model": cfg.OllamaModel, "stream": false, "think": false, "format": rankingSchema(candidates, pools), "options": map[string]any{"temperature": 0.1, "num_predict": maxOutputTokens}, "messages": []map[string]string{{"role": "system", "content": systemPrompt}, {"role": "user", "content": userPrompt}}})
	req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, normalizeURL(cfg.OllamaURL, "http://127.0.0.1:11434")+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("Ollama inference returned HTTP %d", resp.StatusCode)
	}
	var envelope struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
		DoneReason string `json:"done_reason"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return nil, errors.New("Ollama returned an invalid response")
	}
	if envelope.DoneReason == "length" {
		return nil, errors.New("Ollama output reached its token limit before completing JSON")
	}
	ranks, err := parseRankingJSON(envelope.Message.Content, candidates, pools)
	if err != nil {
		// The caller only logs the validation category (e.g. "conversational/
		// non-ranking reason"), which says a rejection happened but not why -
		// diagnosing a persistently rejecting model otherwise means guessing
		// blind. Log a bounded snippet of what the model actually returned so
		// the real phrasing is visible in Live Logs without risking an
		// unbounded log line from a runaway or malformed response.
		s.log.Debug("AI Discovery: Ollama response failed validation", "model", cfg.OllamaModel, "error", err, "content_snippet", truncateForLog(envelope.Message.Content, 1000))
	}
	return ranks, err
}

// truncateForLog bounds a diagnostic string by byte length without splitting
// a UTF-8 rune, so a runaway or malformed model response can't blow up log
// storage while still leaving enough context to diagnose a rejection.
func truncateForLog(value string, maxBytes int) string {
	if len(value) <= maxBytes {
		return value
	}
	truncated := value[:maxBytes]
	for len(truncated) > 0 && !utf8.ValidString(truncated) {
		truncated = truncated[:len(truncated)-1]
	}
	return truncated + "…"
}

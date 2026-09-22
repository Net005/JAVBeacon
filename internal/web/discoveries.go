package web

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	aidiscovery "github.com/Net005/JAVBeacon/internal/discovery"
	"github.com/Net005/JAVBeacon/internal/domain"
)

type discoveryItem struct {
	domain.Release
	Score       float64  `json:"discovery_score"`
	Category    string   `json:"discovery_category"`
	Reasons     []string `json:"discovery_reasons"`
	Pools       []string `json:"discovery_pools,omitempty"`
	HasSubtitle bool     `json:"has_subtitle"`
	AIEnhanced  bool     `json:"ai_enhanced"`
	AIText      string   `json:"ai_text,omitempty"`
	// SubtitleUsed is true only when subtitle dialogue text actually made it
	// into the AI prompt payload for this release, unlike HasSubtitle which
	// only means a sidecar subtitle file exists. discoveryAIBatches rations a
	// shared character budget across a batch, so an eligible release can
	// still receive no subtitle excerpt at all if the budget ran out first.
	SubtitleUsed bool `json:"subtitle_used"`
}

type affinityProfile struct {
	actress map[string]float64
	genre   map[string]float64
	studio  map[string]float64
	label   map[string]float64
}

// archivedAffinityReleases overlays the authoritative Stash playback/O history
// counters and event timestamps onto release metadata. Release-row playback
// fields remain a fallback for installations which have not built an archive.
func archivedAffinityReleases(ctx context.Context, st any, releases []domain.Release) ([]domain.Release, error) {
	reader, ok := st.(interface {
		StashHistoryExport(context.Context) (domain.StashHistoryExport, error)
	})
	if !ok {
		return releases, nil
	}
	archive, err := reader.StashHistoryExport(ctx)
	if err != nil {
		return nil, err
	}
	type activity struct {
		plays, orgasms       int
		lastPlay, lastOrgasm time.Time
	}
	byRelease := map[int64]*activity{}
	sceneRelease := make(map[string]int64, len(archive.Scenes))
	for _, scene := range archive.Scenes {
		if scene.ReleaseID <= 0 {
			continue
		}
		sceneRelease[scene.StashSceneID] = scene.ReleaseID
		a := byRelease[scene.ReleaseID]
		if a == nil {
			a = &activity{}
			byRelease[scene.ReleaseID] = a
		}
		a.plays += scene.PlayCount
		a.orgasms += scene.OrgasmCount
	}
	for _, event := range archive.Events {
		a := byRelease[sceneRelease[event.StashSceneID]]
		if a == nil {
			continue
		}
		if event.Type == "play" && event.OccurredAt.After(a.lastPlay) {
			a.lastPlay = event.OccurredAt
		}
		if event.Type == "orgasm" && event.OccurredAt.After(a.lastOrgasm) {
			a.lastOrgasm = event.OccurredAt
		}
	}
	for i := range releases {
		if a := byRelease[releases[i].ID]; a != nil {
			releases[i].PlayCount, releases[i].OCounter = a.plays, a.orgasms
			if !a.lastPlay.IsZero() {
				releases[i].LastPlayedAt = a.lastPlay.UTC().Format(time.RFC3339)
			}
			if !a.lastOrgasm.IsZero() {
				releases[i].LastOCountAt = a.lastOrgasm.UTC().Format(time.RFC3339)
			}
		}
	}
	return releases, nil
}

type openAIRank struct {
	ID     int64    `json:"id"`
	Score  float64  `json:"score"`
	Reason string   `json:"reason"`
	Pools  []string `json:"pools"`
	// SubtitleUsed mirrors domain.DiscoveryAIRank.SubtitleUsed: whether
	// subtitle dialogue text was actually part of the payload sent to the AI
	// for this release, not merely whether a subtitle file exists.
	SubtitleUsed bool `json:"subtitle_used"`
}

type discoveryAICandidate struct {
	ID                int64                    `json:"id"`
	VideoID           string                   `json:"video_id"`
	Title             string                   `json:"title"`
	Story             string                   `json:"story"`
	Actresses         []string                 `json:"actresses"`
	Genres            []string                 `json:"genres"`
	Studio            string                   `json:"studio"`
	Label             string                   `json:"label"`
	Director          string                   `json:"director"`
	ReleaseDate       string                   `json:"release_date"`
	Duration          string                   `json:"duration"`
	Local             bool                     `json:"local"`
	Played            int                      `json:"play_count"`
	Orgasms           int                      `json:"orgasm_count"`
	BaselineScore     float64                  `json:"deterministic_score"`
	DiscoveryState    string                   `json:"discovery_state"`
	SubtitleAvailable bool                     `json:"subtitle_available"`
	Evidence          []string                 `json:"-"`
	Taste             aidiscovery.TasteSignals `json:"taste_match"`
	EligiblePools     []string                 `json:"eligible_pools"`
	Subtitle          string                   `json:"subtitle_excerpt,omitempty"`
}

func (s *Server) testDiscoveryOpenAI(w http.ResponseWriter, r *http.Request) {
	settings, _ := s.store.Settings(r.Context())
	var input struct {
		APIKey        string `json:"api_key"`
		BaseURL       string `json:"base_url"`
		Model         string `json:"model"`
		TimeoutSecond int    `json:"timeout_seconds"`
	}
	if !s.decode(w, r, &input) {
		return
	}
	if strings.TrimSpace(input.APIKey) != "" && input.APIKey != maskedSecret {
		settings["discoveries_openai_api_key"] = input.APIKey
	}
	if strings.TrimSpace(input.BaseURL) != "" {
		settings["discoveries_openai_base_url"] = input.BaseURL
	}
	if strings.TrimSpace(input.Model) != "" {
		settings["discoveries_openai_model"] = input.Model
	}
	if input.TimeoutSecond > 0 {
		settings["discoveries_openai_timeout_seconds"] = strconv.Itoa(input.TimeoutSecond)
	}
	elapsed, err := s.discoveryAI.TestOpenAI(r.Context(), discoveryAIConfig(settings))
	if err != nil {
		s.problem(w, http.StatusBadGateway, err.Error())
		return
	}
	s.json(w, http.StatusOK, map[string]any{"ok": true, "model": settings["discoveries_openai_model"], "elapsed_ms": elapsed.Milliseconds()})
}

type discoveryOpenAIEstimate struct {
	Candidates       int     `json:"candidates"`
	Batches          int     `json:"batches"`
	EstimatedInput   int64   `json:"estimated_input_tokens"`
	MaximumInput     int64   `json:"maximum_input_tokens"`
	EstimatedOutput  int64   `json:"estimated_output_tokens"`
	MaximumOutput    int64   `json:"maximum_output_tokens"`
	EstimatedCostUSD float64 `json:"estimated_cost_usd"`
	MaximumCostUSD   float64 `json:"maximum_cost_usd"`
}

func estimateDiscoveryOpenAI(model string, candidates, batchSize, maxInputChars int, includeSubtitles bool) discoveryOpenAIEstimate {
	candidates = max(candidates, 0)
	batchSize = min(max(batchSize, 1), 5)
	maxInputChars = min(max(maxInputChars, 20000), 60000)
	batches := 0
	if candidates > 0 {
		batches = (candidates + batchSize - 1) / batchSize
	}
	maximumInput := int64(math.Ceil(float64(batches*maxInputChars) / 4))
	// A concise five-ranking JSON response is normally much smaller than its
	// output allowance. Sixty-five percent input occupancy and 100 output
	// tokens per candidate provide a useful planning estimate; maxima mirror
	// the actual per-request caps and form a conservative budget ceiling.
	inputOccupancy := 0.65
	if !includeSubtitles {
		inputOccupancy = 0.40
	}
	estimatedInput := int64(math.Ceil(float64(maximumInput) * inputOccupancy))
	estimatedOutput := int64(candidates * 100)
	maximumOutput := int64(batches * min(max(batchSize*160, 2048), 32768))
	return discoveryOpenAIEstimate{
		Candidates: candidates, Batches: batches,
		EstimatedInput: estimatedInput, MaximumInput: maximumInput,
		EstimatedOutput: estimatedOutput, MaximumOutput: maximumOutput,
		EstimatedCostUSD: discoveryOpenAICostUSD(model, estimatedInput, estimatedOutput),
		MaximumCostUSD:   discoveryOpenAICostUSD(model, maximumInput, maximumOutput),
	}
}

func (s *Server) estimateDiscoveryOpenAI(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Model            string `json:"model"`
		CandidateLimit   int    `json:"candidate_limit"`
		BatchSize        int    `json:"batch_size"`
		MaxInputChars    int    `json:"max_input_chars"`
		IncludeSubtitles bool   `json:"include_subtitles"`
	}
	if !s.decode(w, r, &input) {
		return
	}
	settings, _ := s.store.Settings(r.Context())
	model := strings.TrimSpace(input.Model)
	if model == "" {
		model = strings.TrimSpace(settings["discoveries_openai_model"])
	}
	if model == "" {
		model = "gpt-5-mini"
	}
	if input.CandidateLimit <= 0 {
		input.CandidateLimit = discoveryInt(settings, "discoveries_openai_candidate_limit", 150)
	}
	if input.BatchSize <= 0 {
		input.BatchSize = discoveryInt(settings, "discoveries_openai_batch_size", 5)
	}
	if input.MaxInputChars <= 0 {
		input.MaxInputChars = discoveryInt(settings, "discoveries_openai_max_input_chars", 50000)
	}
	total, err := s.store.ReleasesCount(r.Context(), domain.ReleaseFilter{ShowNonPreferred: true})
	if err != nil {
		s.problem(w, http.StatusInternalServerError, err.Error())
		return
	}
	runCandidates := min(max(input.CandidateLimit, 10), min(total, 1000))
	_, pricingKnown := discoveryOpenAIRates(model)
	s.json(w, http.StatusOK, map[string]any{
		"dry_run": true, "openai_called": false, "model": model, "pricing_known": pricingKnown,
		"include_subtitles": input.IncludeSubtitles,
		"run":               estimateDiscoveryOpenAI(model, runCandidates, input.BatchSize, input.MaxInputChars, input.IncludeSubtitles),
		"full_library":      estimateDiscoveryOpenAI(model, total, input.BatchSize, input.MaxInputChars, input.IncludeSubtitles),
	})
}

func (s *Server) testDiscoveryOllama(w http.ResponseWriter, r *http.Request) {
	settings, _ := s.store.Settings(r.Context())
	var input struct {
		URL                  string `json:"url"`
		Model                string `json:"model"`
		HealthTimeoutSeconds int    `json:"health_timeout_seconds"`
	}
	if !s.decode(w, r, &input) {
		return
	}
	if strings.TrimSpace(input.URL) != "" {
		settings["discoveries_ollama_url"] = input.URL
	}
	if strings.TrimSpace(input.Model) != "" {
		settings["discoveries_ollama_model"] = input.Model
	}
	if input.HealthTimeoutSeconds > 0 {
		settings["discoveries_ollama_health_timeout_seconds"] = strconv.Itoa(input.HealthTimeoutSeconds)
	}
	status := s.discoveryAI.CheckOllama(r.Context(), discoveryAIConfig(settings), true)
	s.json(w, http.StatusOK, status)
}

func (s *Server) clearDiscoveryAIRankings(w http.ResponseWriter, r *http.Request) {
	discoveryAIStatus.Lock()
	running := discoveryAIStatus.Running
	discoveryAIStatus.Unlock()
	if running {
		s.problem(w, http.StatusConflict, "AI enrichment is currently running; wait for it to finish before clearing recommendations")
		return
	}
	removed, err := s.store.ClearDiscoveryAIRanks(r.Context())
	if err != nil {
		s.problem(w, http.StatusInternalServerError, "clear AI recommendations: "+err.Error())
		return
	}
	discoveryRankCache.Lock()
	discoveryRankCache.entries = map[[32]byte]discoveryRankCacheEntry{}
	discoveryRankCache.Unlock()
	s.log.Info("AI Discovery recommendations cleared", "removed", removed)
	s.json(w, http.StatusOK, map[string]any{"removed": removed})
}

var discoveryRankCache = struct {
	sync.Mutex
	entries map[[32]byte]discoveryRankCacheEntry
}{entries: map[[32]byte]discoveryRankCacheEntry{}}

var discoveryAIStatus = struct {
	sync.RWMutex
	Running           bool
	Completed         int
	Total             int
	Batch             int
	Batches           int
	Current           int
	Error             string
	StartedAt         time.Time
	BatchStartedAt    time.Time
	LastBatchSeconds  float64
	CurrentItems      []string
	StartingCompleted int
	Provider          string
	Model             string
	InputTokens       int64
	OutputTokens      int64
	EstimatedCostUSD  float64
}{}

var discoveryResultCache = struct {
	sync.RWMutex
	key     [32]byte
	created time.Time
	items   []discoveryItem
	mode    string
	pools   []string
}{}

var discoverySubtitleCache = struct {
	sync.RWMutex
	created      time.Time
	availability map[int64]bool
	checked      map[int64]bool
}{}

var discoveryAffinityCache = struct {
	sync.RWMutex
	created time.Time
	key     [32]byte
	profile affinityProfile
}{}

type discoveryRankCacheEntry struct {
	created time.Time
	ranks   []openAIRank
}

func applyOpenAIRanks(items []discoveryItem, ranks []openAIRank) []discoveryItem {
	byID := make(map[int64]openAIRank, len(ranks))
	for _, rank := range ranks {
		byID[rank.ID] = rank
	}
	for i := range items {
		if rank, ok := byID[items[i].ID]; ok {
			items[i].AIEnhanced = true
			items[i].SubtitleUsed = rank.SubtitleUsed
			items[i].AIText = strings.TrimSpace(rank.Reason)
			items[i].Score = math.Round((items[i].Score*.35+rank.Score*.65)*10) / 10
			items[i].Pools = append(items[i].Pools, rank.Pools...)
			if strings.TrimSpace(rank.Reason) != "" {
				// The AI sentence already combines the strongest signals. Appending
				// labelled internal evidence makes the card dense and repetitive.
				items[i].Reasons = []string{rank.Reason}
			}
		}
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].Score > items[j].Score })
	return items
}

func cleanedSubtitleExcerpt(release domain.Release, maxChars int) string {
	if maxChars <= 0 {
		return ""
	}
	var out strings.Builder
	written := 0
	seen := map[string]bool{}
	for _, path := range subtitleFiles(release) {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.Contains(line, "-->") {
				continue
			}
			if _, err := strconv.Atoi(line); err == nil {
				continue
			}
			line = strings.NewReplacer("<i>", "", "</i>", "", "<b>", "", "</b>", "", "{\\i1}", "", "{\\i0}", "").Replace(line)
			line = strings.TrimSpace(line)
			key := strings.ToLower(line)
			if !aidiscovery.MeaningfulSubtitleLine(line) || seen[key] {
				continue
			}
			seen[key] = true
			lineRunes := utf8.RuneCountInString(line)
			if written+lineRunes+1 > maxChars {
				remaining := maxChars - written
				if remaining > 0 {
					out.WriteString(aidiscovery.TruncateUTF8(line, remaining))
				}
				return out.String()
			}
			out.WriteString(line)
			out.WriteByte('\n')
			written += lineRunes + 1
		}
	}
	return out.String()
}

func discoveryTasteSignals(reasons []string) aidiscovery.TasteSignals {
	signals := aidiscovery.TasteSignals{PreferredPerformers: []string{}, PreferredThemes: []string{}, WatchedTextThemes: []string{}}
	for _, reason := range reasons {
		reason = strings.TrimSpace(reason)
		switch {
		case strings.HasPrefix(reason, "Performer preference: "):
			signals.PreferredPerformers = append(signals.PreferredPerformers, strings.TrimSpace(strings.TrimPrefix(reason, "Performer preference: ")))
		case strings.HasPrefix(reason, "Theme preference: "):
			signals.PreferredThemes = append(signals.PreferredThemes, strings.TrimSpace(strings.TrimPrefix(reason, "Theme preference: ")))
		case strings.HasPrefix(reason, "Studio preference: "):
			signals.PreferredStudio = strings.TrimSpace(strings.TrimPrefix(reason, "Studio preference: "))
		case strings.HasPrefix(reason, "Label preference: "):
			signals.PreferredLabel = strings.TrimSpace(strings.TrimPrefix(reason, "Label preference: "))
		case strings.HasPrefix(reason, "Title matches a watched theme: "):
			signals.WatchedTextThemes = append(signals.WatchedTextThemes, strings.TrimSpace(strings.TrimPrefix(reason, "Title matches a watched theme: ")))
		case strings.HasPrefix(reason, "Story matches a watched theme: "):
			signals.WatchedTextThemes = append(signals.WatchedTextThemes, strings.TrimSpace(strings.TrimPrefix(reason, "Story matches a watched theme: ")))
		case reason == "A fresh release outside your usual history":
			signals.FreshDiscovery = true
		}
	}
	return signals
}

func discoveryAIBatches(items []discoveryItem, settings map[string]string, limit, batchSize, maxInputChars int) ([][]discoveryAICandidate, [][]byte) {
	items = items[:min(limit, len(items))]
	poolsSize := len(settings["discoveries_pools"])
	subtitleEnabled := settings["discoveries_subtitle_analysis_enabled"] == "true"
	if strings.EqualFold(strings.TrimSpace(settings["discoveries_ai_primary_provider"]), "openai") && settings["discoveries_openai_include_subtitles"] == "false" {
		subtitleEnabled = false
	}
	// Subtitle dialogue is weak supporting evidence. Large excerpts dominate
	// prompt evaluation time on small local models without improving ranking
	// quality, so each candidate receives a compact cleaned sample.
	configuredSubtitleChars := min(max(discoveryInt(settings, "discoveries_subtitle_max_chars", 4000), 0), 4000)
	configuredPools := discoveryPools(settings["discoveries_pools"])
	batches, payloads := make([][]discoveryAICandidate, 0, (len(items)+batchSize-1)/batchSize), make([][]byte, 0, (len(items)+batchSize-1)/batchSize)
	for start := 0; start < len(items); start += batchSize {
		end := min(start+batchSize, len(items))
		batch := make([]discoveryAICandidate, 0, end-start)
		eligible := 0
		for _, item := range items[start:end] {
			story := item.Story
			story = aidiscovery.TruncateUTF8(story, 1200)
			eligiblePools := []string{}
			for name, keywords := range configuredPools {
				for _, keyword := range keywords {
					if discoveryTextMatches(item.Release, keyword) {
						eligiblePools = append(eligiblePools, name)
						break
					}
				}
			}
			sort.Strings(eligiblePools)
			batch = append(batch, discoveryAICandidate{ID: item.ID, VideoID: item.VideoID, Title: item.Title, Story: story, Actresses: item.Actresses, Genres: item.Genres, Studio: item.Studio, Label: item.Label, Director: item.Director, ReleaseDate: item.ReleaseDate, Duration: item.Duration, Local: item.Local, Played: item.PlayCount, Orgasms: item.OCounter, BaselineScore: item.Score, DiscoveryState: item.Category, SubtitleAvailable: item.HasSubtitle, Evidence: slices.Clone(item.Reasons), Taste: discoveryTasteSignals(item.Reasons), EligiblePools: eligiblePools})
			if subtitleEnabled && item.HasSubtitle {
				eligible++
			}
		}
		base, _ := json.Marshal(batch)
		// Reserve room for the instructions, custom pools, JSON framing, and
		// character escaping. Subtitle excerpts share whatever remains.
		perSubtitle := 0
		if eligible > 0 {
			remaining := maxInputChars - len(base) - poolsSize - 12000
			perSubtitle = min(configuredSubtitleChars, max(remaining/eligible, 0))
		}
		if perSubtitle > 0 {
			for i, item := range items[start:end] {
				if !subtitleEnabled || !item.HasSubtitle {
					continue
				}
				mapped := discoveryRemapReleases([]domain.Release{item.Release}, settings["stash_missing_path_remaps"])
				batch[i].Subtitle = cleanedSubtitleExcerpt(mapped[0], perSubtitle)
			}
		}
		payload, _ := json.Marshal(batch)
		target := max(maxInputChars-poolsSize-12000, 1000)
		for len(payload) > target {
			changed := false
			for i := range batch {
				if len(batch[i].Subtitle) > 0 {
					batch[i].Subtitle = aidiscovery.TruncateUTF8(batch[i].Subtitle, max(utf8.RuneCountInString(batch[i].Subtitle)/2, 1))
					changed = true
				} else if utf8.RuneCountInString(batch[i].Story) > 160 {
					batch[i].Story = aidiscovery.TruncateUTF8(batch[i].Story, utf8.RuneCountInString(batch[i].Story)/2)
					changed = true
				}
			}
			if !changed {
				break
			}
			payload, _ = json.Marshal(batch)
		}
		batches, payloads = append(batches, batch), append(payloads, payload)
	}
	return batches, payloads
}

func discoveryAIRequestLimits(settings map[string]string) (int, int) {
	batchSize := min(max(discoveryInt(settings, "discoveries_openai_batch_size", 5), 1), 5)
	maxInputChars := min(max(discoveryInt(settings, "discoveries_openai_max_input_chars", 50000), 20000), 60000)
	return batchSize, maxInputChars
}

func (s *Server) enhanceDiscoveries(r *http.Request, settings map[string]string, items []discoveryItem) ([]discoveryItem, bool) {
	if settings["discoveries_ai_enabled"] != "true" || len(items) == 0 {
		return items, false
	}
	limit := discoveryInt(settings, "discoveries_openai_candidate_limit", 150)
	limit = min(max(limit, 10), min(len(items), 1000))
	// Small batches make the first durable results visible quickly and avoid a
	// single oversized constrained-generation request monopolizing remote GPUs.
	// This caps request size, not the total number of candidates enriched.
	batchSize, maxInputChars := discoveryAIRequestLimits(settings)
	batches, payloads := discoveryAIBatches(items, settings, limit, batchSize, maxInputChars)
	pools := strings.TrimSpace(settings["discoveries_pools"])
	provider := strings.ToLower(strings.TrimSpace(settings["discoveries_ai_primary_provider"]))
	if provider != "openai" {
		provider = "ollama"
	}
	model := strings.TrimSpace(settings["discoveries_ollama_model"])
	if provider == "openai" {
		model = strings.TrimSpace(settings["discoveries_openai_model"])
	}
	if model == "" {
		model = "qwen3:8b"
	}
	providerFingerprint := discoveryProviderFingerprint(settings, model)
	fingerprints := make(map[int64]string, limit)
	releaseIDs := make([]int64, 0, limit)
	for _, batch := range batches {
		for _, candidate := range batch {
			data, _ := json.Marshal(candidate)
			sum := sha256.Sum256(append([]byte(providerFingerprint+"\n"+pools+"\n"+settings["discoveries_subtitle_analysis_enabled"]+"\n"), data...))
			fingerprints[candidate.ID] = fmt.Sprintf("%x", sum)
			releaseIDs = append(releaseIDs, candidate.ID)
		}
	}
	persisted := make([]openAIRank, 0, limit)
	stored, err := s.store.DiscoveryAIRanks(r.Context(), releaseIDs)
	if err == nil {
		for _, id := range releaseIDs {
			if rank, ok := stored[id]; ok && rank.Fingerprint == fingerprints[id] && aidiscovery.ValidateStoredRank(rank, pools) == nil {
				persisted = append(persisted, openAIRank{ID: id, Score: rank.Score, Reason: rank.Reason, Pools: rank.Pools, SubtitleUsed: rank.SubtitleUsed})
			}
		}
	}
	persistedIDs := make(map[int64]bool, len(persisted))
	for _, rank := range persisted {
		persistedIDs[rank.ID] = true
	}
	missingBatches := make([][]discoveryAICandidate, 0, len(batches))
	for _, batch := range batches {
		missing := make([]discoveryAICandidate, 0, len(batch))
		for _, candidate := range batch {
			if !persistedIDs[candidate.ID] {
				missing = append(missing, candidate)
			}
		}
		if len(missing) > 0 {
			missingBatches = append(missingBatches, missing)
		}
	}
	if len(missingBatches) == 0 {
		return applyOpenAIRanks(items, persisted), len(persisted) > 0
	}
	hasher := sha256.New()
	hasher.Write([]byte(strings.Join([]string{providerFingerprint, pools, settings["discoveries_subtitle_analysis_enabled"], strconv.Itoa(batchSize), strconv.Itoa(maxInputChars)}, "\n")))
	for _, payload := range payloads {
		hasher.Write(payload)
	}
	var cacheKey [32]byte
	copy(cacheKey[:], hasher.Sum(nil))
	discoveryRankCache.Lock()
	cached, cacheHit := discoveryRankCache.entries[cacheKey]
	discoveryRankCache.Unlock()
	if cacheHit {
		age := time.Since(cached.created)
		if len(cached.ranks) > 0 && age < discoveryDuration(settings, "discoveries_openai_cache_interval", 6*time.Hour) {
			return applyOpenAIRanks(items, cached.ranks), true
		}
		if len(cached.ranks) == 0 && age < 10*time.Second {
			return applyOpenAIRanks(items, persisted), len(persisted) > 0
		}
	}
	// An enrichment cache miss must never hold the Discoveries page open on an
	// external API. Return deterministic results immediately and populate the
	// cache in the background; a later refresh automatically uses the enhanced
	// ranking.
	settingsCopy := maps.Clone(settings)
	discoveryAIStatus.Lock()
	if discoveryAIStatus.Running {
		discoveryAIStatus.Unlock()
		return applyOpenAIRanks(items, persisted), len(persisted) > 0
	}
	discoveryAIStatus.Running, discoveryAIStatus.Completed, discoveryAIStatus.Total, discoveryAIStatus.Batch, discoveryAIStatus.Batches, discoveryAIStatus.Current, discoveryAIStatus.Error = true, len(persisted), limit, 1, len(missingBatches), len(missingBatches[0]), ""
	discoveryAIStatus.StartedAt, discoveryAIStatus.BatchStartedAt, discoveryAIStatus.LastBatchSeconds = time.Now().UTC(), time.Now().UTC(), 0
	discoveryAIStatus.CurrentItems = discoveryBatchLabels(missingBatches[0])
	discoveryAIStatus.StartingCompleted, discoveryAIStatus.Provider, discoveryAIStatus.Model = len(persisted), provider, model
	discoveryAIStatus.InputTokens, discoveryAIStatus.OutputTokens, discoveryAIStatus.EstimatedCostUSD = 0, 0, 0
	discoveryAIStatus.Unlock()
	discoveryRankCache.Lock()
	discoveryRankCache.entries[cacheKey] = discoveryRankCacheEntry{created: time.Now(), ranks: slices.Clone(persisted)}
	discoveryRankCache.Unlock()
	go func() {
		defer func() {
			discoveryAIStatus.Lock()
			discoveryAIStatus.Running, discoveryAIStatus.Current = false, 0
			discoveryAIStatus.CurrentItems = nil
			discoveryAIStatus.Unlock()
		}()
		combined := slices.Clone(persisted)
		completed := len(persisted)
		for index, batch := range missingBatches {
			discoveryAIStatus.Lock()
			discoveryAIStatus.Batch, discoveryAIStatus.Current = index+1, len(batch)
			discoveryAIStatus.BatchStartedAt = time.Now().UTC()
			discoveryAIStatus.CurrentItems = discoveryBatchLabels(batch)
			discoveryAIStatus.Unlock()
			aiCandidates := make([]aidiscovery.Candidate, 0, len(batch))
			for _, candidate := range batch {
				aiCandidates = append(aiCandidates, aidiscovery.Candidate{ID: candidate.ID, VideoID: candidate.VideoID, Title: candidate.Title, Story: candidate.Story, Actresses: candidate.Actresses, Genres: candidate.Genres, Studio: candidate.Studio, Label: candidate.Label, Director: candidate.Director, ReleaseDate: candidate.ReleaseDate, Duration: candidate.Duration, Local: candidate.Local, Played: candidate.Played, Orgasms: candidate.Orgasms, BaselineScore: candidate.BaselineScore, DiscoveryState: candidate.DiscoveryState, SubtitleAvailable: candidate.SubtitleAvailable, Evidence: candidate.Evidence, Taste: candidate.Taste, EligiblePools: candidate.EligiblePools, Subtitle: candidate.Subtitle})
			}
			result := s.discoveryAI.Rank(context.Background(), discoveryAIConfig(settingsCopy), aiCandidates, pools)
			resultModel := model
			telemetryProvider := result.Provider
			if telemetryProvider == "" {
				telemetryProvider = result.AttemptedProvider
			}
			if telemetryProvider == "openai" {
				resultModel = strings.TrimSpace(settingsCopy["discoveries_openai_model"])
			}
			discoveryAIStatus.Lock()
			if telemetryProvider != "" {
				discoveryAIStatus.Provider, discoveryAIStatus.Model = telemetryProvider, resultModel
			}
			discoveryAIStatus.InputTokens += result.Usage.InputTokens
			discoveryAIStatus.OutputTokens += result.Usage.OutputTokens
			discoveryAIStatus.EstimatedCostUSD = discoveryOpenAICostUSD(resultModel, discoveryAIStatus.InputTokens, discoveryAIStatus.OutputTokens)
			usageSettings := map[string]string{
				"discoveries_ai_last_provider":           discoveryAIStatus.Provider,
				"discoveries_ai_last_model":              discoveryAIStatus.Model,
				"discoveries_ai_last_input_tokens":       strconv.FormatInt(discoveryAIStatus.InputTokens, 10),
				"discoveries_ai_last_output_tokens":      strconv.FormatInt(discoveryAIStatus.OutputTokens, 10),
				"discoveries_ai_last_estimated_cost_usd": strconv.FormatFloat(discoveryAIStatus.EstimatedCostUSD, 'f', 8, 64),
			}
			discoveryAIStatus.Unlock()
			_ = s.store.SaveSettings(context.Background(), usageSettings)
			if result.Skipped || len(result.Ranks) == 0 {
				discoveryAIStatus.Lock()
				discoveryAIStatus.Error = fmt.Sprintf("Batch %d/%d: %s", index+1, len(missingBatches), result.Status)
				discoveryAIStatus.Unlock()
				return
			}
			// The batch's own candidates are the ground truth for whether a
			// subtitle excerpt actually reached the AI payload - HasSubtitle
			// only means a file exists, but the shared per-batch character
			// budget in discoveryAIBatches can leave Subtitle empty anyway.
			subtitleUsedByID := make(map[int64]bool, len(batch))
			for _, candidate := range batch {
				subtitleUsedByID[candidate.ID] = candidate.Subtitle != ""
			}
			ranks := make([]openAIRank, 0, len(result.Ranks))
			for _, rank := range result.Ranks {
				ranks = append(ranks, openAIRank{ID: rank.ID, Score: rank.Score, Reason: rank.Reason, Pools: rank.Pools, SubtitleUsed: subtitleUsedByID[rank.ID]})
			}
			now := time.Now().UTC()
			durable := make([]domain.DiscoveryAIRank, 0, len(ranks))
			for _, rank := range ranks {
				durable = append(durable, domain.DiscoveryAIRank{ReleaseID: rank.ID, Fingerprint: fingerprints[rank.ID], Model: result.Provider + ":" + resultModel, Score: rank.Score, Reason: rank.Reason, Pools: rank.Pools, SubtitleUsed: rank.SubtitleUsed, GeneratedAt: now})
			}
			if err := saveValidatedDiscoveryAIRanks(context.Background(), s.store, durable, pools); err != nil {
				discoveryAIStatus.Lock()
				discoveryAIStatus.Error = fmt.Sprintf("Batch %d/%d database save: %v", index+1, len(missingBatches), err)
				discoveryAIStatus.Unlock()
				return
			}
			combined = append(combined, ranks...)
			completed += len(batch)
			// Publish each completed batch so the page can progressively use and
			// cache successful work even if a later request fails.
			discoveryRankCache.Lock()
			discoveryRankCache.entries[cacheKey] = discoveryRankCacheEntry{created: time.Now(), ranks: slices.Clone(combined)}
			discoveryRankCache.Unlock()
			discoveryAIStatus.Lock()
			discoveryAIStatus.Completed = min(completed, limit)
			discoveryAIStatus.LastBatchSeconds = time.Since(discoveryAIStatus.BatchStartedAt).Seconds()
			discoveryAIStatus.Unlock()
		}
	}()
	return applyOpenAIRanks(items, persisted), len(persisted) > 0
}

func discoveryBatchLabels(batch []discoveryAICandidate) []string {
	labels := make([]string, 0, len(batch))
	for _, candidate := range batch {
		label := strings.TrimSpace(candidate.VideoID)
		if label == "" {
			label = strconv.FormatInt(candidate.ID, 10)
		}
		labels = append(labels, label)
	}
	return labels
}

func discoveryProviderFingerprint(settings map[string]string, model string) string {
	return strings.Join([]string{"ai-discovery-schema:" + aidiscovery.SchemaVersion, settings["discoveries_ai_primary_provider"], settings["discoveries_ollama_url"], model, settings["discoveries_openai_fallback_enabled"], settings["discoveries_openai_model"], settings["discoveries_openai_include_subtitles"]}, "\n")
}

func discoveryOpenAICostUSD(model string, inputTokens, outputTokens int64) float64 {
	// Standard Responses API rates per million tokens. Unknown/custom models
	// deliberately return zero rather than presenting a misleading estimate.
	rate, ok := discoveryOpenAIRates(model)
	if !ok {
		return 0
	}
	return float64(inputTokens)/1_000_000*rate[0] + float64(outputTokens)/1_000_000*rate[1]
}

func discoveryOpenAIRates(model string) ([2]float64, bool) {
	rates := map[string][2]float64{
		"gpt-5-mini":    {0.25, 2.00},
		"gpt-5.4-mini":  {0.75, 4.50},
		"gpt-5.6-luna":  {0.20, 1.20},
		"gpt-5.6-terra": {2.00, 12.00},
		"gpt-5.6-sol":   {4.00, 20.00},
		"gpt-6-astra":   {10.00, 50.00},
	}
	rate, ok := rates[strings.ToLower(strings.TrimSpace(model))]
	return rate, ok
}

type discoveryAIRankSaver interface {
	SaveDiscoveryAIRanks(context.Context, []domain.DiscoveryAIRank) error
}

func saveValidatedDiscoveryAIRanks(ctx context.Context, saver discoveryAIRankSaver, ranks []domain.DiscoveryAIRank, pools string) error {
	for _, rank := range ranks {
		if err := aidiscovery.ValidateStoredRank(rank, pools); err != nil {
			return fmt.Errorf("refusing to persist invalid AI ranking: %w", err)
		}
	}
	return saver.SaveDiscoveryAIRanks(ctx, ranks)
}

func discoveryFloat(settings map[string]string, key string, fallback float64) float64 {
	v, err := strconv.ParseFloat(strings.TrimSpace(settings[key]), 64)
	if err != nil {
		return fallback
	}
	return v
}

func discoveryAIConfig(settings map[string]string) aidiscovery.Config {
	return aidiscovery.Config{
		Enabled:                settings["discoveries_ai_enabled"] == "true",
		PrimaryProvider:        strings.TrimSpace(settings["discoveries_ai_primary_provider"]),
		OllamaURL:              strings.TrimSpace(settings["discoveries_ollama_url"]),
		OllamaModel:            strings.TrimSpace(settings["discoveries_ollama_model"]),
		RequestTimeout:         time.Duration(min(max(discoveryInt(settings, "discoveries_ollama_request_timeout_seconds", 120), 5), 7200)) * time.Second,
		HealthTimeout:          time.Duration(min(max(discoveryInt(settings, "discoveries_ollama_health_timeout_seconds", 2), 1), 30)) * time.Second,
		OpenAIFallbackEnabled:  settings["discoveries_openai_fallback_enabled"] == "true",
		OpenAIAPIKey:           strings.TrimSpace(settings["discoveries_openai_api_key"]),
		OpenAIBaseURL:          strings.TrimSpace(settings["discoveries_openai_base_url"]),
		OpenAIModel:            strings.TrimSpace(settings["discoveries_openai_model"]),
		OpenAITimeout:          time.Duration(min(max(discoveryInt(settings, "discoveries_openai_timeout_seconds", 120), 15), 600)) * time.Second,
		OpenAIIncludeSubtitles: settings["discoveries_openai_include_subtitles"] != "false",
	}
}

func (s *Server) cachedDiscoveryAffinity(ctx context.Context, settings map[string]string, now time.Time) (affinityProfile, error) {
	key := sha256.Sum256([]byte(strings.Join([]string{settings["discoveries_play_weight"], settings["discoveries_orgasm_weight"], settings["discoveries_recency_half_life_days"], settings["discoveries_excluded_tags"], settings["discoveries_last_synced_at"]}, "\n")))
	discoveryAffinityCache.RLock()
	if discoveryAffinityCache.key == key && time.Since(discoveryAffinityCache.created) < time.Hour {
		profile := discoveryAffinityCache.profile
		discoveryAffinityCache.RUnlock()
		return profile, nil
	}
	discoveryAffinityCache.RUnlock()
	releases, err := s.discoveryReleasePage(ctx, domain.ReleaseFilter{Status: "local", Sort: "updated", Direction: "desc", ShowNonPreferred: true}, 0)
	if err != nil {
		return affinityProfile{}, err
	}
	releases, err = archivedAffinityReleases(ctx, s.store, releases)
	if err != nil {
		return affinityProfile{}, err
	}
	excluded := discoveryExcludedTags(settings["discoveries_excluded_tags"])
	eligible := releases[:0]
	for _, release := range releases {
		if !discoveryHasExcludedTag(release, excluded) {
			eligible = append(eligible, release)
		}
	}
	profile := buildAffinity(eligible, settings, now)
	discoveryAffinityCache.Lock()
	discoveryAffinityCache.created, discoveryAffinityCache.key, discoveryAffinityCache.profile = time.Now(), key, profile
	discoveryAffinityCache.Unlock()
	return profile, nil
}

func discoveryInt(settings map[string]string, key string, fallback int) int {
	v, err := strconv.Atoi(strings.TrimSpace(settings[key]))
	if err != nil {
		return fallback
	}
	return v
}

func discoveryDuration(settings map[string]string, key string, fallback time.Duration) time.Duration {
	duration, err := domain.ParseScheduleDuration(settings[key])
	if err != nil || duration < time.Minute {
		return fallback
	}
	return duration
}

func addAffinity(values []string, weight float64, target map[string]float64) {
	for _, value := range values {
		key := strings.ToLower(strings.TrimSpace(value))
		if key != "" {
			target[key] += weight
		}
	}
}

func buildAffinity(releases []domain.Release, settings map[string]string, now time.Time) affinityProfile {
	p := affinityProfile{map[string]float64{}, map[string]float64{}, map[string]float64{}, map[string]float64{}}
	playWeight := discoveryFloat(settings, "discoveries_play_weight", 1)
	orgasmWeight := discoveryFloat(settings, "discoveries_orgasm_weight", 3)
	halfLife := math.Max(1, discoveryFloat(settings, "discoveries_recency_half_life_days", 180))
	for _, release := range releases {
		if release.PlayCount <= 0 && release.OCounter <= 0 {
			continue
		}
		age := 0.0
		latest := release.LastPlayedAt
		if orgasmAt, err := time.Parse(time.RFC3339, release.LastOCountAt); err == nil {
			if playedAt, playErr := time.Parse(time.RFC3339, latest); playErr != nil || orgasmAt.After(playedAt) {
				latest = orgasmAt.Format(time.RFC3339)
			}
		}
		if parsed, err := time.Parse(time.RFC3339, latest); err == nil {
			age = math.Max(0, now.Sub(parsed).Hours()/24)
		}
		decay := math.Pow(.5, age/halfLife)
		weight := (float64(release.PlayCount)*playWeight + math.Log1p(float64(release.OCounter))*orgasmWeight) * decay
		addAffinity(release.Actresses, weight, p.actress)
		addAffinity(release.Genres, weight, p.genre)
		addAffinity([]string{release.Studio}, weight, p.studio)
		addAffinity([]string{release.Label}, weight, p.label)
	}
	return p
}

func affinityScore(values []string, weights map[string]float64) (float64, string) {
	best, reason := 0.0, ""
	for _, value := range values {
		if score := weights[strings.ToLower(strings.TrimSpace(value))]; score > best {
			best, reason = score, value
		}
	}
	return best, reason
}

// textAffinity turns meaningful phrases learned from watch history into a
// deterministic title/story signal. Longer phrases win over incidental short
// words, and the returned field is included in the user-facing explanation.
func textAffinity(release domain.Release, weights map[string]float64) (float64, string, string) {
	title, story := strings.ToLower(release.Title), strings.ToLower(release.Story)
	best, phrase, field := 0.0, "", ""
	for candidate, weight := range weights {
		candidate = strings.TrimSpace(candidate)
		if len([]rune(candidate)) < 3 || weight <= best {
			continue
		}
		matchedField := ""
		if strings.Contains(title, candidate) {
			matchedField = "Title"
		} else if strings.Contains(story, candidate) {
			matchedField = "Story"
		}
		if matchedField != "" {
			best, phrase, field = weight, candidate, matchedField
		}
	}
	return best, phrase, field
}

func scoreDiscoveryRelease(release domain.Release, profile affinityProfile, hasSubtitle bool, settings map[string]string, rewatchDays int, now time.Time) (float64, []string) {
	a, actress := affinityScore(release.Actresses, profile.actress)
	g, genre := affinityScore(release.Genres, profile.genre)
	st, studio := affinityScore([]string{release.Studio}, profile.studio)
	l, label := affinityScore([]string{release.Label}, profile.label)
	textScore, textTheme, textField := textAffinity(release, profile.genre)
	score := a*.30 + g*.25 + st*.12 + l*.08 + textScore*.12
	if release.PlayCount == 0 {
		score += 8
	}
	if hasSubtitle {
		score += discoveryFloat(settings, "discoveries_subtitle_bonus", 10)
	}
	if discoveryCategory(release, rewatchDays, now) == "rewatch" {
		score += 6
	}
	reasons := make([]string, 0, 4)
	if actress != "" {
		reasons = append(reasons, "Performer preference: "+actress)
	}
	if genre != "" {
		reasons = append(reasons, "Theme preference: "+genre)
	}
	if studio != "" {
		reasons = append(reasons, "Studio preference: "+studio)
	} else if label != "" {
		reasons = append(reasons, "Label preference: "+label)
	}
	if textTheme != "" {
		reasons = append(reasons, textField+" matches a watched theme: "+textTheme)
	}
	if len(reasons) == 0 {
		reasons = append(reasons, "A fresh release outside your usual history")
	}
	return math.Round(score*10) / 10, reasons
}

func hasSubtitleFile(release domain.Release) bool {
	return len(subtitleFiles(release)) > 0
}

// subtitleAvailability inventories each media directory once per Discoveries
// request. A large Stash library commonly keeps hundreds or thousands of
// scenes in one folder; calling os.ReadDir separately for every release made
// the initial page request appear to hang on network-backed libraries.
func subtitleAvailability(releases []domain.Release) map[int64]bool {
	return subtitleAvailabilityWithProgress(releases, nil)
}

type subtitleScanStats struct {
	Directories           int
	UnreadableDirectories int
	// MissingFilePath counts releases skipped without ever attempting a
	// directory read because Stash has not recorded a file path for them
	// yet (not matched to a local scene, or the sync hasn't populated it).
	// This is the dominant, otherwise-silent reason a fresh library shows
	// zero subtitles: distinguishing it from UnreadableDirectories (a real
	// permission/mount problem) is what makes the "why is this zero"
	// question answerable from the UI instead of guesswork.
	MissingFilePath int
}

func subtitleSidecarMatches(videoBase, name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	if ext != ".srt" && ext != ".ass" && ext != ".ssa" && ext != ".vtt" {
		return false
	}
	base := strings.ToLower(strings.TrimSpace(videoBase))
	name = strings.ToLower(strings.TrimSpace(name))
	if base == "" || !strings.HasPrefix(name, base) {
		return false
	}
	// Require a sidecar boundary so ABC-12 does not claim ABC-123.en.srt.
	remainder := strings.TrimPrefix(name, base)
	return strings.HasPrefix(remainder, ".") || strings.HasPrefix(remainder, "-") || strings.HasPrefix(remainder, "_")
}

func scanSubtitleAvailability(releases []domain.Release, progress func(completed, found int)) (map[int64]bool, subtitleScanStats) {
	directories := map[string][]os.DirEntry{}
	directoryErrors := map[string]bool{}
	out := make(map[int64]bool, len(releases))
	stats := subtitleScanStats{}
	for index, release := range releases {
		path := strings.TrimSpace(release.StashFilePath)
		if path == "" {
			stats.MissingFilePath++
			if progress != nil && (index%25 == 0 || index == len(releases)-1) {
				progress(index+1, len(out))
			}
			continue
		}
		base := strings.TrimSuffix(path, filepath.Ext(path))
		directory := filepath.Dir(base)
		entries, loaded := directories[directory]
		if !loaded {
			stats.Directories++
			var err error
			entries, err = os.ReadDir(directory)
			if err != nil {
				directoryErrors[directory] = true
				stats.UnreadableDirectories++
			}
			directories[directory] = entries
		}
		if directoryErrors[directory] {
			if progress != nil && (index%25 == 0 || index == len(releases)-1) {
				progress(index+1, len(out))
			}
			continue
		}
		prefix := filepath.Base(base)
		for _, entry := range entries {
			if !entry.IsDir() && subtitleSidecarMatches(prefix, entry.Name()) {
				out[release.ID] = true
				break
			}
		}
		if progress != nil && (index%25 == 0 || index == len(releases)-1) {
			progress(index+1, len(out))
		}
	}
	return out, stats
}

func subtitleAvailabilityWithProgress(releases []domain.Release, progress func(int)) map[int64]bool {
	availability, _ := scanSubtitleAvailability(releases, func(completed, _ int) {
		if progress != nil {
			progress(completed)
		}
	})
	return availability
}

func cachedSubtitleAvailability(releases []domain.Release, ttl time.Duration) map[int64]bool {
	discoverySubtitleCache.RLock()
	created, cached, checked := discoverySubtitleCache.created, maps.Clone(discoverySubtitleCache.availability), maps.Clone(discoverySubtitleCache.checked)
	discoverySubtitleCache.RUnlock()
	if cached == nil || time.Since(created) >= ttl {
		cached = map[int64]bool{}
		checked = map[int64]bool{}
	}
	missing := make([]domain.Release, 0, len(releases))
	for _, release := range releases {
		if !checked[release.ID] {
			missing = append(missing, release)
		}
	}
	if len(missing) == 0 {
		return cached
	}
	for releaseID, present := range subtitleAvailability(missing) {
		cached[releaseID] = present
	}
	for _, release := range missing {
		checked[release.ID] = true
	}
	discoverySubtitleCache.Lock()
	discoverySubtitleCache.created = time.Now()
	discoverySubtitleCache.availability = cached
	discoverySubtitleCache.checked = checked
	discoverySubtitleCache.Unlock()
	return cached
}

func (s *Server) discoveryReleasePage(ctx context.Context, filter domain.ReleaseFilter, maximum int) ([]domain.Release, error) {
	capacity := maximum
	if capacity <= 0 {
		capacity = 1000
	}
	releases := make([]domain.Release, 0, capacity)
	startOffset := filter.Offset
	for offset := startOffset; maximum <= 0 || len(releases) < maximum; offset += 500 {
		filter.Limit = 500
		if maximum > 0 {
			filter.Limit = min(500, maximum-len(releases))
		}
		filter.Offset = offset
		page, err := s.store.Releases(ctx, filter)
		if err != nil {
			return nil, err
		}
		releases = append(releases, page...)
		if len(page) < filter.Limit {
			break
		}
	}
	return releases, nil
}

func subtitleFiles(release domain.Release) []string {
	path := strings.TrimSpace(release.StashFilePath)
	if path == "" {
		return nil
	}
	base := strings.TrimSuffix(path, filepath.Ext(path))
	files := make([]string, 0, 2)
	entries, err := os.ReadDir(filepath.Dir(base))
	if err != nil {
		return files
	}
	prefix := filepath.Base(base)
	for _, entry := range entries {
		name := entry.Name()
		if !entry.IsDir() && subtitleSidecarMatches(prefix, name) {
			files = append(files, filepath.Join(filepath.Dir(base), name))
		}
	}
	return files
}

func discoveryCategory(release domain.Release, rewatchDays int, now time.Time) string {
	if !release.Local {
		return "new"
	}
	if release.PlayCount == 0 {
		return "unwatched"
	}
	if parsed, err := time.Parse(time.RFC3339, release.LastPlayedAt); err == nil && now.Sub(parsed) >= time.Duration(rewatchDays)*24*time.Hour {
		return "rewatch"
	}
	return "watched"
}

func discoveryTextMatches(release domain.Release, query string) bool {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return true
	}
	haystack := strings.ToLower(strings.Join([]string{release.VideoID, release.Title, release.Story, release.Studio, release.Label, strings.Join(release.Actresses, " "), strings.Join(release.Genres, " ")}, " "))
	for _, token := range strings.Fields(query) {
		if !strings.Contains(haystack, token) {
			return false
		}
	}
	return true
}

func discoveryPools(raw string) map[string][]string {
	out := map[string][]string{}
	for _, line := range strings.Split(raw, "\n") {
		parts := strings.SplitN(line, "|", 2)
		if strings.TrimSpace(parts[0]) == "" {
			continue
		}
		if len(parts) == 1 {
			name := strings.TrimSpace(parts[0])
			out[name] = []string{name}
			continue
		}
		seen := map[string]bool{}
		for _, keyword := range strings.Split(parts[1], ",") {
			if keyword = strings.TrimSpace(keyword); keyword != "" {
				normalized := strings.ToLower(keyword)
				if !seen[normalized] {
					out[strings.TrimSpace(parts[0])] = append(out[strings.TrimSpace(parts[0])], keyword)
					seen[normalized] = true
				}
			}
		}
	}
	return out
}

func discoveryExcludedTags(raw string) map[string]bool {
	out := map[string]bool{}
	for _, value := range strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == '\n' || r == ';' }) {
		if value = strings.ToLower(strings.TrimSpace(value)); value != "" {
			out[value] = true
		}
	}
	return out
}

func discoveryExcludedTagValues(raw string) []string {
	excluded := discoveryExcludedTags(raw)
	values := make([]string, 0, len(excluded))
	for value := range excluded {
		values = append(values, value)
	}
	sort.Strings(values)
	return values
}

func discoveryHasExcludedTag(release domain.Release, excluded map[string]bool) bool {
	for _, tag := range release.Genres {
		if excluded[strings.ToLower(strings.TrimSpace(tag))] {
			return true
		}
	}
	return false
}

type discoveryPathRemap struct {
	From string `json:"from"`
	To   string `json:"to"`
}

func discoveryRemapReleases(releases []domain.Release, raw string) []domain.Release {
	var remaps []discoveryPathRemap
	if json.Unmarshal([]byte(raw), &remaps) != nil {
		return releases
	}
	for i := range releases {
		path := releases[i].StashFilePath
		for _, remap := range remaps {
			from := strings.TrimRight(strings.TrimSpace(remap.From), "/\\")
			if from != "" && (path == from || strings.HasPrefix(path, from+string(filepath.Separator)) || strings.HasPrefix(path, from+"/")) {
				releases[i].StashFilePath = filepath.Join(strings.TrimSpace(remap.To), strings.TrimLeft(path[len(from):], "/\\"))
				break
			}
		}
	}
	return releases
}

func diversifyDiscoveries(items []discoveryItem, strength float64) []discoveryItem {
	if strength <= 0 || len(items) < 3 {
		return items
	}
	strength = math.Min(strength, 100) / 100
	seen := map[string]int{}
	for i := range items {
		keys := append([]string{items[i].Studio}, items[i].Actresses...)
		penalty := 0.0
		for _, key := range keys {
			penalty += float64(seen[strings.ToLower(strings.TrimSpace(key))]) * strength * 2
		}
		items[i].Score -= penalty
		for _, key := range keys {
			if key = strings.ToLower(strings.TrimSpace(key)); key != "" {
				seen[key]++
			}
		}
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].Score > items[j].Score })
	return items
}

func discoveryFilterFromQuery(q url.Values, settings map[string]string, category string) (domain.ReleaseFilter, map[string][]string, string) {
	pools := discoveryPools(settings["discoveries_pools"])
	pool := strings.TrimSpace(q.Get("pool"))
	filter := domain.ReleaseFilter{Search: q.Get("search"), SearchWildcards: q.Get("search_wildcards") == "true", Category: q.Get("filter_category"), Entries: q.Get("entries"), WildcardLogic: q.Get("wildcard_logic"), SearchExpression: q.Get("search_expression"), HideLocal: q.Get("hide_local") == "true", ShowNonPreferred: q.Get("show_non_preferred") == "true", Sort: q.Get("sort"), Direction: q.Get("direction")}
	// Apply discovery-specific exclusions in SQL so totals, offsets and pages
	// describe the same candidate set. Filtering these only after fetching a
	// page could produce an empty page while still reporting thousands of
	// matches whenever the highest-scored releases carried an excluded tag.
	filter.ExcludeTags = discoveryExcludedTagValues(settings["discoveries_excluded_tags"])
	// AI text is produced after the database query, so it must be filtered
	// after enrichment rather than being mistaken for a release column.
	if strings.EqualFold(strings.TrimSpace(filter.Category), "AI text") {
		filter.Category, filter.Entries = "", ""
	}
	if keywords := pools[pool]; pool != "" {
		filter.PoolSearch = strings.Join(keywords, ",")
	}
	if filter.Sort == "" || filter.Sort == "score" {
		filter.Sort, filter.Direction = "score", "desc"
	} else if filter.Sort == "release_score" {
		filter.Direction = "desc"
	}
	if category == "new" {
		filter.HideLocal = true
	} else if category == "ready" || category == "unwatched" || category == "rewatch" || category == "needs_subtitles" {
		filter.Status = "local"
	}
	if !filter.ShowNonPreferred {
		filter.IgnoreTags = domain.ParseIgnoreList(settings["ignore_tags"])
		filter.IgnoreTitles = domain.ParseIgnoreList(settings["ignore_titles"])
		filter.UsePreferred = len(filter.IgnoreTags) > 0 || len(filter.IgnoreTitles) > 0
	}
	return filter, pools, pool
}

func discoveryAITextMatches(text, rawEntries, logic string) bool {
	if strings.TrimSpace(rawEntries) == "" {
		return true
	}
	var entries []string
	if strings.HasPrefix(strings.TrimSpace(rawEntries), "[") {
		_ = json.Unmarshal([]byte(rawEntries), &entries)
	} else {
		entries = strings.Split(rawEntries, ",")
	}
	text = strings.ToLower(text)
	matched := 0
	wanted := 0
	for _, entry := range entries {
		entry = strings.ToLower(strings.TrimSpace(entry))
		if entry != "" {
			wanted++
			pattern := regexp.QuoteMeta(entry)
			pattern = strings.ReplaceAll(pattern, `\*`, `.*`)
			pattern = strings.ReplaceAll(pattern, `\?`, `.`)
			if ok, _ := regexp.MatchString(pattern, text); ok {
				matched++
			}
		}
	}
	if strings.EqualFold(logic, "and") {
		return wanted == 0 || matched == wanted
	}
	return wanted == 0 || matched > 0
}

func discoveryReleaseMatches(release domain.Release, category, subtitles string, hasSubtitle bool, excluded map[string]bool, rewatchDays int, now time.Time) bool {
	if discoveryHasExcludedTag(release, excluded) {
		return false
	}
	itemCategory := discoveryCategory(release, rewatchDays, now)
	if category != "" && category != "all" && category != "for_you" && category != "random" && category != itemCategory && !(category == "ready" && itemCategory == "unwatched") && !(category == "needs_subtitles" && release.Local) {
		return false
	}
	return !((subtitles == "yes" && !hasSubtitle) || (subtitles == "no" && hasSubtitle) || (category == "ready" && !hasSubtitle) || (category == "needs_subtitles" && hasSubtitle))
}

func (s *Server) discoveries(w http.ResponseWriter, r *http.Request) {
	settings, _ := s.store.Settings(r.Context())
	if settings["discoveries_enabled"] == "false" {
		s.json(w, http.StatusOK, map[string]any{"items": []discoveryItem{}, "total": 0, "disabled": true})
		return
	}
	q := r.URL.Query()
	category := strings.TrimSpace(q.Get("category"))
	requestedLimit := discoveryInt(map[string]string{"limit": q.Get("limit")}, "limit", discoveryInt(settings, "discoveries_result_limit", 100))
	requestedLimit = min(max(requestedLimit, 1), 500)
	offset := max(discoveryInt(map[string]string{"offset": q.Get("offset")}, "offset", 0), 0)
	cacheQuery := make(url.Values, len(q))
	for key, values := range q {
		cacheQuery[key] = append([]string(nil), values...)
	}
	cacheQuery.Del("limit")
	settingsJSON, _ := json.Marshal(settings)
	cacheKey := sha256.Sum256(append([]byte(cacheQuery.Encode()+"\n"), settingsJSON...))
	discoveryResultCache.RLock()
	cachedItems, cachedMode, cachedPools := discoveryResultCache.items, discoveryResultCache.mode, discoveryResultCache.pools
	// Page responses are deliberately not reused as if they represented the
	// complete result set. Affinity, subtitle and OpenAI work have their own
	// caches below; the database page itself is cheap and always current.
	cacheHit := false
	discoveryResultCache.RUnlock()
	if cacheHit {
		total := len(cachedItems)
		page := []discoveryItem{}
		if offset < total {
			page = cachedItems[offset:min(offset+requestedLimit, total)]
		}
		discoveryAIStatus.RLock()
		aiRunning, aiCompleted, aiTotal, aiError := discoveryAIStatus.Running, discoveryAIStatus.Completed, discoveryAIStatus.Total, discoveryAIStatus.Error
		discoveryAIStatus.RUnlock()
		s.json(w, http.StatusOK, map[string]any{"items": page, "total": total, "offset": offset, "has_more": offset+len(page) < total, "generated_at": discoveryResultCache.created, "mode": cachedMode, "pools": cachedPools, "openai": map[string]any{"enabled": settings["discoveries_ai_enabled"] == "true", "running": aiRunning, "completed": aiCompleted, "total": aiTotal, "error": aiError}})
		return
	}
	// Filter/order/page in the database before the expensive recommendation
	// enrichment. Every catalog row remains reachable without blocking the UI
	// on a full-library scoring pass.
	candidateLimit := requestedLimit
	aiOnly := q.Get("ai_only") == "true"
	filter, pools, pool := discoveryFilterFromQuery(q, settings, category)
	filter.AIEnhanced = aiOnly
	filter.Offset = offset
	fullTotal, err := s.store.ReleasesCount(r.Context(), filter)
	if err != nil {
		s.problem(w, http.StatusInternalServerError, err.Error())
		return
	}
	releases, err := s.discoveryReleasePage(r.Context(), filter, candidateLimit)
	if err != nil {
		s.problem(w, http.StatusInternalServerError, err.Error())
		return
	}
	now := time.Now().UTC()
	libraryOrder := make(map[int64]int, len(releases))
	for index, release := range releases {
		libraryOrder[release.ID] = index
	}
	excluded := discoveryExcludedTags(settings["discoveries_excluded_tags"])
	profile, err := s.cachedDiscoveryAffinity(r.Context(), settings, now)
	if err != nil {
		s.problem(w, http.StatusInternalServerError, "load authoritative Stash history: "+err.Error())
		return
	}
	remapped := discoveryRemapReleases(releases, settings["stash_missing_path_remaps"])
	subtitlesByRelease := cachedSubtitleAvailability(remapped, discoveryDuration(settings, "discoveries_subtitle_refresh_interval", 6*time.Hour))
	rewatchDays := discoveryInt(settings, "discoveries_rewatch_days", 90)
	subtitles := strings.TrimSpace(r.URL.Query().Get("subtitles"))
	items := make([]discoveryItem, 0, len(releases))
	for _, release := range releases {
		itemCategory := discoveryCategory(release, rewatchDays, now)
		hasSubtitle := subtitlesByRelease[release.ID]
		if !discoveryReleaseMatches(release, category, subtitles, hasSubtitle, excluded, rewatchDays, now) {
			continue
		}
		score, reasons := scoreDiscoveryRelease(release, profile, hasSubtitle, settings, rewatchDays, now)
		if keywords := pools[pool]; pool != "" {
			for _, keyword := range keywords {
				if discoveryTextMatches(release, keyword) {
					reasons = append(reasons, "Discovery pool match: "+pool+" · "+keyword)
					break
				}
			}
		}
		itemPools := []string{}
		if pool != "" {
			itemPools = append(itemPools, pool)
		}
		items = append(items, discoveryItem{Release: release, Score: score, Category: itemCategory, Reasons: reasons, Pools: itemPools, HasSubtitle: hasSubtitle})
	}
	enhanced := false
	if category != "random" && category != "new" {
		items, enhanced = s.enhanceDiscoveries(r, settings, items)
	}
	aiTextEntries := ""
	if strings.EqualFold(strings.TrimSpace(q.Get("filter_category")), "AI text") {
		aiTextEntries = q.Get("entries")
	}
	if aiOnly || aiTextEntries != "" {
		filtered := items[:0]
		for _, item := range items {
			if aiOnly && !item.AIEnhanced {
				continue
			}
			if aiTextEntries != "" && (!item.AIEnhanced || !discoveryAITextMatches(item.AIText, aiTextEntries, filter.WildcardLogic)) {
				continue
			}
			filtered = append(filtered, item)
		}
		items = filtered
	}
	requestedSort := strings.TrimSpace(q.Get("sort"))
	if requestedSort == "score" && category != "random" {
		sort.SliceStable(items, func(i, j int) bool { return libraryOrder[items[i].ID] < libraryOrder[items[j].ID] })
	} else if requestedSort == "release_score" && category != "random" {
		sort.SliceStable(items, func(i, j int) bool {
			if items[i].ReleaseDate == items[j].ReleaseDate {
				return items[i].Score > items[j].Score
			}
			return items[i].ReleaseDate > items[j].ReleaseDate
		})
	} else if requestedSort != "" && requestedSort != "score" && category != "random" {
		sort.SliceStable(items, func(i, j int) bool { return libraryOrder[items[i].ID] < libraryOrder[items[j].ID] })
	} else if category == "random" {
		// Keep the daily surprise order stable across progressive page requests;
		// a fresh seed per request would duplicate or skip cards while scrolling.
		rng := rand.New(rand.NewSource(now.Truncate(24 * time.Hour).Unix()))
		rng.Shuffle(len(items), func(i, j int) { items[i], items[j] = items[j], items[i] })
	} else if category == "new" {
		sort.SliceStable(items, func(i, j int) bool { return items[i].ReleaseDate > items[j].ReleaseDate })
	} else {
		sort.SliceStable(items, func(i, j int) bool {
			if items[i].Score == items[j].Score {
				return items[i].ReleaseDate > items[j].ReleaseDate
			}
			return items[i].Score > items[j].Score
		})
		items = diversifyDiscoveries(items, discoveryFloat(settings, "discoveries_diversity_percent", 25))
	}
	total := fullTotal
	allItems := items
	mode := "deterministic"
	if enhanced {
		mode = "openai"
	}
	poolNames := make([]string, 0, len(pools))
	for name := range pools {
		poolNames = append(poolNames, name)
	}
	sort.Strings(poolNames)
	discoveryResultCache.Lock()
	discoveryResultCache.key, discoveryResultCache.created, discoveryResultCache.items, discoveryResultCache.mode, discoveryResultCache.pools = cacheKey, now, allItems, mode, poolNames
	discoveryResultCache.Unlock()
	discoveryAIStatus.RLock()
	aiRunning, aiCompleted, aiTotal, aiError := discoveryAIStatus.Running, discoveryAIStatus.Completed, discoveryAIStatus.Total, discoveryAIStatus.Error
	discoveryAIStatus.RUnlock()
	nextOffset := offset + len(releases)
	s.json(w, http.StatusOK, map[string]any{"items": items, "total": total, "offset": offset, "next_offset": nextOffset, "has_more": nextOffset < total, "generated_at": now, "mode": mode, "pools": poolNames, "selected_pool": pool, "pool_keywords": pools[pool], "openai": map[string]any{"enabled": settings["discoveries_ai_enabled"] == "true", "running": aiRunning, "completed": aiCompleted, "total": aiTotal, "error": aiError}})
}

package web

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"math"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

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
}

type affinityProfile struct {
	actress map[string]float64
	genre   map[string]float64
	studio  map[string]float64
	label   map[string]float64
}

// archivedAffinityReleases overlays the durable "Your playback archive"
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
}

func (s *Server) testDiscoveryOpenAI(w http.ResponseWriter, r *http.Request) {
	settings, _ := s.store.Settings(r.Context())
	var input struct {
		APIKey  string `json:"api_key"`
		BaseURL string `json:"base_url"`
		Model   string `json:"model"`
	}
	if !s.decode(w, r, &input) {
		return
	}
	apiKey := strings.TrimSpace(input.APIKey)
	if apiKey == "" {
		apiKey = strings.TrimSpace(settings["discoveries_openai_api_key"])
	}
	baseURL := strings.TrimRight(strings.TrimSpace(input.BaseURL), "/")
	if baseURL == "" {
		baseURL = strings.TrimRight(strings.TrimSpace(settings["discoveries_openai_base_url"]), "/")
	}
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}
	model := strings.TrimSpace(input.Model)
	if model == "" {
		model = strings.TrimSpace(settings["discoveries_openai_model"])
	}
	if model == "" {
		model = "gpt-5-mini"
	}
	if apiKey == "" {
		s.problem(w, http.StatusUnprocessableEntity, "OpenAI API key is empty")
		return
	}
	body, _ := json.Marshal(map[string]any{"model": model, "input": "Reply with exactly: JAVBeacon discovery test passed", "max_output_tokens": 32})
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, baseURL+"/responses", bytes.NewReader(body))
	if err != nil {
		s.problem(w, 500, err.Error())
		return
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	started := time.Now()
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		s.problem(w, http.StatusBadGateway, err.Error())
		return
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		s.problem(w, http.StatusBadGateway, fmt.Sprintf("OpenAI returned HTTP %d", resp.StatusCode))
		return
	}
	s.json(w, http.StatusOK, map[string]any{"ok": true, "model": model, "elapsed_ms": time.Since(started).Milliseconds(), "response": strings.TrimSpace(openAIText(jsonObject(data)))})
}

func jsonObject(data []byte) map[string]any {
	var value map[string]any
	_ = json.Unmarshal(data, &value)
	return value
}

var discoveryRankCache = struct {
	sync.Mutex
	entries map[[32]byte]discoveryRankCacheEntry
}{entries: map[[32]byte]discoveryRankCacheEntry{}}

var discoveryAIStatus = struct {
	sync.RWMutex
	Running   bool
	Completed int
	Total     int
	Error     string
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
			items[i].Score = math.Round((items[i].Score*.35+rank.Score*.65)*10) / 10
			items[i].Pools = append(items[i].Pools, rank.Pools...)
			if strings.TrimSpace(rank.Reason) != "" {
				items[i].Reasons = append([]string{rank.Reason}, items[i].Reasons...)
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
			if out.Len()+len(line)+1 > maxChars {
				remaining := maxChars - out.Len()
				if remaining > 0 {
					out.WriteString(line[:min(remaining, len(line))])
				}
				return out.String()
			}
			out.WriteString(line)
			out.WriteByte('\n')
		}
	}
	return out.String()
}

func openAIText(response map[string]any) string {
	output, _ := response["output"].([]any)
	for _, raw := range output {
		item, _ := raw.(map[string]any)
		content, _ := item["content"].([]any)
		for _, partRaw := range content {
			part, _ := partRaw.(map[string]any)
			if text, _ := part["text"].(string); text != "" {
				return text
			}
		}
	}
	return ""
}

func enhanceDiscoveries(_ *http.Request, settings map[string]string, items []discoveryItem) ([]discoveryItem, bool) {
	if settings["discoveries_openai_enabled"] != "true" || strings.TrimSpace(settings["discoveries_openai_api_key"]) == "" || len(items) == 0 {
		return items, false
	}
	limit := discoveryInt(settings, "discoveries_openai_candidate_limit", 150)
	limit = min(max(limit, 10), min(len(items), 1000))
	subtitleEnabled := settings["discoveries_subtitle_analysis_enabled"] == "true"
	subtitleChars := min(discoveryInt(settings, "discoveries_subtitle_max_chars", 16000), 16000)
	type candidate struct {
		ID        int64    `json:"id"`
		VideoID   string   `json:"video_id"`
		Title     string   `json:"title"`
		Story     string   `json:"story"`
		Actresses []string `json:"actresses"`
		Genres    []string `json:"genres"`
		Studio    string   `json:"studio"`
		Local     bool     `json:"local"`
		Played    int      `json:"play_count"`
		Orgasms   int      `json:"orgasm_count"`
		Subtitle  string   `json:"subtitle_excerpt,omitempty"`
	}
	candidates := make([]candidate, 0, limit)
	for _, item := range items[:limit] {
		story := item.Story
		if len(story) > 1200 {
			story = story[:1200]
		}
		c := candidate{item.ID, item.VideoID, item.Title, story, item.Actresses, item.Genres, item.Studio, item.Local, item.PlayCount, item.OCounter, ""}
		if subtitleEnabled && item.HasSubtitle {
			mapped := discoveryRemapReleases([]domain.Release{item.Release}, settings["stash_missing_path_remaps"])
			c.Subtitle = cleanedSubtitleExcerpt(mapped[0], subtitleChars)
		}
		candidates = append(candidates, c)
	}
	candidateJSON, _ := json.Marshal(candidates)
	pools := strings.TrimSpace(settings["discoveries_pools"])
	cacheKey := sha256.Sum256(append(append([]byte(strings.TrimSpace(settings["discoveries_openai_model"])+"\n"+pools+"\n"), candidateJSON...), []byte("\n"+settings["discoveries_subtitle_analysis_enabled"])...))
	discoveryRankCache.Lock()
	cached, cacheHit := discoveryRankCache.entries[cacheKey]
	discoveryRankCache.Unlock()
	if cacheHit {
		age := time.Since(cached.created)
		if len(cached.ranks) > 0 && age < discoveryDuration(settings, "discoveries_openai_cache_interval", 6*time.Hour) {
			return applyOpenAIRanks(items, cached.ranks), true
		}
		if len(cached.ranks) == 0 && age < 2*time.Minute {
			return items, false
		}
	}
	// An enrichment cache miss must never hold the Discoveries page open on an
	// external API. Return deterministic results immediately and populate the
	// cache in the background; a later refresh automatically uses the enhanced
	// ranking.
	prompt := fmt.Sprintf("Rerank these adult-media releases for this user's taste. Orgasm count is a stronger positive signal than play count. Use titles, stories, genres and subtitle dialogue to infer themes, but avoid inventing facts. Preserve variety and include occasional exploration. Custom pools:\n%s\nCandidates:\n%s", pools, candidateJSON)
	settingsCopy := maps.Clone(settings)
	discoveryRankCache.Lock()
	discoveryRankCache.entries[cacheKey] = discoveryRankCacheEntry{created: time.Now()}
	discoveryRankCache.Unlock()
	go func() {
		discoveryAIStatus.Lock()
		discoveryAIStatus.Running, discoveryAIStatus.Completed, discoveryAIStatus.Total, discoveryAIStatus.Error = true, 0, len(candidates), ""
		discoveryAIStatus.Unlock()
		defer func() { discoveryAIStatus.Lock(); discoveryAIStatus.Running = false; discoveryAIStatus.Unlock() }()
		schema := map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{"rankings": map[string]any{"type": "array", "items": map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{"id": map[string]any{"type": "integer"}, "score": map[string]any{"type": "number"}, "reason": map[string]any{"type": "string"}, "pools": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}}, "required": []string{"id", "score", "reason", "pools"}}}}, "required": []string{"rankings"}}
		model := strings.TrimSpace(settingsCopy["discoveries_openai_model"])
		if model == "" {
			model = "gpt-5-mini"
		}
		body, _ := json.Marshal(map[string]any{"model": model, "input": prompt, "text": map[string]any{"format": map[string]any{"type": "json_schema", "name": "discovery_rankings", "strict": true, "schema": schema}}})
		baseURL := strings.TrimRight(strings.TrimSpace(settingsCopy["discoveries_openai_base_url"]), "/")
		if baseURL == "" {
			baseURL = "https://api.openai.com/v1"
		}
		req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, baseURL+"/responses", bytes.NewReader(body))
		if err != nil {
			return
		}
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(settingsCopy["discoveries_openai_api_key"]))
		req.Header.Set("Content-Type", "application/json")
		resp, err := (&http.Client{Timeout: 45 * time.Second}).Do(req)
		if err != nil {
			discoveryAIStatus.Lock()
			discoveryAIStatus.Error = err.Error()
			discoveryAIStatus.Unlock()
			return
		}
		defer resp.Body.Close()
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			discoveryAIStatus.Lock()
			discoveryAIStatus.Error = fmt.Sprintf("OpenAI returned HTTP %d", resp.StatusCode)
			discoveryAIStatus.Unlock()
			return
		}
		var envelope map[string]any
		if json.Unmarshal(data, &envelope) != nil {
			return
		}
		var ranked struct {
			Rankings []openAIRank `json:"rankings"`
		}
		if json.Unmarshal([]byte(openAIText(envelope)), &ranked) != nil || len(ranked.Rankings) == 0 {
			return
		}
		discoveryRankCache.Lock()
		discoveryRankCache.entries[cacheKey] = discoveryRankCacheEntry{created: time.Now(), ranks: ranked.Rankings}
		discoveryRankCache.Unlock()
		discoveryAIStatus.Lock()
		discoveryAIStatus.Completed = len(ranked.Rankings)
		discoveryAIStatus.Unlock()
	}()
	return items, false
}

func discoveryFloat(settings map[string]string, key string, fallback float64) float64 {
	v, err := strconv.ParseFloat(strings.TrimSpace(settings[key]), 64)
	if err != nil {
		return fallback
	}
	return v
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

func subtitleAvailabilityWithProgress(releases []domain.Release, progress func(int)) map[int64]bool {
	directories := map[string][]os.DirEntry{}
	out := make(map[int64]bool, len(releases))
	for index, release := range releases {
		path := strings.TrimSpace(release.StashFilePath)
		if path == "" {
			if progress != nil && (index%25 == 0 || index == len(releases)-1) {
				progress(index + 1)
			}
			continue
		}
		base := strings.TrimSuffix(path, filepath.Ext(path))
		directory := filepath.Dir(base)
		entries, loaded := directories[directory]
		if !loaded {
			entries, _ = os.ReadDir(directory)
			directories[directory] = entries
		}
		prefix := filepath.Base(base)
		for _, entry := range entries {
			ext := strings.ToLower(filepath.Ext(entry.Name()))
			if !entry.IsDir() && strings.HasPrefix(entry.Name(), prefix) && (ext == ".srt" || ext == ".ass" || ext == ".ssa" || ext == ".vtt") {
				out[release.ID] = true
				break
			}
		}
		if progress != nil && (index%25 == 0 || index == len(releases)-1) {
			progress(index + 1)
		}
	}
	return out
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
		ext := strings.ToLower(filepath.Ext(name))
		if !entry.IsDir() && strings.HasPrefix(name, prefix) && (ext == ".srt" || ext == ".ass" || ext == ".ssa" || ext == ".vtt") {
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
	filter := domain.ReleaseFilter{Search: q.Get("search"), SearchWildcards: q.Get("search_wildcards") == "true", Category: q.Get("filter_category"), Entries: q.Get("entries"), SearchExpression: q.Get("search_expression"), HideLocal: q.Get("hide_local") == "true", ShowNonPreferred: q.Get("show_non_preferred") == "true", Sort: q.Get("sort"), Direction: q.Get("direction")}
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
		s.json(w, http.StatusOK, map[string]any{"items": page, "total": total, "offset": offset, "has_more": offset+len(page) < total, "generated_at": discoveryResultCache.created, "mode": cachedMode, "pools": cachedPools, "openai": map[string]any{"enabled": settings["discoveries_openai_enabled"] == "true", "running": aiRunning, "completed": aiCompleted, "total": aiTotal, "error": aiError}})
		return
	}
	// Filter/order/page in the database before the expensive recommendation
	// enrichment. Every catalog row remains reachable without blocking the UI
	// on a full-library scoring pass.
	candidateLimit := requestedLimit
	filter, pools, pool := discoveryFilterFromQuery(q, settings, category)
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
		s.problem(w, http.StatusInternalServerError, "load playback archive: "+err.Error())
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
		items, enhanced = enhanceDiscoveries(r, settings, items)
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
	s.json(w, http.StatusOK, map[string]any{"items": items, "total": total, "offset": offset, "next_offset": nextOffset, "has_more": nextOffset < total, "generated_at": now, "mode": mode, "pools": poolNames, "selected_pool": pool, "pool_keywords": pools[pool], "openai": map[string]any{"enabled": settings["discoveries_openai_enabled"] == "true", "running": aiRunning, "completed": aiCompleted, "total": aiTotal, "error": aiError}})
}

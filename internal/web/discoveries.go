package web

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"math/rand"
	"net/http"
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
}

type affinityProfile struct {
	actress map[string]float64
	genre   map[string]float64
	studio  map[string]float64
	label   map[string]float64
}

type openAIRank struct {
	ID     int64    `json:"id"`
	Score  float64  `json:"score"`
	Reason string   `json:"reason"`
	Pools  []string `json:"pools"`
}

var discoveryRankCache = struct {
	sync.Mutex
	entries map[[32]byte]discoveryRankCacheEntry
}{entries: map[[32]byte]discoveryRankCacheEntry{}}

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

func enhanceDiscoveries(r *http.Request, settings map[string]string, items []discoveryItem) ([]discoveryItem, bool) {
	if settings["discoveries_openai_enabled"] != "true" || strings.TrimSpace(settings["discoveries_openai_api_key"]) == "" || len(items) == 0 {
		return items, false
	}
	limit := discoveryInt(settings, "discoveries_openai_candidate_limit", 150)
	limit = min(max(limit, 10), min(len(items), 250))
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
			c.Subtitle = cleanedSubtitleExcerpt(item.Release, subtitleChars)
		}
		candidates = append(candidates, c)
	}
	candidateJSON, _ := json.Marshal(candidates)
	pools := strings.TrimSpace(settings["discoveries_pools"])
	cacheKey := sha256.Sum256(append(append([]byte(strings.TrimSpace(settings["discoveries_openai_model"])+"\n"+pools+"\n"), candidateJSON...), []byte("\n"+settings["discoveries_subtitle_analysis_enabled"])...))
	discoveryRankCache.Lock()
	cached, cacheHit := discoveryRankCache.entries[cacheKey]
	discoveryRankCache.Unlock()
	if cacheHit && time.Since(cached.created) < 6*time.Hour {
		return applyOpenAIRanks(items, cached.ranks), true
	}
	prompt := fmt.Sprintf("Rerank these adult-media releases for this user's taste. Orgasm count is a stronger positive signal than play count. Use titles, stories, genres and subtitle dialogue to infer themes, but avoid inventing facts. Preserve variety and include occasional exploration. Custom pools:\n%s\nCandidates:\n%s", pools, candidateJSON)
	schema := map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{"rankings": map[string]any{"type": "array", "items": map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{"id": map[string]any{"type": "integer"}, "score": map[string]any{"type": "number"}, "reason": map[string]any{"type": "string"}, "pools": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}}, "required": []string{"id", "score", "reason", "pools"}}}}, "required": []string{"rankings"}}
	model := strings.TrimSpace(settings["discoveries_openai_model"])
	if model == "" {
		model = "gpt-5-mini"
	}
	body, _ := json.Marshal(map[string]any{"model": model, "input": prompt, "text": map[string]any{"format": map[string]any{"type": "json_schema", "name": "discovery_rankings", "strict": true, "schema": schema}}})
	baseURL := strings.TrimRight(strings.TrimSpace(settings["discoveries_openai_base_url"]), "/")
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, baseURL+"/responses", bytes.NewReader(body))
	if err != nil {
		return items, false
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(settings["discoveries_openai_api_key"]))
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 45 * time.Second}).Do(req)
	if err != nil {
		return items, false
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return items, false
	}
	var envelope map[string]any
	if json.Unmarshal(data, &envelope) != nil {
		return items, false
	}
	var ranked struct {
		Rankings []openAIRank `json:"rankings"`
	}
	if json.Unmarshal([]byte(openAIText(envelope)), &ranked) != nil || len(ranked.Rankings) == 0 {
		return items, false
	}
	discoveryRankCache.Lock()
	discoveryRankCache.entries[cacheKey] = discoveryRankCacheEntry{created: time.Now(), ranks: ranked.Rankings}
	discoveryRankCache.Unlock()
	return applyOpenAIRanks(items, ranked.Rankings), true
}

func discoveryFloat(settings map[string]string, key string, fallback float64) float64 {
	v, err := strconv.ParseFloat(strings.TrimSpace(settings[key]), 64)
	if err != nil {
		return fallback
	}
	return v
}

func discoveryInt(settings map[string]string, key string, fallback int) int {
	v, err := strconv.Atoi(strings.TrimSpace(settings[key]))
	if err != nil {
		return fallback
	}
	return v
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
		if parsed, err := time.Parse(time.RFC3339, release.LastPlayedAt); err == nil {
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

func hasSubtitleFile(release domain.Release) bool {
	return len(subtitleFiles(release)) > 0
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
		if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" {
			continue
		}
		for _, keyword := range strings.Split(parts[1], ",") {
			if keyword = strings.TrimSpace(keyword); keyword != "" {
				out[strings.TrimSpace(parts[0])] = append(out[strings.TrimSpace(parts[0])], keyword)
			}
		}
	}
	return out
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

func (s *Server) discoveries(w http.ResponseWriter, r *http.Request) {
	settings, _ := s.store.Settings(r.Context())
	if settings["discoveries_enabled"] == "false" {
		s.json(w, http.StatusOK, map[string]any{"items": []discoveryItem{}, "total": 0, "disabled": true})
		return
	}
	releases := make([]domain.Release, 0, 1000)
	for offset := 0; ; offset += 500 {
		filter := domain.ReleaseFilter{Sort: "release", Direction: "desc", Limit: 500, Offset: offset}
		filter.IgnoreTags = domain.ParseIgnoreList(settings["ignore_tags"])
		filter.IgnoreTitles = domain.ParseIgnoreList(settings["ignore_titles"])
		filter.UsePreferred = len(filter.IgnoreTags) > 0 || len(filter.IgnoreTitles) > 0
		page, err := s.store.Releases(r.Context(), filter)
		if err != nil {
			s.problem(w, http.StatusInternalServerError, err.Error())
			return
		}
		releases = append(releases, page...)
		if len(page) < 500 {
			break
		}
	}
	now := time.Now().UTC()
	profile := buildAffinity(releases, settings, now)
	rewatchDays := discoveryInt(settings, "discoveries_rewatch_days", 90)
	category := strings.TrimSpace(r.URL.Query().Get("category"))
	query := r.URL.Query().Get("q")
	pool := strings.TrimSpace(r.URL.Query().Get("pool"))
	pools := discoveryPools(settings["discoveries_pools"])
	subtitles := strings.TrimSpace(r.URL.Query().Get("subtitles"))
	items := make([]discoveryItem, 0, len(releases))
	for _, release := range releases {
		itemCategory := discoveryCategory(release, rewatchDays, now)
		if category != "" && category != "all" && category != "for_you" && category != "random" && category != itemCategory && !(category == "ready" && itemCategory == "unwatched") && !(category == "needs_subtitles" && release.Local) {
			continue
		}
		if !discoveryTextMatches(release, query) {
			continue
		}
		if keywords := pools[pool]; pool != "" {
			matched := false
			for _, keyword := range keywords {
				if discoveryTextMatches(release, keyword) {
					matched = true
					break
				}
			}
			if !matched {
				continue
			}
		}
		hasSubtitle := hasSubtitleFile(release)
		if subtitles == "yes" && !hasSubtitle || subtitles == "no" && hasSubtitle || category == "ready" && !hasSubtitle || category == "needs_subtitles" && hasSubtitle {
			continue
		}
		a, actress := affinityScore(release.Actresses, profile.actress)
		g, genre := affinityScore(release.Genres, profile.genre)
		st, studio := affinityScore([]string{release.Studio}, profile.studio)
		l, label := affinityScore([]string{release.Label}, profile.label)
		score := a*.30 + g*.25 + st*.12 + l*.08
		if release.PlayCount == 0 {
			score += 8
		}
		if hasSubtitle {
			score += discoveryFloat(settings, "discoveries_subtitle_bonus", 10)
		}
		if itemCategory == "rewatch" {
			score += 6
		}
		reasons := make([]string, 0, 3)
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
		if len(reasons) == 0 {
			reasons = append(reasons, "A fresh release outside your usual history")
		}
		itemPools := []string{}
		if pool != "" {
			itemPools = append(itemPools, pool)
		}
		items = append(items, discoveryItem{Release: release, Score: math.Round(score*10) / 10, Category: itemCategory, Reasons: reasons, Pools: itemPools, HasSubtitle: hasSubtitle})
	}
	enhanced := false
	if category != "random" && category != "new" {
		items, enhanced = enhanceDiscoveries(r, settings, items)
	}
	if category == "random" {
		rng := rand.New(rand.NewSource(now.UnixNano()))
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
	total := len(items)
	limit := discoveryInt(settings, "discoveries_result_limit", 100)
	if requested := discoveryInt(map[string]string{"limit": r.URL.Query().Get("limit")}, "limit", limit); requested > 0 {
		limit = requested
	}
	if limit > 500 {
		limit = 500
	}
	if len(items) > limit {
		items = items[:limit]
	}
	mode := "deterministic"
	if enhanced {
		mode = "openai"
	}
	poolNames := make([]string, 0, len(pools))
	for name := range pools {
		poolNames = append(poolNames, name)
	}
	sort.Strings(poolNames)
	s.json(w, http.StatusOK, map[string]any{"items": items, "total": total, "generated_at": now, "mode": mode, "pools": poolNames})
}

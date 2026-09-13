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

func rankingSchema() map[string]any {
	return map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{"rankings": map[string]any{"type": "array", "items": map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{"id": map[string]any{"type": "integer"}, "score": map[string]any{"type": "number", "minimum": 0, "maximum": 100}, "reason": map[string]any{"type": "string"}, "pools": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}}, "required": []string{"id", "score", "reason", "pools"}}}}, "required": []string{"rankings"}}
}

func rankingPrompt(candidates []Candidate, pools string) string {
	data, _ := json.Marshal(candidates)
	return "Return JSON only and no Markdown or explanatory prose. Rank every supplied candidate from 0 to 100 for this user's preferences. Orgasm count is a stronger signal than play count. Use only supplied release IDs, titles, stories, studios, performers, tags and subtitle excerpts. Never invent metadata; leave unknown information empty. Custom discovery pools:\n" + pools + "\nCandidates:\n" + string(data)
}

func validateRanks(ranks []Rank, candidates []Candidate) error {
	allowed := make(map[int64]bool, len(candidates))
	for _, candidate := range candidates {
		allowed[candidate.ID] = true
	}
	if len(ranks) == 0 {
		return errors.New("empty AI result")
	}
	seen := map[int64]bool{}
	for _, rank := range ranks {
		if !allowed[rank.ID] || seen[rank.ID] || rank.Score < 0 || rank.Score > 100 || strings.TrimSpace(rank.Reason) == "" {
			return errors.New("structurally invalid AI result")
		}
		seen[rank.ID] = true
	}
	return nil
}

func parseRankingJSON(content string, candidates []Candidate) ([]Rank, error) {
	var envelope struct {
		Rankings []Rank `json:"rankings"`
	}
	if strings.TrimSpace(content) == "" {
		return nil, errors.New("empty AI result")
	}
	if err := json.Unmarshal([]byte(content), &envelope); err != nil {
		return nil, fmt.Errorf("invalid AI JSON: %w", err)
	}
	if err := validateRanks(envelope.Rankings, candidates); err != nil {
		return nil, err
	}
	return envelope.Rankings, nil
}

func (s *Service) ollamaRank(ctx context.Context, cfg Config, candidates []Candidate, pools string) ([]Rank, error) {
	timeout := cfg.RequestTimeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	body, _ := json.Marshal(map[string]any{"model": cfg.OllamaModel, "stream": false, "think": false, "format": rankingSchema(), "options": map[string]any{"temperature": 0.2}, "messages": []map[string]string{{"role": "system", "content": "You are a conservative JSON-only ranking assistant. Never invent facts."}, {"role": "user", "content": rankingPrompt(candidates, pools)}}})
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
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return nil, errors.New("Ollama returned an invalid response")
	}
	return parseRankingJSON(envelope.Message.Content, candidates)
}

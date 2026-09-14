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

func responseText(response map[string]any) string {
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

func (s *Service) openAIRank(ctx context.Context, cfg Config, candidates []Candidate, pools string) ([]Rank, error) {
	if strings.TrimSpace(cfg.OpenAIAPIKey) == "" {
		return nil, errors.New("OpenAI fallback API key is not configured")
	}
	timeout := cfg.OpenAITimeout
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	model := strings.TrimSpace(cfg.OpenAIModel)
	if model == "" {
		model = "gpt-5-mini"
	}
	body, _ := json.Marshal(map[string]any{"model": model, "input": rankingPrompt(candidates, pools), "max_output_tokens": min(max(len(candidates)*160, 2048), 32768), "truncation": "auto", "store": false, "text": map[string]any{"format": map[string]any{"type": "json_schema", "name": "discovery_rankings", "strict": true, "schema": rankingSchema(candidates)}}})
	req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, normalizeURL(cfg.OpenAIBaseURL, "https://api.openai.com/v1")+"/responses", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(cfg.OpenAIAPIKey))
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
		return nil, fmt.Errorf("OpenAI fallback returned HTTP %d", resp.StatusCode)
	}
	var envelope map[string]any
	if json.Unmarshal(data, &envelope) != nil {
		return nil, errors.New("OpenAI fallback returned an invalid response")
	}
	return parseRankingJSON(responseText(envelope), candidates, pools)
}

func (s *Service) TestOpenAI(ctx context.Context, cfg Config) (time.Duration, error) {
	if strings.TrimSpace(cfg.OpenAIAPIKey) == "" {
		return 0, errors.New("OpenAI API key is empty")
	}
	timeout := cfg.OpenAITimeout
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	model := strings.TrimSpace(cfg.OpenAIModel)
	if model == "" {
		model = "gpt-5-mini"
	}
	body, _ := json.Marshal(map[string]any{"model": model, "input": "Reply with exactly: JAVBeacon discovery test passed", "max_output_tokens": 128, "truncation": "auto", "store": false})
	req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, normalizeURL(cfg.OpenAIBaseURL, "https://api.openai.com/v1")+"/responses", bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(cfg.OpenAIAPIKey))
	req.Header.Set("Content-Type", "application/json")
	started := time.Now()
	resp, err := s.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return 0, fmt.Errorf("OpenAI returned HTTP %d", resp.StatusCode)
	}
	var envelope map[string]any
	if json.Unmarshal(data, &envelope) != nil || strings.TrimSpace(responseText(envelope)) == "" {
		return 0, errors.New("OpenAI returned an invalid response")
	}
	return time.Since(started), nil
}

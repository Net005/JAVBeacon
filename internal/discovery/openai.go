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

func (s *Service) openAIRank(ctx context.Context, cfg Config, candidates []Candidate, pools string) ([]Rank, Usage, error) {
	var totalUsage Usage
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		ranks, usage, err := s.openAIRankOnce(ctx, cfg, candidates, pools, attempt > 0, lastErr)
		totalUsage.InputTokens += usage.InputTokens
		totalUsage.OutputTokens += usage.OutputTokens
		totalUsage.TotalTokens += usage.TotalTokens
		if err == nil {
			return ranks, totalUsage, nil
		}
		lastErr = err
		var invalid validationError
		if !errors.As(err, &invalid) || attempt > 0 {
			return nil, totalUsage, err
		}
		s.log.Warn("AI Discovery: retrying rejected OpenAI result", "model", cfg.OpenAIModel, "reason", invalid.kind)
	}
	return nil, totalUsage, lastErr
}

func (s *Service) openAIRankOnce(ctx context.Context, cfg Config, candidates []Candidate, pools string, repair bool, previousErr error) ([]Rank, Usage, error) {
	if strings.TrimSpace(cfg.OpenAIAPIKey) == "" {
		return nil, Usage{}, errors.New("OpenAI API key is not configured")
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
	openAICandidates := candidates
	if !cfg.OpenAIIncludeSubtitles {
		openAICandidates = append([]Candidate(nil), candidates...)
		for index := range openAICandidates {
			openAICandidates[index].Subtitle = ""
		}
	}
	prompt := rankingPrompt(openAICandidates, pools)
	if repair {
		kind := "invalid output"
		var invalid validationError
		if errors.As(previousErr, &invalid) {
			kind = invalid.kind
		}
		prompt = "REPAIR REQUIRED: The previous response was rejected for " + kind + ". Regenerate the complete batch from scratch. Do not repeat or discuss the rejected response. Return exactly one ranking for every supplied candidate ID.\n\n" + prompt
	}
	body, _ := json.Marshal(map[string]any{"model": model, "input": prompt, "max_output_tokens": min(max(len(openAICandidates)*160, 2048), 32768), "truncation": "auto", "store": false, "text": map[string]any{"format": map[string]any{"type": "json_schema", "name": "discovery_rankings", "strict": true, "schema": rankingSchema(openAICandidates, pools)}}})
	req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, normalizeURL(cfg.OpenAIBaseURL, "https://api.openai.com/v1")+"/responses", bytes.NewReader(body))
	if err != nil {
		return nil, Usage{}, err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(cfg.OpenAIAPIKey))
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, Usage{}, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, Usage{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, Usage{}, fmt.Errorf("OpenAI returned HTTP %d", resp.StatusCode)
	}
	var envelope map[string]any
	if json.Unmarshal(data, &envelope) != nil {
		return nil, Usage{}, errors.New("OpenAI returned an invalid response")
	}
	ranks, err := parseRankingJSON(responseText(envelope), candidates, pools)
	usage := responseUsage(envelope)
	if err != nil {
		return nil, usage, err
	}
	return ranks, usage, nil
}

func responseUsage(response map[string]any) Usage {
	raw, _ := response["usage"].(map[string]any)
	usage := Usage{InputTokens: numberInt64(raw["input_tokens"]), OutputTokens: numberInt64(raw["output_tokens"]), TotalTokens: numberInt64(raw["total_tokens"])}
	if usage.TotalTokens == 0 {
		usage.TotalTokens = usage.InputTokens + usage.OutputTokens
	}
	return usage
}

func numberInt64(value any) int64 {
	switch number := value.(type) {
	case float64:
		return int64(number)
	case json.Number:
		result, _ := number.Int64()
		return result
	default:
		return 0
	}
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

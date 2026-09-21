package discovery

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func testCandidates() []Candidate {
	return []Candidate{{ID: 7, VideoID: "SSIS-123", Title: "Supplied title", Story: "Supplied story", Studio: "S1", Actresses: []string{"Example Performer"}, Genres: []string{"Sci-Fi"}, Evidence: []string{"Performer preference: Example Performer", "Studio preference: S1", "Theme preference: Sci-Fi"}}}
}

func ollamaServer(t *testing.T, models []string, chatStatus int, chatContent string, delay time.Duration) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if delay > 0 {
			time.Sleep(delay)
		}
		switch r.URL.Path {
		case "/api/tags":
			rows := make([]map[string]string, 0, len(models))
			for _, model := range models {
				rows = append(rows, map[string]string{"name": model})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"models": rows})
		case "/api/chat":
			w.WriteHeader(chatStatus)
			if chatStatus >= 200 && chatStatus < 300 {
				_ = json.NewEncoder(w).Encode(map[string]any{"message": map[string]string{"content": chatContent}})
			}
		default:
			http.NotFound(w, r)
		}
	}))
}

func baseConfig(url string) Config {
	return Config{Enabled: true, OllamaURL: url, OllamaModel: "qwen3:8b", HealthTimeout: 100 * time.Millisecond, RequestTimeout: 100 * time.Millisecond, OpenAITimeout: 100 * time.Millisecond}
}

func TestOllamaAvailabilityCheckSucceeds(t *testing.T) {
	server := ollamaServer(t, []string{"qwen3:8b", "other:latest"}, http.StatusOK, `{"rankings":[]}`, 0)
	defer server.Close()
	status := New(nil).CheckOllama(context.Background(), baseConfig(server.URL), true)
	if !status.Reachable || !status.ModelAvailable || len(status.Models) != 2 {
		t.Fatalf("unexpected status: %+v", status)
	}
}

func TestRankingSchemaRestrictsIDsToSubmittedCandidates(t *testing.T) {
	schema := rankingSchema([]Candidate{{ID: 41}, {ID: 907}}, "Sci-Fi | space\nInvestigator | detective")
	properties := schema["properties"].(map[string]any)
	rankings := properties["rankings"].(map[string]any)
	if rankings["minItems"] != 2 || rankings["maxItems"] != 2 {
		t.Fatalf("schema does not require complete batch size: %#v", rankings)
	}
	items := rankings["items"].(map[string]any)
	rankProperties := items["properties"].(map[string]any)
	idSchema := rankProperties["id"].(map[string]any)
	ids, ok := idSchema["enum"].([]int64)
	if !ok || len(ids) != 2 || ids[0] != 41 || ids[1] != 907 {
		t.Fatalf("candidate ID enum mismatch: %#v", idSchema["enum"])
	}
	poolSchema := rankProperties["pools"].(map[string]any)
	poolItems := poolSchema["items"].(map[string]any)
	poolEnum, ok := poolItems["enum"].([]string)
	if !ok || len(poolEnum) != 2 || poolEnum[0] != "Sci-Fi" || poolEnum[1] != "Investigator" {
		t.Fatalf("configured pool enum mismatch: %#v", poolItems["enum"])
	}
	reasonSchema := rankProperties["reason"].(map[string]any)
	if reasonSchema["maxLength"] != 240 {
		t.Fatalf("reason length is not constrained: %#v", reasonSchema)
	}
}

func TestOllamaLengthStopReportsUsefulFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			_ = json.NewEncoder(w).Encode(map[string]any{"models": []map[string]string{{"name": "qwen3:8b"}}})
		case "/api/chat":
			_ = json.NewEncoder(w).Encode(map[string]any{"message": map[string]string{"content": `{"rankings":[`}, "done_reason": "length"})
		}
	}))
	defer server.Close()
	result := New(nil).Rank(context.Background(), baseConfig(server.URL), testCandidates(), "")
	if !result.Skipped || !strings.Contains(result.Status, "token limit") {
		t.Fatalf("length stop did not return a useful status: %+v", result)
	}
}

func TestPromptRequiresExactCandidateIDCoverage(t *testing.T) {
	prompt := rankingPrompt([]Candidate{{ID: 41}, {ID: 907}}, "")
	for _, text := range []string{"copy candidate.id exactly", "Never invent or transform an ID", "return exactly N rankings", "must appear exactly once", `Begin every reason with "Match:"`} {
		if !strings.Contains(prompt, text) {
			t.Fatalf("prompt missing ID rule %q", text)
		}
	}
}

func TestRejectedConversationalOllamaResultGetsOneLocalRepairAttempt(t *testing.T) {
	var chatCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			_ = json.NewEncoder(w).Encode(map[string]any{"models": []map[string]string{{"name": "qwen3:8b"}}})
		case "/api/chat":
			body, _ := io.ReadAll(r.Body)
			call := chatCalls.Add(1)
			if call == 1 {
				_ = json.NewEncoder(w).Encode(map[string]any{"message": map[string]string{"content": `{"rankings":[{"id":7,"score":40,"reason":"Please provide more context so I can help you.","pools":[]}]}`}})
				return
			}
			if !strings.Contains(string(body), "REPAIR REQUIRED") || strings.Contains(string(body), "Please provide more context") {
				t.Errorf("repair request did not use safe regeneration instructions: %s", body)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"message": map[string]string{"content": `{"rankings":[{"id":7,"score":88,"reason":"Match: supplied story and Sci-Fi tag align with configured evidence.","pools":[]}]}`}})
		}
	}))
	defer server.Close()
	result := New(nil).Rank(context.Background(), baseConfig(server.URL), testCandidates(), "")
	if result.Provider != "ollama" || len(result.Ranks) != 1 || chatCalls.Load() != 2 {
		t.Fatalf("local repair failed: %+v calls=%d", result, chatCalls.Load())
	}
}

func TestOllamaAvailabilityConnectionRefused(t *testing.T) {
	server := ollamaServer(t, nil, http.StatusOK, "", 0)
	url := server.URL
	server.Close()
	status := New(nil).CheckOllama(context.Background(), baseConfig(url), true)
	if status.Reachable {
		t.Fatalf("closed server reported reachable: %+v", status)
	}
}

func TestOllamaAvailabilityTimeout(t *testing.T) {
	server := ollamaServer(t, []string{"qwen3:8b"}, http.StatusOK, "", 100*time.Millisecond)
	defer server.Close()
	cfg := baseConfig(server.URL)
	cfg.HealthTimeout = 10 * time.Millisecond
	status := New(nil).CheckOllama(context.Background(), cfg, true)
	if status.Reachable || status.Message != "Ollama connection timed out." {
		t.Fatalf("unexpected timeout status: %+v", status)
	}
}

func TestOllamaMalformedHealthResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"unexpected":true}`)) }))
	defer server.Close()
	status := New(nil).CheckOllama(context.Background(), baseConfig(server.URL), true)
	if status.Reachable {
		t.Fatalf("malformed response reported reachable: %+v", status)
	}
}

func TestOllamaOfflineNeverCallsOpenAI(t *testing.T) {
	offline := ollamaServer(t, nil, http.StatusOK, "", 0)
	url := offline.URL
	offline.Close()
	var openAICalls atomic.Int32
	openAI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { openAICalls.Add(1); w.WriteHeader(http.StatusOK) }))
	defer openAI.Close()
	cfg := baseConfig(url)
	cfg.OpenAIFallbackEnabled = true
	cfg.OpenAIAPIKey = "secret"
	cfg.OpenAIBaseURL = openAI.URL
	result := New(nil).Rank(context.Background(), cfg, testCandidates(), "")
	if !result.Skipped || openAICalls.Load() != 0 {
		t.Fatalf("offline fallback violation: %+v calls=%d", result, openAICalls.Load())
	}
}

func TestMissingOllamaModelNeverCallsOpenAI(t *testing.T) {
	ollama := ollamaServer(t, []string{"llama3:8b"}, http.StatusOK, "", 0)
	defer ollama.Close()
	var openAICalls atomic.Int32
	openAI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { openAICalls.Add(1) }))
	defer openAI.Close()
	cfg := baseConfig(ollama.URL)
	cfg.OpenAIFallbackEnabled = true
	cfg.OpenAIAPIKey = "secret"
	cfg.OpenAIBaseURL = openAI.URL
	result := New(nil).Rank(context.Background(), cfg, testCandidates(), "")
	if !result.Skipped || openAICalls.Load() != 0 {
		t.Fatalf("missing-model fallback violation: %+v calls=%d", result, openAICalls.Load())
	}
}

func TestQwenSuccessNeverCallsOpenAI(t *testing.T) {
	ollama := ollamaServer(t, []string{"qwen3:8b"}, http.StatusOK, `{"rankings":[{"id":7,"score":94,"reason":"Match: Its supplied title, story, and science-fiction tag provide strong recommendation evidence.","pools":[]}]}`, 0)
	defer ollama.Close()
	var openAICalls atomic.Int32
	openAI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { openAICalls.Add(1) }))
	defer openAI.Close()
	cfg := baseConfig(ollama.URL)
	cfg.OpenAIFallbackEnabled = true
	cfg.OpenAIAPIKey = "secret"
	cfg.OpenAIBaseURL = openAI.URL
	result := New(nil).Rank(context.Background(), cfg, testCandidates(), "")
	if result.Provider != "ollama" || len(result.Ranks) != 1 || openAICalls.Load() != 0 {
		t.Fatalf("unexpected Qwen result: %+v calls=%d", result, openAICalls.Load())
	}
}

func TestOpenAIPrimarySkipsOllamaAndReportsUsage(t *testing.T) {
	var ollamaCalls, openAICalls atomic.Int32
	ollama := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { ollamaCalls.Add(1) }))
	defer ollama.Close()
	openAI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		openAICalls.Add(1)
		content := `{"rankings":[{"id":7,"score":91,"reason":"Match: Its supplied story and familiar studio align with established viewing preferences.","pools":[]}]}`
		_ = json.NewEncoder(w).Encode(map[string]any{
			"output": []any{map[string]any{"content": []any{map[string]any{"text": content}}}},
			"usage":  map[string]any{"input_tokens": 1234, "output_tokens": 56, "total_tokens": 1290},
		})
	}))
	defer openAI.Close()
	cfg := baseConfig(ollama.URL)
	cfg.PrimaryProvider = "openai"
	cfg.OpenAIAPIKey, cfg.OpenAIBaseURL, cfg.OpenAIModel = "secret", openAI.URL, "gpt-5-mini"
	result := New(nil).Rank(context.Background(), cfg, testCandidates(), "")
	if result.Provider != "openai" || len(result.Ranks) != 1 || ollamaCalls.Load() != 0 || openAICalls.Load() != 1 {
		t.Fatalf("OpenAI primary decision flow failed: %+v ollama=%d openai=%d", result, ollamaCalls.Load(), openAICalls.Load())
	}
	if result.Usage.InputTokens != 1234 || result.Usage.OutputTokens != 56 || result.Usage.TotalTokens != 1290 {
		t.Fatalf("OpenAI usage was not returned: %+v", result.Usage)
	}
}

func TestEmptyOpenAIResultGetsOneStrictRegenerationAttempt(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		call := calls.Add(1)
		var request map[string]any
		if err := json.Unmarshal(body, &request); err != nil {
			t.Errorf("decode OpenAI request: %v", err)
		}
		reasoning, _ := request["reasoning"].(map[string]any)
		if reasoning["effort"] != "minimal" {
			t.Errorf("GPT-5 Mini reasoning effort = %#v, want minimal", reasoning["effort"])
		}
		wantTokens := 4096
		if call == 2 {
			wantTokens = 8192
		}
		if got := int(request["max_output_tokens"].(float64)); got != wantTokens {
			t.Errorf("attempt %d max_output_tokens = %d, want %d", call, got, wantTokens)
		}
		content := `{"rankings":[]}`
		if call == 2 {
			if !strings.Contains(string(body), "REPAIR REQUIRED") || !strings.Contains(string(body), "empty AI result") {
				t.Errorf("second request did not explain the safe regeneration requirement: %s", body)
			}
			content = `{"rankings":[{"id":7,"score":87,"reason":"Match: supplied story and Sci-Fi tag support this recommendation.","pools":[]}]}`
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"output": []any{map[string]any{"content": []any{map[string]any{"text": content}}}},
			"usage":  map[string]any{"input_tokens": 100, "output_tokens": 20, "total_tokens": 120},
		})
	}))
	defer server.Close()
	cfg := baseConfig("")
	cfg.PrimaryProvider, cfg.OpenAIAPIKey, cfg.OpenAIBaseURL = "openai", "secret", server.URL
	result := New(nil).Rank(context.Background(), cfg, testCandidates(), "")
	if result.Provider != "openai" || len(result.Ranks) != 1 || calls.Load() != 2 {
		t.Fatalf("OpenAI regeneration failed: %+v calls=%d", result, calls.Load())
	}
	if result.Usage.InputTokens != 200 || result.Usage.OutputTokens != 40 || result.Usage.TotalTokens != 240 {
		t.Fatalf("retry usage was not accumulated: %+v", result.Usage)
	}
}

func TestIncompleteOpenAIResponseIsRetriedWithLargerBudget(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		call := calls.Add(1)
		if call == 1 {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status":             "incomplete",
				"incomplete_details": map[string]any{"reason": "max_output_tokens"},
				"usage":              map[string]any{"input_tokens": 100, "output_tokens": 4096, "total_tokens": 4196},
			})
			return
		}
		content := `{"rankings":[{"id":7,"score":87,"reason":"Match: supplied story and Sci-Fi tag support this recommendation.","pools":[]}]}`
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "completed",
			"output": []any{map[string]any{"content": []any{map[string]any{"text": content}}}},
			"usage":  map[string]any{"input_tokens": 100, "output_tokens": 30, "total_tokens": 130},
		})
	}))
	defer server.Close()
	cfg := baseConfig("")
	cfg.PrimaryProvider, cfg.OpenAIAPIKey, cfg.OpenAIBaseURL = "openai", "secret", server.URL
	result := New(nil).Rank(context.Background(), cfg, testCandidates(), "")
	if result.Provider != "openai" || len(result.Ranks) != 1 || calls.Load() != 2 {
		t.Fatalf("incomplete OpenAI response did not recover: %+v calls=%d", result, calls.Load())
	}
	if result.Usage.TotalTokens != 4326 {
		t.Fatalf("usage across incomplete and completed attempts = %+v", result.Usage)
	}
}

func TestOpenAIPrimaryFailureDoesNotCallOllama(t *testing.T) {
	var ollamaCalls atomic.Int32
	ollama := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { ollamaCalls.Add(1) }))
	defer ollama.Close()
	cfg := baseConfig(ollama.URL)
	cfg.PrimaryProvider = "openai"
	result := New(nil).Rank(context.Background(), cfg, testCandidates(), "")
	if !result.Skipped || ollamaCalls.Load() != 0 || !strings.Contains(result.Status, "OpenAI primary failed") {
		t.Fatalf("OpenAI primary failure was not isolated: %+v ollama=%d", result, ollamaCalls.Load())
	}
}

func TestOpenAICanExcludeSubtitleEvidence(t *testing.T) {
	const subtitle = "UNIQUE SUBTITLE DIALOGUE MUST NOT LEAVE JAVBEACON"
	openAI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), subtitle) {
			t.Errorf("OpenAI request contained excluded subtitle evidence: %s", body)
		}
		content := `{"rankings":[{"id":7,"score":88,"reason":"Match: Its supplied title and studio provide clear metadata support for this recommendation.","pools":[]}]}`
		_ = json.NewEncoder(w).Encode(map[string]any{"output": []any{map[string]any{"content": []any{map[string]any{"text": content}}}}})
	}))
	defer openAI.Close()
	candidates := testCandidates()
	candidates[0].Subtitle = subtitle
	cfg := baseConfig("")
	cfg.PrimaryProvider, cfg.OpenAIAPIKey, cfg.OpenAIBaseURL = "openai", "secret", openAI.URL
	cfg.OpenAIIncludeSubtitles = false
	result := New(nil).Rank(context.Background(), cfg, candidates, "")
	if result.Provider != "openai" || len(result.Ranks) != 1 {
		t.Fatalf("metadata-only OpenAI ranking failed: %+v", result)
	}
}

func TestQwenInvalidJSONFallbackDisabled(t *testing.T) {
	ollama := ollamaServer(t, []string{"qwen3:8b"}, http.StatusOK, `not json`, 0)
	defer ollama.Close()
	result := New(nil).Rank(context.Background(), baseConfig(ollama.URL), testCandidates(), "")
	if !result.Skipped || result.Provider != "" {
		t.Fatalf("invalid JSON should skip: %+v", result)
	}
}

func TestInvalidQwenResponseIsNeverPersisted(t *testing.T) {
	var openAICalls atomic.Int32
	ollama := ollamaServer(t, []string{"qwen3:8b"}, http.StatusOK, `{"rankings":[{"id":7,"score":40,"reason":"Please provide more context so I can help you.","pools":[]}]}`, 0)
	defer ollama.Close()
	openAI := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { openAICalls.Add(1) }))
	defer openAI.Close()
	cfg := baseConfig(ollama.URL)
	cfg.OpenAIBaseURL = openAI.URL
	result := New(nil).Rank(context.Background(), cfg, testCandidates(), "")
	if !result.Skipped || len(result.Ranks) != 0 || openAICalls.Load() != 0 {
		t.Fatalf("invalid Qwen output escaped validation: %+v calls=%d", result, openAICalls.Load())
	}
}

func TestInvalidQwenResponseMayFallbackOnlyWhenOllamaWasReachable(t *testing.T) {
	ollama := ollamaServer(t, []string{"qwen3:8b"}, http.StatusOK, `{"rankings":[{"id":7,"score":40,"reason":"I cannot determine what this means.","pools":[]}]}`, 0)
	defer ollama.Close()
	var openAICalls atomic.Int32
	openAI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		openAICalls.Add(1)
		content := `{"rankings":[{"id":7,"score":86,"reason":"Match: Its supplied story and familiar studio align with established viewing preferences.","pools":[]}]}`
		_ = json.NewEncoder(w).Encode(map[string]any{"output": []any{map[string]any{"content": []any{map[string]any{"text": content}}}}})
	}))
	defer openAI.Close()
	cfg := baseConfig(ollama.URL)
	cfg.OpenAIFallbackEnabled = true
	cfg.OpenAIAPIKey = "secret"
	cfg.OpenAIBaseURL = openAI.URL
	result := New(nil).Rank(context.Background(), cfg, testCandidates(), "")
	if result.Provider != "openai" || len(result.Ranks) != 1 || openAICalls.Load() != 1 {
		t.Fatalf("eligible fallback failed: %+v calls=%d", result, openAICalls.Load())
	}
}

func TestUnknownCandidateIDMayFallbackOnlyWhenEnabled(t *testing.T) {
	ollama := ollamaServer(t, []string{"qwen3:8b"}, http.StatusOK, `{"rankings":[{"id":1,"score":80,"reason":"Strong title match.","pools":[]}]}`, 0)
	defer ollama.Close()
	var openAICalls atomic.Int32
	openAI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		openAICalls.Add(1)
		content := `{"rankings":[{"id":7,"score":86,"reason":"Match: Its supplied title and story provide clear thematic support for this recommendation.","pools":[]}]}`
		_ = json.NewEncoder(w).Encode(map[string]any{"output": []any{map[string]any{"content": []any{map[string]any{"text": content}}}}})
	}))
	defer openAI.Close()

	disabled := baseConfig(ollama.URL)
	disabled.OpenAIAPIKey, disabled.OpenAIBaseURL = "secret", openAI.URL
	result := New(nil).Rank(context.Background(), disabled, testCandidates(), "")
	if !result.Skipped || openAICalls.Load() != 0 {
		t.Fatalf("unknown ID used disabled fallback: %+v calls=%d", result, openAICalls.Load())
	}

	enabled := disabled
	enabled.OpenAIFallbackEnabled = true
	result = New(nil).Rank(context.Background(), enabled, testCandidates(), "")
	if result.Provider != "openai" || len(result.Ranks) != 1 || openAICalls.Load() != 1 {
		t.Fatalf("eligible unknown-ID fallback failed: %+v calls=%d", result, openAICalls.Load())
	}
}

func TestInvalidOpenAIFallbackResponseIsRejected(t *testing.T) {
	ollama := ollamaServer(t, []string{"qwen3:8b"}, http.StatusOK, `not json`, 0)
	defer ollama.Close()
	openAI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		content := `{"rankings":[{"id":7,"score":50,"reason":"The content you provided appears corrupted. Please clarify your request.","pools":[]}]}`
		_ = json.NewEncoder(w).Encode(map[string]any{"output": []any{map[string]any{"content": []any{map[string]any{"text": content}}}}})
	}))
	defer openAI.Close()
	cfg := baseConfig(ollama.URL)
	cfg.OpenAIFallbackEnabled = true
	cfg.OpenAIAPIKey = "secret"
	cfg.OpenAIBaseURL = openAI.URL
	result := New(nil).Rank(context.Background(), cfg, testCandidates(), "")
	if !result.Skipped || len(result.Ranks) != 0 {
		t.Fatalf("invalid OpenAI output escaped validation: %+v", result)
	}
}

func TestQwenEmptyResultFallbackDisabled(t *testing.T) {
	ollama := ollamaServer(t, []string{"qwen3:8b"}, http.StatusOK, `{"rankings":[]}`, 0)
	defer ollama.Close()
	result := New(nil).Rank(context.Background(), baseConfig(ollama.URL), testCandidates(), "")
	if !result.Skipped || result.Provider != "" {
		t.Fatalf("empty result should skip: %+v", result)
	}
}

func TestQwenInferenceHTTPErrorFallbackDisabled(t *testing.T) {
	ollama := ollamaServer(t, []string{"qwen3:8b"}, http.StatusServiceUnavailable, "", 0)
	defer ollama.Close()
	result := New(nil).Rank(context.Background(), baseConfig(ollama.URL), testCandidates(), "")
	if !result.Skipped || result.Provider != "" {
		t.Fatalf("inference error should skip without fallback: %+v", result)
	}
}

func TestQwenInferenceTimeoutIsNonFatalWithoutFallback(t *testing.T) {
	ollama := ollamaServer(t, []string{"qwen3:8b"}, http.StatusOK, `{"rankings":[{"id":7,"score":90,"reason":"late","pools":[]}]}`, 50*time.Millisecond)
	defer ollama.Close()
	cfg := baseConfig(ollama.URL)
	cfg.HealthTimeout = time.Second
	cfg.RequestTimeout = 10 * time.Millisecond
	// Prime health separately so the artificial delay applies only to the
	// inference deadline being exercised.
	service := New(nil)
	status := service.CheckOllama(context.Background(), cfg, true)
	if !status.Reachable {
		t.Fatalf("health check failed: %+v", status)
	}
	result := service.Rank(context.Background(), cfg, testCandidates(), "")
	if !result.Skipped || result.Provider != "" {
		t.Fatalf("timeout should skip cleanly: %+v", result)
	}
}

func TestQwenInferenceErrorCanUseEnabledOpenAIFallback(t *testing.T) {
	ollama := ollamaServer(t, []string{"qwen3:8b"}, http.StatusInternalServerError, "", 0)
	defer ollama.Close()
	openAI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			http.NotFound(w, r)
			return
		}
		content := `{"rankings":[{"id":7,"score":80,"reason":"Match: Its supplied title, story, and metadata provide clear support for this recommendation.","pools":[]}]}`
		_ = json.NewEncoder(w).Encode(map[string]any{"output": []any{map[string]any{"content": []any{map[string]any{"text": content}}}}})
	}))
	defer openAI.Close()
	cfg := baseConfig(ollama.URL)
	cfg.OpenAIFallbackEnabled = true
	cfg.OpenAIAPIKey = "secret"
	cfg.OpenAIBaseURL = openAI.URL
	result := New(nil).Rank(context.Background(), cfg, testCandidates(), "")
	if result.Provider != "openai" || len(result.Ranks) != 1 {
		t.Fatalf("fallback failed: %+v", result)
	}
}

func TestOpenAIFallbackFailureIsNonFatal(t *testing.T) {
	ollama := ollamaServer(t, []string{"qwen3:8b"}, http.StatusOK, "", 0)
	defer ollama.Close()
	openAI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusUnauthorized) }))
	defer openAI.Close()
	cfg := baseConfig(ollama.URL)
	cfg.OpenAIFallbackEnabled = true
	cfg.OpenAIAPIKey = "secret"
	cfg.OpenAIBaseURL = openAI.URL
	result := New(nil).Rank(context.Background(), cfg, testCandidates(), "")
	if !result.Skipped || result.Provider != "" {
		t.Fatalf("fallback failure should skip: %+v", result)
	}
}

func TestAIDisabledContactsNoProvider(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls.Add(1) }))
	defer server.Close()
	cfg := baseConfig(server.URL)
	cfg.Enabled = false
	cfg.OpenAIFallbackEnabled = true
	cfg.OpenAIBaseURL = server.URL
	result := New(nil).Rank(context.Background(), cfg, testCandidates(), "")
	if !result.Skipped || calls.Load() != 0 {
		t.Fatalf("disabled AI contacted provider: %+v calls=%d", result, calls.Load())
	}
}

func TestOllamaTestButtonNeverCallsOpenAI(t *testing.T) {
	var openAICalls atomic.Int32
	openAI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { openAICalls.Add(1) }))
	defer openAI.Close()
	ollama := ollamaServer(t, []string{"qwen3:8b"}, http.StatusOK, "", 0)
	defer ollama.Close()
	cfg := baseConfig(ollama.URL)
	cfg.OpenAIFallbackEnabled = true
	cfg.OpenAIBaseURL = openAI.URL
	_ = New(nil).CheckOllama(context.Background(), cfg, true)
	if openAICalls.Load() != 0 {
		t.Fatalf("Ollama test contacted OpenAI %d times", openAICalls.Load())
	}
}

func TestFreshOllamaCheckBypassesCachedHealth(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"models": []map[string]string{{"name": "qwen3:8b"}}})
	}))
	defer server.Close()
	service := New(nil)
	cfg := baseConfig(server.URL)
	service.CheckOllama(context.Background(), cfg, false)
	service.CheckOllama(context.Background(), cfg, false)
	service.CheckOllama(context.Background(), cfg, true)
	if calls.Load() != 2 {
		t.Fatalf("fresh check did not bypass cache: %d calls", calls.Load())
	}
}

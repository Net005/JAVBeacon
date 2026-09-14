package discovery

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
	ollama := ollamaServer(t, []string{"qwen3:8b"}, http.StatusOK, `{"rankings":[{"id":7,"score":94,"reason":"supplied title match","pools":[]}]}`, 0)
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
		content := `{"rankings":[{"id":7,"score":86,"reason":"Strong story match with preferred studio signals.","pools":[]}]}`
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
		content := `{"rankings":[{"id":7,"score":80,"reason":"fallback match","pools":[]}]}`
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

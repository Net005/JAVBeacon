package discovery

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

type cachedHealth struct {
	status OllamaStatus
	until  time.Time
}

type Service struct {
	client   *http.Client
	log      *slog.Logger
	healthMu sync.Mutex
	health   map[string]cachedHealth
}

func New(log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{client: &http.Client{}, log: log, health: map[string]cachedHealth{}}
}

func NewWithClient(client *http.Client, log *slog.Logger) *Service {
	s := New(log)
	if client != nil {
		s.client = client
	}
	return s
}

func (s *Service) CheckOllama(ctx context.Context, cfg Config, fresh bool) OllamaStatus {
	return s.checkOllama(ctx, cfg, fresh)
}

// Rank encodes the provider decision tree explicitly. In particular, an
// unavailable Ollama host or missing model returns before the OpenAI fallback
// branch can be reached.
func (s *Service) Rank(ctx context.Context, cfg Config, candidates []Candidate, pools string) Result {
	if !cfg.Enabled || len(candidates) == 0 {
		return Result{Skipped: true, Status: "AI Discovery disabled"}
	}
	s.log.Info("AI Discovery requested", "candidate_count", len(candidates))
	s.log.Debug("Checking Ollama availability", "ollama_url", normalizeURL(cfg.OllamaURL, "http://127.0.0.1:11434"))
	status := s.checkOllama(ctx, cfg, false)
	if !status.Reachable {
		s.log.Info("Ollama unavailable, AI Discovery skipped", "ollama_url", status.URL)
		return Result{Skipped: true, Status: "Ollama unavailable, AI Discovery skipped"}
	}
	if !status.ModelAvailable {
		s.log.Warn("Configured Ollama model unavailable", "model", status.Model)
		return Result{Skipped: true, Status: "Configured Ollama model is unavailable"}
	}
	s.log.Info("Using Ollama model", "model", status.Model)
	ranks, err := s.ollamaRank(ctx, cfg, candidates, pools)
	if err == nil {
		s.log.Info("Qwen Discovery completed", "candidate_count", len(candidates))
		return Result{Ranks: ranks, Provider: "ollama", Status: "Qwen Discovery completed"}
	}
	s.log.Warn("Qwen inference failed", "error", err)
	if !cfg.OpenAIFallbackEnabled {
		return Result{Skipped: true, Status: "Qwen inference failed; OpenAI fallback disabled"}
	}
	s.log.Info("Using OpenAI fallback after Qwen inference failure")
	ranks, fallbackErr := s.openAIRank(ctx, cfg, candidates, pools)
	if fallbackErr != nil {
		s.log.Warn("OpenAI fallback failed", "error", fallbackErr)
		return Result{Skipped: true, Status: "OpenAI fallback failed: " + fallbackErr.Error()}
	}
	return Result{Ranks: ranks, Provider: "openai", Status: "OpenAI fallback completed"}
}

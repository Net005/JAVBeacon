package discovery

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
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
	if strings.EqualFold(strings.TrimSpace(cfg.PrimaryProvider), "openai") {
		s.log.Info("Using OpenAI as primary AI Discovery provider", "model", cfg.OpenAIModel)
		ranks, usage, err := s.openAIRank(ctx, cfg, candidates, pools)
		if err != nil {
			s.log.Warn("OpenAI primary Discovery failed", "error", err)
			return Result{AttemptedProvider: "openai", Usage: usage, Skipped: true, Status: "OpenAI primary failed: " + err.Error()}
		}
		return Result{Ranks: ranks, Provider: "openai", Usage: usage, Status: "OpenAI Discovery completed"}
	}
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
		s.log.Info("Ollama Discovery completed", "model", status.Model, "candidate_count", len(candidates))
		return Result{Ranks: ranks, Provider: "ollama", Status: "Ollama Discovery completed"}
	}
	var invalid validationError
	if errors.As(err, &invalid) {
		s.log.Warn("AI Discovery: Ollama result rejected", "model", status.Model, "reason", invalid.kind)
	} else {
		s.log.Warn("Ollama inference failed", "model", status.Model, "error", err)
	}
	failureStatus := ollamaFailureStatus(err, cfg.RequestTimeout)
	if !cfg.OpenAIFallbackEnabled {
		return Result{Skipped: true, Status: failureStatus + "; OpenAI fallback disabled"}
	}
	s.log.Info("Using OpenAI fallback after Ollama inference failure", "model", status.Model)
	ranks, usage, fallbackErr := s.openAIRank(ctx, cfg, candidates, pools)
	if fallbackErr != nil {
		s.log.Warn("OpenAI fallback failed", "error", fallbackErr)
		return Result{AttemptedProvider: "openai", Usage: usage, Skipped: true, Status: "OpenAI fallback failed: " + fallbackErr.Error()}
	}
	return Result{Ranks: ranks, Provider: "openai", Usage: usage, Status: "OpenAI fallback completed"}
}

func ollamaFailureStatus(err error, timeout time.Duration) string {
	var invalid validationError
	if errors.As(err, &invalid) {
		return "Ollama result rejected: " + invalid.kind
	}
	if errors.Is(err, context.DeadlineExceeded) {
		if timeout <= 0 {
			timeout = 60 * time.Second
		}
		return fmt.Sprintf("Ollama inference timed out after %s", timeout)
	}
	message := err.Error()
	if message == "Ollama output reached its token limit before completing JSON" || strings.HasPrefix(message, "Ollama inference returned HTTP ") {
		return message
	}
	return "Ollama request failed"
}

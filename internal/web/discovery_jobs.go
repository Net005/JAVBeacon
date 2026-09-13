package web

import (
	"context"
	"errors"
	"log/slog"
	"maps"
	"net/http"
	"sync"
	"time"

	"github.com/Net005/JAVBeacon/internal/domain"
	"github.com/Net005/JAVBeacon/internal/store"
)

type discoveryJobStatus struct {
	Running       bool      `json:"running"`
	Mode          string    `json:"mode,omitempty"`
	Stage         string    `json:"stage,omitempty"`
	StartedAt     time.Time `json:"started_at,omitempty"`
	FinishedAt    time.Time `json:"finished_at,omitempty"`
	LastSyncedAt  time.Time `json:"last_synced_at,omitempty"`
	NextSyncAt    time.Time `json:"next_sync_at,omitempty"`
	Total         int       `json:"total"`
	Completed     int       `json:"completed"`
	SubtitleCount int       `json:"subtitle_count"`
	Error         string    `json:"error,omitempty"`
}

var discoveryJobs = struct {
	sync.RWMutex
	status discoveryJobStatus
}{}

func discoveryInterval(settings map[string]string) time.Duration {
	interval, err := domain.ParseScheduleDuration(settings["discoveries_refresh_interval"])
	if err != nil || interval < time.Minute {
		return 24 * time.Hour
	}
	return interval
}

func discoveryJobSnapshot(settings map[string]string) discoveryJobStatus {
	discoveryJobs.RLock()
	status := discoveryJobs.status
	discoveryJobs.RUnlock()
	if status.LastSyncedAt.IsZero() {
		status.LastSyncedAt, _ = time.Parse(time.RFC3339Nano, settings["discoveries_last_synced_at"])
	}
	if !status.LastSyncedAt.IsZero() {
		status.NextSyncAt = status.LastSyncedAt.Add(discoveryInterval(settings))
	} else {
		status.NextSyncAt = time.Now().Add(discoveryInterval(settings))
	}
	return status
}

func startDiscoveryJob(ctx context.Context, st store.Store, log *slog.Logger, mode string) error {
	discoveryJobs.Lock()
	if discoveryJobs.status.Running {
		discoveryJobs.Unlock()
		return errors.New("a Discoveries refresh is already running")
	}
	discoveryJobs.status = discoveryJobStatus{Running: true, Mode: mode, Stage: "Loading releases", StartedAt: time.Now().UTC()}
	discoveryJobs.Unlock()
	go func() {
		jobContext := context.WithoutCancel(ctx)
		settings, _ := st.Settings(jobContext)
		finish := func(err error) {
			var synchronizedAt time.Time
			discoveryJobs.Lock()
			discoveryJobs.status.Running = false
			discoveryJobs.status.FinishedAt = time.Now().UTC()
			if err != nil {
				discoveryJobs.status.Stage = "Failed"
				discoveryJobs.status.Error = err.Error()
			} else {
				discoveryJobs.status.Stage = "Synchronized"
				discoveryJobs.status.LastSyncedAt = discoveryJobs.status.FinishedAt
				synchronizedAt = discoveryJobs.status.LastSyncedAt
			}
			discoveryJobs.Unlock()
			if !synchronizedAt.IsZero() {
				if saveErr := st.SaveSettings(jobContext, map[string]string{"discoveries_last_synced_at": synchronizedAt.Format(time.RFC3339Nano)}); saveErr != nil && log != nil {
					log.Warn("Could not persist Discoveries synchronization time", "error", saveErr)
				}
			}
			if err != nil && log != nil {
				log.Error("Discoveries refresh failed", "mode", mode, "error", err)
			}
		}
		releases := make([]domain.Release, 0, 1000)
		for offset := 0; ; offset += 500 {
			page, err := st.Releases(jobContext, domain.ReleaseFilter{Sort: "release", Direction: "desc", Limit: 500, Offset: offset, ShowNonPreferred: true})
			if err != nil {
				finish(err)
				return
			}
			releases = append(releases, page...)
			discoveryJobs.Lock()
			discoveryJobs.status.Completed = len(releases)
			discoveryJobs.status.Total = len(releases)
			discoveryJobs.Unlock()
			if len(page) < 500 {
				break
			}
		}
		discoverySubtitleCache.RLock()
		availability := maps.Clone(discoverySubtitleCache.availability)
		subtitleCreated := discoverySubtitleCache.created
		discoverySubtitleCache.RUnlock()
		subtitleDue := mode == "manual" || availability == nil || time.Since(subtitleCreated) >= discoveryDuration(settings, "discoveries_subtitle_refresh_interval", 6*time.Hour)
		if subtitleDue {
			discoveryJobs.Lock()
			discoveryJobs.status.Stage = "Indexing subtitle availability"
			discoveryJobs.status.Completed = 0
			discoveryJobs.status.Total = len(releases)
			discoveryJobs.Unlock()
			availability = subtitleAvailabilityWithProgress(releases, func(completed int) {
				discoveryJobs.Lock()
				discoveryJobs.status.Completed = completed
				discoveryJobs.Unlock()
			})
			discoverySubtitleCache.Lock()
			discoverySubtitleCache.created = time.Now()
			discoverySubtitleCache.availability = availability
			discoverySubtitleCache.Unlock()
		} else {
			discoveryJobs.Lock()
			discoveryJobs.status.Stage = "Using current subtitle index"
			discoveryJobs.status.Completed = len(releases)
			discoveryJobs.status.Total = len(releases)
			discoveryJobs.Unlock()
		}
		if mode == "manual" {
			discoveryRankCache.Lock()
			discoveryRankCache.entries = map[[32]byte]discoveryRankCacheEntry{}
			discoveryRankCache.Unlock()
		}
		discoveryJobs.Lock()
		discoveryJobs.status.Completed = len(releases)
		discoveryJobs.status.SubtitleCount = len(availability)
		discoveryJobs.Unlock()
		finish(nil)
	}()
	return nil
}

// ScheduleDiscoveries runs the independently configurable Discoveries index
// schedule. The OpenAI ranking itself remains lazy/background and uses its own
// cache, so a scheduled index refresh never blocks on an external service.
func ScheduleDiscoveries(ctx context.Context, st store.Store, log *slog.Logger) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		settings, err := st.Settings(ctx)
		if err == nil && settings["discoveries_enabled"] == "true" && settings["discoveries_refresh_enabled"] == "true" {
			status := discoveryJobSnapshot(settings)
			if status.LastSyncedAt.IsZero() || (!status.Running && !status.NextSyncAt.After(time.Now())) {
				_ = startDiscoveryJob(ctx, st, log, "scheduled")
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Server) discoveryJob(w http.ResponseWriter, r *http.Request) {
	settings, _ := s.store.Settings(r.Context())
	if r.Method == http.MethodGet {
		s.json(w, http.StatusOK, discoveryJobSnapshot(settings))
		return
	}
	if err := startDiscoveryJob(r.Context(), s.store, s.log, "manual"); err != nil {
		s.problem(w, http.StatusConflict, err.Error())
		return
	}
	s.json(w, http.StatusAccepted, discoveryJobSnapshot(settings))
}

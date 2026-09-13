package web

import (
	"context"
	"errors"
	"log/slog"
	"maps"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Net005/JAVBeacon/internal/domain"
	"github.com/Net005/JAVBeacon/internal/monitor"
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

func discoveryScheduleMode(settings map[string]string) string {
	return monitor.NormalizeScheduleMode(settings["discoveries_schedule_mode"], settings["discoveries_start_time"], settings["discoveries_weekdays"], settings["discoveries_cron"])
}

func discoveryNextRuns(settings map[string]string, now time.Time, count int) []time.Time {
	if settings["discoveries_enabled"] != "true" || settings["discoveries_refresh_enabled"] != "true" || count <= 0 {
		return nil
	}
	interval := discoveryInterval(settings)
	switch discoveryScheduleMode(settings) {
	case "cron":
		return monitor.NextCalendarRuns(now, "", "", settings["discoveries_cron"], count)
	case "advanced":
		return monitor.NextAdvancedRuns(now, settings["discoveries_start_time"], settings["discoveries_weekdays"], interval, count)
	default:
		next := time.Time{}
		if strings.TrimSpace(settings["discoveries_start_time"]) == "" {
			if last, err := time.Parse(time.RFC3339Nano, settings["discoveries_last_synced_at"]); err == nil {
				next = last.Add(interval)
				for !next.After(now) {
					next = next.Add(interval)
				}
			}
		}
		if next.IsZero() {
			next = monitor.NextBasicRun(now, interval, settings["discoveries_start_time"])
		}
		runs := make([]time.Time, 0, count)
		for len(runs) < count {
			runs = append(runs, next)
			next = next.Add(interval)
		}
		return runs
	}
}

func discoveryJobSnapshot(settings map[string]string) discoveryJobStatus {
	discoveryJobs.RLock()
	status := discoveryJobs.status
	discoveryJobs.RUnlock()
	if status.LastSyncedAt.IsZero() {
		status.LastSyncedAt, _ = time.Parse(time.RFC3339Nano, settings["discoveries_last_synced_at"])
	}
	if runs := discoveryNextRuns(settings, time.Now(), 1); len(runs) > 0 {
		status.NextSyncAt = runs[0]
	} else {
		status.NextSyncAt = time.Time{}
	}
	return status
}

func startDiscoveryJob(ctx context.Context, st store.Store, log *slog.Logger, mode string) error {
	jobStartedAt := time.Now().UTC()
	discoveryJobs.Lock()
	if discoveryJobs.status.Running {
		discoveryJobs.Unlock()
		return errors.New("a Discoveries refresh is already running")
	}
	discoveryJobs.status = discoveryJobStatus{Running: true, Mode: mode, Stage: "Loading changed releases", StartedAt: jobStartedAt}
	discoveryJobs.Unlock()
	go func() {
		jobContext := context.WithoutCancel(ctx)
		settings, _ := st.Settings(jobContext)
		cursor, _ := time.Parse(time.RFC3339Nano, settings["discoveries_incremental_cursor_at"])
		if cursor.IsZero() {
			// A successful synchronization from versions before the dedicated
			// cursor was introduced is already a valid incremental baseline.
			cursor, _ = time.Parse(time.RFC3339Nano, settings["discoveries_last_synced_at"])
		}
		fullRefresh := mode == "manual" || cursor.IsZero()
		if fullRefresh {
			discoveryJobs.Lock()
			discoveryJobs.status.Stage = "Loading releases"
			discoveryJobs.Unlock()
		}
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
				if saveErr := st.SaveSettings(jobContext, map[string]string{"discoveries_last_synced_at": synchronizedAt.Format(time.RFC3339Nano), "discoveries_incremental_cursor_at": jobStartedAt.Format(time.RFC3339Nano)}); saveErr != nil && log != nil {
					log.Warn("Could not persist Discoveries synchronization time", "error", saveErr)
				}
			}
			if err != nil && log != nil {
				log.Error("Discoveries refresh failed", "mode", mode, "error", err)
			}
		}
		releases := make([]domain.Release, 0, 1000)
		for offset := 0; ; offset += 500 {
			filter := domain.ReleaseFilter{Sort: "updated", Direction: "desc", Limit: 500, Offset: offset, ShowNonPreferred: true}
			if !fullRefresh {
				filter.UpdatedAfter = cursor
			}
			page, err := st.Releases(jobContext, filter)
			if err != nil {
				finish(err)
				return
			}
			releases = append(releases, page...)
			discoveryJobs.Lock()
			discoveryJobs.status.Completed = len(releases)
			// The total is not known until the final page has been loaded. Keep
			// it at zero so the UI does not present each intermediate batch as
			// a misleading 100% complete current/total value.
			discoveryJobs.status.Total = 0
			discoveryJobs.Unlock()
			if len(page) < 500 {
				break
			}
		}
		discoverySubtitleCache.RLock()
		availability := maps.Clone(discoverySubtitleCache.availability)
		checked := maps.Clone(discoverySubtitleCache.checked)
		discoverySubtitleCache.RUnlock()
		subtitleDue := fullRefresh || (availability != nil && len(releases) > 0)
		if subtitleDue {
			discoveryJobs.Lock()
			discoveryJobs.status.Stage = "Indexing subtitle availability"
			discoveryJobs.status.Completed = 0
			discoveryJobs.status.Total = len(releases)
			discoveryJobs.Unlock()
			changedAvailability := subtitleAvailabilityWithProgress(releases, func(completed int) {
				discoveryJobs.Lock()
				discoveryJobs.status.Completed = completed
				discoveryJobs.Unlock()
			})
			if fullRefresh {
				availability = changedAvailability
				checked = make(map[int64]bool, len(releases))
			} else {
				for _, release := range releases {
					delete(availability, release.ID)
				}
				for releaseID, present := range changedAvailability {
					availability[releaseID] = present
				}
			}
			for _, release := range releases {
				checked[release.ID] = true
			}
			discoverySubtitleCache.Lock()
			discoverySubtitleCache.created = time.Now()
			discoverySubtitleCache.availability = availability
			discoverySubtitleCache.checked = checked
			discoverySubtitleCache.Unlock()
		} else if availability != nil {
			discoveryJobs.Lock()
			discoveryJobs.status.Stage = "Using current subtitle index"
			discoveryJobs.status.Completed = len(releases)
			discoveryJobs.status.Total = len(releases)
			discoveryJobs.Unlock()
		} else {
			discoveryJobs.Lock()
			discoveryJobs.status.Stage = "Changed releases synchronized"
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
	var basicNext time.Time
	var basicSignature, lastCalendarMinute string
	for {
		settings, err := st.Settings(ctx)
		if err == nil && settings["discoveries_enabled"] == "true" && settings["discoveries_refresh_enabled"] == "true" {
			now := time.Now()
			status := discoveryJobSnapshot(settings)
			mode := discoveryScheduleMode(settings)
			interval := discoveryInterval(settings)
			if mode == "cron" || mode == "advanced" {
				cronText := ""
				if mode == "cron" {
					cronText = settings["discoveries_cron"]
				}
				minuteKey := now.Format("200601021504")
				matches, matchErr := monitor.CalendarScheduleMatches(now, settings["discoveries_start_time"], settings["discoveries_weekdays"], cronText)
				dueByInterval := mode == "cron" || status.LastSyncedAt.IsZero() || now.Sub(status.LastSyncedAt) >= interval
				if matchErr != nil {
					if log != nil {
						log.Error("invalid Discoveries schedule", "error", matchErr)
					}
				} else if matches && dueByInterval && lastCalendarMinute != minuteKey && !status.Running {
					lastCalendarMinute = minuteKey
					_ = startDiscoveryJob(ctx, st, log, "scheduled")
				}
			} else {
				signature := interval.String() + "|" + strings.TrimSpace(settings["discoveries_start_time"])
				if basicSignature != signature || basicNext.IsZero() {
					basicSignature = signature
					if strings.TrimSpace(settings["discoveries_start_time"]) == "" && !status.LastSyncedAt.IsZero() {
						basicNext = status.LastSyncedAt.Add(interval)
					} else {
						basicNext = monitor.NextBasicRun(now, interval, settings["discoveries_start_time"])
					}
				}
				if !now.Before(basicNext) {
					if !status.Running {
						_ = startDiscoveryJob(ctx, st, log, "scheduled")
					}
					for !basicNext.After(now) {
						basicNext = basicNext.Add(interval)
					}
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func discoveryScheduleForecast(ctx context.Context, st store.Store) domain.ScheduleForecast {
	settings, _ := st.Settings(ctx)
	enabled := settings["discoveries_enabled"] == "true" && settings["discoveries_refresh_enabled"] == "true"
	mode := discoveryScheduleMode(settings)
	interval := discoveryInterval(settings)
	forecast := domain.ScheduleForecast{Group: "Discoveries", Name: "Discovery synchronization", Enabled: enabled}
	switch mode {
	case "cron":
		forecast.Interval = "cron: " + strings.TrimSpace(settings["discoveries_cron"])
	case "advanced":
		forecast.Interval = "advanced: " + interval.String()
	default:
		forecast.Interval = "basic: " + interval.String()
	}
	forecast.NextRuns = discoveryNextRuns(settings, time.Now(), 3)
	return forecast
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

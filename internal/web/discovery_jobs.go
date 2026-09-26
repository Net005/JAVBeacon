package web

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"maps"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Net005/JAVBeacon/internal/domain"
	"github.com/Net005/JAVBeacon/internal/monitor"
	"github.com/Net005/JAVBeacon/internal/store"
)

type discoveryJobStatus struct {
	Running         bool      `json:"running"`
	Mode            string    `json:"mode,omitempty"`
	Stage           string    `json:"stage,omitempty"`
	StartedAt       time.Time `json:"started_at,omitempty"`
	StageStartedAt  time.Time `json:"stage_started_at,omitempty"`
	FinishedAt      time.Time `json:"finished_at,omitempty"`
	LastSyncedAt    time.Time `json:"last_synced_at,omitempty"`
	NextSyncAt      time.Time `json:"next_sync_at,omitempty"`
	Total           int       `json:"total"`
	Completed       int       `json:"completed"`
	SubtitleCount   int       `json:"subtitle_count"`
	Error           string    `json:"error,omitempty"`
	SubtitleLastRun time.Time `json:"subtitle_last_run_at,omitempty"`
	// SubtitleScanned/SubtitleUnreadableDirs/SubtitleMissingPath describe the
	// most recently completed subtitle availability scan. Unlike
	// Total/Completed (which later job stages reuse and overwrite), these
	// persist until the next subtitle scan runs, so the Settings page can
	// always show what the last scan actually found - most importantly,
	// whether "0 subtitles" means none exist or the scan couldn't read them.
	SubtitleScanned        int       `json:"subtitle_scanned"`
	SubtitleUnreadableDirs int       `json:"subtitle_unreadable_directories"`
	SubtitleMissingPath    int       `json:"subtitle_missing_file_path"`
	OpenAILastRun          time.Time `json:"openai_last_run_at,omitempty"`
	OpenAIRunning          bool      `json:"openai_running"`
	OpenAICompleted        int       `json:"openai_completed"`
	OpenAITotal            int       `json:"openai_total"`
	OpenAIBatch            int       `json:"openai_batch"`
	OpenAIBatches          int       `json:"openai_batches"`
	OpenAICurrent          int       `json:"openai_current"`
	OpenAIError            string    `json:"openai_error,omitempty"`
	CurrentItem            string    `json:"current_item,omitempty"`
	ElapsedSeconds         float64   `json:"elapsed_seconds"`
	ItemsPerSecond         float64   `json:"items_per_second"`
	ETASeconds             float64   `json:"eta_seconds"`
	OpenAIStartedAt        time.Time `json:"openai_started_at,omitempty"`
	OpenAIBatchAt          time.Time `json:"openai_batch_started_at,omitempty"`
	OpenAIItems            []string  `json:"openai_current_items,omitempty"`
	OpenAIElapsed          float64   `json:"openai_elapsed_seconds"`
	OpenAIBatchTime        float64   `json:"openai_batch_elapsed_seconds"`
	OpenAILastBatch        float64   `json:"openai_last_batch_seconds"`
	OpenAIRate             float64   `json:"openai_items_per_second"`
	OpenAIETA              float64   `json:"openai_eta_seconds"`
	AIProvider             string    `json:"ai_provider,omitempty"`
	AIModel                string    `json:"ai_model,omitempty"`
	AIInputTokens          int64     `json:"ai_input_tokens"`
	AIOutputTokens         int64     `json:"ai_output_tokens"`
	AICostUSD              float64   `json:"ai_estimated_cost_usd"`
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
	status.SubtitleLastRun, _ = time.Parse(time.RFC3339Nano, settings["discoveries_subtitle_last_run_at"])
	status.OpenAILastRun, _ = time.Parse(time.RFC3339Nano, settings["discoveries_openai_last_run_at"])
	now := time.Now().UTC()
	if status.Running && !status.StartedAt.IsZero() {
		stageStartedAt := status.StageStartedAt
		if stageStartedAt.IsZero() {
			stageStartedAt = status.StartedAt
		}
		status.ElapsedSeconds = now.Sub(stageStartedAt).Seconds()
		if status.Completed > 0 && status.ElapsedSeconds > 0 {
			status.ItemsPerSecond = float64(status.Completed) / status.ElapsedSeconds
			if status.Total > status.Completed {
				status.ETASeconds = float64(status.Total-status.Completed) / status.ItemsPerSecond
			}
		}
	} else if !status.StartedAt.IsZero() && !status.FinishedAt.IsZero() {
		status.ElapsedSeconds = status.FinishedAt.Sub(status.StartedAt).Seconds()
		if status.Completed > 0 && status.ElapsedSeconds > 0 {
			status.ItemsPerSecond = float64(status.Completed) / status.ElapsedSeconds
		}
	}
	discoveryAIStatus.RLock()
	status.OpenAIRunning, status.OpenAICompleted, status.OpenAITotal, status.OpenAIBatch, status.OpenAIBatches, status.OpenAICurrent, status.OpenAIError = discoveryAIStatus.Running, discoveryAIStatus.Completed, discoveryAIStatus.Total, discoveryAIStatus.Batch, discoveryAIStatus.Batches, discoveryAIStatus.Current, discoveryAIStatus.Error
	status.OpenAIStartedAt, status.OpenAIBatchAt, status.OpenAILastBatch = discoveryAIStatus.StartedAt, discoveryAIStatus.BatchStartedAt, discoveryAIStatus.LastBatchSeconds
	status.OpenAIItems = append([]string(nil), discoveryAIStatus.CurrentItems...)
	status.AIProvider, status.AIModel = discoveryAIStatus.Provider, discoveryAIStatus.Model
	status.AIInputTokens, status.AIOutputTokens, status.AICostUSD = discoveryAIStatus.InputTokens, discoveryAIStatus.OutputTokens, discoveryAIStatus.EstimatedCostUSD
	openAIStartingCompleted := discoveryAIStatus.StartingCompleted
	discoveryAIStatus.RUnlock()
	if !status.OpenAIRunning && status.AIProvider == "" {
		status.AIProvider = strings.TrimSpace(settings["discoveries_ai_last_provider"])
		status.AIModel = strings.TrimSpace(settings["discoveries_ai_last_model"])
		status.AIInputTokens, _ = strconv.ParseInt(settings["discoveries_ai_last_input_tokens"], 10, 64)
		status.AIOutputTokens, _ = strconv.ParseInt(settings["discoveries_ai_last_output_tokens"], 10, 64)
		status.AICostUSD, _ = strconv.ParseFloat(settings["discoveries_ai_last_estimated_cost_usd"], 64)
	}
	if status.OpenAIRunning && !status.OpenAIStartedAt.IsZero() {
		status.OpenAIElapsed = now.Sub(status.OpenAIStartedAt).Seconds()
		if !status.OpenAIBatchAt.IsZero() {
			status.OpenAIBatchTime = now.Sub(status.OpenAIBatchAt).Seconds()
		}
		processed := status.OpenAICompleted - openAIStartingCompleted
		if processed > 0 && status.OpenAIElapsed > 0 {
			status.OpenAIRate = float64(processed) / status.OpenAIElapsed
			if status.OpenAITotal > status.OpenAICompleted {
				status.OpenAIETA = float64(status.OpenAITotal-status.OpenAICompleted) / status.OpenAIRate
			}
		}
	}
	return status
}

func startDiscoveryJob(ctx context.Context, st store.Store, log *slog.Logger, mode string) error {
	jobStartedAt := time.Now().UTC()
	discoverySubtitleCache.RLock()
	previousSubtitleCount := len(discoverySubtitleCache.availability)
	discoverySubtitleCache.RUnlock()
	discoveryJobs.Lock()
	if discoveryJobs.status.Running {
		discoveryJobs.Unlock()
		return errors.New("a Discoveries refresh is already running")
	}
	discoveryJobs.status = discoveryJobStatus{Running: true, Mode: mode, Stage: "Loading changed releases", StartedAt: jobStartedAt, StageStartedAt: jobStartedAt, SubtitleCount: previousSubtitleCount}
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
		fullRefresh := mode == "manual" || strings.HasPrefix(mode, "subtitle-") || cursor.IsZero()
		// Upgrades from versions before the persistent recommendation index
		// need one complete pass. Without it, an incremental scheduled run
		// would score only recently changed releases and leave older catalog
		// rows with no globally sortable score.
		if scoreCount, err := st.DiscoveryScoreCount(jobContext); err == nil && scoreCount == 0 {
			fullRefresh = true
		}
		if fullRefresh {
			discoveryJobs.Lock()
			discoveryJobs.status.Stage = "Loading releases"
			discoveryJobs.status.StageStartedAt = time.Now().UTC()
			discoveryJobs.Unlock()
		}
		countFilter := domain.ReleaseFilter{ShowNonPreferred: true}
		if !fullRefresh {
			countFilter.UpdatedAfter = cursor
		}
		if total, err := st.ReleasesCount(jobContext, countFilter); err == nil {
			discoveryJobs.Lock()
			discoveryJobs.status.Total = total
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
				synchronizedAt = discoveryJobs.status.FinishedAt
				if strings.HasPrefix(mode, "subtitle-") {
					discoveryJobs.status.SubtitleLastRun = synchronizedAt
				} else {
					discoveryJobs.status.LastSyncedAt = synchronizedAt
				}
			}
			discoveryJobs.Unlock()
			if !synchronizedAt.IsZero() {
				discoveryResultCache.Lock()
				discoveryResultCache.created = time.Time{}
				discoveryResultCache.items = nil
				discoveryResultCache.Unlock()
				values := map[string]string{"discoveries_last_synced_at": synchronizedAt.Format(time.RFC3339Nano), "discoveries_incremental_cursor_at": jobStartedAt.Format(time.RFC3339Nano)}
				if strings.HasPrefix(mode, "subtitle-") {
					values = map[string]string{"discoveries_subtitle_last_run_at": synchronizedAt.Format(time.RFC3339Nano)}
				}
				if saveErr := st.SaveSettings(jobContext, values); saveErr != nil && log != nil {
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
			discoveryJobs.status.CurrentItem = "Database page " + strconv.Itoa(offset/500+1)
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
			discoveryJobs.status.StageStartedAt = time.Now().UTC()
			discoveryJobs.status.Completed = 0
			discoveryJobs.status.Total = len(releases)
			discoveryJobs.status.CurrentItem = "Filesystem subtitle paths"
			discoveryJobs.Unlock()
			changedAvailability, subtitleStats := scanSubtitleAvailability(discoveryRemapReleases(releases, settings["stash_missing_path_remaps"]), func(completed, found int) {
				discoveryJobs.Lock()
				discoveryJobs.status.Completed = completed
				discoveryJobs.status.SubtitleCount = found
				discoveryJobs.Unlock()
			})
			discoveryJobs.Lock()
			discoveryJobs.status.SubtitleScanned = len(releases)
			discoveryJobs.status.SubtitleUnreadableDirs = subtitleStats.UnreadableDirectories
			discoveryJobs.status.SubtitleMissingPath = subtitleStats.MissingFilePath
			discoveryJobs.Unlock()
			if log != nil {
				if subtitleStats.UnreadableDirectories > 0 {
					log.Warn("Discovery subtitle scan could not read media directories", "unreadable", subtitleStats.UnreadableDirectories, "directories", subtitleStats.Directories)
				}
				if subtitleStats.MissingFilePath > 0 {
					log.Warn("Discovery subtitle scan skipped releases with no recorded file path", "missing_file_path", subtitleStats.MissingFilePath, "scanned", len(releases))
				}
			}
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
			discoveryJobs.status.StageStartedAt = time.Now().UTC()
			discoveryJobs.status.CurrentItem = "Cached subtitle index"
			discoveryJobs.status.Completed = len(releases)
			discoveryJobs.status.Total = len(releases)
			discoveryJobs.Unlock()
		} else {
			discoveryJobs.Lock()
			discoveryJobs.status.Stage = "Changed releases synchronized"
			discoveryJobs.status.StageStartedAt = time.Now().UTC()
			discoveryJobs.status.CurrentItem = "No subtitle rescan needed"
			discoveryJobs.status.Completed = len(releases)
			discoveryJobs.status.Total = len(releases)
			discoveryJobs.Unlock()
		}
		if mode == "manual" {
			discoveryRankCache.Lock()
			discoveryRankCache.entries = map[[32]byte]discoveryRankCacheEntry{}
			discoveryRankCache.Unlock()
		}
		if len(releases) > 0 {
			discoveryJobs.Lock()
			discoveryJobs.status.Stage = "Updating recommendation scores"
			discoveryJobs.status.StageStartedAt = time.Now().UTC()
			discoveryJobs.status.Completed = 0
			discoveryJobs.status.Total = len(releases)
			discoveryJobs.status.CurrentItem = "Building affinity profile"
			discoveryJobs.Unlock()
			profileReleases := make([]domain.Release, 0, 1000)
			for offset := 0; ; offset += 500 {
				page, err := st.Releases(jobContext, domain.ReleaseFilter{Status: "local", Sort: "updated", Direction: "desc", Limit: 500, Offset: offset, ShowNonPreferred: true})
				if err != nil {
					finish(err)
					return
				}
				profileReleases = append(profileReleases, page...)
				if len(page) < 500 {
					break
				}
			}
			profileReleases, err := archivedAffinityReleases(jobContext, st, profileReleases)
			if err != nil {
				finish(err)
				return
			}
			excluded := discoveryExcludedTags(settings["discoveries_excluded_tags"])
			eligible := profileReleases[:0]
			for _, release := range profileReleases {
				if !discoveryHasExcludedTag(release, excluded) {
					eligible = append(eligible, release)
				}
			}
			profile := buildAffinity(eligible, settings, time.Now().UTC())
			rewatchDays := discoveryInt(settings, "discoveries_rewatch_days", 90)
			scores := make(map[int64]float64, len(releases))
			for index, release := range releases {
				score, _ := scoreDiscoveryRelease(release, profile, availability[release.ID], settings, rewatchDays, time.Now().UTC())
				scores[release.ID] = score
				if index%250 == 0 || index == len(releases)-1 {
					discoveryJobs.Lock()
					discoveryJobs.status.Completed = index + 1
					discoveryJobs.status.CurrentItem = release.VideoID
					discoveryJobs.Unlock()
				}
			}
			if err := st.SaveDiscoveryScores(jobContext, scores); err != nil {
				finish(err)
				return
			}
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
	var basicSignature, lastCalendarMinute, lastSubtitleMinute, lastOpenAIMinute string
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
			minuteKey := now.Format("200601021504")
			if settings["discoveries_subtitle_refresh_enabled"] == "true" && lastSubtitleMinute != minuteKey && discoveryAuxScheduleDue(settings, "discoveries_subtitle", now) {
				lastSubtitleMinute = minuteKey
				if !discoveryJobSnapshot(settings).Running {
					_ = startDiscoveryJob(ctx, st, log, "subtitle-scheduled")
					_ = st.SaveSettings(ctx, map[string]string{"discoveries_subtitle_last_run_at": now.UTC().Format(time.RFC3339Nano)})
				}
			}
			if settings["discoveries_openai_refresh_enabled"] == "true" && lastOpenAIMinute != minuteKey && discoveryAuxScheduleDue(settings, "discoveries_openai", now) {
				lastOpenAIMinute = minuteKey
				discoveryRankCache.Lock()
				discoveryRankCache.entries = map[[32]byte]discoveryRankCacheEntry{}
				discoveryRankCache.Unlock()
				_ = st.SaveSettings(ctx, map[string]string{"discoveries_openai_last_run_at": now.UTC().Format(time.RFC3339Nano)})
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func discoveryAuxScheduleDue(settings map[string]string, prefix string, now time.Time) bool {
	mode := monitor.NormalizeScheduleMode(settings[prefix+"_schedule_mode"], settings[prefix+"_start_time"], settings[prefix+"_weekdays"], settings[prefix+"_cron"])
	interval := discoveryDuration(settings, map[string]string{"discoveries_subtitle": "discoveries_subtitle_refresh_interval", "discoveries_openai": "discoveries_openai_cache_interval"}[prefix], 6*time.Hour)
	last, _ := time.Parse(time.RFC3339Nano, settings[prefix+"_last_run_at"])
	if !last.IsZero() && now.Sub(last) < interval && mode != "cron" {
		return false
	}
	if mode == "cron" {
		matched, err := monitor.CalendarScheduleMatches(now, "", "", settings[prefix+"_cron"])
		return err == nil && matched
	}
	if mode == "advanced" {
		matched, err := monitor.CalendarScheduleMatches(now, settings[prefix+"_start_time"], settings[prefix+"_weekdays"], "")
		return err == nil && matched
	}
	if last.IsZero() {
		return true
	}
	next := last.Add(interval)
	if start := strings.TrimSpace(settings[prefix+"_start_time"]); start != "" {
		next = monitor.NextBasicRun(last, interval, start)
	}
	return !now.Before(next)
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
	var request struct {
		Operation string `json:"operation"`
	}
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&request)
	}
	operation := strings.ToLower(strings.TrimSpace(request.Operation))
	if operation == "" {
		operation = "recommendations"
	}
	if operation == "openai" {
		discoveryRankCache.Lock()
		discoveryRankCache.entries = map[[32]byte]discoveryRankCacheEntry{}
		discoveryRankCache.Unlock()
		discoveryResultCache.Lock()
		discoveryResultCache.created = time.Time{}
		discoveryResultCache.items = nil
		discoveryResultCache.Unlock()
		now := time.Now().UTC()
		_ = s.store.SaveSettings(r.Context(), map[string]string{"discoveries_openai_last_run_at": now.Format(time.RFC3339Nano)})
		settings["discoveries_openai_last_run_at"] = now.Format(time.RFC3339Nano)
		// Actually start a sweep here rather than leaving this handler to only
		// clear caches: previously the real enrichment work only ever happened
		// as a side effect of the page reload the UI does right after this
		// call, bounded by that one page's small "Results" size regardless of
		// the configured "Maximum candidates per enrichment run" - see
		// runDiscoveryEnrichmentSweep's doc comment for the full reasoning.
		// Fetching/scoring up to a few thousand candidates can take a moment
		// on a large library, so it runs in the background; progress is
		// already tracked through discoveryAIStatus, which the UI polls via
		// GET /jobs/discoveries independent of this response.
		jobContext := context.WithoutCancel(r.Context())
		go func() {
			if err := s.runDiscoveryEnrichmentSweep(jobContext, settings); err != nil && s.log != nil {
				s.log.Warn("Discovery AI enrichment sweep failed to start", "error", err)
			}
		}()
		s.json(w, http.StatusAccepted, discoveryJobSnapshot(settings))
		return
	}
	mode := "manual"
	if operation == "subtitles" {
		mode = "subtitle-manual"
	}
	if operation != "recommendations" && operation != "subtitles" {
		s.problem(w, http.StatusBadRequest, "operation must be recommendations, subtitles, or openai")
		return
	}
	if err := startDiscoveryJob(r.Context(), s.store, s.log, mode); err != nil {
		s.problem(w, http.StatusConflict, err.Error())
		return
	}
	s.json(w, http.StatusAccepted, discoveryJobSnapshot(settings))
}

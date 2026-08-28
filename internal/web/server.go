package web

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Net005/JAVBeacon/internal/auth"
	"github.com/Net005/JAVBeacon/internal/covers"
	"github.com/Net005/JAVBeacon/internal/domain"
	"github.com/Net005/JAVBeacon/internal/download"
	"github.com/Net005/JAVBeacon/internal/logging"
	"github.com/Net005/JAVBeacon/internal/monitor"
	"github.com/Net005/JAVBeacon/internal/screenshots"
	"github.com/Net005/JAVBeacon/internal/stash"
	"github.com/Net005/JAVBeacon/internal/store"
	buildversion "github.com/Net005/JAVBeacon/internal/version"
	"golang.org/x/net/websocket"
)

//go:embed static/*
var assets embed.FS

type Server struct {
	store         store.Store
	auth          *auth.Service
	monitor       *monitor.Service
	stash         *stash.Service
	downloads     *download.Service
	covers        *covers.Cache
	screenshots   *screenshots.Cache
	key           string
	dbEngine      string
	sqlitePath    string
	log           *slog.Logger
	mux           *http.ServeMux
	logs          *logging.RingHandler
	clientsMu     sync.Mutex
	clients       map[*websocket.Conn]bool
	coverJobMu    sync.RWMutex
	coverJob      coverCacheStatus
	screenshotJob screenshotBackfillStatus
	// migrationMu guards migration (DB Phase 7's migration-wizard status,
	// see migration.go) - in-memory only, single-user app.
	migrationMu sync.Mutex
	migration   migrationState
}

type coverCacheStatus struct {
	Running    bool      `json:"running"`
	StartedAt  time.Time `json:"started_at,omitempty"`
	FinishedAt time.Time `json:"finished_at,omitempty"`
	Total      int       `json:"total"`
	Checked    int       `json:"checked"`
	Cached     int       `json:"cached"`
	Skipped    int       `json:"skipped"`
	Failed     int       `json:"failed"`
	VideoID    string    `json:"video_id,omitempty"`
	LastError  string    `json:"last_error,omitempty"`
}

type screenshotBackfillStatus struct {
	Running    bool      `json:"running"`
	StartedAt  time.Time `json:"started_at,omitempty"`
	FinishedAt time.Time `json:"finished_at,omitempty"`
	Total      int       `json:"total"`
	Checked    int       `json:"checked"`
	Completed  int       `json:"completed"`
	Skipped    int       `json:"skipped"`
	Failed     int       `json:"failed"`
	VideoID    string    `json:"video_id,omitempty"`
	LastError  string    `json:"last_error,omitempty"`
}

// sqlitePath is the application's configured SQLite database path
// (config.Config.DatabasePath) regardless of which engine is currently
// active - the DB Phase 7 migration wizard's "currently configured SQLite
// database" source option (setupMigrationSource) needs to know it even
// when the app is presently running on PostgreSQL.
func New(st store.Store, authService *auth.Service, m *monitor.Service, stashSync *stash.Service, downloadService *download.Service, covers *covers.Cache, key string, dbEngine string, sqlitePath string, l *slog.Logger, logs *logging.RingHandler, screenshotCaches ...*screenshots.Cache) http.Handler {
	s := &Server{store: st, auth: authService, monitor: m, stash: stashSync, downloads: downloadService, covers: covers, key: key, dbEngine: dbEngine, sqlitePath: sqlitePath, log: l, logs: logs, mux: http.NewServeMux(), clients: map[*websocket.Conn]bool{}}
	if len(screenshotCaches) > 0 {
		s.screenshots = screenshotCaches[0]
	}
	m.OnRelease(s.broadcastRelease)
	s.routes()
	return s.security(s.mux)
}
func (s *Server) routes() {
	static, _ := fs.Sub(assets, "static")
	s.mux.Handle("GET /assets/", http.StripPrefix("/assets/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		http.FileServer(http.FS(static)).ServeHTTP(w, r)
	})))
	s.mux.HandleFunc("GET /login", func(w http.ResponseWriter, r *http.Request) {
		if cookie, e := r.Cookie("javbeacon_session"); e == nil && s.auth.Valid(r.Context(), cookie.Value) {
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
		http.ServeFileFS(w, r, assets, "static/login.html")
	})
	s.mux.HandleFunc("POST /api/auth/login", s.login)
	s.mux.HandleFunc("POST /api/auth/logout", s.logout)
	s.mux.HandleFunc("PUT /api/auth/credentials", s.changeCredentials)
	s.mux.HandleFunc("GET /api/auth/me", func(w http.ResponseWriter, r *http.Request) {
		u, e := s.store.User(r.Context())
		if e != nil {
			s.problem(w, 500, e.Error())
			return
		}
		s.json(w, 200, map[string]any{"id": u.ID, "username": u.Username})
	})
	s.mux.HandleFunc("GET /api/version", func(w http.ResponseWriter, r *http.Request) {
		s.json(w, http.StatusOK, map[string]string{"version": buildversion.Current()})
	})
	s.mux.HandleFunc("GET /api/changelog/pending", s.pendingChangelog)
	s.mux.HandleFunc("POST /api/changelog/acknowledge", s.acknowledgeChangelog)
	s.mux.Handle("GET /api/ws", websocket.Handler(s.releaseStream))
	s.mux.HandleFunc("GET /covers/{id}", s.cover)
	s.mux.HandleFunc("GET /screenshots/{id}/{index}", s.screenshot)
	s.mux.HandleFunc("GET /api/releases/{id}/screenshots", s.releaseScreenshots)
	s.mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		http.ServeFileFS(w, r, assets, "static/index.html")
	})
	s.mux.HandleFunc("GET /release/{id}", func(w http.ResponseWriter, r *http.Request) {
		if _, err := strconv.ParseInt(r.PathValue("id"), 10, 64); err != nil {
			http.NotFound(w, r)
			return
		}
		http.ServeFileFS(w, r, assets, "static/index.html")
	})
	s.mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) {
		s.json(w, 200, map[string]any{"status": "ok", "time": time.Now().UTC()})
	})
	s.mux.HandleFunc("GET /api/stats", s.stats)
	s.mux.HandleFunc("GET /api/settings", s.settings)
	s.mux.HandleFunc("PUT /api/settings", s.settings)
	s.mux.HandleFunc("POST /api/settings/qb-test", s.testQBittorrent)
	s.mux.HandleFunc("GET /api/setup/db/status", s.setupDBStatus)
	s.mux.HandleFunc("GET /api/setup/db/options", s.setupDBOptions)
	s.mux.HandleFunc("POST /api/setup/db/generate", s.setupDBGenerate)
	s.mux.HandleFunc("POST /api/setup/db/test-connection", s.setupDBTestConnection)
	s.mux.HandleFunc("POST /api/setup/db/save", s.setupDBSave)
	s.mux.HandleFunc("GET /api/setup/migration/status", s.setupMigrationStatus)
	s.mux.HandleFunc("POST /api/setup/migration/source", s.setupMigrationSource)
	s.mux.HandleFunc("POST /api/setup/migration/validate-source", s.setupMigrationValidateSource)
	s.mux.HandleFunc("POST /api/setup/migration/postgres", s.setupMigrationPostgres)
	s.mux.HandleFunc("POST /api/setup/migration/inspect-target", s.setupMigrationInspectTarget)
	s.mux.HandleFunc("POST /api/setup/migration/prepare-target", s.setupMigrationPrepareTarget)
	s.mux.HandleFunc("POST /api/setup/migration/migrate", s.setupMigrationMigrate)
	s.mux.HandleFunc("POST /api/setup/migration/activate", s.setupMigrationActivate)
	s.mux.HandleFunc("GET /api/preferences", s.preferences)
	s.mux.HandleFunc("PUT /api/preferences", s.preferences)
	s.mux.HandleFunc("GET /api/filter-presets", s.filterPresets)
	s.mux.HandleFunc("POST /api/filter-presets", s.filterPresets)
	s.mux.HandleFunc("PUT /api/filter-presets/{id}", s.filterPresets)
	s.mux.HandleFunc("DELETE /api/filter-presets/{id}", s.filterPresets)
	s.mux.HandleFunc("GET /api/jobs/history", func(w http.ResponseWriter, r *http.Request) {
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		x, total, e := s.store.JobHistory(r.Context(), limit, offset)
		if e != nil {
			s.problem(w, 500, e.Error())
			return
		}
		s.json(w, 200, map[string]any{"items": x, "total": total})
	})
	s.mux.HandleFunc("GET /api/logs", s.logEntries)
	s.mux.HandleFunc("GET /api/sites", s.sites)
	s.mux.HandleFunc("POST /api/sites", s.saveSite)
	s.mux.HandleFunc("PUT /api/sites/{id}", s.saveSite)
	s.mux.HandleFunc("DELETE /api/sites/{id}", s.deleteSite)
	s.mux.HandleFunc("GET /api/releases", s.releases)
	s.mux.HandleFunc("GET /api/releases/count", s.releasesCount)
	s.mux.HandleFunc("GET /api/release-filter-options", s.releaseFilterOptions)
	s.mux.HandleFunc("PATCH /api/releases/bulk", s.patchReleasesBulk)
	s.mux.HandleFunc("GET /api/releases/{id}", s.release)
	s.mux.HandleFunc("PATCH /api/releases/{id}", s.patchRelease)
	s.mux.HandleFunc("GET /api/releases/{id}/search", s.searchRelease)
	s.mux.HandleFunc("POST /api/releases/{id}/download", s.downloadRelease)
	s.mux.HandleFunc("GET /api/downloads", s.downloadList)
	s.mux.HandleFunc("DELETE /api/downloads/{id}", s.removeDownload)
	s.mux.HandleFunc("POST /api/downloads/bulk-remove", s.bulkRemoveDownloads)
	s.mux.HandleFunc("GET /api/jobs/download-replacements", func(w http.ResponseWriter, r *http.Request) {
		s.json(w, http.StatusOK, s.downloads.ReplacementStatus())
	})
	s.mux.HandleFunc("GET /api/jobs/download-search", func(w http.ResponseWriter, r *http.Request) { s.json(w, 200, s.downloads.SearchStatus()) })
	s.mux.HandleFunc("POST /api/jobs/download-search", func(w http.ResponseWriter, r *http.Request) {
		if e := s.downloads.StartSearch(r.Context()); e != nil {
			s.problem(w, http.StatusConflict, e.Error())
			return
		}
		s.json(w, http.StatusAccepted, s.downloads.SearchStatus())
	})
	// download-search-older is the "older releases" monitored-search
	// schedule's own status/manual-run endpoints (task 38's two-schedule
	// split), mirroring download-search exactly but backed by
	// SearchStatusOlder/StartSearchOlder so it can be polled and run
	// independently from the Monitored releases UI.
	s.mux.HandleFunc("GET /api/jobs/download-search-older", func(w http.ResponseWriter, r *http.Request) { s.json(w, 200, s.downloads.SearchStatusOlder()) })
	s.mux.HandleFunc("POST /api/jobs/download-search-older", func(w http.ResponseWriter, r *http.Request) {
		if e := s.downloads.StartSearchOlder(r.Context()); e != nil {
			s.problem(w, http.StatusConflict, e.Error())
			return
		}
		s.json(w, http.StatusAccepted, s.downloads.SearchStatusOlder())
	})
	s.mux.HandleFunc("GET /api/jobs/download-search-history", func(w http.ResponseWriter, r *http.Request) {
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		rows, err := s.store.DownloadSearchRuns(r.Context(), r.URL.Query().Get("schedule"), limit)
		if err != nil {
			s.problem(w, http.StatusInternalServerError, err.Error())
			return
		}
		s.json(w, http.StatusOK, rows)
	})
	s.mux.HandleFunc("GET /api/jobs/covers", func(w http.ResponseWriter, r *http.Request) { s.json(w, 200, s.coverCacheStatus()) })
	s.mux.HandleFunc("POST /api/jobs/covers", func(w http.ResponseWriter, r *http.Request) {
		if err := s.startCoverCache(r.Context()); err != nil {
			s.problem(w, http.StatusConflict, err.Error())
			return
		}
		s.json(w, http.StatusAccepted, s.coverCacheStatus())
	})
	s.mux.HandleFunc("GET /api/jobs/screenshots", func(w http.ResponseWriter, r *http.Request) { s.json(w, http.StatusOK, s.screenshotBackfillStatus()) })
	s.mux.HandleFunc("POST /api/jobs/screenshots", func(w http.ResponseWriter, r *http.Request) {
		if err := s.startScreenshotBackfill(r.Context()); err != nil {
			s.problem(w, http.StatusConflict, err.Error())
			return
		}
		s.json(w, http.StatusAccepted, s.screenshotBackfillStatus())
	})
	s.mux.HandleFunc("GET /api/path-mappings", s.pathMappings)
	s.mux.HandleFunc("POST /api/path-mappings", s.pathMappings)
	s.mux.HandleFunc("PUT /api/path-mappings/{id}", s.pathMappings)
	s.mux.HandleFunc("DELETE /api/path-mappings/{id}", s.pathMappings)
	s.mux.HandleFunc("GET /api/pipeline", s.pipeline)
	s.mux.HandleFunc("PUT /api/pipeline", s.pipeline)
	s.mux.HandleFunc("POST /api/pipeline/test", s.testPipelineStep)
	s.mux.HandleFunc("GET /api/pipeline/logs", func(w http.ResponseWriter, r *http.Request) {
		n, _ := strconv.ParseInt(r.URL.Query().Get("download_id"), 10, 64)
		x, e := s.store.PipelineLogs(r.Context(), n)
		if e != nil {
			s.problem(w, 500, e.Error())
			return
		}
		s.json(w, 200, x)
	})
	s.mux.HandleFunc("GET /api/notifications", s.notifications)
	s.mux.HandleFunc("DELETE /api/notifications", s.clearNotifications)
	s.mux.HandleFunc("GET /api/jobs/refresh", func(w http.ResponseWriter, r *http.Request) {
		if raw := r.URL.Query().Get("release_id"); raw != "" {
			releaseID, err := strconv.ParseInt(raw, 10, 64)
			if err != nil || releaseID <= 0 {
				s.problem(w, http.StatusBadRequest, "invalid release id")
				return
			}
			s.json(w, 200, s.monitor.StatusForRelease(releaseID))
			return
		}
		s.json(w, 200, s.monitor.Status())
	})
	s.mux.HandleFunc("DELETE /api/jobs/refresh", func(w http.ResponseWriter, r *http.Request) {
		cleared, stopped := s.monitor.Stop()
		if !stopped {
			s.problem(w, http.StatusConflict, "no scrape job is running")
			return
		}
		s.json(w, http.StatusAccepted, map[string]any{"status": "stopping", "cleared_queued_jobs": cleared})
	})
	s.mux.HandleFunc("POST /api/jobs/refresh", s.refresh)
	s.mux.HandleFunc("GET /api/jobs/stash", func(w http.ResponseWriter, r *http.Request) { s.json(w, 200, s.stash.Status()) })
	s.mux.HandleFunc("POST /api/jobs/stash", func(w http.ResponseWriter, r *http.Request) {
		if e := s.stash.Start(r.Context()); e != nil {
			s.problem(w, 409, e.Error())
			return
		}
		s.json(w, 202, s.stash.Status())
	})
	s.mux.HandleFunc("POST /api/jobs/stash/desired", func(w http.ResponseWriter, r *http.Request) {
		x, e := s.stash.SyncDesired(r.Context())
		if e != nil {
			s.problem(w, 502, e.Error())
			return
		}
		s.json(w, 200, x)
	})

	// TODO-2.0 Phase 2: "Missing Library Files" - find StashApp scenes whose
	// file(s) are gone from disk, retrieve a JAVBeacon release for them
	// from JavLibrary, and drive Monitor/Download from there. See
	// internal/web/stash_missing.go and internal/stash/missing.go.
	s.mux.HandleFunc("GET /api/stash-missing", s.stashMissingList)
	s.mux.HandleFunc("GET /api/stash-missing/count", s.stashMissingCount)
	s.mux.HandleFunc("DELETE /api/stash-missing", s.stashMissingClear)
	s.mux.HandleFunc("GET /api/jobs/stash-missing-scan", s.stashMissingScanJob)
	s.mux.HandleFunc("POST /api/jobs/stash-missing-scan", s.stashMissingScanJob)
	s.mux.HandleFunc("GET /api/jobs/stash-missing-retrieve", s.stashMissingRetrieveJob)
	s.mux.HandleFunc("POST /api/jobs/stash-missing-retrieve", s.stashMissingRetrieveJob)
	s.mux.HandleFunc("GET /api/jobs/stash-missing-apply", s.stashMissingApplyJob)
	s.mux.HandleFunc("POST /api/jobs/stash-missing-apply", s.stashMissingApplyJob)
	s.mux.HandleFunc("GET /api/system/browse-dir", s.browseDir)
	s.mux.HandleFunc("GET /api/jobs/schedule-forecast", s.scheduleForecast)
}

// scheduleForecast aggregates every user-configurable background schedule's
// live enabled/interval state and next few predicted run times across the
// monitor (Scheduled scrapes), download (Monitored releases search), and
// stash (StashApp sync) services into one compact response for the
// Monitoring view's schedule summary widget. Deliberately excludes the
// qBittorrent reconciliation poll (Schedule/tick in internal/download) -
// it's a fixed, non-configurable 1-minute ticker with no meaningful "next
// run" beyond "within a minute" - and the notification/RSS intervals, which
// have settings keys but no exposed UI control to change them.
func (s *Server) scheduleForecast(w http.ResponseWriter, r *http.Request) {
	forecasts := make([]domain.ScheduleForecast, 0, 8)
	forecasts = append(forecasts, s.monitor.ScheduleForecast(r.Context())...)
	forecasts = append(forecasts, s.downloads.SearchScheduleForecast(r.Context())...)
	forecasts = append(forecasts, s.stash.ScheduleForecast(r.Context())...)
	s.json(w, 200, forecasts)
}

func (s *Server) pendingChangelog(w http.ResponseWriter, r *http.Request) {
	change, err := buildversion.PendingChange(r.Context(), s.store)
	if err != nil {
		s.problem(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.json(w, http.StatusOK, change)
}

func (s *Server) acknowledgeChangelog(w http.ResponseWriter, r *http.Request) {
	var request struct {
		From string `json:"from"`
		To   string `json:"to"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		s.problem(w, http.StatusBadRequest, "invalid changelog acknowledgement")
		return
	}
	acknowledged, err := buildversion.AcknowledgeChange(r.Context(), s.store, request.From, request.To)
	if err != nil {
		s.problem(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.json(w, http.StatusOK, map[string]bool{"acknowledged": acknowledged})
}

func (s *Server) coverCacheStatus() coverCacheStatus {
	s.coverJobMu.RLock()
	defer s.coverJobMu.RUnlock()
	return s.coverJob
}

func (s *Server) screenshotBackfillStatus() screenshotBackfillStatus {
	s.coverJobMu.RLock()
	defer s.coverJobMu.RUnlock()
	return s.screenshotJob
}

func (s *Server) startScreenshotBackfill(ctx context.Context) error {
	if s.screenshots == nil {
		return errors.New("screenshot cache is unavailable")
	}
	s.coverJobMu.Lock()
	if s.screenshotJob.Running {
		s.coverJobMu.Unlock()
		return errors.New("screenshot backfill is already running")
	}
	total, err := s.store.ReleasesCount(ctx, domain.ReleaseFilter{Source: "JavLibrary"})
	if err != nil {
		s.coverJobMu.Unlock()
		return err
	}
	s.screenshotJob = screenshotBackfillStatus{Running: true, StartedAt: time.Now().UTC(), Total: total}
	s.coverJobMu.Unlock()
	go s.runScreenshotBackfill(context.WithoutCancel(ctx))
	return nil
}

// runScreenshotBackfill sweeps every JavLibrary release needing a
// screenshot check through a bounded pool of concurrent workers, each
// calling monitor.Service.RefreshReleaseNow directly - bypassing the single
// global scrape job queue entirely, since that queue only ever runs one
// thing at a time and this sweep is exactly the kind of independent,
// lowest-priority background work the multi-instance Byparr pool exists to
// soak up without starving a real scan or manual action (RefreshReleaseNow
// still asks the pool for an instance at the screenshot-backfill priority,
// so it fairly loses out when one of those is also contending for the same
// instances). Worker count is min(configured byparr_max_instances_screenshots
// cap, enabled pool instances) - or 1 if no solver is configured at all,
// matching the pre-pooling one-at-a-time behavior in that case.
func (s *Server) runScreenshotBackfill(ctx context.Context) {
	defer func() {
		s.coverJobMu.Lock()
		s.screenshotJob.Running = false
		s.screenshotJob.FinishedAt = time.Now().UTC()
		s.screenshotJob.VideoID = ""
		s.coverJobMu.Unlock()
	}()
	workers := s.screenshotBackfillWorkers(ctx)
	candidates := make(chan domain.Release)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for release := range candidates {
				s.screenshotBackfillOne(ctx, release)
			}
		}()
	}
	for offset := 0; ; offset += 500 {
		releases, err := s.store.Releases(ctx, domain.ReleaseFilter{Source: "JavLibrary", Sort: "release", Direction: "desc", Limit: 500, Offset: offset})
		if err != nil {
			s.coverJobMu.Lock()
			s.screenshotJob.LastError = err.Error()
			s.coverJobMu.Unlock()
			break
		}
		fed := true
		for _, release := range releases {
			select {
			case candidates <- release:
			case <-ctx.Done():
				fed = false
			}
			if !fed {
				break
			}
		}
		if !fed || len(releases) < 500 {
			break
		}
	}
	close(candidates)
	wg.Wait()
}

// screenshotBackfillWorkers sizes the concurrent worker pool
// runScreenshotBackfill dispatches releases to - see that function's doc
// comment for the exact rule.
func (s *Server) screenshotBackfillWorkers(ctx context.Context) int {
	cap := 0
	if settings, err := s.store.Settings(ctx); err == nil {
		if n, err := strconv.Atoi(strings.TrimSpace(settings["byparr_max_instances_screenshots"])); err == nil && n > 0 {
			cap = n
		}
	}
	workers := s.monitor.SolverPoolEnabledCount()
	if workers < 1 {
		workers = 1
	}
	if cap > 0 && cap < workers {
		workers = cap
	}
	return workers
}

// screenshotBackfillOne checks (and, if needed, refreshes) one release's
// screenshots, recording the outcome via updateScreenshotBackfill. Called
// concurrently from runScreenshotBackfill's worker pool - safe to do so
// since it touches no state that isn't either per-release (independent rows)
// or already protected by its own lock (coverJobMu, inside
// updateScreenshotBackfill; the solver pool, inside RefreshReleaseNow).
func (s *Server) screenshotBackfillOne(ctx context.Context, release domain.Release) {
	completed, completedErr := s.store.ScreenshotBackfillCompleted(ctx, release.ID)
	if completedErr != nil {
		s.updateScreenshotBackfill(release.VideoID, "failed", completedErr)
		return
	}
	cacheComplete := s.screenshots.Complete(release.VideoID, release.Screenshots)
	// A completed zero-screenshot scrape is remembered, while releases
	// with screenshot metadata are only skipped while every local file
	// still exists. This lets the job repair an interrupted/removed cache
	// without repeatedly scraping releases that genuinely have no shots.
	if (completed && len(release.Screenshots) == 0) || cacheComplete {
		if !completed {
			_ = s.store.MarkScreenshotBackfillCompleted(ctx, release.ID)
		}
		s.updateScreenshotBackfill(release.VideoID, "skipped", nil)
		return
	}
	job := s.monitor.RefreshReleaseNow(ctx, release.ID)
	if job.State == "completed" && job.Error == "" {
		if err := s.store.MarkScreenshotBackfillCompleted(ctx, release.ID); err != nil {
			s.updateScreenshotBackfill(release.VideoID, "failed", err)
		} else {
			s.updateScreenshotBackfill(release.VideoID, "completed", nil)
		}
		return
	}
	jobErr := errors.New("screenshot scrape did not complete")
	if strings.TrimSpace(job.Error) != "" {
		jobErr = errors.New(job.Error)
	}
	s.updateScreenshotBackfill(release.VideoID, "failed", jobErr)
}

func (s *Server) updateScreenshotBackfill(videoID, outcome string, err error) {
	s.coverJobMu.Lock()
	defer s.coverJobMu.Unlock()
	s.screenshotJob.VideoID = videoID
	s.screenshotJob.Checked++
	switch outcome {
	case "completed":
		s.screenshotJob.Completed++
	case "skipped":
		s.screenshotJob.Skipped++
	default:
		s.screenshotJob.Failed++
		if err != nil {
			s.screenshotJob.LastError = videoID + ": " + err.Error()
		}
	}
}

func (s *Server) startCoverCache(ctx context.Context) error {
	s.coverJobMu.Lock()
	if s.coverJob.Running {
		s.coverJobMu.Unlock()
		return errors.New("cover cache job is already running")
	}
	stats, err := s.store.Stats(ctx)
	if err != nil {
		s.coverJobMu.Unlock()
		return err
	}
	s.coverJob = coverCacheStatus{Running: true, StartedAt: time.Now().UTC(), Total: stats.Releases}
	s.coverJobMu.Unlock()
	go s.runCoverCache(context.WithoutCancel(ctx))
	return nil
}

func (s *Server) runCoverCache(ctx context.Context) {
	defer func() {
		s.coverJobMu.Lock()
		s.coverJob.Running = false
		s.coverJob.FinishedAt = time.Now().UTC()
		s.coverJob.VideoID = ""
		status := s.coverJob
		s.coverJobMu.Unlock()
		s.log.Info("cover cache job completed", "checked", status.Checked, "cached", status.Cached, "skipped", status.Skipped, "failed", status.Failed)
	}()
	s.log.Info("cover cache job started", "total", s.coverCacheStatus().Total)
	for offset := 0; ; offset += 500 {
		releases, err := s.store.Releases(ctx, domain.ReleaseFilter{Sort: "added", Direction: "desc", Limit: 500, Offset: offset})
		if err != nil {
			s.coverJobMu.Lock()
			s.coverJob.LastError = err.Error()
			s.coverJobMu.Unlock()
			return
		}
		for _, release := range releases {
			_, cached, coverErr := s.covers.Ensure(ctx, release.VideoID, release.ImageURL)
			s.coverJobMu.Lock()
			s.coverJob.Checked++
			s.coverJob.VideoID = release.VideoID
			switch {
			case coverErr != nil:
				s.coverJob.Failed++
				s.coverJob.LastError = release.VideoID + ": " + coverErr.Error()
			case cached:
				s.coverJob.Cached++
			default:
				s.coverJob.Skipped++
			}
			s.coverJobMu.Unlock()
		}
		if len(releases) < 500 {
			return
		}
	}
}

func (s *Server) releaseStream(conn *websocket.Conn) {
	s.clientsMu.Lock()
	s.clients[conn] = true
	s.clientsMu.Unlock()
	defer func() { s.clientsMu.Lock(); delete(s.clients, conn); s.clientsMu.Unlock(); _ = conn.Close() }()
	for {
		var ignored string
		if err := websocket.Message.Receive(conn, &ignored); err != nil {
			return
		}
	}
}

func (s *Server) broadcastRelease(release domain.Release) {
	payload, _ := json.Marshal(map[string]any{"type": "release", "release": release})
	s.clientsMu.Lock()
	defer s.clientsMu.Unlock()
	for conn := range s.clients {
		if err := websocket.Message.Send(conn, string(payload)); err != nil {
			_ = conn.Close()
			delete(s.clients, conn)
		}
	}
}

func (s *Server) searchRelease(w http.ResponseWriter, r *http.Request) {
	n, e := id(r)
	if e != nil {
		s.problem(w, 400, "invalid release id")
		return
	}
	release, e := s.store.Release(r.Context(), n)
	if e != nil {
		s.problem(w, 404, "release not found")
		return
	}
	rows, e := s.downloads.Search(r.Context(), release)
	if e != nil {
		s.problem(w, 502, e.Error())
		return
	}
	s.json(w, 200, rows)
}
func (s *Server) downloadRelease(w http.ResponseWriter, r *http.Request) {
	n, e := id(r)
	if e != nil {
		s.problem(w, 400, "invalid release id")
		return
	}
	release, e := s.store.Release(r.Context(), n)
	if e != nil {
		s.problem(w, 404, "release not found")
		return
	}
	var result domain.SearchResult
	if !s.decode(w, r, &result) {
		return
	}
	x, e := s.downloads.Download(r.Context(), release, result, "Manual Search", result.Link)
	if e != nil {
		s.problem(w, 502, e.Error())
		return
	}
	s.json(w, 202, x)
}

// logEntries serves GET /api/logs?limit=&before=&after= (Phase 13). With
// neither cursor it returns the most recent `limit` entries (the initial
// page). `before` (a Seq cursor) pages backward - older entries, ascending
// order - for infinite-scroll "load older" requests. `after` (a Seq cursor)
// returns only entries strictly newer than it, for an efficient live
// tail-poll that does not re-fetch the whole visible window on every tick.
// before takes precedence if a request somehow sets both.
func (s *Server) logEntries(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if raw := r.URL.Query().Get("before"); raw != "" {
		cursor, _ := strconv.ParseInt(raw, 10, 64)
		s.json(w, 200, s.logs.EntriesBefore(cursor, limit))
		return
	}
	if raw := r.URL.Query().Get("after"); raw != "" {
		cursor, _ := strconv.ParseInt(raw, 10, 64)
		s.json(w, 200, s.logs.EntriesAfter(cursor, limit))
		return
	}
	s.json(w, 200, s.logs.Entries(limit))
}
func (s *Server) downloadList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	seenComplete := q.Get("seen_complete")
	if seenComplete != "" && seenComplete != "never" && seenComplete != "before" && seenComplete != "after" {
		s.problem(w, http.StatusUnprocessableEntity, "last seen complete filter must be never, before, or after")
		return
	}
	var seenCompleteDate int64
	if raw := q.Get("seen_complete_date"); raw != "" {
		date, err := time.Parse("2006-01-02", raw)
		if err != nil {
			s.problem(w, http.StatusUnprocessableEntity, "last seen complete date must use YYYY-MM-DD")
			return
		}
		seenCompleteDate = date.UTC().Unix()
		if seenComplete == "before" {
			seenCompleteDate = date.UTC().Add(24 * time.Hour).Unix()
		}
	}
	if (seenComplete == "before" || seenComplete == "after") && seenCompleteDate == 0 {
		s.problem(w, http.StatusUnprocessableEntity, "last seen complete date is required for this filter")
		return
	}
	rows, total, e := s.store.DownloadActivity(r.Context(), domain.DownloadFilter{Status: q.Get("status"), Search: q.Get("search"), Source: q.Get("source"), Sort: q.Get("sort"), Direction: q.Get("direction"), FilenamePatternExcluded: q.Get("filename_pattern_excluded") == "true", Stalled: q.Get("stalled") == "true", SeenComplete: seenComplete, SeenCompleteDate: seenCompleteDate, Limit: limit, Offset: offset})
	if e != nil {
		s.problem(w, 500, e.Error())
		return
	}
	s.json(w, 200, map[string]any{"items": rows, "total": total})
}
func (s *Server) removeDownload(w http.ResponseWriter, r *http.Request) {
	n, err := id(r)
	if err != nil {
		s.problem(w, http.StatusBadRequest, "invalid download id")
		return
	}
	deleted, err := s.downloads.RemoveDownload(r.Context(), n)
	if err != nil {
		status := http.StatusBadGateway
		if err.Error() == "download not found" {
			status = http.StatusNotFound
		}
		s.problem(w, status, err.Error())
		return
	}
	s.json(w, http.StatusOK, map[string]any{"removed": true, "history_rows_deleted": deleted})
}
func (s *Server) bulkRemoveDownloads(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		IDs                       []int64 `json:"ids"`
		Replace                   bool    `json:"replace"`
		AllowNonPreferredFilename bool    `json:"allow_non_preferred_filename"`
	}
	if !s.decode(w, r, &payload) {
		return
	}
	job, err := s.downloads.StartBulkRemoveAndReplace(r.Context(), payload.IDs, payload.Replace, payload.AllowNonPreferredFilename)
	if err != nil {
		s.problem(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	s.json(w, http.StatusAccepted, job)
}
func (s *Server) pathMappings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		x, e := s.store.PathMappings(r.Context())
		if e != nil {
			s.problem(w, 500, e.Error())
			return
		}
		s.json(w, 200, x)
	case http.MethodDelete:
		n, e := id(r)
		if e == nil {
			e = s.store.DeletePathMapping(r.Context(), n)
		}
		if e != nil {
			s.problem(w, 400, e.Error())
			return
		}
		w.WriteHeader(204)
	default:
		var x domain.PathMapping
		if !s.decode(w, r, &x) {
			return
		}
		if r.Method == http.MethodPut {
			x.ID, _ = id(r)
		}
		saved, e := s.store.SavePathMapping(r.Context(), x)
		if e != nil {
			s.problem(w, 422, e.Error())
			return
		}
		s.json(w, 200, saved)
	}
}
func (s *Server) pipeline(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		x, e := s.store.PipelineSteps(r.Context())
		if e != nil {
			s.problem(w, 500, e.Error())
			return
		}
		s.json(w, 200, x)
		return
	}
	var x []domain.PipelineStep
	if !s.decode(w, r, &x) {
		return
	}
	if e := s.store.SavePipelineSteps(r.Context(), x); e != nil {
		s.problem(w, 422, e.Error())
		return
	}
	s.json(w, 200, x)
}

// testPipelineStep serves POST /api/pipeline/test: it runs a single Ordered
// event pipeline step - as currently edited in the Settings form, whether or
// not it has been saved yet - against synthetic sample values, and returns
// whether it passed along with its output/error, so a user can verify a
// step from Settings without waiting for a real download.
func (s *Server) testPipelineStep(w http.ResponseWriter, r *http.Request) {
	var step domain.PipelineStep
	if !s.decode(w, r, &step) {
		return
	}
	output, e := s.downloads.TestPipelineStep(r.Context(), step)
	if e != nil {
		s.json(w, 200, map[string]any{"passed": false, "output": output, "error": e.Error()})
		return
	}
	s.json(w, 200, map[string]any{"passed": true, "output": output})
}
func (s *Server) notifications(w http.ResponseWriter, r *http.Request) {
	x, e := s.store.Notifications(r.Context(), r.URL.Query().Get("type"))
	if e != nil {
		s.problem(w, 500, e.Error())
		return
	}
	s.json(w, 200, x)
}
func (s *Server) clearNotifications(w http.ResponseWriter, r *http.Request) {
	kind := strings.TrimSpace(r.URL.Query().Get("type"))
	var payload struct {
		IDs []int64 `json:"ids"`
	}
	if r.ContentLength != 0 && !s.decode(w, r, &payload) {
		return
	}
	deleted, err := s.store.DeleteNotifications(r.Context(), kind, payload.IDs)
	if err != nil {
		s.problem(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	s.json(w, http.StatusOK, map[string]any{"deleted": deleted, "type": kind})
}

func (s *Server) cover(w http.ResponseWriter, r *http.Request) {
	n, err := id(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	release, err := s.store.Release(r.Context(), n)
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.log.Warn("cover lookup failed", "release_id", n, "error", err)
		http.Error(w, "cover unavailable", http.StatusInternalServerError)
		return
	}
	if strings.TrimSpace(release.ImageURL) == "" {
		s.serveUnavailableCover(w, r)
		return
	}
	path, _, err := s.covers.Ensure(r.Context(), release.VideoID, release.ImageURL)
	if err != nil {
		s.log.Warn("local cover unavailable", "release_id", n, "video_id", release.VideoID, "image_url", release.ImageURL, "error", err)
		s.serveUnavailableCover(w, r)
		return
	}
	if s.covers.Unavailable(path) {
		s.serveUnavailableCover(w, r)
		return
	}
	f, err := os.Open(path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		http.NotFound(w, r)
		return
	}
	buf := make([]byte, 512)
	nRead, _ := f.Read(buf)
	_, _ = f.Seek(0, 0)
	w.Header().Set("Content-Type", http.DetectContentType(buf[:nRead]))
	w.Header().Set("Cache-Control", "public, max-age=300, must-revalidate")
	http.ServeContent(w, r, release.VideoID, info.ModTime(), f)
}

func (s *Server) screenshot(w http.ResponseWriter, r *http.Request) {
	if s.screenshots == nil {
		http.NotFound(w, r)
		return
	}
	releaseID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	index, err := strconv.Atoi(r.PathValue("index"))
	if err != nil || index < 0 {
		http.NotFound(w, r)
		return
	}
	release, err := s.store.Release(r.Context(), releaseID)
	if err != nil || index >= len(release.Screenshots) {
		http.NotFound(w, r)
		return
	}
	path := s.screenshots.Path(release.VideoID, index)
	f, err := os.Open(path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "private, max-age=86400")
	http.ServeContent(w, r, release.VideoID+"-screenshot", info.ModTime(), f)
}

func (s *Server) releaseScreenshots(w http.ResponseWriter, r *http.Request) {
	if s.screenshots == nil {
		s.json(w, http.StatusOK, map[string]any{"indexes": []int{}})
		return
	}
	releaseID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.problem(w, http.StatusBadRequest, "invalid release id")
		return
	}
	release, err := s.store.Release(r.Context(), releaseID)
	if err != nil {
		s.problem(w, http.StatusNotFound, "release not found")
		return
	}
	s.json(w, http.StatusOK, map[string]any{"indexes": s.screenshots.Available(release.VideoID, release.Screenshots)})
}

func (s *Server) serveUnavailableCover(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeFileFS(w, r, assets, "static/cover-unavailable.svg")
}

func (s *Server) security(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "same-origin")
		public := r.URL.Path == "/login" || r.URL.Path == "/api/auth/login" || strings.HasPrefix(r.URL.Path, "/assets/")
		if !public {
			cookie, _ := r.Cookie("javbeacon_session")
			token := ""
			if cookie != nil {
				token = cookie.Value
			}
			apiKeyValid := s.key != "" && (r.Header.Get("Authorization") == "Bearer "+s.key || r.URL.Query().Get("api_key") == s.key)
			if !apiKeyValid && !s.auth.Valid(r.Context(), token) {
				if strings.HasPrefix(r.URL.Path, "/api/") {
					s.problem(w, http.StatusUnauthorized, "authentication required")
				} else {
					http.Redirect(w, r, "/login", http.StatusSeeOther)
				}
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var p struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !s.decode(w, r, &p) {
		return
	}
	lifetime := auth.DefaultLifetime
	if settings, e := s.store.Settings(r.Context()); e == nil {
		if d, e := time.ParseDuration(settings["session_lifetime"]); e == nil && d > 0 {
			lifetime = d
		}
	}
	x, e := s.auth.Login(r.Context(), p.Username, p.Password, lifetime)
	if e != nil {
		s.problem(w, http.StatusUnauthorized, e.Error())
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "javbeacon_session", Value: x.Token, Path: "/", Expires: x.ExpiresAt, HttpOnly: true, SameSite: http.SameSiteLaxMode})
	s.json(w, 200, map[string]any{"expires_at": x.ExpiresAt})
}
func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if c, e := r.Cookie("javbeacon_session"); e == nil {
		_ = s.auth.Logout(r.Context(), c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: "javbeacon_session", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteLaxMode})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) changeCredentials(w http.ResponseWriter, r *http.Request) {
	var p struct {
		CurrentPassword string `json:"current_password"`
		Username        string `json:"username"`
		NewPassword     string `json:"new_password"`
	}
	if !s.decode(w, r, &p) {
		return
	}
	if strings.TrimSpace(p.CurrentPassword) == "" {
		s.problem(w, http.StatusUnprocessableEntity, "current password is required")
		return
	}
	if e := s.auth.Change(r.Context(), p.CurrentPassword, p.Username, p.NewPassword); e != nil {
		s.problem(w, http.StatusUnprocessableEntity, e.Error())
		return
	}
	u, _ := s.store.User(r.Context())
	s.json(w, 200, map[string]any{"id": u.ID, "username": u.Username})
}

func (s *Server) preferences(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		x, e := s.store.Preferences(r.Context())
		if e != nil {
			s.problem(w, 500, e.Error())
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(x)
		return
	}
	var x json.RawMessage
	if !s.decode(w, r, &x) {
		return
	}
	if e := s.store.SavePreferences(r.Context(), x); e != nil {
		s.problem(w, 422, e.Error())
		return
	}
	s.json(w, 200, x)
}
func (s *Server) filterPresets(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		x, e := s.store.FilterPresets(r.Context())
		if e != nil {
			s.problem(w, 500, e.Error())
			return
		}
		s.json(w, 200, x)
	case http.MethodDelete:
		n, e := id(r)
		if e == nil {
			e = s.store.DeleteFilterPreset(r.Context(), n)
		}
		if e != nil {
			s.problem(w, 400, e.Error())
			return
		}
		w.WriteHeader(204)
	default:
		var x domain.FilterPreset
		if !s.decode(w, r, &x) {
			return
		}
		if r.Method == http.MethodPut {
			x.ID, _ = id(r)
		}
		saved, e := s.store.SaveFilterPreset(r.Context(), x)
		if e != nil {
			s.problem(w, 422, e.Error())
			return
		}
		s.json(w, map[bool]int{true: 201, false: 200}[r.Method == http.MethodPost], saved)
	}
}
func (s *Server) json(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func (s *Server) problem(w http.ResponseWriter, status int, msg string) {
	s.json(w, status, map[string]string{"error": msg})
}
func (s *Server) decode(w http.ResponseWriter, r *http.Request, v any) bool {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	d.DisallowUnknownFields()
	if e := d.Decode(v); e != nil {
		s.problem(w, 400, e.Error())
		return false
	}
	return true
}
func id(r *http.Request) (int64, error) { return strconv.ParseInt(r.PathValue("id"), 10, 64) }
func (s *Server) stats(w http.ResponseWriter, r *http.Request) {
	x, e := s.store.Stats(r.Context())
	if e != nil {
		s.problem(w, 500, e.Error())
		return
	}
	s.json(w, 200, x)
}
func (s *Server) settings(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		x, e := s.store.Settings(r.Context())
		if e != nil {
			s.problem(w, 500, e.Error())
			return
		}
		s.json(w, 200, x)
		return
	}
	var x map[string]string
	if !s.decode(w, r, &x) {
		return
	}
	allowed := map[string]bool{"screenshot_directory": true, "page_limit": true, "refresh_interval": true, "quick_refresh_enabled": true, "quick_refresh_schedule_mode": true, "quick_refresh_start_time": true, "quick_refresh_weekdays": true, "quick_refresh_cron": true, "full_refresh_enabled": true, "full_refresh_schedule_mode": true, "full_refresh_interval": true, "full_refresh_start_time": true, "full_refresh_weekdays": true, "full_refresh_cron": true, "full_refresh_page_limit": true, "new_release_refresh_enabled": true, "new_release_refresh_schedule_mode": true, "new_release_refresh_interval": true, "new_release_refresh_start_time": true, "new_release_refresh_weekdays": true, "new_release_refresh_cron": true, "new_release_refresh_page_limit": true, "recent_limit": true, "hide_local": true, "sort": true, "view": true, "notification_sort": true, "flaresolverr_url": true, "flaresolverr_cooldown": true, "byparr_instances": true, "byparr_max_instances_quick": true, "byparr_max_instances_full": true, "byparr_max_instances_new": true, "byparr_max_instances_screenshots": true, "cover_directory": true, "stash_base_url": true, "stash_graphql_query": true, "stash_sync_interval": true, "stash_local_sync_enabled": true, "stash_api_key": true, "stash_desired_tag_id": true, "stash_desired_sync_enabled": true, "stash_desired_sync_interval": true, "session_lifetime": true, "search_url_template": true, "accepted_patterns": true, "search_auto_close_seconds": true, "qb_url": true, "qb_username": true, "qb_password": true, "qb_category": true, "minimum_seed_ratio": true, "qb_completed_action": true, "pipeline_timeout_seconds": true, "download_schedule": true, "download_search_enabled": true, "download_search_interval": true, "download_search_older_enabled": true, "download_search_older_interval": true, "monitor_recent_days": true, "monitor_older_days": true, "rss_interval": true, "notification_interval": true, "stash_missing_graphql_query": true, "stash_missing_path_from": true, "stash_missing_path_to": true, "stash_missing_path_remaps": true, "stash_missing_folder_scope": true, "ignore_tags": true, "ignore_titles": true, "release_batch_size": true}
	for _, kind := range monitor.JobPriorityKinds {
		allowed[monitor.JobPrioritySettingKey(kind)] = true
	}
	for _, spec := range []struct{ prefix, intervalKey string }{{"quick_refresh", "refresh_interval"}, {"full_refresh", "full_refresh_interval"}, {"new_release_refresh", "new_release_refresh_interval"}} {
		mode := strings.ToLower(strings.TrimSpace(x[spec.prefix+"_schedule_mode"]))
		if mode == "" {
			mode = "basic"
		}
		if mode != "basic" && mode != "advanced" && mode != "cron" {
			s.problem(w, http.StatusUnprocessableEntity, spec.prefix+": schedule mode must be Basic, Advanced, or Cron")
			return
		}
		if raw, ok := x[spec.intervalKey]; ok && strings.TrimSpace(raw) != "" {
			if parsed, err := domain.ParseScheduleDuration(raw); err != nil || parsed < time.Minute {
				s.problem(w, http.StatusUnprocessableEntity, spec.prefix+": interval must be at least 1 minute (e.g. \"12h\", \"7d\")")
				return
			}
		}
		if mode != "cron" {
			if err := monitor.ValidateCalendarSchedule(x[spec.prefix+"_start_time"], x[spec.prefix+"_weekdays"]); err != nil {
				s.problem(w, http.StatusUnprocessableEntity, spec.prefix+": "+err.Error())
				return
			}
		}
		if mode == "advanced" && strings.TrimSpace(x[spec.prefix+"_start_time"]) == "" {
			s.problem(w, http.StatusUnprocessableEntity, spec.prefix+": Advanced mode requires a start time")
			return
		}
		if mode == "cron" && strings.TrimSpace(x[spec.prefix+"_cron"]) == "" {
			s.problem(w, http.StatusUnprocessableEntity, spec.prefix+": Cron mode requires a five-field cron expression")
			return
		}
		if mode == "cron" {
			if err := monitor.ValidateCronSchedule(x[spec.prefix+"_cron"]); err != nil {
				s.problem(w, http.StatusUnprocessableEntity, spec.prefix+": "+err.Error())
				return
			}
		}
	}
	// download_search_interval/download_search_older_interval (Monitored
	// releases) and stash_sync_interval/stash_desired_sync_interval
	// (StashApp) are plain "Run every" duration strings, same shape as
	// refresh_interval/full_refresh_interval above but without a
	// corresponding calendar/cron override - validated the same way (only
	// when submitted and non-blank, so a save of other settings never fails
	// because one of these was left at its default) so a typo is rejected
	// up front instead of silently falling back to that schedule's default
	// interval downstream, which used to look exactly like the schedule
	// hadn't picked up the change at all.
	for _, key := range []string{"download_search_interval", "download_search_older_interval", "stash_sync_interval", "stash_desired_sync_interval"} {
		if raw, ok := x[key]; ok && strings.TrimSpace(raw) != "" {
			if parsed, err := domain.ParseScheduleDuration(strings.TrimSpace(raw)); err != nil || parsed < time.Minute {
				s.problem(w, http.StatusUnprocessableEntity, key+": schedule must be a valid duration of at least 1 minute (e.g. \"1h\", \"30m\", \"7d\")")
				return
			}
		}
	}
	for k := range x {
		if !allowed[k] {
			s.problem(w, 422, "unsupported setting: "+k)
			return
		}
	}
	for _, kind := range monitor.JobPriorityKinds {
		key := monitor.JobPrioritySettingKey(kind)
		if raw, ok := x[key]; ok && strings.TrimSpace(raw) != "" {
			if priority, e := strconv.Atoi(strings.TrimSpace(raw)); e != nil || priority < 1 || priority > 999 {
				s.problem(w, http.StatusUnprocessableEntity, "job priority for "+kind+" must be a whole number from 1 to 999")
				return
			}
		}
	}
	// byparr_instances is the JSON-encoded list of configured Byparr/
	// FlareSolverr instances backing the multi-instance solver pool -
	// rejected up front (rather than silently stored and ignored later) if
	// it doesn't parse, or if any entry has a blank URL, so a typo in the
	// Settings UI surfaces immediately instead of quietly leaving the pool
	// short an instance.
	if raw, ok := x["byparr_instances"]; ok && strings.TrimSpace(raw) != "" {
		var instances []struct {
			URL      string `json:"url"`
			Priority int    `json:"priority"`
			Enabled  bool   `json:"enabled"`
		}
		if err := json.Unmarshal([]byte(raw), &instances); err != nil {
			s.problem(w, http.StatusUnprocessableEntity, "Byparr instances: invalid data")
			return
		}
		for _, inst := range instances {
			if strings.TrimSpace(inst.URL) == "" {
				s.problem(w, http.StatusUnprocessableEntity, "Byparr instances: each instance needs a URL")
				return
			}
		}
	}
	// byparr_max_instances_* cap how many of the configured Byparr instances
	// each schedule type (quick/full/new releases) or the screenshot
	// backfill job may use concurrently - blank/0 means "no cap, use every
	// enabled instance."
	for _, key := range []string{"byparr_max_instances_quick", "byparr_max_instances_full", "byparr_max_instances_new", "byparr_max_instances_screenshots"} {
		if raw, ok := x[key]; ok && strings.TrimSpace(raw) != "" {
			if n, err := strconv.Atoi(strings.TrimSpace(raw)); err != nil || n < 0 {
				s.problem(w, http.StatusUnprocessableEntity, key+" must be zero or greater")
				return
			}
		}
	}
	if action, ok := x["qb_completed_action"]; ok && action != "keep" && action != "remove_completed" && action != "remove_at_ratio" {
		s.problem(w, http.StatusUnprocessableEntity, "invalid completed torrent cleanup rule")
		return
	}
	// Only validated when non-blank: the settings form always submits this
	// key even when the field has been left empty (its default, meaning "no
	// minimum" downstream in download.Service, which parses a blank value's
	// strconv.ParseFloat error as 0). Rejecting an empty string here used to
	// fail the entire settings save - including every other field submitted
	// alongside it - any time a user hadn't set a minimum seed ratio, with
	// no visible error (the frontend's settingsForm.onsubmit has no
	// try/catch around the save call).
	if raw, ok := x["minimum_seed_ratio"]; ok && strings.TrimSpace(raw) != "" {
		ratio, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
		if err != nil || ratio < 0 {
			s.problem(w, http.StatusUnprocessableEntity, "minimum seed ratio must be zero or greater")
			return
		}
	}
	// monitor_recent_days/monitor_older_days are the Monitored releases
	// two-schedule split's day thresholds (task 38) - validated the same
	// way as minimum_seed_ratio above (only when non-blank, so a form
	// submitting other settings alongside a blank one - falling back to
	// download.defaultMonitoredRecentDays/defaultMonitoredOlderDays
	// downstream - never fails the whole save).
	for _, key := range []string{"monitor_recent_days", "monitor_older_days"} {
		if raw, ok := x[key]; ok && strings.TrimSpace(raw) != "" {
			if days, err := strconv.Atoi(strings.TrimSpace(raw)); err != nil || days < 0 {
				s.problem(w, http.StatusUnprocessableEntity, "monitored-release day threshold must be zero or greater")
				return
			}
		}
	}
	// release_batch_size controls how many releases the Release Library
	// frontend loads per infinite-scroll batch (and per release-details
	// Next/Prev page-in). Validated against the same [10,500] range the
	// Settings UI exposes and the backend query cap in store.Releases
	// already enforces server-side, so a stray value here can never exceed
	// what a single API call is willing to return anyway.
	if raw, ok := x["release_batch_size"]; ok && strings.TrimSpace(raw) != "" {
		if n, err := strconv.Atoi(strings.TrimSpace(raw)); err != nil || n < 10 || n > 500 {
			s.problem(w, http.StatusUnprocessableEntity, "release batch size must be between 10 and 500")
			return
		}
	}
	previousCoverDirectory := s.covers.Directory()
	if dir, ok := x["cover_directory"]; ok {
		if strings.TrimSpace(dir) == "" {
			s.problem(w, http.StatusUnprocessableEntity, "cover cache path is required")
			return
		}
		if e := s.covers.SetDirectory(dir); e != nil {
			s.problem(w, http.StatusUnprocessableEntity, e.Error())
			return
		}
		x["cover_directory"] = s.covers.Directory()
	}
	if e := s.store.SaveSettings(r.Context(), x); e != nil {
		_ = s.covers.SetDirectory(previousCoverDirectory)
		s.problem(w, 500, e.Error())
		return
	}
	s.monitor.ApplySettings(r.Context())
	if action, ok := x["qb_completed_action"]; ok {
		s.log.Info("qBittorrent completed-download cleanup settings updated", "cleanup_rule", action, "minimum_seed_ratio", x["minimum_seed_ratio"], "files_retained", true)
	}
	s.json(w, 200, x)
}
func (s *Server) testQBittorrent(w http.ResponseWriter, r *http.Request) {
	var config struct {
		URL      string `json:"url"`
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !s.decode(w, r, &config) {
		return
	}
	version, categories, err := s.downloads.TestQB(r.Context(), strings.TrimSpace(config.URL), config.Username, config.Password)
	if err != nil {
		s.problem(w, http.StatusBadGateway, err.Error())
		return
	}
	s.json(w, http.StatusOK, map[string]any{"status": "connected", "version": version, "categories": categories})
}
func (s *Server) sites(w http.ResponseWriter, r *http.Request) {
	x, e := s.store.Sites(r.Context())
	if e != nil {
		s.problem(w, 500, e.Error())
		return
	}
	s.json(w, 200, x)
}
func (s *Server) saveSite(w http.ResponseWriter, r *http.Request) {
	var x domain.Site
	if !s.decode(w, r, &x) {
		return
	}
	if r.Method == http.MethodPut {
		var e error
		x.ID, e = id(r)
		if e != nil {
			s.problem(w, 400, "invalid id")
			return
		}
	}
	x.Title = strings.TrimSpace(x.Title)
	x.Name = strings.TrimSpace(x.Name)
	if x.Title == "" || x.Name == "" {
		s.problem(w, 422, "title and scraper name are required")
		return
	}
	saved, e := s.store.SaveSite(r.Context(), x)
	if e != nil {
		s.problem(w, 409, e.Error())
		return
	}
	if e := s.store.SetSiteReleaseMonitoring(r.Context(), saved.ID, saved.DownloadMode == "all"); e != nil {
		s.problem(w, 500, e.Error())
		return
	}
	code := 200
	if r.Method == http.MethodPost {
		code = 201
	}
	s.json(w, code, saved)
}
func (s *Server) deleteSite(w http.ResponseWriter, r *http.Request) {
	n, e := id(r)
	if e == nil {
		e = s.store.DeleteSite(r.Context(), n)
	}
	if errors.Is(e, sql.ErrNoRows) {
		s.problem(w, 404, "site not found")
		return
	}
	if e != nil {
		s.problem(w, 500, e.Error())
		return
	}
	w.WriteHeader(204)
}

// releaseFilterFromQuery builds a domain.ReleaseFilter from the request's
// query parameters. settings is only consulted for ignore_tags/ignore_titles
// (see domain.ReleaseFilter.ShowNonPreferred), and only when the caller is
// not asking to see ignored releases anyway - the common case (a settings
// lookup this function's own callers already had to make regardless)
// avoids threading store access into this otherwise-pure helper.
// validReleaseDateBound returns raw unchanged if it parses as a plain
// "YYYY-MM-DD" date, and "" otherwise - so a malformed min/max_release_date
// query parameter is silently ignored (treated as "no bound") rather than
// reaching the store as a nonsense string comparison.
func validReleaseDateBound(raw string) string {
	if raw == "" {
		return ""
	}
	if _, err := time.Parse("2006-01-02", raw); err != nil {
		return ""
	}
	return raw
}

func releaseFilterFromQuery(q url.Values, settings map[string]string) domain.ReleaseFilter {
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	f := domain.ReleaseFilter{Search: q.Get("search"), Site: q.Get("site"), Status: q.Get("status"), Sort: q.Get("sort"), Direction: q.Get("direction"), Category: q.Get("category"), Entries: q.Get("entries"), SearchExpression: q.Get("search_expression"), Desired: q.Get("desired") == "true", MonitorDownload: q.Get("monitor_download") == "true", HideLocal: q.Get("hide_local") == "true", ShowNonPreferred: q.Get("show_non_preferred") == "true", MinReleaseDate: validReleaseDateBound(q.Get("min_release_date")), MaxReleaseDate: validReleaseDateBound(q.Get("max_release_date")), Limit: limit, Offset: offset}
	if !f.ShowNonPreferred {
		f.IgnoreTags = domain.ParseIgnoreList(settings["ignore_tags"])
		f.IgnoreTitles = domain.ParseIgnoreList(settings["ignore_titles"])
	}
	if raw := q.Get("allow_non_preferred_filenames"); raw == "true" || raw == "false" {
		v := raw == "true"
		f.AllowNonPreferredFilenames = &v
	}
	return f
}
func (s *Server) releases(w http.ResponseWriter, r *http.Request) {
	settings, e := s.store.Settings(r.Context())
	if e != nil {
		s.problem(w, 500, e.Error())
		return
	}
	x, e := s.store.Releases(r.Context(), releaseFilterFromQuery(r.URL.Query(), settings))
	if e != nil {
		s.problem(w, 500, e.Error())
		return
	}
	s.json(w, 200, x)
}

// releasesCount returns the total number of releases matching the same
// filter parameters accepted by releases, ignoring limit/offset, so a
// paginated table (e.g. Monitoring's "releases checked by the scheduled
// job") can show a true total-result count.
func (s *Server) releasesCount(w http.ResponseWriter, r *http.Request) {
	settings, e := s.store.Settings(r.Context())
	if e != nil {
		s.problem(w, 500, e.Error())
		return
	}
	total, e := s.store.ReleasesCount(r.Context(), releaseFilterFromQuery(r.URL.Query(), settings))
	if e != nil {
		s.problem(w, 500, e.Error())
		return
	}
	s.json(w, 200, map[string]any{"total": total})
}

func (s *Server) releaseFilterOptions(w http.ResponseWriter, r *http.Request) {
	values, err := s.store.ReleaseFilterOptions(r.Context(), r.URL.Query().Get("category"), r.URL.Query().Get("search"))
	if err != nil {
		s.problem(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.json(w, http.StatusOK, values)
}
func (s *Server) release(w http.ResponseWriter, r *http.Request) {
	n, e := id(r)
	var x domain.Release
	if e == nil {
		x, e = s.store.Release(r.Context(), n)
	}
	if errors.Is(e, sql.ErrNoRows) {
		s.problem(w, 404, "release not found")
		return
	}
	if e != nil {
		s.problem(w, 400, e.Error())
		return
	}
	s.json(w, 200, x)
}
func (s *Server) patchRelease(w http.ResponseWriter, r *http.Request) {
	var p struct {
		Released                   *bool   `json:"released"`
		Local                      *bool   `json:"local"`
		Notified                   *bool   `json:"notified"`
		NotifyOnRelease            *bool   `json:"notify_on_release"`
		Desired                    *bool   `json:"desired"`
		MonitorDownload            *bool   `json:"monitor_download"`
		Label                      *string `json:"label"`
		AllowNonPreferredFilenames *bool   `json:"allow_non_preferred_filenames"`
	}
	if !s.decode(w, r, &p) {
		return
	}
	n, e := id(r)
	if e == nil {
		e = s.store.PatchRelease(r.Context(), n, p.Released, p.Local, p.Notified, p.NotifyOnRelease, p.Desired, p.MonitorDownload, p.Label, p.AllowNonPreferredFilenames)
	}
	if errors.Is(e, sql.ErrNoRows) {
		s.problem(w, 404, "release not found")
		return
	}
	if e != nil {
		s.problem(w, 400, e.Error())
		return
	}
	x, _ := s.store.Release(r.Context(), n)
	if p.Desired != nil {
		if !*p.Desired {
			x.DesiredSync = "not_desired"
		} else if settingsErr := s.store.SaveSettings(r.Context(), map[string]string{"stash_desired_sync_enabled": "true"}); settingsErr != nil {
			x.DesiredSync = "error: " + settingsErr.Error()
		} else if state, syncErr := s.stash.SyncDesiredRelease(r.Context(), n); syncErr != nil {
			x.DesiredSync = "error: " + syncErr.Error()
			s.log.Warn("immediate Stash Desired sync failed", "release_id", n, "video_id", x.VideoID, "error", syncErr)
		} else {
			x.DesiredSync = state
		}
	}
	s.broadcastRelease(x)
	s.json(w, 200, x)
}

// patchReleasesBulk backs the "Releases checked by the scheduled job" table's
// mass-select actions: stop monitoring (monitor_download=false) and set/
// clear the persistent "allow non-preferred filenames" override, applied to
// every selected release id in one request.
func (s *Server) patchReleasesBulk(w http.ResponseWriter, r *http.Request) {
	var p struct {
		IDs                        []int64 `json:"ids"`
		MonitorDownload            *bool   `json:"monitor_download"`
		AllowNonPreferredFilenames *bool   `json:"allow_non_preferred_filenames"`
	}
	if !s.decode(w, r, &p) {
		return
	}
	if len(p.IDs) == 0 {
		s.problem(w, http.StatusUnprocessableEntity, "select at least one release")
		return
	}
	if p.MonitorDownload == nil && p.AllowNonPreferredFilenames == nil {
		s.problem(w, http.StatusUnprocessableEntity, "nothing to update")
		return
	}
	n, e := s.store.BulkSetReleaseFlags(r.Context(), p.IDs, p.MonitorDownload, p.AllowNonPreferredFilenames)
	if e != nil {
		s.problem(w, 500, e.Error())
		return
	}
	s.json(w, 200, map[string]any{"updated": n})
}
func (s *Server) refresh(w http.ResponseWriter, r *http.Request) {
	var p struct {
		SiteID    int64  `json:"site_id"`
		ReleaseID int64  `json:"release_id"`
		Mode      string `json:"mode"`
		Pages     int    `json:"pages"`
		AllPages  bool   `json:"all_pages"`
		Kind      string `json:"kind"`
		Priority  int    `json:"priority"`
	}
	if !s.decode(w, r, &p) {
		return
	}
	if p.Kind != "" {
		valid := false
		for _, kind := range monitor.JobPriorityKinds {
			if kind == p.Kind {
				valid = true
				break
			}
		}
		if !valid {
			s.problem(w, 422, "unsupported job priority kind: "+p.Kind)
			return
		}
	}
	if e := s.monitor.StartOptions(r.Context(), monitor.RefreshOptions{SiteID: p.SiteID, ReleaseID: p.ReleaseID, Mode: p.Mode, Pages: p.Pages, AllPages: p.AllPages, Kind: p.Kind, Priority: p.Priority}); e != nil {
		s.problem(w, 409, e.Error())
		return
	}
	if p.ReleaseID != 0 {
		s.json(w, 202, s.monitor.StatusForRelease(p.ReleaseID))
		return
	}
	s.json(w, 202, s.monitor.Status())
}

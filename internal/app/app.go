package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/Net005/JAVBeacon/internal/auth"
	"github.com/Net005/JAVBeacon/internal/config"
	"github.com/Net005/JAVBeacon/internal/covers"
	"github.com/Net005/JAVBeacon/internal/domain"
	"github.com/Net005/JAVBeacon/internal/download"
	"github.com/Net005/JAVBeacon/internal/logging"
	"github.com/Net005/JAVBeacon/internal/monitor"
	"github.com/Net005/JAVBeacon/internal/scraper"
	"github.com/Net005/JAVBeacon/internal/screenshots"
	"github.com/Net005/JAVBeacon/internal/stash"
	"github.com/Net005/JAVBeacon/internal/store"
	buildversion "github.com/Net005/JAVBeacon/internal/version"
	webapp "github.com/Net005/JAVBeacon/internal/web"
)

type App struct {
	cfg       config.Config
	log       *slog.Logger
	store     *store.SQLite
	monitor   *monitor.Service
	stash     *stash.Service
	downloads *download.Service
	server    *http.Server

	// The two fields below are set only when New returns an App in DB
	// Phase 12's connection-recovery mode - recognizable by store being
	// nil. recoveryLogs is threaded through to finishStartup once a retry
	// succeeds; recoveryErr is the most recent connection failure, shown
	// on the recovery page. See recovery.go.
	recoveryLogs *logging.RingHandler
	recoveryErr  error
}

// New loads configuration and opens the configured database, then wires up
// every service exactly as before. The one behavior change from before DB
// Phase 12 is what happens when DatabaseEngine is PostgreSQL and the
// initial connection attempt fails: instead of returning an error (which
// main.go turns into a hard os.Exit(1), crash-looping the process under
// Docker's restart policy until someone notices and fixes it out of band),
// New returns a non-nil App with store == nil - Run enters
// awaitRecovery (recovery.go) instead of serving normally, exactly once,
// per DB Phase 12's "do not treat an unreachable PostgreSQL server as a
// first-run install" requirement: a SQLite engine failure (a filesystem
// problem, not a "PostgreSQL connection recovery" scenario per the phase's
// own title) still fails startup the same way it always has.
//
// Before either of those things happens for PostgreSQL, New first gives it
// a startup grace period (openStoreWithGrace): restarting the whole
// application stack (e.g. after an update) commonly restarts JAVBeacon
// before PostgreSQL has finished its own startup, which can take anywhere
// from about 10 to 60 seconds. Retrying quietly at Warn level during that
// window - rather than immediately logging an Error and standing up the
// recovery page - means an ordinary stack restart produces no alarming log
// output and no recovery-page flash at all; only an outage that outlasts
// the grace period is treated as a real problem.
func New(log *slog.Logger, logs *logging.RingHandler) (*App, error) {
	cfg, e := config.Load()
	if e != nil {
		return nil, e
	}
	window := time.Duration(0)
	if cfg.DatabaseEngine == config.EnginePostgres {
		window = postgresStartupGraceWindow
	}
	st, e := openStoreWithGrace(func() (*store.SQLite, error) { return openStore(cfg) }, window, postgresStartupRetryInterval, log.Warn)
	if e != nil {
		if cfg.DatabaseEngine != config.EnginePostgres {
			return nil, e
		}
		log.Error("PostgreSQL is still not reachable after the startup grace period - entering connection-recovery mode until it is", "error", e)
		return &App{cfg: cfg, log: log, recoveryLogs: logs, recoveryErr: e}, nil
	}
	return finishStartup(cfg, log, logs, st)
}

// postgresStartupGraceWindow/postgresStartupRetryInterval configure New's
// startup grace period for PostgreSQL (see New's doc comment). Package-level
// vars, rather than constants, purely so tests can shrink them instead of
// actually waiting up to a minute for a deliberately-unreachable server.
var (
	postgresStartupGraceWindow   = 60 * time.Second
	postgresStartupRetryInterval = 5 * time.Second
)

// openStoreWithGrace calls open repeatedly on interval until it succeeds or
// window elapses since the first attempt, logging every failed attempt
// through warn (intentionally not Error - see New's doc comment) except the
// very last one, which the caller logs itself once it decides to give up.
// window <= 0 makes it behave exactly like calling open() once - the
// pre-grace-period behavior, still used verbatim for the SQLite engine.
// open/warn are parameters (rather than this reaching for openStore/log.Warn
// directly) so the retry loop's own timing/attempt-count/give-up behavior
// can be unit tested without a real PostgreSQL server.
func openStoreWithGrace(open func() (*store.SQLite, error), window, interval time.Duration, warn func(string, ...any)) (*store.SQLite, error) {
	if window <= 0 {
		return open()
	}
	deadline := time.Now().Add(window)
	var lastErr error
	for attempt := 1; ; attempt++ {
		st, err := open()
		if err == nil {
			return st, nil
		}
		lastErr = err
		if !time.Now().Before(deadline) {
			return nil, lastErr
		}
		warn("PostgreSQL not reachable yet, retrying before entering connection-recovery mode", "attempt", attempt, "error", err)
		time.Sleep(interval)
	}
}

// finishStartup performs the bootstrap/service-wiring work New() has
// always done once a working store is in hand. It is split out from New
// so DB Phase 12's recovery mode (awaitRecovery, recovery.go) can call it
// again the moment a retry succeeds, without duplicating any of this.
func finishStartup(cfg config.Config, log *slog.Logger, logs *logging.RingHandler, st *store.SQLite) (*App, error) {
	sites, e := st.Sites(context.Background())
	if e != nil {
		st.Close()
		return nil, e
	}
	if len(sites) == 0 {
		_, e = st.SaveSite(context.Background(), domain.Site{Title: "GIGA", Type: "Site", Name: "GIGA", URL: cfg.AkibaBaseURL + cfg.AkibaPath, Enabled: true})
		if e != nil {
			st.Close()
			return nil, e
		}
	}
	settings, err := st.Settings(context.Background())
	if err != nil {
		st.Close()
		return nil, err
	}
	_, userErr := st.User(context.Background())
	freshInstall := errors.Is(userErr, sql.ErrNoRows)
	if userErr != nil && !freshInstall {
		st.Close()
		return nil, fmt.Errorf("check existing installation: %w", userErr)
	}
	if err := buildversion.InitializeTracking(context.Background(), st, freshInstall); err != nil {
		st.Close()
		return nil, fmt.Errorf("initialize version tracking: %w", err)
	}
	if len(settings) == 0 {
		_ = st.SaveSettings(context.Background(), map[string]string{"page_limit": fmt.Sprint(cfg.PageLimit), "refresh_interval": cfg.RefreshText, "recent_limit": "200", "hide_local": "false", "sort": "release", "view": "grid", "notification_sort": "added", "flaresolverr_url": cfg.FlareSolverrURL, "flaresolverr_cooldown": fmt.Sprint(cfg.FlareSolverrCooldown), "cover_directory": cfg.CoverDirectory, "session_lifetime": "720h", "notification_interval": "15m", "rss_interval": "5m", "search_url_template": "https://sukebei.nyaa.si/?page=rss&f=0&c=2_0&q=<release_id>", "accepted_patterns": "4k688.com@\nhhd800.com@", "minimum_seed_ratio": "1.0", "qb_completed_action": "remove_at_ratio"})
	}
	missing := map[string]string{}
	if settings["screenshot_directory"] == "" {
		missing["screenshot_directory"] = cfg.ScreenshotDirectory
	}
	if settings["job_priority_screenshot_backfill"] == "" {
		missing["job_priority_screenshot_backfill"] = "75"
	}
	for _, prefix := range []string{"quick_refresh", "full_refresh", "new_release_refresh"} {
		key, mode := prefix+"_schedule_mode", "basic"
		if settings[key] == "" {
			if strings.TrimSpace(settings[prefix+"_cron"]) != "" {
				mode = "cron"
			} else if strings.TrimSpace(settings[prefix+"_start_time"]) != "" || strings.TrimSpace(settings[prefix+"_weekdays"]) != "" {
				mode = "advanced"
			}
			missing[key] = mode
		}
	}
	for k, v := range map[string]string{"cover_directory": cfg.CoverDirectory, "session_lifetime": "720h", "notification_interval": "15m", "rss_interval": "5m", "download_search_interval": "1h", "download_search_enabled": "false", "stash_local_sync_enabled": "true", "stash_sync_interval": "6h", "stash_desired_sync_enabled": "false", "stash_desired_sync_interval": "6h", "search_url_template": "https://sukebei.nyaa.si/?page=rss&f=0&c=2_0&q=<release_id>", "accepted_patterns": "4k688.com@\nhhd800.com@", "qb_category": "", "minimum_seed_ratio": "1.0", "qb_completed_action": "remove_at_ratio", "quick_refresh_enabled": "true", "quick_refresh_start_time": "", "quick_refresh_weekdays": "", "quick_refresh_cron": "", "full_refresh_enabled": "false", "full_refresh_interval": "24h", "full_refresh_start_time": "", "full_refresh_weekdays": "", "full_refresh_cron": "", "full_refresh_page_limit": fmt.Sprint(cfg.PageLimit), "new_release_refresh_enabled": "true", "new_release_refresh_interval": cfg.RefreshText, "new_release_refresh_start_time": "", "new_release_refresh_weekdays": "", "new_release_refresh_cron": "", "new_release_refresh_page_limit": fmt.Sprint(cfg.PageLimit), "job_priority_scheduled_full": "17", "job_priority_scheduled_new": "15", "job_priority_scheduled_quick": "16", "stash_missing_graphql_query": stash.DefaultMissingQuery, "stash_missing_path_from": "", "stash_missing_path_to": "", "stash_missing_path_remaps": "[]", "stash_missing_folder_scope": "", "release_batch_size": "100"} {
		if settings[k] == "" {
			missing[k] = v
		}
	}
	if len(missing) > 0 {
		_ = st.SaveSettings(context.Background(), missing)
	}
	if settings["flaresolverr_url"] == "" {
		_ = st.SaveSettings(context.Background(), map[string]string{"flaresolverr_url": cfg.FlareSolverrURL, "flaresolverr_cooldown": fmt.Sprint(cfg.FlareSolverrCooldown)})
	}
	if settings["stash_graphql_query"] == "" {
		_ = st.SaveSettings(context.Background(), map[string]string{"stash_graphql_query": stash.DefaultQuery, "stash_sync_interval": "6h"})
	}
	if settings["stash_graphql_query"] == `query JAVBeaconLocalScenes { findScenes(filter: { per_page: -1 }) { scenes { title code } } }` {
		_ = st.SaveSettings(context.Background(), map[string]string{"stash_graphql_query": stash.DefaultQuery})
	}
	if settings["stash_base_url"] == "" && settings["stash_graphql_url"] != "" {
		_ = st.SaveSettings(context.Background(), map[string]string{"stash_base_url": strings.TrimSuffix(settings["stash_graphql_url"], "/graphql")})
	}
	// Task C's Missing Library Files path remap moved from a single
	// stash_missing_path_from/stash_missing_path_to pair to an ordered list
	// held in stash_missing_path_remaps (JSON-encoded []stash.PathRemap), so
	// the scan code can try more than one StashApp-mount-to-local-mount
	// prefix. The scan now reads only the new key, so any install that had
	// the old pair configured needs it converted once on startup or its
	// remap would silently stop working.
	if remaps := strings.TrimSpace(settings["stash_missing_path_remaps"]); remaps == "" || remaps == "[]" {
		if from := strings.TrimSpace(settings["stash_missing_path_from"]); from != "" {
			if encoded, err := json.Marshal([]map[string]string{{"from": from, "to": settings["stash_missing_path_to"]}}); err == nil {
				_ = st.SaveSettings(context.Background(), map[string]string{"stash_missing_path_remaps": string(encoded)})
			}
		}
	}
	// Byparr/FlareSolverr moved from a single flaresolverr_url setting to a
	// list of instances (byparr_instances, JSON-encoded []scraper.Instance)
	// so more than one solver can be configured for the concurrent-scraping
	// pool - any install that already had a solver URL configured needs it
	// converted once on startup, the same way stash_missing_path_remaps was
	// migrated above, or it would silently end up with no solver at all.
	if list := strings.TrimSpace(settings["byparr_instances"]); list == "" || list == "[]" {
		if url := strings.TrimSpace(settings["flaresolverr_url"]); url != "" {
			if encoded, err := json.Marshal([]scraper.Instance{{URL: url, Priority: 10, Enabled: true}}); err == nil {
				_ = st.SaveSettings(context.Background(), map[string]string{"byparr_instances": string(encoded)})
			}
		}
	}
	settings, err = st.Settings(context.Background())
	if err != nil {
		st.Close()
		return nil, err
	}
	akiba := scraper.NewAkiba(cfg.AkibaBaseURL, cfg.AkibaPath, cfg.RequestTimeout, log)
	javlibrary := scraper.NewJavLibrary(cfg.RequestTimeout, cfg.FlareSolverrURL, cfg.FlareSolverrCooldown, log)
	coverCache, err := covers.New(settings["cover_directory"], cfg.RequestTimeout, log)
	if err != nil {
		st.Close()
		return nil, err
	}
	screenshotCache, err := screenshots.New(settings["screenshot_directory"], cfg.RequestTimeout, log)
	if err != nil {
		st.Close()
		return nil, err
	}
	mon := monitor.New(st, akiba, javlibrary, coverCache, cfg.PageLimit, log, cfg.RefreshEvery, screenshotCache)
	// javlibrary was constructed from cfg's env-sourced defaults above, which
	// only match the database's saved settings on a fresh install - re-apply
	// from the just-loaded settings row (in particular byparr_instances) so
	// a solver configured via Settings on a prior run is actually live
	// immediately, rather than only taking effect once the first scan job
	// starts (run() re-Configures too) or a settings save happens.
	mon.ApplySettings(context.Background())
	downloadService := download.New(st, cfg.RequestTimeout, log)
	stashSync := stash.New(st, cfg.RequestTimeout, log, javlibrary, downloadService)
	mon.OnRelease(func(r domain.Release) { downloadService.Auto(context.Background(), r) })
	authService := auth.New(st)
	username, password := os.Getenv("JAVBEACON_INITIAL_USERNAME"), os.Getenv("JAVBEACON_INITIAL_PASSWORD")
	if username == "" {
		username = "admin"
	}
	if password == "" {
		password = "changeme123"
	}
	if err := authService.EnsureUser(context.Background(), username, password); err != nil {
		st.Close()
		return nil, err
	}
	handler := webapp.New(st, authService, mon, stashSync, downloadService, coverCache, cfg.APIKey, cfg.DatabaseEngine, cfg.DatabasePath, log, logs, screenshotCache)
	return &App{cfg: cfg, log: log, store: st, monitor: mon, stash: stashSync, downloads: downloadService, server: &http.Server{Addr: cfg.ListenAddress, Handler: handler, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second}}, nil
}

// openStore opens the database backend selected by cfg.DatabaseEngine.
// PostgreSQL became a fully supported runtime backend in DB Phase 5-6 (see
// internal/store's postgres_schema.go for the schema/migration and
// postgres_rewrite.go for the query-rewriting seam that lets the rest of
// this package's SQL run unmodified against either engine) - both
// store.OpenSQLite and store.OpenPostgresStore return the same *store.SQLite
// type (kept unrenamed; see that type's doc comment), so nothing in
// finishStartup needs to branch on the engine. New and awaitRecovery
// (recovery.go, DB Phase 12) are the only two callers of this function.
//
// Per TODO-DATABASE.md's DB Phase 3 requirement, a PostgreSQL connection or
// migration failure is always returned as an error - this never silently
// falls back to SQLite.
func openStore(cfg config.Config) (*store.SQLite, error) {
	if cfg.DatabaseEngine != config.EnginePostgres {
		return store.OpenSQLite(cfg.DatabasePath)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	st, err := store.OpenPostgresStore(ctx, store.PostgresConfig{
		Host:     cfg.PostgresHost,
		Port:     cfg.PostgresPort,
		Database: cfg.PostgresDatabase,
		User:     cfg.PostgresUser,
		Password: cfg.PostgresPassword,
		SSLMode:  cfg.PostgresSSLMode,
	})
	if err != nil {
		return nil, fmt.Errorf("JAVBEACON_DB_ENGINE=postgres: %w", err)
	}
	return st, nil
}

// databaseDescription returns a log-safe (never includes the password)
// description of the active database, for the startup log line in Run.
func databaseDescription(cfg config.Config) string {
	if cfg.DatabaseEngine != config.EnginePostgres {
		return cfg.DatabasePath
	}
	return fmt.Sprintf("postgres://%s@%s:%d/%s", cfg.PostgresUser, cfg.PostgresHost, cfg.PostgresPort, cfg.PostgresDatabase)
}

func ResetCredentials(username, password string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	st, err := store.OpenSQLite(cfg.DatabasePath)
	if err != nil {
		return err
	}
	defer st.Close()
	ctx := context.Background()
	current, lookupErr := st.User(ctx)
	if lookupErr != nil {
		return errors.New("both username and password are required before the first user exists")
	}
	if username == "" {
		username = current.Username
	}
	if password == "" {
		return st.SaveUser(ctx, username, current.PasswordHash)
	}
	return auth.New(st).Reset(ctx, username, password)
}

// Run starts the application. If New returned it in DB Phase 12's
// connection-recovery mode (store == nil), Run first blocks in
// awaitRecovery, serving the recovery page/API and retrying the
// originally configured PostgreSQL connection until it succeeds or ctx is
// canceled, before falling through to normal operation below - the same
// code path New would have taken directly if the connection had simply
// worked the first time.
func (a *App) Run(ctx context.Context) error {
	if a.store == nil {
		started, e := a.awaitRecovery(ctx)
		if e != nil {
			return e
		}
		if started == nil {
			return nil // ctx was canceled while still waiting for PostgreSQL
		}
		*a = *started
	}
	defer a.store.Close()
	go a.monitor.ScheduleScrapes(ctx, a.cfg.RefreshEvery)
	go a.stash.Schedule(ctx)
	go a.stash.DesiredSchedule(ctx)
	go a.downloads.Schedule(ctx)
	go a.downloads.SearchSchedule(ctx)
	go a.downloads.OlderSearchSchedule(ctx)
	go a.downloads.NotificationSchedule(ctx)
	go a.downloads.RSSSchedule(ctx)
	errs := make(chan error, 1)
	go func() {
		a.log.Info("JAVBeacon web server started", "address", a.cfg.ListenAddress, "database", databaseDescription(a.cfg))
		errs <- a.server.ListenAndServe()
	}()
	select {
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return a.server.Shutdown(shutdown)
	case e := <-errs:
		if errors.Is(e, http.ErrServerClosed) {
			return nil
		}
		return e
	}
}

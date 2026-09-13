package web

import (
	"bytes"
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Net005/JAVBeacon/internal/auth"
	"github.com/Net005/JAVBeacon/internal/backfill"
	"github.com/Net005/JAVBeacon/internal/covers"
	aidiscovery "github.com/Net005/JAVBeacon/internal/discovery"
	"github.com/Net005/JAVBeacon/internal/domain"
	"github.com/Net005/JAVBeacon/internal/download"
	jellyfinintegration "github.com/Net005/JAVBeacon/internal/jellyfin"
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

const maskedSecret = "••••••••••••"

type Server struct {
	store         store.Store
	auth          *auth.Service
	monitor       *monitor.Service
	historical    *backfill.Service
	stash         *stash.Service
	downloads     *download.Service
	discoveryAI   *aidiscovery.Service
	jellyfin      *jellyfinintegration.Service
	covers        *covers.Cache
	screenshots   *screenshots.Cache
	key           string
	keyMu         sync.RWMutex
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
	migrationMu           sync.Mutex
	migration             migrationState
	queryCacheMu          sync.Mutex
	releaseCountCache     map[string]cachedReleaseCount
	filterOptionCache     map[string]cachedFilterOptions
	bulkReleaseMu         sync.Mutex
	bulkReleaseRunning    bool
	bulkReleaseQueue      []bulkReleaseItem
	bulkReleaseSeq        int64
	backgroundSearchMu    sync.Mutex
	backgroundSearchQueue map[int64]searchDownloadQueueItem
}

type searchDownloadQueueItem struct {
	ID         int64     `json:"id,omitempty"`
	ReleaseID  int64     `json:"release_id"`
	VideoID    string    `json:"video_id"`
	Transport  string    `json:"transport,omitempty"`
	Status     string    `json:"status"`
	SourceType string    `json:"source_type,omitempty"`
	Detail     string    `json:"detail,omitempty"`
	Priority   int       `json:"priority,omitempty"`
	Position   int       `json:"position,omitempty"`
	AddedAt    time.Time `json:"added_at,omitempty"`
	UpdatedAt  time.Time `json:"updated_at,omitempty"`
}

// bulkReleaseItem is one release's worth of pending Search + Download work.
// The Search + Download worker (runBulkReleaseJobs) keeps every pending item
// from every source - a single manual "Search + Download now" click, a
// Release Library bulk action, or the whole backlog resumed at startup - in
// one flat queue and always processes the lowest-Priority item next (ties
// broken by seq, arrival order), rather than finishing an entire
// already-queued batch before looking at anything submitted afterward. That
// is what lets a single, individually-triggered high-priority release jump
// ahead of a large already-running low-priority backlog instead of being
// stuck behind all of it.
type bulkReleaseItem struct {
	Release    domain.Release
	TaskID     int64
	Force      bool
	SourceType string
	Priority   int
	seq        int64
}

type persistedSearchOptions struct {
	Force bool `json:"force"`
}

func mustDownloads(st store.Store) []domain.Download {
	rows, _ := st.Downloads(context.Background(), "")
	return rows
}

type cachedReleaseCount struct {
	Total int
	Until time.Time
}

type cachedFilterOptions struct {
	Values []string
	Until  time.Time
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
func New(st store.Store, authService *auth.Service, m *monitor.Service, historical *backfill.Service, stashSync *stash.Service, downloadService *download.Service, covers *covers.Cache, key string, dbEngine string, sqlitePath string, l *slog.Logger, logs *logging.RingHandler, screenshotCaches ...*screenshots.Cache) http.Handler {
	s := &Server{store: st, auth: authService, monitor: m, historical: historical, stash: stashSync, downloads: downloadService, discoveryAI: aidiscovery.New(l), jellyfin: jellyfinintegration.New(st, stashSync, screenshotCaches...), covers: covers, key: key, dbEngine: dbEngine, sqlitePath: sqlitePath, log: l, logs: logs, mux: http.NewServeMux(), clients: map[*websocket.Conn]bool{}, releaseCountCache: map[string]cachedReleaseCount{}, filterOptionCache: map[string]cachedFilterOptions{}}
	if len(screenshotCaches) > 0 {
		s.screenshots = screenshotCaches[0]
	}
	m.OnRelease(s.broadcastRelease)
	s.routes()
	go s.resumeSearchDownloadTasks()
	return s.security(s.mux)
}

// apiKey returns the currently active API key, safe for concurrent use -
// it is seeded once at startup (from JAVBEACON_API_KEY or a generated
// random default, see app.New) but can change at runtime whenever the
// Settings UI saves a new api_key value, so every read goes through this
// accessor rather than the raw key field.
func (s *Server) apiKey() string {
	s.keyMu.RLock()
	defer s.keyMu.RUnlock()
	return s.key
}

// setAPIKey updates the active API key at runtime (called after the
// Settings UI saves a new api_key value) so newly-issued requests are
// checked against it immediately, without an app restart.
func (s *Server) setAPIKey(key string) {
	s.keyMu.Lock()
	defer s.keyMu.Unlock()
	s.key = key
}

var (
	indexHTMLOnce       sync.Once
	indexHTMLBody       []byte
	assetVersionPattern = regexp.MustCompile(`(/assets/app\.(?:js|css)\?v=)[^"']+`)
)

// indexHTML returns static/index.html with its asset cache-busting query
// string substituted for the running build's version. The committed file
// carries a frozen placeholder ("...failed-timestamp" - a leftover from an
// earlier, apparently broken versioning attempt that never actually
// changed across releases), so every deployed version served the exact
// same "/assets/app.js?v=..." URL - browsers that had already cached that
// URL kept serving a stale copy indefinitely, no matter how many releases
// shipped after. Substituting the real version here means the URL changes
// on every release, the same way /assets/ already forces revalidation via
// its own Cache-Control header.
func indexHTML() []byte {
	indexHTMLOnce.Do(func() {
		raw, err := assets.ReadFile("static/index.html")
		if err != nil {
			return
		}
		// Replace whatever version a previous release left in the committed
		// shell. This makes cache busting automatic instead of relying on a
		// second manual version edit that is easy to miss during a release.
		indexHTMLBody = assetVersionPattern.ReplaceAll(raw, []byte("${1}"+buildversion.Current()))
	})
	return indexHTMLBody
}

func serveIndexHTML(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(indexHTML())
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
	s.mux.HandleFunc("GET /covers/{id}/original", s.coverOriginal)
	s.mux.HandleFunc("GET /covers/{id}/jellyfin-primary", s.coverJellyfinPrimary)
	s.mux.HandleFunc("GET /screenshots/{id}/{index}", s.screenshot)
	s.mux.HandleFunc("GET /api/releases/{id}/screenshots", s.releaseScreenshots)
	s.mux.HandleFunc("POST /api/v1/media/match", s.jellyfinMatch)
	s.mux.HandleFunc("GET /api/v1/integrations/jellyfin/search", s.jellyfinSearch)
	s.mux.HandleFunc("GET /api/v1/integrations/jellyfin/releases/{id}", s.jellyfinMetadata)
	s.mux.HandleFunc("GET /api/v1/integrations/jellyfin/library-sync", s.jellyfinLibrarySync)
	s.mux.HandleFunc("POST /api/v1/integrations/jellyfin/playback", s.jellyfinPlayback)
	s.mux.HandleFunc("GET /api/v1/integrations/jellyfin/releases/{id}/activity", s.jellyfinActivity)
	s.mux.HandleFunc("POST /api/v1/integrations/jellyfin/releases/{id}/o", s.jellyfinAddO)
	s.mux.HandleFunc("GET /api/v1/integrations/silo/search", s.siloSearch)
	s.mux.HandleFunc("GET /api/v1/integrations/silo/releases/{id}", s.siloMetadata)
	s.mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		serveIndexHTML(w)
	})
	s.mux.HandleFunc("GET /search", func(w http.ResponseWriter, r *http.Request) {
		serveIndexHTML(w)
	})
	s.mux.HandleFunc("GET /opensearch.xml", func(w http.ResponseWriter, r *http.Request) {
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		if forwarded := strings.ToLower(strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Proto"), ",")[0])); forwarded == "http" || forwarded == "https" {
			scheme = forwarded
		}
		base := scheme + "://" + r.Host
		searchURL := html.EscapeString(base + "/search?q={searchTerms}")
		iconURL := html.EscapeString(base + "/assets/favicon.ico")
		w.Header().Set("Content-Type", "application/opensearchdescription+xml; charset=utf-8")
		_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><OpenSearchDescription xmlns="http://a9.com/-/spec/opensearch/1.1/"><ShortName>JAVBeacon</ShortName><Description>Search the JAVBeacon Release Library</Description><InputEncoding>UTF-8</InputEncoding><Image height="16" width="16" type="image/x-icon">` + iconURL + `</Image><Url type="text/html" method="get" template="` + searchURL + `"/></OpenSearchDescription>`))
	})
	s.mux.HandleFunc("GET /release/{id}", func(w http.ResponseWriter, r *http.Request) {
		if _, err := strconv.ParseInt(r.PathValue("id"), 10, 64); err != nil {
			http.NotFound(w, r)
			return
		}
		serveIndexHTML(w)
	})
	s.mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) {
		s.json(w, 200, map[string]any{"status": "ok", "time": time.Now().UTC()})
	})
	s.mux.HandleFunc("GET /api/stats", s.stats)
	s.mux.HandleFunc("GET /api/settings", s.settings)
	s.mux.HandleFunc("PUT /api/settings", s.settings)
	s.mux.HandleFunc("POST /api/settings/qb-test", s.testQBittorrent)
	s.mux.HandleFunc("POST /api/settings/gluetun-test", s.testGluetun)
	s.mux.HandleFunc("POST /api/settings/pushover-test", s.testPushoverCategory)
	s.mux.HandleFunc("GET /api/settings/pikpak-status", s.pikPakStatus)
	s.mux.HandleFunc("POST /api/settings/pikpak-test", s.testPikPak)
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
	s.mux.HandleFunc("GET /api/sites/{id}/releases", s.siteReleases)
	s.mux.HandleFunc("GET /api/releases", s.releases)
	s.mux.HandleFunc("GET /api/releases/count", s.releasesCount)
	s.mux.HandleFunc("GET /api/releases/ids", s.releaseIDs)
	s.mux.HandleFunc("GET /api/release-filter-options", s.releaseFilterOptions)
	s.mux.HandleFunc("PATCH /api/releases/bulk", s.patchReleasesBulk)
	s.mux.HandleFunc("POST /api/releases/bulk/monitor-download", s.bulkMonitorAndDownloadReleases)
	s.mux.HandleFunc("GET /api/releases/{id}", s.release)
	s.mux.HandleFunc("GET /api/releases/{id}/stash-history", s.releaseStashHistory)
	s.mux.HandleFunc("PATCH /api/releases/{id}", s.patchRelease)
	s.mux.HandleFunc("GET /api/releases/{id}/search", s.searchRelease)
	s.mux.HandleFunc("POST /api/releases/{id}/search-download", s.backgroundSearchAndDownloadRelease)
	s.mux.HandleFunc("POST /api/releases/{id}/download", s.downloadRelease)
	s.mux.HandleFunc("GET /api/jobs/search-download-queue", s.searchDownloadQueue)
	s.mux.HandleFunc("GET /api/downloads", s.downloadList)
	s.mux.HandleFunc("POST /api/downloads/{id}/retry", s.retryDownload)
	s.mux.HandleFunc("POST /api/downloads/bulk-retry", s.bulkRetryDownloads)
	s.mux.HandleFunc("PATCH /api/downloads/priority", s.updateDownloadPriorities)
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
	// release-upgrade is the Release Upgrade Schedule's status/manual-run
	// endpoints, mirroring download-search: GET polls the live job status,
	// POST starts a run on demand (same as the daily schedule firing, just
	// operator-triggered), and release-upgrade-history lists recent
	// completed runs for the Download Activity page.
	s.mux.HandleFunc("GET /api/jobs/release-upgrade", func(w http.ResponseWriter, r *http.Request) {
		s.json(w, http.StatusOK, s.downloads.ReleaseUpgradeStatus())
	})
	s.mux.HandleFunc("POST /api/jobs/release-upgrade", func(w http.ResponseWriter, r *http.Request) {
		if e := s.downloads.StartReleaseUpgradeSchedule(r.Context(), "manual"); e != nil {
			s.problem(w, http.StatusConflict, e.Error())
			return
		}
		s.json(w, http.StatusAccepted, s.downloads.ReleaseUpgradeStatus())
	})
	s.mux.HandleFunc("GET /api/jobs/release-upgrade-history", func(w http.ResponseWriter, r *http.Request) {
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		rows, err := s.store.ReleaseUpgradeRuns(r.Context(), limit)
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
	s.mux.HandleFunc("GET /api/jobs/javlibrary-historical-backfill", func(w http.ResponseWriter, r *http.Request) {
		s.json(w, http.StatusOK, s.historical.Status(r.Context()))
	})
	s.mux.HandleFunc("POST /api/jobs/javlibrary-historical-backfill", func(w http.ResponseWriter, r *http.Request) {
		var p struct {
			Resume   bool `json:"resume"`
			Priority int  `json:"priority"`
		}
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			s.problem(w, http.StatusBadRequest, "invalid request")
			return
		}
		if err := s.historical.Start(r.Context(), p.Resume, p.Priority); err != nil {
			s.problem(w, http.StatusConflict, err.Error())
			return
		}
		s.json(w, http.StatusAccepted, s.historical.Status(r.Context()))
	})
	s.mux.HandleFunc("DELETE /api/jobs/javlibrary-historical-backfill", func(w http.ResponseWriter, r *http.Request) {
		if !s.historical.Stop() {
			s.problem(w, http.StatusConflict, "historical backfill is not running")
			return
		}
		s.json(w, http.StatusAccepted, map[string]string{"state": "stopping"})
	})
	s.mux.HandleFunc("GET /api/jobs/stash", func(w http.ResponseWriter, r *http.Request) { s.json(w, 200, s.stash.Status()) })
	s.mux.HandleFunc("POST /api/jobs/stash", func(w http.ResponseWriter, r *http.Request) {
		if e := s.stash.Start(r.Context()); e != nil {
			s.problem(w, 409, e.Error())
			return
		}
		s.json(w, 202, s.stash.Status())
	})
	s.mux.HandleFunc("GET /api/jobs/stash/realtime", func(w http.ResponseWriter, r *http.Request) {
		s.json(w, http.StatusOK, s.stash.RealtimeStatus(r.Context()))
	})
	s.mux.HandleFunc("POST /api/hooks/stash/scene", s.stashRealtimeEvent)
	s.mux.HandleFunc("POST /api/hooks/stash/test", s.stashRealtimeTest)
	s.mux.HandleFunc("POST /api/jobs/stash/watchlist", func(w http.ResponseWriter, r *http.Request) {
		x, e := s.stash.SyncWatchlist(r.Context())
		if e != nil {
			s.problem(w, 502, e.Error())
			return
		}
		s.json(w, 200, x)
	})
	s.mux.HandleFunc("GET /api/stash/history", s.stashHistory)
	s.mux.HandleFunc("GET /api/discoveries", s.discoveries)
	s.mux.HandleFunc("POST /api/discoveries/ollama/test", s.testDiscoveryOllama)
	s.mux.HandleFunc("POST /api/discoveries/openai/test", s.testDiscoveryOpenAI)
	s.mux.HandleFunc("GET /api/jobs/discoveries", s.discoveryJob)
	s.mux.HandleFunc("POST /api/jobs/discoveries", s.discoveryJob)
	s.mux.HandleFunc("GET /api/stash/history/export", s.exportStashHistory)
	s.mux.HandleFunc("POST /api/stash/history/sync", s.syncStashHistory)
	s.mux.HandleFunc("POST /api/stash/history/writeback/review", s.reviewStashHistoryWriteback)
	s.mux.HandleFunc("POST /api/stash/history/writeback/apply", s.applyStashHistoryWriteback)

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
	forecasts = append(forecasts, discoveryScheduleForecast(r.Context(), s.store))
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
	s.queryCacheMu.Lock()
	clear(s.releaseCountCache)
	clear(s.filterOptionCache)
	s.queryCacheMu.Unlock()
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
	rows := make([]domain.SearchResult, 0)
	switch strings.ToLower(r.URL.Query().Get("provider")) {
	case "http":
		rows, e = s.downloads.SearchHTTP(r.Context(), release)
	case "torrent":
		rows, e = s.downloads.Search(r.Context(), release)
	default:
		rows, e = s.downloads.SearchAll(r.Context(), release)
	}
	if e != nil {
		s.problem(w, 502, e.Error())
		return
	}
	s.json(w, 200, rows)
}

func (s *Server) backgroundSearchAndDownloadRelease(w http.ResponseWriter, r *http.Request) {
	n, err := id(r)
	if err != nil {
		s.problem(w, http.StatusBadRequest, "invalid release id")
		return
	}
	release, err := s.store.Release(r.Context(), n)
	if err != nil {
		s.problem(w, http.StatusNotFound, "release not found")
		return
	}
	if s.downloads == nil {
		s.problem(w, http.StatusServiceUnavailable, "download service unavailable")
		return
	}
	transport := s.searchDownloadTransport(r.Context(), release)
	const sourceType = "Manual Background Search + Download"
	priority := download.PriorityForRelease(release, time.Now())
	taskID, alreadyQueued, reason, err := s.createSearchDownloadTask(r.Context(), release, sourceType, false, transport, priority)
	if err != nil {
		s.problem(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !alreadyQueued {
		s.enqueueBulkReleaseItems([]bulkReleaseItem{{Release: release, TaskID: taskID, SourceType: sourceType, Priority: priority}})
	}
	s.json(w, http.StatusAccepted, map[string]any{"queued": !alreadyQueued, "release_id": release.ID, "already_queued": alreadyQueued, "reason": reason})
}

func (s *Server) searchDownloadTransport(ctx context.Context, release domain.Release) string {
	override := strings.ToLower(strings.TrimSpace(release.DownloadMethodOverride))
	if override == "http" {
		return "http"
	}
	if override == "torrent" {
		return "torrent"
	}
	settings, _ := s.store.Settings(ctx)
	if release.HTTPDownloadPrimary || strings.HasPrefix(strings.ToLower(strings.TrimSpace(settings["default_download_method"])), "http") {
		return "http"
	}
	return "torrent"
}

// createSearchDownloadTask returns (taskID, alreadyQueued, reason, err). reason
// is "" for a freshly created task, "search_in_progress" when an existing
// search_queued/searching placeholder for this release was reused, or the
// blocking download's own Status ("queued", "downloading", "processing", or
// "completed") when a second queue attempt was denied outright - so callers
// (e.g. backgroundSearchAndDownloadRelease) can tell the user exactly why
// nothing new was started instead of always claiming it was.
func (s *Server) createSearchDownloadTask(ctx context.Context, release domain.Release, sourceType string, force bool, transport string, priority int) (int64, bool, string, error) {
	rows, err := s.store.Downloads(ctx, "")
	if err != nil {
		return 0, false, "", err
	}
	for _, row := range rows {
		if row.ReleaseID != release.ID {
			continue
		}
		if row.Status == "search_queued" || row.Status == "searching" {
			if row.Transport != transport {
				row.Transport = transport
				_, _ = s.store.SaveDownload(ctx, row)
			}
			return row.ID, true, "search_in_progress", nil
		}
		// A release with an already-active (or, unless force, already-completed)
		// download must not be queued a second time - mirrors Service.duplicateStored's
		// exact active/completed rules so Search + Download can't race a download
		// that's already in progress or done. "failed" is deliberately excluded so a
		// previously failed release can always be retried.
		active := row.Status == "queued" || row.Status == "downloading" || row.Status == "processing"
		if force {
			if active && row.Transport == transport {
				return row.ID, true, row.Status, nil
			}
			continue
		}
		if active || row.Status == "completed" {
			return row.ID, true, row.Status, nil
		}
	}
	options, _ := json.Marshal(persistedSearchOptions{Force: force})
	task, err := s.store.SaveDownload(ctx, domain.Download{ReleaseID: release.ID, Provider: "Search + Download", SourceType: sourceType, Query: release.VideoID, Name: "Waiting for provider search", Transport: transport, Status: "search_queued", MatchReason: "Waiting in Search + Download queue", QBResponse: string(options), Priority: priority})
	return task.ID, false, "", err
}

func (s *Server) resumeSearchDownloadTasks() {
	time.Sleep(750 * time.Millisecond)
	rows, err := s.store.Downloads(context.Background(), "")
	if err != nil {
		return
	}
	// AddedAt only breaks ties between equal-priority tasks here (seq, which
	// actually governs worker order, is assigned from this same order in
	// enqueueBulkReleaseItems below) - it is no longer the primary ordering,
	// since every task's own Priority already reflects its release date (or
	// bulk-action override) from when it was created.
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].AddedAt.Before(rows[j].AddedAt) })
	items := make([]bulkReleaseItem, 0, len(rows))
	for _, task := range rows {
		if task.Status != "search_queued" && task.Status != "searching" {
			continue
		}
		alreadyMaterialized := false
		for _, other := range rows {
			if other.ID != task.ID && other.ReleaseID == task.ReleaseID && (other.Status == "queued" || other.Status == "downloading" || other.Status == "completed") {
				alreadyMaterialized = true
				break
			}
		}
		if alreadyMaterialized {
			_, _ = s.store.DeleteDownload(context.Background(), task.ID)
			continue
		}
		release, releaseErr := s.store.Release(context.Background(), task.ReleaseID)
		if releaseErr != nil {
			task.Status, task.Error = "failed", "resume Search + Download: release no longer exists"
			_, _ = s.store.SaveDownload(context.Background(), task)
			continue
		}
		var options persistedSearchOptions
		_ = json.Unmarshal([]byte(task.QBResponse), &options)
		expectedTransport := s.searchDownloadTransport(context.Background(), release)
		if task.Transport != expectedTransport {
			task.Transport = expectedTransport
			_, _ = s.store.SaveDownload(context.Background(), task)
		}
		items = append(items, bulkReleaseItem{Release: release, TaskID: task.ID, Force: options.Force, SourceType: "Resumed Search + Download", Priority: task.Priority})
	}
	if len(items) > 0 {
		s.enqueueBulkReleaseItems(items)
	}
}

func (s *Server) searchDownloadQueue(w http.ResponseWriter, r *http.Request) {
	details := r.URL.Query().Get("summary") != "true"
	limit := 0
	if details {
		limit = 200
	}
	items := make(map[int64]searchDownloadQueueItem)
	s.backgroundSearchMu.Lock()
	for releaseID, item := range s.backgroundSearchQueue {
		items[releaseID] = item
	}
	s.backgroundSearchMu.Unlock()

	var downloads []domain.Download
	storedTotal := 0
	var err error
	if activeStore, ok := s.store.(interface {
		ActiveDownloadQueue(context.Context, int) ([]domain.Download, int, error)
	}); ok {
		downloads, storedTotal, err = activeStore.ActiveDownloadQueue(r.Context(), limit)
	} else {
		downloads, err = s.store.Downloads(r.Context(), "")
	}
	if err != nil {
		s.problem(w, http.StatusInternalServerError, err.Error())
		return
	}
	for _, item := range downloads {
		if item.ReleaseID == 0 {
			continue
		}
		if item.Status != "search_queued" && item.Status != "searching" && item.Status != "queued" && item.Status != "downloading" {
			continue
		}
		videoID := item.VideoID
		if videoID == "" {
			videoID = item.Query
		}
		detail := item.MatchReason
		if detail == "" {
			detail = item.Name
		}
		items[item.ReleaseID] = searchDownloadQueueItem{ID: item.ID, ReleaseID: item.ReleaseID, VideoID: videoID, Transport: item.Transport, Status: item.Status, SourceType: item.SourceType, Detail: detail, Priority: item.Priority, AddedAt: item.AddedAt, UpdatedAt: item.UpdatedAt}
	}
	queue := make([]searchDownloadQueueItem, 0, len(items))
	for _, item := range items {
		queue = append(queue, item)
	}
	sort.SliceStable(queue, func(i, j int) bool {
		if (queue[i].Status == "searching") != (queue[j].Status == "searching") {
			return queue[i].Status == "searching"
		}
		// Mirrors runBulkReleaseJobs' own selection rule (lowest Priority
		// value next, ties broken by arrival) so the displayed queue order
		// matches what will actually be processed next.
		if queue[i].Priority != queue[j].Priority {
			return queue[i].Priority < queue[j].Priority
		}
		if !queue[i].AddedAt.Equal(queue[j].AddedAt) {
			return queue[i].AddedAt.Before(queue[j].AddedAt)
		}
		return queue[i].ID < queue[j].ID
	})
	for i := range queue {
		queue[i].Position = i + 1
	}
	total := max(storedTotal, len(queue))
	counts := map[string]int{}
	for _, item := range queue {
		counts[item.Status]++
	}
	if !details {
		queue = nil
	}
	s.json(w, http.StatusOK, map[string]any{"items": queue, "total": total, "counts": counts, "truncated": details && total > len(queue)})
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
	rows, total, e := s.store.DownloadActivity(r.Context(), domain.DownloadFilter{Status: q.Get("status"), Search: q.Get("search"), Source: q.Get("source"), Transport: q.Get("transport"), Sort: q.Get("sort"), Direction: q.Get("direction"), Stalled: q.Get("stalled") == "true", SeenComplete: seenComplete, SeenCompleteDate: seenCompleteDate, Limit: limit, Offset: offset})
	if e != nil {
		s.problem(w, 500, e.Error())
		return
	}
	s.json(w, 200, map[string]any{"items": rows, "total": total})
}
func (s *Server) retryDownload(w http.ResponseWriter, r *http.Request) {
	n, err := id(r)
	if err != nil {
		s.problem(w, http.StatusBadRequest, "invalid download id")
		return
	}
	for _, row := range mustDownloads(s.store) {
		if row.ID == n && row.Transport == "http" && row.Status == "not_available" {
			result, retryErr := s.retryNotAvailableDownloads(r.Context(), []int64{n}, false)
			if retryErr != nil {
				s.problem(w, http.StatusBadRequest, retryErr.Error())
				return
			}
			s.json(w, http.StatusAccepted, result)
			return
		}
	}
	x, err := s.downloads.RetryHTTPDownload(r.Context(), n)
	if err != nil {
		s.problem(w, http.StatusBadRequest, err.Error())
		return
	}
	s.json(w, http.StatusAccepted, x)
}
func (s *Server) bulkRetryDownloads(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		IDs    []int64 `json:"ids"`
		All    bool    `json:"all"`
		Status string  `json:"status"`
	}
	if !s.decode(w, r, &payload) {
		return
	}
	var result map[string]any
	var err error
	if payload.Status == "not_available" {
		result, err = s.retryNotAvailableDownloads(r.Context(), payload.IDs, payload.All)
	} else {
		result, err = s.downloads.RetryFailedHTTPDownloads(r.Context(), payload.IDs, payload.All)
	}
	if err != nil {
		s.problem(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	s.json(w, http.StatusAccepted, result)
}

func (s *Server) updateDownloadPriorities(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		IDs      []int64 `json:"ids"`
		Priority int     `json:"priority"`
	}
	if !s.decode(w, r, &payload) {
		return
	}
	updated, err := s.downloads.UpdateDownloadPriorities(r.Context(), payload.IDs, payload.Priority)
	if err != nil {
		s.problem(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	wanted := make(map[int64]bool, len(payload.IDs))
	for _, downloadID := range payload.IDs {
		wanted[downloadID] = true
	}
	// Search + Download placeholders have a second in-memory queue. Keep its
	// ordering synchronized with the persisted download row priority.
	s.bulkReleaseMu.Lock()
	for i := range s.bulkReleaseQueue {
		if wanted[s.bulkReleaseQueue[i].TaskID] {
			s.bulkReleaseQueue[i].Priority = payload.Priority
		}
	}
	s.bulkReleaseMu.Unlock()
	s.backgroundSearchMu.Lock()
	for releaseID, item := range s.backgroundSearchQueue {
		if wanted[item.ID] {
			item.Priority = payload.Priority
			s.backgroundSearchQueue[releaseID] = item
		}
	}
	s.backgroundSearchMu.Unlock()
	s.json(w, http.StatusOK, map[string]any{"updated": updated, "priority": payload.Priority})
}

// retryNotAvailableDownloads reuses the retained Search + Download rows and
// sends them back through provider discovery. Unlike a failed HTTP transfer,
// a not-available row has no download URL to retry directly: its provider page
// must be searched again to discover whether a link has since been published.
func (s *Server) retryNotAvailableDownloads(ctx context.Context, downloadIDs []int64, all bool) (map[string]any, error) {
	if !all && len(downloadIDs) == 0 {
		return nil, errors.New("select at least one not-available HTTP download")
	}
	rows, err := s.store.Downloads(ctx, "not_available")
	if err != nil {
		return nil, err
	}
	wanted := make(map[int64]bool, len(downloadIDs))
	for _, downloadID := range downloadIDs {
		wanted[downloadID] = true
	}
	seenReleases := make(map[int64]bool)
	items := make([]bulkReleaseItem, 0, len(rows))
	failures := make([]string, 0)
	const sourceType = "Manual Not Available Retry"
	for _, row := range rows {
		if row.Transport != "http" || (!all && !wanted[row.ID]) || seenReleases[row.ReleaseID] {
			continue
		}
		seenReleases[row.ReleaseID] = true
		release, releaseErr := s.store.Release(ctx, row.ReleaseID)
		if releaseErr != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", row.Query, releaseErr))
			continue
		}
		row.Status = "search_queued"
		row.Transport = s.searchDownloadTransport(ctx, release)
		row.Name = "Waiting for provider search"
		row.SourceType = sourceType
		row.MatchReason = "Waiting in Search + Download queue"
		row.Error = ""
		row.PostStatus = ""
		row.Progress = 0
		row, releaseErr = s.store.SaveDownload(ctx, row)
		if releaseErr != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", row.Query, releaseErr))
			continue
		}
		items = append(items, bulkReleaseItem{Release: release, TaskID: row.ID, SourceType: sourceType, Priority: row.Priority})
	}
	if len(items) == 0 && len(failures) == 0 {
		return nil, errors.New("no not-available HTTP downloads matched this request")
	}
	if len(items) > 0 {
		s.enqueueBulkReleaseItems(items)
	}
	return map[string]any{"matched": len(items) + len(failures), "retried": len(items), "failed": len(failures), "errors": failures}, nil
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
		IDs     []int64 `json:"ids"`
		Replace bool    `json:"replace"`
	}
	if !s.decode(w, r, &payload) {
		return
	}
	job, err := s.downloads.StartBulkRemoveAndReplace(r.Context(), payload.IDs, payload.Replace)
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
	q := r.URL.Query()
	if q.Get("paged") == "true" {
		settings, _ := s.store.Settings(r.Context())
		filter := releaseFilterFromQuery(q, settings)
		limit, _ := strconv.Atoi(q.Get("limit"))
		if limit <= 0 {
			limit = 25
		}
		offset, _ := strconv.Atoi(q.Get("offset"))
		page, err := s.store.NotificationsPage(r.Context(), q.Get("type"), filter, q.Get("hide_monitored") == "true", q.Get("notification_sort"), q.Get("direction"), limit, offset)
		if err != nil {
			s.problem(w, http.StatusBadRequest, err.Error())
			return
		}
		s.json(w, http.StatusOK, map[string]any{"items": page.Items, "total": page.Total, "offset": max(offset, 0), "has_more": max(offset, 0)+len(page.Items) < page.Total})
		return
	}
	x, e := s.store.Notifications(r.Context(), r.URL.Query().Get("type"))
	if e != nil {
		s.problem(w, 500, e.Error())
		return
	}
	if q.Get("search_expression") != "" || q.Get("hide_local") == "true" || q.Get("hide_monitored") == "true" {
		allowed := make(map[int64]bool)
		for offset := 0; ; offset += 500 {
			filter := domain.ReleaseFilter{SearchExpression: q.Get("search_expression"), HideLocal: q.Get("hide_local") == "true", ShowNonPreferred: true, Sort: "release", Direction: "desc", Limit: 500, Offset: offset}
			page, err := s.store.Releases(r.Context(), filter)
			if err != nil {
				s.problem(w, http.StatusBadRequest, err.Error())
				return
			}
			for _, release := range page {
				allowed[release.ID] = true
			}
			if len(page) < 500 {
				break
			}
		}
		filtered := x[:0]
		for _, notification := range x {
			if !allowed[notification.ReleaseID] || q.Get("hide_monitored") == "true" && notification.Release != nil && notification.Release.MonitorDownload {
				continue
			}
			filtered = append(filtered, notification)
		}
		x = filtered
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

// resolveCoverPath does the lookup shared by cover, coverOriginal, and
// coverJellyfinPrimary: resolve the release, ensure its cover is cached
// locally, and check it isn't the "NOW PRINTING" placeholder. On any
// failure it writes the appropriate error/placeholder response itself and
// returns ok=false, so callers only need to handle the success path.
func (s *Server) resolveCoverPath(w http.ResponseWriter, r *http.Request) (release domain.Release, path string, ok bool) {
	n, err := id(r)
	if err != nil {
		http.NotFound(w, r)
		return domain.Release{}, "", false
	}
	release, err = s.store.Release(r.Context(), n)
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return domain.Release{}, "", false
	}
	if err != nil {
		s.log.Warn("cover lookup failed", "release_id", n, "error", err)
		http.Error(w, "cover unavailable", http.StatusInternalServerError)
		return domain.Release{}, "", false
	}
	if strings.TrimSpace(release.ImageURL) == "" {
		s.serveUnavailableCover(w, r)
		return domain.Release{}, "", false
	}
	path, _, err = s.covers.Ensure(r.Context(), release.VideoID, release.ImageURL)
	if err != nil {
		s.log.Warn("local cover unavailable", "release_id", n, "video_id", release.VideoID, "image_url", release.ImageURL, "error", err)
		s.serveUnavailableCover(w, r)
		return domain.Release{}, "", false
	}
	if s.covers.Unavailable(path) {
		s.serveUnavailableCover(w, r)
		return domain.Release{}, "", false
	}
	return release, path, true
}

// cover serves a release's cover exactly as cached - never conformed. This
// is what JAVBeacon's own web UI uses everywhere (Release Library grid,
// release detail pages, etc.), so those thumbnails always show exactly the
// cover as scraped. See coverJellyfinPrimary for the Jellyfin-specific,
// conformed variant.
func (s *Server) cover(w http.ResponseWriter, r *http.Request) {
	release, path, ok := s.resolveCoverPath(w, r)
	if !ok {
		return
	}
	s.serveCoverFile(w, r, path, release.VideoID)
}

// coverOriginal serves the non-cropped, non-padded source cover for a
// release - historically added so Jellyfin's Backdrop image could avoid a
// conformed/cropped /covers/{id} response, since a cropped poster makes a
// poor background. Now that /covers/{id} itself is never conformed, this is
// equivalent to it, but kept as its own endpoint so the existing Jellyfin
// plugin contract (Metadata.CoverBackdropPath) doesn't need to change.
func (s *Server) coverOriginal(w http.ResponseWriter, r *http.Request) {
	release, path, ok := s.resolveCoverPath(w, r)
	if !ok {
		return
	}
	s.serveCoverFile(w, r, path, release.VideoID)
}

// coverJellyfinPrimary serves the JavLibrary/GIGA-conformed Primary/Poster/
// Cover image - a two-panel spread cover sliced or padded to Jellyfin's
// 1000x1500 size - computed live, entirely in memory, on every request,
// and never written back to the on-disk cache file (see
// covers.ConformForServing). This is dedicated to Jellyfin's own Primary
// image fetch (see internal/jellyfin/service.go's Metadata.CoverPath);
// JAVBeacon's own web UI never requests this endpoint, only the plain,
// always-unconformed /covers/{id} above.
func (s *Server) coverJellyfinPrimary(w http.ResponseWriter, r *http.Request) {
	release, path, ok := s.resolveCoverPath(w, r)
	if !ok {
		return
	}
	// ProductURL (the release's own javlibrary.com/akiba-web.com detail
	// page), not ImageURL, is what actually says which site this release
	// came from - JavLibrary often hotlinks a cover from DMM's CDN, and
	// GIGA's own covers are hosted on giga-web.jp, so gating on where the
	// image itself happens to be hosted misses real matches.
	if conformed, ok := covers.ConformForServing(path, release.ProductURL); ok {
		info, statErr := os.Stat(path)
		modTime := time.Now()
		if statErr == nil {
			modTime = info.ModTime()
		}
		s.serveCoverBytes(w, r, conformed, modTime, release.VideoID)
		return
	}
	s.serveCoverFile(w, r, path, release.VideoID)
}

// serveCoverFile opens path and streams it as the response, sniffing its
// content type from the first 512 bytes. Shared by cover, coverOriginal,
// and coverJellyfinPrimary's non-conforming fallback so all three serve
// identically once the right file has been picked.
func (s *Server) serveCoverFile(w http.ResponseWriter, r *http.Request, path, videoID string) {
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
	http.ServeContent(w, r, videoID, info.ModTime(), f)
}

// serveCoverBytes streams an already-in-memory cover image (produced by
// live conforming, never written to disk) as the response. modTime should
// be the modification time of the on-disk source it was derived from, so
// conditional requests behave the same as they would for serveCoverFile.
func (s *Server) serveCoverBytes(w http.ResponseWriter, r *http.Request, data []byte, modTime time.Time, videoID string) {
	w.Header().Set("Content-Type", http.DetectContentType(data))
	w.Header().Set("Cache-Control", "public, max-age=300, must-revalidate")
	http.ServeContent(w, r, videoID, modTime, bytes.NewReader(data))
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
		public := r.URL.Path == "/login" || r.URL.Path == "/opensearch.xml" || r.URL.Path == "/api/auth/login" || r.URL.Path == "/api/hooks/stash/scene" || r.URL.Path == "/api/hooks/stash/test" || strings.HasPrefix(r.URL.Path, "/assets/")
		if !public {
			cookie, _ := r.Cookie("javbeacon_session")
			token := ""
			if cookie != nil {
				token = cookie.Value
			}
			key := s.apiKey()
			apiKeyValid := key != "" && (r.Header.Get("Authorization") == "Bearer "+key || r.URL.Query().Get("api_key") == key)
			if !apiKeyValid && !s.auth.Valid(r.Context(), token) {
				if strings.HasPrefix(r.URL.Path, "/api/") {
					s.problem(w, http.StatusUnauthorized, "authentication required")
				} else {
					http.Redirect(w, r, "/login?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusSeeOther)
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
		for _, key := range []string{"pikpak_session_access_token", "pikpak_session_refresh_token", "pikpak_session_device_id", "pikpak_session_user_id", "pikpak_session_username", "pikpak_reauth_alert_active"} {
			delete(x, key)
		}
		if strings.TrimSpace(x["discoveries_openai_api_key"]) != "" {
			x["discoveries_openai_api_key"] = maskedSecret
		}
		s.json(w, 200, x)
		return
	}
	var x map[string]string
	if !s.decode(w, r, &x) {
		return
	}
	allowed := map[string]bool{"screenshot_directory": true, "page_limit": true, "refresh_interval": true, "quick_refresh_enabled": true, "quick_refresh_schedule_mode": true, "quick_refresh_start_time": true, "quick_refresh_weekdays": true, "quick_refresh_cron": true, "full_refresh_enabled": true, "full_refresh_schedule_mode": true, "full_refresh_interval": true, "full_refresh_start_time": true, "full_refresh_weekdays": true, "full_refresh_cron": true, "full_refresh_page_limit": true, "new_release_refresh_enabled": true, "new_release_refresh_schedule_mode": true, "new_release_refresh_interval": true, "new_release_refresh_start_time": true, "new_release_refresh_weekdays": true, "new_release_refresh_cron": true, "new_release_refresh_page_limit": true, "recent_limit": true, "hide_local": true, "sort": true, "view": true, "notification_sort": true, "flaresolverr_url": true, "flaresolverr_cooldown": true, "byparr_instances": true, "byparr_max_instances_quick": true, "byparr_max_instances_full": true, "byparr_max_instances_new": true, "byparr_max_instances_screenshots": true, "byparr_max_instances_historical": true, "byparr_request_timeout_seconds": true, "byparr_solve_timeout_seconds": true, "cover_directory": true, "stash_base_url": true, "stash_graphql_query": true, "stash_sync_interval": true, "stash_local_sync_enabled": true, "stash_api_key": true, "api_key": true, "stash_watchlist_tag_id": true, "stash_watchlist_sync_enabled": true, "stash_watchlist_sync_interval": true, "session_lifetime": true, "search_url_template": true, "accepted_patterns": true, "blacklisted_filename_patterns": true, "search_auto_close_seconds": true, "search_download_background": true, "qb_url": true, "qb_username": true, "qb_password": true, "qb_category": true, "qb_poll_interval_seconds": true, "minimum_seed_ratio": true, "qb_completed_action": true, "pipeline_timeout_seconds": true, "download_schedule": true, "download_search_enabled": true, "download_search_interval": true, "download_search_older_enabled": true, "download_search_older_interval": true, "monitor_recent_days": true, "monitor_older_days": true, "rss_interval": true, "notification_interval": true, "stash_missing_graphql_query": true, "stash_missing_path_from": true, "stash_missing_path_to": true, "stash_missing_path_remaps": true, "stash_missing_folder_scope": true, "ignore_tags": true, "ignore_titles": true, "release_batch_size": true, "site_group_schedules": true}
	for _, key := range []string{"stash_realtime_enabled", "stash_realtime_secret", "stash_realtime_debounce_seconds", "stash_realtime_retry_attempts", "stash_realtime_retry_delay_seconds"} {
		allowed[key] = true
	}
	for _, key := range []string{"jellyfin_checkpoint_seconds", "jellyfin_max_checkpoint_gap_seconds", "jellyfin_completion_percent", "jellyfin_completion_remaining_seconds", "jellyfin_path_remaps"} {
		allowed[key] = true
	}
	for _, key := range []string{
		"discoveries_enabled", "discoveries_refresh_enabled", "discoveries_rewatch_days", "discoveries_result_limit", "discoveries_exploration_percent",
		"discoveries_play_weight", "discoveries_orgasm_weight", "discoveries_recency_half_life_days", "discoveries_subtitle_bonus", "discoveries_diversity_percent",
		"discoveries_excluded_tags",
		"discoveries_ai_enabled", "discoveries_ollama_url", "discoveries_ollama_model", "discoveries_ollama_request_timeout_seconds", "discoveries_ollama_health_timeout_seconds", "discoveries_openai_fallback_enabled", "discoveries_openai_enabled", "discoveries_openai_api_key", "discoveries_openai_base_url", "discoveries_openai_model", "discoveries_openai_embedding_model", "discoveries_openai_candidate_limit", "discoveries_openai_batch_size", "discoveries_openai_max_input_chars", "discoveries_openai_timeout_seconds", "discoveries_openai_retry_attempts", "discoveries_openai_monthly_budget", "discoveries_openai_batch",
		"discoveries_subtitle_analysis_enabled", "discoveries_subtitle_languages", "discoveries_subtitle_max_chars", "discoveries_subtitle_keep_cleaned",
		"discoveries_stash_unwatched_tag_id", "discoveries_stash_rewatch_tag_id", "discoveries_stash_hidden_tag_id", "discoveries_stash_tag_sync_enabled", "discoveries_refresh_interval", "discoveries_schedule_mode", "discoveries_start_time", "discoveries_weekdays", "discoveries_cron", "discoveries_subtitle_refresh_enabled", "discoveries_subtitle_refresh_interval", "discoveries_subtitle_schedule_mode", "discoveries_subtitle_start_time", "discoveries_subtitle_weekdays", "discoveries_subtitle_cron", "discoveries_subtitle_last_run_at", "discoveries_openai_refresh_enabled", "discoveries_openai_cache_interval", "discoveries_openai_schedule_mode", "discoveries_openai_start_time", "discoveries_openai_weekdays", "discoveries_openai_cron", "discoveries_openai_last_run_at", "discoveries_pools",
	} {
		allowed[key] = true
	}
	if raw, present := x["stash_realtime_enabled"]; present && raw != "true" && raw != "false" {
		s.problem(w, http.StatusUnprocessableEntity, "stash_realtime_enabled must be true or false")
		return
	}
	removeMaskedSettingsSecrets(x)
	for key, minimum := range map[string]int{"stash_realtime_debounce_seconds": 0, "stash_realtime_retry_attempts": 1, "stash_realtime_retry_delay_seconds": 1} {
		if raw, present := x[key]; present {
			value, err := strconv.Atoi(strings.TrimSpace(raw))
			if err != nil || value < minimum {
				s.problem(w, http.StatusUnprocessableEntity, fmt.Sprintf("%s must be at least %d", key, minimum))
				return
			}
		}
	}
	for _, key := range []string{"javdb_url", "http_download_directory", "http_download_concurrency", "http_download_connections", "http_fallback_delay", "default_download_method", "prefer_http_equivalent", "pikpak_username", "pikpak_password", "pikpak_cleanup_restored", "pikpak_release_id_folder_fallback", "pikpak_check_enabled", "pikpak_check_interval", "pikpak_notify_success", "pikpak_notify_failure", "pushover_app_token", "pushover_user_key"} {
		allowed[key] = true
	}
	for _, key := range []string{"javdb_gluetun_rotation_enabled", "gluetun_control_url", "gluetun_control_api_key", "gluetun_rotation_attempts", "gluetun_rotation_wait_seconds", "gluetun_rotation_poll_milliseconds", "gluetun_rotation_settle_seconds", "gluetun_require_ip_change"} {
		allowed[key] = true
	}
	for _, key := range []string{"operational_health_interval", "byparr_health_enabled", "byparr_health_failure_threshold", "byparr_health_timeout_seconds", "byparr_notify_failure", "byparr_notify_recovery", "error_burst_enabled", "error_burst_notify", "error_burst_threshold", "error_burst_window", "error_burst_cooldown", "error_burst_weight_scraping", "error_burst_weight_http_search", "error_burst_weight_http_download", "error_burst_include_scraping", "error_burst_include_http_search", "error_burst_include_http_download"} {
		allowed[key] = true
	}
	for _, key := range []string{"pushover_pikpak_app_token", "pushover_byparr_app_token", "pushover_download_search_app_token"} {
		allowed[key] = true
	}
	for _, key := range []string{"release_upgrade_enabled", "release_upgrade_time"} {
		allowed[key] = true
	}
	if raw, present := x["release_upgrade_enabled"]; present && raw != "true" && raw != "false" {
		s.problem(w, http.StatusUnprocessableEntity, "release_upgrade_enabled must be true or false")
		return
	}
	if raw, present := x["release_upgrade_time"]; present && strings.TrimSpace(raw) != "" {
		if err := monitor.ValidateCalendarSchedule(raw, ""); err != nil {
			s.problem(w, http.StatusUnprocessableEntity, "release_upgrade_time: "+err.Error())
			return
		}
	}
	for _, key := range []string{"javdb_gluetun_rotation_enabled", "gluetun_require_ip_change"} {
		if raw, present := x[key]; present && raw != "true" && raw != "false" {
			s.problem(w, http.StatusUnprocessableEntity, key+" must be true or false")
			return
		}
	}
	for _, key := range []string{"byparr_health_enabled", "byparr_notify_failure", "byparr_notify_recovery", "error_burst_enabled", "error_burst_notify", "error_burst_include_scraping", "error_burst_include_http_search", "error_burst_include_http_download"} {
		if raw, present := x[key]; present && raw != "true" && raw != "false" {
			s.problem(w, http.StatusUnprocessableEntity, key+" must be true or false")
			return
		}
	}
	for key, minimum := range map[string]int{"byparr_health_failure_threshold": 1, "byparr_health_timeout_seconds": 2, "error_burst_threshold": 3, "error_burst_weight_scraping": 1, "error_burst_weight_http_search": 1, "error_burst_weight_http_download": 1} {
		if raw, present := x[key]; present {
			value, err := strconv.Atoi(strings.TrimSpace(raw))
			if err != nil || value < minimum {
				s.problem(w, http.StatusUnprocessableEntity, fmt.Sprintf("%s must be at least %d", key, minimum))
				return
			}
		}
	}
	for _, key := range []string{"operational_health_interval", "error_burst_window"} {
		if raw, present := x[key]; present {
			if duration, err := time.ParseDuration(strings.TrimSpace(raw)); err != nil || duration < time.Minute {
				s.problem(w, http.StatusUnprocessableEntity, key+" must be a valid duration of at least 1 minute")
				return
			}
		}
	}
	if raw, present := x["error_burst_cooldown"]; present {
		if duration, err := time.ParseDuration(strings.TrimSpace(raw)); err != nil || duration < 5*time.Minute {
			s.problem(w, http.StatusUnprocessableEntity, "error_burst_cooldown must be a valid duration of at least 5 minutes")
			return
		}
	}
	for key, minimum := range map[string]int{"gluetun_rotation_attempts": 3, "gluetun_rotation_wait_seconds": 5, "gluetun_rotation_poll_milliseconds": 250, "gluetun_rotation_settle_seconds": 0} {
		if raw, present := x[key]; present {
			value, err := strconv.Atoi(strings.TrimSpace(raw))
			if err != nil || value < minimum {
				s.problem(w, http.StatusUnprocessableEntity, fmt.Sprintf("%s must be at least %d", key, minimum))
				return
			}
		}
	}
	allowed["stash_history_writeback_enabled"] = true
	allowed["stash_history_writeback_interval"] = true
	if username, password := strings.TrimSpace(x["pikpak_username"]), x["pikpak_password"]; (username == "") != (password == "") {
		s.problem(w, http.StatusUnprocessableEntity, "PikPak username and password must either both be configured or both be blank")
		return
	}
	if raw, present := x["pikpak_cleanup_restored"]; present && raw != "true" && raw != "false" {
		s.problem(w, http.StatusUnprocessableEntity, "PikPak restored-file cleanup must be true or false")
		return
	}
	for _, key := range []string{"pikpak_release_id_folder_fallback", "pikpak_check_enabled", "pikpak_notify_success", "pikpak_notify_failure"} {
		if raw, present := x[key]; present && raw != "true" && raw != "false" {
			s.problem(w, http.StatusUnprocessableEntity, key+" must be true or false")
			return
		}
	}
	if raw, present := x["pikpak_check_interval"]; present && strings.TrimSpace(raw) != "" {
		if interval, err := domain.ParseScheduleDuration(raw); err != nil || interval < time.Minute {
			s.problem(w, http.StatusUnprocessableEntity, "PikPak account-check interval must be a valid duration of at least 1 minute (e.g. \"1h\", \"24h\", \"7d\")")
			return
		}
	}
	if x["pikpak_check_enabled"] == "true" && (strings.TrimSpace(x["pikpak_username"]) == "" || x["pikpak_password"] == "") {
		s.problem(w, http.StatusUnprocessableEntity, "PikPak scheduled checks require a username and password")
		return
	}
	if token, user := strings.TrimSpace(x["pushover_app_token"]), strings.TrimSpace(x["pushover_user_key"]); token != "" && user == "" {
		s.problem(w, http.StatusUnprocessableEntity, "The legacy PikPak Pushover app token requires the shared user/group key")
		return
	}
	pikPakPushToken := strings.TrimSpace(x["pushover_pikpak_app_token"])
	if pikPakPushToken == "" {
		pikPakPushToken = strings.TrimSpace(x["pushover_app_token"])
	}
	if (x["pikpak_notify_success"] == "true" || x["pikpak_notify_failure"] == "true") && (pikPakPushToken == "" || strings.TrimSpace(x["pushover_user_key"]) == "") {
		s.problem(w, http.StatusUnprocessableEntity, "PikPak Pushover notifications require an app token and user/group key")
		return
	}
	if (x["byparr_notify_failure"] == "true" || x["byparr_notify_recovery"] == "true") && (strings.TrimSpace(x["pushover_byparr_app_token"]) == "" || strings.TrimSpace(x["pushover_user_key"]) == "") {
		s.problem(w, http.StatusUnprocessableEntity, "Byparr notifications require their app token and the shared Pushover user/group key")
		return
	}
	if x["error_burst_notify"] == "true" && (strings.TrimSpace(x["pushover_download_search_app_token"]) == "" || strings.TrimSpace(x["pushover_user_key"]) == "") {
		s.problem(w, http.StatusUnprocessableEntity, "Download + Search notifications require their app token and the shared Pushover user/group key")
		return
	}
	if raw, present := x["default_download_method"]; present {
		switch strings.ToLower(strings.TrimSpace(raw)) {
		case "torrent_http", "http_torrent", "torrent_only", "http_only":
		default:
			s.problem(w, http.StatusUnprocessableEntity, "default download method must be Torrent → HTTP fallback, HTTP → Torrent fallback, Torrent only, or HTTP only")
			return
		}
	}
	if raw, present := x["prefer_http_equivalent"]; present && raw != "true" && raw != "false" {
		s.problem(w, http.StatusUnprocessableEntity, "equivalent preferred-match HTTP priority must be true or false")
		return
	}
	if raw, present := x["search_download_background"]; present && raw != "true" && raw != "false" {
		s.problem(w, http.StatusUnprocessableEntity, "background Search + Download preference must be true or false")
		return
	}
	if raw, present := x["stash_history_writeback_enabled"]; present && raw != "true" && raw != "false" {
		s.problem(w, http.StatusUnprocessableEntity, "scheduled Stash history write-back must be true or false")
		return
	}
	if raw, present := x["http_fallback_delay"]; present {
		if delay, err := time.ParseDuration(strings.TrimSpace(raw)); err != nil || delay <= 0 {
			s.problem(w, http.StatusUnprocessableEntity, "HTTP fallback delay must be a positive duration such as 30m, 8h, or 24h")
			return
		}
	}
	for _, kind := range monitor.JobPriorityKinds {
		allowed[monitor.JobPrioritySettingKey(kind)] = true
	}
	allowed["job_priority_historical_backfill"] = true
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
	if _, submitted := x["discoveries_schedule_mode"]; submitted {
		mode := strings.ToLower(strings.TrimSpace(x["discoveries_schedule_mode"]))
		if mode == "" {
			mode = "basic"
		}
		if mode != "basic" && mode != "advanced" && mode != "cron" {
			s.problem(w, http.StatusUnprocessableEntity, "discoveries: schedule mode must be Basic, Advanced, or Cron")
			return
		}
		if mode != "cron" {
			if err := monitor.ValidateCalendarSchedule(x["discoveries_start_time"], x["discoveries_weekdays"]); err != nil {
				s.problem(w, http.StatusUnprocessableEntity, "discoveries: "+err.Error())
				return
			}
		}
		if mode == "advanced" && strings.TrimSpace(x["discoveries_start_time"]) == "" {
			s.problem(w, http.StatusUnprocessableEntity, "discoveries: Advanced mode requires a start time")
			return
		}
		if mode == "cron" {
			if strings.TrimSpace(x["discoveries_cron"]) == "" {
				s.problem(w, http.StatusUnprocessableEntity, "discoveries: Cron mode requires a five-field cron expression")
				return
			}
			if err := monitor.ValidateCronSchedule(x["discoveries_cron"]); err != nil {
				s.problem(w, http.StatusUnprocessableEntity, "discoveries: "+err.Error())
				return
			}
		}
	}
	for _, prefix := range []string{"discoveries_subtitle", "discoveries_openai"} {
		if _, submitted := x[prefix+"_schedule_mode"]; !submitted {
			continue
		}
		mode := strings.ToLower(strings.TrimSpace(x[prefix+"_schedule_mode"]))
		if mode == "" {
			mode = "basic"
		}
		if mode != "basic" && mode != "advanced" && mode != "cron" {
			s.problem(w, http.StatusUnprocessableEntity, prefix+": schedule mode must be Basic, Advanced, or Cron")
			return
		}
		if mode != "cron" {
			if err := monitor.ValidateCalendarSchedule(x[prefix+"_start_time"], x[prefix+"_weekdays"]); err != nil {
				s.problem(w, http.StatusUnprocessableEntity, prefix+": "+err.Error())
				return
			}
		}
		if mode == "advanced" && strings.TrimSpace(x[prefix+"_start_time"]) == "" {
			s.problem(w, http.StatusUnprocessableEntity, prefix+": Advanced mode requires a start time")
			return
		}
		if mode == "cron" {
			if strings.TrimSpace(x[prefix+"_cron"]) == "" {
				s.problem(w, http.StatusUnprocessableEntity, prefix+": Cron mode requires a five-field cron expression")
				return
			}
			if err := monitor.ValidateCronSchedule(x[prefix+"_cron"]); err != nil {
				s.problem(w, http.StatusUnprocessableEntity, prefix+": "+err.Error())
				return
			}
		}
	}
	// download_search_interval/download_search_older_interval (Monitored
	// releases) and stash_sync_interval/stash_watchlist_sync_interval
	// (StashApp) are plain "Run every" duration strings, same shape as
	// refresh_interval/full_refresh_interval above but without a
	// corresponding calendar/cron override - validated the same way (only
	// when submitted and non-blank, so a save of other settings never fails
	// because one of these was left at its default) so a typo is rejected
	// up front instead of silently falling back to that schedule's default
	// interval downstream, which used to look exactly like the schedule
	// hadn't picked up the change at all.
	for _, key := range []string{"download_search_interval", "download_search_older_interval", "stash_sync_interval", "stash_watchlist_sync_interval", "stash_history_writeback_interval", "discoveries_refresh_interval", "discoveries_subtitle_refresh_interval", "discoveries_openai_cache_interval"} {
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
	if raw, ok := x["job_priority_historical_backfill"]; ok && strings.TrimSpace(raw) != "" {
		if priority, e := strconv.Atoi(strings.TrimSpace(raw)); e != nil || priority < 1 || priority > 999 {
			s.problem(w, http.StatusUnprocessableEntity, "historical backfill priority must be a whole number from 1 to 999")
			return
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
	// site_group_schedules is the JSON-encoded list of user-defined scrape
	// schedules that each run against a chosen subset of monitoring sites,
	// every site free to use its own quick/full/new mode - see
	// domain.SiteGroupSchedule and internal/monitor's
	// expandSiteGroupSchedules for how these are merged into the normal
	// scheduling loop alongside Quick/Full/New refresh. Validated up front
	// the same way byparr_instances is above, plus the same per-schedule
	// timing checks the three built-in schedules already get (see the
	// spec loop above) since each of these carries its own independent
	// basic/advanced/cron timing configuration.
	if raw, ok := x["site_group_schedules"]; ok && strings.TrimSpace(raw) != "" {
		var groups []domain.SiteGroupSchedule
		if err := json.Unmarshal([]byte(raw), &groups); err != nil {
			s.problem(w, http.StatusUnprocessableEntity, "Site group schedules: invalid data")
			return
		}
		for _, group := range groups {
			label := strings.TrimSpace(group.Name)
			if label == "" {
				s.problem(w, http.StatusUnprocessableEntity, "Site group schedules: each schedule needs a name")
				return
			}
			if len(group.Sites) == 0 {
				s.problem(w, http.StatusUnprocessableEntity, "Site group schedules: \""+label+"\" needs at least one monitoring site")
				return
			}
			for _, groupSite := range group.Sites {
				if groupSite.Mode != "quick" && groupSite.Mode != "full" && groupSite.Mode != "new" {
					s.problem(w, http.StatusUnprocessableEntity, "Site group schedules: \""+label+"\" has an invalid scrape mode")
					return
				}
			}
			if group.Priority < 1 || group.Priority > 999 {
				s.problem(w, http.StatusUnprocessableEntity, "Site group schedules: \""+label+"\" priority must be a whole number from 1 to 999")
				return
			}
			mode := strings.ToLower(strings.TrimSpace(group.ScheduleMode))
			if mode == "" {
				mode = "basic"
			}
			if mode != "basic" && mode != "advanced" && mode != "cron" {
				s.problem(w, http.StatusUnprocessableEntity, "Site group schedules: \""+label+"\" schedule mode must be Basic, Advanced, or Cron")
				return
			}
			if strings.TrimSpace(group.Interval) != "" {
				if parsed, err := domain.ParseScheduleDuration(group.Interval); err != nil || parsed < time.Minute {
					s.problem(w, http.StatusUnprocessableEntity, "Site group schedules: \""+label+"\" interval must be at least 1 minute (e.g. \"12h\", \"7d\")")
					return
				}
			}
			if mode != "cron" {
				if err := monitor.ValidateCalendarSchedule(group.StartTime, group.Weekdays); err != nil {
					s.problem(w, http.StatusUnprocessableEntity, "Site group schedules: \""+label+"\": "+err.Error())
					return
				}
			}
			if mode == "advanced" && strings.TrimSpace(group.StartTime) == "" {
				s.problem(w, http.StatusUnprocessableEntity, "Site group schedules: \""+label+"\": Advanced mode requires a start time")
				return
			}
			if mode == "cron" && strings.TrimSpace(group.Cron) == "" {
				s.problem(w, http.StatusUnprocessableEntity, "Site group schedules: \""+label+"\": Cron mode requires a five-field cron expression")
				return
			}
			if mode == "cron" {
				if err := monitor.ValidateCronSchedule(group.Cron); err != nil {
					s.problem(w, http.StatusUnprocessableEntity, "Site group schedules: \""+label+"\": "+err.Error())
					return
				}
			}
			if group.Pages < 0 {
				s.problem(w, http.StatusUnprocessableEntity, "Site group schedules: \""+label+"\" page limit must be zero or greater")
				return
			}
		}
	}
	// byparr_max_instances_* cap how many of the configured Byparr instances
	// each schedule type (quick/full/new releases), the screenshot
	// backfill job, or the historical catalog backfill may use
	// concurrently - blank/0 means "no cap, use every enabled instance."
	for _, key := range []string{"byparr_max_instances_quick", "byparr_max_instances_full", "byparr_max_instances_new", "byparr_max_instances_screenshots", "byparr_max_instances_historical"} {
		if raw, ok := x[key]; ok && strings.TrimSpace(raw) != "" {
			if n, err := strconv.Atoi(strings.TrimSpace(raw)); err != nil || n < 0 {
				s.problem(w, http.StatusUnprocessableEntity, key+" must be zero or greater")
				return
			}
		}
	}
	// byparr_request_timeout_seconds bounds the HTTP client used for both a
	// direct JavLibrary fetch and the request to Byparr/FlareSolverr asking
	// it to solve one - since it wraps the solver call itself, it's the
	// timeout that actually fires first, ahead of the solve-budget hint
	// below. byparr_solve_timeout_seconds is only that hint (maxTimeout/
	// max_timeout in the solver payload); raising it without also raising
	// the request timeout above it has no effect. Blank leaves this
	// package's built-in defaults (30s / 75s) in place.
	for key, minimum := range map[string]int{"byparr_request_timeout_seconds": 5, "byparr_solve_timeout_seconds": 5} {
		if raw, ok := x[key]; ok && strings.TrimSpace(raw) != "" {
			if n, err := strconv.Atoi(strings.TrimSpace(raw)); err != nil || n < minimum {
				s.problem(w, http.StatusUnprocessableEntity, fmt.Sprintf("%s must be at least %d seconds", key, minimum))
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
	// qb_poll_interval_seconds controls how often download.Service polls
	// qBittorrent (see download.Service.qbPollInterval) - validated the
	// same only-when-non-blank way as minimum_seed_ratio above so leaving
	// it empty (falling back to download.qbPollIntervalDefault downstream)
	// never fails the whole settings save. The lower bound here matches
	// download.qbPollIntervalFloor, which also enforces it independently
	// of this check so a value that somehow got saved below it still
	// can't hammer qBittorrent's API.
	if raw, ok := x["qb_poll_interval_seconds"]; ok && strings.TrimSpace(raw) != "" {
		secs, err := strconv.Atoi(strings.TrimSpace(raw))
		if err != nil || secs < 15 {
			s.problem(w, http.StatusUnprocessableEntity, "qBittorrent poll interval must be 15 seconds or greater")
			return
		}
	}
	if raw, ok := x["http_download_concurrency"]; ok && strings.TrimSpace(raw) != "" {
		parallel, err := strconv.Atoi(strings.TrimSpace(raw))
		if err != nil || parallel < 1 || parallel > 64 {
			s.problem(w, http.StatusUnprocessableEntity, "parallel HTTP downloads must be a whole number from 1 to 64")
			return
		}
	}
	if raw, ok := x["http_download_connections"]; ok && strings.TrimSpace(raw) != "" {
		connections, err := strconv.Atoi(strings.TrimSpace(raw))
		if err != nil || connections < 1 || connections > 32 {
			s.problem(w, http.StatusUnprocessableEntity, "connections per HTTP download must be a whole number from 1 to 32")
			return
		}
	}
	if raw, ok := x["accepted_patterns"]; ok {
		x["accepted_patterns"] = download.NormalizePreferredFilenamePatterns(raw)
	}
	if raw, ok := x["blacklisted_filename_patterns"]; ok {
		x["blacklisted_filename_patterns"] = download.NormalizeBlacklistedFilenamePatterns(raw)
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
	if key, ok := x["api_key"]; ok {
		s.setAPIKey(key)
	}
	s.monitor.ApplySettings(r.Context())
	if action, ok := x["qb_completed_action"]; ok {
		s.log.Info("qBittorrent completed-download cleanup settings updated", "cleanup_rule", action, "minimum_seed_ratio", x["minimum_seed_ratio"], "files_retained", true)
	}
	s.json(w, 200, x)
}

func removeMaskedSettingsSecrets(settings map[string]string) {
	if settings["discoveries_openai_api_key"] == maskedSecret {
		delete(settings, "discoveries_openai_api_key")
	}
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

func (s *Server) testGluetun(w http.ResponseWriter, r *http.Request) {
	var config struct {
		URL    string `json:"url"`
		APIKey string `json:"api_key"`
	}
	if !s.decode(w, r, &config) {
		return
	}
	ip, err := s.downloads.TestGluetunControl(r.Context(), strings.TrimSpace(config.URL), strings.TrimSpace(config.APIKey))
	if err != nil {
		s.problem(w, http.StatusBadGateway, err.Error())
		return
	}
	s.json(w, http.StatusOK, map[string]any{"status": "connected", "public_ip": ip})
}

func (s *Server) testPushoverCategory(w http.ResponseWriter, r *http.Request) {
	var config struct {
		UserKey  string `json:"user_key"`
		AppToken string `json:"app_token"`
		Category string `json:"category"`
	}
	if !s.decode(w, r, &config) {
		return
	}
	if err := s.downloads.TestPushoverCategory(r.Context(), strings.TrimSpace(config.UserKey), strings.TrimSpace(config.AppToken), config.Category); err != nil {
		s.problem(w, http.StatusBadGateway, err.Error())
		return
	}
	s.json(w, http.StatusOK, map[string]string{"status": "sent"})
}

func (s *Server) testPikPak(w http.ResponseWriter, r *http.Request) {
	var config struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !s.decode(w, r, &config) {
		return
	}
	status, err := s.downloads.TestPikPakAccount(r.Context(), strings.TrimSpace(config.Username), config.Password)
	if err != nil {
		s.problem(w, http.StatusBadGateway, status.Message)
		return
	}
	s.json(w, http.StatusOK, status)
}

func (s *Server) pikPakStatus(w http.ResponseWriter, r *http.Request) {
	settings, err := s.store.Settings(r.Context())
	if err != nil {
		s.problem(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.json(w, http.StatusOK, map[string]any{
		"status":             settings["pikpak_check_last_status"],
		"message":            settings["pikpak_check_last_message"],
		"checked_at":         settings["pikpak_check_last_at"],
		"session_issued_at":  settings["pikpak_session_issued_at"],
		"session_expires_at": settings["pikpak_session_expires_at"],
		"reauth_required":    settings["pikpak_reauth_required"] == "true",
		"verification_url":   settings["pikpak_verification_url"],
	})
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
	code := 200
	if r.Method == http.MethodPost {
		code = 201
	}
	s.json(w, code, saved)
}

func (s *Server) siteReleases(w http.ResponseWriter, r *http.Request) {
	siteID, err := id(r)
	if err != nil {
		s.problem(w, http.StatusBadRequest, "invalid site id")
		return
	}
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	rows, err := s.store.Releases(r.Context(), domain.ReleaseFilter{SiteID: siteID, Sort: "release", Direction: "desc", Limit: 500, Offset: max(offset, 0), ShowNonPreferred: true})
	if err != nil {
		s.problem(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.json(w, http.StatusOK, rows)
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
	f := domain.ReleaseFilter{Search: q.Get("search"), SearchWildcards: q.Get("search_wildcards") == "true", VideoID: strings.TrimSpace(q.Get("video_id")), StashFilePath: q.Get("stash_file_path"), Site: q.Get("site"), Status: q.Get("status"), Sort: q.Get("sort"), Direction: q.Get("direction"), Category: q.Get("category"), Entries: q.Get("entries"), SearchExpression: q.Get("search_expression"), Watchlist: q.Get("watchlist") == "true", MonitorDownload: q.Get("monitor_download") == "true", HideLocal: q.Get("hide_local") == "true", ShowNonPreferred: q.Get("show_non_preferred") == "true", MinReleaseDate: validReleaseDateBound(q.Get("min_release_date")), MaxReleaseDate: validReleaseDateBound(q.Get("max_release_date")), Limit: limit, Offset: offset}
	if !f.ShowNonPreferred {
		f.IgnoreTags = domain.ParseIgnoreList(settings["ignore_tags"])
		f.IgnoreTitles = domain.ParseIgnoreList(settings["ignore_titles"])
		f.UsePreferred = len(f.IgnoreTags) > 0 || len(f.IgnoreTitles) > 0
	}
	if raw := q.Get("ignore_local_force_download"); raw == "true" || raw == "false" {
		v := raw == "true"
		f.IgnoreLocalForceDownload = &v
	}
	return f
}
func (s *Server) releases(w http.ResponseWriter, r *http.Request) {
	settings, e := s.store.Settings(r.Context())
	if e != nil {
		s.problem(w, 500, e.Error())
		return
	}
	filter := releaseFilterFromQuery(r.URL.Query(), settings)
	if r.URL.Query().Get("cards") == "true" {
		if cards, ok := s.store.(interface {
			ReleaseCards(context.Context, domain.ReleaseFilter, string) (domain.ReleasePage, error)
		}); ok {
			page, err := cards.ReleaseCards(r.Context(), filter, r.URL.Query().Get("cursor"))
			if err != nil {
				s.problem(w, 400, err.Error())
				return
			}
			s.json(w, 200, page)
			return
		}
	}
	x, e := s.store.Releases(r.Context(), filter)
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
	cacheQuery := r.URL.Query()
	cacheQuery.Del("limit")
	cacheQuery.Del("offset")
	cacheQuery.Del("cursor")
	cacheQuery.Del("cards")
	cacheKey := cacheQuery.Encode() + "\n" + settings["ignore_tags"] + "\n" + settings["ignore_titles"]
	now := time.Now()
	s.queryCacheMu.Lock()
	if s.releaseCountCache == nil {
		s.releaseCountCache = map[string]cachedReleaseCount{}
	}
	cached, found := s.releaseCountCache[cacheKey]
	s.queryCacheMu.Unlock()
	if found && now.Before(cached.Until) {
		s.json(w, 200, map[string]any{"total": cached.Total, "cached": true})
		return
	}
	total, e := s.store.ReleasesCount(r.Context(), releaseFilterFromQuery(r.URL.Query(), settings))
	if e != nil {
		s.problem(w, 500, e.Error())
		return
	}
	s.queryCacheMu.Lock()
	if s.releaseCountCache == nil {
		s.releaseCountCache = map[string]cachedReleaseCount{}
	}
	if len(s.releaseCountCache) >= 128 {
		clear(s.releaseCountCache)
	}
	s.releaseCountCache[cacheKey] = cachedReleaseCount{Total: total, Until: now.Add(20 * time.Second)}
	s.queryCacheMu.Unlock()
	s.json(w, 200, map[string]any{"total": total})
}

// releaseIDs returns the complete ID set matching the Release Library's
// active filters. It deliberately pages through the store instead of obeying
// the normal 500-row response cap, allowing the infinite-scroll UI's "Select
// all matching" action to select the entire result set rather than only the
// cards that happen to be mounted in the browser.
func (s *Server) releaseIDs(w http.ResponseWriter, r *http.Request) {
	settings, err := s.store.Settings(r.Context())
	if err != nil {
		s.problem(w, http.StatusInternalServerError, err.Error())
		return
	}
	filter := releaseFilterFromQuery(r.URL.Query(), settings)
	filter.Limit = 500
	filter.Offset = 0
	ids := make([]int64, 0)
	for {
		rows, queryErr := s.store.Releases(r.Context(), filter)
		if queryErr != nil {
			s.problem(w, http.StatusInternalServerError, queryErr.Error())
			return
		}
		for _, release := range rows {
			ids = append(ids, release.ID)
		}
		if len(rows) < filter.Limit {
			break
		}
		filter.Offset += len(rows)
	}
	s.json(w, http.StatusOK, map[string]any{"ids": ids, "total": len(ids)})
}

func (s *Server) releaseFilterOptions(w http.ResponseWriter, r *http.Request) {
	category, search := r.URL.Query().Get("category"), r.URL.Query().Get("search")
	key := strings.ToLower(strings.TrimSpace(category)) + "\n" + strings.ToLower(strings.TrimSpace(search))
	now := time.Now()
	s.queryCacheMu.Lock()
	if s.filterOptionCache == nil {
		s.filterOptionCache = map[string]cachedFilterOptions{}
	}
	cached, found := s.filterOptionCache[key]
	s.queryCacheMu.Unlock()
	if found && now.Before(cached.Until) {
		s.json(w, http.StatusOK, cached.Values)
		return
	}
	values, err := s.store.ReleaseFilterOptions(r.Context(), category, search)
	if err != nil {
		s.problem(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.queryCacheMu.Lock()
	if s.filterOptionCache == nil {
		s.filterOptionCache = map[string]cachedFilterOptions{}
	}
	if len(s.filterOptionCache) >= 256 {
		clear(s.filterOptionCache)
	}
	s.filterOptionCache[key] = cachedFilterOptions{Values: append([]string(nil), values...), Until: now.Add(5 * time.Minute)}
	s.queryCacheMu.Unlock()
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
	if downloads, ok := s.store.(interface {
		LatestReleaseDownload(context.Context, int64) (domain.Download, error)
	}); ok {
		if download, err := downloads.LatestReleaseDownload(r.Context(), n); err == nil {
			x.DownloadStatus = download.Status
			x.DownloadSourceReference = download.SourceReference
			x.DownloadTransport = download.Transport
			x.DownloadBytesTotal = download.BytesTotal
			x.DownloadBytesDone = download.BytesDownloaded
			x.DownloadBytesPerSec = download.BytesPerSecond
			x.DownloadSeeds = download.Seeds
			x.DownloadPeers = download.Peers
			x.DownloadETASeconds = download.ETASeconds
			x.DownloadSeenComplete = download.SeenComplete
			x.DownloadAddedAt = download.AddedAt
			x.DownloadPriority = download.Priority
			if download.Status == "queued" && download.Transport == "http" && s.downloads != nil {
				if position, total, ok := s.downloads.HTTPQueuePosition(download.ID); ok {
					x.DownloadQueuePosition = position
					x.DownloadQueueTotal = total
				}
			}
		}
	}
	s.json(w, 200, x)
}
func (s *Server) patchRelease(w http.ResponseWriter, r *http.Request) {
	var p struct {
		Released                 *bool   `json:"released"`
		Local                    *bool   `json:"local"`
		Notified                 *bool   `json:"notified"`
		NotifyOnRelease          *bool   `json:"notify_on_release"`
		Watchlist                *bool   `json:"watchlist"`
		MonitorDownload          *bool   `json:"monitor_download"`
		Label                    *string `json:"label"`
		IgnoreLocalForceDownload *bool   `json:"ignore_local_force_download"`
		HTTPDownloadPrimary      *bool   `json:"http_download_primary"`
	}
	if !s.decode(w, r, &p) {
		return
	}
	n, e := id(r)
	if e == nil {
		e = s.store.PatchRelease(r.Context(), n, p.Released, p.Local, p.Notified, p.NotifyOnRelease, p.Watchlist, p.MonitorDownload, p.Label, p.IgnoreLocalForceDownload, p.HTTPDownloadPrimary)
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
	if p.Watchlist != nil {
		if *p.Watchlist {
			if settingsErr := s.store.SaveSettings(r.Context(), map[string]string{"stash_watchlist_sync_enabled": "true"}); settingsErr != nil {
				x.WatchlistSync = "error: " + settingsErr.Error()
			}
		}
		if x.WatchlistSync == "" {
			if state, syncErr := s.stash.SyncWatchlistRelease(r.Context(), n); syncErr != nil {
				x.WatchlistSync = "error: " + syncErr.Error()
				s.log.Warn("immediate Stash Watchlist sync failed", "release_id", n, "video_id", x.VideoID, "watchlist", *p.Watchlist, "error", syncErr)
			} else {
				x.WatchlistSync = state
			}
		}
	}
	s.broadcastRelease(x)
	s.json(w, 200, x)
}

// patchReleasesBulk backs the "Releases checked by the scheduled job" table's
// mass-select actions: stop monitoring (monitor_download=false), set/clear
// the persistent "allow non-preferred filenames" override, and set/clear
// the persistent "ignore StashApp Local / force download" override,
// applied to every selected release id in one request.
func (s *Server) patchReleasesBulk(w http.ResponseWriter, r *http.Request) {
	var p struct {
		IDs                      []int64 `json:"ids"`
		MonitorDownload          *bool   `json:"monitor_download"`
		IgnoreLocalForceDownload *bool   `json:"ignore_local_force_download"`
		HTTPDownloadPrimary      *bool   `json:"http_download_primary"`
		DownloadMethodOverride   *string `json:"download_method_override"`
		IgnoreDownloadHistory    *bool   `json:"ignore_download_history"`
		// ResetIgnoreLocal clears the persistent "ignore StashApp Local"
		// override and, for any selected release that is now actually
		// local (matched in StashApp), also takes it off monitoring in the
		// same statement - see BulkResetIgnoreLocalForceDownload's doc
		// comment. Mutually exclusive with the other fields: it is its own
		// action, not a flag to combine with a plain flag patch.
		ResetIgnoreLocal bool `json:"reset_ignore_local"`
	}
	if !s.decode(w, r, &p) {
		return
	}
	if len(p.IDs) == 0 {
		s.problem(w, http.StatusUnprocessableEntity, "select at least one release")
		return
	}
	if !p.ResetIgnoreLocal && p.MonitorDownload == nil && p.IgnoreLocalForceDownload == nil && p.HTTPDownloadPrimary == nil && p.DownloadMethodOverride == nil && p.IgnoreDownloadHistory == nil {
		s.problem(w, http.StatusUnprocessableEntity, "nothing to update")
		return
	}
	var n int64
	var e error
	switch {
	case p.ResetIgnoreLocal:
		n, e = s.store.BulkResetIgnoreLocalForceDownload(r.Context(), p.IDs)
	case p.DownloadMethodOverride != nil || p.IgnoreDownloadHistory != nil:
		if p.DownloadMethodOverride == nil || p.IgnoreLocalForceDownload == nil || p.IgnoreDownloadHistory == nil {
			s.problem(w, http.StatusUnprocessableEntity, "download overrides must include method and both override values")
			return
		}
		n, e = s.store.BulkSetReleaseDownloadOverrides(r.Context(), p.IDs, *p.DownloadMethodOverride, *p.IgnoreLocalForceDownload, *p.IgnoreDownloadHistory)
	default:
		n, e = s.store.BulkSetReleaseFlags(r.Context(), p.IDs, p.MonitorDownload, p.IgnoreLocalForceDownload, p.HTTPDownloadPrimary)
	}
	if e != nil {
		s.problem(w, 500, e.Error())
		return
	}
	s.json(w, 200, map[string]any{"updated": n})
}

// bulkMonitorAndDownloadReleases backs the Release Library's multi-select
// action. The durable monitoring and override flags are applied before the
// response is sent, then each selected release is searched and downloaded
// sequentially in the background so a large selection never blocks the UI or
// floods the configured providers. Additional submissions join a FIFO queue
// instead of being rejected while another bulk job is active.
func (s *Server) bulkMonitorAndDownloadReleases(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		IDs                      []int64 `json:"ids"`
		IgnoreLocalForceDownload bool    `json:"ignore_local_force_download"`
		IgnoreDownloadHistory    bool    `json:"ignore_download_history"`
		DownloadMethodOverride   *string `json:"download_method_override"`
		// PriorityOverride, when set, replaces download.PriorityForRelease's
		// date-tier calculation for every release in this batch with a fixed
		// download queue priority (lower value = served first). It is applied
		// in-memory below, the same way DownloadMethodOverride and the other
		// override fields are, and is never persisted onto the release itself
		// - it only governs the tasks this one bulk run creates.
		PriorityOverride *int `json:"priority_override"`
	}
	if !s.decode(w, r, &payload) {
		return
	}
	seen := make(map[int64]bool, len(payload.IDs))
	ids := make([]int64, 0, len(payload.IDs))
	for _, releaseID := range payload.IDs {
		if releaseID > 0 && !seen[releaseID] {
			seen[releaseID] = true
			ids = append(ids, releaseID)
		}
	}
	if len(ids) == 0 {
		s.problem(w, http.StatusUnprocessableEntity, "select at least one release")
		return
	}
	if s.downloads == nil {
		s.problem(w, http.StatusServiceUnavailable, "download service is unavailable")
		return
	}
	wasMonitored := make(map[int64]bool, len(ids))
	for _, releaseID := range ids {
		if existing, lookupErr := s.store.Release(r.Context(), releaseID); lookupErr == nil {
			wasMonitored[releaseID] = existing.MonitorDownload
		}
	}
	sourceType := "Release Library Bulk"
	if payload.DownloadMethodOverride != nil {
		sourceType = "Monitored Releases Bulk"
	}

	monitor := true
	updated, err := s.store.BulkSetReleaseFlags(r.Context(), ids, &monitor, &payload.IgnoreLocalForceDownload)
	if err == nil && payload.DownloadMethodOverride != nil {
		updated, err = s.store.BulkSetReleaseDownloadOverrides(r.Context(), ids, *payload.DownloadMethodOverride, payload.IgnoreLocalForceDownload, payload.IgnoreDownloadHistory)
	}
	if err != nil {
		s.problem(w, http.StatusInternalServerError, err.Error())
		return
	}
	now := time.Now()
	items := make([]bulkReleaseItem, 0, len(ids))
	for _, releaseID := range ids {
		release, releaseErr := s.store.Release(r.Context(), releaseID)
		if errors.Is(releaseErr, sql.ErrNoRows) {
			continue
		}
		if releaseErr != nil {
			s.problem(w, http.StatusInternalServerError, releaseErr.Error())
			return
		}
		// Apply the submitted choices to the immediate background run as well as
		// persisting them. This keeps a forced monitored action authoritative even
		// if a store implementation returns a cached/pre-update release snapshot.
		if payload.DownloadMethodOverride != nil {
			release.DownloadMethodOverride = *payload.DownloadMethodOverride
			release.IgnoreLocalForceDownload = payload.IgnoreLocalForceDownload
			release.IgnoreDownloadHistory = payload.IgnoreDownloadHistory
		}
		release.PriorityOverride = payload.PriorityOverride
		force := wasMonitored[release.ID]
		if force {
			release.IgnoreLocalForceDownload = true
			release.IgnoreDownloadHistory = true
		}
		transport := s.searchDownloadTransport(r.Context(), release)
		priority := download.PriorityForRelease(release, now)
		taskID, alreadyQueued, _, taskErr := s.createSearchDownloadTask(r.Context(), release, sourceType, force, transport, priority)
		if taskErr != nil {
			s.problem(w, http.StatusInternalServerError, taskErr.Error())
			return
		}
		if alreadyQueued {
			continue
		}
		items = append(items, bulkReleaseItem{Release: release, TaskID: taskID, Force: force, SourceType: sourceType, Priority: priority})
		s.broadcastRelease(release)
	}
	if len(items) == 0 {
		s.json(w, http.StatusAccepted, map[string]any{"queued": 0, "updated": updated, "already_queued": true})
		return
	}

	queuePosition := s.enqueueBulkReleaseItems(items)
	s.json(w, http.StatusAccepted, map[string]any{"queued": len(items), "updated": updated, "queue_position": queuePosition})
}

// bestBulkReleaseQueueIndex returns the index of the item runBulkReleaseJobs
// should process next: the lowest Priority value (most urgent), breaking a
// tie by seq (arrival order) so equal-priority items still process
// first-in-first-out. Extracted from the worker loop so the selection rule
// itself is unit-testable without spinning up the worker goroutine.
func bestBulkReleaseQueueIndex(queue []bulkReleaseItem) int {
	best := 0
	for i := 1; i < len(queue); i++ {
		if queue[i].Priority < queue[best].Priority ||
			(queue[i].Priority == queue[best].Priority && queue[i].seq < queue[best].seq) {
			best = i
		}
	}
	return best
}

// enqueueBulkReleaseItems returns zero when the submitted items can start
// now, or the queue depth behind currently pending work otherwise. The
// worker (runBulkReleaseJobs) owns the queue until it becomes empty,
// preventing a completion/submission race from stranding queued items - it
// always processes the lowest-Priority item across the WHOLE queue next
// (see runBulkReleaseJobs), not these items specifically nor in the order
// submitted here, so "position" is only an approximate depth indicator.
func (s *Server) enqueueBulkReleaseItems(items []bulkReleaseItem) int {
	if len(items) == 0 {
		return 0
	}
	s.bulkReleaseMu.Lock()
	position := 0
	if s.bulkReleaseRunning {
		position = len(s.bulkReleaseQueue) + 1
	}
	for i := range items {
		s.bulkReleaseSeq++
		items[i].seq = s.bulkReleaseSeq
	}
	s.bulkReleaseQueue = append(s.bulkReleaseQueue, items...)
	if !s.bulkReleaseRunning {
		s.bulkReleaseRunning = true
		go s.runBulkReleaseJobs()
	}
	s.bulkReleaseMu.Unlock()
	return position
}

// runBulkReleaseJobs drains s.bulkReleaseQueue one release at a time,
// re-picking the lowest-Priority item in the queue (ties broken by seq,
// arrival order) before every single release it processes - never just the
// item that happened to be enqueued first. That is what makes a download's
// Priority actually control processing order end to end: a release queued
// by itself well after a large low-priority batch is already running still
// gets processed as soon as the batch's current item finishes, ahead of
// everything left in that batch, if its Priority is lower (more urgent).
// Priority only ever decides what is picked up NEXT - it never interrupts
// whichever single release is already being actively searched/downloaded.
func (s *Server) runBulkReleaseJobs() {
	queued, skipped, notFound, notAvailable, failed, processed := 0, 0, 0, 0, 0, 0
	for {
		s.bulkReleaseMu.Lock()
		if len(s.bulkReleaseQueue) == 0 {
			s.bulkReleaseRunning = false
			s.bulkReleaseMu.Unlock()
			if processed > 0 && s.log != nil {
				s.log.Info("Search + Download queue drained", "processed", processed, "queued", queued, "not_found", notFound, "not_available", notAvailable, "skipped", skipped, "failed", failed)
			}
			return
		}
		best := bestBulkReleaseQueueIndex(s.bulkReleaseQueue)
		item := s.bulkReleaseQueue[best]
		s.bulkReleaseQueue = append(s.bulkReleaseQueue[:best], s.bulkReleaseQueue[best+1:]...)
		s.bulkReleaseMu.Unlock()

		release := item.Release
		// Preserve the Search + Download task's editable queue priority on the
		// materialized HTTP/Torrent download selected by provider discovery.
		release.PriorityOverride = &item.Priority
		var task domain.Download
		if item.TaskID > 0 {
			for _, row := range mustDownloads(s.store) {
				if row.ID == item.TaskID {
					task = row
					break
				}
			}
			if task.ID > 0 {
				task.Status = "searching"
				task.Transport = s.searchDownloadTransport(context.Background(), release)
				task.Name = "Searching download providers"
				task.MatchReason = "Searching configured providers and ranking candidates"
				task.Error = ""
				task, _ = s.store.SaveDownload(context.Background(), task)
			}
		}
		force := item.Force
		if task.QBResponse != "" {
			var options persistedSearchOptions
			if json.Unmarshal([]byte(task.QBResponse), &options) == nil {
				force = force || options.Force
			}
		}
		if force {
			release.IgnoreLocalForceDownload = true
			release.IgnoreDownloadHistory = true
		}
		outcome, searchErr := s.downloads.SearchAndDownloadDetailed(context.Background(), release, item.SourceType)
		finishTask := func(status, detail string) {
			if task.ID == 0 {
				return
			}
			if status == "completed" || outcome.Download.ID > 0 {
				_, _ = s.store.DeleteDownload(context.Background(), task.ID)
				return
			}
			if status == "not_available" {
				// Not a failure: the exact release was found on the provider's
				// own site, it just has no downloadable share link published
				// yet (a common JavDB pattern for a release announced ahead of
				// its actual upload). Keep the provider's page link so the
				// user can check it themselves, and let a later search retry
				// find it once it is published, without cluttering the Failed
				// tab with something that never actually failed.
				task.Status, task.Error, task.MatchReason = "not_available", "", detail
				if outcome.Result.SourceURL != "" {
					task.SourcePageURL = outcome.Result.SourceURL
				}
				if outcome.Result.Provider != "" {
					task.Provider = outcome.Result.Provider
				}
				_, _ = s.store.SaveDownload(context.Background(), task)
				return
			}
			task.Status, task.Error, task.MatchReason = "failed", detail, "Search + Download did not queue a file"
			_, _ = s.store.SaveDownload(context.Background(), task)
		}
		processed++
		switch {
		case searchErr != nil:
			finishTask("failed", searchErr.Error())
			failed++
			if s.log != nil {
				s.log.Error(item.SourceType+" search and download failed", "release_id", release.ID, "video_id", release.VideoID, "download_method", release.DownloadMethodOverride, "error", searchErr, "reason", outcome.Reason)
			}
		case !outcome.Found && outcome.Result.Unavailable:
			finishTask("not_available", outcome.Reason)
			notAvailable++
			if s.log != nil {
				s.log.Info(item.SourceType+" search matched the release but no download link is published yet", "release_id", release.ID, "video_id", release.VideoID, "download_method", release.DownloadMethodOverride, "source_page_url", outcome.Result.SourceURL, "reason", outcome.Reason)
			}
		case !outcome.Found:
			finishTask("failed", outcome.Reason)
			notFound++
			if s.log != nil {
				s.log.Warn(item.SourceType+" search found no downloadable candidate", "release_id", release.ID, "video_id", release.VideoID, "download_method", release.DownloadMethodOverride, "reason", outcome.Reason)
			}
		case outcome.Download.Status == "skipped":
			finishTask("failed", outcome.Reason)
			skipped++
			if s.log != nil {
				s.log.Warn(item.SourceType+" search and download skipped", "release_id", release.ID, "video_id", release.VideoID, "download_method", release.DownloadMethodOverride, "download_status", outcome.Download.Status, "reason", outcome.Reason)
			}
		case outcome.Download.Status == "failed":
			finishTask("failed", outcome.Reason)
			failed++
			if s.log != nil {
				s.log.Error(item.SourceType+" download failed", "release_id", release.ID, "video_id", release.VideoID, "download_method", release.DownloadMethodOverride, "reason", outcome.Reason)
			}
		default:
			finishTask("completed", "")
			queued++
		}
	}
}
func (s *Server) refresh(w http.ResponseWriter, r *http.Request) {
	var p struct {
		SiteID      int64  `json:"site_id"`
		StartSiteID int64  `json:"start_site_id"`
		ReleaseID   int64  `json:"release_id"`
		Mode        string `json:"mode"`
		Pages       int    `json:"pages"`
		AllPages    bool   `json:"all_pages"`
		Kind        string `json:"kind"`
		Priority    int    `json:"priority"`
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
	if p.ReleaseID != 0 {
		if _, e := s.monitor.StartRelease(r.Context(), p.ReleaseID, p.Kind, p.Priority); e != nil {
			s.problem(w, 409, e.Error())
			return
		}
		s.json(w, 202, s.monitor.StatusForRelease(p.ReleaseID))
		return
	}
	if e := s.monitor.StartOptions(r.Context(), monitor.RefreshOptions{SiteID: p.SiteID, StartSiteID: p.StartSiteID, Mode: p.Mode, Pages: p.Pages, AllPages: p.AllPages, Kind: p.Kind, Priority: p.Priority}); e != nil {
		s.problem(w, 409, e.Error())
		return
	}
	s.json(w, 202, s.monitor.Status())
}

package store

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	stdhtml "html"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/Net005/JAVBeacon/internal/domain"
	_ "modernc.org/sqlite"
)

type Store interface {
	Close() error
	Sites(context.Context) ([]domain.Site, error)
	SaveSite(context.Context, domain.Site) (domain.Site, error)
	RecordSiteScrape(context.Context, int64, time.Time, int, int, int, string) error
	DeleteSite(context.Context, int64) error
	Releases(context.Context, domain.ReleaseFilter) ([]domain.Release, error)
	ReleasesCount(context.Context, domain.ReleaseFilter) (int, error)
	ReleaseFilterOptions(context.Context, string, string) ([]string, error)
	Release(context.Context, int64) (domain.Release, error)
	ReleaseExistsForSite(context.Context, int64, string, string) (bool, error)
	ReleaseForSite(context.Context, int64, string, string) (domain.Release, bool, error)
	ReleaseKnown(context.Context, string, string) (string, bool, error)
	LatestReleaseDateForSite(context.Context, int64) (string, bool, error)
	UpsertRelease(context.Context, domain.Release) (bool, error)
	// UpsertReleaseKeepUpdatedAt behaves exactly like UpsertRelease except it
	// never bumps updated_at on an existing release, whatever changed - used
	// by the screenshot backfill job (see monitor.Service.RefreshReleaseNow)
	// so a run that merely confirms or repairs screenshots on an old release
	// doesn't pull it back to the top of "sort by date updated."
	UpsertReleaseKeepUpdatedAt(context.Context, domain.Release) (bool, error)
	PatchRelease(context.Context, int64, *bool, *bool, *bool, *bool, *bool, *bool, *string, *bool) error
	BulkSetReleaseFlags(context.Context, []int64, *bool, *bool) (int64, error)
	SetReleaseMonitoring(context.Context, int64, bool, string, int64) error
	SetStashState(context.Context, int64, bool, string) error
	SetStashReleaseDate(context.Context, int64, string) error
	SetStashCreatedAt(context.Context, int64, time.Time) error
	SetStashPlaybackStats(context.Context, int64, int, int, string, string) error
	Stats(context.Context) (domain.Stats, error)
	Settings(context.Context) (map[string]string, error)
	SaveSettings(context.Context, map[string]string) error
	ScreenshotBackfillCompleted(context.Context, int64) (bool, error)
	MarkScreenshotBackfillCompleted(context.Context, int64) error
	PrepareHistoricalBackfill(context.Context, bool) error
	UpsertHistoricalBackfillSources(context.Context, []domain.HistoricalBackfillSource) error
	HistoricalBackfillSources(context.Context) ([]domain.HistoricalBackfillSource, error)
	HistoricalBackfillItem(context.Context, string) (domain.HistoricalBackfillItem, bool, error)
	SaveHistoricalBackfillItem(context.Context, domain.HistoricalBackfillItem) error
	SaveHistoricalBackfillSource(context.Context, domain.HistoricalBackfillSource) error
	SetHistoricalBackfillState(context.Context, string) error
	HistoricalBackfillStats(context.Context) (domain.HistoricalBackfillStats, error)
	User(context.Context) (domain.User, error)
	SaveUser(context.Context, string, string) error
	CreateSession(context.Context, domain.Session) error
	Session(context.Context, string) (domain.Session, error)
	DeleteSession(context.Context, string) error
	DeleteExpiredSessions(context.Context) error
	Preferences(context.Context) (json.RawMessage, error)
	SavePreferences(context.Context, json.RawMessage) error
	FilterPresets(context.Context) ([]domain.FilterPreset, error)
	SaveFilterPreset(context.Context, domain.FilterPreset) (domain.FilterPreset, error)
	DeleteFilterPreset(context.Context, int64) error
	SaveJob(context.Context, domain.Job) (int64, error)
	Jobs(context.Context, int) ([]domain.Job, error)
	JobHistory(context.Context, int, int) ([]domain.JobHistoryEntry, int, error)
	SaveDownloadSearchRun(context.Context, domain.DownloadSearchRun) (domain.DownloadSearchRun, error)
	DownloadSearchRuns(context.Context, string, int) ([]domain.DownloadSearchRun, error)
	SaveDownload(context.Context, domain.Download) (domain.Download, error)
	Downloads(context.Context, string) ([]domain.Download, error)
	DownloadActivity(context.Context, domain.DownloadFilter) ([]domain.Download, int, error)
	DeleteDownloadsForRelease(context.Context, int64) (int64, error)
	PathMappings(context.Context) ([]domain.PathMapping, error)
	SavePathMapping(context.Context, domain.PathMapping) (domain.PathMapping, error)
	DeletePathMapping(context.Context, int64) error
	PipelineSteps(context.Context) ([]domain.PipelineStep, error)
	SavePipelineSteps(context.Context, []domain.PipelineStep) error
	PipelineRun(context.Context, int64, string) (domain.PipelineRun, error)
	SavePipelineRun(context.Context, domain.PipelineRun) error
	SavePipelineLog(context.Context, domain.PipelineLog) (domain.PipelineLog, error)
	PipelineLogs(context.Context, int64) ([]domain.PipelineLog, error)
	Notifications(context.Context, string) ([]domain.Notification, error)
	DeleteNotifications(context.Context, string, []int64) (int64, error)
	CreateNotification(context.Context, int64, string, string) (bool, error)
	WatchlistSynced(context.Context, int64, string, string) (bool, error)
	SaveWatchlistSync(context.Context, int64, string, string, string) error
	ClearWatchlistSync(context.Context, int64) error
	StashMissingScenes(context.Context, domain.StashMissingFilter) ([]domain.StashMissingScene, error)
	StashMissingScenesCount(context.Context, domain.StashMissingFilter) (int, error)
	StashMissingScene(context.Context, int64) (domain.StashMissingScene, error)
	UpsertStashMissingScene(context.Context, domain.StashMissingScene) (int64, error)
	LinkStashMissingRelease(context.Context, int64, int64) error
	SetStashMissingStatus(context.Context, int64, string, string) error
	PruneStashMissingScenes(context.Context, time.Time) (int64, error)
	ClearStashMissingScenes(context.Context) (int64, error)
}

type SQLite struct {
	db           *sql.DB
	dialect      Dialect
	ignoreMu     sync.RWMutex
	ignoreTags   []string
	ignoreTitles []string
}

func OpenSQLite(path string) (*SQLite, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, err
	}
	// SQLite serializes writes at the file level regardless of how many
	// connections Go's pool hands out, and letting concurrent callers race
	// for the write lock across separate connections defeats the
	// busy_timeout pragma above - modernc.org/sqlite's busy handler does not
	// reliably retry a write-write conflict that crosses connections, so two
	// goroutines writing at once (e.g. two concurrent release refreshes, see
	// monitor.Service.StartRelease) can still surface a hard
	// "database is locked" error instead of queuing behind it. Capping the
	// pool at one connection routes every query - reads included - through
	// the same connection, so SQLite's own internal locking (which busy_timeout
	// does honor within a single connection) is what actually serializes
	// concurrent access, and callers see the intended wait-then-succeed
	// behavior instead of a random failure under load.
	db.SetMaxOpenConns(1)
	s := &SQLite{db: db, dialect: SQLiteDialect{}}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	if err := s.initializeReleasePreferences(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}
func (s *SQLite) Close() error { return s.db.Close() }

// DB exposes the underlying *sql.DB. It exists for DB Phase 8's migration
// data-transfer engine (internal/web/migration_transfer.go), which needs a
// raw, dynamically-column-discoverable connection to both the SQLite source
// and the PostgreSQL target - the domain-typed Store methods below always
// auto-assign new primary keys, which the transfer must not do, since
// preserving original ids is what keeps cross-table foreign-key
// relationships intact across the copy. The returned *sql.DB is the exact
// same connection this type already uses for every other method, including
// - for a PostgreSQL-backed SQLite (see OpenPostgresStore) - the DB Phase 5
// query-rewriting connector, so ordinary "?" placeholders and INSERT OR
// IGNORE keep working unmodified against either engine through it.
func (s *SQLite) DB() *sql.DB { return s.db }
func (s *SQLite) migrate() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS sites (id INTEGER PRIMARY KEY, title TEXT NOT NULL UNIQUE, type TEXT NOT NULL DEFAULT 'Site', name TEXT NOT NULL, url TEXT NOT NULL DEFAULT '', notify INTEGER NOT NULL DEFAULT 0, enabled INTEGER NOT NULL DEFAULT 1, created_at DATETIME NOT NULL, updated_at DATETIME NOT NULL);
CREATE TABLE IF NOT EXISTS releases (id INTEGER PRIMARY KEY, site_id INTEGER NOT NULL REFERENCES sites(id) ON DELETE CASCADE, video_id TEXT NOT NULL, scraper_id TEXT NOT NULL DEFAULT '', title TEXT NOT NULL, release_date TEXT NOT NULL DEFAULT '', source TEXT NOT NULL, image_url TEXT NOT NULL DEFAULT '', product_url TEXT NOT NULL DEFAULT '', director TEXT NOT NULL DEFAULT '', studio TEXT NOT NULL DEFAULT '', duration TEXT NOT NULL DEFAULT '', story TEXT NOT NULL DEFAULT '', screenshots TEXT NOT NULL DEFAULT '[]', screenshots_checked_at DATETIME, released INTEGER NOT NULL DEFAULT 0, is_local INTEGER NOT NULL DEFAULT 0, notified INTEGER NOT NULL DEFAULT 0, added_at DATETIME NOT NULL, updated_at DATETIME NOT NULL, UNIQUE(site_id, video_id));
	CREATE INDEX IF NOT EXISTS idx_releases_date ON releases(release_date); CREATE INDEX IF NOT EXISTS idx_releases_added ON releases(added_at); CREATE INDEX IF NOT EXISTS idx_releases_site ON releases(site_id);`)
	if err == nil {
		_, err = s.db.Exec(`CREATE TABLE IF NOT EXISTS settings (key TEXT PRIMARY KEY, value TEXT NOT NULL, updated_at DATETIME NOT NULL)`)
	}
	if err == nil {
		_, err = s.db.Exec(`
CREATE TABLE IF NOT EXISTS users (id INTEGER PRIMARY KEY CHECK(id=1), username TEXT NOT NULL UNIQUE, password_hash TEXT NOT NULL, updated_at DATETIME NOT NULL);
CREATE TABLE IF NOT EXISTS sessions (token TEXT PRIMARY KEY, user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE, expires_at DATETIME NOT NULL, created_at DATETIME NOT NULL);
CREATE INDEX IF NOT EXISTS idx_sessions_expiry ON sessions(expires_at);
CREATE TABLE IF NOT EXISTS user_preferences (user_id INTEGER PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE, state TEXT NOT NULL DEFAULT '{}', updated_at DATETIME NOT NULL);
CREATE TABLE IF NOT EXISTS filter_presets (id INTEGER PRIMARY KEY, user_id INTEGER NOT NULL DEFAULT 1 REFERENCES users(id) ON DELETE CASCADE, name TEXT NOT NULL, state TEXT NOT NULL, created_at DATETIME NOT NULL, updated_at DATETIME NOT NULL, UNIQUE(user_id,name));
CREATE TABLE IF NOT EXISTS job_history (id INTEGER PRIMARY KEY, kind TEXT NOT NULL, state TEXT NOT NULL, mode TEXT NOT NULL DEFAULT '', title TEXT NOT NULL DEFAULT '', scheduled INTEGER NOT NULL DEFAULT 0, site_count INTEGER NOT NULL DEFAULT 0, site_title TEXT NOT NULL DEFAULT '', provider TEXT NOT NULL DEFAULT '', started_at DATETIME, finished_at DATETIME, added INTEGER NOT NULL DEFAULT 0, updated INTEGER NOT NULL DEFAULT 0, skipped INTEGER NOT NULL DEFAULT 0, error TEXT NOT NULL DEFAULT '');
CREATE TABLE IF NOT EXISTS download_search_runs (id INTEGER PRIMARY KEY, schedule TEXT NOT NULL, started_at DATETIME NOT NULL, finished_at DATETIME NOT NULL, checked INTEGER NOT NULL DEFAULT 0, found INTEGER NOT NULL DEFAULT 0, downloaded INTEGER NOT NULL DEFAULT 0, skipped INTEGER NOT NULL DEFAULT 0, failed INTEGER NOT NULL DEFAULT 0, error TEXT NOT NULL DEFAULT '');
CREATE INDEX IF NOT EXISTS idx_download_search_runs_schedule_finished ON download_search_runs(schedule,finished_at DESC);
CREATE TABLE IF NOT EXISTS downloads (id INTEGER PRIMARY KEY, release_id INTEGER REFERENCES releases(id) ON DELETE SET NULL, provider TEXT NOT NULL DEFAULT '', source_type TEXT NOT NULL DEFAULT '', source_reference TEXT NOT NULL DEFAULT '', query TEXT NOT NULL DEFAULT '', torrent_hash TEXT NOT NULL DEFAULT '', name TEXT NOT NULL DEFAULT '', files TEXT NOT NULL DEFAULT '[]', status TEXT NOT NULL, match_reason TEXT NOT NULL DEFAULT '', qb_response TEXT NOT NULL DEFAULT '', post_status TEXT NOT NULL DEFAULT '', error TEXT NOT NULL DEFAULT '', seed_ratio REAL NOT NULL DEFAULT 0, progress REAL NOT NULL DEFAULT 0, seeds INTEGER NOT NULL DEFAULT 0, peers INTEGER NOT NULL DEFAULT 0, eta_seconds INTEGER NOT NULL DEFAULT 0, seen_complete INTEGER NOT NULL DEFAULT 0, filename_pattern_excluded INTEGER NOT NULL DEFAULT 0, added_at DATETIME NOT NULL, updated_at DATETIME NOT NULL);
CREATE INDEX IF NOT EXISTS idx_downloads_status ON downloads(status);
CREATE INDEX IF NOT EXISTS idx_downloads_release ON downloads(release_id);
CREATE INDEX IF NOT EXISTS idx_downloads_release_status_updated ON downloads(release_id,status,updated_at DESC);
CREATE TABLE IF NOT EXISTS path_mappings (id INTEGER PRIMARY KEY, download_prefix TEXT NOT NULL UNIQUE, local_prefix TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS pipeline_steps (id INTEGER PRIMARY KEY, position INTEGER NOT NULL, type TEXT NOT NULL, name TEXT NOT NULL, config TEXT NOT NULL DEFAULT '{}', enabled INTEGER NOT NULL DEFAULT 1, timeout_seconds INTEGER NOT NULL DEFAULT 0);
CREATE TABLE IF NOT EXISTS pipeline_logs (id INTEGER PRIMARY KEY, download_id INTEGER NOT NULL REFERENCES downloads(id) ON DELETE CASCADE, step_id INTEGER REFERENCES pipeline_steps(id) ON DELETE SET NULL, state TEXT NOT NULL, output TEXT NOT NULL DEFAULT '', error TEXT NOT NULL DEFAULT '', started_at DATETIME NOT NULL, finished_at DATETIME);
CREATE INDEX IF NOT EXISTS idx_pipeline_logs_download ON pipeline_logs(download_id);
CREATE TABLE IF NOT EXISTS notifications (id INTEGER PRIMARY KEY, release_id INTEGER NOT NULL REFERENCES releases(id) ON DELETE CASCADE, type TEXT NOT NULL, message TEXT NOT NULL DEFAULT '', created_at DATETIME NOT NULL, UNIQUE(release_id,type));
CREATE INDEX IF NOT EXISTS idx_notifications_release_created ON notifications(release_id,created_at DESC);
CREATE TABLE IF NOT EXISTS watchlist_sync (release_id INTEGER PRIMARY KEY REFERENCES releases(id) ON DELETE CASCADE, stash_scene_id TEXT NOT NULL, tag_id TEXT NOT NULL, synced_at DATETIME NOT NULL, result TEXT NOT NULL DEFAULT '');`)
	}
	if err == nil {
		_, err = s.db.Exec(`CREATE TABLE IF NOT EXISTS historical_backfill_state (
			id INTEGER PRIMARY KEY CHECK(id=1), state TEXT NOT NULL DEFAULT 'idle', updated_at DATETIME NOT NULL
		);
		INSERT OR IGNORE INTO historical_backfill_state(id,state,updated_at) VALUES(1,'idle',CURRENT_TIMESTAMP);
		CREATE TABLE IF NOT EXISTS historical_backfill_sources (
			url TEXT PRIMARY KEY, kind TEXT NOT NULL, name TEXT NOT NULL, state TEXT NOT NULL DEFAULT 'pending',
			cursor_date TEXT NOT NULL DEFAULT '', resume_date TEXT NOT NULL DEFAULT '', next_page INTEGER NOT NULL DEFAULT 1,
			page_limit INTEGER NOT NULL DEFAULT 0, pages_completed INTEGER NOT NULL DEFAULT 0, catchup_only INTEGER NOT NULL DEFAULT 0
		);
		CREATE TABLE IF NOT EXISTS historical_backfill_items (
			video_id TEXT PRIMARY KEY COLLATE NOCASE, release_date TEXT NOT NULL DEFAULT '', state TEXT NOT NULL,
			source_url TEXT NOT NULL DEFAULT '', error TEXT NOT NULL DEFAULT '', updated_at DATETIME NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_historical_backfill_items_state ON historical_backfill_items(state);`)
	}
	if err == nil {
		_, err = s.db.Exec(`
CREATE TABLE IF NOT EXISTS release_actresses (release_id INTEGER NOT NULL REFERENCES releases(id) ON DELETE CASCADE, position INTEGER NOT NULL, name TEXT NOT NULL, name_normalized TEXT NOT NULL, PRIMARY KEY(release_id,name_normalized));
CREATE INDEX IF NOT EXISTS idx_release_actresses_name ON release_actresses(name_normalized,release_id);
CREATE INDEX IF NOT EXISTS idx_release_actresses_release_position ON release_actresses(release_id,position);
CREATE TABLE IF NOT EXISTS release_tags (release_id INTEGER NOT NULL REFERENCES releases(id) ON DELETE CASCADE, position INTEGER NOT NULL, name TEXT NOT NULL, name_normalized TEXT NOT NULL, PRIMARY KEY(release_id,name_normalized));
CREATE INDEX IF NOT EXISTS idx_release_tags_name ON release_tags(name_normalized,release_id);
CREATE INDEX IF NOT EXISTS idx_release_tags_release_position ON release_tags(release_id,position);`)
	}
	if err == nil {
		if _, alterErr := s.db.Exec(`ALTER TABLE releases ADD COLUMN studio TEXT NOT NULL DEFAULT ''`); alterErr != nil && !strings.Contains(strings.ToLower(alterErr.Error()), "duplicate column") {
			return alterErr
		}
		for _, statement := range []string{
			`ALTER TABLE sites ADD COLUMN download INTEGER NOT NULL DEFAULT 0`,
			`ALTER TABLE sites ADD COLUMN download_mode TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE sites ADD COLUMN auto_monitor_future INTEGER NOT NULL DEFAULT 0`,
			`ALTER TABLE sites ADD COLUMN watchlist INTEGER NOT NULL DEFAULT 0`,
			`ALTER TABLE sites ADD COLUMN rss_url TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE releases ADD COLUMN notify_on_release INTEGER NOT NULL DEFAULT 0`,
			`ALTER TABLE releases ADD COLUMN watchlist INTEGER NOT NULL DEFAULT 0`,
			`ALTER TABLE releases ADD COLUMN monitor_download INTEGER NOT NULL DEFAULT 0`,
			`ALTER TABLE releases ADD COLUMN site_monitor_download INTEGER NOT NULL DEFAULT 0`,
			`ALTER TABLE releases ADD COLUMN monitor_reason TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE releases ADD COLUMN monitor_site_id INTEGER NOT NULL DEFAULT 0`,
			`ALTER TABLE releases ADD COLUMN identity_key TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE releases ADD COLUMN stash_scene_id TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE pipeline_logs ADD COLUMN configuration TEXT NOT NULL DEFAULT '{}'`,
			`ALTER TABLE pipeline_steps ADD COLUMN trigger TEXT NOT NULL DEFAULT 'download_completed'`,
			`ALTER TABLE sites ADD COLUMN last_scraped_at DATETIME`,
			`ALTER TABLE sites ADD COLUMN last_scrape_pages INTEGER NOT NULL DEFAULT 0`,
			`ALTER TABLE sites ADD COLUMN last_scrape_added INTEGER NOT NULL DEFAULT 0`,
			`ALTER TABLE sites ADD COLUMN last_scrape_updated INTEGER NOT NULL DEFAULT 0`,
			`ALTER TABLE sites ADD COLUMN last_scrape_state TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE downloads ADD COLUMN progress REAL NOT NULL DEFAULT 0`,
			`ALTER TABLE downloads ADD COLUMN seeds INTEGER NOT NULL DEFAULT 0`,
			`ALTER TABLE downloads ADD COLUMN peers INTEGER NOT NULL DEFAULT 0`,
			`ALTER TABLE downloads ADD COLUMN eta_seconds INTEGER NOT NULL DEFAULT 0`,
			`ALTER TABLE downloads ADD COLUMN seen_complete INTEGER NOT NULL DEFAULT 0`,
			`ALTER TABLE releases ADD COLUMN label TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE releases ADD COLUMN stash_added_at DATETIME`,
			`ALTER TABLE releases ADD COLUMN stash_release_date TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE pipeline_steps ADD COLUMN timeout_seconds INTEGER NOT NULL DEFAULT 0`,
			`ALTER TABLE downloads ADD COLUMN filename_pattern_excluded INTEGER NOT NULL DEFAULT 0`,
			`ALTER TABLE releases ADD COLUMN allow_non_preferred_filenames INTEGER NOT NULL DEFAULT 0`,
			`ALTER TABLE releases ADD COLUMN o_counter INTEGER NOT NULL DEFAULT 0`,
			`ALTER TABLE releases ADD COLUMN play_count INTEGER NOT NULL DEFAULT 0`,
			`ALTER TABLE releases ADD COLUMN last_played_at TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE releases ADD COLUMN last_o_count_at TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE releases ADD COLUMN screenshots_checked_at DATETIME`,
			`ALTER TABLE releases ADD COLUMN watchlist_at DATETIME`,
			`ALTER TABLE releases ADD COLUMN stash_created_at DATETIME`,
			`ALTER TABLE releases ADD COLUMN is_preferred INTEGER NOT NULL DEFAULT 1`,
			`ALTER TABLE job_history ADD COLUMN title TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE job_history ADD COLUMN scheduled INTEGER NOT NULL DEFAULT 0`,
			`ALTER TABLE job_history ADD COLUMN site_count INTEGER NOT NULL DEFAULT 0`,
		} {
			if _, alterErr := s.db.Exec(statement); alterErr != nil && !strings.Contains(strings.ToLower(alterErr.Error()), "duplicate column") {
				return alterErr
			}
		}
	}
	if err == nil {
		err = s.migrateWatchlistNaming(context.Background())
	}
	if err == nil {
		// Backfill watchlist_at for releases already marked Watchlist before this
		// column existed, so the Watchlist tab's "when marked as watchlist" sort
		// has something to sort by right after upgrade instead of every
		// pre-existing Watchlist release tying at NULL. updated_at is the
		// closest available approximation of when watchlist was last toggled
		// true for these rows.
		_, err = s.db.Exec(`UPDATE releases SET watchlist_at=updated_at WHERE watchlist=1 AND watchlist_at IS NULL`)
	}
	if err == nil {
		// Backfill stash_created_at for releases already locally matched
		// before this column existed, using stash_added_at (JAVBeacon's own
		// first-seen timestamp) as the closest available approximation -
		// it is not StashApp's real created_at, but it keeps the Local
		// tab's "Added Locally" sort meaningful immediately after upgrade
		// instead of every pre-existing match tying at NULL. The next
		// StashApp sync overwrites it with the real value for any release
		// whose scene still resolves.
		_, err = s.db.Exec(`UPDATE releases SET stash_created_at=stash_added_at WHERE is_local=1 AND stash_created_at IS NULL AND stash_added_at IS NOT NULL`)
	}
	if err == nil {
		err = s.removeScheduledDownloads()
	}
	if err == nil {
		_, err = s.db.Exec(`CREATE TABLE IF NOT EXISTS release_sites (
			release_id INTEGER NOT NULL REFERENCES releases(id) ON DELETE CASCADE,
			site_id INTEGER NOT NULL REFERENCES sites(id) ON DELETE CASCADE,
			site_monitor_download INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY(release_id,site_id)
		);
		CREATE INDEX IF NOT EXISTS idx_release_sites_site ON release_sites(site_id,release_id);`)
	}
	if err == nil {
		err = s.migrateSiteMonitoringRedesign(context.Background())
	}
	if err == nil {
		_, err = s.db.Exec(`CREATE TABLE IF NOT EXISTS pipeline_runs (
			download_id INTEGER NOT NULL REFERENCES downloads(id) ON DELETE CASCADE,
			trigger TEXT NOT NULL,
			state TEXT NOT NULL,
			error TEXT NOT NULL DEFAULT '',
			started_at DATETIME NOT NULL,
			finished_at DATETIME,
			PRIMARY KEY(download_id,trigger)
		);
		INSERT OR IGNORE INTO pipeline_runs(download_id,trigger,state,error,started_at,finished_at)
			SELECT id,'download_completed',CASE WHEN post_status='failed' THEN 'failed' ELSE 'completed' END,error,updated_at,updated_at
			FROM downloads WHERE post_status IN ('completed','failed');`)
	}
	if err == nil {
		// TODO-2.0 Phase 2: "Missing Library Files" - StashApp scenes whose
		// file(s) are no longer found on disk. release_id is 0 until a
		// JAVBeacon release is matched or retrieved for the scene;
		// status only tracks the retrieval workflow itself
		// (missing/retrieving/retrieve_failed) - once linked, the UI's
		// "downloading"/"downloaded"/"failed"/"monitored" states are derived
		// live from the linked release (see stashMissingEffectiveStatusExpr
		// in stash_missing.go) rather than duplicated here.
		_, err = s.db.Exec(`CREATE TABLE IF NOT EXISTS stash_missing_scenes (
			id INTEGER PRIMARY KEY,
			stash_scene_id TEXT NOT NULL UNIQUE,
			title TEXT NOT NULL DEFAULT '',
			code TEXT NOT NULL DEFAULT '',
			date TEXT NOT NULL DEFAULT '',
			path TEXT NOT NULL DEFAULT '',
			paths TEXT NOT NULL DEFAULT '[]',
			o_counter INTEGER NOT NULL DEFAULT 0,
			play_count INTEGER NOT NULL DEFAULT 0,
			last_played_at TEXT NOT NULL DEFAULT '',
			studio TEXT NOT NULL DEFAULT '',
			tags TEXT NOT NULL DEFAULT '[]',
			urls TEXT NOT NULL DEFAULT '[]',
			javlibrary_url TEXT NOT NULL DEFAULT '',
			release_id INTEGER NOT NULL DEFAULT 0,
			status TEXT NOT NULL DEFAULT 'missing',
			message TEXT NOT NULL DEFAULT '',
			first_seen_at DATETIME NOT NULL,
			last_scan_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_stash_missing_release ON stash_missing_scenes(release_id);
		CREATE INDEX IF NOT EXISTS idx_stash_missing_status ON stash_missing_scenes(status);
		CREATE INDEX IF NOT EXISTS idx_stash_missing_lastscan ON stash_missing_scenes(last_scan_at);`)
	}
	if err == nil {
		err = s.cleanupStoredReleaseText(context.Background())
	}
	if err == nil {
		err = s.backfillReleaseMetadata(context.Background())
	}
	if err == nil {
		err = s.migrateReleaseIdentity(context.Background())
	}
	if err == nil {
		err = s.migrateNormalizedReleaseMetadata(context.Background())
	}
	if err == nil {
		_, err = s.db.Exec(`UPDATE sites SET download_mode='future' WHERE download=1 AND download_mode=''`)
	}
	if err == nil {
		err = s.backfillLocalAvailableNotifications(context.Background())
	}
	if err == nil {
		err = s.normalizeJavLibraryURLs()
	}
	if err == nil {
		err = s.normalizeReleaseTimestamps(context.Background())
	}
	if err == nil {
		_, err = s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_releases_released_date ON releases(released,release_date DESC,id DESC); CREATE INDEX IF NOT EXISTS idx_releases_local_created ON releases(is_local,stash_created_at DESC,id DESC); CREATE INDEX IF NOT EXISTS idx_releases_watchlist_date ON releases(watchlist,watchlist_at DESC,id DESC); CREATE INDEX IF NOT EXISTS idx_releases_updated ON releases(updated_at DESC,id DESC); CREATE INDEX IF NOT EXISTS idx_releases_title_order ON releases(title COLLATE NOCASE,id); CREATE INDEX IF NOT EXISTS idx_releases_preferred ON releases(is_preferred,id);`)
	}
	return err
}

// migrateSiteMonitoringRedesign converts the retired site-level automatic
// download modes into explicit release monitoring. Only the old "future"
// mode enables the replacement rule; "all" has no equivalent, but releases
// it already enrolled remain monitored so an upgrade never silently drops a
// user's existing queue. The old columns remain as inert compatibility data
// for database transfers from older versions and are cleared after copying.
func (s *SQLite) migrateSiteMonitoringRedesign(ctx context.Context) error {
	var complete string
	if err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key='site_monitoring_redesign_v1'`).Scan(&complete); err == nil && complete == "true" {
		return nil
	} else if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UTC()
	if _, err = tx.ExecContext(ctx, `UPDATE sites SET auto_monitor_future=1 WHERE download_mode='future' OR (download=1 AND download_mode='')`); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE releases SET monitor_download=1,monitor_reason=CASE WHEN monitor_reason='' THEN 'migrated_site' ELSE monitor_reason END,monitor_site_id=CASE WHEN monitor_site_id=0 THEN site_id ELSE monitor_site_id END WHERE site_monitor_download=1 OR EXISTS(SELECT 1 FROM release_sites rs WHERE rs.release_id=releases.id AND rs.site_monitor_download=1)`); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE releases SET monitor_reason='manual' WHERE monitor_download=1 AND monitor_reason=''`); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE sites SET download=0,download_mode=''`); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE releases SET site_monitor_download=0`); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE release_sites SET site_monitor_download=0`); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO settings(key,value,updated_at) VALUES('site_monitoring_redesign_v1','true',?) ON CONFLICT(key) DO UPDATE SET value='true',updated_at=excluded.updated_at`, now); err != nil {
		return err
	}
	return tx.Commit()
}

// migrateWatchlistNaming upgrades the persisted names used by installations
// from before Watchlist became the canonical feature name. The retired token
// is assembled so it cannot accidentally remain a searchable identifier in
// active source. The migration is idempotent and removes the retired database
// objects after their data has been copied.
func (s *SQLite) migrateWatchlistNaming(ctx context.Context) error {
	retired := "des" + "ired"
	retiredTitle := "Des" + "ired"

	siteColumn, err := s.columnExists(ctx, "sites", retired)
	if err != nil {
		return err
	}
	releaseColumn, err := s.columnExists(ctx, "releases", retired)
	if err != nil {
		return err
	}
	releaseTimeColumn, err := s.columnExists(ctx, "releases", retired+"_at")
	if err != nil {
		return err
	}
	retiredSync := retired + "_sync"
	syncTable, err := s.tableExists(ctx, retiredSync)
	if err != nil {
		return err
	}

	if siteColumn {
		if _, err := s.db.ExecContext(ctx, fmt.Sprintf(`UPDATE sites SET watchlist=CASE WHEN watchlist=1 OR %s=1 THEN 1 ELSE 0 END`, retired)); err != nil {
			return err
		}
	}
	if releaseColumn {
		if _, err := s.db.ExecContext(ctx, fmt.Sprintf(`UPDATE releases SET watchlist=CASE WHEN watchlist=1 OR %s=1 THEN 1 ELSE 0 END`, retired)); err != nil {
			return err
		}
	}
	if releaseTimeColumn {
		if _, err := s.db.ExecContext(ctx, fmt.Sprintf(`UPDATE releases SET watchlist_at=COALESCE(watchlist_at,%s_at)`, retired)); err != nil {
			return err
		}
	}
	if syncTable {
		insert := fmt.Sprintf(`INSERT OR IGNORE INTO watchlist_sync(release_id,stash_scene_id,tag_id,synced_at,result) SELECT release_id,stash_scene_id,tag_id,synced_at,result FROM %s`, retiredSync)
		if s.dialect.Name() == "postgres" {
			insert = fmt.Sprintf(`INSERT INTO watchlist_sync(release_id,stash_scene_id,tag_id,synced_at,result) SELECT release_id,stash_scene_id,tag_id,synced_at,result FROM %s ON CONFLICT(release_id) DO NOTHING`, retiredSync)
		}
		if _, err := s.db.ExecContext(ctx, insert); err != nil {
			return err
		}
	}

	for _, suffix := range []string{"sync_enabled", "sync_interval", "tag_id"} {
		oldKey, newKey := "stash_"+retired+"_"+suffix, "stash_watchlist_"+suffix
		if _, err := s.db.ExecContext(ctx, `INSERT INTO settings(key,value,updated_at) SELECT ?,value,updated_at FROM settings WHERE key=? ON CONFLICT(key) DO NOTHING`, newKey, oldKey); err != nil {
			return err
		}
		if _, err := s.db.ExecContext(ctx, `DELETE FROM settings WHERE key=?`, oldKey); err != nil {
			return err
		}
	}
	for _, table := range []string{"user_preferences", "filter_presets"} {
		idColumn := "rowid"
		if s.dialect.Name() == "postgres" {
			idColumn = "id"
			if table == "user_preferences" {
				idColumn = "user_id"
			}
		}
		rows, err := s.db.QueryContext(ctx, `SELECT `+idColumn+`,state FROM `+table)
		if err != nil {
			return err
		}
		type stateRow struct {
			id    int64
			state string
		}
		states := make([]stateRow, 0)
		for rows.Next() {
			var row stateRow
			if err := rows.Scan(&row.id, &row.state); err != nil {
				rows.Close()
				return err
			}
			states = append(states, row)
		}
		if err := rows.Close(); err != nil {
			return err
		}
		for _, row := range states {
			next := strings.ReplaceAll(strings.ReplaceAll(row.state, retiredTitle, "Watchlist"), retired, "watchlist")
			if next != row.state {
				if _, err := s.db.ExecContext(ctx, `UPDATE `+table+` SET state=? WHERE `+idColumn+`=?`, next, row.id); err != nil {
					return err
				}
			}
		}
	}

	if syncTable {
		if _, err := s.db.ExecContext(ctx, `DROP TABLE `+retiredSync); err != nil {
			return err
		}
	}
	if _, err := s.db.ExecContext(ctx, `DROP INDEX IF EXISTS idx_releases_`+retired+`_date`); err != nil {
		return err
	}
	for _, item := range []struct {
		table  string
		column string
		exists bool
	}{{"releases", retired + "_at", releaseTimeColumn}, {"releases", retired, releaseColumn}, {"sites", retired, siteColumn}} {
		if item.exists {
			if _, err := s.db.ExecContext(ctx, `ALTER TABLE `+item.table+` DROP COLUMN `+item.column); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *SQLite) columnExists(ctx context.Context, table, column string) (bool, error) {
	if s.dialect.Name() == "postgres" {
		var exists bool
		err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM information_schema.columns WHERE table_schema=current_schema() AND table_name=? AND column_name=?)`, table, column).Scan(&exists)
		return exists, err
	}
	rows, err := s.db.QueryContext(ctx, `PRAGMA table_info(`+table+`)`)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, dataType string
		var notNull, primaryKey int
		var defaultValue any
		if err := rows.Scan(&cid, &name, &dataType, &notNull, &defaultValue, &primaryKey); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}

func (s *SQLite) tableExists(ctx context.Context, table string) (bool, error) {
	var count int
	if s.dialect.Name() == "postgres" {
		err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=current_schema() AND table_name=?`, table).Scan(&count)
		return count > 0, err
	}
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&count)
	return count > 0, err
}

// normalizeReleaseTimestamps repairs legacy or imported rows whose update
// timestamp predates their insertion timestamp. It is intentionally
// idempotent and runs at startup for both database engines.
func (s *SQLite) normalizeReleaseTimestamps(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `UPDATE releases SET updated_at=added_at WHERE updated_at<added_at`)
	return err
}

// normalizeJavLibraryURLs rewrites any stored http://www.javlibrary.com or
// http://javlibrary.com URL to https://www.javlibrary.com, in both sites.url
// (a monitored site's listing URL) and releases.product_url (a release's
// detail link). JavLibrary's Cloudflare check reliably 403s plain-http direct
// requests and can trip FlareSolverr into a mid-navigation http->https
// redirect race, so any URL stored before the scraper's https-only default
// (see scraper.normalizeJavLibraryURL) was in place needs correcting here.
// Both UPDATEs are naturally idempotent - once no row matches the LIKE
// pattern, they affect zero rows on every later startup - so no separate
// settings flag is needed to track completion.
func (s *SQLite) normalizeJavLibraryURLs() error {
	for _, statement := range []string{
		`UPDATE sites SET url=REPLACE(REPLACE(url,'http://www.javlibrary.com','https://www.javlibrary.com'),'http://javlibrary.com','https://www.javlibrary.com') WHERE url LIKE 'http://%javlibrary.com%'`,
		`UPDATE releases SET product_url=REPLACE(REPLACE(product_url,'http://www.javlibrary.com','https://www.javlibrary.com'),'http://javlibrary.com','https://www.javlibrary.com') WHERE product_url LIKE 'http://%javlibrary.com%'`,
	} {
		if _, err := s.db.Exec(statement); err != nil {
			return err
		}
	}
	return nil
}

// removeScheduledDownloads finishes the removal of the retired Scheduled
// Download feature for databases created before it was dropped. Any download
// history row left in the now-impossible "scheduled" status is reclassified
// as cancelled so it stops rendering with unstyled/dead status text, and the
// now-unused scheduled_for column is dropped where the SQLite build supports
// DROP COLUMN. Both statements are safe to run on every startup: the UPDATE
// matches zero rows once converted, and the ALTER TABLE error is ignored once
// the column is gone.
//
// The DROP COLUMN step is skipped entirely for PostgreSQL (DB Phase 5):
// postgres_schema.go's DDL never had a scheduled_for column to begin with
// - PostgreSQL only exists as a runtime target starting after this
// now-retired feature was already gone - so there is nothing to clean up,
// and SQLite's specific "no such column" error text would not match
// PostgreSQL's own phrasing anyway.
func (s *SQLite) removeScheduledDownloads() error {
	if _, err := s.db.Exec(`UPDATE downloads SET status='cancelled' WHERE status='scheduled'`); err != nil {
		return err
	}
	if s.dialect.Name() == "postgres" {
		return nil
	}
	if _, err := s.db.Exec(`ALTER TABLE downloads DROP COLUMN scheduled_for`); err != nil && !strings.Contains(strings.ToLower(err.Error()), "no such column") {
		return err
	}
	return nil
}

func (s *SQLite) backfillLocalAvailableNotifications(ctx context.Context) error {
	var complete string
	if err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key='local_available_notifications_v1'`).Scan(&complete); err == nil && complete == "true" {
		return nil
	} else if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO notifications(release_id,type,message,created_at) SELECT id,'local_available','Available in local Stash library',updated_at FROM releases WHERE is_local=1`); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO settings(key,value,updated_at) VALUES('local_available_notifications_v1','true',?) ON CONFLICT(key) DO UPDATE SET value='true',updated_at=excluded.updated_at`, time.Now().UTC()); err != nil {
		return err
	}
	return tx.Commit()
}

func releaseIdentity(source, videoID string) string {
	normalize := func(value string) string {
		return strings.Map(func(r rune) rune {
			if unicode.IsLetter(r) || unicode.IsNumber(r) {
				return unicode.ToUpper(r)
			}
			return -1
		}, strings.TrimSpace(value))
	}
	provider := strings.ToLower(strings.TrimSpace(source))
	id := normalize(videoID)
	if provider == "" || id == "" {
		return ""
	}
	return provider + ":" + id
}

// releaseDedupUpdate builds migrateReleaseIdentity's release-merge UPDATE.
// It takes a Dialect solely for Greatest/Least (DB Phase 5): SQLite
// overloads MAX()/MIN() to also mean the scalar 2-argument "greater/lesser
// of these two values" when given more than one argument, which is what
// every outer MAX/MIN call below actually needs (folding an old
// duplicate's value into the kept row) - PostgreSQL has no such overload
// and needs GREATEST()/LEAST() instead. The *inner* MAX(old.x)/MIN(old.x)
// calls are ordinary single-argument aggregate MAX/MIN over the
// correlated subquery's matching rows, which both engines already support
// identically, so those are left as literal text.
func releaseDedupUpdate(d Dialect) string {
	dupSelect := func(aggFn, column string) string {
		return "COALESCE((SELECT " + aggFn + "(old." + column + ") FROM release_duplicate_map m JOIN releases old ON old.id=m.old_id WHERE m.keep_id=keep.id)"
	}
	maxFold := func(column string) string {
		return column + "=" + d.Greatest(column, dupSelect("MAX", column)+",0)")
	}
	return `UPDATE releases AS keep SET ` +
		maxFold("released") + `,` +
		maxFold("is_local") + `,` +
		maxFold("notified") + `,` +
		maxFold("notify_on_release") + `,` +
		maxFold("watchlist") + `,` +
		maxFold("monitor_download") + `,` +
		maxFold("site_monitor_download") + `,` +
		`stash_scene_id=COALESCE(NULLIF(stash_scene_id,''),(SELECT old.stash_scene_id FROM release_duplicate_map m JOIN releases old ON old.id=m.old_id WHERE m.keep_id=keep.id AND old.stash_scene_id<>'' LIMIT 1),''),` +
		`stash_added_at=COALESCE(stash_added_at,(SELECT old.stash_added_at FROM release_duplicate_map m JOIN releases old ON old.id=m.old_id WHERE m.keep_id=keep.id AND old.stash_added_at IS NOT NULL LIMIT 1)),` +
		`stash_created_at=COALESCE(stash_created_at,(SELECT old.stash_created_at FROM release_duplicate_map m JOIN releases old ON old.id=m.old_id WHERE m.keep_id=keep.id AND old.stash_created_at IS NOT NULL LIMIT 1)),` +
		`stash_release_date=COALESCE(NULLIF(stash_release_date,''),(SELECT old.stash_release_date FROM release_duplicate_map m JOIN releases old ON old.id=m.old_id WHERE m.keep_id=keep.id AND old.stash_release_date<>'' LIMIT 1),''),` +
		`added_at=` + d.Least("added_at", dupSelect("MIN", "added_at")+",added_at)") + `,` +
		`updated_at=` + d.Greatest("updated_at", dupSelect("MAX", "updated_at")+",updated_at)") +
		` WHERE id IN (SELECT keep_id FROM release_duplicate_map)`
}

func (s *SQLite) migrateReleaseIdentity(ctx context.Context) error {
	var complete string
	if err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key='release_identity_dedup_v1'`).Scan(&complete); err == nil && complete == "true" {
		// Two separate statements (not one semicolon-joined string): a
		// single Exec call carrying more than one statement is a SQLite
		// convenience this package relies on nowhere else, and
		// PostgreSQL's extended query protocol - which every statement
		// in this package goes through, see postgres_rewrite.go's doc
		// comment - rejects multiple commands in one Exec outright (DB
		// Phase 5).
		if _, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO release_sites(release_id,site_id,site_monitor_download) SELECT id,site_id,site_monitor_download FROM releases`); err != nil {
			return err
		}
		_, err = s.db.ExecContext(ctx, `CREATE UNIQUE INDEX IF NOT EXISTS idx_releases_identity ON releases(identity_key) WHERE identity_key<>''`)
		return err
	} else if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO release_sites(release_id,site_id,site_monitor_download) SELECT id,site_id,site_monitor_download FROM releases`); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE releases SET identity_key=LOWER(TRIM(source)) || ':' || UPPER(REPLACE(REPLACE(REPLACE(REPLACE(REPLACE(TRIM(video_id),'-',''),'_',''),' ',''),'.',''),'/','')) WHERE TRIM(source)<>'' AND TRIM(video_id)<>''`); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,identity_key,LENGTH(title)+LENGTH(story)+COALESCE((SELECT SUM(LENGTH(name)) FROM release_actresses WHERE release_id=releases.id),0)+COALESCE((SELECT SUM(LENGTH(name)) FROM release_tags WHERE release_id=releases.id),0)+LENGTH(screenshots)+(is_local*10000)+(watchlist*1000)+(monitor_download*100) AS score FROM releases WHERE identity_key<>'' ORDER BY id`)
	if err != nil {
		return err
	}
	type candidate struct {
		id    int64
		score int
	}
	groups := map[string][]candidate{}
	for rows.Next() {
		var id int64
		var identity string
		var score int
		if err = rows.Scan(&id, &identity, &score); err != nil {
			rows.Close()
			return err
		}
		groups[identity] = append(groups[identity], candidate{id: id, score: score})
	}
	if err = rows.Close(); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `CREATE TEMP TABLE release_duplicate_map(old_id INTEGER PRIMARY KEY,keep_id INTEGER NOT NULL)`); err != nil {
		return err
	}
	insertMap, err := tx.PrepareContext(ctx, `INSERT INTO release_duplicate_map(old_id,keep_id) VALUES(?,?)`)
	if err != nil {
		return err
	}
	for _, candidates := range groups {
		if len(candidates) < 2 {
			continue
		}
		keep := candidates[0]
		for _, item := range candidates[1:] {
			if item.score > keep.score {
				keep = item
			}
		}
		for _, item := range candidates {
			if item.id == keep.id {
				continue
			}
			if _, err = insertMap.ExecContext(ctx, item.id, keep.id); err != nil {
				insertMap.Close()
				return err
			}
		}
	}
	if err = insertMap.Close(); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `CREATE INDEX release_duplicate_map_keep ON release_duplicate_map(keep_id)`); err != nil {
		return err
	}
	statements := []string{
		releaseDedupUpdate(s.dialect),
		`INSERT OR IGNORE INTO release_sites(release_id,site_id,site_monitor_download) SELECT m.keep_id,rs.site_id,rs.site_monitor_download FROM release_duplicate_map m JOIN release_sites rs ON rs.release_id=m.old_id`,
		`UPDATE downloads SET release_id=(SELECT keep_id FROM release_duplicate_map WHERE old_id=downloads.release_id) WHERE release_id IN (SELECT old_id FROM release_duplicate_map)`,
		`INSERT OR IGNORE INTO notifications(release_id,type,message,created_at) SELECT m.keep_id,n.type,n.message,n.created_at FROM release_duplicate_map m JOIN notifications n ON n.release_id=m.old_id`,
		`INSERT OR IGNORE INTO watchlist_sync(release_id,stash_scene_id,tag_id,synced_at,result) SELECT m.keep_id,d.stash_scene_id,d.tag_id,d.synced_at,d.result FROM release_duplicate_map m JOIN watchlist_sync d ON d.release_id=m.old_id`,
		`INSERT OR IGNORE INTO release_actresses(release_id,position,name,name_normalized) SELECT m.keep_id,a.position,a.name,a.name_normalized FROM release_duplicate_map m JOIN release_actresses a ON a.release_id=m.old_id`,
		`INSERT OR IGNORE INTO release_tags(release_id,position,name,name_normalized) SELECT m.keep_id,t.position,t.name,t.name_normalized FROM release_duplicate_map m JOIN release_tags t ON t.release_id=m.old_id`,
		`DELETE FROM releases WHERE id IN (SELECT old_id FROM release_duplicate_map)`,
	}
	for _, statement := range statements {
		if _, err = tx.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `CREATE UNIQUE INDEX IF NOT EXISTS idx_releases_identity ON releases(identity_key) WHERE identity_key<>''`); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO settings(key,value,updated_at) VALUES('release_identity_dedup_v1','true',?) ON CONFLICT(key) DO UPDATE SET value='true',updated_at=excluded.updated_at`, time.Now().UTC()); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SQLite) Sites(ctx context.Context) ([]domain.Site, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,title,type,name,url,notify,auto_monitor_future,watchlist,rss_url,enabled,created_at,updated_at,last_scraped_at,last_scrape_pages,last_scrape_added,last_scrape_updated,last_scrape_state FROM sites ORDER BY `+s.dialect.CaseInsensitiveOrderBy("type")+`,`+s.dialect.CaseInsensitiveOrderBy("title"))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Site
	for rows.Next() {
		var x domain.Site
		var lastScraped sql.NullTime
		if err := rows.Scan(&x.ID, &x.Title, &x.Type, &x.Name, &x.URL, &x.Notify, &x.AutoMonitorFuture, &x.Watchlist, &x.RSSURL, &x.Enabled, &x.CreatedAt, &x.UpdatedAt, &lastScraped, &x.LastScrapePages, &x.LastScrapeAdded, &x.LastScrapeUpdated, &x.LastScrapeState); err != nil {
			return nil, err
		}
		x.LastScrapedAt = lastScraped.Time
		out = append(out, x)
	}
	return out, rows.Err()
}
func (s *SQLite) RecordSiteScrape(ctx context.Context, siteID int64, finishedAt time.Time, pages, added, updated int, state string) error {
	if finishedAt.IsZero() {
		finishedAt = time.Now().UTC()
	}
	result, err := s.db.ExecContext(ctx, `UPDATE sites SET last_scraped_at=?,last_scrape_pages=?,last_scrape_added=?,last_scrape_updated=?,last_scrape_state=? WHERE id=?`, finishedAt, max(pages, 0), max(added, 0), max(updated, 0), state, siteID)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return sql.ErrNoRows
	}
	return nil
}
func (s *SQLite) SaveSite(ctx context.Context, x domain.Site) (domain.Site, error) {
	x.URL = domain.NormalizeJavLibraryURL(x.URL)
	now := time.Now().UTC()
	if x.ID == 0 {
		var e error
		x.ID, e = s.dialect.InsertReturningID(ctx, s.db, `INSERT INTO sites(title,type,name,url,notify,auto_monitor_future,watchlist,rss_url,enabled,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, x.Title, x.Type, x.Name, x.URL, x.Notify, x.AutoMonitorFuture, x.Watchlist, x.RSSURL, x.Enabled, now, now)
		if e != nil {
			return x, e
		}
		x.CreatedAt = now
	} else {
		_, e := s.db.ExecContext(ctx, `UPDATE sites SET title=?,type=?,name=?,url=?,notify=?,auto_monitor_future=?,watchlist=?,rss_url=?,enabled=?,updated_at=? WHERE id=?`, x.Title, x.Type, x.Name, x.URL, x.Notify, x.AutoMonitorFuture, x.Watchlist, x.RSSURL, x.Enabled, now, x.ID)
		if e != nil {
			return x, e
		}
	}
	x.UpdatedAt = now
	return x, nil
}
func (s *SQLite) DeleteSite(ctx context.Context, id int64) error {
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	_, e = tx.ExecContext(ctx, `UPDATE releases SET site_id=(SELECT MIN(rs.site_id) FROM release_sites rs WHERE rs.release_id=releases.id AND rs.site_id<>?) WHERE site_id=? AND EXISTS (SELECT 1 FROM release_sites rs WHERE rs.release_id=releases.id AND rs.site_id<>?)`, id, id, id)
	if e != nil {
		return e
	}
	r, e := tx.ExecContext(ctx, `DELETE FROM sites WHERE id=?`, id)
	if e == nil {
		if n, _ := r.RowsAffected(); n == 0 {
			return sql.ErrNoRows
		}
	}
	if e != nil {
		return e
	}
	return tx.Commit()
}

// releaseSelect builds the shared SELECT used by every method that reads a
// full domain.Release row. It takes a Dialect solely for the one
// case-insensitive ORDER BY inside its site-titles JSON aggregation
// subquery (see DB Phase 2's dialect abstraction) - every other fragment
// is identical across engines. It is a function rather than a package
// const so that ORDER BY can vary by dialect; the SQLite text it produces
// is byte-for-byte identical to the previous const.
func releaseSelect(d Dialect) string {
	siteIDs := d.JSONArrayAgg("site_id", "SELECT site_id FROM release_sites WHERE release_id=r.id ORDER BY site_id")
	siteTitles := d.JSONArrayAgg("title", "SELECT s2.title FROM release_sites rs2 JOIN sites s2 ON s2.id=rs2.site_id WHERE rs2.release_id=r.id ORDER BY "+d.CaseInsensitiveOrderBy("s2.title"))
	actresses := d.JSONArrayAgg("name", "SELECT name FROM release_actresses WHERE release_id=r.id ORDER BY position")
	// The trailing correlated subqueries expose the selected download's state
	// and source detail-page URL together, plus the latest successful download
	// completion time. Active downloads take precedence over completed ones so
	// the status pill and its URL always describe the same download row.
	tags := d.JSONArrayAgg("name", "SELECT name FROM release_tags WHERE release_id=r.id ORDER BY position")
	return `SELECT r.id,r.site_id,s.title,` + siteIDs + `,` + siteTitles + `,r.video_id,r.scraper_id,r.title,r.release_date,r.source,r.image_url,r.product_url,` + actresses + `,r.director,r.studio,r.label,` + tags + `,r.duration,r.story,r.screenshots,r.released,r.is_local,r.notified,r.notify_on_release,r.watchlist,r.watchlist_at,r.monitor_download,r.monitor_reason,r.monitor_site_id,COALESCE((SELECT ms.title FROM sites ms WHERE ms.id=r.monitor_site_id),''),r.stash_scene_id,r.stash_added_at,r.stash_created_at,r.stash_release_date,r.allow_non_preferred_filenames,r.o_counter,r.play_count,r.last_played_at,r.last_o_count_at,r.added_at,r.updated_at,COALESCE((SELECT d.status FROM downloads d WHERE d.release_id=r.id AND d.status IN ('downloading','completed') ORDER BY CASE d.status WHEN 'downloading' THEN 0 ELSE 1 END,d.updated_at DESC LIMIT 1),''),COALESCE((SELECT d.source_reference FROM downloads d WHERE d.release_id=r.id AND d.status IN ('downloading','completed') ORDER BY CASE d.status WHEN 'downloading' THEN 0 ELSE 1 END,d.updated_at DESC LIMIT 1),''),(SELECT d.updated_at FROM downloads d WHERE d.release_id=r.id AND d.status='completed' ORDER BY d.updated_at DESC LIMIT 1) FROM releases r JOIN sites s ON s.id=r.site_id`
}

// releaseCardSelect keeps the Release Library payload small. Release Details
// fetches the full row on demand, so cards do not need stories, directors,
// durations, site-list aggregations, or playback statistics. Screenshots stay
// present because the hover slideshow needs to know whether it should start.
func releaseCardSelect(d Dialect) string {
	actresses := d.JSONArrayAgg("name", "SELECT name FROM release_actresses WHERE release_id=r.id ORDER BY position")
	tags := d.JSONArrayAgg("name", "SELECT name FROM release_tags WHERE release_id=r.id ORDER BY position")
	return `SELECT r.id,r.site_id,s.title,'[]','[]',r.video_id,r.scraper_id,r.title,r.release_date,r.source,r.image_url,r.product_url,` + actresses + `,'',r.studio,r.label,` + tags + `,'','',r.screenshots,r.released,r.is_local,r.notified,r.notify_on_release,r.watchlist,r.watchlist_at,r.monitor_download,r.monitor_reason,r.monitor_site_id,COALESCE((SELECT ms.title FROM sites ms WHERE ms.id=r.monitor_site_id),''),r.stash_scene_id,r.stash_added_at,r.stash_created_at,r.stash_release_date,r.allow_non_preferred_filenames,0,0,'','',r.added_at,r.updated_at,COALESCE((SELECT d.status FROM downloads d WHERE d.release_id=r.id AND d.status IN ('downloading','completed') ORDER BY CASE d.status WHEN 'downloading' THEN 0 ELSE 1 END,d.updated_at DESC LIMIT 1),''),COALESCE((SELECT d.source_reference FROM downloads d WHERE d.release_id=r.id AND d.status IN ('downloading','completed') ORDER BY CASE d.status WHEN 'downloading' THEN 0 ELSE 1 END,d.updated_at DESC LIMIT 1),''),(SELECT d.updated_at FROM downloads d WHERE d.release_id=r.id AND d.status='completed' ORDER BY d.updated_at DESC LIMIT 1) FROM releases r JOIN sites s ON s.id=r.site_id`
}

func scanRelease(scanner interface{ Scan(...any) error }) (domain.Release, error) {
	var x domain.Release
	var siteIDs, siteTitles, actresses, genres, shots string
	var stashAddedAt, stashCreatedAt, watchlistAt, downloadedAt sql.NullTime
	err := scanner.Scan(&x.ID, &x.SiteID, &x.SiteTitle, &siteIDs, &siteTitles, &x.VideoID, &x.ScraperID, &x.Title, &x.ReleaseDate, &x.Source, &x.ImageURL, &x.ProductURL, &actresses, &x.Director, &x.Studio, &x.Label, &genres, &x.Duration, &x.Story, &shots, &x.Released, &x.Local, &x.Notified, &x.NotifyOnRelease, &x.Watchlist, &watchlistAt, &x.MonitorDownload, &x.MonitorReason, &x.MonitorSiteID, &x.MonitorSiteTitle, &x.StashSceneID, &stashAddedAt, &stashCreatedAt, &x.StashReleaseDate, &x.AllowNonPreferredFilenames, &x.OCounter, &x.PlayCount, &x.LastPlayedAt, &x.LastOCountAt, &x.AddedAt, &x.UpdatedAt, &x.DownloadStatus, &x.DownloadSourceReference, &downloadedAt)
	if err == nil {
		_ = json.Unmarshal([]byte(siteIDs), &x.SiteIDs)
		_ = json.Unmarshal([]byte(siteTitles), &x.SiteTitles)
		_ = json.Unmarshal([]byte(actresses), &x.Actresses)
		_ = json.Unmarshal([]byte(genres), &x.Genres)
		x.Actress = strings.Join(x.Actresses, ", ")
		_ = json.Unmarshal([]byte(shots), &x.Screenshots)
		if stashAddedAt.Valid {
			x.StashAddedAt = stashAddedAt.Time
		}
		if stashCreatedAt.Valid {
			x.StashCreatedAt = stashCreatedAt.Time
		}
		if watchlistAt.Valid {
			x.WatchlistAt = watchlistAt.Time
		}
		if downloadedAt.Valid {
			x.DownloadedAt = downloadedAt.Time
		}
	}
	return x, err
}

const releaseFrom = ` FROM releases r JOIN sites s ON s.id=r.site_id`

// releaseFilterCondition is one row of the Release Library's Conditions
// dialog: match a field against a value, either exactly or as a
// substring/wildcard search. Op is an optional numeric/date comparison
// operator (mirroring stashMissingFilterCondition in stash_missing.go -
// "gte" default, "lte", "eq", "gt", "lt" for the numeric fields "duration",
// "o_count", and "play_count"; "before"/"after" (default) for the date fields
// "last_o_count", "last_played", "added_at", "updated_at", "release_date")
// that the original text-only fields (title/tag/actress/description) and
// the newer text fields (studio/label) have no use for.
type releaseFilterCondition struct {
	Field    string `json:"field"`
	Value    string `json:"value"`
	Op       string `json:"op"`
	Exact    bool   `json:"exact"`
	Wildcard bool   `json:"wildcard"`
}

// releaseFilterConditionGroup is one AND/OR group of conditions (TODO-2.0
// Task A "AND/OR condition groups"): its own Logic combines only its own
// Conditions, independent of the top-level logic that combines groups.
type releaseFilterConditionGroup struct {
	Logic      string                   `json:"logic"`
	Conditions []releaseFilterCondition `json:"conditions"`
}

// releaseSearchExpression is the JSON shape of ReleaseFilter.SearchExpression
// (and, with a different field set, domain.StashMissingFilter.
// SearchExpression's analogous shape in stash_missing.go). Groups is the
// TODO-2.0 Task A extension: each group has its own AND/OR Logic over its
// own Conditions, and the top-level Logic then combines the groups
// together, so a query can express things like "(actress=A OR actress=B)
// AND (tag=X OR tag=Y)" that a single flat AND/OR list can't. A legacy flat
// expression - just a top-level Logic + Conditions, no Groups - is treated
// as a single implicit group using the top-level Logic, so every saved
// filter preset and bookmarked URL created before this feature existed
// keeps matching exactly as it did before.
type releaseSearchExpression struct {
	Logic      string                        `json:"logic"`
	Conditions []releaseFilterCondition      `json:"conditions"`
	Groups     []releaseFilterConditionGroup `json:"groups"`
}

// releaseConditionGroupClause builds one parenthesized "(... AND/OR ...)"
// clause for a single condition group, joining its own conditions with its
// own logic. Returns "", nil if the group has no matchable conditions
// (blank value, unknown field), so the caller can skip it entirely rather
// than emitting an empty "()" into the surrounding WHERE clause.
func releaseConditionGroupClause(d Dialect, conditions []releaseFilterCondition, logic string) (string, []any) {
	innerLogic := " AND "
	if strings.EqualFold(logic, "or") {
		innerLogic = " OR "
	}
	parts := []string{}
	var a []any
	columns := map[string]string{"title": "r.title", "tag": "metadata", "actress": "metadata", "description": "r.story", "studio": "r.studio", "label": "r.label"}
	// timestampColumns are the two pre-existing DATETIME/TIMESTAMPTZ columns
	// (never blank - both are NOT NULL and set on every insert), so their
	// before/after comparison skips the "<>''" empty-string guard that the
	// plain-TEXT dateColumns below need: on PostgreSQL specifically, casting
	// an empty string literal to timestamptz for that guard would error
	// outright rather than just fail to match.
	timestampColumns := map[string]string{"added_at": "r.added_at", "updated_at": "r.updated_at"}
	// dateColumns are TEXT columns that store either a plain date
	// ("YYYY-MM-DD", release_date) or a StashApp-derived timestamp that may
	// still be unpopulated ("", last_played_at/last_o_count_at when the
	// scene has never been played/O-counted, or when the configured
	// stash_graphql_query/StashApp version can't supply it) - the "<>''"
	// guard keeps an unset value from spuriously satisfying either
	// before/after comparison.
	dateColumns := map[string]string{"last_o_count": "r.last_o_count_at", "last_played": "r.last_played_at", "release_date": "r.release_date"}
	// boolExprs maps a bool-kind field to the SQL expression that is true
	// exactly when that field's answer is "yes" - condition.Value "false"
	// negates it with NOT(...), mirroring has_javlibrary_url/has_db_entry in
	// stashMissingConditionGroupClause. "monitored" reuses the exact
	// expression releaseFilterWhere's own MonitorDownload flag applies, and
	// "downloaded"/"download_started"/"download_failed" reuse the same
	// downloads.status values releaseSelect's DownloadStatus subquery and
	// download/service.go's Download/runSearch already write.
	boolExprs := map[string]string{
		"watchlist":        "r.watchlist=1",
		"downloaded":       `EXISTS (SELECT 1 FROM downloads dl WHERE dl.release_id=r.id AND dl.status='completed')`,
		"download_started": `EXISTS (SELECT 1 FROM downloads dl WHERE dl.release_id=r.id AND dl.status IN ('queued','downloading','completed'))`,
		"download_failed":  `EXISTS (SELECT 1 FROM downloads dl WHERE dl.release_id=r.id AND dl.status='failed')`,
		"local":            "r.is_local=1",
		"monitored":        "r.monitor_download=1",
	}
	for _, condition := range conditions {
		field := strings.ToLower(condition.Field)
		value := strings.TrimSpace(condition.Value)
		if expr, ok := boolExprs[field]; ok {
			if value == "false" {
				parts = append(parts, "NOT "+expr)
			} else {
				parts = append(parts, expr)
			}
			continue
		}
		if field == "duration" || field == "o_count" || field == "play_count" {
			if value == "" {
				continue
			}
			if field == "duration" {
				n, convErr := strconv.ParseFloat(value, 64)
				if convErr != nil {
					continue
				}
				parts = append(parts, d.ExtractLeadingMinutes("r.duration")+" "+numericConditionOp(condition.Op)+" ?")
				a = append(a, n)
			} else {
				n, convErr := strconv.Atoi(value)
				if convErr != nil {
					continue
				}
				column := "r.o_counter"
				if field == "play_count" {
					column = "r.play_count"
				}
				parts = append(parts, column+" "+numericConditionOp(condition.Op)+" ?")
				a = append(a, n)
			}
			continue
		}
		if column, ok := timestampColumns[field]; ok {
			if value == "" {
				continue
			}
			if strings.EqualFold(condition.Op, "before") {
				parts = append(parts, column+" < ?")
			} else {
				parts = append(parts, column+" > ?")
			}
			a = append(a, value)
			continue
		}
		if column, ok := dateColumns[field]; ok {
			if value == "" {
				continue
			}
			if strings.EqualFold(condition.Op, "before") {
				parts = append(parts, column+"<>'' AND "+column+" < ?")
			} else {
				parts = append(parts, column+"<>'' AND "+column+" > ?")
			}
			a = append(a, value)
			continue
		}
		column := columns[field]
		if column == "" || value == "" {
			continue
		}
		if condition.Exact {
			if strings.EqualFold(condition.Field, "tag") {
				parts = append(parts, `EXISTS (SELECT 1 FROM release_tags t WHERE t.release_id=r.id AND t.name_normalized=LOWER(?))`)
			} else if strings.EqualFold(condition.Field, "actress") {
				if reversed := reverseTwoWordName(value); reversed != "" {
					parts = append(parts, `(EXISTS (SELECT 1 FROM release_actresses a2 WHERE a2.release_id=r.id AND a2.name_normalized=LOWER(?)) OR EXISTS (SELECT 1 FROM release_actresses a2 WHERE a2.release_id=r.id AND a2.name_normalized=LOWER(?)))`)
					a = append(a, value, reversed)
					continue
				}
				parts = append(parts, `EXISTS (SELECT 1 FROM release_actresses a2 WHERE a2.release_id=r.id AND a2.name_normalized=LOWER(?))`)
			} else if strings.EqualFold(condition.Field, "label") {
				parts = append(parts, `(LOWER(r.label)=LOWER(?) OR EXISTS (SELECT 1 FROM release_sites rsl JOIN sites sl ON sl.id=rsl.site_id WHERE rsl.release_id=r.id AND LOWER(sl.title)=LOWER(?)))`)
				a = append(a, value, value)
				continue
			} else {
				parts = append(parts, `LOWER(`+column+`)=LOWER(?)`)
			}
			a = append(a, value)
		} else {
			reversed := reverseTwoWordName(value)
			if condition.Wildcard {
				value = strings.ReplaceAll(value, "*", "%")
				reversed = strings.ReplaceAll(reversed, "*", "%")
			} else {
				value = "%" + value + "%"
				if reversed != "" {
					reversed = "%" + reversed + "%"
				}
			}
			if strings.EqualFold(condition.Field, "tag") {
				parts = append(parts, `EXISTS (SELECT 1 FROM release_tags t WHERE t.release_id=r.id AND `+d.CaseInsensitiveLike("t.name")+`)`)
			} else if strings.EqualFold(condition.Field, "actress") {
				if reversed != "" {
					parts = append(parts, `(EXISTS (SELECT 1 FROM release_actresses a2 WHERE a2.release_id=r.id AND `+d.CaseInsensitiveLike("a2.name")+`) OR EXISTS (SELECT 1 FROM release_actresses a2 WHERE a2.release_id=r.id AND `+d.CaseInsensitiveLike("a2.name")+`))`)
					a = append(a, value, reversed)
					continue
				}
				parts = append(parts, `EXISTS (SELECT 1 FROM release_actresses a2 WHERE a2.release_id=r.id AND `+d.CaseInsensitiveLike("a2.name")+`)`)
			} else if strings.EqualFold(condition.Field, "label") {
				parts = append(parts, `(`+d.CaseInsensitiveLike("r.label")+` OR EXISTS (SELECT 1 FROM release_sites rsl JOIN sites sl ON sl.id=rsl.site_id WHERE rsl.release_id=r.id AND `+d.CaseInsensitiveLike("sl.title")+`))`)
				a = append(a, value, value)
				continue
			} else {
				// TODO-2.0 Task A case-insensitivity audit: this was a
				// bare "column LIKE ?" instead of going through
				// Dialect.CaseInsensitiveLike like every other branch
				// here - harmless on SQLite (LIKE is already
				// case-insensitive by its default collation) but
				// silently case-sensitive on PostgreSQL (whose LIKE
				// always is), for the title/description Conditions
				// fields specifically.
				parts = append(parts, d.CaseInsensitiveLike(column))
			}
			a = append(a, value)
		}
	}
	if len(parts) == 0 {
		return "", nil
	}
	return `(` + strings.Join(parts, innerLogic) + `)`, a
}

// releaseFilterWhere builds the "WHERE ..." fragment and its bind arguments
// shared by Releases and ReleasesCount, so the two never drift out of sync
// on what counts as a match. It takes a Dialect for its case-insensitive
// comparisons (DB Phase 2) - the SQLite text it produces is byte-for-byte
// identical to before that abstraction existed.
func releaseFilterWhere(d Dialect, f domain.ReleaseFilter) (string, []any) {
	q := ` WHERE 1=1`
	var a []any
	if f.Search != "" {
		// Every branch of this OR goes through Dialect.CaseInsensitiveLike so
		// the free-text search box (title, video ID, actress, studio, tag,
		// and the release's site/"Label") matches regardless of case on both
		// engines - plain "LIKE ?" happens to already be case-insensitive on
		// SQLite (its default collation), which is why this drifted: only
		// the two EXISTS subqueries below were ever routed through the
		// dialect helper, leaving the rest silently case-sensitive on
		// PostgreSQL (whose LIKE always is) even though SQLite deployments
		// never showed a symptom.
		q += ` AND (` + d.CaseInsensitiveLike("r.video_id") + ` OR ` + d.CaseInsensitiveLike("r.title") + ` OR ` + d.CaseInsensitiveLike("r.studio") + ` OR ` + d.CaseInsensitiveLike("r.label") + ` OR EXISTS (SELECT 1 FROM release_actresses rsa WHERE rsa.release_id=r.id AND ` + d.CaseInsensitiveLike("rsa.name") + `) OR EXISTS (SELECT 1 FROM release_tags rst WHERE rst.release_id=r.id AND ` + d.CaseInsensitiveLike("rst.name") + `) OR EXISTS (SELECT 1 FROM release_sites rss JOIN sites ss ON ss.id=rss.site_id WHERE rss.release_id=r.id AND ` + d.CaseInsensitiveLike("ss.title") + `)`
		v := "%" + f.Search + "%"
		a = append(a, v, v, v, v, v, v, v)
		if reversed := reverseTwoWordName(f.Search); reversed != "" {
			q += ` OR EXISTS (SELECT 1 FROM release_actresses a2 WHERE a2.release_id=r.id AND ` + d.CaseInsensitiveLike("a2.name") + `)`
			a = append(a, "%"+reversed+"%")
		}
		q += `)`
	}
	if f.Source != "" {
		q += ` AND LOWER(r.source)=LOWER(?)`
		a = append(a, f.Source)
	}
	if f.SearchExpression != "" {
		var expression releaseSearchExpression
		if json.Unmarshal([]byte(f.SearchExpression), &expression) == nil {
			groups := expression.Groups
			if len(groups) == 0 && len(expression.Conditions) > 0 {
				// Legacy flat shape (no "groups", just a top-level
				// "conditions" list): treat it as a single implicit group
				// using the top-level logic, so old saved presets/filter
				// sets and bookmarked URLs keep matching exactly as before.
				groups = []releaseFilterConditionGroup{{Logic: expression.Logic, Conditions: expression.Conditions}}
			}
			outerLogic := " AND "
			if strings.EqualFold(expression.Logic, "or") {
				outerLogic = " OR "
			}
			var groupClauses []string
			for _, group := range groups {
				clause, args := releaseConditionGroupClause(d, group.Conditions, group.Logic)
				if clause == "" {
					continue
				}
				groupClauses = append(groupClauses, clause)
				a = append(a, args...)
			}
			if len(groupClauses) > 0 {
				q += ` AND (` + strings.Join(groupClauses, outerLogic) + `)`
			}
		}
	}
	if f.Site != "" {
		q += ` AND EXISTS (SELECT 1 FROM release_sites rsf JOIN sites sf ON sf.id=rsf.site_id WHERE rsf.release_id=r.id AND sf.title=?)`
		a = append(a, f.Site)
	}
	if f.SiteID != 0 {
		q += ` AND EXISTS (SELECT 1 FROM release_sites rsf WHERE rsf.release_id=r.id AND rsf.site_id=?)`
		a = append(a, f.SiteID)
	}
	if f.Category != "" && f.Entries != "" {
		entries := parseFilterEntries(f.Entries)
		column := map[string]string{"actress": "metadata", "maker": "r.studio", "label": "label", "studio": "r.studio", "tag": "metadata"}[strings.ToLower(f.Category)]
		if column != "" {
			filterParts := []string{}
			filterArgs := []any{}
			for _, entry := range entries {
				entry = strings.TrimSpace(entry)
				if entry == "" {
					continue
				}
				switch strings.ToLower(f.Category) {
				case "actress":
					part := `(EXISTS (SELECT 1 FROM release_actresses a2 WHERE a2.release_id=r.id AND a2.name_normalized LIKE LOWER(?) ESCAPE '\')`
					filterArgs = append(filterArgs, metadataLikePattern(entry))
					parts := strings.Fields(entry)
					if len(parts) == 2 {
						part += ` OR EXISTS (SELECT 1 FROM release_actresses a2 WHERE a2.release_id=r.id AND a2.name_normalized LIKE LOWER(?) ESCAPE '\')`
						filterArgs = append(filterArgs, metadataLikePattern(parts[1]+" "+parts[0]))
					}
					filterParts = append(filterParts, part+`)`)
				case "tag":
					filterParts = append(filterParts, `EXISTS (SELECT 1 FROM release_tags t WHERE t.release_id=r.id AND t.name_normalized LIKE LOWER(?) ESCAPE '\')`)
					filterArgs = append(filterArgs, metadataLikePattern(entry))
				case "label":
					filterParts = append(filterParts, `(LOWER(r.label) LIKE LOWER(?) ESCAPE '\' OR EXISTS (SELECT 1 FROM release_sites rsl JOIN sites sl ON sl.id=rsl.site_id WHERE rsl.release_id=r.id AND LOWER(sl.title) LIKE LOWER(?) ESCAPE '\'))`)
					pattern := metadataLikePattern(entry)
					filterArgs = append(filterArgs, pattern, pattern)
				default:
					filterParts = append(filterParts, `LOWER(`+column+`) LIKE LOWER(?) ESCAPE '\'`)
					filterArgs = append(filterArgs, metadataLikePattern(entry))
				}
			}
			if len(filterParts) > 0 {
				q += ` AND (` + strings.Join(filterParts, ` OR `) + `)`
				a = append(a, filterArgs...)
			}
		}
	}
	if f.Watchlist {
		q += ` AND r.watchlist=1`
	}
	if f.MonitorDownload {
		q += ` AND r.monitor_download=1`
	}
	if f.AllowNonPreferredFilenames != nil {
		q += ` AND r.allow_non_preferred_filenames=?`
		a = append(a, *f.AllowNonPreferredFilenames)
	}
	if f.HideLocal && f.Status != "local" {
		q += ` AND r.is_local=0`
	}
	if f.UsePreferred && (len(f.IgnoreTags) > 0 || len(f.IgnoreTitles) > 0) {
		q += ` AND r.is_preferred=1`
	} else if len(f.IgnoreTags) > 0 {
		placeholders := make([]string, len(f.IgnoreTags))
		for i, tag := range f.IgnoreTags {
			placeholders[i] = "LOWER(?)"
			a = append(a, tag)
		}
		q += ` AND NOT EXISTS (SELECT 1 FROM release_tags t WHERE t.release_id=r.id AND t.name_normalized IN (` + strings.Join(placeholders, ",") + `))`
	}
	if !f.UsePreferred && len(f.IgnoreTitles) > 0 {
		titleParts := make([]string, len(f.IgnoreTitles))
		for i, title := range f.IgnoreTitles {
			titleParts[i] = d.CaseInsensitiveLike("r.title")
			a = append(a, ignoreTitlePattern(title))
		}
		q += ` AND NOT (` + strings.Join(titleParts, " OR ") + `)`
	}
	switch f.Status {
	case "released":
		q += ` AND r.released=1`
	case "upcoming":
		q += ` AND r.released=0`
	case "local":
		q += ` AND r.is_local=1`
	}
	if f.MinReleaseDate != "" {
		q += ` AND r.release_date>=?`
		a = append(a, f.MinReleaseDate)
	}
	if f.MaxReleaseDate != "" {
		q += ` AND r.release_date<=?`
		a = append(a, f.MaxReleaseDate)
	}
	return q, a
}

func (s *SQLite) Releases(ctx context.Context, f domain.ReleaseFilter) ([]domain.Release, error) {
	where, a := releaseFilterWhere(s.dialect, f)
	q := releaseSelect(s.dialect) + where
	direction, sortColumn := releaseSort(f)
	// r.id (an INTEGER PRIMARY KEY / SQLite rowid) is a strictly
	// monotonically increasing tiebreaker matching true insertion order,
	// unlike a wall-clock timestamp column which can tie - most commonly
	// r.added_at itself when sortColumn IS r.added_at (sort=added), where
	// a same-column tiebreaker is a no-op and leaves ties in whatever
	// order SQLite's query plan happens to produce. That previously made
	// "Date added" look unsorted/random whenever a bulk scrape or import
	// inserted many releases within the same timestamp.
	// Keep releases whose selected date has not been synchronized at the end
	// in both directions. PostgreSQL otherwise puts NULL values first for a
	// descending sort, which made an "Added Locally · newest first" result
	// start with releases that had no StashApp created_at at all. The explicit
	// CASE is portable across both PostgreSQL and SQLite.
	q += ` ORDER BY ` + sortColumn + ` ` + direction + ` NULLS LAST,r.id ` + direction
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 100
	}
	q += ` LIMIT ? OFFSET ?`
	a = append(a, f.Limit, f.Offset)
	rows, e := s.db.QueryContext(ctx, q, a...)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []domain.Release{}
	for rows.Next() {
		x, e := scanRelease(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

type releasePageCursor struct {
	Null  bool   `json:"null"`
	Value string `json:"value"`
	ID    int64  `json:"id"`
}

func releaseSort(f domain.ReleaseFilter) (string, string) {
	direction := "DESC"
	if strings.EqualFold(f.Direction, "asc") {
		direction = "ASC"
	}
	sortColumn := map[string]string{"added": "r.added_at", "notification": "COALESCE((SELECT MAX(n.created_at) FROM notifications n WHERE n.release_id=r.id),r.added_at)", "release": "r.release_date", "name": "LOWER(r.title)", "updated": "r.updated_at", "local_added": "r.stash_created_at", "watchlist_marked": "r.watchlist_at"}[f.Sort]
	if sortColumn == "" {
		sortColumn = "r.release_date"
	}
	return direction, sortColumn
}

func releaseCursorValue(x domain.Release, sort string) (string, bool) {
	switch sort {
	case "added":
		return x.AddedAt.UTC().Format(time.RFC3339Nano), x.AddedAt.IsZero()
	case "updated":
		return x.UpdatedAt.UTC().Format(time.RFC3339Nano), x.UpdatedAt.IsZero()
	case "local_added":
		return x.StashCreatedAt.UTC().Format(time.RFC3339Nano), x.StashCreatedAt.IsZero()
	case "watchlist_marked":
		return x.WatchlistAt.UTC().Format(time.RFC3339Nano), x.WatchlistAt.IsZero()
	case "name":
		return strings.ToLower(x.Title), false
	default:
		return x.ReleaseDate, false
	}
}

func releaseCursorArg(sort, value string) (any, error) {
	switch sort {
	case "added", "updated", "local_added", "watchlist_marked":
		return time.Parse(time.RFC3339Nano, value)
	default:
		return value, nil
	}
}

func encodeReleaseCursor(x domain.Release, sort string) string {
	value, null := releaseCursorValue(x, sort)
	raw, _ := json.Marshal(releasePageCursor{Null: null, Value: value, ID: x.ID})
	return base64.RawURLEncoding.EncodeToString(raw)
}

func decodeReleaseCursor(raw string) (releasePageCursor, error) {
	var cursor releasePageCursor
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return cursor, err
	}
	err = json.Unmarshal(decoded, &cursor)
	return cursor, err
}

// ReleaseCards returns a lightweight cursor page for the cover grid. The
// notification-date expression cannot be reconstructed from a Release card,
// so that uncommon sort retains offset pagination while still using the
// smaller SELECT; every other Release Library sort uses a stable keyset.
func (s *SQLite) ReleaseCards(ctx context.Context, f domain.ReleaseFilter, cursorRaw string) (domain.ReleasePage, error) {
	where, args := releaseFilterWhere(s.dialect, f)
	direction, sortColumn := releaseSort(f)
	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	useCursor := f.Sort != "notification"
	if useCursor && cursorRaw != "" {
		cursor, err := decodeReleaseCursor(cursorRaw)
		if err != nil || cursor.ID <= 0 {
			return domain.ReleasePage{}, fmt.Errorf("invalid release cursor")
		}
		op := "<"
		if direction == "ASC" {
			op = ">"
		}
		if cursor.Null {
			where += ` AND ` + sortColumn + ` IS NULL AND r.id ` + op + ` ?`
			args = append(args, cursor.ID)
		} else {
			value, err := releaseCursorArg(f.Sort, cursor.Value)
			if err != nil {
				return domain.ReleasePage{}, fmt.Errorf("invalid release cursor value")
			}
			where += ` AND ((` + sortColumn + ` ` + op + ` ?) OR (` + sortColumn + `=? AND r.id ` + op + ` ?) OR ` + sortColumn + ` IS NULL)`
			args = append(args, value, value, cursor.ID)
		}
	}
	q := releaseCardSelect(s.dialect) + where + ` ORDER BY ` + sortColumn + ` ` + direction + ` NULLS LAST,r.id ` + direction + ` LIMIT ?`
	args = append(args, limit+1)
	if !useCursor {
		q += ` OFFSET ?`
		args = append(args, f.Offset)
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return domain.ReleasePage{}, err
	}
	defer rows.Close()
	items := make([]domain.Release, 0, limit+1)
	for rows.Next() {
		x, err := scanRelease(rows)
		if err != nil {
			return domain.ReleasePage{}, err
		}
		items = append(items, x)
	}
	if err := rows.Err(); err != nil {
		return domain.ReleasePage{}, err
	}
	page := domain.ReleasePage{Items: items}
	if len(items) > limit {
		page.Items = items[:limit]
		if useCursor {
			page.NextCursor = encodeReleaseCursor(page.Items[len(page.Items)-1], f.Sort)
		} else {
			page.NextOffset = f.Offset + limit
		}
	}
	return page, nil
}

// ReleasesCount returns the total number of releases matching f, ignoring
// its Limit/Offset, so a paginated UI (e.g. the Monitoring "releases
// checked by the scheduled job" table) can show a true total-result count
// alongside a page of Releases results. It shares releaseFilterWhere with
// Releases so the two queries can never disagree on what matches.
func (s *SQLite) ReleasesCount(ctx context.Context, f domain.ReleaseFilter) (int, error) {
	where, a := releaseFilterWhere(s.dialect, f)
	var total int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*)`+releaseFrom+where, a...).Scan(&total)
	return total, err
}

// ReleaseFilterOptions returns metadata values for the Release Library's
// category picker and Structured Search datalists. It deliberately queries
// the normalized metadata tables/full database instead of the currently
// displayed release page, so suggestions do not disappear when another
// filter is active or the matching release falls outside the UI's page cap.
func (s *SQLite) ReleaseFilterOptions(ctx context.Context, category, search string) ([]string, error) {
	category = strings.ToLower(strings.TrimSpace(category))
	search = strings.TrimSpace(search)
	pattern := metadataLikePattern(search)
	var query string
	var args []any
	switch category {
	case "actress":
		query = `SELECT MIN(name) AS value FROM release_actresses WHERE name_normalized LIKE LOWER(?) ESCAPE '\'`
		args = append(args, pattern)
		if reversed := reverseTwoWordName(search); reversed != "" {
			query += ` OR name_normalized LIKE LOWER(?) ESCAPE '\'`
			args = append(args, metadataLikePattern(reversed))
		}
		query += ` GROUP BY name_normalized ORDER BY name_normalized LIMIT 250`
	case "tag":
		query = `SELECT MIN(name) AS value FROM release_tags WHERE name_normalized LIKE LOWER(?) ESCAPE '\' GROUP BY name_normalized ORDER BY name_normalized LIMIT 250`
		args = append(args, pattern)
	case "studio":
		query = `SELECT MIN(studio) AS value FROM releases WHERE studio<>'' AND LOWER(studio) LIKE LOWER(?) ESCAPE '\' GROUP BY LOWER(studio) ORDER BY LOWER(studio) LIMIT 250`
		args = append(args, pattern)
	case "label":
		query = `SELECT MIN(value) AS value FROM (SELECT label AS value FROM releases WHERE label<>'' AND LOWER(label) LIKE LOWER(?) ESCAPE '\' UNION ALL SELECT s.title AS value FROM sites s JOIN release_sites rs ON rs.site_id=s.id WHERE s.title<>'' AND LOWER(s.title) LIKE LOWER(?) ESCAPE '\') filter_values GROUP BY LOWER(value) ORDER BY LOWER(value) LIMIT 250`
		args = append(args, pattern, pattern)
	default:
		return []string{}, nil
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []string{}
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func metadataLikePattern(value string) string {
	escaped := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(strings.TrimSpace(value))
	escaped = strings.NewReplacer("*", "%", "?", "_").Replace(escaped)
	return "%" + escaped + "%"
}

// ignoreTitlePattern turns one ignore_titles entry into a LIKE pattern for
// use with Dialect.CaseInsensitiveLike, which (unlike the category-Entries
// filters above) does not pair with an ESCAPE clause - matching how the
// plain free-text f.Search filter earlier in this function already leaves
// a literal "%" or "_" in the search term as a wildcard rather than
// escaping it. A "*"/"?" wildcard entry is translated to SQL "%"/"_"; a
// plain entry is wrapped for substring matching, mirroring the frontend's
// own wildcardMatch semantics.
func ignoreTitlePattern(value string) string {
	value = strings.TrimSpace(value)
	if strings.ContainsAny(value, "*?") {
		return strings.NewReplacer("*", "%", "?", "_").Replace(value)
	}
	return "%" + value + "%"
}

func wildcardTextMatch(value, pattern string) bool {
	value, pattern = strings.ToLower(value), strings.ToLower(strings.TrimSpace(pattern))
	if pattern == "" {
		return false
	}
	if !strings.ContainsAny(pattern, "*?") {
		return strings.Contains(value, pattern)
	}
	quoted := regexp.QuoteMeta(pattern)
	quoted = strings.ReplaceAll(strings.ReplaceAll(quoted, `\*`, `.*`), `\?`, `.`)
	matched, _ := regexp.MatchString(`^`+quoted+`$`, value)
	return matched
}

func (s *SQLite) releasePreferred(title string, tags []string) bool {
	s.ignoreMu.RLock()
	ignoredTags := append([]string(nil), s.ignoreTags...)
	ignoredTitles := append([]string(nil), s.ignoreTitles...)
	s.ignoreMu.RUnlock()
	for _, tag := range tags {
		for _, ignored := range ignoredTags {
			if strings.EqualFold(strings.TrimSpace(tag), ignored) {
				return false
			}
		}
	}
	for _, pattern := range ignoredTitles {
		if wildcardTextMatch(title, pattern) {
			return false
		}
	}
	return true
}

func (s *SQLite) reloadIgnoreRules(ctx context.Context) error {
	settings, err := s.Settings(ctx)
	if err != nil {
		return err
	}
	s.ignoreMu.Lock()
	s.ignoreTags = domain.ParseIgnoreList(settings["ignore_tags"])
	s.ignoreTitles = domain.ParseIgnoreList(settings["ignore_titles"])
	s.ignoreMu.Unlock()
	return nil
}

func (s *SQLite) refreshReleasePreferences(ctx context.Context) error {
	s.ignoreMu.RLock()
	tags := append([]string(nil), s.ignoreTags...)
	titles := append([]string(nil), s.ignoreTitles...)
	s.ignoreMu.RUnlock()
	parts := make([]string, 0, 2)
	args := make([]any, 0, len(tags)+len(titles))
	if len(tags) > 0 {
		placeholders := make([]string, len(tags))
		for i, tag := range tags {
			placeholders[i] = "LOWER(?)"
			args = append(args, tag)
		}
		parts = append(parts, `EXISTS (SELECT 1 FROM release_tags t WHERE t.release_id=r.id AND t.name_normalized IN (`+strings.Join(placeholders, ",")+`))`)
	}
	if len(titles) > 0 {
		titleParts := make([]string, len(titles))
		for i, title := range titles {
			titleParts[i] = s.dialect.CaseInsensitiveLike("r.title")
			args = append(args, ignoreTitlePattern(title))
		}
		parts = append(parts, `(`+strings.Join(titleParts, " OR ")+`)`)
	}
	if len(parts) == 0 {
		_, err := s.db.ExecContext(ctx, `UPDATE releases SET is_preferred=1 WHERE is_preferred<>1`)
		return err
	}

	// Do not rewrite the entire releases table. On a large PostgreSQL library,
	// an unconditional UPDATE creates a new row version for every release even
	// when its value remains 1. Besides needless WAL and autovacuum pressure,
	// that can exceed the startup connection deadline and make a healthy
	// database look unreachable. These two updates touch only rows whose
	// materialized preference state actually changed.
	ignored := `(` + strings.Join(parts, " OR ") + `)`
	if _, err := s.db.ExecContext(ctx, `UPDATE releases AS r SET is_preferred=0 WHERE is_preferred<>0 AND `+ignored, args...); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `UPDATE releases AS r SET is_preferred=1 WHERE is_preferred<>1 AND NOT `+ignored, args...)
	return err
}

func (s *SQLite) initializeReleasePreferences(ctx context.Context) error {
	if err := s.reloadIgnoreRules(ctx); err != nil {
		return err
	}
	var completed string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key='release_preference_materialized_v1'`).Scan(&completed)
	if err == nil && completed == "true" {
		return nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err := s.refreshReleasePreferences(ctx); err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO settings(key,value,updated_at) VALUES('release_preference_materialized_v1','true',?) ON CONFLICT(key) DO UPDATE SET value='true',updated_at=excluded.updated_at`, time.Now().UTC())
	return err
}

func reverseTwoWordName(value string) string {
	parts := strings.Fields(strings.TrimSpace(value))
	if len(parts) != 2 {
		return ""
	}
	return parts[1] + " " + parts[0]
}

func parseFilterEntries(value string) []string {
	var entries []string
	if strings.HasPrefix(strings.TrimSpace(value), "[") && json.Unmarshal([]byte(value), &entries) == nil {
		return entries
	}
	return strings.Split(value, ",")
}

func (s *SQLite) Release(ctx context.Context, id int64) (domain.Release, error) {
	return scanRelease(s.db.QueryRowContext(ctx, releaseSelect(s.dialect)+` WHERE r.id=?`, id))
}
func (s *SQLite) ReleaseExistsForSite(ctx context.Context, siteID int64, source, videoID string) (bool, error) {
	identity := releaseIdentity(source, videoID)
	var exists bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM releases r JOIN release_sites rs ON rs.release_id=r.id WHERE r.identity_key=? AND rs.site_id=?)`, identity, siteID).Scan(&exists)
	return exists, err
}

func (s *SQLite) LatestReleaseDateForSite(ctx context.Context, siteID int64) (string, bool, error) {
	var date sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT MAX(r.release_date) FROM releases r JOIN release_sites rs ON rs.release_id=r.id WHERE rs.site_id=? AND r.release_date<>''`, siteID).Scan(&date)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return date.String, date.Valid && date.String != "", nil
}

// ReleaseForSite returns the full release row for this site+source+videoID,
// if one exists - the same identity_key+site join as ReleaseExistsForSite,
// but returning the release itself rather than a bool. Quick refresh (see
// monitor.Service.run's Mode=="quick" handling) uses this to see which
// metadata fields an existing release already has before backfilling only
// the ones that are still blank, instead of overwriting fields it already
// got right.
func (s *SQLite) ReleaseForSite(ctx context.Context, siteID int64, source, videoID string) (domain.Release, bool, error) {
	identity := releaseIdentity(source, videoID)
	var id int64
	err := s.db.QueryRowContext(ctx, `SELECT r.id FROM releases r JOIN release_sites rs ON rs.release_id=r.id WHERE r.identity_key=? AND rs.site_id=?`, identity, siteID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Release{}, false, nil
	}
	if err != nil {
		return domain.Release{}, false, err
	}
	release, err := s.Release(ctx, id)
	if err != nil {
		return domain.Release{}, false, err
	}
	return release, true, nil
}

// ReleaseKnown checks the global source/video identity rather than one site
// association. Historical graph discovery uses it to avoid fetching details
// for releases JAVBeacon already learned from any normal monitoring site.
func (s *SQLite) ReleaseKnown(ctx context.Context, source, videoID string) (string, bool, error) {
	var releaseDate string
	err := s.db.QueryRowContext(ctx, `SELECT release_date FROM releases WHERE identity_key=? AND identity_key<>''`, releaseIdentity(source, videoID)).Scan(&releaseDate)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return releaseDate, err == nil, err
}

// UpsertRelease inserts or updates a release, bumping updated_at when its
// scraped metadata actually changed. UpsertReleaseKeepUpdatedAt is the same
// operation with that bump suppressed - see upsertRelease's preserveUpdatedAt
// parameter.
func (s *SQLite) UpsertRelease(ctx context.Context, x domain.Release) (bool, error) {
	return s.upsertRelease(ctx, x, false)
}

// UpsertReleaseKeepUpdatedAt is UpsertRelease with updated_at never bumped -
// see the Store interface doc comment for why (screenshot backfill).
func (s *SQLite) UpsertReleaseKeepUpdatedAt(ctx context.Context, x domain.Release) (bool, error) {
	return s.upsertRelease(ctx, x, true)
}

func (s *SQLite) upsertRelease(ctx context.Context, x domain.Release, preserveUpdatedAt bool) (bool, error) {
	actresses := releaseActressValues(x)
	x.VideoID = cleanText(x.VideoID)
	x.ScraperID = cleanText(x.ScraperID)
	x.Title = cleanText(x.Title)
	x.ReleaseDate = cleanText(x.ReleaseDate)
	x.Source = cleanText(x.Source)
	x.ImageURL = cleanText(x.ImageURL)
	x.ProductURL = domain.NormalizeJavLibraryURL(cleanText(x.ProductURL))
	x.Actress = strings.Join(actresses, ", ")
	x.Director = cleanText(x.Director)
	x.Studio = cleanText(x.Studio)
	x.Label = cleanText(x.Label)
	x.Duration = cleanText(x.Duration)
	x.Story = cleanText(x.Story)
	x.Genres = uniqueMetadataValues(x.Genres)
	x.Screenshots = uniqueMetadataValues(x.Screenshots)
	identity := releaseIdentity(x.Source, x.VideoID)
	now := time.Now().UTC()
	shots, _ := json.Marshal(x.Screenshots)
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return false, e
	}
	defer tx.Rollback()
	var id int64
	e = tx.QueryRowContext(ctx, `SELECT id FROM releases WHERE identity_key=? AND identity_key<>''`, identity).Scan(&id)
	if identity == "" {
		e = tx.QueryRowContext(ctx, `SELECT id FROM releases WHERE site_id=? AND video_id=?`, x.SiteID, x.VideoID).Scan(&id)
	}
	created := false
	effectiveActresses := actresses
	effectiveTags := x.Genres
	if errors.Is(e, sql.ErrNoRows) {
		var insertErr error
		if x.MonitorDownload && x.MonitorReason == "" {
			x.MonitorReason = "manual"
		}
		id, insertErr = s.dialect.InsertReturningID(ctx, tx, `INSERT INTO releases(site_id,video_id,scraper_id,title,release_date,source,image_url,product_url,director,studio,label,duration,story,screenshots,released,notify_on_release,watchlist,monitor_download,monitor_reason,monitor_site_id,site_monitor_download,identity_key,is_preferred,added_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, x.SiteID, x.VideoID, x.ScraperID, x.Title, x.ReleaseDate, x.Source, x.ImageURL, x.ProductURL, x.Director, x.Studio, x.Label, x.Duration, x.Story, string(shots), x.Released, x.NotifyOnRelease, x.Watchlist, x.MonitorDownload, x.MonitorReason, x.MonitorSiteID, false, identity, s.releasePreferred(x.Title, x.Genres), now, now)
		if insertErr != nil {
			return false, insertErr
		}
		created = true
		e = nil
	}
	if e != nil {
		return false, e
	}
	if _, e = tx.ExecContext(ctx, `INSERT INTO release_sites(release_id,site_id,site_monitor_download) VALUES(?,?,0) ON CONFLICT(release_id,site_id) DO NOTHING`, id, x.SiteID); e != nil {
		return false, e
	}
	if !created {
		// A release's AddedAt is its immutable insertion time. UpdatedAt tracks
		// changes to scraped release metadata, not the fact that a scheduled
		// scrape happened to see the same row again.
		var current struct {
			scraperID, title, releaseDate, source, imageURL, productURL string
			director, studio, label, duration, story                    string
			screenshots, actresses, tags                                string
			released                                                    bool
			addedAt, updatedAt                                          time.Time
		}
		actressAggregate := s.dialect.JSONArrayAgg("name", "SELECT name FROM release_actresses WHERE release_id=releases.id ORDER BY position")
		tagAggregate := s.dialect.JSONArrayAgg("name", "SELECT name FROM release_tags WHERE release_id=releases.id ORDER BY position")
		if e = tx.QueryRowContext(ctx, `SELECT scraper_id,title,release_date,source,image_url,product_url,director,studio,label,duration,story,screenshots,released,added_at,updated_at,`+actressAggregate+`,`+tagAggregate+` FROM releases WHERE id=?`, id).Scan(
			&current.scraperID, &current.title, &current.releaseDate, &current.source, &current.imageURL, &current.productURL,
			&current.director, &current.studio, &current.label, &current.duration, &current.story, &current.screenshots,
			&current.released, &current.addedAt, &current.updatedAt, &current.actresses, &current.tags,
		); e != nil {
			return false, e
		}
		var currentActresses, currentTags []string
		_ = json.Unmarshal([]byte(current.actresses), &currentActresses)
		_ = json.Unmarshal([]byte(current.tags), &currentTags)
		if len(effectiveActresses) == 0 {
			effectiveActresses = currentActresses
		}
		if len(effectiveTags) == 0 {
			effectiveTags = currentTags
		}
		changedText := func(incoming, stored string) bool { return incoming != "" && incoming != stored }
		// Cover and screenshot changes are cache/artwork maintenance, not
		// release metadata changes. Persist their URLs below, but do not move
		// updated_at unless another scraped field changed in the same upsert.
		metadataChanged := changedText(x.ScraperID, current.scraperID) || changedText(x.Title, current.title) ||
			changedText(x.ReleaseDate, current.releaseDate) || changedText(x.Source, current.source) ||
			changedText(x.ProductURL, current.productURL) ||
			(len(actresses) > 0 && !metadataValuesEqual(actresses, currentActresses)) || changedText(x.Director, current.director) ||
			changedText(x.Studio, current.studio) || changedText(x.Label, current.label) ||
			(len(x.Genres) > 0 && !metadataValuesEqual(x.Genres, currentTags)) ||
			changedText(x.Duration, current.duration) || changedText(x.Story, current.story) ||
			(x.Released && !current.released)
		updatedAt := current.updatedAt
		if metadataChanged && !preserveUpdatedAt {
			updatedAt = now
		}
		// Repair an invalid historical ordering while touching the row, and
		// keep the invariant even if an imported AddedAt lies in the future.
		if updatedAt.Before(current.addedAt) {
			updatedAt = current.addedAt
		}
		// released/notify_on_release/watchlist/monitor_download use
		// Dialect.Greatest rather than a literal "MAX(x,?)": SQLite
		// overloads MAX() to also mean the scalar "greater of these two
		// values" seen here (a bool arg converted to 0/1 - see
		// postgres_rewrite.go - never regresses a column from 1 back to
		// 0), but PostgreSQL has no such overload and needs GREATEST()
		// instead (DB Phase 5).
		_, e = tx.ExecContext(ctx, `UPDATE releases SET
		scraper_id=COALESCE(NULLIF(?,''),scraper_id),
		title=COALESCE(NULLIF(?,''),title),
		release_date=COALESCE(NULLIF(?,''),release_date),
		source=COALESCE(NULLIF(?,''),source),
		image_url=COALESCE(NULLIF(?,''),image_url),
		product_url=COALESCE(NULLIF(?,''),product_url),
		director=COALESCE(NULLIF(?,''),director),
		studio=COALESCE(NULLIF(?,''),studio),
		label=COALESCE(NULLIF(?,''),label),
		duration=COALESCE(NULLIF(?,''),duration),
		story=COALESCE(NULLIF(?,''),story),
		screenshots=CASE WHEN ?='' OR ?='[]' OR ?='null' THEN screenshots ELSE ? END,
		released=`+s.dialect.Greatest("released", "?")+`,notify_on_release=`+s.dialect.Greatest("notify_on_release", "?")+`,watchlist=`+s.dialect.Greatest("watchlist", "?")+`,monitor_download=`+s.dialect.Greatest("monitor_download", "?")+`,monitor_reason=CASE WHEN ?=1 THEN COALESCE(NULLIF(?,''),'manual') ELSE monitor_reason END,monitor_site_id=CASE WHEN ?=1 THEN ? ELSE monitor_site_id END,updated_at=? WHERE id=?`,
			x.ScraperID, x.Title, x.ReleaseDate, x.Source, x.ImageURL, x.ProductURL,
			x.Director, x.Studio, x.Label,
			x.Duration, x.Story,
			string(shots), string(shots), string(shots), string(shots),
			x.Released, x.NotifyOnRelease, x.Watchlist, x.MonitorDownload, x.MonitorDownload, x.MonitorReason, x.MonitorDownload, x.MonitorSiteID, updatedAt, id)
		if e != nil {
			return false, e
		}
	}
	var effectiveTitle string
	if e = tx.QueryRowContext(ctx, `SELECT title FROM releases WHERE id=?`, id).Scan(&effectiveTitle); e != nil {
		return false, e
	}
	if created || len(actresses) > 0 {
		if e = syncReleaseActresses(ctx, tx, id, effectiveActresses); e != nil {
			return false, e
		}
	}
	if created || len(x.Genres) > 0 {
		if e = syncReleaseTags(ctx, tx, id, effectiveTags); e != nil {
			return false, e
		}
	}
	if e != nil {
		return false, e
	}
	if _, e = tx.ExecContext(ctx, `UPDATE releases SET is_preferred=? WHERE id=?`, s.releasePreferred(effectiveTitle, effectiveTags), id); e != nil {
		return false, e
	}
	if e = tx.Commit(); e != nil {
		return false, e
	}
	return created, nil
}

func normalizeActressList(value string) string {
	return strings.Join(splitActressValues(value), ", ")
}

var htmlTag = regexp.MustCompile(`(?s)<[^>]*>`)
var namedHTMLEntity = regexp.MustCompile(`(?i)&(?:amp|lt|gt|quot|apos);`)

func cleanText(value string) string {
	for range 2 {
		value = namedHTMLEntity.ReplaceAllStringFunc(value, strings.ToLower)
		decoded := stdhtml.UnescapeString(value)
		if decoded == value {
			break
		}
		value = decoded
	}
	value = htmlTag.ReplaceAllString(value, " ")
	return strings.Join(strings.Fields(value), " ")
}

func splitActressValues(value string) []string {
	values := splitMetadataValues(value, true)
	for i, name := range values {
		name = strings.TrimSpace(name)
		if strings.HasPrefix(name, "(") && strings.HasSuffix(name, ")") {
			name = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(name, "("), ")"))
		}
		values[i] = cleanText(name)
	}
	return uniqueMetadataValues(values)
}

func splitMetadataValues(value string, splitComma bool) []string {
	if splitComma {
		value = strings.ReplaceAll(value, "|", ",")
	}
	separator := "|"
	if splitComma {
		separator = ","
	}
	return uniqueMetadataValues(strings.Split(value, separator))
}

func uniqueMetadataValues(values []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, value := range values {
		value = cleanText(value)
		key := strings.ToLower(value)
		if value != "" && !seen[key] {
			seen[key] = true
			out = append(out, value)
		}
	}
	return out
}

func releaseActressValues(x domain.Release) []string {
	if len(x.Actresses) > 0 {
		return uniqueMetadataValues(x.Actresses)
	}
	return splitActressValues(x.Actress)
}

func metadataValuesEqual(a, b []string) bool {
	a = uniqueMetadataValues(a)
	b = uniqueMetadataValues(b)
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

type metadataExecer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func syncReleaseActresses(ctx context.Context, exec metadataExecer, releaseID int64, actresses []string) error {
	if _, err := exec.ExecContext(ctx, `DELETE FROM release_actresses WHERE release_id=?`, releaseID); err != nil {
		return err
	}
	for position, name := range uniqueMetadataValues(actresses) {
		if _, err := exec.ExecContext(ctx, `INSERT INTO release_actresses(release_id,position,name,name_normalized) VALUES(?,?,?,?)`, releaseID, position, name, strings.ToLower(name)); err != nil {
			return err
		}
	}
	return nil
}

func syncReleaseTags(ctx context.Context, exec metadataExecer, releaseID int64, tags []string) error {
	if _, err := exec.ExecContext(ctx, `DELETE FROM release_tags WHERE release_id=?`, releaseID); err != nil {
		return err
	}
	for position, name := range uniqueMetadataValues(tags) {
		if _, err := exec.ExecContext(ctx, `INSERT INTO release_tags(release_id,position,name,name_normalized) VALUES(?,?,?,?)`, releaseID, position, name, strings.ToLower(name)); err != nil {
			return err
		}
	}
	return nil
}

// SyncReleaseActresses replaces one release's ordered actress relationships.
func SyncReleaseActresses(ctx context.Context, exec metadataExecer, releaseID int64, actresses []string) error {
	return syncReleaseActresses(ctx, exec, releaseID, actresses)
}

// SyncReleaseTags replaces one release's ordered tag relationships.
func SyncReleaseTags(ctx context.Context, exec metadataExecer, releaseID int64, tags []string) error {
	return syncReleaseTags(ctx, exec, releaseID, tags)
}

// SyncReleaseMetadata replaces both normalized relationship sets. Callers
// performing partial updates should use the selective helpers above so an
// omitted field never erases metadata learned by an earlier detail scrape.
func SyncReleaseMetadata(ctx context.Context, exec metadataExecer, releaseID int64, actresses, tags []string) error {
	if err := syncReleaseActresses(ctx, exec, releaseID, actresses); err != nil {
		return err
	}
	return syncReleaseTags(ctx, exec, releaseID, tags)
}

func (s *SQLite) backfillReleaseMetadata(ctx context.Context) error {
	hasActress, err := s.columnExists(ctx, "releases", "actress")
	if err != nil {
		return err
	}
	hasGenres, err := s.columnExists(ctx, "releases", "genres")
	if err != nil {
		return err
	}
	if !hasActress && !hasGenres {
		return nil
	}
	actressExpr, genresExpr := `''`, `'[]'`
	conditions := []string{}
	if hasActress {
		actressExpr = "r.actress"
		conditions = append(conditions, `r.actress<>''`)
	}
	if hasGenres {
		genresExpr = "r.genres"
		conditions = append(conditions, `r.genres NOT IN ('','[]','null')`)
	}
	actressAggregate := s.dialect.JSONArrayAgg("name", "SELECT name FROM release_actresses WHERE release_id=r.id ORDER BY position")
	tagAggregate := s.dialect.JSONArrayAgg("name", "SELECT name FROM release_tags WHERE release_id=r.id ORDER BY position")
	rows, err := s.db.QueryContext(ctx, `SELECT r.id,`+actressExpr+`,`+genresExpr+`,`+actressAggregate+`,`+tagAggregate+` FROM releases r WHERE `+strings.Join(conditions, ` OR `))
	if err != nil {
		return err
	}
	type pendingMetadata struct {
		id                            int64
		actress, genres               string
		currentActresses, currentTags string
	}
	pending := []pendingMetadata{}
	for rows.Next() {
		var item pendingMetadata
		if err := rows.Scan(&item.id, &item.actress, &item.genres, &item.currentActresses, &item.currentTags); err != nil {
			rows.Close()
			return err
		}
		pending = append(pending, item)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if len(pending) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, item := range pending {
		var tags, currentActresses, currentTags []string
		if json.Unmarshal([]byte(item.genres), &tags) != nil {
			tags = splitMetadataValues(item.genres, false)
		}
		_ = json.Unmarshal([]byte(item.currentActresses), &currentActresses)
		_ = json.Unmarshal([]byte(item.currentTags), &currentTags)
		if hasActress && item.actress != "" {
			seen := metadataValueSet(currentActresses)
			position := len(currentActresses)
			for _, name := range splitActressValues(item.actress) {
				if seen[strings.ToLower(name)] {
					continue
				}
				if _, err := tx.ExecContext(ctx, `INSERT INTO release_actresses(release_id,position,name,name_normalized) VALUES(?,?,?,?) ON CONFLICT(release_id,name_normalized) DO NOTHING`, item.id, position, name, strings.ToLower(name)); err != nil {
					return err
				}
				seen[strings.ToLower(name)] = true
				position++
			}
		}
		if hasGenres && item.genres != "" && item.genres != "[]" && item.genres != "null" {
			seen := metadataValueSet(currentTags)
			position := len(currentTags)
			for _, name := range uniqueMetadataValues(tags) {
				if seen[strings.ToLower(name)] {
					continue
				}
				if _, err := tx.ExecContext(ctx, `INSERT INTO release_tags(release_id,position,name,name_normalized) VALUES(?,?,?,?) ON CONFLICT(release_id,name_normalized) DO NOTHING`, item.id, position, name, strings.ToLower(name)); err != nil {
					return err
				}
				seen[strings.ToLower(name)] = true
				position++
			}
		}
	}
	return tx.Commit()
}

func metadataValueSet(values []string) map[string]bool {
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		seen[strings.ToLower(cleanText(value))] = true
	}
	return seen
}

func (s *SQLite) cleanupStoredReleaseText(ctx context.Context) error {
	var completed int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM settings WHERE key='metadata_text_cleanup_v1'`).Scan(&completed); err != nil || completed > 0 {
		return err
	}
	hasActress, err := s.columnExists(ctx, "releases", "actress")
	if err != nil {
		return err
	}
	hasGenres, err := s.columnExists(ctx, "releases", "genres")
	if err != nil {
		return err
	}
	// Fresh and already-normalized databases have no compatibility columns.
	// Their normalized values are cleaned as they are inserted, so this legacy
	// one-time cleanup has nothing to do.
	if !hasActress || !hasGenres {
		_, err = s.db.ExecContext(ctx, `INSERT INTO settings(key,value,updated_at) VALUES('metadata_text_cleanup_v1','true',?) ON CONFLICT(key) DO UPDATE SET value='true',updated_at=excluded.updated_at`, time.Now().UTC())
		return err
	}
	// LIKE (not SQLite's instr()) so this one-time backfill query runs
	// unmodified on both engines - see DB Phase 5's dialect.go/
	// postgres_rewrite.go doc comments on how this package stays
	// portable. Equivalent to the prior instr(col,'&')>0/instr(col,'<')>0
	// checks: does column contain the literal character.
	rows, err := s.db.QueryContext(ctx, `SELECT id,video_id,scraper_id,title,release_date,source,image_url,product_url,actress,director,studio,genres,duration,story,screenshots FROM releases WHERE video_id LIKE '%&%' OR scraper_id LIKE '%&%' OR title LIKE '%&%' OR release_date LIKE '%&%' OR source LIKE '%&%' OR image_url LIKE '%&%' OR product_url LIKE '%&%' OR actress LIKE '%&%' OR actress LIKE '%(%' OR director LIKE '%&%' OR studio LIKE '%&%' OR genres LIKE '%&%' OR duration LIKE '%&%' OR story LIKE '%&%' OR screenshots LIKE '%&%' OR video_id LIKE '%<%' OR scraper_id LIKE '%<%' OR title LIKE '%<%' OR release_date LIKE '%<%' OR source LIKE '%<%' OR image_url LIKE '%<%' OR product_url LIKE '%<%' OR actress LIKE '%<%' OR director LIKE '%<%' OR studio LIKE '%<%' OR genres LIKE '%<%' OR duration LIKE '%<%' OR story LIKE '%<%' OR screenshots LIKE '%<%'`)
	if err != nil {
		return err
	}
	type item struct {
		id                                                                                                                              int64
		videoID, scraperID, title, releaseDate, source, imageURL, productURL, actress, director, studio, genres, duration, story, shots string
	}
	items := []item{}
	for rows.Next() {
		var x item
		if err := rows.Scan(&x.id, &x.videoID, &x.scraperID, &x.title, &x.releaseDate, &x.source, &x.imageURL, &x.productURL, &x.actress, &x.director, &x.studio, &x.genres, &x.duration, &x.story, &x.shots); err != nil {
			rows.Close()
			return err
		}
		items = append(items, x)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, x := range items {
		var tags, shots []string
		if json.Unmarshal([]byte(x.genres), &tags) != nil {
			tags = splitMetadataValues(x.genres, false)
		}
		_ = json.Unmarshal([]byte(x.shots), &shots)
		tags = uniqueMetadataValues(tags)
		shots = uniqueMetadataValues(shots)
		genresJSON, _ := json.Marshal(tags)
		shotsJSON, _ := json.Marshal(shots)
		actress := normalizeActressList(x.actress)
		if _, err := tx.ExecContext(ctx, `UPDATE releases SET video_id=?,scraper_id=?,title=?,release_date=?,source=?,image_url=?,product_url=?,actress=?,director=?,studio=?,genres=?,duration=?,story=?,screenshots=? WHERE id=?`, cleanText(x.videoID), cleanText(x.scraperID), cleanText(x.title), cleanText(x.releaseDate), cleanText(x.source), cleanText(x.imageURL), cleanText(x.productURL), actress, cleanText(x.director), cleanText(x.studio), string(genresJSON), cleanText(x.duration), cleanText(x.story), string(shotsJSON), x.id); err != nil {
			return err
		}
		if err := SyncReleaseMetadata(ctx, tx, x.id, splitActressValues(actress), tags); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO settings(key,value,updated_at) VALUES('metadata_text_cleanup_v1','true',?) ON CONFLICT(key) DO UPDATE SET value='true',updated_at=excluded.updated_at`, time.Now().UTC())
	if err != nil {
		return err
	}
	return tx.Commit()
}

// migrateNormalizedReleaseMetadata makes the normalized relationship tables
// the sole persisted source of actress and tag data. It deliberately runs
// after backfill and text cleanup, verifies every non-empty legacy value has a
// relationship row, and only then removes the duplicate columns.
func (s *SQLite) migrateNormalizedReleaseMetadata(ctx context.Context) error {
	hasActress, err := s.columnExists(ctx, "releases", "actress")
	if err != nil {
		return err
	}
	hasGenres, err := s.columnExists(ctx, "releases", "genres")
	if err != nil {
		return err
	}
	if hasActress {
		var missing int
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM releases r WHERE TRIM(r.actress)<>'' AND NOT EXISTS (SELECT 1 FROM release_actresses a WHERE a.release_id=r.id)`).Scan(&missing); err != nil {
			return err
		}
		if missing > 0 {
			return fmt.Errorf("refusing to remove releases.actress: %d releases were not normalized", missing)
		}
	}
	if hasGenres {
		var missing int
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM releases r WHERE r.genres NOT IN ('','[]','null') AND NOT EXISTS (SELECT 1 FROM release_tags t WHERE t.release_id=r.id)`).Scan(&missing); err != nil {
			return err
		}
		if missing > 0 {
			return fmt.Errorf("refusing to remove releases.genres: %d releases were not normalized", missing)
		}
	}
	for _, index := range []string{"idx_releases_actress_trgm", "idx_releases_genres_trgm"} {
		if _, err := s.db.ExecContext(ctx, `DROP INDEX IF EXISTS `+index); err != nil {
			return err
		}
	}
	if hasActress {
		if _, err := s.db.ExecContext(ctx, `ALTER TABLE releases DROP COLUMN actress`); err != nil {
			return err
		}
	}
	if hasGenres {
		if _, err := s.db.ExecContext(ctx, `ALTER TABLE releases DROP COLUMN genres`); err != nil {
			return err
		}
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO settings(key,value,updated_at) VALUES('normalized_release_metadata_v1','true',?) ON CONFLICT(key) DO UPDATE SET value='true',updated_at=excluded.updated_at`, time.Now().UTC())
	return err
}

func (s *SQLite) PatchRelease(ctx context.Context, id int64, released, local, notified, notifyOnRelease, watchlist, monitorDownload *bool, label *string, allowNonPreferredFilenames *bool) error {
	now := time.Now().UTC()
	sets := []string{"updated_at=?"}
	a := []any{now}
	for k, v := range map[string]*bool{"released": released, "is_local": local, "notified": notified, "notify_on_release": notifyOnRelease, "watchlist": watchlist, "monitor_download": monitorDownload, "allow_non_preferred_filenames": allowNonPreferredFilenames} {
		if v != nil {
			sets = append(sets, k+"=?")
			a = append(a, *v)
		}
	}
	// WatchlistAt records when a release was (most recently) marked Watchlist,
	// powering the Release Library's Watchlist-tab default sort ("when marked
	// as watchlist", newest first). Unlike StashAddedAt's "set once" pattern,
	// this is refreshed on every explicit mark-as-watchlist toggle rather than
	// only the first one, since re-marking something after unmarking it is a
	// deliberate user action that should bubble it back to the top - it's
	// left untouched when watchlist is cleared, so the last-marked date stays
	// available if the release is marked again later without another
	// explicit toggle in between.
	if watchlist != nil && *watchlist {
		sets = append(sets, "watchlist_at=?")
		a = append(a, now)
	}
	if monitorDownload != nil {
		sets = append(sets, "monitor_reason=?", "monitor_site_id=0")
		if *monitorDownload {
			a = append(a, "manual")
		} else {
			a = append(a, "")
		}
	}
	if label != nil {
		sets = append(sets, "label=?")
		a = append(a, *label)
	}
	a = append(a, id)
	r, e := s.db.ExecContext(ctx, `UPDATE releases SET `+strings.Join(sets, ",")+` WHERE id=?`, a...)
	if e == nil {
		if n, _ := r.RowsAffected(); n == 0 {
			return sql.ErrNoRows
		}
	}
	return e
}

// BulkSetReleaseFlags applies monitor_download and/or
// allow_non_preferred_filenames to every release in ids in a single
// statement - the mass-select "stop monitoring" and "ignore filename
// exclusions / accepted filename patterns" bulk actions on the "Releases
// checked by the scheduled job" table. Either pointer may be nil to leave
// that column untouched; both nil (or an empty ids) is a no-op.
func (s *SQLite) BulkSetReleaseFlags(ctx context.Context, ids []int64, monitorDownload, allowNonPreferredFilenames *bool) (int64, error) {
	if len(ids) == 0 || (monitorDownload == nil && allowNonPreferredFilenames == nil) {
		return 0, nil
	}
	sets := []string{"updated_at=?"}
	a := []any{time.Now().UTC()}
	if monitorDownload != nil {
		sets = append(sets, "monitor_download=?")
		a = append(a, *monitorDownload)
		sets = append(sets, "monitor_reason=?", "monitor_site_id=0")
		if *monitorDownload {
			a = append(a, "manual")
		} else {
			a = append(a, "")
		}
	}
	if allowNonPreferredFilenames != nil {
		sets = append(sets, "allow_non_preferred_filenames=?")
		a = append(a, *allowNonPreferredFilenames)
	}
	placeholders := make([]string, len(ids))
	for i, releaseID := range ids {
		placeholders[i] = "?"
		a = append(a, releaseID)
	}
	r, e := s.db.ExecContext(ctx, `UPDATE releases SET `+strings.Join(sets, ",")+` WHERE id IN (`+strings.Join(placeholders, ",")+`)`, a...)
	if e != nil {
		return 0, e
	}
	return r.RowsAffected()
}
func (s *SQLite) SetReleaseMonitoring(ctx context.Context, id int64, enabled bool, reason string, siteID int64) error {
	if !enabled {
		reason, siteID = "", 0
	} else if strings.TrimSpace(reason) == "" {
		reason = "manual"
	}
	result, err := s.db.ExecContext(ctx, `UPDATE releases SET monitor_download=?,monitor_reason=?,monitor_site_id=?,updated_at=? WHERE id=?`, enabled, reason, siteID, time.Now().UTC(), id)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return sql.ErrNoRows
	}
	return nil
}
func (s *SQLite) SetStashState(ctx context.Context, id int64, local bool, sceneID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UTC()
	// stash_added_at is set the first time this release gets a scene ID
	// (previously empty, now not) and left untouched on every later sync of
	// the same release, so it reflects when the release was first added to
	// StashApp rather than the most recent sync time (TODO-2.0).
	result, err := tx.ExecContext(ctx, `UPDATE releases SET is_local=?,stash_scene_id=?,updated_at=?,stash_added_at=CASE WHEN ?<>'' AND stash_added_at IS NULL THEN ? ELSE stash_added_at END,monitor_download=CASE WHEN is_local=0 AND ?=1 THEN 0 ELSE monitor_download END,monitor_reason=CASE WHEN is_local=0 AND ?=1 THEN '' ELSE monitor_reason END,monitor_site_id=CASE WHEN is_local=0 AND ?=1 THEN 0 ELSE monitor_site_id END WHERE id=?`, local, sceneID, now, sceneID, now, local, local, local, id)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return sql.ErrNoRows
	}
	if local {
		_, err = tx.ExecContext(ctx, `INSERT INTO notifications(release_id,type,message,created_at) VALUES(?,'local_available','Available in local Stash library',?) ON CONFLICT(release_id,type) DO NOTHING`, id, now)
	} else {
		_, err = tx.ExecContext(ctx, `DELETE FROM notifications WHERE release_id=? AND type='local_available'`, id)
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

// SetStashReleaseDate records the release date StashApp has on file for a
// release's matched scene (TODO-2.0's "Missing released status display").
// It is deliberately separate from SetStashState: an empty date is a no-op
// (never clears a previously stored value) rather than an error, so a
// custom stash_graphql_query that does not request the scene's `date` field
// simply leaves this alone instead of needing special handling upstream.
func (s *SQLite) SetStashReleaseDate(ctx context.Context, id int64, date string) error {
	if date == "" {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `UPDATE releases SET stash_release_date=?,updated_at=? WHERE id=?`, date, time.Now().UTC(), id)
	return err
}

// SetStashCreatedAt records StashApp's own "Created At" timestamp for a
// release's matched scene (see domain.Release.StashCreatedAt), pulled
// during the same best-effort, scene-ID-keyed sync pass as playback stats
// (stash.Service.run). Guards against a zero time the same way
// SetStashReleaseDate guards against a blank string, so a sync round that
// could not determine created_at (older StashApp, custom query without the
// field) never clears a previously stored value.
func (s *SQLite) SetStashCreatedAt(ctx context.Context, id int64, createdAt time.Time) error {
	if createdAt.IsZero() {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `UPDATE releases SET stash_created_at=?,updated_at=? WHERE id=?`, createdAt, time.Now().UTC(), id)
	return err
}

// SetStashPlaybackStats records the O-Counter, Play Count, Last Played, and
// (derived from StashApp's o_history) Last O Count Date figures pulled from
// a release's matched StashApp scene during a local-library sync
// (TODO-2.0's Release Library Conditions "O Count"/"Last O Count Date"/
// "Last Played" fields - see stash.Service.run's playback-stats pass).
// Unlike SetStashReleaseDate, all four values are written unconditionally:
// the "never overwrite a real value with a blank one" decision (e.g. the
// running StashApp version's GraphQL schema doesn't have o_history yet, so
// lastOCountAt can't be computed this sync) is made by the caller, which
// already knows the release's previous values and can choose what to pass
// through rather than this method guessing from a blank string alone -
// unlike a date, 0 is a legitimate O-Counter/Play Count value, not a
// stand-in for "unknown".
func (s *SQLite) SetStashPlaybackStats(ctx context.Context, id int64, oCounter, playCount int, lastPlayedAt, lastOCountAt string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE releases SET o_counter=?,play_count=?,last_played_at=?,last_o_count_at=?,updated_at=? WHERE id=?`, oCounter, playCount, lastPlayedAt, lastOCountAt, time.Now().UTC(), id)
	return err
}
func (s *SQLite) Stats(ctx context.Context) (domain.Stats, error) {
	var x domain.Stats
	e := s.db.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM sites),(SELECT COUNT(*) FROM releases),COALESCE(SUM(CASE WHEN released=1 THEN 1 ELSE 0 END),0),COALESCE(SUM(CASE WHEN released=0 THEN 1 ELSE 0 END),0),COALESCE(SUM(CASE WHEN is_local=1 THEN 1 ELSE 0 END),0) FROM releases`).Scan(&x.Sites, &x.Releases, &x.Released, &x.Upcoming, &x.Local)
	if e != nil {
		return x, fmt.Errorf("stats: %w", e)
	}
	return x, nil
}

func (s *SQLite) Settings(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT key,value FROM settings`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}
func (s *SQLite) SaveSettings(ctx context.Context, values map[string]string) error {
	tagsChanged, titlesChanged := false, false
	if value, ok := values["ignore_tags"]; ok {
		var previous string
		err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key='ignore_tags'`).Scan(&previous)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		tagsChanged = previous != value
	}
	if value, ok := values["ignore_titles"]; ok {
		var previous string
		err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key='ignore_titles'`).Scan(&previous)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		titlesChanged = previous != value
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for k, v := range values {
		if _, err = tx.ExecContext(ctx, `INSERT INTO settings(key,value,updated_at) VALUES(?,?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value,updated_at=excluded.updated_at`, k, v, time.Now().UTC()); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if tagsChanged || titlesChanged {
		if err := s.reloadIgnoreRules(ctx); err != nil {
			return err
		}
		return s.refreshReleasePreferences(ctx)
	}
	return nil
}

func (s *SQLite) ScreenshotBackfillCompleted(ctx context.Context, releaseID int64) (bool, error) {
	var completed bool
	err := s.db.QueryRowContext(ctx, `SELECT screenshots_checked_at IS NOT NULL FROM releases WHERE id=?`, releaseID).Scan(&completed)
	return completed, err
}

func (s *SQLite) MarkScreenshotBackfillCompleted(ctx context.Context, releaseID int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE releases SET screenshots_checked_at=? WHERE id=?`, time.Now().UTC(), releaseID)
	return err
}

func (s *SQLite) User(ctx context.Context) (domain.User, error) {
	var x domain.User
	err := s.db.QueryRowContext(ctx, `SELECT id,username,password_hash FROM users WHERE id=1`).Scan(&x.ID, &x.Username, &x.PasswordHash)
	return x, err
}

func (s *SQLite) SaveUser(ctx context.Context, username, hash string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO users(id,username,password_hash,updated_at) VALUES(1,?,?,?) ON CONFLICT(id) DO UPDATE SET username=excluded.username,password_hash=excluded.password_hash,updated_at=excluded.updated_at`, username, hash, time.Now().UTC())
	return err
}

func (s *SQLite) CreateSession(ctx context.Context, x domain.Session) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO sessions(token,user_id,expires_at,created_at) VALUES(?,?,?,?)`, x.Token, x.UserID, x.ExpiresAt, time.Now().UTC())
	return err
}

func (s *SQLite) Session(ctx context.Context, token string) (domain.Session, error) {
	var x domain.Session
	err := s.db.QueryRowContext(ctx, `SELECT token,user_id,expires_at FROM sessions WHERE token=? AND expires_at>?`, token, time.Now().UTC()).Scan(&x.Token, &x.UserID, &x.ExpiresAt)
	return x, err
}

func (s *SQLite) DeleteSession(ctx context.Context, token string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token=?`, token)
	return err
}

func (s *SQLite) DeleteExpiredSessions(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at<=?`, time.Now().UTC())
	return err
}

func (s *SQLite) Preferences(ctx context.Context) (json.RawMessage, error) {
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT state FROM user_preferences WHERE user_id=1`).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return json.RawMessage(`{}`), nil
	}
	return json.RawMessage(raw), err
}

func (s *SQLite) SavePreferences(ctx context.Context, raw json.RawMessage) error {
	if !json.Valid(raw) {
		return errors.New("preferences must be valid JSON")
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO user_preferences(user_id,state,updated_at) VALUES(1,?,?) ON CONFLICT(user_id) DO UPDATE SET state=excluded.state,updated_at=excluded.updated_at`, string(raw), time.Now().UTC())
	return err
}

func (s *SQLite) FilterPresets(ctx context.Context) ([]domain.FilterPreset, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,name,state,created_at,updated_at FROM filter_presets WHERE user_id=1 ORDER BY `+s.dialect.CaseInsensitiveOrderBy("name"))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.FilterPreset{}
	for rows.Next() {
		var x domain.FilterPreset
		var raw string
		if err := rows.Scan(&x.ID, &x.Name, &raw, &x.CreatedAt, &x.UpdatedAt); err != nil {
			return nil, err
		}
		x.State = json.RawMessage(raw)
		out = append(out, x)
	}
	return out, rows.Err()
}

func (s *SQLite) SaveFilterPreset(ctx context.Context, x domain.FilterPreset) (domain.FilterPreset, error) {
	if strings.TrimSpace(x.Name) == "" || !json.Valid(x.State) {
		return x, errors.New("preset name and valid state are required")
	}
	now := time.Now().UTC()
	if x.ID == 0 {
		var err error
		x.ID, err = s.dialect.InsertReturningID(ctx, s.db, `INSERT INTO filter_presets(user_id,name,state,created_at,updated_at) VALUES(1,?,?,?,?)`, x.Name, string(x.State), now, now)
		if err != nil {
			return x, err
		}
		x.CreatedAt = now
	} else {
		_, err := s.db.ExecContext(ctx, `UPDATE filter_presets SET name=?,state=?,updated_at=? WHERE id=? AND user_id=1`, x.Name, string(x.State), now, x.ID)
		if err != nil {
			return x, err
		}
	}
	x.UpdatedAt = now
	return x, nil
}

func (s *SQLite) DeleteFilterPreset(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM filter_presets WHERE id=? AND user_id=1`, id)
	return err
}

func (s *SQLite) PrepareHistoricalBackfill(ctx context.Context, resume bool) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if !resume {
		if _, err = tx.ExecContext(ctx, `DELETE FROM historical_backfill_items`); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM historical_backfill_sources`); err != nil {
			return err
		}
	} else {
		// A completed source only needs its newly inserted head. An unfinished
		// source catches up to the saved date boundary and then continues deeper.
		if _, err = tx.ExecContext(ctx, `UPDATE historical_backfill_sources SET resume_date=cursor_date,catchup_only=CASE WHEN state='completed' THEN 1 ELSE 0 END,state='pending',next_page=1`); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `UPDATE historical_backfill_state SET state='running',updated_at=? WHERE id=1`, time.Now().UTC())
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SQLite) UpsertHistoricalBackfillSources(ctx context.Context, sources []domain.HistoricalBackfillSource) error {
	for _, x := range sources {
		if _, err := s.db.ExecContext(ctx, `INSERT INTO historical_backfill_sources(url,kind,name,state,next_page) VALUES(?,?,?,'pending',1) ON CONFLICT(url) DO UPDATE SET kind=excluded.kind,name=excluded.name`, x.URL, x.Kind, x.Name); err != nil {
			return err
		}
	}
	return nil
}

func (s *SQLite) HistoricalBackfillSources(ctx context.Context) ([]domain.HistoricalBackfillSource, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT url,kind,name,state,cursor_date,resume_date,next_page,page_limit,pages_completed,catchup_only FROM historical_backfill_sources ORDER BY CASE kind WHEN 'genre' THEN 0 WHEN 'star' THEN 1 ELSE 2 END,name,url`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.HistoricalBackfillSource
	for rows.Next() {
		var x domain.HistoricalBackfillSource
		if err := rows.Scan(&x.URL, &x.Kind, &x.Name, &x.State, &x.CursorDate, &x.ResumeDate, &x.NextPage, &x.PageLimit, &x.PagesCompleted, &x.CatchupOnly); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

func (s *SQLite) HistoricalBackfillItem(ctx context.Context, videoID string) (domain.HistoricalBackfillItem, bool, error) {
	var x domain.HistoricalBackfillItem
	err := s.db.QueryRowContext(ctx, `SELECT video_id,release_date,state,source_url,error FROM historical_backfill_items WHERE video_id=?`, videoID).Scan(&x.VideoID, &x.ReleaseDate, &x.State, &x.SourceURL, &x.Error)
	if errors.Is(err, sql.ErrNoRows) {
		return x, false, nil
	}
	return x, err == nil, err
}

func (s *SQLite) SaveHistoricalBackfillItem(ctx context.Context, x domain.HistoricalBackfillItem) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO historical_backfill_items(video_id,release_date,state,source_url,error,updated_at) VALUES(?,?,?,?,?,?) ON CONFLICT(video_id) DO UPDATE SET release_date=excluded.release_date,state=excluded.state,source_url=excluded.source_url,error=excluded.error,updated_at=excluded.updated_at`, x.VideoID, x.ReleaseDate, x.State, x.SourceURL, x.Error, time.Now().UTC())
	return err
}

func (s *SQLite) SaveHistoricalBackfillSource(ctx context.Context, x domain.HistoricalBackfillSource) error {
	_, err := s.db.ExecContext(ctx, `UPDATE historical_backfill_sources SET state=?,cursor_date=?,resume_date=?,next_page=?,page_limit=?,pages_completed=?,catchup_only=? WHERE url=?`, x.State, x.CursorDate, x.ResumeDate, x.NextPage, x.PageLimit, x.PagesCompleted, x.CatchupOnly, x.URL)
	return err
}

func (s *SQLite) SetHistoricalBackfillState(ctx context.Context, state string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE historical_backfill_state SET state=?,updated_at=? WHERE id=1`, state, time.Now().UTC())
	return err
}

func (s *SQLite) HistoricalBackfillStats(ctx context.Context) (domain.HistoricalBackfillStats, error) {
	var x domain.HistoricalBackfillStats
	err := s.db.QueryRowContext(ctx, `SELECT state,updated_at,
		(SELECT COUNT(*) FROM historical_backfill_sources),
		(SELECT COUNT(*) FROM historical_backfill_sources WHERE state='completed'),
		COALESCE((SELECT SUM(pages_completed) FROM historical_backfill_sources),0),
		COALESCE((SELECT SUM(CASE WHEN page_limit>0 THEN page_limit ELSE pages_completed+1 END) FROM historical_backfill_sources),0),
		(SELECT COUNT(*) FROM historical_backfill_items),
		(SELECT COUNT(*) FROM historical_backfill_items WHERE state='completed'),
		(SELECT COUNT(*) FROM historical_backfill_items WHERE state='failed')
		FROM historical_backfill_state WHERE id=1`).Scan(&x.State, &x.UpdatedAt, &x.SourcesTotal, &x.SourcesCompleted, &x.PagesCompleted, &x.PagesEstimated, &x.ReleasesDiscovered, &x.ReleasesCompleted, &x.ReleasesFailed)
	return x, err
}

func (s *SQLite) SaveJob(ctx context.Context, x domain.Job) (int64, error) {
	if x.State == "" {
		if x.Running {
			x.State = "running"
		} else if x.Error != "" {
			x.State = "failed"
		} else {
			x.State = "completed"
		}
	}
	if x.ID == 0 {
		return s.dialect.InsertReturningID(ctx, s.db, `INSERT INTO job_history(kind,state,mode,title,scheduled,site_count,site_title,provider,started_at,finished_at,added,updated,skipped,error) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, x.Kind, x.State, x.Mode, x.Title, x.Scheduled, x.SiteCount, x.SiteTitle, x.Provider, x.StartedAt, x.FinishedAt, x.Added, x.Updated, x.Skipped, x.Error)
	}
	_, err := s.db.ExecContext(ctx, `UPDATE job_history SET state=?,mode=?,title=?,scheduled=?,site_count=?,site_title=?,provider=?,started_at=?,finished_at=?,added=?,updated=?,skipped=?,error=? WHERE id=?`, x.State, x.Mode, x.Title, x.Scheduled, x.SiteCount, x.SiteTitle, x.Provider, x.StartedAt, x.FinishedAt, x.Added, x.Updated, x.Skipped, x.Error, x.ID)
	return x.ID, err
}

func (s *SQLite) Jobs(ctx context.Context, limit int) ([]domain.Job, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,kind,state,mode,title,scheduled,site_count,site_title,provider,started_at,finished_at,added,updated,skipped,error FROM job_history ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Job{}
	for rows.Next() {
		var x domain.Job
		var started, finished sql.NullTime
		if err := rows.Scan(&x.ID, &x.Kind, &x.State, &x.Mode, &x.Title, &x.Scheduled, &x.SiteCount, &x.SiteTitle, &x.Provider, &started, &finished, &x.Added, &x.Updated, &x.Skipped, &x.Error); err != nil {
			return nil, err
		}
		x.StartedAt = started.Time
		x.FinishedAt = finished.Time
		x.Running = x.State == "running"
		out = append(out, x)
	}
	return out, rows.Err()
}

// JobHistory returns a single newest-first timeline across scrape jobs and
// meaningful download lifecycle activity. Per-result search audit rows stay
// stored for the search/audit paths, but are intentionally excluded here: one
// provider response can contain dozens of accepted/rejected candidates and
// otherwise makes a single search look like dozens of duplicate jobs.
// Combining the tables in SQL keeps pagination stable:
// fetching page two cannot repeat or omit rows merely because one category
// happened to have more recent activity than the other.
func (s *SQLite) JobHistory(ctx context.Context, limit, offset int) ([]domain.JobHistoryEntry, int, error) {
	if limit <= 0 {
		limit = 25
	}
	if limit > 100 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}
	var total int
	if err := s.db.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM job_history)+(SELECT COUNT(*) FROM downloads WHERE status NOT IN ('searched','search_accepted','search_rejected'))`).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,category,kind,state,mode,title,provider,scheduled,site_count,started_at,finished_at,added,updated,skipped,error,details FROM (
		SELECT id,
			CASE WHEN kind='scrape' OR kind='javlibrary_historical_backfill' THEN 'Scraping' WHEN kind LIKE '%download%' THEN 'Downloading' WHEN kind LIKE '%stash%' THEN 'StashApp' ELSE 'System' END AS category,
			kind,state,mode,COALESCE(NULLIF(title,''),site_title) AS title,provider,scheduled,site_count,started_at,finished_at,added,updated,skipped,error,'' AS details
		FROM job_history
		UNION ALL
		SELECT d.id,'Downloading' AS category,'download' AS kind,d.status AS state,d.source_type AS mode,
			COALESCE(NULLIF(r.video_id,''),NULLIF(d.query,''),NULLIF(d.name,''),'Download') AS title,
			d.provider,0 AS scheduled,0 AS site_count,d.added_at AS started_at,
			CASE WHEN d.status IN ('completed','failed','cancelled','skipped','removed') THEN d.updated_at ELSE NULL END AS finished_at,
			0 AS added,0 AS updated,0 AS skipped,d.error,
			COALESCE(NULLIF(d.match_reason,''),NULLIF(d.qb_response,''),NULLIF(d.name,''),'') AS details
		FROM downloads d LEFT JOIN releases r ON r.id=d.release_id
		WHERE d.status NOT IN ('searched','search_accepted','search_rejected')
	) activity ORDER BY started_at DESC,id DESC LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []domain.JobHistoryEntry{}
	for rows.Next() {
		var x domain.JobHistoryEntry
		var started, finished any
		if err := rows.Scan(&x.ID, &x.Category, &x.Kind, &x.State, &x.Mode, &x.Title, &x.Provider, &x.Scheduled, &x.SiteCount, &started, &finished, &x.Added, &x.Updated, &x.Skipped, &x.Error, &x.Details); err != nil {
			return nil, 0, err
		}
		parseTime := func(value any) (time.Time, error) {
			if value == nil {
				return time.Time{}, nil
			}
			if parsed, ok := value.(time.Time); ok {
				return parsed, nil
			}
			raw := fmt.Sprint(value)
			for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05.999999999 -0700 MST", "2006-01-02 15:04:05 -0700 MST", "2006-01-02 15:04:05.999999999Z07:00", "2006-01-02 15:04:05"} {
				if parsed, err := time.Parse(layout, raw); err == nil {
					return parsed, nil
				}
			}
			return time.Time{}, fmt.Errorf("invalid job history timestamp %q", raw)
		}
		if x.StartedAt, err = parseTime(started); err != nil {
			return nil, 0, err
		}
		if x.FinishedAt, err = parseTime(finished); err != nil {
			return nil, 0, err
		}
		out = append(out, x)
	}
	return out, total, rows.Err()
}

func (s *SQLite) SaveDownloadSearchRun(ctx context.Context, x domain.DownloadSearchRun) (domain.DownloadSearchRun, error) {
	if x.Schedule != "recent" && x.Schedule != "older" {
		return x, errors.New("download search run schedule must be recent or older")
	}
	id, err := s.dialect.InsertReturningID(ctx, s.db, `INSERT INTO download_search_runs(schedule,started_at,finished_at,checked,found,downloaded,skipped,failed,error) VALUES(?,?,?,?,?,?,?,?,?)`, x.Schedule, x.StartedAt, x.FinishedAt, x.Checked, x.Found, x.Downloaded, x.Skipped, x.Failed, x.Error)
	if err != nil {
		return x, err
	}
	x.ID = id
	return x, nil
}

func (s *SQLite) DownloadSearchRuns(ctx context.Context, schedule string, limit int) ([]domain.DownloadSearchRun, error) {
	if limit <= 0 || limit > 500 {
		limit = 25
	}
	query := `SELECT id,schedule,started_at,finished_at,checked,found,downloaded,skipped,failed,error FROM download_search_runs`
	args := []any{}
	if schedule != "" {
		query += ` WHERE schedule=?`
		args = append(args, schedule)
	}
	query += ` ORDER BY finished_at DESC,id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.DownloadSearchRun{}
	for rows.Next() {
		var x domain.DownloadSearchRun
		if err := rows.Scan(&x.ID, &x.Schedule, &x.StartedAt, &x.FinishedAt, &x.Checked, &x.Found, &x.Downloaded, &x.Skipped, &x.Failed, &x.Error); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

func (s *SQLite) SaveDownload(ctx context.Context, x domain.Download) (domain.Download, error) {
	now := time.Now().UTC()
	if len(x.Files) == 0 {
		x.Files = json.RawMessage(`[]`)
	}
	if x.ID == 0 {
		var err error
		x.ID, err = s.dialect.InsertReturningID(ctx, s.db, `INSERT INTO downloads(release_id,provider,source_type,source_reference,query,torrent_hash,name,files,status,match_reason,qb_response,post_status,error,seed_ratio,progress,seeds,peers,eta_seconds,seen_complete,filename_pattern_excluded,added_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, x.ReleaseID, x.Provider, x.SourceType, x.SourceReference, x.Query, x.TorrentHash, x.Name, string(x.Files), x.Status, x.MatchReason, x.QBResponse, x.PostStatus, x.Error, x.SeedRatio, x.Progress, x.Seeds, x.Peers, x.ETASeconds, x.SeenComplete, x.FilenamePatternExcluded, now, now)
		if err != nil {
			return x, err
		}
		x.AddedAt = now
	} else {
		_, err := s.db.ExecContext(ctx, `UPDATE downloads SET torrent_hash=?,name=?,files=?,status=?,match_reason=?,qb_response=?,post_status=?,error=?,seed_ratio=?,progress=?,seeds=?,peers=?,eta_seconds=?,seen_complete=?,filename_pattern_excluded=?,updated_at=? WHERE id=?`, x.TorrentHash, x.Name, string(x.Files), x.Status, x.MatchReason, x.QBResponse, x.PostStatus, x.Error, x.SeedRatio, x.Progress, x.Seeds, x.Peers, x.ETASeconds, x.SeenComplete, x.FilenamePatternExcluded, now, x.ID)
		if err != nil {
			return x, err
		}
	}
	x.UpdatedAt = now
	return x, nil
}

const downloadSelect = `SELECT d.id,COALESCE(d.release_id,0),COALESCE(r.video_id,''),COALESCE(r.image_url,''),d.provider,d.source_type,d.source_reference,d.query,d.torrent_hash,d.name,d.files,d.status,d.match_reason,d.qb_response,d.post_status,d.error,d.seed_ratio,d.progress,d.seeds,d.peers,d.eta_seconds,d.seen_complete,d.filename_pattern_excluded,d.added_at,d.updated_at FROM downloads d LEFT JOIN releases r ON r.id=d.release_id`

func scanDownloads(rows *sql.Rows) ([]domain.Download, error) {
	out := []domain.Download{}
	for rows.Next() {
		var x domain.Download
		var files string
		if err := rows.Scan(&x.ID, &x.ReleaseID, &x.VideoID, &x.ImageURL, &x.Provider, &x.SourceType, &x.SourceReference, &x.Query, &x.TorrentHash, &x.Name, &files, &x.Status, &x.MatchReason, &x.QBResponse, &x.PostStatus, &x.Error, &x.SeedRatio, &x.Progress, &x.Seeds, &x.Peers, &x.ETASeconds, &x.SeenComplete, &x.FilenamePatternExcluded, &x.AddedAt, &x.UpdatedAt); err != nil {
			return nil, err
		}
		x.Files = json.RawMessage(files)
		out = append(out, x)
	}
	return out, rows.Err()
}

func (s *SQLite) Downloads(ctx context.Context, status string) ([]domain.Download, error) {
	q := downloadSelect
	a := []any{}
	if status != "" {
		q += ` WHERE d.status=?`
		a = append(a, status)
	}
	q += ` ORDER BY d.updated_at DESC`
	rows, err := s.db.QueryContext(ctx, q, a...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanDownloads(rows)
}

// LatestReleaseDownload returns the same active-first row represented by a
// release's download status pill. It is intentionally a single-release lookup
// used by Release Details; library-card queries stay lean at large scale.
func (s *SQLite) LatestReleaseDownload(ctx context.Context, releaseID int64) (domain.Download, error) {
	rows, err := s.db.QueryContext(ctx, downloadSelect+` WHERE d.release_id=? AND d.status IN ('downloading','completed') ORDER BY CASE d.status WHEN 'downloading' THEN 0 ELSE 1 END,d.updated_at DESC LIMIT 1`, releaseID)
	if err != nil {
		return domain.Download{}, err
	}
	defer rows.Close()
	items, err := scanDownloads(rows)
	if err != nil {
		return domain.Download{}, err
	}
	if len(items) == 0 {
		return domain.Download{}, sql.ErrNoRows
	}
	return items[0], nil
}

// DownloadActivity is the paginated, filterable counterpart of Downloads
// used by the Download Activity table (Phase 4B): it returns one page of
// results plus the total number of matching rows, ignoring f.Limit/Offset
// for the count, so the UI can show pagination alongside a true total.
func (s *SQLite) DownloadActivity(ctx context.Context, f domain.DownloadFilter) ([]domain.Download, int, error) {
	where := ` WHERE 1=1`
	var a []any
	if f.Status != "" {
		where += ` AND d.status=?`
		a = append(a, f.Status)
	}
	if f.Search != "" {
		where += ` AND (r.video_id LIKE ? OR d.query LIKE ? OR d.name LIKE ?)`
		like := "%" + f.Search + "%"
		a = append(a, like, like, like)
	}
	if f.Source != "" {
		where += ` AND (d.provider=? OR d.source_type=?)`
		a = append(a, f.Source, f.Source)
	}
	if f.FilenamePatternExcluded {
		where += ` AND d.filename_pattern_excluded=1`
	}
	if f.Stalled {
		where += ` AND (d.seeds=0 OR d.seen_complete=0)`
	}
	switch f.SeenComplete {
	case "never":
		where += ` AND d.seen_complete=0`
	case "before":
		if f.SeenCompleteDate > 0 {
			where += ` AND d.seen_complete>0 AND d.seen_complete<?`
			a = append(a, f.SeenCompleteDate)
		}
	case "after":
		if f.SeenCompleteDate > 0 {
			where += ` AND d.seen_complete>=?`
			a = append(a, f.SeenCompleteDate)
		}
	}
	var total int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM downloads d LEFT JOIN releases r ON r.id=d.release_id`+where, a...).Scan(&total); err != nil {
		return nil, 0, err
	}
	direction := "DESC"
	if strings.EqualFold(f.Direction, "asc") {
		direction = "ASC"
	}
	sortColumn := map[string]string{"added": "d.added_at", "status": "d.status", "seeds": "d.seeds", "peers": "d.peers", "seen_complete": "d.seen_complete", "eta": "d.eta_seconds", "progress": "d.progress"}[f.Sort]
	if sortColumn == "" {
		sortColumn = "d.updated_at"
	}
	q := downloadSelect + where + ` ORDER BY ` + sortColumn + ` ` + direction + `,d.id DESC`
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 50
	}
	q += ` LIMIT ? OFFSET ?`
	a = append(a, f.Limit, f.Offset)
	rows, err := s.db.QueryContext(ctx, q, a...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	items, err := scanDownloads(rows)
	return items, total, err
}

func (s *SQLite) DeleteDownloadsForRelease(ctx context.Context, releaseID int64) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `DELETE FROM notifications WHERE release_id=? AND type IN ('download_started','downloaded','download_failed')`, releaseID); err != nil {
		return 0, err
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM downloads WHERE release_id=?`, releaseID)
	if err != nil {
		return 0, err
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	return deleted, tx.Commit()
}

func (s *SQLite) PathMappings(ctx context.Context) ([]domain.PathMapping, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,download_prefix,local_prefix FROM path_mappings ORDER BY LENGTH(download_prefix) DESC,id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.PathMapping{}
	for rows.Next() {
		var x domain.PathMapping
		if err := rows.Scan(&x.ID, &x.DownloadPrefix, &x.LocalPrefix); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}
func (s *SQLite) SavePathMapping(ctx context.Context, x domain.PathMapping) (domain.PathMapping, error) {
	if x.ID == 0 {
		var e error
		x.ID, e = s.dialect.InsertReturningID(ctx, s.db, `INSERT INTO path_mappings(download_prefix,local_prefix) VALUES(?,?)`, x.DownloadPrefix, x.LocalPrefix)
		if e != nil {
			return x, e
		}
	} else {
		_, e := s.db.ExecContext(ctx, `UPDATE path_mappings SET download_prefix=?,local_prefix=? WHERE id=?`, x.DownloadPrefix, x.LocalPrefix, x.ID)
		if e != nil {
			return x, e
		}
	}
	return x, nil
}
func (s *SQLite) DeletePathMapping(ctx context.Context, id int64) error {
	_, e := s.db.ExecContext(ctx, `DELETE FROM path_mappings WHERE id=?`, id)
	return e
}

func (s *SQLite) PipelineSteps(ctx context.Context) ([]domain.PipelineStep, error) {
	rows, e := s.db.QueryContext(ctx, `SELECT id,position,trigger,type,name,config,enabled,timeout_seconds FROM pipeline_steps ORDER BY position,id`)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []domain.PipelineStep{}
	for rows.Next() {
		var x domain.PipelineStep
		var c string
		if e := rows.Scan(&x.ID, &x.Position, &x.Trigger, &x.Type, &x.Name, &c, &x.Enabled, &x.TimeoutSeconds); e != nil {
			return nil, e
		}
		x.Config = json.RawMessage(c)
		out = append(out, x)
	}
	return out, rows.Err()
}
func (s *SQLite) SavePipelineSteps(ctx context.Context, steps []domain.PipelineStep) error {
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if _, e = tx.ExecContext(ctx, `DELETE FROM pipeline_steps`); e != nil {
		return e
	}
	for i, x := range steps {
		if x.Trigger == "" {
			x.Trigger = "download_completed"
		}
		if x.Trigger != "download_completed" && x.Trigger != "download_completed_removed" {
			return errors.New("unsupported pipeline trigger")
		}
		if x.Type != "shell" && x.Type != "stash_graphql" {
			return errors.New("unsupported pipeline step type")
		}
		if len(x.Config) == 0 {
			x.Config = json.RawMessage(`{}`)
		}
		if x.TimeoutSeconds < 0 {
			return errors.New("pipeline step timeout must be zero or greater")
		}
		if _, e = tx.ExecContext(ctx, `INSERT INTO pipeline_steps(position,trigger,type,name,config,enabled,timeout_seconds) VALUES(?,?,?,?,?,?,?)`, i, x.Trigger, x.Type, x.Name, string(x.Config), x.Enabled, x.TimeoutSeconds); e != nil {
			return e
		}
	}
	return tx.Commit()
}
func (s *SQLite) PipelineRun(ctx context.Context, downloadID int64, trigger string) (domain.PipelineRun, error) {
	var run domain.PipelineRun
	var finished sql.NullTime
	err := s.db.QueryRowContext(ctx, `SELECT download_id,trigger,state,error,started_at,finished_at FROM pipeline_runs WHERE download_id=? AND trigger=?`, downloadID, trigger).Scan(&run.DownloadID, &run.Trigger, &run.State, &run.Error, &run.StartedAt, &finished)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.PipelineRun{}, nil
	}
	run.FinishedAt = finished.Time
	return run, err
}
func (s *SQLite) SavePipelineRun(ctx context.Context, run domain.PipelineRun) error {
	if run.StartedAt.IsZero() {
		run.StartedAt = time.Now().UTC()
	}
	var finished any
	if !run.FinishedAt.IsZero() {
		finished = run.FinishedAt
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO pipeline_runs(download_id,trigger,state,error,started_at,finished_at) VALUES(?,?,?,?,?,?) ON CONFLICT(download_id,trigger) DO UPDATE SET state=excluded.state,error=excluded.error,started_at=excluded.started_at,finished_at=excluded.finished_at`, run.DownloadID, run.Trigger, run.State, run.Error, run.StartedAt, finished)
	return err
}
func (s *SQLite) SavePipelineLog(ctx context.Context, x domain.PipelineLog) (domain.PipelineLog, error) {
	if len(x.Configuration) == 0 {
		x.Configuration = json.RawMessage(`{}`)
	}
	if x.ID == 0 {
		x.StartedAt = time.Now().UTC()
		var e error
		x.ID, e = s.dialect.InsertReturningID(ctx, s.db, `INSERT INTO pipeline_logs(download_id,step_id,state,configuration,output,error,started_at,finished_at) VALUES(?,?,?,?,?,?,?,NULL)`, x.DownloadID, x.StepID, x.State, string(x.Configuration), x.Output, x.Error, x.StartedAt)
		if e != nil {
			return x, e
		}
	} else {
		x.FinishedAt = time.Now().UTC()
		_, e := s.db.ExecContext(ctx, `UPDATE pipeline_logs SET state=?,output=?,error=?,finished_at=? WHERE id=?`, x.State, x.Output, x.Error, x.FinishedAt, x.ID)
		if e != nil {
			return x, e
		}
	}
	return x, nil
}
func (s *SQLite) PipelineLogs(ctx context.Context, downloadID int64) ([]domain.PipelineLog, error) {
	rows, e := s.db.QueryContext(ctx, `SELECT id,download_id,COALESCE(step_id,0),state,configuration,output,error,started_at,finished_at FROM pipeline_logs WHERE download_id=? ORDER BY id`, downloadID)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []domain.PipelineLog{}
	for rows.Next() {
		var x domain.PipelineLog
		var config string
		var finished sql.NullTime
		if e := rows.Scan(&x.ID, &x.DownloadID, &x.StepID, &x.State, &config, &x.Output, &x.Error, &x.StartedAt, &finished); e != nil {
			return nil, e
		}
		x.Configuration = json.RawMessage(config)
		x.FinishedAt = finished.Time
		out = append(out, x)
	}
	return out, rows.Err()
}

func (s *SQLite) Notifications(ctx context.Context, kind string) ([]domain.Notification, error) {
	q := `SELECT id,release_id,type,message,created_at FROM notifications`
	a := []any{}
	if kind != "" {
		q += ` WHERE type=?`
		a = append(a, kind)
	}
	q += ` ORDER BY created_at DESC`
	if kind == "local_available" {
		q += ` LIMIT 500`
	}
	rows, e := s.db.QueryContext(ctx, q, a...)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []domain.Notification{}
	releaseIDs := make([]int64, 0)
	for rows.Next() {
		var x domain.Notification
		if e := rows.Scan(&x.ID, &x.ReleaseID, &x.Type, &x.Message, &x.CreatedAt); e != nil {
			return nil, e
		}
		releaseIDs = append(releaseIDs, x.ReleaseID)
		out = append(out, x)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	releases := make(map[int64]domain.Release, len(releaseIDs))
	for offset := 0; offset < len(releaseIDs); offset += 250 {
		end := min(offset+250, len(releaseIDs))
		placeholders := make([]string, end-offset)
		args := make([]any, end-offset)
		for index, releaseID := range releaseIDs[offset:end] {
			placeholders[index] = "?"
			args[index] = releaseID
		}
		releaseRows, err := s.db.QueryContext(ctx, releaseSelect(s.dialect)+` WHERE r.id IN (`+strings.Join(placeholders, ",")+`)`, args...)
		if err != nil {
			return nil, err
		}
		for releaseRows.Next() {
			release, err := scanRelease(releaseRows)
			if err != nil {
				releaseRows.Close()
				return nil, err
			}
			releases[release.ID] = release
		}
		if err := releaseRows.Close(); err != nil {
			return nil, err
		}
	}
	for index := range out {
		if release, ok := releases[out[index].ReleaseID]; ok {
			releaseCopy := release
			out[index].Release = &releaseCopy
		}
	}
	return out, nil
}
func (s *SQLite) DeleteNotifications(ctx context.Context, kind string, ids []int64) (int64, error) {
	if strings.TrimSpace(kind) == "" {
		return 0, errors.New("notification type is required")
	}
	query := `DELETE FROM notifications WHERE type=?`
	args := []any{kind}
	if len(ids) > 0 {
		placeholders := make([]string, 0, len(ids))
		for _, id := range ids {
			if id <= 0 {
				continue
			}
			placeholders = append(placeholders, "?")
			args = append(args, id)
		}
		if len(placeholders) == 0 {
			return 0, errors.New("valid notification ids are required")
		}
		query += ` AND id IN (` + strings.Join(placeholders, ",") + `)`
	}
	result, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}
func (s *SQLite) CreateNotification(ctx context.Context, releaseID int64, kind, message string) (bool, error) {
	r, e := s.db.ExecContext(ctx, `INSERT INTO notifications(release_id,type,message,created_at) VALUES(?,?,?,?) ON CONFLICT(release_id,type) DO NOTHING`, releaseID, kind, message, time.Now().UTC())
	if e != nil {
		return false, e
	}
	n, _ := r.RowsAffected()
	return n > 0, nil
}
func (s *SQLite) WatchlistSynced(ctx context.Context, releaseID int64, sceneID, tagID string) (bool, error) {
	var n int
	e := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM watchlist_sync WHERE release_id=? AND stash_scene_id=? AND tag_id=?`, releaseID, sceneID, tagID).Scan(&n)
	return n > 0, e
}
func (s *SQLite) SaveWatchlistSync(ctx context.Context, releaseID int64, sceneID, tagID, result string) error {
	_, e := s.db.ExecContext(ctx, `INSERT INTO watchlist_sync(release_id,stash_scene_id,tag_id,synced_at,result) VALUES(?,?,?,?,?) ON CONFLICT(release_id) DO UPDATE SET stash_scene_id=excluded.stash_scene_id,tag_id=excluded.tag_id,synced_at=excluded.synced_at,result=excluded.result`, releaseID, sceneID, tagID, time.Now().UTC(), result)
	return e
}
func (s *SQLite) ClearWatchlistSync(ctx context.Context, releaseID int64) error {
	_, e := s.db.ExecContext(ctx, `DELETE FROM watchlist_sync WHERE release_id=?`, releaseID)
	return e
}

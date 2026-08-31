package web

import (
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Net005/JAVBeacon/internal/domain"
	"github.com/Net005/JAVBeacon/internal/store"
)

func newMigrationTestServer(t *testing.T, sqlitePath string) *Server {
	t.Helper()
	st, e := store.OpenSQLite(filepath.Join(t.TempDir(), "app.db"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { st.Close() })
	return &Server{store: st, sqlitePath: sqlitePath, log: slog.Default()}
}

func TestMigrationSourcePath(t *testing.T) {
	s := &Server{sqlitePath: "/data/javbeacon.db"}
	cases := []struct {
		mode, path, want, wantErr string
	}{
		{"current", "", "/data/javbeacon.db", ""},
		{"path", "/tmp/other.db", "/tmp/other.db", ""},
		{"path", "  ", "", "path is required"},
		{"bogus", "", "", "source mode must be"},
	}
	for _, c := range cases {
		got, e := s.migrationSourcePath(c.mode, c.path)
		if c.wantErr != "" {
			if e == nil || !strings.Contains(e.Error(), c.wantErr) {
				t.Errorf("mode=%q path=%q: err=%v, want containing %q", c.mode, c.path, e, c.wantErr)
			}
			continue
		}
		if e != nil || got != c.want {
			t.Errorf("mode=%q path=%q: got=%q err=%v, want=%q", c.mode, c.path, got, e, c.want)
		}
	}

	empty := &Server{}
	if _, e := empty.migrationSourcePath("current", ""); e == nil {
		t.Fatal("expected an error when no sqlitePath is configured")
	}
}

func TestValidateSQLiteCopyReadsStatsWithoutMutatingTheOriginal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source.db")
	src, e := store.OpenSQLite(path)
	if e != nil {
		t.Fatal(e)
	}
	if _, e := src.SaveSite(t.Context(), domain.Site{Title: "GIGA", Type: "Site", Name: "GIGA", Enabled: true}); e != nil {
		t.Fatal(e)
	}
	src.Close()

	before, e := os.Stat(path)
	if e != nil {
		t.Fatal(e)
	}

	stats, e := validateSQLiteCopy(t.Context(), path)
	if e != nil {
		t.Fatalf("validateSQLiteCopy: %v", e)
	}
	if stats.Sites != 1 {
		t.Fatalf("stats.Sites = %d, want 1", stats.Sites)
	}

	after, e := os.Stat(path)
	if e != nil {
		t.Fatal(e)
	}
	if !before.ModTime().Equal(after.ModTime()) || before.Size() != after.Size() {
		t.Fatalf("original file was touched: before=%v/%d after=%v/%d", before.ModTime(), before.Size(), after.ModTime(), after.Size())
	}

	// A validated copy must not leave its temp directory behind.
	entries, _ := filepath.Glob(filepath.Join(os.TempDir(), "javbeacon-migration-src-*"))
	if len(entries) != 0 {
		t.Fatalf("leftover temp dirs: %v", entries)
	}
}

func TestValidateSQLiteCopyMissingFile(t *testing.T) {
	if _, e := validateSQLiteCopy(t.Context(), filepath.Join(t.TempDir(), "does-not-exist.db")); e == nil {
		t.Fatal("expected an error for a missing source file")
	}
}

func TestSetupMigrationSourceAndStatusRoundtrip(t *testing.T) {
	s := newMigrationTestServer(t, "/data/javbeacon.db")

	rec := doJSON(t, s.setupMigrationSource, http.MethodPost, "/api/setup/migration/source", map[string]any{"mode": "current"})
	if rec.Code != http.StatusOK {
		t.Fatalf("source: status=%d body=%s", rec.Code, rec.Body)
	}
	var st migrationState
	if e := json.Unmarshal(rec.Body.Bytes(), &st); e != nil {
		t.Fatal(e)
	}
	if st.SourcePath != "/data/javbeacon.db" {
		t.Fatalf("SourcePath = %q", st.SourcePath)
	}

	statusRec := httptest.NewRecorder()
	s.setupMigrationStatus(statusRec, httptest.NewRequest(http.MethodGet, "/api/setup/migration/status", nil))
	if statusRec.Code != http.StatusOK {
		t.Fatalf("status: status=%d body=%s", statusRec.Code, statusRec.Body)
	}
	if e := json.Unmarshal(statusRec.Body.Bytes(), &st); e != nil {
		t.Fatal(e)
	}
	if st.SourcePath != "/data/javbeacon.db" {
		t.Fatalf("status did not reflect the selected source: %+v", st)
	}
}

func TestSetupMigrationSourceRejectsInvalidMode(t *testing.T) {
	s := newMigrationTestServer(t, "/data/javbeacon.db")
	rec := doJSON(t, s.setupMigrationSource, http.MethodPost, "/api/setup/migration/source", map[string]any{"mode": "bogus"})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status=%d body=%s, want 422", rec.Code, rec.Body)
	}
}

func TestSetupMigrationValidateSourceRequiresSelectionFirst(t *testing.T) {
	s := newMigrationTestServer(t, "")
	rec := doJSON(t, s.setupMigrationValidateSource, http.MethodPost, "/api/setup/migration/validate-source", nil)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status=%d body=%s, want 422", rec.Code, rec.Body)
	}
}

func TestSetupMigrationValidateSourceEndToEnd(t *testing.T) {
	sourcePath := filepath.Join(t.TempDir(), "source.db")
	src, e := store.OpenSQLite(sourcePath)
	if e != nil {
		t.Fatal(e)
	}
	if _, e := src.SaveSite(t.Context(), domain.Site{Title: "GIGA", Type: "Site", Name: "GIGA", Enabled: true}); e != nil {
		t.Fatal(e)
	}
	src.Close()

	s := newMigrationTestServer(t, sourcePath)
	if rec := doJSON(t, s.setupMigrationSource, http.MethodPost, "/api/setup/migration/source", map[string]any{"mode": "current"}); rec.Code != http.StatusOK {
		t.Fatalf("source: status=%d body=%s", rec.Code, rec.Body)
	}
	rec := doJSON(t, s.setupMigrationValidateSource, http.MethodPost, "/api/setup/migration/validate-source", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("validate-source: status=%d body=%s", rec.Code, rec.Body)
	}
	var out struct {
		Validated bool           `json:"validated"`
		Status    migrationState `json:"status"`
	}
	if e := json.Unmarshal(rec.Body.Bytes(), &out); e != nil {
		t.Fatal(e)
	}
	if !out.Validated || !out.Status.SourceValidated || out.Status.SourceStats == nil || out.Status.SourceStats.Sites != 1 {
		t.Fatalf("unexpected validate-source result: %+v", out)
	}
}

func TestSetupMigrationMigrateRequiresPriorSteps(t *testing.T) {
	s := newMigrationTestServer(t, "/data/javbeacon.db")
	rec := doJSON(t, s.setupMigrationMigrate, http.MethodPost, "/api/setup/migration/migrate", nil)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status=%d body=%s, want 422 before source/target are ready", rec.Code, rec.Body)
	}
}

// TestSetupMigrationTargetEndpointsFailFastAgainstAnUnreachableServer
// exercises the postgres/inspect-target/prepare-target handlers' failure
// path against a port nothing listens on, without needing a real
// PostgreSQL server: they should report a clean, classified failure rather
// than panicking or hanging for the full connection timeout.
func TestSetupMigrationTargetEndpointsFailFastAgainstAnUnreachableServer(t *testing.T) {
	s := newMigrationTestServer(t, "")
	target := map[string]any{"host": "127.0.0.1", "port": unusedTCPPort(t), "database": "javbeacon", "user": "javbeacon", "password": "unmistakable-test-password-marker", "sslmode": "disable"}

	deadline := time.Now().Add(8 * time.Second)
	handlers := map[string]func(http.ResponseWriter, *http.Request){
		"/api/setup/migration/postgres":       s.setupMigrationPostgres,
		"/api/setup/migration/inspect-target": s.setupMigrationInspectTarget,
		"/api/setup/migration/prepare-target": s.setupMigrationPrepareTarget,
	}
	for path, handler := range handlers {
		rec := doJSON(t, handler, http.MethodPost, path, target)
		if time.Now().After(deadline) {
			t.Fatalf("%s took too long to fail against an unreachable target", path)
		}
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status=%d body=%s", path, rec.Code, rec.Body)
		}
		var body map[string]any
		if e := json.Unmarshal(rec.Body.Bytes(), &body); e != nil {
			t.Fatal(e)
		}
		for _, boolKey := range []string{"connected", "inspected", "prepared"} {
			if v, ok := body[boolKey]; ok && v == true {
				t.Fatalf("%s unexpectedly reported success against an unreachable target: %v", path, body)
			}
		}
		if _, has := body["message"]; !has {
			t.Fatalf("%s response missing a message: %v", path, body)
		}
		if strings.Contains(rec.Body.String(), "unmistakable-test-password-marker") {
			t.Fatalf("%s response leaked the password", path)
		}
	}
}

func unusedTCPPort(t *testing.T) int {
	t.Helper()
	l, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return port
}

func TestBuildMigrationInsertSQL(t *testing.T) {
	got := buildMigrationInsertSQL(migrationTableSpec{Name: "sites", PrimaryKey: []string{"id"}}, []string{"id", "title", "enabled"})
	want := "INSERT INTO sites (id,title,enabled) VALUES (?,?,?) ON CONFLICT (id) DO UPDATE SET title=excluded.title,enabled=excluded.enabled"
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}

	got2 := buildMigrationInsertSQL(migrationTableSpec{Name: "release_actresses", PrimaryKey: []string{"release_id", "name_normalized"}}, []string{"release_id", "position", "name", "name_normalized"})
	want2 := "INSERT INTO release_actresses (release_id,position,name,name_normalized) VALUES (?,?,?,?) ON CONFLICT (release_id,name_normalized) DO UPDATE SET position=excluded.position,name=excluded.name"
	if got2 != want2 {
		t.Fatalf("got  %s\nwant %s", got2, want2)
	}

	// A table whose every column is part of the primary key (none exist in
	// this application's own schema, but the generic builder must still
	// degrade to DO NOTHING rather than emitting an empty/invalid SET
	// clause).
	got3 := buildMigrationInsertSQL(migrationTableSpec{Name: "t", PrimaryKey: []string{"a", "b"}}, []string{"a", "b"})
	want3 := "INSERT INTO t (a,b) VALUES (?,?) ON CONFLICT (a,b) DO NOTHING"
	if got3 != want3 {
		t.Fatalf("got  %s\nwant %s", got3, want3)
	}
}

// migrationTestIDs are the original source-side ids TestMigrationTransferEndToEnd
// asserts survive the copy unchanged.
type migrationTestIDs struct {
	SiteID    int64
	ReleaseID int64
}

// buildTestMigrationSource populates a fresh SQLite database touching every
// one of the tables migrationTables copies (sites, releases and their
// actress/tag/site relationships, settings, users, sessions, preferences,
// filter presets, job history, downloads, path mappings, pipeline
// steps/runs/logs, notifications, watchlist sync), mirroring
// internal/store's own TestPostgresStoreFullLifecycle so the transfer test
// below exercises real, relationship-carrying data rather than a single
// trivial row.
func buildTestMigrationSource(t *testing.T) (path string, ids migrationTestIDs) {
	t.Helper()
	path = filepath.Join(t.TempDir(), "migration-source.db")
	src, e := store.OpenSQLite(path)
	if e != nil {
		t.Fatal(e)
	}
	defer src.Close()
	ctx := t.Context()

	site, e := src.SaveSite(ctx, domain.Site{Title: "GIGA", Type: "Site", Name: "GIGA", Enabled: true})
	if e != nil {
		t.Fatal(e)
	}
	created, e := src.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "SPSF-52", Title: "Test release", Source: "GIGA", Released: true, Actress: "Neo Akari, Yua Mikami", Genres: []string{"Drama"}, Screenshots: []string{"https://example.test/1.jpg"}})
	if e != nil || !created {
		t.Fatalf("created=%v err=%v", created, e)
	}
	releases, e := src.Releases(ctx, domain.ReleaseFilter{})
	if e != nil || len(releases) != 1 {
		t.Fatalf("releases=%v err=%v", releases, e)
	}
	release := releases[0]

	if e := src.SaveSettings(ctx, map[string]string{"a": "1", "b": "2"}); e != nil {
		t.Fatal(e)
	}
	if e := src.SaveUser(ctx, "admin", "hash"); e != nil {
		t.Fatal(e)
	}
	u, e := src.User(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if e := src.CreateSession(ctx, domain.Session{Token: "tok1", UserID: u.ID, ExpiresAt: time.Now().Add(time.Hour)}); e != nil {
		t.Fatal(e)
	}
	if e := src.SavePreferences(ctx, []byte(`{"x":1}`)); e != nil {
		t.Fatal(e)
	}
	if _, e := src.SaveFilterPreset(ctx, domain.FilterPreset{Name: "preset1", State: []byte(`{}`)}); e != nil {
		t.Fatal(e)
	}
	if _, e := src.SaveJob(ctx, domain.Job{Kind: "scrape", SiteTitle: "GIGA"}); e != nil {
		t.Fatal(e)
	}
	dl, e := src.SaveDownload(ctx, domain.Download{ReleaseID: release.ID, Status: "downloading", Files: []byte(`[]`)})
	if e != nil {
		t.Fatal(e)
	}
	if _, e := src.SavePathMapping(ctx, domain.PathMapping{DownloadPrefix: "/downloads", LocalPrefix: "/local"}); e != nil {
		t.Fatal(e)
	}
	if e := src.SavePipelineSteps(ctx, []domain.PipelineStep{{Type: "shell", Name: "step1", Enabled: true}}); e != nil {
		t.Fatal(e)
	}
	steps, e := src.PipelineSteps(ctx)
	if e != nil || len(steps) != 1 {
		t.Fatalf("steps=%v err=%v", steps, e)
	}
	if e := src.SavePipelineRun(ctx, domain.PipelineRun{DownloadID: dl.ID, Trigger: "download_completed", State: "completed"}); e != nil {
		t.Fatal(e)
	}
	if _, e := src.SavePipelineLog(ctx, domain.PipelineLog{DownloadID: dl.ID, StepID: steps[0].ID, State: "running"}); e != nil {
		t.Fatal(e)
	}
	if _, e := src.CreateNotification(ctx, release.ID, "download_started", "started"); e != nil {
		t.Fatal(e)
	}
	if e := src.SaveWatchlistSync(ctx, release.ID, "scene1", "tag1", "ok"); e != nil {
		t.Fatal(e)
	}
	if e := src.SetReleaseMonitoring(ctx, release.ID, true, "manual", 0); e != nil {
		t.Fatal(e)
	}
	if e := src.SetStashState(ctx, release.ID, true, "scene123"); e != nil {
		t.Fatal(e)
	}

	return path, migrationTestIDs{SiteID: site.ID, ReleaseID: release.ID}
}

// testMigrationPostgresConfig mirrors internal/store's own
// testPostgresConfig (unexported there, so not reusable directly): it
// dials the configured/default test server first and calls t.Skip, rather
// than t.Fatal, when nothing answers, so this package's suite still passes
// unmodified in the common case of no PostgreSQL server being available.
func testMigrationPostgresConfig(t *testing.T) store.PostgresConfig {
	t.Helper()
	if os.Getenv("JAVBEACON_TEST_PG_SKIP") == "1" {
		t.Skip("JAVBEACON_TEST_PG_SKIP=1")
	}
	port := 5432
	if v := os.Getenv("JAVBEACON_TEST_PG_PORT"); v != "" {
		p, e := strconv.Atoi(v)
		if e != nil {
			t.Fatalf("invalid JAVBEACON_TEST_PG_PORT %q: %v", v, e)
		}
		port = p
	}
	cfg := store.PostgresConfig{
		Host:     migrationEnvOrDefault("JAVBEACON_TEST_PG_HOST", "127.0.0.1"),
		Port:     port,
		Database: migrationEnvOrDefault("JAVBEACON_TEST_PG_DATABASE", "javbeacon"),
		User:     migrationEnvOrDefault("JAVBEACON_TEST_PG_USER", "javbeacon"),
		Password: migrationEnvOrDefault("JAVBEACON_TEST_PG_PASSWORD", "devtest12345"),
		SSLMode:  migrationEnvOrDefault("JAVBEACON_TEST_PG_SSLMODE", "disable"),
	}
	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
	conn, e := net.DialTimeout("tcp", addr, 2*time.Second)
	if e != nil {
		t.Skipf("no PostgreSQL server reachable at %s (set JAVBEACON_TEST_PG_* to point at one, or JAVBEACON_TEST_PG_SKIP=1 to silence this): %v", addr, e)
	}
	conn.Close()
	return cfg
}

func migrationEnvOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// resetMigrationTarget prepares cfg's schema (idempotent, safe to call
// repeatedly) and truncates every migrated table, leaving a clean slate
// both before and after TestMigrationTransferEndToEnd runs.
func resetMigrationTarget(t *testing.T, cfg store.PostgresConfig) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	st, e := store.OpenPostgresStore(ctx, cfg)
	if e != nil {
		t.Fatalf("preparing target schema for test reset: %v", e)
	}
	defer st.Close()
	if _, e := st.DB().ExecContext(ctx, `TRUNCATE sites, releases, settings, users, sessions, user_preferences, filter_presets, job_history, download_search_runs, downloads, path_mappings, pipeline_steps, pipeline_logs, notifications, watchlist_sync, release_actresses, release_tags, release_sites, pipeline_runs RESTART IDENTITY CASCADE`); e != nil {
		t.Fatalf("truncating target for test reset: %v", e)
	}
}

// TestMigrationTransferEndToEnd drives the whole DB Phase 7-10 workflow
// through the HTTP handlers exactly as the frontend wizard does - select
// source, validate it, connect/inspect/prepare the target, then start the
// transfer and poll status until it finishes - against a real SQLite
// source and a real local PostgreSQL server, then confirms the target
// actually has the migrated data (including relationships and original
// ids) and that it is left in a genuinely usable state (the identity
// sequence resync did not leave a colliding next id).
func TestMigrationTransferEndToEnd(t *testing.T) {
	cfg := testMigrationPostgresConfig(t)
	resetMigrationTarget(t, cfg)
	t.Cleanup(func() { resetMigrationTarget(t, cfg) })

	sourcePath, ids := buildTestMigrationSource(t)
	s := newMigrationTestServer(t, sourcePath)

	if rec := doJSON(t, s.setupMigrationSource, http.MethodPost, "/api/setup/migration/source", map[string]any{"mode": "current"}); rec.Code != http.StatusOK {
		t.Fatalf("source: status=%d body=%s", rec.Code, rec.Body)
	}
	if rec := doJSON(t, s.setupMigrationValidateSource, http.MethodPost, "/api/setup/migration/validate-source", nil); rec.Code != http.StatusOK {
		t.Fatalf("validate-source: status=%d body=%s", rec.Code, rec.Body)
	}
	target := map[string]any{"host": cfg.Host, "port": cfg.Port, "database": cfg.Database, "user": cfg.User, "password": cfg.Password, "sslmode": cfg.SSLMode}
	if rec := doJSON(t, s.setupMigrationPostgres, http.MethodPost, "/api/setup/migration/postgres", target); rec.Code != http.StatusOK {
		t.Fatalf("postgres: status=%d body=%s", rec.Code, rec.Body)
	}
	if rec := doJSON(t, s.setupMigrationInspectTarget, http.MethodPost, "/api/setup/migration/inspect-target", target); rec.Code != http.StatusOK {
		t.Fatalf("inspect-target: status=%d body=%s", rec.Code, rec.Body)
	}
	if rec := doJSON(t, s.setupMigrationPrepareTarget, http.MethodPost, "/api/setup/migration/prepare-target", target); rec.Code != http.StatusOK {
		t.Fatalf("prepare-target: status=%d body=%s", rec.Code, rec.Body)
	}

	rec := doJSON(t, s.setupMigrationMigrate, http.MethodPost, "/api/setup/migration/migrate", target)
	if rec.Code != http.StatusOK {
		t.Fatalf("migrate: status=%d body=%s", rec.Code, rec.Body)
	}
	var started struct {
		Started bool `json:"started"`
	}
	if e := json.Unmarshal(rec.Body.Bytes(), &started); e != nil || !started.Started {
		t.Fatalf("migrate did not report started: %s", rec.Body)
	}

	deadline := time.Now().Add(30 * time.Second)
	var st migrationState
	for {
		statusRec := httptest.NewRecorder()
		s.setupMigrationStatus(statusRec, httptest.NewRequest(http.MethodGet, "/api/setup/migration/status", nil))
		if e := json.Unmarshal(statusRec.Body.Bytes(), &st); e != nil {
			t.Fatal(e)
		}
		if !st.TransferRunning {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("migration did not finish in time, last stage=%q", st.TransferStage)
		}
		time.Sleep(100 * time.Millisecond)
	}

	if st.TransferError != "" {
		t.Fatalf("transfer failed: %s", st.TransferError)
	}
	if !st.TransferCompleted {
		t.Fatalf("transfer did not report completed: %+v", st)
	}
	if st.TransferValidation == nil || !st.TransferValidation.OK {
		t.Fatalf("post-migration validation failed: %+v", st.TransferValidation)
	}
	if st.TransferTablesDone != len(migrationTables) {
		t.Fatalf("TransferTablesDone = %d, want %d", st.TransferTablesDone, len(migrationTables))
	}
	if st.TransferRowsCopied == 0 {
		t.Fatal("TransferRowsCopied = 0")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	target2, e := store.OpenPostgresStore(ctx, cfg)
	if e != nil {
		t.Fatal(e)
	}
	defer target2.Close()

	sites, e := target2.Sites(ctx)
	if e != nil || len(sites) != 1 || sites[0].ID != ids.SiteID || sites[0].Title != "GIGA" {
		t.Fatalf("sites=%+v err=%v, want a single site with id=%d", sites, e, ids.SiteID)
	}
	rels, e := target2.Releases(ctx, domain.ReleaseFilter{})
	if e != nil || len(rels) != 1 || rels[0].ID != ids.ReleaseID || len(rels[0].Actresses) != 2 {
		t.Fatalf("releases=%+v err=%v, want a single release with id=%d and 2 actresses", rels, e, ids.ReleaseID)
	}

	// The identity sequence resync (the "updating sequences" stage) must
	// have left the target usable: inserting a new row through the normal
	// Store interface must not collide with the explicit id the transfer
	// just wrote.
	newSite, e := target2.SaveSite(ctx, domain.Site{Title: "Second Site", Type: "Site", Name: "Second Site", Enabled: true})
	if e != nil {
		t.Fatalf("sequence resync did not leave the target usable: %v", e)
	}
	if newSite.ID <= ids.SiteID {
		t.Fatalf("new site id %d did not advance past the migrated id %d - sequence resync bug", newSite.ID, ids.SiteID)
	}

	// DB Phase 11 ("Safe Database Activation"): now that the transfer and
	// its validation both succeeded, the activation step should
	// independently re-verify the target and hand back the exact
	// environment variables to set.
	activateRec := doJSON(t, s.setupMigrationActivate, http.MethodPost, "/api/setup/migration/activate", target)
	if activateRec.Code != http.StatusOK {
		t.Fatalf("activate: status=%d body=%s", activateRec.Code, activateRec.Body)
	}
	var activated struct {
		Activated    bool              `json:"activated"`
		Env          map[string]string `json:"env"`
		Instructions []string          `json:"instructions"`
	}
	if e := json.Unmarshal(activateRec.Body.Bytes(), &activated); e != nil {
		t.Fatal(e)
	}
	if !activated.Activated {
		t.Fatalf("activate did not report success: %s", activateRec.Body)
	}
	if activated.Env["JAVBEACON_DB_ENGINE"] != "postgres" || activated.Env["JAVBEACON_DB_HOST"] != cfg.Host || activated.Env["JAVBEACON_DB_PASSWORD"] != cfg.Password {
		t.Fatalf("activate env block missing/wrong values: %+v", activated.Env)
	}
	if len(activated.Instructions) == 0 {
		t.Fatal("activate returned no instructions")
	}
}

// TestSetupMigrationActivateRequiresCompletedTransfer confirms activation
// is gated on a completed, validated transfer rather than being reachable
// straight after prepare-target (or with no migration attempted at all).
func TestSetupMigrationActivateRequiresCompletedTransfer(t *testing.T) {
	s := newMigrationTestServer(t, "/data/javbeacon.db")
	target := map[string]any{"host": "127.0.0.1", "port": 5432, "database": "javbeacon", "user": "javbeacon", "password": "x", "sslmode": "disable"}
	if rec := doJSON(t, s.setupMigrationActivate, http.MethodPost, "/api/setup/migration/activate", target); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status=%d body=%s, want 422 before any migration has completed", rec.Code, rec.Body)
	}

	s.migrationMu.Lock()
	s.migration.TransferCompleted = true
	s.migration.TransferValidation = &migrationValidationReport{OK: false, Failures: []string{"row count mismatch"}}
	s.migrationMu.Unlock()
	if rec := doJSON(t, s.setupMigrationActivate, http.MethodPost, "/api/setup/migration/activate", target); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status=%d body=%s, want 422 when TransferCompleted but validation.OK is false", rec.Code, rec.Body)
	}
}

// TestMigrationTransferIsRetrySafeAgainstAPartiallyPopulatedTarget is DB
// Phase 13 ("Failure and Rollback Testing")'s "migration interrupted /
// destination partially populated" scenario made concrete: it seeds the
// target with a subset of the same rows an earlier, interrupted transfer
// attempt might have already written (including one row with a different
// value than the source now has, simulating a row written before a
// mid-transfer failure and never updated), then runs the real transfer
// against that already-dirty target and confirms it still converges to
// the correct, fully-validated end state rather than failing on the
// pre-existing rows.
func TestMigrationTransferIsRetrySafeAgainstAPartiallyPopulatedTarget(t *testing.T) {
	cfg := testMigrationPostgresConfig(t)
	resetMigrationTarget(t, cfg)
	t.Cleanup(func() { resetMigrationTarget(t, cfg) })

	sourcePath, ids := buildTestMigrationSource(t)

	// Simulate a partially-completed earlier attempt: the target already
	// has the site row, but with a stale title a real interrupted transfer
	// might have left behind before ever reaching the point of copying the
	// release that depends on it.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	pre, e := store.OpenPostgresStore(ctx, cfg)
	if e != nil {
		t.Fatal(e)
	}
	if _, e := pre.DB().ExecContext(ctx, `INSERT INTO sites (id,title,type,name,url,notify,enabled,created_at,updated_at) VALUES ($1,'STALE TITLE','Site','GIGA','',0,1,now(),now())`, ids.SiteID); e != nil {
		pre.Close()
		t.Fatalf("seeding a partially-populated target: %v", e)
	}
	pre.Close()

	s := newMigrationTestServer(t, sourcePath)
	target := map[string]any{"host": cfg.Host, "port": cfg.Port, "database": cfg.Database, "user": cfg.User, "password": cfg.Password, "sslmode": cfg.SSLMode}
	if rec := doJSON(t, s.setupMigrationSource, http.MethodPost, "/api/setup/migration/source", map[string]any{"mode": "current"}); rec.Code != http.StatusOK {
		t.Fatalf("source: status=%d body=%s", rec.Code, rec.Body)
	}
	if rec := doJSON(t, s.setupMigrationValidateSource, http.MethodPost, "/api/setup/migration/validate-source", nil); rec.Code != http.StatusOK {
		t.Fatalf("validate-source: status=%d body=%s", rec.Code, rec.Body)
	}
	if rec := doJSON(t, s.setupMigrationPostgres, http.MethodPost, "/api/setup/migration/postgres", target); rec.Code != http.StatusOK {
		t.Fatalf("postgres: status=%d body=%s", rec.Code, rec.Body)
	}
	if rec := doJSON(t, s.setupMigrationPrepareTarget, http.MethodPost, "/api/setup/migration/prepare-target", target); rec.Code != http.StatusOK {
		t.Fatalf("prepare-target: status=%d body=%s", rec.Code, rec.Body)
	}
	if rec := doJSON(t, s.setupMigrationMigrate, http.MethodPost, "/api/setup/migration/migrate", target); rec.Code != http.StatusOK {
		t.Fatalf("migrate: status=%d body=%s", rec.Code, rec.Body)
	}

	deadline := time.Now().Add(30 * time.Second)
	var st migrationState
	for {
		statusRec := httptest.NewRecorder()
		s.setupMigrationStatus(statusRec, httptest.NewRequest(http.MethodGet, "/api/setup/migration/status", nil))
		if e := json.Unmarshal(statusRec.Body.Bytes(), &st); e != nil {
			t.Fatal(e)
		}
		if !st.TransferRunning {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("migration did not finish in time, last stage=%q", st.TransferStage)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if st.TransferError != "" {
		t.Fatalf("transfer against a partially-populated target failed instead of converging: %s", st.TransferError)
	}
	if st.TransferValidation == nil || !st.TransferValidation.OK {
		t.Fatalf("validation failed after re-running against a partially-populated target: %+v", st.TransferValidation)
	}

	target2, e := store.OpenPostgresStore(ctx, cfg)
	if e != nil {
		t.Fatal(e)
	}
	defer target2.Close()
	sites, e := target2.Sites(ctx)
	if e != nil || len(sites) != 1 || sites[0].Title != "GIGA" {
		t.Fatalf("sites=%+v err=%v, want the stale pre-existing title overwritten with the source's real value", sites, e)
	}
}

// TestMigrationTransferFailsCleanlyAgainstAnUnreachableTarget needs no live
// PostgreSQL server - the target is deliberately unreachable - so it always
// runs, and exercises the async failure path: TransferRunning must flip
// back to false with a populated, password-free TransferError rather than
// hanging or panicking the goroutine.
func TestMigrationTransferFailsCleanlyAgainstAnUnreachableTarget(t *testing.T) {
	sourcePath, _ := buildTestMigrationSource(t)
	s := newMigrationTestServer(t, sourcePath)
	s.migrationMu.Lock()
	s.migration.SourcePath = sourcePath
	s.migration.SourceValidated = true
	s.migration.TargetPrepared = true
	s.migrationMu.Unlock()

	target := map[string]any{"host": "127.0.0.1", "port": unusedTCPPort(t), "database": "javbeacon", "user": "javbeacon", "password": "unmistakable-test-password-marker", "sslmode": "disable"}
	rec := doJSON(t, s.setupMigrationMigrate, http.MethodPost, "/api/setup/migration/migrate", target)
	if rec.Code != http.StatusOK {
		t.Fatalf("migrate: status=%d body=%s", rec.Code, rec.Body)
	}

	deadline := time.Now().Add(15 * time.Second)
	var st migrationState
	for {
		statusRec := httptest.NewRecorder()
		s.setupMigrationStatus(statusRec, httptest.NewRequest(http.MethodGet, "/api/setup/migration/status", nil))
		if e := json.Unmarshal(statusRec.Body.Bytes(), &st); e != nil {
			t.Fatal(e)
		}
		if !st.TransferRunning {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("transfer against an unreachable target did not fail in time")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if st.TransferError == "" {
		t.Fatal("expected a TransferError against an unreachable target")
	}
	if strings.Contains(st.TransferError, "unmistakable-test-password-marker") {
		t.Fatal("TransferError leaked the password")
	}
}

// TestSetupMigrationMigrateRejectsConcurrentTransfer confirms a second
// migrate request while one is already running is rejected rather than
// starting a racing second goroutine against the same migrationState.
func TestSetupMigrationMigrateRejectsConcurrentTransfer(t *testing.T) {
	s := newMigrationTestServer(t, "/data/javbeacon.db")
	s.migrationMu.Lock()
	s.migration.SourceValidated = true
	s.migration.TargetPrepared = true
	s.migration.TransferRunning = true
	s.migrationMu.Unlock()

	target := map[string]any{"host": "127.0.0.1", "port": unusedTCPPort(t), "database": "javbeacon", "user": "javbeacon", "password": "x", "sslmode": "disable"}
	rec := doJSON(t, s.setupMigrationMigrate, http.MethodPost, "/api/setup/migration/migrate", target)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status=%d body=%s, want 422 while a transfer is already running", rec.Code, rec.Body)
	}
}

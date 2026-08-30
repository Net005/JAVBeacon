package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/Net005/JAVBeacon/internal/config"
	"github.com/Net005/JAVBeacon/internal/logging"
	"github.com/Net005/JAVBeacon/internal/store"
)

// This file is DB Phase 12's ("PostgreSQL Connection Recovery") test
// coverage, plus one DB Phase 13 ("Failure and Rollback Testing") scenario
// specific to this package - "PostgreSQL unavailable at startup" and
// "application restarted after failed migration" both land here, since
// they are about what New/Run do at process startup, not about the
// migration transfer itself (covered by internal/web/migration_test.go).

func testLogger() *slog.Logger {
	return slog.New(logging.NewRing(slog.NewTextHandler(io.Discard, nil), 100))
}

func unusedTCPAddr(t *testing.T) string {
	t.Helper()
	l, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	addr := l.Addr().String()
	l.Close()
	return addr
}

// setPostgresEnv sets every environment variable config.Load requires for
// JAVBEACON_DB_ENGINE=postgres, pointed at a host:port nothing listens
// on, so config.Load itself succeeds but the resulting connection attempt
// fails. t.Setenv unsets everything automatically at test end.
func setPostgresEnv(t *testing.T, host string, port int) {
	t.Helper()
	t.Setenv("JAVBEACON_DB_ENGINE", "postgres")
	t.Setenv("JAVBEACON_DB_HOST", host)
	t.Setenv("JAVBEACON_DB_PORT", strconv.Itoa(port))
	t.Setenv("JAVBEACON_DB_NAME", "javbeacon")
	t.Setenv("JAVBEACON_DB_USER", "javbeacon")
	t.Setenv("JAVBEACON_DB_PASSWORD", "unmistakable-test-password-marker")
	t.Setenv("JAVBEACON_DB_SSLMODE", "disable")
}

// TestNewEntersRecoveryModeWhenPostgresUnreachableAtStartup is DB Phase
// 12's core behavior change: New must not return an error just because
// the configured PostgreSQL server is unreachable - it must hand back a
// degraded App (store == nil) for Run to serve a recovery page from,
// instead of main.go's os.Exit(1) crash-looping the process.
func TestNewEntersRecoveryModeWhenPostgresUnreachableAtStartup(t *testing.T) {
	_, portStr, _ := net.SplitHostPort(unusedTCPAddr(t))
	port, e := strconv.Atoi(portStr)
	if e != nil {
		t.Fatal(e)
	}
	setPostgresEnv(t, "127.0.0.1", port)
	t.Setenv("JAVBEACON_LISTEN", unusedTCPAddr(t))

	// New's startup grace period (see its doc comment) would otherwise make
	// this test actually wait out the full window against a server that is
	// never going to answer - shrink it to zero so a single failed attempt
	// still enters recovery mode immediately, matching this test's intent.
	origWindow, origInterval := postgresStartupGraceWindow, postgresStartupRetryInterval
	postgresStartupGraceWindow, postgresStartupRetryInterval = 0, time.Millisecond
	t.Cleanup(func() { postgresStartupGraceWindow, postgresStartupRetryInterval = origWindow, origInterval })

	logger := testLogger()
	a, e := New(logger, logging.NewRing(slog.NewTextHandler(io.Discard, nil), 100))
	if e != nil {
		t.Fatalf("New returned an error instead of entering recovery mode: %v", e)
	}
	if a == nil {
		t.Fatal("New returned a nil App")
	}
	if a.store != nil {
		t.Fatal("App.store should be nil while PostgreSQL is unreachable (degraded/recovery mode)")
	}
	if a.recoveryErr == nil {
		t.Fatal("App.recoveryErr should record why the connection failed")
	}
}

// TestOpenStoreWithGraceRetriesUntilWindowElapses covers New's startup
// grace period in isolation (no real PostgreSQL server, no dependence on
// wall-clock precision beyond a generous margin): a store that never opens
// successfully must be retried more than once, spaced roughly interval
// apart, and only reported as failed once window has actually elapsed -
// not on the very first failure, which is the whole point of the grace
// period (a stack restart racing PostgreSQL's own startup).
func TestOpenStoreWithGraceRetriesUntilWindowElapses(t *testing.T) {
	var attempts int
	open := func() (*store.SQLite, error) {
		attempts++
		return nil, errors.New("still down")
	}
	var warnCalls int
	warn := func(string, ...any) { warnCalls++ }

	start := time.Now()
	_, err := openStoreWithGrace(open, 30*time.Millisecond, 10*time.Millisecond, warn)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected an error once the grace window elapses with the store still unreachable")
	}
	if attempts < 3 {
		t.Fatalf("expected at least 3 attempts within a 30ms window at a 10ms interval, got %d", attempts)
	}
	if elapsed < 20*time.Millisecond {
		t.Fatalf("returned after only %v - the grace window was not honored", elapsed)
	}
	if warnCalls == 0 {
		t.Fatal("expected at least one Warn-level retry log before giving up")
	}
}

// TestOpenStoreWithGraceSucceedsPartwayThroughWindow covers the actual
// point of the grace period: a store that starts failing but succeeds
// before the window elapses must be returned as a success, with no error
// at all - proving a transient startup race (not a real outage) never
// reaches New's Error log or the recovery page.
func TestOpenStoreWithGraceSucceedsPartwayThroughWindow(t *testing.T) {
	sentinel := &store.SQLite{}
	var attempts int
	open := func() (*store.SQLite, error) {
		attempts++
		if attempts < 3 {
			return nil, errors.New("not ready yet")
		}
		return sentinel, nil
	}

	st, err := openStoreWithGrace(open, time.Second, 5*time.Millisecond, func(string, ...any) {})
	if err != nil {
		t.Fatalf("expected success once open succeeds within the window, got error: %v", err)
	}
	if st != sentinel {
		t.Fatalf("expected the successful store to be returned, got %+v", st)
	}
	if attempts != 3 {
		t.Fatalf("expected exactly 3 attempts (stopping at the first success), got %d", attempts)
	}
}

// TestOpenStoreWithGraceSkipsRetryLoopWhenWindowIsZero covers the SQLite
// engine's path through New, and this test's own default state before
// TestNewEntersRecoveryModeWhenPostgresUnreachableAtStartup overrides the
// package vars: window<=0 must behave exactly like calling open() once,
// with no sleep and no retry, so a SQLite open failure still fails startup
// immediately rather than picking up a PostgreSQL-only grace period.
func TestOpenStoreWithGraceSkipsRetryLoopWhenWindowIsZero(t *testing.T) {
	var attempts int
	open := func() (*store.SQLite, error) {
		attempts++
		return nil, errors.New("boom")
	}
	start := time.Now()
	_, err := openStoreWithGrace(open, 0, time.Hour, func(string, ...any) { t.Fatal("warn should never be called when window<=0") })
	if err == nil {
		t.Fatal("expected the single attempt's error to be returned")
	}
	if attempts != 1 {
		t.Fatalf("expected exactly 1 attempt when window<=0, got %d", attempts)
	}
	if time.Since(start) > 100*time.Millisecond {
		t.Fatal("window<=0 should return immediately, not sleep")
	}
}

func mustAtoi(t *testing.T, s string) int {
	t.Helper()
	n, e := strconv.Atoi(s)
	if e != nil {
		t.Fatalf("not a port number: %q: %v", s, e)
	}
	return n
}

// TestNewStillFailsHardForASQLiteStartupError is the regression guard for
// DB Phase 12's scope: it is specifically "PostgreSQL Connection
// Recovery" - a SQLite open failure (a filesystem problem) must keep
// failing exactly as it always has, not silently enter a recovery mode
// that was never built for it.
func TestNewStillFailsHardForASQLiteStartupError(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "not-a-directory")
	if e := os.WriteFile(blocker, []byte("x"), 0644); e != nil {
		t.Fatal(e)
	}
	t.Setenv("JAVBEACON_DB", filepath.Join(blocker, "sub", "javbeacon.db"))

	if _, e := New(testLogger(), logging.NewRing(slog.NewTextHandler(io.Discard, nil), 100)); e == nil {
		t.Fatal("expected New to fail hard for a SQLite open error, not enter recovery mode")
	}
}

// TestRecoveryServerHandlers exercises the recovery mux's HTTP surface in
// isolation (no goroutines/timers involved): the status endpoint reports
// the classified last error without leaking the password, the page itself
// renders without panicking, and the test-connection endpoint reuses the
// same store.OpenPostgres/ClassifyPostgresError path as the setup wizard
// against a deliberately unreachable target.
func TestRecoveryServerHandlers(t *testing.T) {
	cfg := config.Config{DatabaseEngine: config.EnginePostgres, PostgresHost: "127.0.0.1", PostgresPort: 5432, PostgresDatabase: "javbeacon", PostgresUser: "javbeacon", PostgresSSLMode: "disable"}
	rec := newRecoveryServer(cfg, errors.New("connect to postgres: connection refused"))
	mux := rec.mux(func(string, ...any) {})

	statusRec := httptest.NewRecorder()
	mux.ServeHTTP(statusRec, httptest.NewRequest(http.MethodGet, "/api/recovery/status", nil))
	if statusRec.Code != http.StatusOK {
		t.Fatalf("status: code=%d body=%s", statusRec.Code, statusRec.Body)
	}
	var status recoveryStatusPayload
	if e := json.Unmarshal(statusRec.Body.Bytes(), &status); e != nil {
		t.Fatal(e)
	}
	if !status.Recovering || status.Message == "" {
		t.Fatalf("unexpected status payload: %+v", status)
	}

	pageRec := httptest.NewRecorder()
	mux.ServeHTTP(pageRec, httptest.NewRequest(http.MethodGet, "/", nil))
	if pageRec.Code != http.StatusServiceUnavailable {
		t.Fatalf("page: code=%d", pageRec.Code)
	}
	if !bytes.Contains(pageRec.Body.Bytes(), []byte("JAVBeacon can't reach its PostgreSQL database")) {
		t.Fatal("recovery page did not render the expected heading")
	}

	_, portStr, _ := net.SplitHostPort(unusedTCPAddr(t))
	testBody, _ := json.Marshal(map[string]any{"host": "127.0.0.1", "port": mustAtoi(t, portStr), "database": "javbeacon", "user": "javbeacon", "password": "unmistakable-test-password-marker", "sslmode": "disable"})
	testRec := httptest.NewRecorder()
	mux.ServeHTTP(testRec, httptest.NewRequest(http.MethodPost, "/api/recovery/test", bytes.NewReader(testBody)))
	if testRec.Code != http.StatusOK {
		t.Fatalf("test: code=%d body=%s", testRec.Code, testRec.Body)
	}
	var testResult struct {
		Connected bool   `json:"connected"`
		Message   string `json:"message"`
	}
	if e := json.Unmarshal(testRec.Body.Bytes(), &testResult); e != nil {
		t.Fatal(e)
	}
	if testResult.Connected {
		t.Fatal("expected the diagnostic test to fail against an unreachable port")
	}
	if bytes.Contains(testRec.Body.Bytes(), []byte("unmistakable-test-password-marker")) {
		t.Fatal("recovery test-connection response leaked the password")
	}

	retryRec := httptest.NewRecorder()
	mux.ServeHTTP(retryRec, httptest.NewRequest(http.MethodPost, "/api/recovery/retry", nil))
	if retryRec.Code != http.StatusOK {
		t.Fatalf("retry: code=%d body=%s", retryRec.Code, retryRec.Body)
	}
	select {
	case <-rec.retryNow:
	default:
		t.Fatal("POST /api/recovery/retry did not queue a retry")
	}
}

// testReachablePostgresEnv mirrors internal/web/migration_test.go's
// testMigrationPostgresConfig (unexported there, so not reusable
// directly): it dials the configured/default local test server first and
// calls t.Skip, rather than t.Fatal, when nothing answers, so this
// package's suite still passes unmodified without a live PostgreSQL
// server available, and sets the matching JAVBEACON_DB_* environment
// variables via t.Setenv for the caller.
func testReachablePostgresEnv(t *testing.T) {
	t.Helper()
	if os.Getenv("JAVBEACON_TEST_PG_SKIP") == "1" {
		t.Skip("JAVBEACON_TEST_PG_SKIP=1")
	}
	host := envOrDefault("JAVBEACON_TEST_PG_HOST", "127.0.0.1")
	port := envOrDefault("JAVBEACON_TEST_PG_PORT", "5432")
	addr := net.JoinHostPort(host, port)
	conn, e := net.DialTimeout("tcp", addr, 2*time.Second)
	if e != nil {
		t.Skipf("no PostgreSQL server reachable at %s (set JAVBEACON_TEST_PG_* to point at one, or JAVBEACON_TEST_PG_SKIP=1 to silence this): %v", addr, e)
	}
	conn.Close()

	t.Setenv("JAVBEACON_DB_ENGINE", "postgres")
	t.Setenv("JAVBEACON_DB_HOST", host)
	t.Setenv("JAVBEACON_DB_PORT", port)
	t.Setenv("JAVBEACON_DB_NAME", envOrDefault("JAVBEACON_TEST_PG_DATABASE", "javbeacon"))
	t.Setenv("JAVBEACON_DB_USER", envOrDefault("JAVBEACON_TEST_PG_USER", "javbeacon"))
	t.Setenv("JAVBEACON_DB_PASSWORD", envOrDefault("JAVBEACON_TEST_PG_PASSWORD", "devtest12345"))
	t.Setenv("JAVBEACON_DB_SSLMODE", envOrDefault("JAVBEACON_TEST_PG_SSLMODE", "disable"))
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// TestAwaitRecoveryTransitionsToNormalOperationOnSuccessfulRetry is DB
// Phase 12's "return to normal operation once connectivity is restored"
// requirement, proven end to end against a real PostgreSQL server rather
// than only asserted structurally: an App is put into recovery mode by
// hand (store == nil) even though its cfg actually points at a reachable
// server - a click on "Retry now" (POST /api/recovery/retry) must make
// Run transition from serving the recovery page to serving the real
// application, on the same listen address, without a process restart.
func TestAwaitRecoveryTransitionsToNormalOperationOnSuccessfulRetry(t *testing.T) {
	testReachablePostgresEnv(t)
	listenAddr := unusedTCPAddr(t)
	t.Setenv("JAVBEACON_LISTEN", listenAddr)

	cfg, e := config.Load()
	if e != nil {
		t.Fatal(e)
	}

	// This test runs the real startup bootstrap (finishStartup) against
	// the shared dev Postgres server that internal/store's and
	// internal/web's own Postgres integration tests also default to -
	// without this, the "sites" row finishStartup creates (title
	// "GIGA", enforced unique) would persist after the test and can
	// collide with those other packages' tests when run concurrently
	// via `go test ./...`. Truncate everything finishStartup's bootstrap
	// can touch afterward, mirroring postgres_store_test.go's own
	// t.Cleanup pattern.
	t.Cleanup(func() {
		db, e := store.OpenPostgres(context.Background(), store.PostgresConfig{
			Host: cfg.PostgresHost, Port: cfg.PostgresPort, Database: cfg.PostgresDatabase,
			User: cfg.PostgresUser, Password: cfg.PostgresPassword, SSLMode: cfg.PostgresSSLMode,
		})
		if e != nil {
			t.Logf("cleanup: could not reconnect to truncate test data: %v", e)
			return
		}
		defer db.Close()
		if _, e := db.ExecContext(context.Background(), `TRUNCATE sites, releases, settings, users, sessions, user_preferences, filter_presets, job_history, download_search_runs, downloads, path_mappings, pipeline_steps, pipeline_logs, notifications, watchlist_sync, release_actresses, release_tags, release_sites, pipeline_runs RESTART IDENTITY CASCADE`); e != nil {
			t.Logf("cleanup: truncate failed: %v", e)
		}
	})

	a := &App{cfg: cfg, log: testLogger(), recoveryLogs: logging.NewRing(slog.NewTextHandler(io.Discard, nil), 100), recoveryErr: errors.New("simulated: not attempted yet")}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runErr := make(chan error, 1)
	go func() { runErr <- a.Run(ctx) }()

	statusURL := "http://" + listenAddr + "/api/recovery/status"
	deadline := time.Now().Add(5 * time.Second)
	for {
		if resp, e := http.Get(statusURL); e == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("recovery status endpoint never became reachable")
		}
		time.Sleep(50 * time.Millisecond)
	}

	resp, e := http.Post("http://"+listenAddr+"/api/recovery/retry", "application/json", nil)
	if e != nil {
		t.Fatal(e)
	}
	resp.Body.Close()

	// Once finishStartup takes over, the process is serving the real
	// webapp mux rather than the recovery mux - and unlike the recovery
	// server's own unauthenticated /api/health, the real app's
	// /api/health sits behind its normal session-auth middleware (see
	// internal/web/server.go's security wrapper), so an unauthenticated
	// caller there gets 401, not "status":"ok". An unauthenticated
	// GET / is a signal both sides agree on either way: the recovery
	// mux serves the recovery page (200) for it, while the real app's
	// auth middleware redirects it to /login (303) since "/" isn't one
	// of its public paths - so that redirect is what proves the
	// transition happened.
	rootURL := "http://" + listenAddr + "/"
	noRedirect := &http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error { return http.ErrUseLastResponse }}
	deadline = time.Now().Add(15 * time.Second)
	var lastStatus int
	var lastLocation string
	for time.Now().Before(deadline) {
		resp, e := noRedirect.Get(rootURL)
		if e == nil {
			lastStatus = resp.StatusCode
			lastLocation = resp.Header.Get("Location")
			resp.Body.Close()
			if lastStatus == http.StatusSeeOther && lastLocation == "/login" {
				goto recovered
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("app did not transition to normal operation after a successful retry; last GET / status=%d location=%q", lastStatus, lastLocation)

recovered:
	cancel()
	select {
	case e := <-runErr:
		if e != nil {
			t.Fatalf("Run returned an error on shutdown after recovering: %v", e)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return within 5s of context cancellation after recovering")
	}
}

// TestAwaitRecoveryServesAndShutsDownCleanly runs the real Run() loop
// end to end against an unreachable PostgreSQL server: it confirms the
// recovery page is actually reachable over HTTP on the configured listen
// address, and that canceling the context stops it cleanly (no panic,
// Run returns promptly) rather than hanging - DB Phase 13's "connection
// lost during migration/startup, application should remain recoverable"
// requirement applied to the recovery server's own lifecycle.
func TestAwaitRecoveryServesAndShutsDownCleanly(t *testing.T) {
	_, portStr, _ := net.SplitHostPort(unusedTCPAddr(t))
	setPostgresEnv(t, "127.0.0.1", mustAtoi(t, portStr))
	listenAddr := unusedTCPAddr(t)
	t.Setenv("JAVBEACON_LISTEN", listenAddr)

	a, e := New(testLogger(), logging.NewRing(slog.NewTextHandler(io.Discard, nil), 100))
	if e != nil {
		t.Fatalf("New: %v", e)
	}
	if a.store != nil {
		t.Fatal("expected recovery mode (store == nil)")
	}

	ctx, cancel := context.WithCancel(context.Background())
	runErr := make(chan error, 1)
	go func() { runErr <- a.Run(ctx) }()

	url := "http://" + listenAddr + "/api/recovery/status"
	deadline := time.Now().Add(5 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		resp, e := http.Get(url)
		if e == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				lastErr = nil
				break
			}
		}
		lastErr = e
		time.Sleep(50 * time.Millisecond)
	}
	if lastErr != nil {
		t.Fatalf("recovery status endpoint never became reachable at %s: %v", url, lastErr)
	}

	cancel()
	select {
	case e := <-runErr:
		if e != nil {
			t.Fatalf("Run returned an error on shutdown: %v", e)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return within 5s of context cancellation")
	}
}

package store

import (
	"context"
	"net"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/Net005/JAVBeacon/internal/domain"
)

// testPostgresConfig returns the PostgreSQL connection settings for this
// file's integration test, overridable via JAVBEACON_TEST_PG_* so it can
// point at whatever server is available. The defaults match a throwaway
// local dev/test database (see docs/OPERATIONS.md's DB Phase 5-6 section for
// how to stand one up).
//
// Most environments - including this repository's own CI by default - do
// not have a PostgreSQL server listening, so this dials the target
// host:port first and calls t.Skip rather than t.Fatal when nothing answers,
// exactly like the rest of this package's suite still running unmodified
// against SQLite. Set JAVBEACON_TEST_PG_SKIP=1 to force-skip regardless
// of reachability (e.g. to avoid the dial's timeout in a sandboxed CI run
// that has no network access at all).
func testPostgresConfig(t *testing.T) PostgresConfig {
	t.Helper()

	if os.Getenv("JAVBEACON_TEST_PG_SKIP") == "1" {
		t.Skip("JAVBEACON_TEST_PG_SKIP=1")
	}

	port := 5432
	if v := os.Getenv("JAVBEACON_TEST_PG_PORT"); v != "" {
		p, err := strconv.Atoi(v)
		if err != nil {
			t.Fatalf("invalid JAVBEACON_TEST_PG_PORT %q: %v", v, err)
		}
		port = p
	}
	cfg := PostgresConfig{
		Host:     envOrDefault("JAVBEACON_TEST_PG_HOST", "127.0.0.1"),
		Port:     port,
		Database: envOrDefault("JAVBEACON_TEST_PG_DATABASE", "javbeacon"),
		User:     envOrDefault("JAVBEACON_TEST_PG_USER", "javbeacon"),
		Password: envOrDefault("JAVBEACON_TEST_PG_PASSWORD", "devtest12345"),
		SSLMode:  envOrDefault("JAVBEACON_TEST_PG_SSLMODE", "disable"),
	}

	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Skipf("no PostgreSQL server reachable at %s (set JAVBEACON_TEST_PG_* to point at one, or JAVBEACON_TEST_PG_SKIP=1 to silence this): %v", addr, err)
	}
	conn.Close()
	return cfg
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// TestPostgresStoreFullLifecycle exercises OpenPostgresStore end to end
// against a real PostgreSQL server, covering the DB Phase 6 compatibility
// audit checklist: releases with their actress/tag/site relationships,
// filtering/search, upsert-not-duplicate, settings/users/sessions/
// preferences/filter presets, job history, downloads and download activity,
// path mappings, the pipeline steps/run/log tables, notifications, watchlist
// sync, and site-level monitoring recompute. This is the PostgreSQL
// counterpart to the SQLite coverage in store_test.go - it does not
// duplicate that file's assertions field-by-field, just confirms every
// Store method used by the application executes successfully (and returns
// sane results) against PostgreSQL, since that is exactly what DB Phase 5's
// query-rewriting seam (postgres_rewrite.go) and DB Phase 6's dialect
// additions (Dialect.Greatest/Least/JSONArrayAgg/BoolExprToInt) exist to
// guarantee.
func TestPostgresStoreFullLifecycle(t *testing.T) {
	cfg := testPostgresConfig(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	s, err := OpenPostgresStore(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	// t.Cleanup, not defer s.Close(): regular defers in this function run
	// before t.Cleanup callbacks, so a separate "defer s.Close()" here would
	// close the connection before this truncate ever reached the server,
	// leaving it dirty for the next run. Truncate first, then close, both
	// in this one callback, so the ordering is explicit.
	t.Cleanup(func() {
		s.db.ExecContext(context.Background(), `TRUNCATE sites, releases, settings, users, sessions, user_preferences, filter_presets, job_history, download_search_runs, downloads, path_mappings, pipeline_steps, pipeline_logs, notifications, watchlist_sync, release_actresses, release_tags, release_sites, pipeline_runs RESTART IDENTITY CASCADE`)
		s.Close()
	})

	// Re-open against the same (already-migrated) database to confirm
	// migratePostgres is idempotent, including its "already complete" fast
	// path (DB Phase 5 found and fixed two bugs specific to this path: a
	// multi-statement Exec call PostgreSQL's extended protocol rejects, and
	// the query-rewriter misplacing ON CONFLICT DO NOTHING across it).
	s2, err := OpenPostgresStore(ctx, cfg)
	if err != nil {
		t.Fatalf("second open (idempotent migrate) failed: %v", err)
	}
	s2.Close()

	site, err := s.SaveSite(ctx, domain.Site{Title: "GIGA", Type: "Site", Name: "GIGA", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	created, err := s.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "SPSF-52", Title: "Test release", Source: "GIGA", Released: true, Actress: "Neo Akari, Yua Mikami", Genres: []string{"Drama"}, Screenshots: []string{"https://example.test/1.jpg"}})
	if err != nil || !created {
		t.Fatalf("created=%v err=%v", created, err)
	}
	items, err := s.Releases(ctx, domain.ReleaseFilter{Search: "SPSF", Status: "released"})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("items=%d", len(items))
	}
	if len(items[0].Actresses) != 2 || items[0].SiteTitles[0] != "GIGA" {
		t.Fatalf("JSONArrayAgg not decoded right: %+v", items[0])
	}
	local := true
	if err := s.PatchRelease(ctx, items[0].ID, nil, &local, nil, nil, nil, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	stats, err := s.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Sites != 1 || stats.Releases != 1 || stats.Released != 1 || stats.Local != 1 {
		t.Fatalf("unexpected stats: %+v", stats)
	}

	// Second upsert of the same identity should update, not duplicate -
	// this is what exercises Dialect.Greatest (UpsertRelease's UPDATE
	// branch) and BoolExprToInt (the site_monitor_download recompute).
	created2, err := s.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "SPSF-52", Title: "Updated title", Source: "GIGA"})
	if err != nil || created2 {
		t.Fatalf("created2=%v err=%v", created2, err)
	}
	count, err := s.ReleasesCount(ctx, domain.ReleaseFilter{})
	if err != nil || count != 1 {
		t.Fatalf("count=%d err=%v", count, err)
	}

	// settings / user / session / preferences / filter presets
	if err := s.SaveSettings(ctx, map[string]string{"a": "1", "b": "2"}); err != nil {
		t.Fatal(err)
	}
	settings, err := s.Settings(ctx)
	if err != nil || settings["a"] != "1" {
		t.Fatalf("settings=%v err=%v", settings, err)
	}
	if err := s.SaveUser(ctx, "admin", "hash"); err != nil {
		t.Fatal(err)
	}
	u, err := s.User(ctx)
	if err != nil || u.Username != "admin" {
		t.Fatalf("user=%v err=%v", u, err)
	}
	if err := s.CreateSession(ctx, domain.Session{Token: "tok1", UserID: u.ID, ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	sess, err := s.Session(ctx, "tok1")
	if err != nil || sess.Token != "tok1" {
		t.Fatalf("session=%v err=%v", sess, err)
	}
	if err := s.SavePreferences(ctx, []byte(`{"x":1}`)); err != nil {
		t.Fatal(err)
	}
	fp, err := s.SaveFilterPreset(ctx, domain.FilterPreset{Name: "preset1", State: []byte(`{}`)})
	if err != nil || fp.ID == 0 {
		t.Fatalf("fp=%v err=%v", fp, err)
	}

	// jobs / downloads / path mappings / pipeline
	jobID, err := s.SaveJob(ctx, domain.Job{Kind: "scrape", SiteTitle: "GIGA"})
	if err != nil || jobID == 0 {
		t.Fatalf("jobID=%d err=%v", jobID, err)
	}
	dl, err := s.SaveDownload(ctx, domain.Download{ReleaseID: items[0].ID, Status: "downloading", Files: []byte(`[]`)})
	if err != nil || dl.ID == 0 {
		t.Fatalf("dl=%v err=%v", dl, err)
	}
	activity, total, err := s.DownloadActivity(ctx, domain.DownloadFilter{})
	if err != nil || total != 1 || len(activity) != 1 {
		t.Fatalf("activity=%v total=%d err=%v", activity, total, err)
	}
	pm, err := s.SavePathMapping(ctx, domain.PathMapping{DownloadPrefix: "/downloads", LocalPrefix: "/local"})
	if err != nil || pm.ID == 0 {
		t.Fatalf("pm=%v err=%v", pm, err)
	}
	if err := s.SavePipelineSteps(ctx, []domain.PipelineStep{{Type: "shell", Name: "step1", Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	steps, err := s.PipelineSteps(ctx)
	if err != nil || len(steps) != 1 {
		t.Fatalf("steps=%v err=%v", steps, err)
	}
	if err := s.SavePipelineRun(ctx, domain.PipelineRun{DownloadID: dl.ID, Trigger: "download_completed", State: "completed"}); err != nil {
		t.Fatal(err)
	}
	run, err := s.PipelineRun(ctx, dl.ID, "download_completed")
	if err != nil || run.State != "completed" {
		t.Fatalf("run=%v err=%v", run, err)
	}
	plog, err := s.SavePipelineLog(ctx, domain.PipelineLog{DownloadID: dl.ID, StepID: steps[0].ID, State: "running"})
	if err != nil || plog.ID == 0 {
		t.Fatalf("plog=%v err=%v", plog, err)
	}
	logs, err := s.PipelineLogs(ctx, dl.ID)
	if err != nil || len(logs) != 1 {
		t.Fatalf("logs=%v err=%v", logs, err)
	}

	// notifications / watchlist sync
	created3, err := s.CreateNotification(ctx, items[0].ID, "download_started", "started")
	if err != nil || !created3 {
		t.Fatalf("created3=%v err=%v", created3, err)
	}
	notifications, err := s.Notifications(ctx, "download_started")
	if err != nil || len(notifications) != 1 || notifications[0].Release == nil {
		t.Fatalf("notifications=%v err=%v", notifications, err)
	}
	if err := s.SaveWatchlistSync(ctx, items[0].ID, "scene1", "tag1", "ok"); err != nil {
		t.Fatal(err)
	}
	synced, err := s.WatchlistSynced(ctx, items[0].ID, "scene1", "tag1")
	if err != nil || !synced {
		t.Fatalf("synced=%v err=%v", synced, err)
	}

	// site monitoring / stash state - exercises BoolExprToInt a second way
	// (SetSiteReleaseMonitoring's release-level recompute) plus
	// convertBoolArgs (the plain bool arguments both calls bind).
	if err := s.SetSiteReleaseMonitoring(ctx, site.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := s.SetStashState(ctx, items[0].ID, true, "scene123"); err != nil {
		t.Fatal(err)
	}
}

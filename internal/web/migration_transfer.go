package web

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Net005/JAVBeacon/internal/store"
)

// This file implements DB Phases 8, 9 and 10 together
// (TODO-DATABASE.md's own "Recommended Database Execution Order" groups
// them as one execution block, "DB Phases 8-10 - Transfer + progress +
// validation"): the actual SQLite-to-PostgreSQL data transfer, live
// progress reporting while it runs, and post-migration validation before
// the workflow reports success. setupMigrationMigrate (migration.go) is
// the only entry point - it starts runMigrationTransfer in a goroutine and
// returns immediately; setupMigrationStatus (also migration.go) is polled
// for progress via the Transfer* fields added to migrationState.
//
// Design notes:
//
//   - The source is never opened directly. Exactly like
//     setupMigrationValidateSource, a throwaway copy (plus -wal/-shm
//     sidecars) is made first and opened via store.OpenSQLite, which runs
//     the normal schema-upgrading migrate() - this both satisfies
//     "do not modify the selected SQLite source database" and guarantees
//     the source's column set is the current one (every ALTER TABLE
//     applied), matching postgresSchemaDDL's column names exactly. The
//     copy is kept open for the whole transfer, unlike the Phase 7 source
//     check, which discards it immediately.
//   - Both source and target are used as plain *sql.DB via the new
//     store.SQLite.DB() accessor - not through the domain-typed Store
//     interface, which always assigns a *new* primary key on insert. This
//     transfer explicitly preserves original ids (see migrationTableSpec)
//     so cross-table foreign keys stay consistent, so it writes raw SQL
//     instead.
//   - Every table is copied with a single generic loop driven by
//     migrationTables, using the *source's* rows.Columns() as the column
//     list rather than a hand-maintained list per table - store.go's
//     migrate() and postgres_schema.go's postgresSchemaDDL are already
//     kept in lockstep (same column names on both engines, see that
//     file's doc comment), so this stays correct without duplicating any
//     column list a third time.
//   - Booleans and timestamps need no special-case conversion: every
//     boolean-shaped column is a plain INTEGER 0/1 on both engines (see
//     postgres_schema.go), so it scans out of SQLite as int64 and back
//     into PostgreSQL as int64 unchanged; every DATETIME column comes back
//     from modernc.org/sqlite as a time.Time already (its Rows.Next
//     converts any TEXT column whose declared type is DATE/DATETIME/
//     TIMESTAMP via its own parseTime - see vendor/modernc.org/sqlite/
//     sqlite.go), which pgx accepts directly against TIMESTAMPTZ. A NULL
//     column scans as a nil interface{} and is passed straight through as
//     a SQL NULL argument.
//   - Each INSERT is an upsert ("ON CONFLICT (<primary key>) DO UPDATE").
//     This is what makes the transfer idempotent/retry-safe (Working Rule
//     #9, "migration operations must fail safely") - a target schema that
//     was just created by store.OpenPostgresStore already has a few
//     settings rows (its one-time backfill completion flags), so even the
//     very first table copied can hit an existing key; DO UPDATE also
//     means re-running a partially-failed transfer converges rather than
//     erroring on every already-copied row.
//   - Rows are streamed with database/sql's normal cursor (rows.Next()),
//     never buffered as a whole table in memory, and committed in batches
//     of migrationBatchSize rather than one enormous transaction or one
//     transaction per row - this is what "use batched migration" means
//     here.
//   - PostgreSQL identity sequences are only touched after every table has
//     been copied (the "updating sequences" stage), once per identity
//     column, via the standard setval(pg_get_serial_sequence(...), ...)
//     pattern - see resyncPostgresSequence.

// migrationTableSpec describes one table's copy: its FK-dependency-ordered
// position in migrationTables (parents before children - every foreign key
// among these tables points to a table earlier in the slice), the
// column(s) that make an ON CONFLICT upsert target, and - for the 9 tables
// with an application-assigned identity primary key - the column whose
// PostgreSQL sequence must be resynchronized once explicit ids have been
// inserted.
type migrationTableSpec struct {
	Name           string
	PrimaryKey     []string
	IdentityColumn string
}

var migrationTables = []migrationTableSpec{
	{"sites", []string{"id"}, "id"},
	{"users", []string{"id"}, ""},
	{"settings", []string{"key"}, ""},
	{"releases", []string{"id"}, "id"},
	{"release_actresses", []string{"release_id", "name_normalized"}, ""},
	{"release_tags", []string{"release_id", "name_normalized"}, ""},
	{"release_sites", []string{"release_id", "site_id"}, ""},
	{"downloads", []string{"id"}, "id"},
	{"pipeline_steps", []string{"id"}, "id"},
	{"pipeline_logs", []string{"id"}, "id"},
	{"pipeline_runs", []string{"download_id", "trigger"}, ""},
	{"notifications", []string{"id"}, "id"},
	{"desired_sync", []string{"release_id"}, ""},
	{"sessions", []string{"token"}, ""},
	{"user_preferences", []string{"user_id"}, ""},
	{"filter_presets", []string{"id"}, "id"},
	{"job_history", []string{"id"}, "id"},
	{"download_search_runs", []string{"id"}, "id"},
	{"path_mappings", []string{"id"}, "id"},
}

// migrationBatchSize is the number of rows committed per transaction while
// copying a table - large enough to keep round trips reasonable, small
// enough that a failure partway through a big table only has to redo a
// bounded amount of work (the upsert semantics above make that redo safe).
const migrationBatchSize = 200

// migrationSampleQueries are DB Phase 10's "sample/aggregate checks for
// important release data" - a handful of portable, engine-agnostic
// COUNT(*) queries whose result must match between source and target,
// beyond the per-table row-count checks every migrationTables entry
// already gets.
var migrationSampleQueries = []struct{ Description, Query string }{
	{"releases marked released", `SELECT count(*) FROM releases WHERE released=1`},
	{"releases marked local", `SELECT count(*) FROM releases WHERE is_local=1`},
	{"releases desired for download", `SELECT count(*) FROM releases WHERE desired=1`},
	{"sites with monitoring enabled", `SELECT count(*) FROM sites WHERE enabled=1`},
	{"downloads with a completed post-processing status", `SELECT count(*) FROM downloads WHERE post_status='completed'`},
}

// migrationOrphanQueries are DB Phase 10's "orphaned foreign keys where
// detectable" checks, one LEFT JOIN per foreign-key relationship among the
// 18 migrated tables. They run against the target only: PostgreSQL's own
// foreign-key constraints should make every one of these permanently zero
// given migrationTables' dependency order, so a nonzero result here means
// the transfer had a real bug, not an expected condition.
var migrationOrphanQueries = []struct{ Description, Query string }{
	{"releases with an unknown site", `SELECT count(*) FROM releases r LEFT JOIN sites s ON s.id=r.site_id WHERE s.id IS NULL`},
	{"release_actresses with an unknown release", `SELECT count(*) FROM release_actresses a LEFT JOIN releases r ON r.id=a.release_id WHERE r.id IS NULL`},
	{"release_tags with an unknown release", `SELECT count(*) FROM release_tags t LEFT JOIN releases r ON r.id=t.release_id WHERE r.id IS NULL`},
	{"release_sites with an unknown release", `SELECT count(*) FROM release_sites rs LEFT JOIN releases r ON r.id=rs.release_id WHERE r.id IS NULL`},
	{"release_sites with an unknown site", `SELECT count(*) FROM release_sites rs LEFT JOIN sites s ON s.id=rs.site_id WHERE s.id IS NULL`},
	{"downloads with an unknown release", `SELECT count(*) FROM downloads d LEFT JOIN releases r ON r.id=d.release_id WHERE d.release_id IS NOT NULL AND r.id IS NULL`},
	{"pipeline_logs with an unknown download", `SELECT count(*) FROM pipeline_logs pl LEFT JOIN downloads d ON d.id=pl.download_id WHERE d.id IS NULL`},
	{"pipeline_logs with an unknown step", `SELECT count(*) FROM pipeline_logs pl LEFT JOIN pipeline_steps ps ON ps.id=pl.step_id WHERE pl.step_id IS NOT NULL AND ps.id IS NULL`},
	{"pipeline_runs with an unknown download", `SELECT count(*) FROM pipeline_runs pr LEFT JOIN downloads d ON d.id=pr.download_id WHERE d.id IS NULL`},
	{"notifications with an unknown release", `SELECT count(*) FROM notifications n LEFT JOIN releases r ON r.id=n.release_id WHERE r.id IS NULL`},
	{"desired_sync with an unknown release", `SELECT count(*) FROM desired_sync ds LEFT JOIN releases r ON r.id=ds.release_id WHERE r.id IS NULL`},
	{"sessions with an unknown user", `SELECT count(*) FROM sessions se LEFT JOIN users u ON u.id=se.user_id WHERE u.id IS NULL`},
	{"filter_presets with an unknown user", `SELECT count(*) FROM filter_presets fp LEFT JOIN users u ON u.id=fp.user_id WHERE u.id IS NULL`},
	{"user_preferences with an unknown user", `SELECT count(*) FROM user_preferences up LEFT JOIN users u ON u.id=up.user_id WHERE u.id IS NULL`},
}

// migrationValidationReport is DB Phase 10's post-migration validation
// result, attached to migrationState once the transfer finishes (success
// or failure) so the frontend can present it (workflow step 9, "present
// migration result").
type migrationValidationReport struct {
	OK                 bool                    `json:"ok"`
	Counts             []migrationCountResult  `json:"counts"`
	OrphanChecks       []migrationOrphanResult `json:"orphan_checks"`
	SettingsMissing    []string                `json:"settings_missing,omitempty"`
	SettingsMismatched []string                `json:"settings_mismatched,omitempty"`
	Failures           []string                `json:"failures,omitempty"`
}

type migrationCountResult struct {
	Description string `json:"description"`
	Source      int64  `json:"source"`
	Target      int64  `json:"target"`
	Match       bool   `json:"match"`
}

type migrationOrphanResult struct {
	Description string `json:"description"`
	Count       int64  `json:"count"`
}

// setTransferStage records the transfer's current DB Phase 9 stage (and,
// while copying a table, which one) for setupMigrationStatus to report.
func (s *Server) setTransferStage(stage, table string) {
	s.migrationMu.Lock()
	s.migration.TransferStage = stage
	s.migration.TransferCurrentTable = table
	s.migrationMu.Unlock()
}

// recordTableProgress updates the live row count for one table as it is
// being copied (called after each batch commits, not just once at the
// end) and keeps the cumulative TransferRowsCopied counter in sync with
// it, so a large table's progress is visible while it is still running
// rather than only once it completes.
// recordTableProgress replaces s.migration.TransferTableCounts with a new
// map on every call rather than mutating the existing one in place. This
// matters because setupMigrationStatus (migration.go) reads s.migration
// under s.migrationMu but then hands its *shallow* copy off to s.json for
// JSON encoding *after* unlocking - copying a Go struct only copies a map
// field's header, not its contents, so that snapshot's TransferTableCounts
// would otherwise still be the very same map this function goes on
// mutating from the transfer goroutine, racing with the encoder's
// unsynchronized reads. Allocating a fresh map here instead means any
// snapshot taken before this call keeps referring to an old map that is
// never written to again - the standard "replace, don't mutate" copy-on-
// write pattern for safely publishing state protected by a mutex.
func (s *Server) recordTableProgress(table string, rowsSoFar int64) {
	s.migrationMu.Lock()
	next := make(map[string]int64, len(s.migration.TransferTableCounts)+1)
	for k, v := range s.migration.TransferTableCounts {
		next[k] = v
	}
	prev := next[table]
	next[table] = rowsSoFar
	s.migration.TransferTableCounts = next
	s.migration.TransferRowsCopied += rowsSoFar - prev
	s.migrationMu.Unlock()
}

// failTransfer records a terminal failure. Nothing about the running
// application or the SQLite source is touched by this - the transfer only
// ever wrote to the PostgreSQL target, which is not activated regardless
// of outcome (that is DB Phase 11), so failing safely here just means the
// user sees why and can retry.
func (s *Server) failTransfer(stage string, err error) {
	s.migrationMu.Lock()
	s.migration.TransferRunning = false
	s.migration.TransferStage = "failed"
	s.migration.TransferError = fmt.Sprintf("%s: %v", stage, err)
	s.migrationMu.Unlock()
	s.log.Error("migration transfer failed", "stage", stage, "error", err)
}

// runMigrationTransfer is the DB Phase 8-10 pipeline itself. It is started
// as a goroutine by setupMigrationMigrate and must not be called any other
// way - every mutation of s.migration goes through s.migrationMu, but
// nothing else about it is safe to call concurrently with itself (a second
// call would race its own state updates), which is why
// setupMigrationMigrate rejects a request while TransferRunning is true.
func (s *Server) runMigrationTransfer(sourcePath string, cfg store.PostgresConfig) {
	ctx := context.Background() // deliberately outlives the HTTP request that started it

	s.setTransferStage("validating source", "")
	copyPath, cleanup, err := stageMigrationSourceCopy(sourcePath)
	if err != nil {
		s.failTransfer("validating source", err)
		return
	}
	defer cleanup()
	src, err := store.OpenSQLite(copyPath)
	if err != nil {
		s.failTransfer("validating source", err)
		return
	}
	defer src.Close()
	srcDB := src.DB()

	s.setTransferStage("validating destination", "")
	probe, err := store.OpenPostgres(ctx, cfg)
	if err != nil {
		s.failTransfer("validating destination", err)
		return
	}
	probe.Close()

	s.setTransferStage("preparing schema", "")
	targetStore, err := store.OpenPostgresStore(ctx, cfg)
	if err != nil {
		s.failTransfer("preparing schema", err)
		return
	}
	defer targetStore.Close()
	targetDB := targetStore.DB()

	s.migrationMu.Lock()
	s.migration.TransferTablesTotal = len(migrationTables)
	s.migration.TransferTablesDone = 0
	s.migration.TransferTableCounts = map[string]int64{}
	s.migration.TransferRowsCopied = 0
	s.migrationMu.Unlock()

	for _, spec := range migrationTables {
		s.setTransferStage("migrating table "+spec.Name, spec.Name)
		if _, err := copyMigrationTable(ctx, srcDB, targetDB, spec, func(rowsSoFar int64) {
			s.recordTableProgress(spec.Name, rowsSoFar)
		}); err != nil {
			s.failTransfer("migrating table "+spec.Name, err)
			return
		}
		s.migrationMu.Lock()
		s.migration.TransferTablesDone++
		s.migrationMu.Unlock()
	}

	report := &migrationValidationReport{OK: true}

	s.setTransferStage("validating row counts", "")
	if err := validateMigrationCounts(ctx, srcDB, targetDB, report); err != nil {
		s.failTransfer("validating row counts", err)
		return
	}

	s.setTransferStage("validating relationships", "")
	if err := validateMigrationOrphans(ctx, targetDB, report); err != nil {
		s.failTransfer("validating relationships", err)
		return
	}

	s.setTransferStage("updating sequences", "")
	for _, spec := range migrationTables {
		if spec.IdentityColumn == "" {
			continue
		}
		if err := resyncPostgresSequence(ctx, targetDB, spec.Name, spec.IdentityColumn); err != nil {
			s.failTransfer("updating sequences", err)
			return
		}
	}

	s.setTransferStage("final verification", "")
	if err := validateMigrationSettings(ctx, srcDB, targetDB, report); err != nil {
		s.failTransfer("final verification", err)
		return
	}

	if !report.OK {
		s.migrationMu.Lock()
		s.migration.TransferRunning = false
		s.migration.TransferStage = "failed"
		s.migration.TransferError = "post-migration validation failed: " + strings.Join(report.Failures, "; ")
		s.migration.TransferValidation = report
		s.migrationMu.Unlock()
		s.log.Error("migration transfer validation failed", "failures", report.Failures)
		return
	}

	s.setTransferStage("completed", "")
	s.migrationMu.Lock()
	s.migration.TransferRunning = false
	s.migration.TransferCompleted = true
	s.migration.TransferCompletedAt = time.Now().UTC()
	s.migration.TransferValidation = report
	rows := s.migration.TransferRowsCopied
	s.migrationMu.Unlock()
	s.log.Info("migration transfer completed", "tables", len(migrationTables), "rows", rows)
}

// stageMigrationSourceCopy copies path (plus -wal/-shm sidecars, if
// present) into a fresh temporary directory, exactly like
// validateSQLiteCopy in migration.go, but returns the copy's path and a
// cleanup function instead of immediately discarding it - the transfer
// needs the copy open for its whole duration, not just long enough to read
// a summary. The original file is never opened for writing.
func stageMigrationSourceCopy(path string) (copyPath string, cleanup func(), err error) {
	if _, e := os.Stat(path); e != nil {
		return "", nil, fmt.Errorf("cannot read %s: %w", path, e)
	}
	tmpDir, err := os.MkdirTemp("", "javbeacon-migration-transfer-*")
	if err != nil {
		return "", nil, fmt.Errorf("failed to create a temporary directory: %w", err)
	}
	cleanup = func() { os.RemoveAll(tmpDir) }
	cp := filepath.Join(tmpDir, "source.db")
	if e := copyFileIfExists(path, cp, true); e != nil {
		cleanup()
		return "", nil, e
	}
	_ = copyFileIfExists(path+"-wal", cp+"-wal", false)
	_ = copyFileIfExists(path+"-shm", cp+"-shm", false)
	return cp, cleanup, nil
}

// copyMigrationTable streams every row of spec.Name from src to target,
// using the source's own column list (see this file's top comment for why)
// and an upsert keyed on spec.PrimaryKey, committing every
// migrationBatchSize rows. onBatch, if non-nil, is called after each
// commit (and once more after the final partial batch) with the total rows
// copied so far, for live progress reporting.
func copyMigrationTable(ctx context.Context, src, target *sql.DB, spec migrationTableSpec, onBatch func(rowsSoFar int64)) (int64, error) {
	rows, err := src.QueryContext(ctx, "SELECT * FROM "+spec.Name)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return 0, err
	}
	insertSQL := buildMigrationInsertSQL(spec, cols)

	tx, err := target.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	stmt, err := tx.PrepareContext(ctx, insertSQL)
	if err != nil {
		tx.Rollback()
		return 0, err
	}

	vals := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}

	var copied int64
	inBatch := 0
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			stmt.Close()
			tx.Rollback()
			return copied, err
		}
		if _, err := stmt.ExecContext(ctx, vals...); err != nil {
			stmt.Close()
			tx.Rollback()
			return copied, fmt.Errorf("inserting %s row %d: %w", spec.Name, copied+1, err)
		}
		copied++
		inBatch++
		if inBatch >= migrationBatchSize {
			if err := stmt.Close(); err != nil {
				tx.Rollback()
				return copied, err
			}
			if err := tx.Commit(); err != nil {
				return copied, err
			}
			if onBatch != nil {
				onBatch(copied)
			}
			if tx, err = target.BeginTx(ctx, nil); err != nil {
				return copied, err
			}
			if stmt, err = tx.PrepareContext(ctx, insertSQL); err != nil {
				tx.Rollback()
				return copied, err
			}
			inBatch = 0
		}
	}
	if err := rows.Err(); err != nil {
		stmt.Close()
		tx.Rollback()
		return copied, err
	}
	if err := stmt.Close(); err != nil {
		tx.Rollback()
		return copied, err
	}
	if err := tx.Commit(); err != nil {
		return copied, err
	}
	if onBatch != nil {
		onBatch(copied)
	}
	return copied, nil
}

// buildMigrationInsertSQL builds "INSERT INTO table (cols...) VALUES
// (?...) ON CONFLICT (primary key) DO UPDATE SET col=excluded.col, ..."
// (or "DO NOTHING" for the vanishingly unlikely case that every column is
// part of the primary key) for spec, using cols - the source's own column
// names, in the source's own column order - as both the insert list and
// the update-on-conflict list. See this file's top comment for why every
// insert is an upsert rather than a plain INSERT.
func buildMigrationInsertSQL(spec migrationTableSpec, cols []string) string {
	placeholders := make([]string, len(cols))
	for i := range cols {
		placeholders[i] = "?"
	}
	pk := make(map[string]bool, len(spec.PrimaryKey))
	for _, c := range spec.PrimaryKey {
		pk[c] = true
	}
	var setClauses []string
	for _, c := range cols {
		if !pk[c] {
			setClauses = append(setClauses, c+"=excluded."+c)
		}
	}
	conflict := "ON CONFLICT (" + strings.Join(spec.PrimaryKey, ",") + ") DO "
	if len(setClauses) == 0 {
		conflict += "NOTHING"
	} else {
		conflict += "UPDATE SET " + strings.Join(setClauses, ",")
	}
	return "INSERT INTO " + spec.Name + " (" + strings.Join(cols, ",") + ") VALUES (" + strings.Join(placeholders, ",") + ") " + conflict
}

// resyncPostgresSequence sets table's IdentityColumn sequence so the next
// insert continues after the highest id just copied in, rather than
// restarting at 1 and immediately colliding with it. setval's three-arg
// form is used so an empty table (MAX(id) IS NULL) still leaves the
// sequence correctly primed to hand out 1 on its first real nextval, not
// 2 - see this file's top comment.
func resyncPostgresSequence(ctx context.Context, target *sql.DB, table, column string) error {
	query := fmt.Sprintf(
		`SELECT setval(pg_get_serial_sequence('%s','%s'), COALESCE((SELECT MAX(%s) FROM %s),1), (SELECT MAX(%s) FROM %s) IS NOT NULL)`,
		table, column, column, table, column, table,
	)
	var v int64
	return target.QueryRowContext(ctx, query).Scan(&v)
}

// validateMigrationCounts is DB Phase 10's row-count and sample/aggregate
// checks: every migrationTables entry's total row count, plus
// migrationSampleQueries' finer-grained aggregate checks, each compared
// between source and target.
func validateMigrationCounts(ctx context.Context, src, target *sql.DB, report *migrationValidationReport) error {
	check := func(desc, query string) error {
		var s0, t0 int64
		if err := src.QueryRowContext(ctx, query).Scan(&s0); err != nil {
			return fmt.Errorf("source: %s: %w", desc, err)
		}
		if err := target.QueryRowContext(ctx, query).Scan(&t0); err != nil {
			return fmt.Errorf("target: %s: %w", desc, err)
		}
		match := s0 == t0
		report.Counts = append(report.Counts, migrationCountResult{desc, s0, t0, match})
		if !match {
			report.OK = false
			report.Failures = append(report.Failures, fmt.Sprintf("%s: source=%d target=%d", desc, s0, t0))
		}
		return nil
	}
	for _, spec := range migrationTables {
		if err := check(spec.Name+" row count", "SELECT count(*) FROM "+spec.Name); err != nil {
			return err
		}
	}
	for _, q := range migrationSampleQueries {
		if err := check(q.Description, q.Query); err != nil {
			return err
		}
	}
	return nil
}

// validateMigrationOrphans is DB Phase 10's "orphaned foreign keys where
// detectable" check - see migrationOrphanQueries.
func validateMigrationOrphans(ctx context.Context, target *sql.DB, report *migrationValidationReport) error {
	for _, q := range migrationOrphanQueries {
		var n int64
		if err := target.QueryRowContext(ctx, q.Query).Scan(&n); err != nil {
			return fmt.Errorf("%s: %w", q.Description, err)
		}
		report.OrphanChecks = append(report.OrphanChecks, migrationOrphanResult{q.Description, n})
		if n > 0 {
			report.OK = false
			report.Failures = append(report.Failures, fmt.Sprintf("%s: %d orphaned row(s)", q.Description, n))
		}
	}
	return nil
}

// validateMigrationSettings is DB Phase 10's "application settings
// presence" check. This codebase has no separate schema-version table to
// check (store.go's migrate()/postgres_schema.go's migratePostgres *are*
// the versioning mechanism - see that file's doc comment), so the closest
// meaningful analog is comparing every settings row, key by key, including
// the one-time backfill completion flags (release_identity_dedup_v1 and
// friends) that prove the target has actually run the same one-time data
// migrations the source has.
func validateMigrationSettings(ctx context.Context, src, target *sql.DB, report *migrationValidationReport) error {
	rows, err := src.QueryContext(ctx, "SELECT key, value FROM settings")
	if err != nil {
		return err
	}
	defer rows.Close()
	var missing, mismatched []string
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return err
		}
		var tv string
		switch err := target.QueryRowContext(ctx, "SELECT value FROM settings WHERE key=?", k).Scan(&tv); {
		case errors.Is(err, sql.ErrNoRows):
			missing = append(missing, k)
		case err != nil:
			return fmt.Errorf("reading target settings key %q: %w", k, err)
		case tv != v:
			mismatched = append(mismatched, k)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	sort.Strings(missing)
	sort.Strings(mismatched)
	report.SettingsMissing, report.SettingsMismatched = missing, mismatched
	if len(missing) > 0 || len(mismatched) > 0 {
		report.OK = false
		report.Failures = append(report.Failures, fmt.Sprintf("settings: %d missing, %d different from source", len(missing), len(mismatched)))
	}
	return nil
}

package web

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Net005/JAVBeacon/internal/domain"
	"github.com/Net005/JAVBeacon/internal/store"
)

// This file implements the DB Phase 7 "SQLite to PostgreSQL Migration
// Workflow" shell: the source-selection, source-validation,
// target-connection, target-inspection and target-schema-preparation steps
// (TODO-DATABASE.md's workflow steps 1-6). Actual row-by-row data transfer
// (step 7 onward) is out of scope for this phase - see DB Phase 8 - so
// setupMigrationMigrate below intentionally returns a "not implemented yet"
// result rather than copying any data. Nothing in this file switches the
// application's active database; that still requires restarting the
// process with matching JAVBEACON_DB_* environment variables (or, per
// TODO-DATABASE.md, a later explicit DB Phase 11 activation step once real
// data transfer exists).
//
// State is kept in memory only (Server.migration, guarded by
// Server.migrationMu) - this is a single-user, single-process application with no
// existing pattern for persisting wizard-in-progress state, and losing this
// state on restart is the safe default for a workflow that hasn't moved any
// data yet. PostgreSQL passwords are deliberately never stored in this
// state or anywhere else; every endpoint that needs one requires it in that
// request's body, matching Codex Database Working Rule #8 and the existing
// DB Phase 1A/4 wizard's same policy in setup.go.

// migrationState is the shell's status snapshot, returned by
// setupMigrationStatus and updated by the other handlers below. It never
// contains a password.
type migrationState struct {
	SourceMode string `json:"source_mode,omitempty"` // "current" or "path"
	SourcePath string `json:"source_path,omitempty"`

	SourceValidated   bool          `json:"source_validated"`
	SourceValidatedAt time.Time     `json:"source_validated_at,omitempty"`
	SourceError       string        `json:"source_error,omitempty"`
	SourceStats       *domain.Stats `json:"source_stats,omitempty"`

	TargetHost     string `json:"target_host,omitempty"`
	TargetPort     int    `json:"target_port,omitempty"`
	TargetDatabase string `json:"target_database,omitempty"`
	TargetUser     string `json:"target_user,omitempty"`
	TargetSSLMode  string `json:"target_sslmode,omitempty"`

	TargetConnected   bool      `json:"target_connected"`
	TargetConnectedAt time.Time `json:"target_connected_at,omitempty"`
	TargetMessage     string    `json:"target_message,omitempty"`

	TargetInspected     bool      `json:"target_inspected"`
	TargetInspectedAt   time.Time `json:"target_inspected_at,omitempty"`
	TargetSchemaPresent bool      `json:"target_schema_present"`
	TargetReleaseCount  int       `json:"target_release_count,omitempty"`

	TargetPrepared   bool      `json:"target_prepared"`
	TargetPreparedAt time.Time `json:"target_prepared_at,omitempty"`

	// The fields below are DB Phase 8-10's transfer progress/result, set by
	// runMigrationTransfer (migration_transfer.go) while it runs in its own
	// goroutine, and read by setupMigrationStatus for polling - this is the
	// DB Phase 9 "expose clear progress rather than appearing frozen"
	// requirement. TransferStage matches TODO-DATABASE.md's DB Phase 9
	// stage list verbatim ("validating source", "validating destination",
	// "preparing schema", "migrating table X", "validating row counts",
	// "validating relationships", "updating sequences",
	// "final verification", "completed"), plus "failed" as the terminal
	// error state.
	TransferRunning      bool                       `json:"transfer_running"`
	TransferStartedAt    time.Time                  `json:"transfer_started_at,omitempty"`
	TransferStage        string                     `json:"transfer_stage,omitempty"`
	TransferCurrentTable string                     `json:"transfer_current_table,omitempty"`
	TransferTablesDone   int                        `json:"transfer_tables_done,omitempty"`
	TransferTablesTotal  int                        `json:"transfer_tables_total,omitempty"`
	TransferRowsCopied   int64                      `json:"transfer_rows_copied,omitempty"`
	TransferTableCounts  map[string]int64           `json:"transfer_table_counts,omitempty"`
	TransferError        string                     `json:"transfer_error,omitempty"`
	TransferCompleted    bool                       `json:"transfer_completed"`
	TransferCompletedAt  time.Time                  `json:"transfer_completed_at,omitempty"`
	TransferValidation   *migrationValidationReport `json:"transfer_validation,omitempty"`

	// DB Phase 11 ("Safe Database Activation"): set by setupMigrationActivate
	// once it has independently reconnected to the target, confirmed the
	// schema is current and run a representative set of the same queries
	// internal/app.New() runs at real startup. It is informational only -
	// nothing in this file ever switches the running application's active
	// database; see setupMigrationActivate's doc comment for why.
	ActivationVerified   bool      `json:"activation_verified"`
	ActivationVerifiedAt time.Time `json:"activation_verified_at,omitempty"`
}

// migrationSourcePath resolves the "currently configured SQLite database"
// option to the actual path New() was given at startup, regardless of
// which engine is currently active - the setting still names a real path
// even when the app is running on PostgreSQL today.
func (s *Server) migrationSourcePath(mode, explicitPath string) (string, error) {
	switch mode {
	case "current":
		if s.sqlitePath == "" {
			return "", fmt.Errorf("no configured SQLite database path is known")
		}
		return s.sqlitePath, nil
	case "path":
		p := strings.TrimSpace(explicitPath)
		if p == "" {
			return "", fmt.Errorf("path is required when source mode is \"path\"")
		}
		return p, nil
	default:
		return "", fmt.Errorf("source mode must be \"current\" or \"path\"")
	}
}

func (s *Server) setupMigrationStatus(w http.ResponseWriter, r *http.Request) {
	s.migrationMu.Lock()
	st := s.migration
	s.migrationMu.Unlock()
	s.json(w, 200, st)
}

func (s *Server) setupMigrationSource(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Mode string `json:"mode"`
		Path string `json:"path"`
	}
	if !s.decode(w, r, &req) {
		return
	}
	resolved, e := s.migrationSourcePath(req.Mode, req.Path)
	if e != nil {
		s.problem(w, 422, e.Error())
		return
	}
	s.migrationMu.Lock()
	s.migration = migrationState{SourceMode: req.Mode, SourcePath: resolved}
	st := s.migration
	s.migrationMu.Unlock()
	s.json(w, 200, st)
}

// setupMigrationValidateSource opens a throwaway copy of the selected
// SQLite database (never the original file - the copy is what gets
// migrated, checked and discarded) and runs the same schema
// creation/upgrade path OpenSQLite always runs, plus an integrity check and
// a summary read via Stats(). Copying first, rather than opening the
// original file directly, is what makes "do not modify the selected SQLite
// source database" (TODO-DATABASE.md's explicit requirement for this step)
// true regardless of how the SQLite driver would otherwise behave against
// an older on-disk schema - the original file is never opened for writing.
// The -wal/-shm sidecar files are copied alongside the main file, when
// present, so a database still in WAL mode is validated with all of its
// committed-but-not-checkpointed data intact.
func (s *Server) setupMigrationValidateSource(w http.ResponseWriter, r *http.Request) {
	s.migrationMu.Lock()
	path := s.migration.SourcePath
	s.migrationMu.Unlock()
	if path == "" {
		s.problem(w, 422, "select a source database first (POST /api/setup/migration/source)")
		return
	}

	stats, e := validateSQLiteCopy(r.Context(), path)
	s.migrationMu.Lock()
	if e != nil {
		s.migration.SourceValidated = false
		s.migration.SourceError = e.Error()
		s.migration.SourceStats = nil
	} else {
		s.migration.SourceValidated = true
		s.migration.SourceValidatedAt = time.Now().UTC()
		s.migration.SourceError = ""
		s.migration.SourceStats = stats
	}
	st := s.migration
	s.migrationMu.Unlock()

	if e != nil {
		s.json(w, 200, map[string]any{"validated": false, "message": e.Error(), "status": st})
		return
	}
	s.json(w, 200, map[string]any{"validated": true, "message": "source database looks valid", "status": st})
}

// validateSQLiteCopy performs the actual copy-then-open-then-discard work
// described above. It returns the source's domain.Stats on success.
func validateSQLiteCopy(ctx context.Context, path string) (*domain.Stats, error) {
	if _, e := os.Stat(path); e != nil {
		return nil, fmt.Errorf("cannot read %s: %w", path, e)
	}

	tmpDir, e := os.MkdirTemp("", "javbeacon-migration-src-*")
	if e != nil {
		return nil, fmt.Errorf("failed to create a temporary directory: %w", e)
	}
	defer os.RemoveAll(tmpDir)

	copyPath := filepath.Join(tmpDir, "source.db")
	if e := copyFileIfExists(path, copyPath, true); e != nil {
		return nil, e
	}
	// WAL/SHM sidecars are optional - a database in DELETE/TRUNCATE journal
	// mode simply won't have them.
	_ = copyFileIfExists(path+"-wal", copyPath+"-wal", false)
	_ = copyFileIfExists(path+"-shm", copyPath+"-shm", false)

	src, e := store.OpenSQLite(copyPath)
	if e != nil {
		return nil, fmt.Errorf("failed to open the copy: %w", e)
	}
	defer src.Close()

	stats, e := src.Stats(ctx)
	if e != nil {
		return nil, fmt.Errorf("failed to read source database summary: %w", e)
	}
	return &stats, nil
}

func copyFileIfExists(from, to string, required bool) error {
	in, e := os.Open(from)
	if e != nil {
		if os.IsNotExist(e) && !required {
			return nil
		}
		return fmt.Errorf("failed to read %s: %w", from, e)
	}
	defer in.Close()
	out, e := os.OpenFile(to, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if e != nil {
		return fmt.Errorf("failed to stage a copy of %s: %w", from, e)
	}
	defer out.Close()
	if _, e := io.Copy(out, in); e != nil {
		return fmt.Errorf("failed to copy %s: %w", from, e)
	}
	return nil
}

// migrationTargetRequest is the shared connection-details shape for every
// target-facing endpoint below. The password is intentionally part of the
// request, never of migrationState - see this file's top comment.
type migrationTargetRequest struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Database string `json:"database"`
	User     string `json:"user"`
	Password string `json:"password"`
	SSLMode  string `json:"sslmode"`
}

func (req migrationTargetRequest) config() store.PostgresConfig {
	return store.PostgresConfig{
		Host: strings.TrimSpace(req.Host), Port: req.Port,
		Database: strings.TrimSpace(req.Database), User: strings.TrimSpace(req.User),
		Password: req.Password, SSLMode: req.SSLMode,
	}
}

func (req migrationTargetRequest) validate() error {
	if strings.TrimSpace(req.Host) == "" {
		return fmt.Errorf("host is required")
	}
	if req.Port < 1 || req.Port > 65535 {
		return fmt.Errorf("port must be between 1 and 65535")
	}
	if strings.TrimSpace(req.Database) == "" {
		return fmt.Errorf("database is required")
	}
	if strings.TrimSpace(req.User) == "" {
		return fmt.Errorf("user is required")
	}
	return nil
}

// setupMigrationPostgres validates connectivity to the migration target -
// workflow steps 3+4 combined into one call, since entering settings the
// server immediately rejects isn't a useful intermediate state to expose.
// This reuses the exact same store.OpenPostgres/ClassifyPostgresError path
// as the DB Phase 1A/4 wizard's Test Connection (setupDBTestConnection) -
// see that function's doc comment for why. On success the non-secret parts
// of the connection are recorded in migrationState for the status display;
// the password is never stored.
func (s *Server) setupMigrationPostgres(w http.ResponseWriter, r *http.Request) {
	var req migrationTargetRequest
	if !s.decode(w, r, &req) {
		return
	}
	if e := req.validate(); e != nil {
		s.problem(w, 422, e.Error())
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	db, e := store.OpenPostgres(ctx, req.config())
	if e != nil {
		category, message := store.ClassifyPostgresError(e)
		s.json(w, 200, map[string]any{"connected": false, "category": string(category), "message": message})
		return
	}
	_ = db.Close()

	s.migrationMu.Lock()
	s.migration.TargetHost, s.migration.TargetPort = req.Host, req.Port
	s.migration.TargetDatabase, s.migration.TargetUser, s.migration.TargetSSLMode = req.Database, req.User, req.SSLMode
	s.migration.TargetConnected = true
	s.migration.TargetConnectedAt = time.Now().UTC()
	s.migration.TargetMessage = "connected successfully as " + req.User + " to database \"" + req.Database + "\""
	// A newly (re)validated target invalidates any earlier inspection/
	// preparation result taken against a possibly different server.
	s.migration.TargetInspected, s.migration.TargetPrepared = false, false
	st := s.migration
	s.migrationMu.Unlock()

	s.json(w, 200, map[string]any{"connected": true, "message": st.TargetMessage, "status": st})
}

// setupMigrationInspectTarget reports whether the target already has a
// JAVBeacon schema (and, if so, how many releases it already holds), so
// the user can see before preparing/migrating whether they'd be pointing at
// an already-populated database. It never creates or modifies anything -
// that's setupMigrationPrepareTarget's job.
func (s *Server) setupMigrationInspectTarget(w http.ResponseWriter, r *http.Request) {
	var req migrationTargetRequest
	if !s.decode(w, r, &req) {
		return
	}
	if e := req.validate(); e != nil {
		s.problem(w, 422, e.Error())
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	db, e := store.OpenPostgres(ctx, req.config())
	if e != nil {
		category, message := store.ClassifyPostgresError(e)
		s.json(w, 200, map[string]any{"inspected": false, "category": string(category), "message": message})
		return
	}
	defer db.Close()

	var tableCount int
	if e := db.QueryRowContext(ctx, `SELECT count(*) FROM information_schema.tables WHERE table_schema='public' AND table_name IN ('sites','releases','settings','users')`).Scan(&tableCount); e != nil {
		s.problem(w, 500, "failed to inspect target schema: "+e.Error())
		return
	}
	schemaPresent := tableCount >= 4
	releaseCount := 0
	if schemaPresent {
		// This table's presence was just confirmed above; ignore a scan
		// error here rather than failing the whole inspection over it.
		_ = db.QueryRowContext(ctx, `SELECT count(*) FROM releases`).Scan(&releaseCount)
	}

	s.migrationMu.Lock()
	s.migration.TargetInspected = true
	s.migration.TargetInspectedAt = time.Now().UTC()
	s.migration.TargetSchemaPresent = schemaPresent
	s.migration.TargetReleaseCount = releaseCount
	st := s.migration
	s.migrationMu.Unlock()

	s.json(w, 200, map[string]any{"inspected": true, "schema_present": schemaPresent, "release_count": releaseCount, "status": st})
}

// setupMigrationPrepareTarget ensures the target's schema exists and is
// current by running the exact same store.OpenPostgresStore path the
// application itself uses to open a PostgreSQL-backed Store at startup
// (DB Phase 5-6) - it does not switch the running application's active
// database, and it is safe to call more than once (both the DDL and the
// one-time data-migration steps it runs are idempotent).
func (s *Server) setupMigrationPrepareTarget(w http.ResponseWriter, r *http.Request) {
	var req migrationTargetRequest
	if !s.decode(w, r, &req) {
		return
	}
	if e := req.validate(); e != nil {
		s.problem(w, 422, e.Error())
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	target, e := store.OpenPostgresStore(ctx, req.config())
	if e != nil {
		category, message := store.ClassifyPostgresError(e)
		s.json(w, 200, map[string]any{"prepared": false, "category": string(category), "message": message})
		return
	}
	target.Close()

	s.migrationMu.Lock()
	s.migration.TargetPrepared = true
	s.migration.TargetPreparedAt = time.Now().UTC()
	st := s.migration
	s.migrationMu.Unlock()

	s.json(w, 200, map[string]any{"prepared": true, "message": "target schema created/verified", "status": st})
}

// setupMigrationMigrate starts the DB Phase 8-10 data transfer (workflow
// steps 7-9: migrate data, validate migrated data, present the result) in a
// background goroutine and returns immediately - a library-sized transfer
// would make a synchronous single request impractical, and DB Phase 9
// explicitly calls for polling-friendly progress instead. It requires a
// fresh target connection request body (the same shape every other
// target-facing endpoint in this file takes) because, per this file's top
// comment, the password is never part of migrationState. Workflow step 10
// ("only after successful validation allow PostgreSQL to become active") is
// DB Phase 11 and out of scope here - this handler never switches the
// running application's active database.
func (s *Server) setupMigrationMigrate(w http.ResponseWriter, r *http.Request) {
	s.migrationMu.Lock()
	st := s.migration
	s.migrationMu.Unlock()
	if !st.SourceValidated {
		s.problem(w, 422, "validate the source database first")
		return
	}
	if !st.TargetPrepared {
		s.problem(w, 422, "prepare the target schema first")
		return
	}
	if st.TransferRunning {
		s.problem(w, 422, "a migration is already in progress")
		return
	}

	var req migrationTargetRequest
	if !s.decode(w, r, &req) {
		return
	}
	if e := req.validate(); e != nil {
		s.problem(w, 422, e.Error())
		return
	}

	s.migrationMu.Lock()
	s.migration.TransferRunning = true
	s.migration.TransferStartedAt = time.Now().UTC()
	s.migration.TransferStage = "validating source"
	s.migration.TransferCurrentTable = ""
	s.migration.TransferTablesDone = 0
	s.migration.TransferTablesTotal = len(migrationTables)
	s.migration.TransferRowsCopied = 0
	s.migration.TransferTableCounts = nil
	s.migration.TransferError = ""
	s.migration.TransferCompleted = false
	s.migration.TransferCompletedAt = time.Time{}
	s.migration.TransferValidation = nil
	sourcePath := s.migration.SourcePath
	st = s.migration
	s.migrationMu.Unlock()

	go s.runMigrationTransfer(sourcePath, req.config())

	s.json(w, 200, map[string]any{"started": true, "status": st})
}

// setupMigrationActivate is DB Phase 11 ("Safe Database Activation"). It
// requires a completed, successfully-validated transfer (workflow step 9,
// "present migration result") plus a fresh target connection request body,
// like every other target-facing endpoint in this file. It independently
// reconnects to the target - never trusting that an earlier step's
// connection is still open or still correct - confirms the schema is
// current via the exact store.OpenPostgresStore path the application uses
// at real startup, and then runs a representative sample of the queries
// internal/app.New() itself performs during startup (Sites and Settings
// directly; User via the same store.User the application's EnsureUser
// startup call exercises). This is "verify the application can perform
// required startup queries" made concrete, rather than a generic ping.
//
// It never switches the running application's active database. Per
// TODO-DATABASE.md's DB Phase 5-6/setupDBSave precedent (see that
// function's doc comment), this application is a single Docker process
// whose database engine is chosen once at startup from JAVBEACON_DB_*
// environment variables - there is no live, in-process way to swap the
// active Store without restarting, and building one would be a large,
// unrelated architectural change this phase does not call for. So "the
// explicit activation step" here is the verification itself: once it
// succeeds, the response hands back the exact environment variables to
// set (including the password just supplied - shown once and never
// stored, matching the DB Phase 1A wizard's password-generation UX) and
// the restart instruction. The original SQLite source is never touched by
// this or any other step in this file, so it remains available as a
// rollback source per TODO-DATABASE.md's explicit requirement.
func (s *Server) setupMigrationActivate(w http.ResponseWriter, r *http.Request) {
	s.migrationMu.Lock()
	st := s.migration
	s.migrationMu.Unlock()
	if !st.TransferCompleted || st.TransferValidation == nil || !st.TransferValidation.OK {
		s.problem(w, 422, "complete a successful migration and validation first")
		return
	}

	var req migrationTargetRequest
	if !s.decode(w, r, &req) {
		return
	}
	if e := req.validate(); e != nil {
		s.problem(w, 422, e.Error())
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	target, e := store.OpenPostgresStore(ctx, req.config())
	if e != nil {
		category, message := store.ClassifyPostgresError(e)
		s.json(w, 200, map[string]any{"activated": false, "category": string(category), "message": message})
		return
	}
	defer target.Close()

	if _, e := target.Sites(ctx); e != nil {
		s.json(w, 200, map[string]any{"activated": false, "message": "connected, but a startup query (Sites) failed: " + e.Error()})
		return
	}
	if _, e := target.Settings(ctx); e != nil {
		s.json(w, 200, map[string]any{"activated": false, "message": "connected, but a startup query (Settings) failed: " + e.Error()})
		return
	}
	if _, e := target.User(ctx); e != nil {
		s.json(w, 200, map[string]any{"activated": false, "message": "connected, but a startup query (User) failed: " + e.Error()})
		return
	}

	s.migrationMu.Lock()
	s.migration.ActivationVerified = true
	s.migration.ActivationVerifiedAt = time.Now().UTC()
	st = s.migration
	s.migrationMu.Unlock()

	env := map[string]string{
		"JAVBEACON_DB_ENGINE":   "postgres",
		"JAVBEACON_DB_HOST":     req.Host,
		"JAVBEACON_DB_PORT":     strconv.Itoa(req.Port),
		"JAVBEACON_DB_NAME":     req.Database,
		"JAVBEACON_DB_USER":     req.User,
		"JAVBEACON_DB_PASSWORD": req.Password,
		"JAVBEACON_DB_SSLMODE":  req.SSLMode,
	}
	instructions := []string{
		"Set the environment variables below for the JAVBeacon process/container (e.g. in your docker-compose.yml or .env).",
		"Restart JAVBeacon.",
		`Confirm the startup log line reads "database":"postgres://..." with the values below.`,
		"Your original SQLite database is left untouched as a rollback source - it is not deleted or modified by this workflow.",
	}
	s.json(w, 200, map[string]any{
		"activated":    true,
		"message":      "PostgreSQL is ready to activate: connection, schema and startup queries all verified.",
		"env":          env,
		"instructions": instructions,
		"status":       st,
	})
}

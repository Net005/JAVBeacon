package web

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Net005/JAVBeacon/internal/setup"
	"github.com/Net005/JAVBeacon/internal/store"
)

// This file implements the DB Phase 1A "Database Setup Wizard" endpoints:
// generating a PostgreSQL Docker Compose stack + .env tailored to the
// user's memory/storage/topology choices, a real connection check (added
// in DB Phase 4, see below), and persisting the wizard's (non-secret)
// selections for later reuse.
//
// setupDBTestConnection performs a full PostgreSQL connection attempt via
// store.OpenPostgres/store.ClassifyPostgresError - the same driver and
// error-classification path internal/app uses at startup for
// JAVBEACON_DB_ENGINE=postgres. This is a connectivity-only check (it
// does not run migratePostgres), so a success here confirms the server is
// reachable and the credentials/database are accepted, not that the schema
// is present yet - that happens the moment the application actually starts
// against it (store.OpenPostgresStore, DB Phase 5-6), which is idempotent
// and safe to run against an already-migrated database.
//
// These endpoints require normal application authentication like the rest
// of the API (they are not added to the security() public allowlist): this
// is a single-user private app where an admin account already exists
// before Settings is reachable, so gating the wizard behind auth avoids
// introducing an unauthenticated configuration surface without any
// corresponding first-run/no-account-yet flow to justify one.
//
// Persisted wizard selections never include the PostgreSQL password -
// only the host/port/database/user/topology/profile choices, which are not
// secret. The password is generated/shown once per generate call and must
// be copied into the user's own .env by hand, per Codex Database Working
// Rule #8 ("Never expose database passwords or connection secrets in
// logs") and DB Phase 1A's "do not persist the plaintext password" UX
// requirement.

const (
	pgSetupKeyEngine        = "db_engine"
	pgSetupKeyHost          = "pg_host"
	pgSetupKeyPort          = "pg_port"
	pgSetupKeyDatabase      = "pg_database"
	pgSetupKeyUser          = "pg_user"
	pgSetupKeySSLMode       = "pg_sslmode"
	pgSetupKeyMethod        = "pg_setup_method"
	pgSetupKeyTopology      = "pg_topology"
	pgSetupKeyBindAddress   = "pg_bind_address"
	pgSetupKeyDataPath      = "pg_data_path"
	pgSetupKeyMemoryProfile = "pg_memory_profile"
	pgSetupKeyMemoryBudget  = "pg_memory_budget_mb"
	pgSetupKeyStorage       = "pg_storage_profile"
)

var pgSetupKeys = []string{
	pgSetupKeyEngine, pgSetupKeyHost, pgSetupKeyPort, pgSetupKeyDatabase, pgSetupKeyUser,
	pgSetupKeySSLMode, pgSetupKeyMethod, pgSetupKeyTopology, pgSetupKeyBindAddress,
	pgSetupKeyDataPath, pgSetupKeyMemoryProfile, pgSetupKeyMemoryBudget, pgSetupKeyStorage,
}

// setupDBStatus reports the engine the running process actually connected
// with (from config.Config, wired in at startup) plus any wizard
// selections staged from an earlier visit.
func (s *Server) setupDBStatus(w http.ResponseWriter, r *http.Request) {
	staged, e := s.store.Settings(r.Context())
	if e != nil {
		s.problem(w, 500, e.Error())
		return
	}
	out := map[string]any{"active_engine": s.dbEngine}
	stagedOut := map[string]string{}
	for _, k := range pgSetupKeys {
		if v := staged[k]; v != "" {
			stagedOut[k] = v
		}
	}
	out["staged"] = stagedOut
	s.json(w, 200, out)
}

// setupDBOptions returns the static reference data (presets, defaults,
// topology descriptions) the wizard UI renders as choices.
func (s *Server) setupDBOptions(w http.ResponseWriter, r *http.Request) {
	pool := setup.DefaultApplicationPool()
	s.json(w, 200, map[string]any{
		"recommended_engine":       "postgres",
		"lightweight_engine":       "sqlite",
		"default_setup_method":     "docker_compose",
		"default_postgres_version": 18,
		"default_memory_profile":   setup.ProfileLarge,
		"default_storage_profile":  setup.StorageSSD,
		"default_database_name":    "javbeacon",
		"default_database_user":    "javbeacon",
		"default_port":             5432,
		"default_data_path":        "./postgres-data",
		"memory_presets":           setup.MemoryPresetOptions(),
		"topologies": []map[string]string{
			{"key": string(setup.TopologySameNetwork), "label": "Application and PostgreSQL in the same Docker network", "detail": "PostgreSQL is not published to the host; the app connects via the service hostname postgres:5432."},
			{"key": string(setup.TopologyHost), "label": "Application runs directly on this host", "detail": "PostgreSQL is published on loopback only (127.0.0.1)."},
			{"key": string(setup.TopologyLAN), "label": "Application runs on another LAN host", "detail": "PostgreSQL is published on an explicit LAN address you choose. Never 0.0.0.0, never the public internet."},
		},
		"application_pool_defaults": pool,
		"instructions":              setup.SetupInstructions(),
	})
}

type setupDBGenerateRequest struct {
	DatabaseName   string `json:"database_name"`
	DatabaseUser   string `json:"database_user"`
	Password       string `json:"password"` // if empty, a new one is generated
	RegeneratePass bool   `json:"regenerate_password"`
	DataPath       string `json:"data_path"`
	Topology       string `json:"topology"`
	BindAddress    string `json:"bind_address"`
	Port           int    `json:"port"`
	StorageProfile string `json:"storage_profile"`
	MemoryProfile  string `json:"memory_profile"`
	MemoryBudgetMB int    `json:"memory_budget_mb"`
	SSLMode        string `json:"sslmode"`
}

// setupDBGenerate renders the Compose file, .env file and connection
// preview for the wizard's current selections. It performs no I/O beyond
// crypto/rand for the password - no file is written and no database is
// contacted.
func (s *Server) setupDBGenerate(w http.ResponseWriter, r *http.Request) {
	var req setupDBGenerateRequest
	if !s.decode(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.DatabaseName) == "" {
		req.DatabaseName = "javbeacon"
	}
	if strings.TrimSpace(req.DatabaseUser) == "" {
		req.DatabaseUser = "javbeacon"
	}
	if strings.TrimSpace(req.DataPath) == "" {
		req.DataPath = "./postgres-data"
	}
	if req.Port == 0 {
		req.Port = 5432
	}
	if strings.TrimSpace(req.Topology) == "" {
		req.Topology = string(setup.TopologyHost)
	}
	if strings.TrimSpace(req.StorageProfile) == "" {
		req.StorageProfile = string(setup.StorageSSD)
	}
	if strings.TrimSpace(req.MemoryProfile) == "" {
		req.MemoryProfile = string(setup.ProfileLarge)
	}
	if strings.TrimSpace(req.SSLMode) == "" {
		req.SSLMode = "prefer"
	}

	password := strings.TrimSpace(req.Password)
	if password == "" || req.RegeneratePass {
		p, e := setup.GeneratePassword()
		if e != nil {
			s.problem(w, 500, "failed to generate password: "+e.Error())
			return
		}
		password = p
	}

	memory, e := setup.ResolveMemoryTuning(setup.MemoryProfileName(req.MemoryProfile), req.MemoryBudgetMB)
	if e != nil {
		s.problem(w, 422, e.Error())
		return
	}
	io, e := setup.ResolveIOTuning(setup.StorageProfile(req.StorageProfile))
	if e != nil {
		s.problem(w, 422, e.Error())
		return
	}

	opts := setup.ComposeOptions{
		DatabaseName: req.DatabaseName,
		DatabaseUser: req.DatabaseUser,
		Password:     password,
		DataPath:     req.DataPath,
		Topology:     setup.Topology(req.Topology),
		BindAddress:  req.BindAddress,
		Port:         req.Port,
		Storage:      setup.StorageProfile(req.StorageProfile),
		Memory:       memory,
	}
	fixed := setup.DefaultFixedTuning()
	compose, e := setup.GenerateCompose(opts, fixed, io)
	if e != nil {
		s.problem(w, 422, e.Error())
		return
	}
	env, e := setup.GenerateEnv(opts, fixed, io)
	if e != nil {
		s.problem(w, 422, e.Error())
		return
	}

	connectionHost := "127.0.0.1"
	switch opts.Topology {
	case setup.TopologySameNetwork:
		connectionHost = "postgres"
	case setup.TopologyLAN:
		connectionHost = req.BindAddress
	}

	s.json(w, 200, map[string]any{
		"compose":      compose,
		"env":          env,
		"password":     password,
		"instructions": setup.SetupInstructions(),
		"connection": map[string]any{
			"host":     connectionHost,
			"port":     req.Port,
			"database": req.DatabaseName,
			"user":     req.DatabaseUser,
			"sslmode":  req.SSLMode,
		},
		"memory_tuning": memory,
		"io_tuning":     io,
	})
}

// setupDBTestConnection attempts a real PostgreSQL connection for the
// wizard's current selections and reports back a classified success or
// failure. It does not persist anything (the password never reaches
// setupDBSave/storage) and does not switch the application's active
// database - activation still requires restarting the process with
// matching JAVBEACON_DB_* environment variables.
func (s *Server) setupDBTestConnection(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Host     string `json:"host"`
		Port     int    `json:"port"`
		Database string `json:"database"`
		User     string `json:"user"`
		Password string `json:"password"`
		SSLMode  string `json:"sslmode"`
	}
	if !s.decode(w, r, &req) {
		return
	}
	host := strings.TrimSpace(req.Host)
	if host == "" {
		s.problem(w, 422, "host is required")
		return
	}
	if req.Port < 1 || req.Port > 65535 {
		s.problem(w, 422, "port must be between 1 and 65535")
		return
	}
	database := strings.TrimSpace(req.Database)
	if database == "" {
		s.problem(w, 422, "database is required")
		return
	}
	user := strings.TrimSpace(req.User)
	if user == "" {
		s.problem(w, 422, "user is required")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	db, e := store.OpenPostgres(ctx, store.PostgresConfig{
		Host:     host,
		Port:     req.Port,
		Database: database,
		User:     user,
		Password: req.Password,
		SSLMode:  req.SSLMode,
	})
	if e != nil {
		category, message := store.ClassifyPostgresError(e)
		s.json(w, 200, map[string]any{
			"connected": false,
			"category":  string(category),
			"message":   message,
		})
		return
	}
	_ = db.Close()
	s.json(w, 200, map[string]any{
		"connected": true,
		"message":   "connected successfully as " + user + " to database \"" + database + "\"",
	})
}

// setupDBSave persists the wizard's non-secret selections so a later visit
// (or a later database phase, such as the Phase 7 migration workflow) can
// pre-fill them. The password is never accepted here. Saving does not
// switch the application's active database - per DB Phase 1's explicit
// "do not switch the application's active database yet merely because
// PostgreSQL settings were entered" requirement, activation always
// requires restarting the process with matching JAVBEACON_DB_* env
// vars (or, later, the Phase 11 explicit activation step).
func (s *Server) setupDBSave(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Engine         string `json:"engine"`
		Host           string `json:"host"`
		Port           int    `json:"port"`
		Database       string `json:"database"`
		User           string `json:"user"`
		SSLMode        string `json:"sslmode"`
		SetupMethod    string `json:"setup_method"`
		Topology       string `json:"topology"`
		BindAddress    string `json:"bind_address"`
		DataPath       string `json:"data_path"`
		MemoryProfile  string `json:"memory_profile"`
		MemoryBudgetMB int    `json:"memory_budget_mb"`
		StorageProfile string `json:"storage_profile"`
	}
	if !s.decode(w, r, &req) {
		return
	}
	if req.Engine != "" && req.Engine != "sqlite" && req.Engine != "postgres" {
		s.problem(w, 422, "engine must be \"sqlite\" or \"postgres\"")
		return
	}
	values := map[string]string{
		pgSetupKeyEngine:        req.Engine,
		pgSetupKeyHost:          req.Host,
		pgSetupKeyDatabase:      req.Database,
		pgSetupKeyUser:          req.User,
		pgSetupKeySSLMode:       req.SSLMode,
		pgSetupKeyMethod:        req.SetupMethod,
		pgSetupKeyTopology:      req.Topology,
		pgSetupKeyBindAddress:   req.BindAddress,
		pgSetupKeyDataPath:      req.DataPath,
		pgSetupKeyMemoryProfile: req.MemoryProfile,
		pgSetupKeyStorage:       req.StorageProfile,
	}
	if req.Port > 0 {
		values[pgSetupKeyPort] = strconv.Itoa(req.Port)
	}
	if req.MemoryBudgetMB > 0 {
		values[pgSetupKeyMemoryBudget] = strconv.Itoa(req.MemoryBudgetMB)
	}
	if e := s.store.SaveSettings(r.Context(), values); e != nil {
		s.problem(w, 500, e.Error())
		return
	}
	s.json(w, 200, map[string]any{
		"saved":   true,
		"message": "Selections saved. This does not switch the active database - set the matching JAVBEACON_DB_* environment variables and restart the application to activate PostgreSQL.",
	})
}

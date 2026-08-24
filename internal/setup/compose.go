package setup

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

// Topology describes where the JAVBeacon application runs relative to
// the generated PostgreSQL container, per DB Phase 1A's "Setup topology"
// section. It controls whether/how the container's port is published.
type Topology string

const (
	// TopologySameNetwork: app and PostgreSQL share a Docker network.
	// PostgreSQL is not published to the host at all; the app connects via
	// the service hostname (postgres:5432).
	TopologySameNetwork Topology = "same_network"
	// TopologyHost: the application runs directly on the same host as
	// Docker. PostgreSQL is published on loopback only (127.0.0.1).
	TopologyHost Topology = "host"
	// TopologyLAN: the application runs on another LAN host. PostgreSQL is
	// published on an explicit, user-provided LAN bind address. Never
	// defaults to 0.0.0.0 and never suggests exposing PostgreSQL to the
	// public internet.
	TopologyLAN Topology = "lan"
)

// ApplicationPoolDefaults are the recommended application-side connection
// pool settings for the Large Library profile (see DB Phase 1A's
// "Application connection-pool defaults"). A large PostgreSQL
// max_connections does not mean the application should open that many
// connections itself.
type ApplicationPoolDefaults struct {
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime string
	ConnMaxIdleTime string
}

// DefaultApplicationPool returns the Large Library starting target: ~20-30
// max open connections, ~5-10 idle.
func DefaultApplicationPool() ApplicationPoolDefaults {
	return ApplicationPoolDefaults{MaxOpenConns: 25, MaxIdleConns: 8, ConnMaxLifetime: "30m", ConnMaxIdleTime: "5m"}
}

// GeneratePassword returns a cryptographically random password suitable for
// POSTGRES_PASSWORD, using the same crypto/rand + base64 idiom already used
// for session tokens in internal/auth. 24 random bytes -> 32 url-safe
// base64 characters: long enough to be strong, and free of characters that
// would need quoting in a .env file or YAML.
func GeneratePassword() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// ComposeOptions captures every wizard selection needed to render the
// PostgreSQL Docker Compose stack and its .env file.
type ComposeOptions struct {
	DatabaseName string
	DatabaseUser string
	Password     string // required; never logged, never echoed back after generation
	DataPath     string // host bind-mount path for the persistent volume, e.g. ./postgres-data
	Topology     Topology
	BindAddress  string // required for TopologyLAN; ignored otherwise
	Port         int    // host-published port; defaults to 5432
	Storage      StorageProfile
	Memory       MemoryTuning
}

// Validate checks the options for the safety rules DB Phase 1A calls out
// explicitly: never default (or silently accept) a public LAN bind, always
// require the fields needed to render a working stack.
func (o ComposeOptions) Validate() error {
	if strings.TrimSpace(o.DatabaseName) == "" {
		return errors.New("database name is required")
	}
	if strings.TrimSpace(o.DatabaseUser) == "" {
		return errors.New("database user is required")
	}
	if strings.TrimSpace(o.Password) == "" {
		return errors.New("a PostgreSQL password is required - generate one or enter your own")
	}
	if o.Port < 1 || o.Port > 65535 {
		return errors.New("port must be between 1 and 65535")
	}
	switch o.Topology {
	case TopologySameNetwork, TopologyHost:
		// no bind address needed
	case TopologyLAN:
		addr := strings.TrimSpace(o.BindAddress)
		if addr == "" {
			return errors.New("a LAN bind address is required for this topology")
		}
		if addr == "0.0.0.0" {
			return errors.New("binding PostgreSQL to 0.0.0.0 would expose it on every network interface, including the public internet if this host is reachable from one - choose the specific LAN address of this host instead")
		}
	default:
		return errors.New("unknown topology: " + string(o.Topology))
	}
	return nil
}

// GenerateCompose renders a docker-compose.yaml equivalent to the
// TODO-DATABASE.md template: PostgreSQL 18, the /var/lib/postgresql volume
// target (not the pre-18 /var/lib/postgresql/data path), and the Large
// Library command-line tuning flags. All numeric/text tuning values are
// referenced via ${VAR:-default} so the same Compose file works unmodified
// across profiles - only the accompanying .env changes.
func GenerateCompose(o ComposeOptions, fixed FixedTuning, io IOTuning) (string, error) {
	if err := o.Validate(); err != nil {
		return "", err
	}
	var ports strings.Builder
	switch o.Topology {
	case TopologySameNetwork:
		ports.WriteString("    # Application and PostgreSQL share this Compose file's Docker network.\n")
		ports.WriteString("    # PostgreSQL is intentionally not published to the host - connect from\n")
		ports.WriteString("    # the application using the service hostname: postgres:5432\n")
	case TopologyHost:
		ports.WriteString("    ports:\n")
		ports.WriteString("      - \"${POSTGRES_BIND_ADDRESS:-127.0.0.1}:${POSTGRES_PORT:-5432}:5432\"\n")
	case TopologyLAN:
		ports.WriteString("    # WARNING: this publishes PostgreSQL on a LAN-reachable address.\n")
		ports.WriteString("    # Make sure this host's firewall does not forward the port further,\n")
		ports.WriteString("    # and never point POSTGRES_BIND_ADDRESS at 0.0.0.0 or a public interface.\n")
		ports.WriteString("    ports:\n")
		ports.WriteString("      - \"${POSTGRES_BIND_ADDRESS:-127.0.0.1}:${POSTGRES_PORT:-5432}:5432\"\n")
	}

	yaml := fmt.Sprintf(`services:
  postgres:
    image: postgres:18
    container_name: javbeacon-postgres
    restart: unless-stopped
    shm_size: "1gb"

    environment:
      POSTGRES_DB: ${POSTGRES_DB:-%s}
      POSTGRES_USER: ${POSTGRES_USER:-%s}
      POSTGRES_PASSWORD: ${POSTGRES_PASSWORD:?POSTGRES_PASSWORD must be set}
      POSTGRES_INITDB_ARGS: "--data-checksums --encoding=UTF8"

%s
    volumes:
      - ${POSTGRES_DATA_PATH:-%s}:/var/lib/postgresql

    command:
      - postgres

      # Connection/concurrency
      - -c
      - max_connections=${POSTGRES_MAX_CONNECTIONS:-%d}

      # Memory profile
      - -c
      - shared_buffers=${POSTGRES_SHARED_BUFFERS:-%dMB}
      - -c
      - effective_cache_size=${POSTGRES_EFFECTIVE_CACHE_SIZE:-%dMB}
      - -c
      - work_mem=${POSTGRES_WORK_MEM:-%dMB}
      - -c
      - maintenance_work_mem=${POSTGRES_MAINTENANCE_WORK_MEM:-%dMB}
      - -c
      - autovacuum_work_mem=${POSTGRES_AUTOVACUUM_WORK_MEM:-%dMB}

      # Storage-profile planner defaults
      - -c
      - random_page_cost=${POSTGRES_RANDOM_PAGE_COST:-%s}
      - -c
      - effective_io_concurrency=${POSTGRES_EFFECTIVE_IO_CONCURRENCY:-%d}

      # Planner statistics for join-heavy/cross-reference-heavy queries
      - -c
      - default_statistics_target=${POSTGRES_DEFAULT_STATISTICS_TARGET:-%d}

      # WAL/checkpoint behavior for imports, scraping updates and migrations
      - -c
      - wal_compression=${POSTGRES_WAL_COMPRESSION:-%s}
      - -c
      - min_wal_size=${POSTGRES_MIN_WAL_SIZE:-%dGB}
      - -c
      - max_wal_size=${POSTGRES_MAX_WAL_SIZE:-%dGB}
      - -c
      - checkpoint_completion_target=${POSTGRES_CHECKPOINT_COMPLETION_TARGET:-%s}

      # More responsive maintenance for large/changing libraries
      - -c
      - autovacuum_max_workers=${POSTGRES_AUTOVACUUM_MAX_WORKERS:-%d}
      - -c
      - autovacuum_naptime=${POSTGRES_AUTOVACUUM_NAPTIME:-%ds}
      - -c
      - autovacuum_vacuum_scale_factor=${POSTGRES_AUTOVACUUM_VACUUM_SCALE_FACTOR:-%s}
      - -c
      - autovacuum_analyze_scale_factor=${POSTGRES_AUTOVACUUM_ANALYZE_SCALE_FACTOR:-%s}

    healthcheck:
      test:
        [
          "CMD-SHELL",
          "pg_isready -U \"$${POSTGRES_USER}\" -d \"$${POSTGRES_DB}\""
        ]
      interval: 10s
      timeout: 5s
      retries: 10
      start_period: 10s

    stop_grace_period: 1m
`,
		o.DatabaseName, o.DatabaseUser,
		strings.TrimRight(ports.String(), "\n"),
		o.DataPath,
		fixed.MaxConnections,
		o.Memory.SharedBuffersMB, o.Memory.EffectiveCacheSizeMB, o.Memory.WorkMemMB, o.Memory.MaintenanceWorkMemMB, o.Memory.AutovacuumWorkMemMB,
		formatFloat(io.RandomPageCost), io.EffectiveIOConcurrency,
		fixed.DefaultStatisticsTarget,
		fixed.WALCompression, fixed.MinWALSizeGB, fixed.MaxWALSizeGB, formatFloat(fixed.CheckpointCompletionTarget),
		fixed.AutovacuumMaxWorkers, fixed.AutovacuumNaptimeSeconds, formatFloat(fixed.AutovacuumVacuumScaleFactor), formatFloat(fixed.AutovacuumAnalyzeScaleFactor),
	)
	return yaml, nil
}

// GenerateEnv renders the .env file that accompanies GenerateCompose's
// output, with concrete values for every ${VAR:-default} reference.
func GenerateEnv(o ComposeOptions, fixed FixedTuning, io IOTuning) (string, error) {
	if err := o.Validate(); err != nil {
		return "", err
	}
	bindAddress := o.BindAddress
	if o.Topology == TopologyHost || bindAddress == "" {
		bindAddress = "127.0.0.1"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "POSTGRES_DB=%s\n", o.DatabaseName)
	fmt.Fprintf(&b, "POSTGRES_USER=%s\n", o.DatabaseUser)
	fmt.Fprintf(&b, "POSTGRES_PASSWORD=%s\n\n", o.Password)
	fmt.Fprintf(&b, "POSTGRES_DATA_PATH=%s\n", o.DataPath)
	fmt.Fprintf(&b, "POSTGRES_BIND_ADDRESS=%s\n", bindAddress)
	fmt.Fprintf(&b, "POSTGRES_PORT=%d\n\n", o.Port)
	fmt.Fprintf(&b, "# Memory profile (budget ~%d MB)\n", o.Memory.BudgetMB)
	fmt.Fprintf(&b, "POSTGRES_MAX_CONNECTIONS=%d\n", fixed.MaxConnections)
	fmt.Fprintf(&b, "POSTGRES_SHARED_BUFFERS=%dMB\n", o.Memory.SharedBuffersMB)
	fmt.Fprintf(&b, "POSTGRES_EFFECTIVE_CACHE_SIZE=%dMB\n", o.Memory.EffectiveCacheSizeMB)
	fmt.Fprintf(&b, "POSTGRES_WORK_MEM=%dMB\n", o.Memory.WorkMemMB)
	fmt.Fprintf(&b, "POSTGRES_MAINTENANCE_WORK_MEM=%dMB\n", o.Memory.MaintenanceWorkMemMB)
	fmt.Fprintf(&b, "POSTGRES_AUTOVACUUM_WORK_MEM=%dMB\n\n", o.Memory.AutovacuumWorkMemMB)
	fmt.Fprintf(&b, "POSTGRES_RANDOM_PAGE_COST=%s\n", formatFloat(io.RandomPageCost))
	fmt.Fprintf(&b, "POSTGRES_EFFECTIVE_IO_CONCURRENCY=%d\n", io.EffectiveIOConcurrency)
	fmt.Fprintf(&b, "POSTGRES_DEFAULT_STATISTICS_TARGET=%d\n\n", fixed.DefaultStatisticsTarget)
	fmt.Fprintf(&b, "POSTGRES_WAL_COMPRESSION=%s\n", fixed.WALCompression)
	fmt.Fprintf(&b, "POSTGRES_MIN_WAL_SIZE=%dGB\n", fixed.MinWALSizeGB)
	fmt.Fprintf(&b, "POSTGRES_MAX_WAL_SIZE=%dGB\n", fixed.MaxWALSizeGB)
	fmt.Fprintf(&b, "POSTGRES_CHECKPOINT_COMPLETION_TARGET=%s\n\n", formatFloat(fixed.CheckpointCompletionTarget))
	fmt.Fprintf(&b, "POSTGRES_AUTOVACUUM_MAX_WORKERS=%d\n", fixed.AutovacuumMaxWorkers)
	fmt.Fprintf(&b, "POSTGRES_AUTOVACUUM_NAPTIME=%ds\n", fixed.AutovacuumNaptimeSeconds)
	fmt.Fprintf(&b, "POSTGRES_AUTOVACUUM_VACUUM_SCALE_FACTOR=%s\n", formatFloat(fixed.AutovacuumVacuumScaleFactor))
	fmt.Fprintf(&b, "POSTGRES_AUTOVACUUM_ANALYZE_SCALE_FACTOR=%s\n", formatFloat(fixed.AutovacuumAnalyzeScaleFactor))
	return b.String(), nil
}

// SetupInstructions returns the concise post-generation steps from DB Phase
// 1A, so the wizard can render them next to the generated files.
func SetupInstructions() []string {
	return []string{
		"Create a directory for the PostgreSQL stack.",
		"Save the generated YAML as compose.yaml.",
		"Save the generated variables as .env.",
		"Start PostgreSQL: docker compose up -d",
		"Wait until the PostgreSQL container reports healthy.",
		"Return here and click Test / Validate Connection.",
	}
}

func formatFloat(f float64) string {
	s := fmt.Sprintf("%.4f", f)
	s = strings.TrimRight(s, "0")
	s = strings.TrimRight(s, ".")
	if s == "" || s == "-" {
		s = "0"
	}
	return s
}

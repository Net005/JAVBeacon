package config

import (
	"errors"
	"os"
	"strconv"
	"strings"
	"time"
)

// Supported values for Config.DatabaseEngine.
const (
	EngineSQLite   = "sqlite"
	EnginePostgres = "postgres"
)

type Config struct {
	ListenAddress        string        `json:"listen_address"`
	DatabasePath         string        `json:"database_path"`
	CoverDirectory       string        `json:"cover_directory"`
	APIKey               string        `json:"api_key"`
	AkibaBaseURL         string        `json:"akiba_base_url"`
	AkibaPath            string        `json:"akiba_releases_path"`
	FlareSolverrURL      string        `json:"FlareSolverrUrl"`
	FlareSolverrCooldown float64       `json:"FlareSolverrCooldown"`
	RefreshEvery         time.Duration `json:"-"`
	RefreshText          string        `json:"refresh_interval"`
	PageLimit            int           `json:"page_limit"`
	RequestTimeout       time.Duration `json:"-"`

	// DatabaseEngine selects which persistence backend the application
	// connects to at startup. It must be resolvable before any database
	// connection is opened, so (unlike most application settings) it can
	// only come from configuration/environment, never from the DB-backed
	// settings table. Defaults to EngineSQLite for backward compatibility
	// with existing installs.
	DatabaseEngine string `json:"database_engine"`

	// PostgreSQL connection settings. Only meaningful when
	// DatabaseEngine == EnginePostgres. PostgresPassword must never be
	// logged, echoed back over the API, or included in diagnostics -
	// see Redacted().
	PostgresHost     string `json:"postgres_host"`
	PostgresPort     int    `json:"postgres_port"`
	PostgresDatabase string `json:"postgres_database"`
	PostgresUser     string `json:"postgres_user"`
	PostgresPassword string `json:"-"`
	PostgresSSLMode  string `json:"postgres_sslmode"`
}

// Redacted returns a copy of the config safe to log or return from a
// diagnostics endpoint: the PostgreSQL password is replaced with a fixed
// placeholder rather than included verbatim. Nothing in this package or
// internal/web should ever log/serialize a Config value directly - use
// this instead. See Codex Database Working Rule #8 ("Never expose
// database passwords or connection secrets in logs").
func (c Config) Redacted() Config {
	if c.PostgresPassword != "" {
		c.PostgresPassword = "REDACTED"
	}
	return c
}

func Load() (Config, error) {
	c := Config{
		ListenAddress:  ":8080",
		DatabasePath:   "data/javbeacon.db",
		CoverDirectory: "data/covers",
		AkibaBaseURL:   "https://www.akiba-web.com",
		AkibaPath:      "/search/index.php?count=1&year=&month=&day=&narrow=&salesform_id=&tag_id=&actor_id=&series_id=&label_id=&sort=1&s_type=&keyword=",
		// Byparr is the recommended FlareSolverr-compatible solver. Compose
		// overrides this loopback address with its internal service hostname.
		FlareSolverrURL:      "http://127.0.0.1:8191/v1",
		FlareSolverrCooldown: 7.49,
		RefreshText:          "1h",
		PageLimit:            5,
		RequestTimeout:       30 * time.Second,
		DatabaseEngine:       EngineSQLite,
		PostgresHost:         "127.0.0.1",
		PostgresPort:         5432,
		PostgresDatabase:     "javbeacon",
		PostgresUser:         "javbeacon",
		PostgresSSLMode:      "prefer",
	}
	if v := os.Getenv("JAVBEACON_LISTEN"); v != "" {
		c.ListenAddress = v
	}
	if v := os.Getenv("JAVBEACON_DB"); v != "" {
		c.DatabasePath = v
	}
	if v := os.Getenv("JAVBEACON_COVERS"); v != "" {
		c.CoverDirectory = v
	}
	if v := os.Getenv("JAVBEACON_API_KEY"); v != "" {
		c.APIKey = v
	}
	if v := os.Getenv("JAVBEACON_FLARESOLVERR_URL"); v != "" {
		c.FlareSolverrURL = v
	}
	if v := os.Getenv("JAVBEACON_PAGE_LIMIT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.PageLimit = n
		}
	}
	if v := strings.TrimSpace(os.Getenv("JAVBEACON_DB_ENGINE")); v != "" {
		c.DatabaseEngine = strings.ToLower(v)
	}
	if v := os.Getenv("JAVBEACON_DB_HOST"); v != "" {
		c.PostgresHost = v
	}
	if v := os.Getenv("JAVBEACON_DB_PORT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.PostgresPort = n
		} else {
			return c, errors.New("invalid JAVBEACON_DB_PORT: " + err.Error())
		}
	}
	if v := os.Getenv("JAVBEACON_DB_NAME"); v != "" {
		c.PostgresDatabase = v
	}
	if v := os.Getenv("JAVBEACON_DB_USER"); v != "" {
		c.PostgresUser = v
	}
	if v := os.Getenv("JAVBEACON_DB_PASSWORD"); v != "" {
		c.PostgresPassword = v
	}
	if v := os.Getenv("JAVBEACON_DB_SSLMODE"); v != "" {
		c.PostgresSSLMode = v
	}
	var err error
	c.RefreshEvery, err = time.ParseDuration(c.RefreshText)
	if err != nil {
		return c, errors.New("invalid refresh_interval: " + err.Error())
	}
	if c.PageLimit < 1 {
		c.PageLimit = 1
	}
	if c.DatabaseEngine != EngineSQLite && c.DatabaseEngine != EnginePostgres {
		return c, errors.New("invalid JAVBEACON_DB_ENGINE: must be \"sqlite\" or \"postgres\", got " + c.DatabaseEngine)
	}
	if c.DatabaseEngine == EnginePostgres {
		if c.PostgresHost == "" || c.PostgresDatabase == "" || c.PostgresUser == "" {
			return c, errors.New("JAVBEACON_DB_ENGINE=postgres requires JAVBEACON_DB_HOST, JAVBEACON_DB_NAME and JAVBEACON_DB_USER to be set")
		}
		if c.PostgresPassword == "" {
			return c, errors.New("JAVBEACON_DB_ENGINE=postgres requires JAVBEACON_DB_PASSWORD to be set")
		}
		if c.PostgresPort < 1 || c.PostgresPort > 65535 {
			return c, errors.New("invalid JAVBEACON_DB_PORT: must be between 1 and 65535")
		}
	}
	return c, nil
}

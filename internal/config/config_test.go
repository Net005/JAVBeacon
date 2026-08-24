package config

import "testing"

// clearDBEnv resets every JAVBEACON_DB_* var Load() reads so tests don't
// leak state through the process environment via t.Setenv.
func clearDBEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"JAVBEACON_DB_ENGINE",
		"JAVBEACON_DB_HOST",
		"JAVBEACON_DB_PORT",
		"JAVBEACON_DB_NAME",
		"JAVBEACON_DB_USER",
		"JAVBEACON_DB_PASSWORD",
		"JAVBEACON_DB_SSLMODE",
	} {
		t.Setenv(k, "")
	}
}

func TestLoadDefaultsToSQLiteEngine(t *testing.T) {
	clearDBEnv(t)
	c, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if c.DatabaseEngine != EngineSQLite {
		t.Fatalf("DatabaseEngine = %q, want %q", c.DatabaseEngine, EngineSQLite)
	}
	if c.PostgresPort != 5432 {
		t.Fatalf("PostgresPort default = %d, want 5432", c.PostgresPort)
	}
	if c.PostgresSSLMode != "prefer" {
		t.Fatalf("PostgresSSLMode default = %q, want %q", c.PostgresSSLMode, "prefer")
	}
}

func TestLoadRejectsUnknownEngine(t *testing.T) {
	clearDBEnv(t)
	t.Setenv("JAVBEACON_DB_ENGINE", "mysql")
	if _, err := Load(); err == nil {
		t.Fatal("expected error for unsupported engine, got nil")
	}
}

func TestLoadPostgresRequiresCredentials(t *testing.T) {
	clearDBEnv(t)
	t.Setenv("JAVBEACON_DB_ENGINE", "postgres")
	// No host/name/user/password set - should fail with an actionable error.
	if _, err := Load(); err == nil {
		t.Fatal("expected error for postgres engine missing credentials, got nil")
	}
	t.Setenv("JAVBEACON_DB_HOST", "db.internal")
	t.Setenv("JAVBEACON_DB_NAME", "javbeacon")
	t.Setenv("JAVBEACON_DB_USER", "javbeacon")
	if _, err := Load(); err == nil {
		t.Fatal("expected error for postgres engine missing password, got nil")
	}
	t.Setenv("JAVBEACON_DB_PASSWORD", "s3cret")
	c, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if c.DatabaseEngine != EnginePostgres || c.PostgresHost != "db.internal" || c.PostgresPassword != "s3cret" {
		t.Fatalf("unexpected config: %+v", c.Redacted())
	}
}

func TestLoadRejectsInvalidPostgresPort(t *testing.T) {
	clearDBEnv(t)
	t.Setenv("JAVBEACON_DB_ENGINE", "postgres")
	t.Setenv("JAVBEACON_DB_HOST", "db.internal")
	t.Setenv("JAVBEACON_DB_NAME", "javbeacon")
	t.Setenv("JAVBEACON_DB_USER", "javbeacon")
	t.Setenv("JAVBEACON_DB_PASSWORD", "s3cret")
	t.Setenv("JAVBEACON_DB_PORT", "not-a-number")
	if _, err := Load(); err == nil {
		t.Fatal("expected error for non-numeric JAVBEACON_DB_PORT, got nil")
	}
	t.Setenv("JAVBEACON_DB_PORT", "70000")
	if _, err := Load(); err == nil {
		t.Fatal("expected error for out-of-range JAVBEACON_DB_PORT, got nil")
	}
}

func TestRedactedHidesPassword(t *testing.T) {
	c := Config{DatabaseEngine: EnginePostgres, PostgresPassword: "s3cret"}
	r := c.Redacted()
	if r.PostgresPassword == "s3cret" {
		t.Fatal("Redacted() must not return the plaintext password")
	}
	if c.PostgresPassword != "s3cret" {
		t.Fatal("Redacted() must not mutate the receiver")
	}
}

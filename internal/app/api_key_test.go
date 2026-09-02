package app

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/Net005/JAVBeacon/internal/config"
	"github.com/Net005/JAVBeacon/internal/logging"
	"github.com/Net005/JAVBeacon/internal/store"
)

// TestFinishStartupGeneratesRandomAPIKeyWhenUnset covers the
// JAVBEACON_API_KEY UI-configurability change: a fresh install with no env
// var and no prior "api_key" setting must end up with a random key seeded
// into the settings table (so it shows up, editable, in Settings) rather
// than an empty/disabled key.
func TestFinishStartupGeneratesRandomAPIKeyWhenUnset(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "javbeacon.db")
	st, err := store.OpenSQLite(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(logging.NewRing(slog.NewTextHandler(io.Discard, nil), 100))
	a, err := finishStartup(config.Config{DatabasePath: dbPath, RefreshText: "1h"}, log, logging.NewRing(slog.NewTextHandler(io.Discard, nil), 100), st)
	if err != nil {
		t.Fatalf("finishStartup: %v", err)
	}
	defer a.store.Close()

	settings, err := a.store.Settings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	key := settings["api_key"]
	if len(key) < 16 {
		t.Fatalf("expected a random api_key to be seeded, got %q", key)
	}
}

// TestFinishStartupReusesGeneratedAPIKeyAcrossRestarts makes sure the
// random default is only ever generated once - a second startup against
// the same database must not silently rotate the key out from under
// anything already configured to use it.
func TestFinishStartupReusesGeneratedAPIKeyAcrossRestarts(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "javbeacon.db")
	st, err := store.OpenSQLite(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(logging.NewRing(slog.NewTextHandler(io.Discard, nil), 100))
	a, err := finishStartup(config.Config{DatabasePath: dbPath, RefreshText: "1h"}, log, logging.NewRing(slog.NewTextHandler(io.Discard, nil), 100), st)
	if err != nil {
		t.Fatalf("finishStartup (first start): %v", err)
	}
	settings, err := a.store.Settings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	first := settings["api_key"]
	a.store.Close()

	st2, err := store.OpenSQLite(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	a2, err := finishStartup(config.Config{DatabasePath: dbPath, RefreshText: "1h"}, log, logging.NewRing(slog.NewTextHandler(io.Discard, nil), 100), st2)
	if err != nil {
		t.Fatalf("finishStartup (second start): %v", err)
	}
	defer a2.store.Close()
	settings2, err := a2.store.Settings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if settings2["api_key"] != first {
		t.Fatalf("expected the generated api_key to survive a restart unchanged, got %q then %q", first, settings2["api_key"])
	}
}

// TestFinishStartupSeedsAPIKeyFromEnvVarWhenSet keeps upgrades from
// existing installs working unchanged: JAVBEACON_API_KEY (surfaced here as
// cfg.APIKey, exactly as config.Load populates it) still seeds the initial
// key on a database that has never had one saved.
func TestFinishStartupSeedsAPIKeyFromEnvVarWhenSet(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "javbeacon.db")
	st, err := store.OpenSQLite(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(logging.NewRing(slog.NewTextHandler(io.Discard, nil), 100))
	a, err := finishStartup(config.Config{DatabasePath: dbPath, RefreshText: "1h", APIKey: "my-legacy-env-key"}, log, logging.NewRing(slog.NewTextHandler(io.Discard, nil), 100), st)
	if err != nil {
		t.Fatalf("finishStartup: %v", err)
	}
	defer a.store.Close()

	settings, err := a.store.Settings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if settings["api_key"] != "my-legacy-env-key" {
		t.Fatalf("expected JAVBEACON_API_KEY to seed the initial api_key, got %q", settings["api_key"])
	}
}

// TestFinishStartupKeepsExplicitlyClearedAPIKey makes sure a user who
// deliberately saved an empty api_key (disabling API-key auth) via
// Settings does not have it silently regenerated on the next restart -
// "never configured" (key absent) and "configured as empty" must stay
// distinct.
func TestFinishStartupKeepsExplicitlyClearedAPIKey(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "javbeacon.db")
	st, err := store.OpenSQLite(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SaveSettings(context.Background(), map[string]string{"api_key": ""}); err != nil {
		t.Fatal(err)
	}
	st.Close()

	st2, err := store.OpenSQLite(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(logging.NewRing(slog.NewTextHandler(io.Discard, nil), 100))
	a, err := finishStartup(config.Config{DatabasePath: dbPath, RefreshText: "1h"}, log, logging.NewRing(slog.NewTextHandler(io.Discard, nil), 100), st2)
	if err != nil {
		t.Fatalf("finishStartup: %v", err)
	}
	defer a.store.Close()

	settings, err := a.store.Settings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := settings["api_key"]; !ok || v != "" {
		t.Fatalf("expected the explicitly-cleared api_key to remain empty, got (present=%v) %q", ok, v)
	}
}

package app

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/Net005/JAVBeacon/internal/config"
	"github.com/Net005/JAVBeacon/internal/logging"
	"github.com/Net005/JAVBeacon/internal/store"
)

// TestFinishStartupMigratesLegacyMissingPathRemapPair covers Task C's
// Missing Library Files path remap change: the scan used to read a single
// stash_missing_path_from/stash_missing_path_to pair, and now reads only an
// ordered list held in stash_missing_path_remaps (JSON-encoded
// []stash.PathRemap). An install that already had the old pair configured
// must have it converted automatically on the next startup, or its remap
// would silently stop working the moment this change deploys.
func TestFinishStartupMigratesLegacyMissingPathRemapPair(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "javbeacon.db")
	st, err := store.OpenSQLite(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	// Seed settings as they were seeded on an existing install before
	// stash_missing_path_remaps existed, then close so finishStartup
	// re-opens/reads them fresh, exactly like a real restart would.
	if err := st.SaveSettings(context.Background(), map[string]string{
		"stash_missing_path_from": "/stash-mount",
		"stash_missing_path_to":   "/local-mount",
	}); err != nil {
		t.Fatal(err)
	}
	st.Close()

	st, err = store.OpenSQLite(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(logging.NewRing(slog.NewTextHandler(io.Discard, nil), 100))
	a, err := finishStartup(config.Config{DatabasePath: dbPath, PageLimit: 3, RefreshText: "1h"}, log, logging.NewRing(slog.NewTextHandler(io.Discard, nil), 100), st)
	if err != nil {
		t.Fatalf("finishStartup: %v", err)
	}
	defer a.store.Close()

	settings, err := a.store.Settings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var remaps []struct {
		From string `json:"from"`
		To   string `json:"to"`
	}
	if err := json.Unmarshal([]byte(settings["stash_missing_path_remaps"]), &remaps); err != nil {
		t.Fatalf("stash_missing_path_remaps is not valid JSON: %v (value=%q)", err, settings["stash_missing_path_remaps"])
	}
	if len(remaps) != 1 || remaps[0].From != "/stash-mount" || remaps[0].To != "/local-mount" {
		t.Fatalf("expected the legacy from/to pair to be migrated into a single remap, got %+v", remaps)
	}
}

// TestFinishStartupDefaultsMissingPathRemapsToEmptyListWhenNothingConfigured
// makes sure a fresh install (no legacy pair, no remaps) ends up with a
// valid empty JSON array rather than an empty string, so the frontend's
// JSON.parse of the setting never fails on first load.
func TestFinishStartupDefaultsMissingPathRemapsToEmptyListWhenNothingConfigured(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "javbeacon.db")
	st, err := store.OpenSQLite(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(logging.NewRing(slog.NewTextHandler(io.Discard, nil), 100))
	a, err := finishStartup(config.Config{DatabasePath: dbPath, PageLimit: 3, RefreshText: "1h"}, log, logging.NewRing(slog.NewTextHandler(io.Discard, nil), 100), st)
	if err != nil {
		t.Fatalf("finishStartup: %v", err)
	}
	defer a.store.Close()

	settings, err := a.store.Settings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var remaps []any
	if err := json.Unmarshal([]byte(settings["stash_missing_path_remaps"]), &remaps); err != nil {
		t.Fatalf("stash_missing_path_remaps is not valid JSON: %v (value=%q)", err, settings["stash_missing_path_remaps"])
	}
	if len(remaps) != 0 {
		t.Fatalf("expected no remaps for a fresh install, got %+v", remaps)
	}
	for key, want := range map[string]string{
		"new_release_refresh_enabled":  "true",
		"quick_refresh_enabled":        "true",
		"full_refresh_enabled":         "false",
		"job_priority_scheduled_new":   "15",
		"job_priority_scheduled_quick": "16",
		"job_priority_scheduled_full":  "17",
	} {
		if got := settings[key]; got != want {
			t.Errorf("%s=%q, want %q", key, got, want)
		}
	}
}

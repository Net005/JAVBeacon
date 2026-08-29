package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Net005/JAVBeacon/internal/config"
	"github.com/Net005/JAVBeacon/internal/store"
)

func TestStartupMigrationProgressStatusAndPage(t *testing.T) {
	tracker := newStartupMigrationTracker(config.Config{
		DatabaseEngine:   config.EnginePostgres,
		PostgresHost:     "postgres",
		PostgresPort:     5432,
		PostgresDatabase: "javbeacon",
		PostgresUser:     "javbeacon",
	})
	tracker.beginAttempt(2)
	tracker.record(store.MigrationProgress{Phase: "Database schema", Step: "Preparing database index idx_releases_title_trgm", Current: 7, Total: 10})

	statusReq := httptest.NewRequest(http.MethodGet, "/api/startup/status", nil)
	statusResponse := httptest.NewRecorder()
	tracker.handler().ServeHTTP(statusResponse, statusReq)
	var status startupMigrationStatus
	if err := json.Unmarshal(statusResponse.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status.Phase != "Database schema" || status.Current != 7 || status.Total != 10 || status.Attempt != 2 {
		t.Fatalf("unexpected startup migration status: %+v", status)
	}

	pageReq := httptest.NewRequest(http.MethodGet, "/", nil)
	pageResponse := httptest.NewRecorder()
	tracker.handler().ServeHTTP(pageResponse, pageReq)
	if pageResponse.Code != http.StatusServiceUnavailable || !strings.Contains(pageResponse.Body.String(), "Preparing your database") || !strings.Contains(pageResponse.Body.String(), "/api/startup/status") {
		t.Fatalf("unexpected startup page: status=%d body=%q", pageResponse.Code, pageResponse.Body.String())
	}
}

func TestRecoveryStatusIncludesMigrationProgress(t *testing.T) {
	recovery := newRecoveryServer(config.Config{DatabaseEngine: config.EnginePostgres, PostgresHost: "postgres", PostgresPort: 5432, PostgresDatabase: "javbeacon", PostgresUser: "javbeacon"}, nil)
	recovery.recordMigration(store.MigrationProgress{Phase: "Release preferences", Step: "Updating saved filters", Current: 1, Total: 2})
	status := recovery.status()
	if !status.Migrating || status.Phase != "Release preferences" || status.Step != "Updating saved filters" || status.Current != 1 || status.Total != 2 {
		t.Fatalf("unexpected recovery migration status: %+v", status)
	}
}

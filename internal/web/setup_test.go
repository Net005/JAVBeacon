package web

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Net005/JAVBeacon/internal/store"
)

func newSetupTestServer(t *testing.T) *Server {
	t.Helper()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "setup.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return &Server{store: st, dbEngine: "sqlite", log: slog.Default()}
}

func TestSetupDBStatusReportsActiveEngine(t *testing.T) {
	s := newSetupTestServer(t)
	rec := httptest.NewRecorder()
	s.setupDBStatus(rec, httptest.NewRequest(http.MethodGet, "/api/setup/db/status", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var out struct {
		ActiveEngine string            `json:"active_engine"`
		Staged       map[string]string `json:"staged"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.ActiveEngine != "sqlite" {
		t.Fatalf("active_engine = %q, want sqlite", out.ActiveEngine)
	}
	if len(out.Staged) != 0 {
		t.Fatalf("expected no staged selections yet, got %+v", out.Staged)
	}
}

func TestSetupDBOptionsIncludesLargeLibraryDefault(t *testing.T) {
	s := newSetupTestServer(t)
	rec := httptest.NewRecorder()
	s.setupDBOptions(rec, httptest.NewRequest(http.MethodGet, "/api/setup/db/options", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var out map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out["recommended_engine"] != "postgres" {
		t.Fatalf("recommended_engine = %v, want postgres", out["recommended_engine"])
	}
	if out["default_memory_profile"] != "large" {
		t.Fatalf("default_memory_profile = %v, want large", out["default_memory_profile"])
	}
	if out["default_setup_method"] != "docker_compose" {
		t.Fatalf("default_setup_method = %v, want docker_compose", out["default_setup_method"])
	}
	presets, ok := out["memory_presets"].([]any)
	if !ok || len(presets) != 4 {
		t.Fatalf("memory_presets = %v, want 4 entries", out["memory_presets"])
	}
}

func doJSON(t *testing.T, handler func(http.ResponseWriter, *http.Request), method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(b)
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler(rec, req)
	return rec
}

func TestSetupDBGenerateHostTopologyDefaults(t *testing.T) {
	s := newSetupTestServer(t)
	rec := doJSON(t, s.setupDBGenerate, http.MethodPost, "/api/setup/db/generate", map[string]any{})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Compose  string `json:"compose"`
		Env      string `json:"env"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Password == "" {
		t.Fatal("expected a generated password")
	}
	if !bytes.Contains([]byte(out.Compose), []byte("image: postgres:18")) {
		t.Fatalf("compose missing postgres:18 image: %s", out.Compose)
	}
	if !bytes.Contains([]byte(out.Env), []byte("POSTGRES_PASSWORD="+out.Password)) {
		t.Fatalf("env missing generated password")
	}
}

func TestSetupDBGenerateRejectsPublicLANBind(t *testing.T) {
	s := newSetupTestServer(t)
	rec := doJSON(t, s.setupDBGenerate, http.MethodPost, "/api/setup/db/generate", map[string]any{
		"topology":     "lan",
		"bind_address": "0.0.0.0",
	})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422, body = %s", rec.Code, rec.Body.String())
	}
}

func TestSetupDBGeneratePasswordStableWithoutRegenerate(t *testing.T) {
	s := newSetupTestServer(t)
	first := doJSON(t, s.setupDBGenerate, http.MethodPost, "/api/setup/db/generate", map[string]any{})
	var firstOut struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(first.Body).Decode(&firstOut); err != nil {
		t.Fatal(err)
	}

	second := doJSON(t, s.setupDBGenerate, http.MethodPost, "/api/setup/db/generate", map[string]any{
		"password": firstOut.Password,
	})
	var secondOut struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(second.Body).Decode(&secondOut); err != nil {
		t.Fatal(err)
	}
	if secondOut.Password != firstOut.Password {
		t.Fatalf("expected password to be preserved across regenerate=false calls, got %q vs %q", firstOut.Password, secondOut.Password)
	}

	third := doJSON(t, s.setupDBGenerate, http.MethodPost, "/api/setup/db/generate", map[string]any{
		"password":            firstOut.Password,
		"regenerate_password": true,
	})
	var thirdOut struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(third.Body).Decode(&thirdOut); err != nil {
		t.Fatal(err)
	}
	if thirdOut.Password == firstOut.Password {
		t.Fatal("expected regenerate_password=true to produce a new password")
	}
}

func TestSetupDBTestConnectionReportsUnreachable(t *testing.T) {
	s := newSetupTestServer(t)
	// Port 1 is reserved and nothing should be listening there in the test
	// sandbox; this exercises the "connection failed" branch without
	// depending on any real PostgreSQL server.
	rec := doJSON(t, s.setupDBTestConnection, http.MethodPost, "/api/setup/db/test-connection", map[string]any{
		"host":     "127.0.0.1",
		"port":     1,
		"database": "javbeacon",
		"user":     "javbeacon",
		"password": "unmistakable-test-password-marker",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Connected bool   `json:"connected"`
		Category  string `json:"category"`
		Message   string `json:"message"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Connected {
		t.Fatal("expected port 1 to be reported as a failed connection")
	}
	if out.Category == "" {
		t.Fatal("expected a non-empty error category")
	}
	if strings.Contains(out.Message, "unmistakable-test-password-marker") {
		t.Fatalf("response leaked the password: %s", out.Message)
	}
}

func TestSetupDBTestConnectionValidatesInput(t *testing.T) {
	s := newSetupTestServer(t)
	rec := doJSON(t, s.setupDBTestConnection, http.MethodPost, "/api/setup/db/test-connection", map[string]any{
		"host":     "",
		"port":     5432,
		"database": "javbeacon",
		"user":     "javbeacon",
	})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", rec.Code)
	}
}

func TestSetupDBSavePersistsSelectionsWithoutPassword(t *testing.T) {
	s := newSetupTestServer(t)
	rec := doJSON(t, s.setupDBSave, http.MethodPost, "/api/setup/db/save", map[string]any{
		"engine":          "postgres",
		"host":            "db.internal",
		"port":            5432,
		"database":        "javbeacon",
		"user":            "javbeacon",
		"sslmode":         "require",
		"setup_method":    "docker_compose",
		"topology":        "host",
		"data_path":       "./postgres-data",
		"memory_profile":  "large",
		"storage_profile": "ssd",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	statusRec := httptest.NewRecorder()
	s.setupDBStatus(statusRec, httptest.NewRequest(http.MethodGet, "/api/setup/db/status", nil))
	var out struct {
		Staged map[string]string `json:"staged"`
	}
	if err := json.NewDecoder(statusRec.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Staged["pg_host"] != "db.internal" || out.Staged[pgSetupKeyDatabase] != "javbeacon" {
		t.Fatalf("staged selections not persisted correctly: %+v", out.Staged)
	}
	if _, ok := out.Staged["password"]; ok {
		t.Fatal("password must never be persisted")
	}
	// Belt-and-suspenders: confirm the raw settings table never picked up
	// a password-shaped key from this handler.
	raw, err := s.store.Settings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for k := range raw {
		if k == "pg_password" || k == "password" {
			t.Fatalf("settings table must never contain a persisted PostgreSQL password, found key %q", k)
		}
	}
}

func TestSetupDBSaveRejectsUnknownEngine(t *testing.T) {
	s := newSetupTestServer(t)
	rec := doJSON(t, s.setupDBSave, http.MethodPost, "/api/setup/db/save", map[string]any{"engine": "mysql"})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", rec.Code)
	}
}

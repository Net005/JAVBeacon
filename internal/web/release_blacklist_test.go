package web

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Net005/JAVBeacon/internal/covers"
	"github.com/Net005/JAVBeacon/internal/monitor"
	"github.com/Net005/JAVBeacon/internal/scraper"
	"github.com/Net005/JAVBeacon/internal/store"
)

func TestReleaseSafeguardSettingsValidation(t *testing.T) {
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "settings.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	cache, err := covers.New(t.TempDir(), time.Second, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	mon := monitor.New(st, nil, scraper.NewJavLibrary(time.Second, "", 0, slog.Default()), cache, 1, slog.Default(), time.Hour)
	s := &Server{store: st, covers: cache, monitor: mon}
	rec := httptest.NewRecorder()
	s.settings(rec, httptest.NewRequest("GET", "/api/settings", nil))
	if !strings.Contains(rec.Body.String(), `"download_min_duration_minutes":"10"`) || !strings.Contains(rec.Body.String(), `"download_min_filesize_mb":"200"`) {
		t.Fatalf("missing safeguard defaults: %s", rec.Body.String())
	}
	for _, raw := range []string{"-1", "NaN", "+Inf", "invalid"} {
		rec = httptest.NewRecorder()
		s.settings(rec, httptest.NewRequest("PUT", "/api/settings", strings.NewReader(`{"download_min_duration_minutes":"`+raw+`"}`)))
		if rec.Code != 400 {
			t.Fatalf("invalid threshold %q accepted: %d", raw, rec.Code)
		}
	}
	rec = httptest.NewRecorder()
	s.settings(rec, httptest.NewRequest("PUT", "/api/settings", strings.NewReader(`{"download_min_duration_minutes":"12.5","download_min_filesize_mb":"250"}`)))
	if rec.Code != 200 {
		t.Fatalf("save: %d %s", rec.Code, rec.Body.String())
	}
	values, err := st.Settings(context.Background())
	if err != nil || values["download_min_duration_minutes"] != "12.5" || values["download_min_filesize_mb"] != "250" {
		t.Fatalf("not persisted: %v %v", values, err)
	}
}

func TestReleaseBlacklistURLLifecycle(t *testing.T) {
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "blacklist.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	s := &Server{store: st, mux: http.NewServeMux()}
	s.routes()
	request := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		rec := httptest.NewRecorder()
		s.mux.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
		return rec
	}
	for i := 0; i < 2; i++ {
		rec := request("POST", "/api/blacklisted-releases", `{"url":"https://downloads.example.com/movie.mp4","reason":"Manual exclusion"}`)
		if rec.Code != 201 {
			t.Fatalf("blacklist URL: %d %s", rec.Code, rec.Body.String())
		}
	}
	rec := request("GET", "/api/blacklisted-releases?limit=25&offset=0", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"total":1`) || !strings.Contains(rec.Body.String(), "Manual exclusion") {
		t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
	}
	rec = request("DELETE", "/api/blacklisted-releases", `{"url":"https://downloads.example.com/movie.mp4"}`)
	if rec.Code != 200 {
		t.Fatalf("remove: %d %s", rec.Code, rec.Body.String())
	}
	rec = request("GET", "/api/blacklisted-releases", "")
	if !strings.Contains(rec.Body.String(), `"total":0`) {
		t.Fatal(rec.Body.String())
	}
	if _, err = st.Settings(context.Background()); err != nil {
		t.Fatal(err)
	}
}

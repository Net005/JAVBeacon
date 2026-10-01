package stash

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Net005/JAVBeacon/internal/store"
)

func TestSiloSavedFiltersSelectsScenesAndPreservesQuery(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("ApiKey") != "key" {
			t.Error("missing Stash key")
		}
		var req struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(req.Query, "findSavedFilters") {
			_, _ = w.Write([]byte(`{"data":{"findSavedFilters":[{"id":"7","name":"Scenes I Like","find_filter":{"q":"test","sort":"date","direction":"DESC"},"object_filter":{"title":{"value":"scene","modifier":"INCLUDES"}}},{"id":"8","name":"Other","find_filter":{},"object_filter":null}]}}`))
			return
		}
		find := req.Variables["findFilter"].(map[string]any)
		if find["q"] != "test" || find["sort"] != "date" || find["direction"] != "DESC" || find["per_page"] != float64(200) {
			t.Errorf("find filter=%v", find)
		}
		scene := req.Variables["sceneFilter"].(map[string]any)
		if _, ok := scene["title"]; !ok {
			t.Errorf("scene filter=%v", scene)
		}
		_, _ = w.Write([]byte(`{"data":{"findScenes":{"count":1,"scenes":[{"id":"42","code":"AB-1","title":"Scene","files":[{"path":"/movies/AB-1.mp4"}]}]}}}`))
	}))
	defer server.Close()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "stash.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.SaveSettings(context.Background(), map[string]string{"stash_base_url": server.URL, "stash_api_key": "key"}); err != nil {
		t.Fatal(err)
	}
	svc := New(st, time.Second, slog.Default(), nil, nil)
	if rows, err := svc.SiloSavedFilters(context.Background(), ""); err != nil || len(rows) != 0 || calls != 0 {
		t.Fatalf("empty selection rows=%v err=%v calls=%d", rows, err, calls)
	}
	rows, err := svc.SiloSavedFilters(context.Background(), "7")
	if err != nil || len(rows) != 1 || rows[0].ID != "7" || len(rows[0].Items) != 1 || rows[0].Items[0].Path != "/movies/AB-1.mp4" {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}
	if _, err := svc.SiloSavedFilters(context.Background(), "missing"); err == nil {
		t.Fatal("unknown filter accepted")
	}
}

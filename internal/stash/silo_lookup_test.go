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

func TestSiloLookupMatchesExactStashFilenameWithoutSceneCode(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Query string `json:"query"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if strings.Contains(request.Query, "findScene(") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":{"findScene":{"id":"11631","title":"Scene","code":"","details":"Description","date":"2020-01-02","performers":[{"id":"9","name":"One","image_path":"/portrait.jpg","birthdate":"1990-01-01"}],"tags":[{"name":"Drama"}],"files":[{"path":"/collections/jav/ad-359.avi"}],"paths":{"screenshot":"/screenshot.jpg"}}}}`))
			return
		}
		if !strings.Contains(request.Query, `q: "AD-359"`) {
			t.Errorf("unexpected query: %s", request.Query)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"findScenes":{"scenes":[{"id":"11631","code":"","title":"Scene","files":[{"path":"/collections/jav/ad-359.avi"}]},{"id":"2","code":"OTHER-1","files":[{"path":"/collections/jav/other-1.avi"}]}]}}}`))
	}))
	defer server.Close()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "lookup.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.SaveSettings(context.Background(), map[string]string{"stash_base_url": server.URL, "stash_api_key": "test"}); err != nil {
		t.Fatal(err)
	}
	svc := New(st, time.Second, slog.Default(), nil, nil)
	scenes, err := svc.SearchSiloScenes(context.Background(), "AD-359")
	if err != nil || len(scenes) != 1 || scenes[0].ID != "11631" || !strings.EqualFold(scenes[0].Code, "AD-359") {
		t.Fatalf("scenes=%+v err=%v", scenes, err)
	}
	full, err := svc.SiloSceneByID(context.Background(), "11631")
	if err != nil || full.Code != "ad-359" || len(full.Performers) != 1 || full.Performers[0].ImagePath != "/portrait.jpg" || full.Performers[0].Birthdate != "1990-01-01" {
		t.Fatalf("full=%+v err=%v", full, err)
	}
}

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
		_, _ = w.Write([]byte(`{"data":{"findScenes":{"scenes":[{"id":"11631","code":"","title":"Scene","details":"Story","date":"2020-01-02","studio":{"name":"Studio"},"performers":[{"id":"9","name":"One"}],"tags":[{"id":"tag1","name":"Drama"}],"paths":{"screenshot":"/screenshot.jpg"},"play_count":2,"o_counter":1,"last_played_at":"2026-09-27T10:00:00Z","files":[{"path":"/collections/jav/ad-359.avi"}]},{"id":"2","code":"OTHER-1","files":[{"path":"/collections/jav/other-1.avi"}]}]}}}`))
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
	if err != nil || len(scenes) != 1 || scenes[0].ID != "11631" || !strings.EqualFold(scenes[0].Code, "AD-359") || scenes[0].PlayCount != 2 || scenes[0].OCounter != 1 || scenes[0].Details != "Story" || len(scenes[0].Performers) != 1 || scenes[0].ScreenshotURL != "/screenshot.jpg" {
		t.Fatalf("scenes=%+v err=%v", scenes, err)
	}
	full, err := svc.SiloSceneByID(context.Background(), "11631")
	if err != nil || full.Code != "ad-359" || len(full.Performers) != 1 || full.Performers[0].ImagePath != "/portrait.jpg" || full.Performers[0].Birthdate != "1990-01-01" {
		t.Fatalf("full=%+v err=%v", full, err)
	}
}

func TestSiloLookupFindsCompactStashCodeForHyphenatedFilename(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Query string `json:"query"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		calls++
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(request.Query, `q: "PMID008"`) {
			_, _ = w.Write([]byte(`{"data":{"findScenes":{"scenes":[{"id":"6650","code":"PMID008","title":"PMID008","files":[{"path":"/stash/PMID008.mp4"}]}]}}}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":{"findScenes":{"scenes":[]}}}`))
	}))
	defer server.Close()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "compact.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.SaveSettings(context.Background(), map[string]string{"stash_base_url": server.URL, "stash_api_key": "test"}); err != nil {
		t.Fatal(err)
	}
	svc := New(st, time.Second, slog.Default(), nil, nil)
	rows, err := svc.SearchSiloScenes(context.Background(), "PMID-008")
	if err != nil || calls != 2 || len(rows) != 1 || rows[0].ID != "6650" || rows[0].Code != "PMID-008" {
		t.Fatalf("rows=%+v calls=%d err=%v", rows, calls, err)
	}
}

func TestSiloWatchlistScenesReadsConfiguredStashTagAndUpdatedAt(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Query string `json:"query"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(request.Query, "updated_at tags { id } files { path }") || !strings.Contains(request.Query, `tags: { value: ["watch"], modifier: INCLUDES }`) {
			t.Fatalf("query omitted Stash update date or tag ID: %s", request.Query)
		}
		_, _ = w.Write([]byte(`{"data":{"findScenes":{"scenes":[{"id":"one","updated_at":"2026-09-27T10:00:00Z","tags":[{"id":"watch"}],"files":[{"path":"/media/one.mp4"}]},{"id":"two","updated_at":"2026-09-26T10:00:00Z","tags":[{"id":"other"}],"files":[{"path":"/media/two.mp4"}]}]}}}`))
	}))
	defer server.Close()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "watchlist.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.SaveSettings(context.Background(), map[string]string{"stash_base_url": server.URL, "stash_watchlist_tag_id": "watch"}); err != nil {
		t.Fatal(err)
	}
	svc := New(st, time.Second, slog.Default(), nil, nil)
	scenes, configured, err := svc.SiloWatchlistScenes(context.Background())
	if err != nil || !configured || len(scenes) != 1 || scenes["one"].UTC().Format(time.RFC3339) != "2026-09-27T10:00:00Z" {
		t.Fatalf("scenes=%+v configured=%v err=%v", scenes, configured, err)
	}
}

package stash

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Net005/JAVBeacon/internal/domain"
	"github.com/Net005/JAVBeacon/internal/store"
)

func TestFetchPlaybackStatsRetrievesEveryStashPage(t *testing.T) {
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		var request struct {
			Query string `json:"query"`
		}
		_ = json.NewDecoder(r.Body).Decode(&request)
		start, amount := 0, 250
		if strings.Contains(request.Query, ", page: 2") {
			start, amount = 250, 1
		}
		scenes := make([]map[string]any, 0, amount)
		for i := 0; i < amount; i++ {
			scenes = append(scenes, map[string]any{"id": fmt.Sprintf("scene-%03d", start+i), "play_history": []string{}, "o_history": []string{}, "files": []any{}})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"findScenes": map[string]any{"count": 251, "scenes": scenes}}})
	}))
	defer server.Close()

	stats, err := New(nil, 2*time.Second, slog.Default(), nil, nil).fetchPlaybackStats(context.Background(), server.URL, "")
	if err != nil {
		t.Fatal(err)
	}
	if requests != 2 || len(stats) != 251 {
		t.Fatalf("requests=%d scenes=%d, want two pages containing 251 scenes", requests, len(stats))
	}
}

func TestHistoryWritebackRequiresReviewAndUsesMatchOrder(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "history-review.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	play := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	orgasm := time.Date(2026, 8, 1, 10, 30, 0, 0, time.UTC)
	scene := domain.StashHistoryScene{StashSceneID: "old", ReleaseID: 4, VideoID: "ATID-803", Title: "Archived", JavLibraryURL: "https://www.javlibrary.com/en/?v=abc", FilePath: "/old/ATID-803.mp4", TotalPlaySeconds: 600}
	if err := st.UpsertStashHistory(ctx, scene, []time.Time{play}, []time.Time{orgasm}); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var mutations []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query string `json:"query"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if strings.Contains(req.Query, "findScenes") {
			_, _ = w.Write([]byte(`{"data":{"findScenes":{"scenes":[{"id":"new","title":"Current","code":"OTHER-1","urls":["https://www.javlibrary.com/en/?v=abc"],"play_duration":0,"play_history":[],"o_history":[],"files":[{"path":"/new/other.mp4"}]}]}}}`))
			return
		}
		mu.Lock()
		mutations = append(mutations, req.Query)
		mu.Unlock()
		_, _ = w.Write([]byte(`{"data":{"ok":true}}`))
	}))
	defer server.Close()
	if err := st.SaveSettings(ctx, map[string]string{"stash_base_url": server.URL}); err != nil {
		t.Fatal(err)
	}
	svc := New(st, 2*time.Second, slog.Default(), nil, nil)
	review, err := svc.ReviewHistoryWriteback(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if review.Changes != 1 || review.Items[0].MatchMethod != "JavLibrary URL" || review.Items[0].TargetSceneID != "new" {
		t.Fatalf("unexpected review: %+v", review)
	}
	result, err := svc.ApplyHistoryWriteback(ctx, review.Token)
	if err != nil {
		t.Fatal(err)
	}
	if result.Updated != 1 || result.Plays != 1 || result.Orgasms != 1 || result.Failed != 0 {
		t.Fatalf("unexpected apply result: %+v", result)
	}
	mu.Lock()
	defer mu.Unlock()
	joined := strings.Join(mutations, "\n")
	for _, want := range []string{"sceneAddPlay", "sceneAddO", "sceneSaveActivity"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %s mutation in %s", want, joined)
		}
	}
	if _, err := svc.ApplyHistoryWriteback(ctx, review.Token); err == nil {
		t.Fatal("review token was reusable")
	}
}

func TestHistoryWritebackReviewExcludesEventsAlreadyInStash(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "history-deduplicate.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	play := time.Date(2026, 8, 1, 10, 0, 0, 789000000, time.UTC)
	orgasm := time.Date(2026, 8, 1, 10, 30, 0, 456000000, time.UTC)
	if err := st.UpsertStashHistory(ctx, domain.StashHistoryScene{StashSceneID: "same", VideoID: "ATID-803", Title: "Already synced", FilePath: "/old/ATID-803.mp4", TotalPlaySeconds: 600}, []time.Time{play, play}, []time.Time{orgasm, orgasm}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"findScenes":{"scenes":[{"id":"same","title":"Already synced","code":"ATID-803","urls":[],"play_duration":600,"play_history":["2026-08-01T10:00:00Z"],"o_history":["2026-08-01T10:30:00Z"],"files":[{"path":"/new/ATID-803.mp4"}]}]}}}`))
	}))
	defer server.Close()
	if err := st.SaveSettings(ctx, map[string]string{"stash_base_url": server.URL}); err != nil {
		t.Fatal(err)
	}
	review, err := New(st, 2*time.Second, slog.Default(), nil, nil).ReviewHistoryWriteback(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if review.Changes != 0 || len(review.Items) != 1 || review.Items[0].Status != "matched" || len(review.Items[0].PlayTimes) != 0 || len(review.Items[0].OrgasmTimes) != 0 {
		t.Fatalf("already-synced events must not be listed as changes: %+v", review)
	}
}

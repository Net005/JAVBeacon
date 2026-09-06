package stash

import (
	"context"
	"encoding/json"
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

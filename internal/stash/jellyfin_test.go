package stash

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Net005/JAVBeacon/internal/store"
)

type jellyfinRoundTrip func(*http.Request) (*http.Response, error)

func (f jellyfinRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestJellyfinMutationsStayBehindJAVBeacon(t *testing.T) {
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "stash-jellyfin.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err = st.SaveSettings(context.Background(), map[string]string{"stash_base_url": "https://stash.invalid", "stash_api_key": "stash-secret"}); err != nil {
		t.Fatal(err)
	}
	var queries []string
	svc := New(st, time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, nil)
	svc.client.Transport = jellyfinRoundTrip(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("ApiKey") != "stash-secret" {
			t.Fatalf("missing Stash API key")
		}
		body, _ := io.ReadAll(r.Body)
		queries = append(queries, string(body))
		response := `{"data":{"sceneSaveActivity":true}}`
		if strings.Contains(string(body), "sceneAddO") {
			response = `{"data":{"sceneAddO":{"count":4,"history":[]}}}`
		}
		if strings.Contains(string(body), "findScene") {
			response = `{"data":{"findScene":{"id":"scene-1","o_counter":4,"play_count":2,"play_duration":125.5,"resume_time":44,"last_played_at":"2026-09-08T12:00:00Z","o_history":["2026-09-08T13:00:00Z"]}}}`
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(response)), Header: make(http.Header)}, nil
	})
	if err = svc.SaveJellyfinActivity(context.Background(), "scene-1", 44, 30); err != nil {
		t.Fatal(err)
	}
	if count, e := svc.AddJellyfinO(context.Background(), "scene-1", time.Date(2026, 9, 8, 13, 0, 0, 0, time.UTC)); e != nil || count != 4 {
		t.Fatalf("count=%d err=%v", count, e)
	}
	activity, err := svc.JellyfinActivity(context.Background(), "scene-1")
	if err != nil || activity.OCount != 4 || activity.PlayDuration != 125.5 || activity.ResumeTime != 44 {
		t.Fatalf("activity=%+v err=%v", activity, err)
	}
	if len(queries) != 3 || !strings.Contains(queries[0], "playDuration: 30.000") || !strings.Contains(queries[1], "sceneAddO") {
		t.Fatalf("queries=%v", queries)
	}
}

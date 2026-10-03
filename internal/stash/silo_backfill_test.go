package stash

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Net005/JAVBeacon/internal/store"
)

func TestSiloBackfillNeverDuplicatesExistingOrAmbiguousPlays(t *testing.T) {
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "backfill.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.SaveSettings(context.Background(), map[string]string{"stash_base_url": "https://stash.invalid"}); err != nil {
		t.Fatal(err)
	}
	svc := New(st, time.Second, slog.Default(), nil, nil)
	at := time.Date(2026, 10, 3, 3, 11, 14, 0, time.UTC)
	history := []string{}
	mutations := 0
	svc.client.Transport = stubRoundTrip(func(r *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(r.Body)
		var request struct {
			Query string `json:"query"`
		}
		if err := json.Unmarshal(body, &request); err != nil {
			t.Fatal(err)
		}
		response := ""
		if strings.Contains(request.Query, "findScene") {
			raw, _ := json.Marshal(map[string]any{"data": map[string]any{"findScene": map[string]any{"id": "42936", "play_count": len(history), "play_history": history}}})
			response = string(raw)
		} else if strings.Contains(request.Query, "sceneAddPlay") {
			mutations++
			history = append(history, at.Format(time.RFC3339))
			response = fmt.Sprintf(`{"data":{"sceneAddPlay":{"count":%d}}}`, len(history))
		} else {
			t.Fatalf("unexpected GraphQL %s", request.Query)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(response)), Header: make(http.Header)}, nil
	})
	plays := []SiloCompletedPlay{{SessionID: "session-1", EndedAt: at}}
	first, err := svc.BackfillSiloPlays(context.Background(), "42936", plays)
	if err != nil || first.Added != 1 || mutations != 1 {
		t.Fatalf("first=%+v mutations=%d err=%v", first, mutations, err)
	}
	second, err := svc.BackfillSiloPlays(context.Background(), "42936", plays)
	if err != nil || second.AlreadyPresent != 1 || mutations != 1 {
		t.Fatalf("retry=%+v mutations=%d err=%v", second, mutations, err)
	}
	history = []string{"2026-10-03T03:20:00Z"}
	third, err := svc.BackfillSiloPlays(context.Background(), "42936", plays)
	if err != nil || third.Skipped != 1 || mutations != 1 {
		t.Fatalf("ambiguous=%+v mutations=%d err=%v", third, mutations, err)
	}
}

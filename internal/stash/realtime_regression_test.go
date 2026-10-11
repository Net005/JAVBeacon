package stash

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Net005/JAVBeacon/internal/domain"
	"github.com/Net005/JAVBeacon/internal/store"
)

// Reject detail scans so this checks bounded hydration without timing a test.
type identityOnlyStore struct{ *store.SQLite }

func (s identityOnlyStore) Releases(ctx context.Context, f domain.ReleaseFilter) ([]domain.Release, error) {
	if f.StashSceneID == "" && f.VideoID == "" {
		return nil, errors.New("unexpected catalog detail scan")
	}
	return s.SQLite.Releases(ctx, f)
}

func TestRealtimeMatchAvoidsCatalogDetailScan(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "identities.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	site, _ := st.SaveSite(ctx, domain.Site{Title: "Test", Type: "Site", Name: "Test", Enabled: true})
	_, _ = st.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "ABC-00123", Title: "Match"})
	svc := New(identityOnlyStore{st}, time.Second, slog.Default(), nil, nil)
	for _, code := range []string{"ABC-00123", "abc123"} {
		got, err := svc.matchRealtimeRelease(ctx, "scene-new", &realtimeScene{Code: code})
		if err != nil || got == nil || got.VideoID != "ABC-00123" {
			t.Fatalf("code=%q got=%+v err=%v", code, got, err)
		}
	}
	got, err := svc.matchRealtimeRelease(ctx, "unknown", &realtimeScene{Code: "UNKNOWN-9"})
	if err != nil || got != nil {
		t.Fatalf("unmatched got=%+v err=%v", got, err)
	}
}

func TestRealtimePendingWatchlistFailureCanBeRetried(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "tag-retry.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	site, _ := st.SaveSite(ctx, domain.Site{Title: "Test", Type: "Site", Name: "Test", Enabled: true})
	_, _ = st.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "ABC-123", Title: "Match", Watchlist: true})
	svc := New(st, time.Second, slog.Default(), nil, nil)
	attempts := 0
	svc.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		buf, _ := io.ReadAll(r.Body)
		body := string(buf)
		response := `{"data":{"findScene":{"id":"scene-new","code":"ABC-123","tags":[]}}}`
		if strings.Contains(body, "sceneUpdate") {
			attempts++
			if attempts == 1 {
				return nil, errors.New("temporary tag failure")
			}
			response = `{"data":{"sceneUpdate":{"id":"scene-new"}}}`
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(response)), Header: make(http.Header)}, nil
	})
	settings := map[string]string{"stash_base_url": "https://stash.example.com", "stash_watchlist_tag_id": "watchlist"}
	if _, err := svc.syncRealtimeScene(ctx, settings, "scene-new"); err == nil {
		t.Fatal("tag write failure was reported as success")
	}
	releases, _ := st.Releases(ctx, domain.ReleaseFilter{VideoID: "ABC-123"})
	if !releases[0].Watchlist {
		t.Fatal("pending Watchlist was discarded")
	}
	if synced, _ := st.WatchlistSynced(ctx, releases[0].ID, "scene-new", "watchlist"); synced {
		t.Fatal("failed write marked synced")
	}
	if _, err := svc.syncRealtimeScene(ctx, settings, "scene-new"); err != nil {
		t.Fatal(err)
	}
	if synced, _ := st.WatchlistSynced(ctx, releases[0].ID, "scene-new", "watchlist"); !synced {
		t.Fatal("successful retry was not recorded")
	}
}

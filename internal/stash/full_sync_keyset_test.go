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

// Embedding only Store deliberately hides the optional narrow capability.
type fullSyncFallbackStore struct {
	store.Store
	db *store.SQLite
}

func (s fullSyncFallbackStore) UpsertStashHistory(ctx context.Context, r domain.StashHistoryScene, p, o []time.Time) error {
	return s.db.UpsertStashHistory(ctx, r, p, o)
}

type fullSyncNarrowStore struct {
	*store.SQLite
	snapshots, pages int
}

func (s *fullSyncNarrowStore) Releases(context.Context, domain.ReleaseFilter) ([]domain.Release, error) {
	return nil, errors.New("full release hydration forbidden")
}
func (s *fullSyncNarrowStore) ReleasesCount(context.Context, domain.ReleaseFilter) (int, error) {
	return 0, errors.New("unbounded count forbidden")
}
func (s *fullSyncNarrowStore) StashSyncSnapshot(ctx context.Context, f domain.ReleaseFilter) (int64, int, error) {
	s.snapshots++
	return s.SQLite.StashSyncSnapshot(ctx, f)
}
func (s *fullSyncNarrowStore) StashSyncReleases(ctx context.Context, f domain.ReleaseFilter, a, h int64, l int) ([]domain.Release, error) {
	s.pages++
	return s.SQLite.StashSyncReleases(ctx, f, a, h, l)
}

func TestFullSyncNarrowMatchesFallback(t *testing.T) {
	for _, test := range []struct {
		name   string
		narrow bool
		dates  []string
	}{
		{"fallback", false, []string{"2020-01-01", "2024-01-01", "2020-01-01"}},
		{"narrow", true, []string{"2020-01-01", "2024-01-01", "2020-01-01"}},
		{"fallback_missing_date", false, []string{"", "2024-01-01", ""}},
		{"narrow_missing_date", true, []string{"", "2024-01-01", ""}},
	} {
		t.Run(test.name, func(t *testing.T) {
			narrow := test.narrow
			ctx := context.Background()
			db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "sync.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if err := db.SaveSettings(ctx, map[string]string{"stash_base_url": "https://stash.example", "stash_watchlist_tag_id": "watchlist"}); err != nil {
				t.Fatal(err)
			}
			// Distinct provider identities can canonicalize to the same Stash code.
			// ID order intentionally disagrees with release-date order.
			for i, date := range test.dates {
				site, err := db.SaveSite(ctx, domain.Site{Title: []string{"First", "Second", "Third"}[i], Type: "Site", Name: []string{"First", "Second", "Third"}[i], Enabled: true})
				if err != nil {
					t.Fatal(err)
				}
				_, err = db.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "TEST-123", Title: []string{"Oldest first", "Newest", "Oldest tie"}[i], ReleaseDate: date, ProductURL: "https://www.javlibrary.com/?v=fixture"})
				if err != nil {
					t.Fatal(err)
				}
			}
			rows, err := db.Releases(ctx, domain.ReleaseFilter{Limit: 500})
			if err != nil || len(rows) != 3 {
				t.Fatalf("seed rows=%d err=%v", len(rows), err)
			}
			wantID := rows[len(rows)-1].ID
			var st store.Store = fullSyncFallbackStore{Store: db, db: db}
			spy := &fullSyncNarrowStore{SQLite: db}
			if narrow {
				st = spy
			}
			svc := &Service{store: st, client: &http.Client{}, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
			svc.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
				body, _ := io.ReadAll(r.Body)
				query := string(body)
				response := `{"data":{"findScenes":{"scenes":[{"id":"scene-1","code":"TEST-123","date":"2023-02-01","tags":[{"id":"watchlist"}]}]}}}`
				if strings.Contains(query, "JAVBeaconSceneCreatedAt") {
					response = `{"data":{"findScenes":{"scenes":[{"id":"scene-1","created_at":"2025-01-01T00:00:00Z","files":[{"path":"/media/example.mp4"}]}]}}}`
				}
				if strings.Contains(query, "JAVBeaconPlaybackStats") {
					response = `{"data":{"findScenes":{"count":1,"scenes":[{"id":"scene-1","o_counter":2,"play_count":3,"last_played_at":"2025-02-01T00:00:00Z","play_duration":120,"play_history":["2025-02-01T00:00:00Z"],"o_history":["2025-02-02T00:00:00Z"],"files":[{"path":"/media/example.mp4"}]}]}}}`
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(response)), Header: make(http.Header)}, nil
			})
			svc.run(ctx)
			status := svc.Status()
			if status.Error != "" || status.Total != 3 || status.Processed != 3 || status.Matched != 3 || status.Updated != 3 {
				t.Fatalf("status=%+v", status)
			}
			if narrow && (spy.snapshots != 1 || spy.pages != 1) {
				t.Fatalf("snapshot=%d pages=%d", spy.snapshots, spy.pages)
			}
			for _, before := range rows {
				got, err := db.Release(ctx, before.ID)
				if err != nil {
					t.Fatal(err)
				}
				if !got.Local || !got.Watchlist || got.StashSceneID != "scene-1" || got.StashFilePath != "/media/example.mp4" || got.StashCreatedAt.Format(time.RFC3339) != "2025-01-01T00:00:00Z" || got.StashReleaseDate != "2023-02-01" || got.OCounter != 2 || got.PlayCount != 3 || got.LastPlayedAt != "2025-02-01T00:00:00Z" || got.LastOCountAt != "2025-02-02T00:00:00Z" {
					t.Fatalf("metadata mismatch: %+v", got)
				}
			}
			// A second sync must compare every projected field without redundant
			// state, file, timestamp, date or playback writes.
			svc.publishStatus(Status{}) // Start normally resets per-run counters.
			svc.run(ctx)
			if second := svc.Status(); second.Error != "" || second.Updated != 0 || second.Processed != 3 {
				t.Fatalf("second sync=%+v", second)
			}
			history, err := db.StashHistoryScenes(ctx)
			if err != nil || len(history) != 1 {
				t.Fatalf("history=%+v err=%v", history, err)
			}
			h := history[0]
			if h.ReleaseID != wantID || h.Title != "Oldest first" || h.JavLibraryURL != "https://www.javlibrary.com/?v=fixture" || h.VideoID != "TEST-123" || h.PlayCount != 1 || h.OrgasmCount != 1 || h.TotalPlaySeconds != 120 {
				t.Fatalf("history representative=%+v want id=%d", h, wantID)
			}
		})
	}
}

package store

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/Net005/JAVBeacon/internal/domain"
)

func TestStashSyncProjectionPreservesPreferredFilter(t *testing.T) {
	ctx := context.Background()
	s, err := OpenSQLite(filepath.Join(t.TempDir(), "projection.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	site, err := s.SaveSite(ctx, domain.Site{Title: "Test", Type: "Site", Name: "Test", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 3; i++ {
		_, err := s.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: fmt.Sprintf("TEST-%03d", i), Title: "Fixture", ProductURL: "https://www.javlibrary.com/?v=fixture", ReleaseDate: "2024-01-01"})
		if err != nil {
			t.Fatal(err)
		}
	}
	created := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	if _, err := s.db.ExecContext(ctx, `UPDATE releases SET is_preferred=CASE WHEN id=2 THEN 0 ELSE 1 END,is_local=1,watchlist=1,stash_scene_id='scene',stash_file_path='/media/example.mp4',stash_created_at=?,stash_release_date='2024-02-01',o_counter=2,play_count=3,last_played_at='played',last_o_count_at='orgasm'`, created); err != nil {
		t.Fatal(err)
	}
	// The same concrete store powers both engines. This preferred filter is
	// dialect-neutral; execute it with both dialects, not a separate SQL copy.
	for _, dialect := range []Dialect{SQLiteDialect{}, PostgresDialect{}} {
		s.dialect = dialect
		for _, preferred := range []bool{false, true} {
			f := domain.ReleaseFilter{}
			if preferred {
				f = domain.ReleaseFilter{UsePreferred: true, IgnoreTags: []string{"ignored"}}
			}
			s.dialect = SQLiteDialect{}
			full, err := s.Releases(ctx, f)
			if err != nil {
				t.Fatal(err)
			}
			s.dialect = dialect
			high, total, err := s.StashSyncSnapshot(ctx, f)
			if err != nil || total != len(full) {
				t.Fatalf("total=%d want=%d err=%v", total, len(full), err)
			}
			narrow, err := s.StashSyncReleases(ctx, f, 0, high, 500)
			if err != nil {
				t.Fatal(err)
			}
			if len(narrow) != len(full) {
				t.Fatalf("preferred=%v narrow=%d full=%d", preferred, len(narrow), len(full))
			}
			byID := map[int64]domain.Release{}
			for _, r := range full {
				byID[r.ID] = r
			}
			for _, got := range narrow {
				r := byID[got.ID]
				want := domain.Release{ID: r.ID, VideoID: r.VideoID, Title: r.Title, ProductURL: r.ProductURL, Local: r.Local, Watchlist: r.Watchlist, StashSceneID: r.StashSceneID, StashFilePath: r.StashFilePath, StashCreatedAt: r.StashCreatedAt, StashReleaseDate: r.StashReleaseDate, ReleaseDate: r.ReleaseDate, OCounter: r.OCounter, PlayCount: r.PlayCount, LastPlayedAt: r.LastPlayedAt, LastOCountAt: r.LastOCountAt}
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("projection=%+v want=%+v", got, want)
				}
			}
		}
	}
}

type stashSyncReaderTest interface {
	StashSyncSnapshot(context.Context, domain.ReleaseFilter) (int64, int, error)
	StashSyncReleases(context.Context, domain.ReleaseFilter, int64, int64, int) ([]domain.Release, error)
}

func TestStashSyncKeysetSnapshot(t *testing.T) {
	ctx := context.Background()
	s, err := OpenSQLite(filepath.Join(t.TempDir(), "sync.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	site, err := s.SaveSite(ctx, domain.Site{Title: "GIGA", Type: "Site", Name: "GIGA", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 7; i++ {
		_, err := s.UpsertRelease(ctx, domain.Release{SiteID: site.ID, Source: "GIGA", VideoID: fmt.Sprintf("TEST-%03d", i), Title: "Fixture", ReleaseDate: "2024-01-01", Story: "not needed", Genres: []string{"not needed"}})
		if err != nil {
			t.Fatal(err)
		}
	}
	reader, ok := any(s).(stashSyncReaderTest)
	if !ok {
		t.Fatal("production store lacks narrow Stash keyset reader")
	}
	high, total, err := reader.StashSyncSnapshot(ctx, domain.ReleaseFilter{})
	if err != nil || total != 7 {
		t.Fatalf("snapshot high=%d total=%d err=%v", high, total, err)
	}
	var ids []int64
	var after int64
	for {
		rows, err := reader.StashSyncReleases(ctx, domain.ReleaseFilter{}, after, high, 2)
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) == 0 {
			break
		}
		for _, r := range rows {
			if r.ID <= after || r.ID > high {
				t.Fatalf("out of bounds: %+v", r)
			}
			if r.Story != "" || len(r.Genres) > 0 || r.SiteTitle != "" || r.DownloadStatus != "" {
				t.Fatalf("hydrated UI metadata: %+v", r)
			}
			ids = append(ids, r.ID)
			after = r.ID
		}
		if len(ids) == 2 {
			// Deleting an already visited row must not shift the next page. New rows
			// and release-date edits must not extend or reorder this traversal.
			if _, err := s.db.ExecContext(ctx, `DELETE FROM releases WHERE id=?`, ids[0]); err != nil {
				t.Fatal(err)
			}
			if _, err := s.db.ExecContext(ctx, `UPDATE releases SET release_date='2030-01-01' WHERE id>?`, after); err != nil {
				t.Fatal(err)
			}
			if _, err := s.UpsertRelease(ctx, domain.Release{SiteID: site.ID, Source: "GIGA", VideoID: "NEW-999", Title: "Concurrent"}); err != nil {
				t.Fatal(err)
			}
		}
	}
	if len(ids) != total {
		t.Fatalf("ids=%v total=%d", ids, total)
	}
	want := []int64{1, 2, 3, 4, 5, 6, 7}
	if !reflect.DeepEqual(ids, want) {
		t.Fatalf("ids=%v want=%v", ids, want)
	}
}

package store

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/Net005/JAVBeacon/internal/domain"
)

// Synthetic catalog: no production titles, paths, settings or credentials.
func syntheticSearchCatalog(b testing.TB, count int) *SQLite {
	b.Helper()
	s, err := OpenSQLite(filepath.Join(b.TempDir(), "search.db"))
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { s.Close() })
	seedSearchCatalog(b, s, count)
	return s
}

func seedSearchCatalog(b testing.TB, s *SQLite, count int) {
	b.Helper()
	tx, err := s.db.Begin()
	if err != nil {
		b.Fatal(err)
	}
	defer tx.Rollback()
	for i := 1; i <= 32; i++ {
		if _, err = tx.Exec(`INSERT INTO sites(id,name,title,url,created_at,updated_at) VALUES(?,?,?,?,?,?)`, i, fmt.Sprintf("site-%d", i), fmt.Sprintf("Example Label %02d", i), "https://example.com", time.Now(), time.Now()); err != nil {
			b.Fatal(err)
		}
	}
	release, err := tx.Prepare(`INSERT INTO releases(id,site_id,video_id,title,source,label,added_at,updated_at) VALUES(?,1,?,?,'Example',?,?,?)`)
	if err != nil {
		b.Fatal(err)
	}
	defer release.Close()
	link, err := tx.Prepare(`INSERT INTO release_sites(release_id,site_id) VALUES(?,?)`)
	if err != nil {
		b.Fatal(err)
	}
	defer link.Close()
	now := time.Now()
	for i := 1; i <= count; i++ {
		if _, err = release.Exec(i, fmt.Sprintf("EXAMPLE-%d", i), fmt.Sprintf("Example release %d", i), fmt.Sprintf("Example Label %02d", i%32+1), now, now); err != nil {
			b.Fatal(err)
		}
		for j := 0; j < 4; j++ {
			if _, err = link.Exec(i, (i+j)%32+1); err != nil {
				b.Fatal(err)
			}
		}
	}
	for i := 1; i <= count; i++ {
		name := fmt.Sprintf("Example Performer %02d", i%32)
		if _, err := tx.Exec(`INSERT INTO release_actresses(release_id,position,name,name_normalized) VALUES(?,0,?,LOWER(?))`, i, name, name); err != nil {
			b.Fatal(err)
		}
		tag := fmt.Sprintf("Example Genre %02d", i%16)
		if _, err := tx.Exec(`INSERT INTO release_tags(release_id,position,name,name_normalized) VALUES(?,0,?,LOWER(?))`, i, tag, tag); err != nil {
			b.Fatal(err)
		}
	}
	if err = tx.Commit(); err != nil {
		b.Fatal(err)
	}
}

func BenchmarkReleaseLabelSuggestions(b *testing.B) {
	s := syntheticSearchCatalog(b, 12000)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := s.ReleaseFilterOptions(context.Background(), "label", "Example"); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkReleaseSelectionIDs(b *testing.B) {
	s := syntheticSearchCatalog(b, 12000)
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := s.ReleaseIDs(ctx, domain.ReleaseFilter{Sort: "added"}); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkReleaseTextSearch(b *testing.B) {
	s := syntheticSearchCatalog(b, 12000)
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := s.ReleasesCount(ctx, domain.ReleaseFilter{Search: "Genre 07"}); err != nil {
			b.Fatal(err)
		}
	}
}

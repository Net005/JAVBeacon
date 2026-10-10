package store

import (
	"context"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/Net005/JAVBeacon/internal/domain"
)

// Explicitly opt in with a disposable test database: this test seeds catalog
// rows and must never run against the application's production connection.
func TestPostgresSearchAndSelection(t *testing.T) {
	if os.Getenv("JAVBEACON_TEST_PG_SEARCH") != "1" {
		t.Skip("requires explicit disposable PostgreSQL search test opt-in")
	}
	cfg := testPostgresConfig(t)
	if !strings.HasSuffix(cfg.Database, "_test") {
		t.Fatal("search fixture requires a database name ending in _test")
	}
	ctx := context.Background()
	s, err := OpenPostgresStore(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	seedSearchCatalog(t, s, 520)
	defer s.db.ExecContext(context.Background(), `DELETE FROM sites WHERE id BETWEEN 1 AND 32`)
	for _, f := range []domain.ReleaseFilter{
		{Search: "Genre 07"}, {Search: "Performer 03"}, {Search: "Example Label 16"},
		{Search: "Genre 07,Performer 03", SearchWildcards: true, WildcardLogic: "or"},
		{Search: "Genre 03,Performer 03", SearchWildcards: true, WildcardLogic: "and"},
		{Sort: "added"}, {Sort: "name", Direction: "asc"}, {Sort: "release_score"},
	} {
		ids, err := s.ReleaseIDs(ctx, f)
		if err != nil {
			t.Fatal(err)
		}
		total, err := s.ReleasesCount(ctx, f)
		if err != nil || total != len(ids) {
			t.Fatalf("selection/count mismatch: total=%d IDs=%d err=%v", total, len(ids), err)
		}
		expected := make([]int64, 0)
		f.Limit = 500
		for {
			rows, err := s.Releases(ctx, f)
			if err != nil {
				t.Fatal(err)
			}
			for _, r := range rows {
				expected = append(expected, r.ID)
			}
			if len(rows) < 500 {
				break
			}
			f.Offset += len(rows)
		}
		if !reflect.DeepEqual(ids, expected) {
			t.Fatal("PostgreSQL ID-only selection changed result order")
		}
	}
	values, err := s.ReleaseFilterOptions(ctx, "label", "Example")
	if err != nil || len(values) != 32 {
		t.Fatalf("label suggestions count=%d err=%v", len(values), err)
	}
	// Confirm the upgrade installed the expression index used by director filters.
	var exists bool
	err = s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_indexes WHERE indexname='idx_releases_director_filter_trgm')`).Scan(&exists)
	if err != nil || !exists {
		t.Fatalf("director expression index missing: %v", err)
	}
}

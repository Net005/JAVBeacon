package store

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/Net005/JAVBeacon/internal/domain"
)

func TestPostgresDialectFragments(t *testing.T) {
	d := PostgresDialect{}
	if got, want := d.Name(), "postgres"; got != want {
		t.Fatalf("Name() = %q, want %q", got, want)
	}
	if got, want := d.CaseInsensitiveEquals("r.title"), "LOWER(r.title) = LOWER(?)"; got != want {
		t.Fatalf("CaseInsensitiveEquals() = %q, want %q", got, want)
	}
	if got, want := d.CaseInsensitiveLike("r.title"), "r.title ILIKE ?"; got != want {
		t.Fatalf("CaseInsensitiveLike() = %q, want %q", got, want)
	}
	if got, want := d.CaseInsensitiveOrderBy("r.title"), "LOWER(r.title)"; got != want {
		t.Fatalf("CaseInsensitiveOrderBy() = %q, want %q", got, want)
	}
	if got, want := d.ExtractLeadingMinutes("r.duration"), "CAST(NULLIF(substring(TRIM(r.duration) FROM '^[0-9]+[.]?[0-9]*'),'') AS DOUBLE PRECISION)"; got != want {
		t.Fatalf("ExtractLeadingMinutes() = %q, want %q", got, want)
	}
}

func TestPostgresDurationConditionUsesDialectExtraction(t *testing.T) {
	expr := `{"logic":"and","conditions":[{"field":"duration","op":"gte","value":"90.5"}]}`
	where, args := releaseFilterWhere(PostgresDialect{}, domain.ReleaseFilter{SearchExpression: expr})
	want := "CAST(NULLIF(substring(TRIM(r.duration) FROM '^[0-9]+[.]?[0-9]*'),'') AS DOUBLE PRECISION) >= ?"
	if !strings.Contains(where, want) {
		t.Fatalf("where clause %q does not contain %q", where, want)
	}
	if len(args) != 1 || args[0] != 90.5 {
		t.Fatalf("args = %#v, want []any{90.5}", args)
	}
}

func TestPostgresDialectInsertReturningIDAppendsReturningClause(t *testing.T) {
	// A real *sql.Row can only be constructed via a real *sql.DB query, so
	// this test uses an in-memory SQLite database (which also supports
	// "RETURNING id" since 3.35) purely as a way to produce a *sql.Row and
	// confirm the query string InsertReturningID builds is well-formed and
	// actually resolves the intended id - the point under test is the
	// "append RETURNING id and Scan" behavior itself, not PostgreSQL wire
	// compatibility.
	ctx := context.Background()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, `CREATE TABLE t(id INTEGER PRIMARY KEY, name TEXT)`); err != nil {
		t.Fatal(err)
	}

	d := PostgresDialect{}
	id, err := d.InsertReturningID(ctx, db, `INSERT INTO t(name) VALUES(?)`, "hello")
	if err != nil {
		t.Fatalf("InsertReturningID error = %v", err)
	}
	if id <= 0 {
		t.Fatalf("InsertReturningID id = %d, want > 0", id)
	}

	var name string
	if err := db.QueryRowContext(ctx, `SELECT name FROM t WHERE id=?`, id).Scan(&name); err != nil {
		t.Fatal(err)
	}
	if name != "hello" {
		t.Fatalf("name = %q, want %q", name, "hello")
	}
}

func TestPostgresDialectInsertReturningIDPropagatesScanError(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	d := PostgresDialect{}
	if _, err := d.InsertReturningID(ctx, db, `INSERT INTO no_such_table(name) VALUES(?)`, "x"); err == nil {
		t.Fatal("expected an error for an insert against a nonexistent table")
	}
}

// TestReleaseFilterWhereConditionsUseCaseInsensitiveLikeOnPostgres is the
// regression test for the TODO-2.0 Task A case-insensitivity audit bug in
// releaseFilterWhere: the title/description branch of the Conditions
// (SearchExpression) builder used to emit a bare "column LIKE ?" instead of
// routing through Dialect.CaseInsensitiveLike, which is silently
// case-sensitive on PostgreSQL (SQLite's default collation masked it there).
// Asserting against PostgresDialect{} directly is what actually proves the
// fix, since a SQLite-backed behavioral test can't distinguish "LIKE ?" from
// "ILIKE ?" - SQLite doesn't have ILIKE and its LIKE is already
// case-insensitive by default.
func TestReleaseFilterWhereConditionsUseCaseInsensitiveLikeOnPostgres(t *testing.T) {
	for _, tc := range []struct {
		field string
		want  string
	}{
		{"title", "r.title ILIKE ?"},
		{"description", "r.story ILIKE ?"},
	} {
		expr := `{"logic":"and","conditions":[{"field":"` + tc.field + `","value":"needle"}]}`
		where, args := releaseFilterWhere(PostgresDialect{}, domain.ReleaseFilter{SearchExpression: expr})
		if !strings.Contains(where, tc.want) {
			t.Fatalf("field %q: where clause %q does not contain %q", tc.field, where, tc.want)
		}
		if strings.Contains(where, "LIKE ?") && !strings.Contains(where, "ILIKE ?") {
			t.Fatalf("field %q: where clause %q contains a bare LIKE ? instead of ILIKE ?", tc.field, where)
		}
		found := false
		for _, a := range args {
			if s, ok := a.(string); ok && s == "%needle%" {
				found = true
			}
		}
		if !found {
			t.Fatalf("field %q: args %+v do not contain the expected wildcarded value", tc.field, args)
		}
	}
}

func TestPostgresSchemaIncludesReleaseLibraryPerformanceIndexes(t *testing.T) {
	for _, marker := range []string{
		"CREATE EXTENSION IF NOT EXISTS pg_trgm",
		"idx_releases_title_trgm",
		"idx_release_actresses_name_trgm",
		"idx_release_actresses_release_position",
		"idx_release_tags_name_trgm",
		"idx_release_tags_release_position",
		"idx_releases_released_date",
		"idx_releases_updated",
		"idx_downloads_release_status_updated",
		"idx_notifications_release_created",
		"idx_releases_studio_filter_trgm",
	} {
		if !strings.Contains(postgresSchemaDDL, marker) {
			t.Fatalf("PostgreSQL schema is missing %q", marker)
		}
	}
}

// TestStashMissingFilterWhereUsesCaseInsensitiveLikeOnPostgres is the
// analogous regression test for the same bug class in
// stashMissingFilterWhere's path/studio/tag Conditions branches, which
// previously took no Dialect parameter at all and always used a bare
// "LIKE ?".
func TestStashMissingFilterWhereUsesCaseInsensitiveLikeOnPostgres(t *testing.T) {
	for _, tc := range []struct {
		field string
		want  []string
	}{
		{"path", []string{"m.path ILIKE ?", "m.paths ILIKE ?"}},
		{"studio", []string{"m.studio ILIKE ?"}},
		{"tag", []string{"m.tags ILIKE ?"}},
	} {
		expr := `{"logic":"and","conditions":[{"field":"` + tc.field + `","value":"needle"}]}`
		where, _ := stashMissingFilterWhere(PostgresDialect{}, domain.StashMissingFilter{SearchExpression: expr})
		for _, want := range tc.want {
			if !strings.Contains(where, want) {
				t.Fatalf("field %q: where clause %q does not contain %q", tc.field, where, want)
			}
		}
	}
}

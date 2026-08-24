package store

import "context"

// PostgresDialect is the PostgreSQL implementation of Dialect, added in DB
// Phase 3 and completed in DB Phase 5-6. See the Dialect doc comment in
// dialect.go: this covers only the fragments that are not context-free text
// rewrites (case-insensitive comparisons, JSON aggregation, GREATEST/LEAST,
// returning an inserted row's id, boolean-expression-to-integer casts) -
// "?" placeholder rewriting and the "INSERT OR IGNORE" upsert idiom are
// handled separately by postgres_rewrite.go's driver-level rewriting seam.
// PostgresDialect is wired into a real PostgreSQL-backed Store by
// store.OpenPostgresStore (postgres_schema.go).
type PostgresDialect struct{}

func (PostgresDialect) Name() string { return "postgres" }

// CaseInsensitiveEquals lower-cases both sides rather than relying on a
// column collation. DB Phase 5 kept this LOWER()-based approach rather than
// adopting citext or a case-insensitive collation in postgres_schema.go's
// DDL - it needs no schema-level opt-in per column and matches how
// PostgresDialect's other comparisons (CaseInsensitiveLike, ILIKE) already
// work without depending on collation.
func (PostgresDialect) CaseInsensitiveEquals(column string) string {
	return "LOWER(" + column + ") = LOWER(?)"
}

// CaseInsensitiveLike uses PostgreSQL's native ILIKE, which already
// matches case-insensitively without a collation-dependent fragment.
func (PostgresDialect) CaseInsensitiveLike(column string) string {
	return column + " ILIKE ?"
}

func (PostgresDialect) CaseInsensitiveOrderBy(column string) string {
	return "LOWER(" + column + ")"
}

// InsertReturningID appends a RETURNING clause and uses QueryRowContext
// instead of ExecContext + Result.LastInsertId(), which PostgreSQL does
// not support. query must be an INSERT statement with an "id" column and
// must not already contain a RETURNING clause.
func (PostgresDialect) InsertReturningID(ctx context.Context, execer Execer, query string, args ...any) (int64, error) {
	var id int64
	if err := execer.QueryRowContext(ctx, query+" RETURNING id", args...).Scan(&id); err != nil {
		return 0, err
	}
	return id, nil
}

// JSONArrayAgg uses json_agg instead of SQLite's json_group_array, and -
// unlike SQLite - PostgreSQL requires the derived table in FROM to have an
// alias, hence the trailing "AS t". Like json_group_array, json_agg
// returns SQL NULL (not an empty array) for zero input rows, so the
// caller's COALESCE(...,'[]') wrapper behaves identically on both engines.
func (PostgresDialect) JSONArrayAgg(column, subquery string) string {
	return "COALESCE((SELECT json_agg(" + column + ") FROM (" + subquery + ") AS t),'[]')"
}

func (PostgresDialect) Greatest(a, b string) string { return "GREATEST(" + a + "," + b + ")" }
func (PostgresDialect) Least(a, b string) string    { return "LEAST(" + a + "," + b + ")" }

func (PostgresDialect) ExtractLeadingMinutes(column string) string {
	return "CAST(NULLIF(substring(TRIM(" + column + ") FROM '^[0-9]+[.]?[0-9]*'),'') AS DOUBLE PRECISION)"
}

func (PostgresDialect) BoolExprToInt(expr string) string {
	return "CASE WHEN " + expr + " THEN 1 ELSE 0 END"
}

package store

import (
	"context"
	"database/sql"
)

// Dialect abstracts the small set of SQL constructs that differ between
// supported database engines, so the query-building code in this package
// does not need per-engine branches (`if postgres ... else if sqlite ...`)
// scattered through the repository layer - see DB Phase 2 of
// TODO-DATABASE.md and the recommendations in
// docs/DB_ARCHITECTURE_ASSESSMENT.md.
//
// PostgresDialect (added in DB Phase 3, see postgres_dialect.go) is the
// second implementation. This interface only abstracts the fragments that
// are not context-free text substitutions (COLLATE NOCASE, JSON
// aggregation, scalar GREATEST/LEAST, Result.LastInsertId() vs RETURNING).
// The "?" vs "$1, $2, ..." positional placeholder syntax that every query
// string in this package still spells out directly, plus the "INSERT OR
// IGNORE" upsert idiom, are instead handled by a driver-level rewriting
// seam (postgres_rewrite.go, DB Phase 5) precisely because they *are*
// context-free rewrites - see that file's doc comment for why that approach
// was chosen over hand-rewriting every query-building method here.
type Dialect interface {
	// Name identifies the dialect for logging/diagnostics.
	Name() string

	// CaseInsensitiveEquals returns a SQL fragment comparing column to a
	// single "?" placeholder value, ignoring case regardless of the
	// column's declared collation.
	CaseInsensitiveEquals(column string) string

	// CaseInsensitiveLike returns a SQL fragment matching column against
	// a single "?" placeholder LIKE pattern, ignoring case regardless of
	// the column's declared collation.
	CaseInsensitiveLike(column string) string

	// CaseInsensitiveOrderBy returns a SQL ORDER BY fragment sorting by
	// column case-insensitively.
	CaseInsensitiveOrderBy(column string) string

	// InsertReturningID executes an INSERT statement and returns the
	// newly assigned row id. SQLite (like MySQL) supports
	// sql.Result.LastInsertId(); PostgreSQL does not and instead needs
	// "INSERT ... RETURNING id" plus QueryRow, so this is a seam a
	// PostgresDialect will implement differently rather than a call
	// sites need to branch on. execer is satisfied by *sql.DB, *sql.Tx
	// and *sql.Conn alike.
	InsertReturningID(ctx context.Context, execer Execer, query string, args ...any) (int64, error)

	// JSONArrayAgg wraps subquery - a SELECT with exactly one output
	// column named column - into an expression producing a JSON array of
	// that column's values across all of subquery's rows, or a JSON "[]"
	// if it matches none. Added in DB Phase 5 for releaseSelect, the one
	// place this package aggregates rows into a JSON array: SQLite's
	// json_group_array and PostgreSQL's json_agg are otherwise
	// equivalent, but PostgreSQL also requires the derived table in FROM
	// to have an alias, which SQLite does not - a difference that isn't a
	// context-free text substitution, so it isn't handled by the
	// rewriting seam in postgres_rewrite.go alongside "?"/"INSERT OR
	// IGNORE" and instead gets its own Dialect method like the rest of
	// this interface.
	JSONArrayAgg(column, subquery string) string

	// Greatest and Least return a SQL expression evaluating to whichever
	// of a and b is larger/smaller - the *scalar* (2-argument) form, not
	// the aggregate MAX()/MIN() used elsewhere in this package's queries
	// (which is already portable as-is: both engines support
	// single-argument aggregate MAX/MIN identically). SQLite overloads
	// MAX()/MIN() to mean this scalar comparison when given 2+ arguments;
	// PostgreSQL has no such overload and uses the separate functions
	// GREATEST()/LEAST() instead. Added in DB Phase 5 for
	// migrateReleaseIdentity's release-dedup UPDATE, the one query in
	// this package that needs it.
	Greatest(a, b string) string
	Least(a, b string) string

	// ExtractLeadingMinutes converts a TEXT duration whose leading component is
	// a numeric minute count into a numeric SQL expression suitable for
	// comparisons.
	ExtractLeadingMinutes(column string) string

	// BoolExprToInt wraps expr - a SQL expression that evaluates to a
	// boolean (e.g. "EXISTS (...)", a comparison) - so its result can be
	// assigned to one of this package's INTEGER-typed boolean-shaped
	// columns (see postgres_schema.go's doc comment on that convention).
	// SQLite has no boolean type - EXISTS/comparisons already evaluate to
	// integer 0/1 - so expr is returned unchanged. PostgreSQL's EXISTS and
	// comparisons produce a genuine boolean, which it refuses to assign
	// directly into an integer column ("column ... is of type integer but
	// expression is of type boolean"), so this wraps it in a CASE
	// expression. Added in DB Phase 5 for SetSiteReleaseMonitoring's
	// site_monitor_download recompute, the only place this package assigns
	// a boolean expression's result into a column rather than binding a Go
	// bool as a query argument (which convertBoolArgs in
	// postgres_rewrite.go already handles).
	BoolExprToInt(expr string) string
}

// Execer is the narrow subset of *sql.DB/*sql.Tx/*sql.Conn that
// InsertReturningID needs. Defined here (rather than requiring callers to
// depend on database/sql directly) so dialect methods can accept whichever
// of those three a caller currently holds - most importantly a transaction
// mid-flight, which is the common case for inserts that must participate
// in a larger unit of work.
//
// QueryRowContext is included alongside ExecContext because
// PostgresDialect.InsertReturningID needs "INSERT ... RETURNING id" (a
// query, not a plain exec) - PostgreSQL has no Result.LastInsertId()
// equivalent. *sql.DB, *sql.Tx and *sql.Conn all already implement both
// methods, so this is not a breaking change for existing callers.
type Execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// SQLiteDialect is the Dialect implementation backing *SQLite. Every
// fragment it produces is byte-for-byte identical to what the query
// strings in this package spelled out directly before DB Phase 2 - this
// phase only introduces the seam, it does not change SQLite's behavior.
type SQLiteDialect struct{}

func (SQLiteDialect) Name() string { return "sqlite" }

func (SQLiteDialect) CaseInsensitiveEquals(column string) string {
	return column + " = ? COLLATE NOCASE"
}

func (SQLiteDialect) CaseInsensitiveLike(column string) string {
	return column + " LIKE ? COLLATE NOCASE"
}

func (SQLiteDialect) CaseInsensitiveOrderBy(column string) string {
	return column + " COLLATE NOCASE"
}

func (SQLiteDialect) InsertReturningID(ctx context.Context, execer Execer, query string, args ...any) (int64, error) {
	result, err := execer.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

func (SQLiteDialect) JSONArrayAgg(column, subquery string) string {
	return "COALESCE((SELECT json_group_array(" + column + ") FROM (" + subquery + ")),'[]')"
}

func (SQLiteDialect) Greatest(a, b string) string { return "MAX(" + a + "," + b + ")" }
func (SQLiteDialect) Least(a, b string) string    { return "MIN(" + a + "," + b + ")" }

func (SQLiteDialect) ExtractLeadingMinutes(column string) string {
	return "CASE WHEN TRIM(" + column + ") GLOB '[0-9]*' THEN CAST(TRIM(" + column + ") AS REAL) ELSE NULL END"
}

func (SQLiteDialect) BoolExprToInt(expr string) string { return expr }

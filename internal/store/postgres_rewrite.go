package store

import (
	"context"
	"database/sql/driver"
	"errors"
	"regexp"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/stdlib"
)

// This file is DB Phase 5's "rewriting seam" - the alternative the doc
// comments on Dialect (dialect.go) and PostgresDialect (postgres_dialect.go)
// flagged back in DB Phase 2/3 to rewriting every query string in this
// package by hand: every method on *SQLite already writes portable SQL
// except for two mechanical, purely-syntactic differences from PostgreSQL -
// "?" positional placeholders (PostgreSQL needs "$1, $2, ...") and SQLite's
// "INSERT OR IGNORE INTO" (PostgreSQL needs "INSERT INTO ... ON CONFLICT DO
// NOTHING"). Both are handled here, transparently, at the database/sql
// driver layer, so none of the ~50 query-building methods in store.go need
// to change or branch on engine. This is why *SQLite (despite its name -
// see the doc comment on that type) is also the concrete type
// OpenPostgresStore returns: the struct was already dialect-generic since
// DB Phase 2 (a *sql.DB plus a Dialect), and this file closes the one real
// gap that was stopping it from working against a second engine.
//
// A third, genuinely semantic difference - SQLite's json_group_array vs
// PostgreSQL's json_agg, including PostgreSQL's requirement that a derived
// table in FROM have an alias - is NOT handled here, since it is not a
// context-free text substitution; it is handled by Dialect.JSONArrayAgg
// instead (see dialect.go), used by releaseSelect.
//
// Every rewrite is applied unconditionally to every statement this package
// sends: query building here never lets a syntax difference leak past
// this seam, so *SQLite's ~50 exported methods are the only correctness
// surface a future engine change needs to touch.

// insertOrIgnorePrefix locates a leading "INSERT OR IGNORE INTO" so it can
// be replaced with a plain "INSERT INTO" and an "ON CONFLICT DO NOTHING"
// appended once the rest of the statement has been rewritten. A "?" placed
// outside a single-quoted string literal is found separately, by
// rewriteForPostgres's small state machine below, not by this regexp.
var insertOrIgnorePrefix = regexp.MustCompile(`(?i)^\s*INSERT OR IGNORE INTO\b`)

// rewriteForPostgres translates one SQLite-flavored statement, as written
// throughout this package, into the equivalent PostgreSQL statement:
//
//  1. "?" positional placeholders (outside single-quoted string literals)
//     become "$1", "$2", ... in first-to-last order, matching how
//     database/sql always supplies positional args for this package's
//     queries (it never uses named parameters).
//  2. A leading "INSERT OR IGNORE INTO" becomes a plain "INSERT INTO", and
//     "ON CONFLICT DO NOTHING" is appended to the end of the statement.
//     Every "INSERT OR IGNORE" in this package targets a table whose
//     primary key or a UNIQUE constraint is exactly the row being
//     deduplicated against, matching PostgreSQL's unqualified (no
//     conflict target) ON CONFLICT DO NOTHING, which applies to any
//     unique/exclusion violation on the row being inserted.
//
// Every other statement in this package (DDL using portable types,
// "ON CONFLICT(col) DO UPDATE SET x=excluded.x", CASE/COALESCE/EXISTS,
// string functions, etc.) is already valid, unmodified PostgreSQL syntax -
// SQLite intentionally adopted PostgreSQL's upsert syntax verbatim, so
// nothing else here needs translating.
func rewriteForPostgres(query string) string {
	if insertOrIgnorePrefix.MatchString(query) {
		query = insertOrIgnorePrefix.ReplaceAllString(query, "INSERT INTO") + " ON CONFLICT DO NOTHING"
	}
	if !strings.Contains(query, "?") {
		return query
	}
	var b strings.Builder
	b.Grow(len(query) + 8)
	inString := false
	n := 0
	for i := 0; i < len(query); i++ {
		c := query[i]
		switch {
		case c == '\'':
			inString = !inString
			b.WriteByte(c)
		case c == '?' && !inString:
			n++
			b.WriteByte('$')
			b.WriteString(strconv.Itoa(n))
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// convertBoolArgs returns a copy of args with every bool value replaced by
// an int64 0/1. It exists because this package's schema represents every
// boolean-shaped column (enabled, notify, released, is_local, ...) as a
// plain INTEGER on both engines (see postgres_schema.go's doc comment for
// why), and while SQLite's dynamic typing accepts a bool parameter into an
// INTEGER column transparently, PostgreSQL's pgx driver does not - it
// rejects a bool argument bound against an integer column outright
// ("cannot find encode plan"). Converting at this single seam means none
// of this package's ~50 query methods, which pass Go bool values as args
// throughout, need to change.
func convertBoolArgs(args []driver.NamedValue) []driver.NamedValue {
	converted := make([]driver.NamedValue, len(args))
	for i, a := range args {
		if b, ok := a.Value.(bool); ok {
			if b {
				a.Value = int64(1)
			} else {
				a.Value = int64(0)
			}
		}
		converted[i] = a
	}
	return converted
}

// pgQmarkConnector wraps a real pgx stdlib driver.Connector, inserting
// pgQmarkConn between database/sql and pgx so every statement passes
// through rewriteForPostgres/convertBoolArgs first.
type pgQmarkConnector struct {
	real driver.Connector
}

func (c *pgQmarkConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.real.Connect(ctx)
	if err != nil {
		return nil, err
	}
	real, ok := conn.(*stdlib.Conn)
	if !ok {
		return nil, errors.New("javbeacon: pgx stdlib driver returned an unexpected connection type")
	}
	return &pgQmarkConn{Conn: real}, nil
}

func (c *pgQmarkConnector) Driver() driver.Driver { return c.real.Driver() }

// newPostgresConnector builds a driver.Connector for dsn that rewrites
// every statement through rewriteForPostgres/convertBoolArgs before pgx
// ever sees it. OpenPostgres (postgres.go) uses this for every connection
// it opens - Ping, the only operation the DB Phase 3/4 connectivity-check
// callers (app startup, the setup wizard's Test/Validate Connection) ever
// perform, does not go through Exec/Query/Prepare, so this rewriting layer
// is a no-op for them; OpenPostgresStore (postgres_schema.go) is what
// actually exercises it.
func newPostgresConnector(dsn string) (driver.Connector, error) {
	real, err := (&stdlib.Driver{}).OpenConnector(dsn)
	if err != nil {
		return nil, err
	}
	return &pgQmarkConnector{real: real}, nil
}

// pgQmarkConn embeds the real *stdlib.Conn so every optional driver
// interface it implements (driver.Pinger, driver.SessionResetter,
// driver.NamedValueChecker, driver.ConnBeginTx, ...) is promoted
// automatically; only the three methods that see raw SQL text are
// overridden.
type pgQmarkConn struct {
	*stdlib.Conn
}

func (c *pgQmarkConn) Prepare(query string) (driver.Stmt, error) {
	return c.PrepareContext(context.Background(), query)
}

func (c *pgQmarkConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	stmt, err := c.Conn.PrepareContext(ctx, rewriteForPostgres(query))
	if err != nil {
		return nil, err
	}
	return &pgQmarkStmt{Stmt: stmt}, nil
}

func (c *pgQmarkConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	return c.Conn.ExecContext(ctx, rewriteForPostgres(query), convertBoolArgs(args))
}

func (c *pgQmarkConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	return c.Conn.QueryContext(ctx, rewriteForPostgres(query), convertBoolArgs(args))
}

// pgQmarkStmt embeds the driver.Stmt interface (not pgx's concrete type)
// so it stays decoupled from stdlib internals; only the two arg-bearing
// Context methods need to convert bool args, since the query text was
// already rewritten once at Prepare time.
type pgQmarkStmt struct {
	driver.Stmt
}

func (s *pgQmarkStmt) ExecContext(ctx context.Context, args []driver.NamedValue) (driver.Result, error) {
	sec, ok := s.Stmt.(driver.StmtExecContext)
	if !ok {
		return nil, errors.New("javbeacon: prepared statement does not support ExecContext")
	}
	return sec.ExecContext(ctx, convertBoolArgs(args))
}

func (s *pgQmarkStmt) QueryContext(ctx context.Context, args []driver.NamedValue) (driver.Rows, error) {
	sqc, ok := s.Stmt.(driver.StmtQueryContext)
	if !ok {
		return nil, errors.New("javbeacon: prepared statement does not support QueryContext")
	}
	return sqc.QueryContext(ctx, convertBoolArgs(args))
}

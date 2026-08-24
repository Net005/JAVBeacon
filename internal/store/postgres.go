package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// PostgresConfig holds the connection and pool settings OpenPostgres needs.
// It intentionally does not depend on internal/config (which internal/store
// has never imported) - callers such as internal/app translate
// config.Config's Postgres* fields into this struct.
type PostgresConfig struct {
	Host     string
	Port     int
	Database string
	User     string
	Password string
	// SSLMode is passed through to PostgreSQL's sslmode connection
	// parameter verbatim (e.g. "disable", "prefer", "require",
	// "verify-ca", "verify-full"). Empty defaults to "prefer".
	SSLMode string

	// Pool tuning. Zero values fall back to the DB Phase 1A "Large
	// Library" application-pool starting point (see TODO-DATABASE.md):
	// bounded application-side pooling rather than opening as many
	// connections as the PostgreSQL server's max_connections allows.
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
	ConnMaxIdleTime time.Duration
}

const (
	defaultPostgresMaxOpenConns    = 25
	defaultPostgresMaxIdleConns    = 8
	defaultPostgresConnMaxLifetime = 30 * time.Minute
	defaultPostgresConnMaxIdleTime = 5 * time.Minute
)

// DSN builds a postgres:// connection string via net/url so host, user,
// password and database values with special characters are escaped
// correctly rather than hand-concatenated. sslmode is always included
// explicitly so behavior does not depend on pgx's own default.
func (c PostgresConfig) DSN() string {
	sslMode := c.SSLMode
	if sslMode == "" {
		sslMode = "prefer"
	}
	u := url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(c.User, c.Password),
		Host:   net.JoinHostPort(c.Host, fmt.Sprintf("%d", c.Port)),
		Path:   "/" + c.Database,
	}
	q := url.Values{"sslmode": {sslMode}}
	u.RawQuery = q.Encode()
	return u.String()
}

// redactedDSN is DSN() with the password replaced, safe to include in log
// output or error messages - never log or return DSN()'s result directly.
func (c PostgresConfig) redactedDSN() string {
	redacted := c
	redacted.Password = "REDACTED"
	return redacted.DSN()
}

// OpenPostgres opens a connection pool for PostgreSQL using the pgx driver,
// through the query-rewriting connector in postgres_rewrite.go (DB Phase
// 5 - see that file's doc comment), and verifies connectivity with a ping
// before returning - per DB Phase 3, callers must not silently fall back
// to SQLite if this fails; propagate the returned error instead. The
// returned error is always safe to log: see ClassifyPostgresError for
// turning it into an actionable, secret-free category and message.
//
// The rewriting connector only changes behavior for statements that use
// "?" placeholders or "INSERT OR IGNORE"; a bare connectivity check like
// Ping (all this function itself does) is unaffected by it, so this is a
// safe, behavior-preserving change for OpenPostgres's DB Phase 3/4 callers
// (app startup, the setup wizard's Test/Validate Connection) - it only
// matters once OpenPostgresStore (postgres_schema.go) starts actually
// running queries through a connection opened this way.
func OpenPostgres(ctx context.Context, c PostgresConfig) (*sql.DB, error) {
	connector, err := newPostgresConnector(c.DSN())
	if err != nil {
		return nil, fmt.Errorf("open postgres connection to %s: %w", c.redactedDSN(), sanitizePostgresError(err))
	}
	db := sql.OpenDB(connector)

	maxOpen := c.MaxOpenConns
	if maxOpen <= 0 {
		maxOpen = defaultPostgresMaxOpenConns
	}
	maxIdle := c.MaxIdleConns
	if maxIdle <= 0 {
		maxIdle = defaultPostgresMaxIdleConns
	}
	connMaxLifetime := c.ConnMaxLifetime
	if connMaxLifetime <= 0 {
		connMaxLifetime = defaultPostgresConnMaxLifetime
	}
	connMaxIdleTime := c.ConnMaxIdleTime
	if connMaxIdleTime <= 0 {
		connMaxIdleTime = defaultPostgresConnMaxIdleTime
	}
	db.SetMaxOpenConns(maxOpen)
	db.SetMaxIdleConns(maxIdle)
	db.SetConnMaxLifetime(connMaxLifetime)
	db.SetConnMaxIdleTime(connMaxIdleTime)

	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("connect to postgres at %s: %w", c.redactedDSN(), sanitizePostgresError(err))
	}
	return db, nil
}

// PostgresErrorCategory classifies a PostgreSQL connection failure into one
// of the buckets DB Phase 4 asks Test / Validate Connection to distinguish.
type PostgresErrorCategory string

const (
	PostgresErrorTimeout               PostgresErrorCategory = "timeout"
	PostgresErrorHostUnreachable       PostgresErrorCategory = "host_unreachable"
	PostgresErrorConnectionRefused     PostgresErrorCategory = "connection_refused"
	PostgresErrorAuthenticationFailed  PostgresErrorCategory = "authentication_failed"
	PostgresErrorDatabaseNotFound      PostgresErrorCategory = "database_not_found"
	PostgresErrorInsufficientPrivilege PostgresErrorCategory = "insufficient_privilege"
	PostgresErrorTLS                   PostgresErrorCategory = "tls_configuration_failure"
	PostgresErrorOther                 PostgresErrorCategory = "other"
)

// ClassifyPostgresError inspects err (as returned by OpenPostgres, or any
// other error from a pgx-backed connection attempt) and returns a stable
// category plus a short, actionable, secret-free message. It never
// includes err.Error() verbatim in the returned message for
// ParseConfigError, since that type does not fully redact malformed
// connection strings in all cases; every other message here is a static,
// hand-written string precisely so a password embedded in a lower-level
// error can never leak through this function.
func ClassifyPostgresError(err error) (PostgresErrorCategory, string) {
	if err == nil {
		return "", ""
	}

	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "28P01", "28000": // invalid_password, invalid_authorization_specification
			return PostgresErrorAuthenticationFailed, "authentication failed: the username or password was rejected by the server"
		case "3D000": // invalid_catalog_name
			return PostgresErrorDatabaseNotFound, "the requested database does not exist on that server"
		case "42501", "42P01": // insufficient_privilege, undefined_table (commonly a permissions symptom pre-migration)
			return PostgresErrorInsufficientPrivilege, "the connection succeeded but the account lacks a required permission: " + pgErr.Message
		}
		return PostgresErrorOther, "the server rejected the request (SQLSTATE " + pgErr.Code + "): " + pgErr.Message
	}

	if pgconn.Timeout(err) {
		return PostgresErrorTimeout, "timed out waiting for a response from the server"
	}

	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "password authentication failed") || strings.Contains(msg, "authentication failed") {
		return PostgresErrorAuthenticationFailed, "authentication failed: the username or password was rejected by the server"
	}
	if strings.Contains(msg, "tls") || strings.Contains(msg, "x509") || strings.Contains(msg, "certificate") {
		return PostgresErrorTLS, "TLS/SSL negotiation failed - check the sslmode and, for verify-ca/verify-full, that the server's certificate is trusted"
	}

	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return PostgresErrorHostUnreachable, "could not resolve host " + dnsErr.Name + " - check the hostname"
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		if opErr.Timeout() {
			return PostgresErrorTimeout, "timed out trying to reach the server"
		}
		if strings.Contains(strings.ToLower(opErr.Err.Error()), "refused") {
			return PostgresErrorConnectionRefused, "connection refused - is PostgreSQL running and listening on that host/port?"
		}
		return PostgresErrorHostUnreachable, "could not reach the server - check the host, port and network path"
	}

	var parseErr *pgconn.ParseConfigError
	if errors.As(err, &parseErr) {
		return PostgresErrorOther, "the connection settings could not be parsed - check host, port, database and user"
	}

	if errors.Is(err, context.DeadlineExceeded) {
		return PostgresErrorTimeout, "timed out waiting for a response from the server"
	}

	return PostgresErrorOther, "connection failed"
}

// sanitizePostgresError wraps err so that logging or displaying it (e.g.
// via %v/%s in a startup log line) can never include a password, by
// replacing it with the ClassifyPostgresError message. The original error
// remains available via errors.Unwrap for callers that need
// ClassifyPostgresError-style categorization further up the stack.
type sanitizedPostgresError struct {
	category PostgresErrorCategory
	message  string
	cause    error
}

func (e *sanitizedPostgresError) Error() string { return e.message }
func (e *sanitizedPostgresError) Unwrap() error { return e.cause }

func sanitizePostgresError(err error) error {
	category, message := ClassifyPostgresError(err)
	return &sanitizedPostgresError{category: category, message: message, cause: err}
}

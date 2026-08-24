package store

import (
	"context"
	"errors"
	"net"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestPostgresConfigDSNEscapesSpecialCharacters(t *testing.T) {
	c := PostgresConfig{
		Host:     "db.example.com",
		Port:     5432,
		Database: "JAV Beacon", // space, must be escaped
		User:     "user@name",  // '@' must not be confused with the host separator
		Password: "p@ss:word/weird?chars",
		SSLMode:  "require",
	}
	dsn := c.DSN()
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("DSN() produced an unparseable URL: %v (%s)", err, dsn)
	}
	if u.Scheme != "postgres" {
		t.Fatalf("scheme = %q, want postgres", u.Scheme)
	}
	if u.Hostname() != "db.example.com" || u.Port() != "5432" {
		t.Fatalf("host:port = %s:%s, want db.example.com:5432", u.Hostname(), u.Port())
	}
	if u.User.Username() != "user@name" {
		t.Fatalf("username = %q, want %q", u.User.Username(), "user@name")
	}
	pw, ok := u.User.Password()
	if !ok || pw != "p@ss:word/weird?chars" {
		t.Fatalf("password = %q (ok=%v), want the original password round-tripped", pw, ok)
	}
	if got, want := strings.TrimPrefix(u.Path, "/"), "JAV Beacon"; got != want {
		t.Fatalf("database = %q, want %q", got, want)
	}
	if got, want := u.Query().Get("sslmode"), "require"; got != want {
		t.Fatalf("sslmode = %q, want %q", got, want)
	}
}

func TestPostgresConfigDSNDefaultsSSLMode(t *testing.T) {
	c := PostgresConfig{Host: "127.0.0.1", Port: 5432, Database: "javbeacon", User: "javbeacon", Password: "x"}
	u, err := url.Parse(c.DSN())
	if err != nil {
		t.Fatal(err)
	}
	if got, want := u.Query().Get("sslmode"), "prefer"; got != want {
		t.Fatalf("default sslmode = %q, want %q", got, want)
	}
}

func TestPostgresConfigRedactedDSNNeverContainsThePassword(t *testing.T) {
	c := PostgresConfig{
		Host: "127.0.0.1", Port: 5432, Database: "javbeacon", User: "javbeacon",
		Password: "extremely-secret-marker-987654",
	}
	redacted := c.redactedDSN()
	if strings.Contains(redacted, c.Password) {
		t.Fatalf("redactedDSN() leaked the password: %s", redacted)
	}
	if strings.Contains(redacted, "REDACTED") == false {
		t.Fatalf("redactedDSN() = %q, want it to contain a redaction marker", redacted)
	}
	// The real password must round-trip through the un-redacted DSN, so
	// this test would catch DSN() itself silently dropping it too.
	if pw, _ := mustParsePassword(t, c.DSN()); pw != c.Password {
		t.Fatalf("DSN() password = %q, want %q", pw, c.Password)
	}
}

func mustParsePassword(t *testing.T, dsn string) (string, bool) {
	t.Helper()
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	return u.User.Password()
}

func TestClassifyPostgresErrorNil(t *testing.T) {
	category, message := ClassifyPostgresError(nil)
	if category != "" || message != "" {
		t.Fatalf("ClassifyPostgresError(nil) = (%q, %q), want empty", category, message)
	}
}

func TestClassifyPostgresErrorPgErrorCodes(t *testing.T) {
	cases := []struct {
		name string
		code string
		want PostgresErrorCategory
	}{
		{"invalid password", "28P01", PostgresErrorAuthenticationFailed},
		{"invalid authorization", "28000", PostgresErrorAuthenticationFailed},
		{"invalid catalog name", "3D000", PostgresErrorDatabaseNotFound},
		{"insufficient privilege", "42501", PostgresErrorInsufficientPrivilege},
		{"some other SQLSTATE", "53300", PostgresErrorOther}, // too_many_connections, not specially handled
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := &pgconn.PgError{Code: tc.code, Message: "boom", Severity: "ERROR"}
			category, message := ClassifyPostgresError(err)
			if category != tc.want {
				t.Fatalf("category = %q, want %q", category, tc.want)
			}
			if message == "" {
				t.Fatal("expected a non-empty message")
			}
		})
	}
}

func TestClassifyPostgresErrorDNSFailure(t *testing.T) {
	err := &net.DNSError{Err: "no such host", Name: "does-not-resolve.invalid", IsNotFound: true}
	category, message := ClassifyPostgresError(err)
	if category != PostgresErrorHostUnreachable {
		t.Fatalf("category = %q, want %q", category, PostgresErrorHostUnreachable)
	}
	if !strings.Contains(message, "does-not-resolve.invalid") {
		t.Fatalf("message = %q, want it to mention the unresolvable host", message)
	}
}

func TestClassifyPostgresErrorConnectionRefused(t *testing.T) {
	err := &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connect: connection refused")}
	category, _ := ClassifyPostgresError(err)
	if category != PostgresErrorConnectionRefused {
		t.Fatalf("category = %q, want %q", category, PostgresErrorConnectionRefused)
	}
}

func TestClassifyPostgresErrorDeadlineExceeded(t *testing.T) {
	category, _ := ClassifyPostgresError(context.DeadlineExceeded)
	if category != PostgresErrorTimeout {
		t.Fatalf("category = %q, want %q", category, PostgresErrorTimeout)
	}
}

func TestClassifyPostgresErrorTLS(t *testing.T) {
	category, _ := ClassifyPostgresError(errors.New("tls: failed to verify certificate: x509: certificate signed by unknown authority"))
	if category != PostgresErrorTLS {
		t.Fatalf("category = %q, want %q", category, PostgresErrorTLS)
	}
}

// TestOpenPostgresFailsWithoutLeakingThePasswordOrFallingBackToSQLite
// verifies DB Phase 3's two hard requirements against a guaranteed-closed
// local port (no real PostgreSQL server is available in CI/sandbox
// environments, so this cannot exercise a successful connection - see
// docs/OPERATIONS.md for how this was validated against a real server):
//
//  1. OpenPostgres returns an error rather than silently falling back to
//     SQLite or otherwise succeeding.
//  2. That error's message never contains the plaintext password, however
//     it got there (DSN construction, the driver's own error text, etc.).
func TestOpenPostgresFailsWithoutLeakingThePasswordOrFallingBackToSQLite(t *testing.T) {
	// Bind to an ephemeral port and immediately close it, guaranteeing
	// whatever port number the OS handed out is refusing connections.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().(*net.TCPAddr)
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}

	const secretMarker = "unmistakable-test-password-marker-13579"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	db, err := OpenPostgres(ctx, PostgresConfig{
		Host:     addr.IP.String(),
		Port:     addr.Port,
		Database: "javbeacon",
		User:     "javbeacon",
		Password: secretMarker,
	})
	if err == nil {
		db.Close()
		t.Fatal("expected OpenPostgres to fail against a closed port, got a live *sql.DB instead")
	}
	if strings.Contains(err.Error(), secretMarker) {
		t.Fatalf("OpenPostgres error leaked the password: %v", err)
	}
	category, _ := ClassifyPostgresError(err)
	if category == "" {
		t.Fatal("expected ClassifyPostgresError to recognize OpenPostgres's returned error and return a non-empty category")
	}
}

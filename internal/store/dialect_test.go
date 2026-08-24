package store

import (
	"context"
	"path/filepath"
	"testing"
)

func TestSQLiteDialectFragments(t *testing.T) {
	d := SQLiteDialect{}
	if got, want := d.Name(), "sqlite"; got != want {
		t.Fatalf("Name() = %q, want %q", got, want)
	}
	if got, want := d.CaseInsensitiveEquals("r.title"), "r.title = ? COLLATE NOCASE"; got != want {
		t.Fatalf("CaseInsensitiveEquals() = %q, want %q", got, want)
	}
	if got, want := d.CaseInsensitiveLike("r.title"), "r.title LIKE ? COLLATE NOCASE"; got != want {
		t.Fatalf("CaseInsensitiveLike() = %q, want %q", got, want)
	}
	if got, want := d.CaseInsensitiveOrderBy("r.title"), "r.title COLLATE NOCASE"; got != want {
		t.Fatalf("CaseInsensitiveOrderBy() = %q, want %q", got, want)
	}
	if got, want := d.ExtractLeadingMinutes("r.duration"), "CASE WHEN TRIM(r.duration) GLOB '[0-9]*' THEN CAST(TRIM(r.duration) AS REAL) ELSE NULL END"; got != want {
		t.Fatalf("ExtractLeadingMinutes() = %q, want %q", got, want)
	}
}

func TestSQLiteDialectInsertReturningID(t *testing.T) {
	ctx := context.Background()
	st, err := OpenSQLite(filepath.Join(t.TempDir(), "dialect.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	id, err := st.dialect.InsertReturningID(ctx, st.db, `INSERT INTO path_mappings(download_prefix,local_prefix) VALUES(?,?)`, "/downloads/a", "/local/a")
	if err != nil {
		t.Fatalf("InsertReturningID error = %v", err)
	}
	if id <= 0 {
		t.Fatalf("InsertReturningID id = %d, want > 0", id)
	}

	second, err := st.dialect.InsertReturningID(ctx, st.db, `INSERT INTO path_mappings(download_prefix,local_prefix) VALUES(?,?)`, "/downloads/b", "/local/b")
	if err != nil {
		t.Fatalf("InsertReturningID error = %v", err)
	}
	if second <= id {
		t.Fatalf("second id = %d, want > first id %d", second, id)
	}
}

func TestSQLiteDialectInsertReturningIDPropagatesExecError(t *testing.T) {
	ctx := context.Background()
	st, err := OpenSQLite(filepath.Join(t.TempDir(), "dialect-err.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	if _, err := st.dialect.InsertReturningID(ctx, st.db, `INSERT INTO no_such_table(x) VALUES(?)`, 1); err == nil {
		t.Fatal("expected an error for an insert against a nonexistent table")
	}
}

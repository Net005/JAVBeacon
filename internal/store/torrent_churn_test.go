package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/Net005/JAVBeacon/internal/domain"
)

func torrentChurnStore(t *testing.T, engine string) (*SQLite, domain.Download) {
	t.Helper()
	ctx := context.Background()
	var st *SQLite
	var err error
	if engine == "postgres" {
		st, err = OpenPostgresStore(ctx, testPostgresConfig(t))
	} else {
		st, err = OpenSQLite(filepath.Join(t.TempDir(), "churn.db"))
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	site, err := st.SaveSite(ctx, domain.Site{Title: "Synthetic churn", Name: "Test", Type: "Site"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = st.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "CHURN-123", Title: "Synthetic", Source: "Test"}); err != nil {
		t.Fatal(err)
	}
	rows, err := st.Releases(ctx, domain.ReleaseFilter{SiteID: site.ID, Limit: 1})
	if err != nil || len(rows) != 1 {
		t.Fatalf("releases: %v", err)
	}
	d, err := st.SaveDownload(ctx, domain.Download{ReleaseID: rows[0].ID, TorrentHash: "synthetic-owned", Status: "downloading", Priority: 7, Files: []byte(`[]`)})
	if err != nil {
		t.Fatal(err)
	}
	return st, d
}

// Exercise every changed-value predicate independently, including repeat writes.
func TestTorrentChangedFieldsAdvanceTimestamp(t *testing.T) {
	for _, engine := range []string{"sqlite", "postgres"} {
		t.Run(engine, func(t *testing.T) {
			st, d := torrentChurnStore(t, engine)
			ctx := context.Background()
			cases := []struct {
				name   string
				mutate func(*domain.Download)
				state  bool
			}{
				{"name", func(d *domain.Download) { d.Name = "new name" }, false},
				{"seed_ratio", func(d *domain.Download) { d.SeedRatio = .5 }, false},
				{"progress", func(d *domain.Download) { d.Progress = .5 }, false},
				{"seeds", func(d *domain.Download) { d.Seeds = 3 }, false},
				{"peers", func(d *domain.Download) { d.Peers = 4 }, false},
				{"eta_seconds", func(d *domain.Download) { d.ETASeconds = 50 }, false},
				{"seen_complete", func(d *domain.Download) { d.SeenComplete = 123 }, false},
				{"files", func(d *domain.Download) { d.Files = []byte(`[{"name":"synthetic.mp4"}]`) }, true},
				{"status", func(d *domain.Download) { d.Status = "completed" }, true},
				{"match_reason", func(d *domain.Download) { d.MatchReason = "synthetic match" }, true},
				{"qb_response", func(d *domain.Download) { d.QBResponse = "synthetic response" }, true},
				{"post_status", func(d *domain.Download) { d.PostStatus = "processing" }, true},
				{"error", func(d *domain.Download) { d.Error = "synthetic failure" }, true},
			}
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					base, err := st.SaveDownload(ctx, d)
					if err != nil {
						t.Fatal(err)
					}
					stamp := time.Date(2001, 2, 3, 4, 5, 6, 0, time.UTC)
					if _, err := st.db.ExecContext(ctx, `UPDATE downloads SET updated_at=? WHERE id=?`, stamp, d.ID); err != nil {
						t.Fatal(err)
					}
					tc.mutate(&base)
					save := st.UpdateTorrentProgress
					if tc.state {
						save = st.SaveTorrentState
					}
					if err := save(ctx, base); err != nil {
						t.Fatal(err)
					}
					var changed time.Time
					if err := st.db.QueryRowContext(ctx, `SELECT updated_at FROM downloads WHERE id=?`, d.ID).Scan(&changed); err != nil {
						t.Fatal(err)
					}
					if !changed.After(stamp) {
						t.Fatalf("changed %s did not advance timestamp", tc.name)
					}
					if err := save(ctx, base); err != nil {
						t.Fatal(err)
					}
					var repeated time.Time
					if err := st.db.QueryRowContext(ctx, `SELECT updated_at FROM downloads WHERE id=?`, d.ID).Scan(&repeated); err != nil {
						t.Fatal(err)
					}
					if !repeated.Equal(changed) {
						t.Fatalf("repeated %s advanced timestamp", tc.name)
					}
					rows, err := st.Downloads(ctx, "")
					if err != nil {
						t.Fatal(err)
					}
					if len(rows) != 1 || rows[0].Priority != 7 {
						t.Fatal("narrow update changed unrelated fields")
					}
				})
			}
		})
	}
}

func TestTorrentStaleWritesCannotChangeReplacedOrDeletedRows(t *testing.T) {
	for _, engine := range []string{"sqlite", "postgres"} {
		t.Run(engine, func(t *testing.T) {
			st, d := torrentChurnStore(t, engine)
			ctx := context.Background()
			for _, guard := range []string{"torrent_hash", "transport", "status"} {
				t.Run(guard, func(t *testing.T) {
					if _, err := st.SaveDownload(ctx, d); err != nil {
						t.Fatal(err)
					}
					value := "different-hash"
					if guard == "transport" {
						value = "http"
					}
					if guard == "status" {
						value = "removed"
					}
					stamp := time.Date(2001, 2, 3, 4, 5, 6, 0, time.UTC)
					if _, err := st.db.ExecContext(ctx, `UPDATE downloads SET `+guard+`=?, updated_at=? WHERE id=?`, value, stamp, d.ID); err != nil {
						t.Fatal(err)
					}
					stale := d
					stale.Progress = 1
					stale.Status = "completed"
					stale.Error = "stale"
					if err := st.UpdateTorrentProgress(ctx, stale); err != nil {
						t.Fatal(err)
					}
					if err := st.SaveTorrentState(ctx, stale); err != nil {
						t.Fatal(err)
					}
					var got time.Time
					if err := st.db.QueryRowContext(ctx, `SELECT updated_at FROM downloads WHERE id=?`, d.ID).Scan(&got); err != nil {
						t.Fatal(err)
					}
					if !got.Equal(stamp) {
						t.Fatalf("stale write bypassed %s guard", guard)
					}
				})
			}
			if _, err := st.db.ExecContext(ctx, `DELETE FROM downloads WHERE id=?`, d.ID); err != nil {
				t.Fatal(err)
			}
			if err := st.UpdateTorrentProgress(ctx, d); err != nil {
				t.Fatal(err)
			}
			if err := st.SaveTorrentState(ctx, d); err != nil {
				t.Fatal(err)
			}
			var count int
			if err := st.db.QueryRowContext(ctx, `SELECT count(*) FROM downloads WHERE id=?`, d.ID).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 0 {
				t.Fatal("deleted download resurrected")
			}
		})
	}
}

// Legacy nullable columns must compare distinctly from their incoming zero values.
func TestTorrentNullableValuesArePersistedThenBecomeNoOps(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `CREATE TABLE downloads (id INTEGER PRIMARY KEY, torrent_hash TEXT, transport TEXT, status TEXT, name TEXT, seed_ratio REAL, progress REAL, seeds INTEGER, peers INTEGER, eta_seconds INTEGER, seen_complete BIGINT, files TEXT, match_reason TEXT, qb_response TEXT, post_status TEXT, error TEXT, updated_at DATETIME)`); err != nil {
		t.Fatal(err)
	}
	stamp := time.Date(2001, 2, 3, 4, 5, 6, 0, time.UTC)
	if _, err := db.ExecContext(ctx, `INSERT INTO downloads (id,torrent_hash,transport,status,updated_at) VALUES (1,'synthetic','torrent','downloading',?)`, stamp); err != nil {
		t.Fatal(err)
	}
	st := &SQLite{db: db}
	d := domain.Download{ID: 1, TorrentHash: "synthetic", Status: "downloading"}
	for _, method := range []struct {
		name, column string
		save         func(context.Context, domain.Download) error
	}{{"progress", "name", st.UpdateTorrentProgress}, {"state", "files", st.SaveTorrentState}} {
		t.Run(method.name, func(t *testing.T) {
			if _, err := db.ExecContext(ctx, `UPDATE downloads SET updated_at=? WHERE id=1`, stamp); err != nil {
				t.Fatal(err)
			}
			if err := method.save(ctx, d); err != nil {
				t.Fatal(err)
			}
			var updated time.Time
			var value sql.NullString
			if err := db.QueryRowContext(ctx, `SELECT updated_at,`+method.column+` FROM downloads WHERE id=1`).Scan(&updated, &value); err != nil {
				t.Fatal(err)
			}
			if !updated.After(stamp) || !value.Valid {
				t.Fatal("NULL-to-value transition was ignored")
			}
			if err := method.save(ctx, d); err != nil {
				t.Fatal(err)
			}
			var repeated time.Time
			if err := db.QueryRowContext(ctx, `SELECT updated_at FROM downloads WHERE id=1`).Scan(&repeated); err != nil {
				t.Fatal(err)
			}
			if !repeated.Equal(updated) {
				t.Fatal("normalized nullable row was written again")
			}
		})
	}
}

func TestTorrentIdenticalWritesPreserveExactTimestamp(t *testing.T) {
	for _, engine := range []string{"sqlite", "postgres"} {
		t.Run(engine, func(t *testing.T) {
			st, d := torrentChurnStore(t, engine)
			ctx := context.Background()
			for _, method := range []struct {
				name string
				save func(context.Context, domain.Download) error
			}{{"progress", st.UpdateTorrentProgress}, {"state", st.SaveTorrentState}} {
				t.Run(method.name, func(t *testing.T) {
					stamp := time.Date(2001, 2, 3, 4, 5, 6, 123456000, time.UTC)
					if _, err := st.db.ExecContext(ctx, `UPDATE downloads SET updated_at=? WHERE id=?`, stamp, d.ID); err != nil {
						t.Fatal(err)
					}
					var before, after time.Time
					if err := st.db.QueryRowContext(ctx, `SELECT updated_at FROM downloads WHERE id=?`, d.ID).Scan(&before); err != nil {
						t.Fatal(err)
					}
					if err := method.save(ctx, d); err != nil {
						t.Fatal(err)
					}
					if err := st.db.QueryRowContext(ctx, `SELECT updated_at FROM downloads WHERE id=?`, d.ID).Scan(&after); err != nil {
						t.Fatal(err)
					}
					if !before.Equal(after) {
						t.Fatalf("identical %s write advanced updated_at: %s -> %s", method.name, before, after)
					}
				})
			}
		})
	}
}

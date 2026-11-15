package store

import (
	"context"
	"github.com/Net005/JAVBeacon/internal/domain"
	"os"
	"strings"
	"testing"
	"time"
)

func TestPostgresPerformancePaths(t *testing.T) {
	if os.Getenv("JAVBEACON_TEST_PG_DATABASE") == "" {
		t.Skip("requires explicit disposable PostgreSQL database")
	}
	cfg := testPostgresConfig(t)
	if !strings.Contains(strings.ToLower(cfg.Database), "test") {
		t.Fatal("requires a disposable test database")
	}
	ctx := context.Background()
	s, err := OpenPostgresStore(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	site, err := s.SaveSite(ctx, domain.Site{Title: "Performance fixture", Name: "JavLibrary", Type: "Site", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := s.db.ExecContext(context.Background(), "DELETE FROM sites WHERE id=?", site.ID); err != nil {
			t.Errorf("clean performance fixture site: %v", err)
		}
	})
	_, err = s.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "EXAMPLE-719", Title: "Synthetic performance fixture", Source: "JavLibrary", ReleaseDate: "2024-01-01"})
	if err != nil {
		t.Fatal(err)
	}
	filter := domain.ReleaseFilter{Search: "EXAMPLE-719"}
	high, total, err := s.StashSyncSnapshot(ctx, filter)
	if err != nil || total != 1 {
		t.Fatalf("snapshot total=%d err=%v", total, err)
	}
	rows, err := s.StashSyncReleases(ctx, filter, 0, high, 500)
	if err != nil || len(rows) != 1 || rows[0].VideoID != "EXAMPLE-719" {
		t.Fatalf("page=%v err=%v", rows, err)
	}
	next, err := s.StashSyncReleases(ctx, filter, high, high, 500)
	if err != nil || len(next) != 0 {
		t.Fatalf("terminal page=%v err=%v", next, err)
	}
	d, err := s.SaveDownload(ctx, domain.Download{ReleaseID: rows[0].ID, Query: "EXAMPLE-719", TorrentHash: "synthetic-hash", Transport: "torrent", Status: "downloading", Files: []byte(`[]`)})
	if err != nil {
		t.Fatal(err)
	}
	timestamp := func() time.Time {
		var at time.Time
		if err := s.db.QueryRowContext(ctx, "SELECT updated_at FROM downloads WHERE id=?", d.ID).Scan(&at); err != nil {
			t.Fatal(err)
		}
		return at
	}
	before := timestamp()
	if err = s.UpdateTorrentProgress(ctx, d); err != nil {
		t.Fatal(err)
	}
	if after := timestamp(); !after.Equal(before) {
		t.Fatalf("unchanged progress rewrote PostgreSQL timestamp")
	}
	if err = s.SaveTorrentState(ctx, d); err != nil {
		t.Fatal(err)
	}
	if after := timestamp(); !after.Equal(before) {
		t.Fatalf("unchanged state rewrote PostgreSQL timestamp")
	}
	d.Progress = .5
	if err = s.UpdateTorrentProgress(ctx, d); err != nil {
		t.Fatal(err)
	}
	if after := timestamp(); !after.After(before) {
		t.Fatal("changed progress not persisted")
	}
	d.PostStatus = "synthetic-processing"
	if err = s.SaveTorrentState(ctx, d); err != nil {
		t.Fatal(err)
	}
	var progress float64
	var state string
	if err = s.db.QueryRowContext(ctx, "SELECT progress,post_status FROM downloads WHERE id=?", d.ID).Scan(&progress, &state); err != nil {
		t.Fatal(err)
	}
	if progress != .5 || state != d.PostStatus {
		t.Fatalf("independent state/progress lost: %v %q", progress, state)
	}
	t.Cleanup(func() {
		s.db.ExecContext(context.Background(), "DELETE FROM downloads WHERE id=?", d.ID)
		s.db.ExecContext(context.Background(), "DELETE FROM releases WHERE id=?", rows[0].ID)
	})
}

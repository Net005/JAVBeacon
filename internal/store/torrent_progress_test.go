package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/Net005/JAVBeacon/internal/domain"
)

func TestTorrentProgressAndStateUpdatesDoNotClobberEachOther(t *testing.T) {
	for _, engine := range []string{"sqlite", "postgres"} {
		t.Run(engine, func(t *testing.T) {
			ctx := context.Background()
			var st *SQLite
			var err error
			if engine == "postgres" {
				st, err = OpenPostgresStore(ctx, testPostgresConfig(t))
			} else {
				st, err = OpenSQLite(filepath.Join(t.TempDir(), "torrent-progress.db"))
			}
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			site, err := st.SaveSite(ctx, domain.Site{Title: "Synthetic progress", Name: "Test", Type: "Site"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := st.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "SYNTH-123", Title: "Synthetic", Source: "Test"}); err != nil {
				t.Fatal(err)
			}
			releases, err := st.Releases(ctx, domain.ReleaseFilter{SiteID: site.ID, Limit: 1})
			if err != nil || len(releases) != 1 {
				t.Fatalf("release setup: %v", err)
			}
			old, err := st.SaveDownload(ctx, domain.Download{ReleaseID: releases[0].ID, TorrentHash: "owned-hash", Status: "downloading", PostStatus: "processing", Priority: 7})
			if err != nil {
				t.Fatal(err)
			}
			latest := old
			latest.Progress, latest.SeedRatio = 1, .75
			if err := st.UpdateTorrentProgress(ctx, latest); err != nil {
				t.Fatal(err)
			}
			old.Status, old.PostStatus = "completed", "completed_retained"
			if err := st.SaveTorrentState(ctx, old); err != nil {
				t.Fatal(err)
			}
			get := func() domain.Download {
				t.Helper()
				rows, err := st.Downloads(ctx, "")
				if err != nil {
					t.Fatal(err)
				}
				for _, row := range rows {
					if row.ID == old.ID {
						return row
					}
				}
				t.Fatal("download disappeared")
				return domain.Download{}
			}
			got := get()
			if got.Progress != 1 || got.SeedRatio != .75 || got.PostStatus != "completed_retained" || got.Priority != 7 {
				t.Fatalf("concurrent telemetry or state lost: %+v", got)
			}
			got.Transport, got.Status, got.Progress = "http", "queued", .25
			if _, err := st.SaveDownload(ctx, got); err != nil {
				t.Fatal(err)
			}
			if err := st.UpdateTorrentProgress(ctx, latest); err != nil {
				t.Fatal(err)
			}
			if err := st.SaveTorrentState(ctx, old); err != nil {
				t.Fatal(err)
			}
			if got := get(); got.Transport != "http" || got.Status != "queued" || got.Progress != .25 {
				t.Fatal("stale torrent work overwrote an HTTP replacement")
			}
		})
	}
}

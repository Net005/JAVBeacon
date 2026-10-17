package store

import (
	"context"
	"github.com/Net005/JAVBeacon/internal/domain"
	"path/filepath"
	"testing"
)

func TestDeleteDownloadHistoryForTransport(t *testing.T) {
	for _, engine := range []string{"sqlite", "postgres"} {
		t.Run(engine, func(t *testing.T) {
			ctx := context.Background()
			var st *SQLite
			var err error
			if engine == "postgres" {
				st, err = OpenPostgresStore(ctx, testPostgresConfig(t))
			} else {
				st, err = OpenSQLite(filepath.Join(t.TempDir(), "transport-history.db"))
			}
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			site, err := st.SaveSite(ctx, domain.Site{Title: "Synthetic transport", Type: "Site", Name: "JavLibrary", Enabled: true})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = st.UpsertRelease(ctx, domain.Release{SiteID: site.ID, Source: "JavLibrary", VideoID: "SYNTH-202", Title: "Synthetic"}); err != nil {
				t.Fatal(err)
			}
			releases, err := st.Releases(ctx, domain.ReleaseFilter{Search: "SYNTH-202", Limit: 1})
			if err != nil || len(releases) != 1 {
				t.Fatalf("setup: %v", err)
			}
			id := releases[0].ID
			for _, transport := range []string{"torrent", "http"} {
				if _, err = st.SaveDownload(ctx, domain.Download{ReleaseID: id, Transport: transport, Status: "downloading"}); err != nil {
					t.Fatal(err)
				}
			}
			if _, err = st.CreateNotification(ctx, id, "download_started", "Synthetic download"); err != nil {
				t.Fatal(err)
			}
			n, err := st.DeleteDownloadsForReleaseTransport(ctx, id, "torrent")
			if err != nil || n != 1 {
				t.Fatalf("torrent delete: %d %v", n, err)
			}
			rows, err := st.Downloads(ctx, "")
			if err != nil || len(rows) != 1 || rows[0].Transport != "http" {
				t.Fatalf("HTTP history lost: %v %v", rows, err)
			}
			notifications, err := st.Notifications(ctx, "")
			if err != nil || len(notifications) != 1 {
				t.Fatalf("shared notification lost: %v %v", notifications, err)
			}
			n, err = st.DeleteDownloadsForReleaseTransport(ctx, id, "http")
			if err != nil || n != 1 {
				t.Fatalf("HTTP delete: %d %v", n, err)
			}
			notifications, err = st.Notifications(ctx, "")
			if err != nil || len(notifications) != 0 {
				t.Fatalf("orphan notification retained: %v %v", notifications, err)
			}
			n, err = st.DeleteDownloadsForReleaseTransport(ctx, id, "http")
			if err != nil || n != 0 {
				t.Fatalf("repeat delete: %d %v", n, err)
			}
		})
	}
}

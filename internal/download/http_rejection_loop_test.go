package download

import (
	"context"
	"github.com/Net005/JAVBeacon/internal/domain"
	"github.com/Net005/JAVBeacon/internal/store"
	"log/slog"
	"path/filepath"
	"testing"
	"time"
)

func TestHTTPInspectionRejectionsNeverAccumulateFailedTransfers(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	site, err := st.SaveSite(ctx, domain.Site{Title: "Synthetic", Type: "Site", Name: "JavLibrary", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "SYNTH-101", Title: "Synthetic", Source: "JavLibrary"})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := st.Releases(ctx, domain.ReleaseFilter{Search: "SYNTH-101", Limit: 1})
	if err != nil || len(rows) != 1 {
		t.Fatal(err)
	}
	svc := New(st, time.Second, slog.Default())
	for _, reason := range []string{"release ID matched; Keepshare filename inspection failed: PikPak share contained no file matching SYNTH-101", "release ID matched; Keepshare filename inspection failed: HTTP 429"} {
		for i := 0; i < 3; i++ {
			d, err := svc.queueHTTPDownload(ctx, rows[0], domain.SearchResult{Provider: "JavDB / Keepshare", Link: "https://keepshare.example/share", Reason: reason}, "Monitored Search", "")
			if err != nil {
				t.Fatal(err)
			}
			if d.ID != 0 || d.Status != "not_available" {
				t.Fatalf("inspection created a transfer: %+v", d)
			}
		}
	}
	failed, err := st.Downloads(ctx, "failed")
	if err != nil || len(failed) != 0 {
		t.Fatalf("failed=%d err=%v", len(failed), err)
	}
}

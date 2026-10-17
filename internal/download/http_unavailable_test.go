package download

import (
	"context"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/Net005/JAVBeacon/internal/domain"
	"github.com/Net005/JAVBeacon/internal/store"
)

func TestHTTPUnavailableCandidatesDoNotFailOrStartTransfers(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "unavailable.db"))
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
	releases, err := st.Releases(ctx, domain.ReleaseFilter{Search: "SYNTH-101", Limit: 1})
	if err != nil || len(releases) != 1 {
		t.Fatalf("setup: %+v %v", releases, err)
	}
	svc := New(st, time.Second, slog.Default())
	for _, candidate := range []domain.SearchResult{
		{Provider: "JavDB / Keepshare", Unavailable: true, Reason: "No exact release ID match"},
		{Provider: "JavDB / Keepshare", Unavailable: true, Forced: true, Reason: "No published share"},
		{Provider: "JavDB / Keepshare", Reason: "Different release ID"},
	} {
		row, err := svc.queueHTTPDownload(ctx, releases[0], candidate, "Synthetic test", "")
		if err != nil || row.ID != 0 || row.Status != "not_available" || row.Error != "" || row.MatchReason != candidate.Reason {
			t.Fatalf("candidate %+v produced %+v, %v", candidate, row, err)
		}
	}
	all, err := st.Downloads(ctx, "")
	if err != nil || len(all) != 0 {
		t.Fatalf("unavailable searches accumulated download rows: %+v %v", all, err)
	}
	failed, err := st.Downloads(ctx, "failed")
	if err != nil || len(failed) != 0 {
		t.Fatalf("unexpected failures: %+v %v", failed, err)
	}
	queued, err := st.Downloads(ctx, "queued")
	if err != nil || len(queued) != 0 {
		t.Fatalf("unexpected transfers: %+v %v", queued, err)
	}
	inspectionReason := "release ID matched; Keepshare filename inspection failed: forbidden"
	failure, err := svc.queueHTTPDownload(ctx, releases[0], domain.SearchResult{Provider: "JavDB / Keepshare", Reason: inspectionReason}, "Synthetic test", "")
	if err != nil || failure.Status != "failed" || failure.Error != inspectionReason {
		t.Fatalf("inspection error was hidden as unavailable: %+v %v", failure, err)
	}
}

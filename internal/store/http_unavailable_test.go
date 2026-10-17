package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/Net005/JAVBeacon/internal/domain"
)

func TestUnavailableHTTPHistoryRepairPreservesTransferFailures(t *testing.T) {
	ctx := context.Background()
	st, err := OpenSQLite(filepath.Join(t.TempDir(), "history.db"))
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
	mismatch := "HTTP result did not exactly match the release ID"
	rows := []domain.Download{
		{Transport: "http", Error: mismatch},
		{Transport: "http", Error: "JavDB returned search results but no exact release ID match (requested_id=SYNTH-101 normalized_requested_id=SYNTH101 search_results=2)"},
		{Transport: "http", Error: "JavDB search request failed: forbidden"},
		{Transport: "http", Error: "download section could not be parsed"},
		{Transport: "http", Error: mismatch, BytesDownloaded: 1},
		{Transport: "http", Error: mismatch, DestinationPath: "/media/example.mp4"},
		{Transport: "torrent", Error: mismatch},
		{Transport: "http", Error: mismatch, Progress: 0.5},
	}
	for i := range rows {
		rows[i].ReleaseID, rows[i].Status = releases[0].ID, "failed"
		rows[i], err = st.SaveDownload(ctx, rows[i])
		if err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 2; i++ {
		if err := st.reclassifyUnavailableHTTPMatches(ctx); err != nil {
			t.Fatal(err)
		}
	}
	got, err := st.Downloads(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	byID := map[int64]domain.Download{}
	for _, row := range got {
		byID[row.ID] = row
	}
	for i, before := range rows {
		after := byID[before.ID]
		if i < 2 {
			if after.Status != "not_available" || after.Error != "" || after.MatchReason != before.Error {
				t.Fatalf("mismatch was not reclassified: %+v", after)
			}
		} else if after.Status != "failed" || after.Error != before.Error {
			t.Fatalf("real failure changed: before=%+v after=%+v", before, after)
		}
		if !after.UpdatedAt.Equal(before.UpdatedAt) {
			t.Fatalf("history timestamp changed: %+v", after)
		}
	}
}

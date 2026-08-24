package legacyimport

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Net005/JAVBeacon/internal/domain"
	"github.com/Net005/JAVBeacon/internal/store"
)

func TestReplaceSitesPreservesUnlistedReleasesAndGIGA(t *testing.T) {
	dir := t.TempDir()
	database := filepath.Join(dir, "javbeacon.db")
	sitesPath := filepath.Join(dir, "sites.txt")
	contents := "Title\tType\tSiteName\tSiteUrl\tSiteUpdated\tReleaseNotification\r\n" +
		"GIGA\tSite\tGIGA\t\t2020-12-28 20:01:41.264\t1\r\n" +
		"Attackers\tMaker\tJavLibrary\thttp://example.test/attackers\t2022-06-19 14:56:19.017\t1\r\n" +
		"Cosplay\tGenre\tJavLibrary\thttp://example.test/cosplay\t2022-06-19 15:01:11.725\t0\r\n"
	if err := os.WriteFile(sitesPath, []byte(contents), 0644); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	st, err := store.OpenSQLite(database)
	if err != nil {
		t.Fatal(err)
	}
	giga, _ := st.SaveSite(ctx, domain.Site{Title: "GIGA", Type: "Site", Name: "GIGA", URL: "https://giga.test", Enabled: true})
	attackers, _ := st.SaveSite(ctx, domain.Site{Title: "Attackers", Type: "Maker", Name: "JavLibrary", Enabled: false})
	keep, _ := st.SaveSite(ctx, domain.Site{Title: "Keep Me", Type: "Actress", Name: "JavLibrary", Enabled: false})
	now := time.Now().UTC()
	for _, release := range []domain.Release{
		{SiteID: giga.ID, VideoID: "GIGA-1", Title: "GIGA release", Source: "GIGA", AddedAt: now, UpdatedAt: now},
		{SiteID: attackers.ID, VideoID: "ATT-1", Title: "replace release", Source: "JavLibrary", AddedAt: now, UpdatedAt: now},
		{SiteID: keep.ID, VideoID: "KEEP-1", Title: "preserved release", Source: "JavLibrary", AddedAt: now, UpdatedAt: now},
	} {
		if _, err := st.UpsertRelease(ctx, release); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	report, err := ReplaceSites(ctx, SiteOptions{DatabasePath: database, SitesPath: sitesPath, Apply: true})
	if err != nil {
		t.Fatal(err)
	}
	if report.FileRows != 3 || report.ImportedSites != 2 || report.SkippedGIGA != 1 || report.MatchedSites != 1 || report.ReleasesRemoved != 1 || report.ReleasesPreserved != 2 {
		t.Fatalf("unexpected report: %+v", report)
	}
	st, err = store.OpenSQLite(database)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	allSites, err := st.Sites(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(allSites) != 4 {
		t.Fatalf("got %d sites, want 4", len(allSites))
	}
	for _, site := range allSites {
		if !site.Enabled {
			t.Fatalf("site %q was not enabled", site.Title)
		}
		if site.Title == "GIGA" && site.URL != "https://giga.test" {
			t.Fatalf("GIGA was replaced: %+v", site)
		}
		if site.Title == "Attackers" && site.URL != "http://example.test/attackers" {
			t.Fatalf("Attackers was not recreated from file: %+v", site)
		}
		if site.Title == "Cosplay" && site.Type != "Tag" {
			t.Fatalf("Genre was not normalized to Tag: %+v", site)
		}
	}
	preserved, err := st.Releases(ctx, domain.ReleaseFilter{Search: "KEEP-1", Limit: 10})
	if err != nil || len(preserved) != 1 {
		t.Fatalf("unlisted release was not preserved: rows=%d err=%v", len(preserved), err)
	}
	removed, err := st.Releases(ctx, domain.ReleaseFilter{Search: "ATT-1", Limit: 10})
	if err != nil || len(removed) != 0 {
		t.Fatalf("matching release was not removed: rows=%d err=%v", len(removed), err)
	}
}

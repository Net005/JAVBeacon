package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Net005/JAVBeacon/internal/domain"
)

func TestSQLiteReleaseLifecycle(t *testing.T) {
	ctx := context.Background()
	s, err := OpenSQLite(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	site, err := s.SaveSite(ctx, domain.Site{Title: "GIGA", Type: "Site", Name: "GIGA", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	created, err := s.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "SPSF-52", Title: "Test release", Source: "GIGA", Released: true, Screenshots: []string{"https://example.test/1.jpg"}})
	if err != nil || !created {
		t.Fatalf("created=%v err=%v", created, err)
	}
	items, err := s.Releases(ctx, domain.ReleaseFilter{Search: "SPSF", Status: "released"})
	if err != nil || len(items) != 1 {
		t.Fatalf("items=%d err=%v", len(items), err)
	}
	local := true
	if err := s.PatchRelease(ctx, items[0].ID, nil, &local, nil, nil, nil, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	stats, err := s.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Sites != 1 || stats.Releases != 1 || stats.Released != 1 || stats.Local != 1 {
		t.Fatalf("unexpected stats: %+v", stats)
	}
}

func TestSQLiteMigratesWatchlistNamingWithoutLosingState(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "watchlist-naming.db")
	s, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SaveUser(ctx, "admin", "hash"); err != nil {
		t.Fatal(err)
	}
	retired := "des" + "ired"
	retiredTitle := "Des" + "ired"
	preferenceState := json.RawMessage(fmt.Sprintf(`{"%s":true,"sortField":"%s_marked","releaseShortcuts":{"toggle%s":["x"]},"monitored%sOnly":true}`, retired, retired, retiredTitle, retiredTitle))
	if err := s.SavePreferences(ctx, preferenceState); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveFilterPreset(ctx, domain.FilterPreset{Name: "legacy", State: json.RawMessage(fmt.Sprintf(`{"%s":true}`, retired))}); err != nil {
		t.Fatal(err)
	}
	site, err := s.SaveSite(ctx, domain.Site{Title: "Migration", Type: "Site", Name: "Migration", Enabled: true, Watchlist: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "MIG-001", Title: "Migration", Source: "Migration", Watchlist: true}); err != nil {
		t.Fatal(err)
	}
	release, found, err := s.ReleaseForSite(ctx, site.ID, "Migration", "MIG-001")
	if err != nil || !found {
		t.Fatalf("release lookup before migration: found=%v err=%v", found, err)
	}
	if err := s.SaveWatchlistSync(ctx, release.ID, "scene-1", "tag-1", "tagged"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	statements := []string{
		`DROP INDEX IF EXISTS idx_releases_watchlist_date`,
		fmt.Sprintf(`ALTER TABLE sites RENAME COLUMN watchlist TO %s`, retired),
		fmt.Sprintf(`ALTER TABLE releases RENAME COLUMN watchlist_at TO %s_at`, retired),
		fmt.Sprintf(`ALTER TABLE releases RENAME COLUMN watchlist TO %s`, retired),
		fmt.Sprintf(`ALTER TABLE watchlist_sync RENAME TO %s_sync`, retired),
		fmt.Sprintf(`INSERT INTO settings(key,value,updated_at) VALUES('stash_%s_sync_enabled','true',CURRENT_TIMESTAMP)`, retired),
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			db.Close()
			t.Fatalf("prepare retired schema with %q: %v", statement, err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	s, err = OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	sites, err := s.Sites(ctx)
	if err != nil || len(sites) != 1 || !sites[0].Watchlist {
		t.Fatalf("site watchlist state was not preserved: sites=%+v err=%v", sites, err)
	}
	release, found, err = s.ReleaseForSite(ctx, site.ID, "Migration", "MIG-001")
	if err != nil || !found || !release.Watchlist || release.WatchlistAt.IsZero() {
		t.Fatalf("release watchlist state was not preserved: release=%+v err=%v", release, err)
	}
	if synced, err := s.WatchlistSynced(ctx, release.ID, "scene-1", "tag-1"); err != nil || !synced {
		t.Fatalf("watchlist sync state was not preserved: synced=%v err=%v", synced, err)
	}
	settings, err := s.Settings(ctx)
	if err != nil || settings["stash_watchlist_sync_enabled"] != "true" {
		t.Fatalf("watchlist setting was not migrated: settings=%v err=%v", settings, err)
	}
	preferences, err := s.Preferences(ctx)
	if err != nil || strings.Contains(string(preferences), retired) || !strings.Contains(string(preferences), "watchlist_marked") || !strings.Contains(string(preferences), "toggleWatchlist") {
		t.Fatalf("preferences were not migrated: %s err=%v", preferences, err)
	}
	presets, err := s.FilterPresets(ctx)
	if err != nil || len(presets) != 1 || strings.Contains(string(presets[0].State), retired) || !strings.Contains(string(presets[0].State), "watchlist") {
		t.Fatalf("filter preset was not migrated: presets=%+v err=%v", presets, err)
	}
	for _, target := range []struct{ table, column string }{{"sites", retired}, {"releases", retired}, {"releases", retired + "_at"}} {
		if exists, err := s.columnExists(ctx, target.table, target.column); err != nil || exists {
			t.Fatalf("retired column remains: %s.%s exists=%v err=%v", target.table, target.column, exists, err)
		}
	}
	if exists, err := s.tableExists(ctx, retired+"_sync"); err != nil || exists {
		t.Fatalf("retired sync table remains: exists=%v err=%v", exists, err)
	}
}

func TestReleaseCardsUseCursorPaginationAndLightweightRows(t *testing.T) {
	ctx := context.Background()
	s, err := OpenSQLite(filepath.Join(t.TempDir(), "release-cards.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	site, _ := s.SaveSite(ctx, domain.Site{Title: "Cards", Type: "Site", Name: "JavLibrary", Enabled: true})
	for i := 1; i <= 5; i++ {
		_, err := s.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: fmt.Sprintf("CARD-%d", i), Title: fmt.Sprintf("Card %d", i), Story: "large detail-only story", Source: "JavLibrary", ReleaseDate: fmt.Sprintf("2026-08-%02d", i), Screenshots: []string{"https://example.test/shot.jpg"}})
		if err != nil {
			t.Fatal(err)
		}
	}
	filter := domain.ReleaseFilter{Sort: "release", Direction: "desc", Limit: 2}
	cursor := ""
	var ids []string
	for {
		page, err := s.ReleaseCards(ctx, filter, cursor)
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range page.Items {
			ids = append(ids, item.VideoID)
			if item.Story != "" || len(item.SiteTitles) != 0 {
				t.Fatalf("card row contains detail-only payload: %+v", item)
			}
			if len(item.Screenshots) != 1 {
				t.Fatalf("card lost screenshot availability: %+v", item.Screenshots)
			}
		}
		cursor = page.NextCursor
		if cursor == "" {
			break
		}
	}
	want := []string{"CARD-5", "CARD-4", "CARD-3", "CARD-2", "CARD-1"}
	if strings.Join(ids, ",") != strings.Join(want, ",") {
		t.Fatalf("cursor pages=%v, want %v", ids, want)
	}
}

func TestMaterializedReleasePreferenceTracksSettingsAndNewRows(t *testing.T) {
	ctx := context.Background()
	s, err := OpenSQLite(filepath.Join(t.TempDir(), "preferred.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	site, _ := s.SaveSite(ctx, domain.Site{Title: "Preferred", Type: "Site", Name: "JavLibrary", Enabled: true})
	_, _ = s.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "PREF-1", Title: "Ordinary release", Source: "JavLibrary", Genres: []string{"Drama"}})
	if err := s.SaveSettings(ctx, map[string]string{"ignore_tags": "Drama", "ignore_titles": ""}); err != nil {
		t.Fatal(err)
	}
	filter := domain.ReleaseFilter{UsePreferred: true, IgnoreTags: []string{"Drama"}, Limit: 10}
	if rows, err := s.Releases(ctx, filter); err != nil || len(rows) != 0 {
		t.Fatalf("materialized tag ignore rows=%v err=%v", rows, err)
	}
	_, _ = s.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "PREF-2", Title: "New ignored release", Source: "JavLibrary", Genres: []string{"Drama"}})
	if rows, err := s.Releases(ctx, filter); err != nil || len(rows) != 0 {
		t.Fatalf("new row ignored state rows=%v err=%v", rows, err)
	}
	if err := s.SaveSettings(ctx, map[string]string{"ignore_tags": "", "ignore_titles": ""}); err != nil {
		t.Fatal(err)
	}
	if rows, err := s.Releases(ctx, domain.ReleaseFilter{Limit: 10}); err != nil || len(rows) != 2 {
		t.Fatalf("cleared materialized ignores rows=%v err=%v", rows, err)
	}
}

func TestReleasePreferenceRefreshOnlyUpdatesChangedRows(t *testing.T) {
	ctx := context.Background()
	s, err := OpenSQLite(filepath.Join(t.TempDir(), "preferred-targeted.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	site, _ := s.SaveSite(ctx, domain.Site{Title: "Targeted", Type: "Site", Name: "JavLibrary", Enabled: true})
	_, _ = s.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "TARGET-1", Title: "Drama release", Source: "JavLibrary", Genres: []string{"Drama"}})
	_, _ = s.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "TARGET-2", Title: "Action release", Source: "JavLibrary", Genres: []string{"Action"}})
	if _, err := s.db.ExecContext(ctx, `CREATE TABLE preference_update_audit(n INTEGER); CREATE TRIGGER audit_preference_update AFTER UPDATE OF is_preferred ON releases BEGIN INSERT INTO preference_update_audit(n) VALUES(1); END`); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveSettings(ctx, map[string]string{"ignore_tags": "Drama", "ignore_titles": ""}); err != nil {
		t.Fatal(err)
	}
	var updates int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM preference_update_audit`).Scan(&updates); err != nil || updates != 1 {
		t.Fatalf("preference updates=%d err=%v, want only the one changed release", updates, err)
	}
	if err := s.SaveSettings(ctx, map[string]string{"ignore_tags": "Drama", "ignore_titles": ""}); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM preference_update_audit`).Scan(&updates); err != nil || updates != 1 {
		t.Fatalf("unchanged settings caused preference rewrites: updates=%d err=%v", updates, err)
	}
}

func TestScreenshotBackfillCompletionPersists(t *testing.T) {
	ctx := context.Background()
	s, err := OpenSQLite(filepath.Join(t.TempDir(), "screenshots.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	site, _ := s.SaveSite(ctx, domain.Site{Title: "JavLibrary", Type: "Site", Name: "JavLibrary", Enabled: true})
	_, _ = s.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "SHOT-1", Title: "Screenshot test", Source: "JavLibrary"})
	rows, _ := s.Releases(ctx, domain.ReleaseFilter{Search: "SHOT-1", Limit: 1})
	if len(rows) != 1 {
		t.Fatal("release setup failed")
	}
	if completed, err := s.ScreenshotBackfillCompleted(ctx, rows[0].ID); err != nil || completed {
		t.Fatalf("initial completion=%v err=%v", completed, err)
	}
	if err := s.MarkScreenshotBackfillCompleted(ctx, rows[0].ID); err != nil {
		t.Fatal(err)
	}
	if completed, err := s.ScreenshotBackfillCompleted(ctx, rows[0].ID); err != nil || !completed {
		t.Fatalf("persisted completion=%v err=%v", completed, err)
	}
}

func TestHistoricalBackfillResumeRebasesCompletedSourceByDate(t *testing.T) {
	s, err := OpenSQLite(filepath.Join(t.TempDir(), "historical.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	if err := s.PrepareHistoricalBackfill(ctx, false); err != nil {
		t.Fatal(err)
	}
	source := domain.HistoricalBackfillSource{URL: "https://example.test/genre?mode=2", Kind: "genre", Name: "Test"}
	if err := s.UpsertHistoricalBackfillSources(ctx, []domain.HistoricalBackfillSource{source}); err != nil {
		t.Fatal(err)
	}
	source.State = "completed"
	source.CursorDate = "2020-02-03"
	source.NextPage = 42
	source.PageLimit = 42
	source.PagesCompleted = 42
	if err := s.SaveHistoricalBackfillSource(ctx, source); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveHistoricalBackfillItem(ctx, domain.HistoricalBackfillItem{VideoID: "ABC-1", ReleaseDate: source.CursorDate, State: "completed", SourceURL: source.URL}); err != nil {
		t.Fatal(err)
	}
	if err := s.PrepareHistoricalBackfill(ctx, true); err != nil {
		t.Fatal(err)
	}
	rows, err := s.HistoricalBackfillSources(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].State != "pending" || rows[0].NextPage != 1 || rows[0].ResumeDate != "2020-02-03" || !rows[0].CatchupOnly {
		t.Fatalf("rebased source: %+v", rows)
	}
	stats, err := s.HistoricalBackfillStats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stats.ReleasesDiscovered != 1 || stats.ReleasesCompleted != 1 {
		t.Fatalf("stats: %+v", stats)
	}
}

func TestReleaseSourceFilterRestrictsScreenshotBackfillCandidates(t *testing.T) {
	ctx := context.Background()
	s, err := OpenSQLite(filepath.Join(t.TempDir(), "source-filter.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	javSite, _ := s.SaveSite(ctx, domain.Site{Title: "JavLibrary", Type: "Site", Name: "JavLibrary", Enabled: true})
	gigaSite, _ := s.SaveSite(ctx, domain.Site{Title: "GIGA", Type: "Site", Name: "GIGA", Enabled: true})
	_, _ = s.UpsertRelease(ctx, domain.Release{SiteID: javSite.ID, VideoID: "JAV-1", Title: "Jav", Source: "JavLibrary"})
	_, _ = s.UpsertRelease(ctx, domain.Release{SiteID: gigaSite.ID, VideoID: "GIGA-1", Title: "Giga", Source: "GIGA"})
	rows, err := s.Releases(ctx, domain.ReleaseFilter{Source: "javlibrary", Limit: 10})
	if err != nil || len(rows) != 1 || rows[0].VideoID != "JAV-1" {
		t.Fatalf("source-filtered releases=%v err=%v", rows, err)
	}
	if total, err := s.ReleasesCount(ctx, domain.ReleaseFilter{Source: "JavLibrary"}); err != nil || total != 1 {
		t.Fatalf("source-filtered count=%d err=%v", total, err)
	}
}

func TestReleaseTimestampsTrackMetadataChangesAndRepairInvalidOrder(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "release-timestamps.db")
	s, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	site, err := s.SaveSite(ctx, domain.Site{Title: "Timestamps", Type: "Site", Name: "JavLibrary", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	input := domain.Release{SiteID: site.ID, VideoID: "TIME-1", Title: "Original title", Source: "JavLibrary", Released: true}
	if created, err := s.UpsertRelease(ctx, input); err != nil || !created {
		t.Fatalf("initial upsert: created=%v err=%v", created, err)
	}
	item, err := s.Releases(ctx, domain.ReleaseFilter{Search: "TIME-1", Limit: 1})
	if err != nil || len(item) != 1 {
		t.Fatalf("release lookup: items=%d err=%v", len(item), err)
	}
	id := item[0].ID
	added := time.Now().UTC().Add(-4 * time.Hour).Truncate(time.Microsecond)
	unchangedUpdated := added.Add(time.Hour)
	if _, err := s.db.ExecContext(ctx, `UPDATE releases SET added_at=?,updated_at=? WHERE id=?`, added, unchangedUpdated, id); err != nil {
		t.Fatal(err)
	}
	if created, err := s.UpsertRelease(ctx, input); err != nil || created {
		t.Fatalf("unchanged upsert: created=%v err=%v", created, err)
	}
	got, err := s.Release(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if !got.UpdatedAt.Equal(unchangedUpdated) {
		t.Fatalf("unchanged scrape advanced updated_at: got=%v want=%v", got.UpdatedAt, unchangedUpdated)
	}

	input.Title = "New metadata title"
	if _, err := s.UpsertRelease(ctx, input); err != nil {
		t.Fatal(err)
	}
	got, err = s.Release(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if !got.UpdatedAt.After(unchangedUpdated) || got.UpdatedAt.Before(got.AddedAt) {
		t.Fatalf("metadata update timestamp invalid: added=%v updated=%v", got.AddedAt, got.UpdatedAt)
	}

	// Simulate an invalid legacy/imported row and verify the next startup
	// repairs it for display even if that release is never scraped again.
	futureAdded := time.Now().UTC().Add(2 * time.Hour).Truncate(time.Microsecond)
	if _, err := s.db.ExecContext(ctx, `UPDATE releases SET added_at=?,updated_at=? WHERE id=?`, futureAdded, futureAdded.Add(-time.Hour), id); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, err = s.Release(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if !got.UpdatedAt.Equal(got.AddedAt) {
		t.Fatalf("startup did not repair timestamp ordering: added=%v updated=%v", got.AddedAt, got.UpdatedAt)
	}
}

func TestDownloadSearchRunsPersistAndFilter(t *testing.T) {
	s, err := OpenSQLite(filepath.Join(t.TempDir(), "download-search-runs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	for _, run := range []domain.DownloadSearchRun{
		{Schedule: "recent", StartedAt: now.Add(-time.Minute), FinishedAt: now, Checked: 12, Found: 4, Downloaded: 2, Skipped: 9, Failed: 1, Error: "one failure"},
		{Schedule: "older", StartedAt: now.Add(time.Minute), FinishedAt: now.Add(2 * time.Minute), Checked: 20, Found: 3, Downloaded: 1, Skipped: 19},
	} {
		if _, err := s.SaveDownloadSearchRun(ctx, run); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := s.DownloadSearchRuns(ctx, "recent", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Checked != 12 || rows[0].Found != 4 || rows[0].Downloaded != 2 || rows[0].Failed != 1 {
		t.Fatalf("recent history=%+v", rows)
	}
	all, err := s.DownloadSearchRuns(ctx, "", 10)
	if err != nil || len(all) != 2 || all[0].Schedule != "older" {
		t.Fatalf("all history=%+v err=%v", all, err)
	}
}

func TestJobHistoryCombinesCategoriesAndPaginatesChronologically(t *testing.T) {
	ctx := context.Background()
	s, err := OpenSQLite(filepath.Join(t.TempDir(), "job-history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	started := time.Now().UTC().Add(-2 * time.Hour)
	finished := started.Add(15 * time.Minute)
	if _, err := s.SaveJob(ctx, domain.Job{Kind: "scrape", State: "completed", Mode: "quick", SiteTitle: "JavLibrary", StartedAt: started, FinishedAt: finished, Added: 3, Skipped: 20}); err != nil {
		t.Fatal(err)
	}
	site, err := s.SaveSite(ctx, domain.Site{Title: "Download site", Type: "Site", Name: "JavLibrary", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "HIST-1", Title: "History", Source: "JavLibrary"}); err != nil {
		t.Fatal(err)
	}
	releases, err := s.Releases(ctx, domain.ReleaseFilter{Search: "HIST-1", Limit: 1})
	if err != nil || len(releases) != 1 {
		t.Fatalf("release lookup: rows=%d err=%v", len(releases), err)
	}
	if _, err := s.SaveDownload(ctx, domain.Download{ReleaseID: releases[0].ID, Provider: "Sukebei", SourceType: "torrent", Query: "HIST-1", Status: "completed", MatchReason: "accepted filename", Files: json.RawMessage(`[]`)}); err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{"searched", "search_accepted", "search_rejected"} {
		if _, err := s.SaveDownload(ctx, domain.Download{ReleaseID: releases[0].ID, Provider: "Sukebei", SourceType: "Automatic Search", Query: "HIST-1", Status: status, MatchReason: "candidate audit row"}); err != nil {
			t.Fatal(err)
		}
	}
	first, total, err := s.JobHistory(ctx, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 || len(first) != 1 || first[0].Category != "Downloading" || first[0].Title != "HIST-1" || first[0].FinishedAt.IsZero() {
		t.Fatalf("first page=%+v total=%d", first, total)
	}
	second, total, err := s.JobHistory(ctx, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 || len(second) != 1 || second[0].Category != "Scraping" || second[0].Mode != "quick" || !second[0].StartedAt.Equal(started) || !second[0].FinishedAt.Equal(finished) {
		t.Fatalf("second page=%+v total=%d", second, total)
	}
}

func TestSQLiteDurationConditionsParseLeadingMinutes(t *testing.T) {
	ctx := context.Background()
	s, err := OpenSQLite(filepath.Join(t.TempDir(), "duration-filter.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	site, err := s.SaveSite(ctx, domain.Site{Title: "Durations", Type: "Site", Name: "JavLibrary", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, release := range []domain.Release{
		{SiteID: site.ID, VideoID: "DUR-1", Title: "Long", Duration: "120 min"},
		{SiteID: site.ID, VideoID: "DUR-2", Title: "Short", Duration: "89.5 minutes"},
		{SiteID: site.ID, VideoID: "DUR-3", Title: "Unknown", Duration: "Unknown"},
	} {
		if _, err := s.UpsertRelease(ctx, release); err != nil {
			t.Fatal(err)
		}
	}
	expr := `{"logic":"and","conditions":[{"field":"duration","op":"gte","value":"90"}]}`
	rows, err := s.Releases(ctx, domain.ReleaseFilter{SearchExpression: expr, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].VideoID != "DUR-1" {
		t.Fatalf("duration filter returned %+v, want only DUR-1", rows)
	}
}

func TestReleaseFilterOptionsSearchFullMetadataCaseInsensitively(t *testing.T) {
	ctx := context.Background()
	s, err := OpenSQLite(filepath.Join(t.TempDir(), "filter-options.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	site, _ := s.SaveSite(ctx, domain.Site{Title: "Moon Label", Type: "Label", Name: "JavLibrary", Enabled: true})
	if _, err := s.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "OPT-1", Title: "Options", Source: "JavLibrary", Actress: "Neo Akari", Studio: "Silver Studio", Label: "Crystal Label", Genres: []string{"Female Investigator"}}); err != nil {
		t.Fatal(err)
	}

	for name, tc := range map[string]struct {
		category string
		search   string
		want     string
	}{
		"reverse actress": {"actress", "akari neo", "Neo Akari"},
		"partial tag":     {"tag", "investigator", "Female Investigator"},
		"partial studio":  {"studio", "silver", "Silver Studio"},
		"release label":   {"label", "crystal", "Crystal Label"},
		"site label":      {"label", "moon", "Moon Label"},
	} {
		values, err := s.ReleaseFilterOptions(ctx, tc.category, tc.search)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		found := false
		for _, value := range values {
			found = found || value == tc.want
		}
		if !found {
			t.Fatalf("%s: options=%q, want %q", name, values, tc.want)
		}
	}
}

func TestReleaseConditionsSupportMetadataDatesNumbersAndStates(t *testing.T) {
	ctx := context.Background()
	s, err := OpenSQLite(filepath.Join(t.TempDir(), "all-release-conditions.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	site, _ := s.SaveSite(ctx, domain.Site{Title: "Condition Label", Type: "Label", Name: "JavLibrary", Enabled: true})
	if _, err := s.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "COND-ALL", Title: "Condition release", Source: "JavLibrary", ReleaseDate: "2024-05-01", Studio: "Bright Studio", Label: "Crystal Label", Duration: "120 min", Watchlist: true, MonitorDownload: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "COND-OTHER", Title: "Other", Source: "JavLibrary"}); err != nil {
		t.Fatal(err)
	}
	rows, err := s.Releases(ctx, domain.ReleaseFilter{Search: "COND-ALL", Limit: 10})
	if err != nil || len(rows) != 1 {
		t.Fatalf("release lookup: rows=%v err=%v", rows, err)
	}
	id := rows[0].ID
	if _, err := s.db.ExecContext(ctx, `UPDATE releases SET is_local=1,o_counter=7,play_count=11,last_o_count_at='2024-04-20',last_played_at='2024-04-25',added_at='2024-01-15',updated_at='2024-06-15' WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{"completed", "failed"} {
		if _, err := s.SaveDownload(ctx, domain.Download{ReleaseID: id, Status: status}); err != nil {
			t.Fatal(err)
		}
	}

	tests := map[string]string{
		"studio partial case insensitive": `{"field":"studio","value":"bright"}`,
		"label partial case insensitive":  `{"field":"label","value":"CRYSTAL"}`,
		"duration minimum":                `{"field":"duration","op":"gte","value":"120"}`,
		"o count minimum":                 `{"field":"o_count","op":"gte","value":"7"}`,
		"play count minimum":              `{"field":"play_count","op":"gte","value":"11"}`,
		"last o count after":              `{"field":"last_o_count","op":"after","value":"2024-04-01"}`,
		"last played before":              `{"field":"last_played","op":"before","value":"2024-05-01"}`,
		"date added before":               `{"field":"added_at","op":"before","value":"2025-01-01"}`,
		"date updated before":             `{"field":"updated_at","op":"before","value":"2024-07-01"}`,
		"release date after":              `{"field":"release_date","op":"after","value":"2024-04-01"}`,
		"watchlist":                       `{"field":"watchlist","value":"true"}`,
		"downloaded":                      `{"field":"downloaded","value":"true"}`,
		"download started":                `{"field":"download_started","value":"true"}`,
		"download failed":                 `{"field":"download_failed","value":"true"}`,
		"locally available":               `{"field":"local","value":"true"}`,
		"monitored":                       `{"field":"monitored","value":"true"}`,
	}
	for name, condition := range tests {
		expr := `{"logic":"and","conditions":[` + condition + `]}`
		matches, err := s.Releases(ctx, domain.ReleaseFilter{SearchExpression: expr, Limit: 10})
		if err != nil || len(matches) != 1 || matches[0].VideoID != "COND-ALL" {
			t.Fatalf("%s: matches=%v err=%v", name, matches, err)
		}
	}
	falseExpr := `{"logic":"and","conditions":[{"field":"watchlist","value":"false"}]}`
	matches, err := s.Releases(ctx, domain.ReleaseFilter{SearchExpression: falseExpr, Limit: 10})
	if err != nil || len(matches) != 1 || matches[0].VideoID != "COND-OTHER" {
		t.Fatalf("watchlist=no: matches=%v err=%v", matches, err)
	}
}

func TestStructuredReleaseSearchCanHideLocalMatches(t *testing.T) {
	ctx := context.Background()
	s, err := OpenSQLite(filepath.Join(t.TempDir(), "structured-hide-local.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	site, err := s.SaveSite(ctx, domain.Site{Title: "JavLibrary", Type: "Tag", Name: "JavLibrary", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, release := range []domain.Release{
		{SiteID: site.ID, VideoID: "LOCAL-1", Title: "Drug story", Genres: []string{"Drug"}},
		{SiteID: site.ID, VideoID: "REMOTE-1", Title: "Drug story", Genres: []string{"Drug"}},
	} {
		if _, err := s.UpsertRelease(ctx, release); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE releases SET is_local=1,stash_scene_id='scene-local-1' WHERE video_id='LOCAL-1'`); err != nil {
		t.Fatal(err)
	}
	expr := `{"logic":"and","conditions":[{"field":"tag","value":"Drug","exact":true}]}`
	rows, err := s.Releases(ctx, domain.ReleaseFilter{SearchExpression: expr, HideLocal: true, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].VideoID != "REMOTE-1" || rows[0].Local {
		t.Fatalf("structured search with hide-local returned %+v, want only REMOTE-1", rows)
	}
}

func TestActressSearchAcceptsReversedTwoPartNames(t *testing.T) {
	ctx := context.Background()
	s, err := OpenSQLite(filepath.Join(t.TempDir(), "actress-search.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	site, err := s.SaveSite(ctx, domain.Site{Title: "JavLibrary", Type: "Actress", Name: "JavLibrary", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "NAME-1", Title: "Reverse-name test", Actress: "Neo Akari"}); err != nil {
		t.Fatal(err)
	}

	tests := []domain.ReleaseFilter{
		{Search: "Akari Neo", Limit: 10},
		{Category: "Actress", Entries: "Akari Neo", Limit: 10},
		{SearchExpression: `{"logic":"and","conditions":[{"field":"actress","value":"Akari Neo","exact":true}]}`, Limit: 10},
	}
	for _, filter := range tests {
		rows, err := s.Releases(ctx, filter)
		if err != nil || len(rows) != 1 || rows[0].VideoID != "NAME-1" {
			t.Fatalf("reverse actress filter %+v returned %+v: %v", filter, rows, err)
		}
	}
}

func TestReleaseSearchDistinguishesLabelAndStudio(t *testing.T) {
	ctx := context.Background()
	s, err := OpenSQLite(filepath.Join(t.TempDir(), "label-studio-search.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	site, _ := s.SaveSite(ctx, domain.Site{Title: "Purple Label", Type: "Label", Name: "JavLibrary", Enabled: true})
	_, _ = s.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "META-1", Title: "Metadata search", Source: "JavLibrary", Studio: "Silver Studio"})
	for name, filter := range map[string]domain.ReleaseFilter{
		"global label":  {Search: "Purple Label", Limit: 10},
		"label filter":  {Category: "Label", Entries: "Purple*", Limit: 10},
		"global studio": {Search: "Silver Studio", Limit: 10},
		"studio filter": {Category: "Studio", Entries: "Silver*", Limit: 10},
	} {
		rows, err := s.Releases(ctx, filter)
		if err != nil || len(rows) != 1 || rows[0].VideoID != "META-1" {
			t.Fatalf("%s returned %+v: %v", name, rows, err)
		}
	}
}

// TestReleaseSearchIsCaseInsensitiveAcrossFields guards the fix that routed
// every branch of the free-text search's OR clause through
// Dialect.CaseInsensitiveLike (video_id, title, actress, studio, genres, and
// the site/"Label" EXISTS subquery). Before that fix, only the site/label
// subquery went through the dialect helper; the rest used a plain "LIKE ?",
// which happens to already be case-insensitive on SQLite's default collation
// (masking the bug here) but is always case-sensitive on PostgreSQL. This
// test exercises the same SQLite-backed store the rest of the suite uses, so
// it won't fail if the bug regresses on SQLite specifically - it exists to
// document and lock in the expected case-insensitive behavior at the
// releaseFilterWhere level, matched by the analogous CaseInsensitiveLike
// unit coverage for PostgresDialect elsewhere.
func TestReleaseSearchIsCaseInsensitiveAcrossFields(t *testing.T) {
	ctx := context.Background()
	s, err := OpenSQLite(filepath.Join(t.TempDir(), "case-insensitive-search.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	site, _ := s.SaveSite(ctx, domain.Site{Title: "Moon Label", Type: "Label", Name: "JavLibrary", Enabled: true})
	if _, err := s.UpsertRelease(ctx, domain.Release{
		SiteID:  site.ID,
		VideoID: "CASE-1",
		Title:   "Mixed Case Title",
		Source:  "JavLibrary",
		Actress: "Yui Tsubaki",
		Studio:  "Bright Studio",
		Genres:  []string{"Solowork", "Beautiful Girl"},
	}); err != nil {
		t.Fatal(err)
	}

	for name, search := range map[string]string{
		"video id upper":   "case-1",
		"video id lower":   "CASE-1",
		"title mixed":      "MIXED case",
		"actress mixed":    "yui TSUBAKI",
		"studio mixed":     "bright studio",
		"genre mixed":      "SOLOWORK",
		"site label mixed": "moon LABEL",
	} {
		rows, err := s.Releases(ctx, domain.ReleaseFilter{Search: search, Limit: 10})
		if err != nil || len(rows) != 1 || rows[0].VideoID != "CASE-1" {
			t.Fatalf("%s: search %q returned %+v: %v", name, search, rows, err)
		}
	}
}

// TestReleaseConditionsSearchIsCaseInsensitiveForTitleAndDescription guards
// the TODO-2.0 Task A case-insensitivity audit fix to releaseFilterWhere's
// Conditions (SearchExpression) builder: the title/description branch used
// to emit a bare "column LIKE ?" instead of routing through
// Dialect.CaseInsensitiveLike. As with
// TestReleaseSearchIsCaseInsensitiveAcrossFields above, this SQLite-backed
// test won't itself fail if the bug regresses on SQLite specifically (its
// default LIKE collation is already case-insensitive) - it documents and
// locks in the expected behavior here, matched by the decisive
// PostgresDialect{}-based assertion in
// TestReleaseFilterWhereConditionsUseCaseInsensitiveLikeOnPostgres.
func TestReleaseConditionsSearchIsCaseInsensitiveForTitleAndDescription(t *testing.T) {
	ctx := context.Background()
	s, err := OpenSQLite(filepath.Join(t.TempDir(), "conditions-case-insensitive.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	site, _ := s.SaveSite(ctx, domain.Site{Title: "JavLibrary", Type: "Site", Name: "JavLibrary", Enabled: true})
	if _, err := s.UpsertRelease(ctx, domain.Release{
		SiteID:  site.ID,
		VideoID: "COND-1",
		Title:   "Mixed Case Title",
		Source:  "JavLibrary",
		Story:   "A Mixed Case Story",
	}); err != nil {
		t.Fatal(err)
	}

	for name, expr := range map[string]string{
		"title mixed":       `{"logic":"and","conditions":[{"field":"title","value":"MIXED case"}]}`,
		"description mixed": `{"logic":"and","conditions":[{"field":"description","value":"mixed CASE story"}]}`,
	} {
		rows, err := s.Releases(ctx, domain.ReleaseFilter{SearchExpression: expr, Limit: 10})
		if err != nil || len(rows) != 1 || rows[0].VideoID != "COND-1" {
			t.Fatalf("%s: expression %q returned %+v: %v", name, expr, rows, err)
		}
	}
}

// TestReleaseConditionsSupportsAndOrGroups is the regression test for
// TODO-2.0 Task A's "AND/OR condition groups" feature: releaseFilterWhere's
// SearchExpression can now combine multiple independently-AND/OR'd groups
// of conditions - e.g. "(actress=A OR actress=B) AND (tag=X OR tag=Y)" -
// which a single flat AND/OR list can't express. It also proves the
// backward-compatible legacy flat shape (top-level "conditions", no
// "groups") still matches exactly as it always did.
func TestReleaseConditionsSupportsAndOrGroups(t *testing.T) {
	ctx := context.Background()
	s, err := OpenSQLite(filepath.Join(t.TempDir(), "conditions-groups.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	site, _ := s.SaveSite(ctx, domain.Site{Title: "JavLibrary", Type: "Site", Name: "JavLibrary", Enabled: true})
	seed := func(videoID, actress string, genres []string) {
		if _, err := s.UpsertRelease(ctx, domain.Release{
			SiteID: site.ID, VideoID: videoID, Title: "Release " + videoID, Source: "JavLibrary",
			Actress: actress, Genres: genres,
		}); err != nil {
			t.Fatal(err)
		}
	}
	seed("GRP-1", "Actress A", []string{"Tag X"}) // both groups true -> matches
	seed("GRP-2", "Actress A", []string{"Tag Z"}) // group 1 true, group 2 false -> no match
	seed("GRP-3", "Actress C", []string{"Tag X"}) // group 1 false, group 2 true -> no match
	seed("GRP-4", "Actress B", []string{"Tag Y"}) // both groups true -> matches

	grouped := `{"logic":"and","groups":[` +
		`{"logic":"or","conditions":[{"field":"actress","value":"Actress A"},{"field":"actress","value":"Actress B"}]},` +
		`{"logic":"or","conditions":[{"field":"tag","value":"Tag X"},{"field":"tag","value":"Tag Y"}]}` +
		`]}`
	rows, err := s.Releases(ctx, domain.ReleaseFilter{SearchExpression: grouped, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, r := range rows {
		got[r.VideoID] = true
	}
	if !got["GRP-1"] || !got["GRP-4"] || got["GRP-2"] || got["GRP-3"] || len(rows) != 2 {
		t.Fatalf("grouped AND-of-ORs expression matched %+v, want exactly GRP-1 and GRP-4", rows)
	}

	// An OR of two AND groups: "(actress=A AND tag=X) OR (actress=B AND
	// tag=Y)" should match the same two releases via the opposite logic
	// shape, and exclude GRP-2/GRP-3 for the same reason in reverse.
	orOfAnds := `{"logic":"or","groups":[` +
		`{"logic":"and","conditions":[{"field":"actress","value":"Actress A"},{"field":"tag","value":"Tag X"}]},` +
		`{"logic":"and","conditions":[{"field":"actress","value":"Actress B"},{"field":"tag","value":"Tag Y"}]}` +
		`]}`
	rows, err = s.Releases(ctx, domain.ReleaseFilter{SearchExpression: orOfAnds, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	got = map[string]bool{}
	for _, r := range rows {
		got[r.VideoID] = true
	}
	if !got["GRP-1"] || !got["GRP-4"] || len(rows) != 2 {
		t.Fatalf("OR-of-ANDs expression matched %+v, want exactly GRP-1 and GRP-4", rows)
	}

	// Legacy flat shape (no "groups") must still behave exactly as before:
	// a single OR list matches any release with either actress.
	legacyFlat := `{"logic":"or","conditions":[{"field":"actress","value":"Actress A"},{"field":"actress","value":"Actress B"}]}`
	rows, err = s.Releases(ctx, domain.ReleaseFilter{SearchExpression: legacyFlat, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	got = map[string]bool{}
	for _, r := range rows {
		got[r.VideoID] = true
	}
	if !got["GRP-1"] || !got["GRP-2"] || !got["GRP-4"] || got["GRP-3"] || len(rows) != 3 {
		t.Fatalf("legacy flat OR expression matched %+v, want GRP-1, GRP-2, GRP-4", rows)
	}
}

// TestReleaseLabelAndDownloadStatus covers Phase 6: the new release "label"
// property (persisted via UpsertRelease, preserved across partial rescrapes,
// and patchable via PatchRelease) and the computed download_status column
// that reflects the most relevant row in the downloads table.
func TestReleaseLabelAndDownloadStatus(t *testing.T) {
	ctx := context.Background()
	s, err := OpenSQLite(filepath.Join(t.TempDir(), "label-status.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	site, err := s.SaveSite(ctx, domain.Site{Title: "JavLibrary", Type: "Site", Name: "JavLibrary", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "LBL-1", Title: "Label test", Source: "JavLibrary", Studio: "Some Studio", Label: "Original Label"}); err != nil {
		t.Fatal(err)
	}
	fetch := func() domain.Release {
		t.Helper()
		items, err := s.Releases(ctx, domain.ReleaseFilter{Search: "LBL-1"})
		if err != nil || len(items) != 1 {
			t.Fatalf("release lookup: items=%d err=%v", len(items), err)
		}
		return items[0]
	}
	got := fetch()
	if got.Label != "Original Label" {
		t.Fatalf("label not persisted: %+v", got)
	}
	if got.DownloadStatus != "" {
		t.Fatalf("expected no download status yet: %+v", got)
	}

	// A partial rescrape (no label supplied) must not erase the existing value.
	if _, err := s.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "LBL-1", Title: "Listing title", Source: "JavLibrary"}); err != nil {
		t.Fatal(err)
	}
	if got := fetch(); got.Label != "Original Label" {
		t.Fatalf("partial rescrape erased label: %+v", got)
	}

	dl, err := s.SaveDownload(ctx, domain.Download{ReleaseID: got.ID, Provider: "Sukebei", Query: "LBL-1", Status: "downloading"})
	if err != nil {
		t.Fatal(err)
	}
	if got := fetch(); got.DownloadStatus != "downloading" {
		t.Fatalf("expected downloading status: %+v", got)
	}
	dl.Status = "completed"
	if _, err := s.SaveDownload(ctx, dl); err != nil {
		t.Fatal(err)
	}
	if got := fetch(); got.DownloadStatus != "completed" {
		t.Fatalf("expected completed status: %+v", got)
	}

	updated := "Patched Label"
	if err := s.PatchRelease(ctx, got.ID, nil, nil, nil, nil, nil, nil, &updated, nil); err != nil {
		t.Fatal(err)
	}
	if got := fetch(); got.Label != "Patched Label" {
		t.Fatalf("patch did not update label: %+v", got)
	}
}

// TestReleaseDownloadedAtAndStashAddedAt covers TODO-2.0's card/detail
// "Downloaded (with date)" and "Added to StashApp (with date)": DownloadedAt
// should reflect the most recent completed download (and clear back to
// zero-value if that download is later removed), and StashAddedAt should be
// set the first time a release gets a StashApp scene ID and then stay fixed
// across later syncs of the same release.
func TestReleaseDownloadedAtAndStashAddedAt(t *testing.T) {
	ctx := context.Background()
	s, err := OpenSQLite(filepath.Join(t.TempDir(), "downloaded-stash-dates.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	site, err := s.SaveSite(ctx, domain.Site{Title: "JavLibrary", Type: "Site", Name: "JavLibrary", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "DATE-1", Title: "Dated release", Source: "JavLibrary"}); err != nil {
		t.Fatal(err)
	}
	fetch := func() domain.Release {
		t.Helper()
		items, err := s.Releases(ctx, domain.ReleaseFilter{Search: "DATE-1"})
		if err != nil || len(items) != 1 {
			t.Fatalf("release lookup: items=%d err=%v", len(items), err)
		}
		return items[0]
	}
	got := fetch()
	if !got.DownloadedAt.IsZero() {
		t.Fatalf("expected no downloaded_at yet: %+v", got)
	}
	if !got.StashAddedAt.IsZero() {
		t.Fatalf("expected no stash_added_at yet: %+v", got)
	}

	dl, err := s.SaveDownload(ctx, domain.Download{ReleaseID: got.ID, Provider: "Sukebei", Query: "DATE-1", Status: "downloading", SourceReference: "https://sukebei.nyaa.si/view/123", Seeds: 23, Peers: 4, ETASeconds: 321, SeenComplete: 1700000000})
	if err != nil {
		t.Fatal(err)
	}
	latest, err := s.LatestReleaseDownload(ctx, got.ID)
	if err != nil || latest.ID != dl.ID || latest.Seeds != 23 || latest.Peers != 4 || latest.ETASeconds != 321 || latest.SeenComplete != 1700000000 || latest.AddedAt.IsZero() {
		t.Fatalf("latest release download telemetry: %+v err=%v", latest, err)
	}
	if got := fetch(); !got.DownloadedAt.IsZero() {
		t.Fatalf("still downloading: expected no downloaded_at yet: %+v", got)
	}
	dl.Status = "completed"
	if _, err := s.SaveDownload(ctx, dl); err != nil {
		t.Fatal(err)
	}
	completedAt := fetch().DownloadedAt
	if completedAt.IsZero() {
		t.Fatalf("expected downloaded_at to be set once completed: %+v", fetch())
	}

	if err := s.SetStashState(ctx, got.ID, true, "scene-1"); err != nil {
		t.Fatal(err)
	}
	firstStashAddedAt := fetch().StashAddedAt
	if firstStashAddedAt.IsZero() {
		t.Fatalf("expected stash_added_at to be set after first sync: %+v", fetch())
	}
	if !fetch().DownloadedAt.Equal(completedAt) {
		t.Fatalf("stash sync should not disturb downloaded_at: %+v", fetch())
	}

	// A later sync with the same (or a different) scene ID must not move
	// stash_added_at forward - it marks when the release was first added,
	// not the most recent sync.
	if err := s.SetStashState(ctx, got.ID, true, "scene-1"); err != nil {
		t.Fatal(err)
	}
	if second := fetch().StashAddedAt; !second.Equal(firstStashAddedAt) {
		t.Fatalf("expected stash_added_at to stay fixed across resyncs: first=%v second=%v", firstStashAddedAt, second)
	}
}

func TestUpsertReleasePreservesDetailMetadataOnPartialScrape(t *testing.T) {
	ctx := context.Background()
	s, err := OpenSQLite(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	site, err := s.SaveSite(ctx, domain.Site{Title: "JavLibrary", Type: "Site", Name: "JavLibrary", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	full := domain.Release{
		SiteID: site.ID, VideoID: "PRED-888", ScraperID: "javabc", Title: "Full title",
		ReleaseDate: "2026-08-15", Source: "JavLibrary", ImageURL: "https://example.test/cover.jpg",
		ProductURL: "https://example.test/product", Actress: "Actor", Director: "Director",
		Studio: "Studio", Genres: []string{"Drama", "Featured Actress"}, Duration: "120 min",
		Story: "Description", Screenshots: []string{"https://example.test/shot.jpg"}, Released: true,
	}
	if created, err := s.UpsertRelease(ctx, full); err != nil || !created {
		t.Fatalf("initial upsert: created=%v err=%v", created, err)
	}
	partial := domain.Release{SiteID: site.ID, VideoID: "PRED-888", Title: "Listing title", Source: "JavLibrary", Released: true}
	if created, err := s.UpsertRelease(ctx, partial); err != nil || created {
		t.Fatalf("partial upsert: created=%v err=%v", created, err)
	}
	items, err := s.Releases(ctx, domain.ReleaseFilter{Search: "PRED-888"})
	if err != nil || len(items) != 1 {
		t.Fatalf("release lookup: items=%d err=%v", len(items), err)
	}
	got := items[0]
	if got.Actress != full.Actress || got.Director != full.Director || got.Studio != full.Studio || got.ReleaseDate != full.ReleaseDate || got.ImageURL != full.ImageURL || got.ProductURL != full.ProductURL || got.Duration != full.Duration || got.Story != full.Story {
		t.Fatalf("detail metadata was erased: %+v", got)
	}
	if len(got.Genres) != 2 || len(got.Screenshots) != 1 {
		t.Fatalf("list metadata was erased: genres=%v screenshots=%v", got.Genres, got.Screenshots)
	}
	if got.Title != "Listing title" {
		t.Fatalf("non-empty listing title was not updated: %q", got.Title)
	}
}

func TestReleaseFiltersReverseActressNameStructuredSearchAndWatchlist(t *testing.T) {
	ctx := context.Background()
	s, err := OpenSQLite(filepath.Join(t.TempDir(), "filters.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	site, err := s.SaveSite(ctx, domain.Site{Title: "Actress feed", Type: "Actress", Name: "JavLibrary", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "TEST-1", Title: "Moon &Amp; Story", Actress: "Neo Akari | (Kojima Ami) | Other Actress", Studio: "M&#039;s Video Group", Genres: []string{"Drama", "Best, Omnibus"}, Story: "A &lt;b&gt;detailed&lt;/b&gt; story", Watchlist: true})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := s.Releases(ctx, domain.ReleaseFilter{Category: "Actress", Entries: "Akari Neo", Watchlist: true, Limit: 10})
	if err != nil || len(rows) != 1 {
		t.Fatalf("reverse actress filter: rows=%d err=%v", len(rows), err)
	}
	if rows[0].Actress != "Neo Akari, Kojima Ami, Other Actress" {
		t.Fatalf("actresses were not normalized separately: %q", rows[0].Actress)
	}
	if len(rows[0].Actresses) != 3 || rows[0].Actresses[0] != "Neo Akari" || rows[0].Actresses[1] != "Kojima Ami" || rows[0].Actresses[2] != "Other Actress" {
		t.Fatalf("structured actresses were not returned in order: %v", rows[0].Actresses)
	}
	if rows[0].Title != "Moon & Story" || rows[0].Studio != "M's Video Group" || rows[0].Story != "A detailed story" {
		t.Fatalf("HTML markup was not cleaned: studio=%q story=%q", rows[0].Studio, rows[0].Story)
	}
	rows, err = s.Releases(ctx, domain.ReleaseFilter{Category: "Actress", Entries: "Kojima Ami", Limit: 10})
	if err != nil || len(rows) != 1 {
		t.Fatalf("parenthesized actress was not independently searchable: rows=%d err=%v", len(rows), err)
	}
	exactActress := `{"logic":"and","conditions":[{"field":"actress","value":"Neo Akari","exact":true}]}`
	rows, err = s.Releases(ctx, domain.ReleaseFilter{SearchExpression: exactActress, Limit: 10})
	if err != nil || len(rows) != 1 {
		t.Fatalf("exact actress token filter: rows=%d err=%v", len(rows), err)
	}
	rows, err = s.Releases(ctx, domain.ReleaseFilter{Category: "Tag", Entries: `["Best, Omnibus"]`, Limit: 10})
	if err != nil || len(rows) != 1 {
		t.Fatalf("comma-containing normalized tag filter: rows=%d err=%v", len(rows), err)
	}
	rows, err = s.Releases(ctx, domain.ReleaseFilter{Category: "Actress", Entries: "koji*ami", Limit: 10})
	if err != nil || len(rows) != 1 {
		t.Fatalf("wildcard actress filter: rows=%d err=%v", len(rows), err)
	}
	rows, err = s.Releases(ctx, domain.ReleaseFilter{Category: "Tag", Entries: "omni", Limit: 10})
	if err != nil || len(rows) != 1 {
		t.Fatalf("partial tag filter: rows=%d err=%v", len(rows), err)
	}
	rows, err = s.Releases(ctx, domain.ReleaseFilter{Category: "Studio", Entries: "video gr?up", Limit: 10})
	if err != nil || len(rows) != 1 {
		t.Fatalf("wildcard studio filter: rows=%d err=%v", len(rows), err)
	}
	rows, err = s.Releases(ctx, domain.ReleaseFilter{Category: "Actress", Entries: `[]`, Limit: 10})
	if err != nil || len(rows) != 1 {
		t.Fatalf("empty restored category entries: rows=%d err=%v", len(rows), err)
	}
	var actresses, tags int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM release_actresses`).Scan(&actresses); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM release_tags`).Scan(&tags); err != nil {
		t.Fatal(err)
	}
	if actresses != 3 || tags != 2 {
		t.Fatalf("normalized metadata rows: actresses=%d tags=%d", actresses, tags)
	}
	expression := `{"logic":"and","conditions":[{"field":"title","value":"Moon","exact":false},{"field":"description","value":"*detailed*","wildcard":true}]}`
	rows, err = s.Releases(ctx, domain.ReleaseFilter{SearchExpression: expression, Limit: 10})
	if err != nil || len(rows) != 1 {
		t.Fatalf("structured filter: rows=%d err=%v", len(rows), err)
	}
}

func TestStructuredActressListPreservesNamesContainingCommas(t *testing.T) {
	ctx := context.Background()
	s, err := OpenSQLite(filepath.Join(t.TempDir(), "structured-actresses.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	site, err := s.SaveSite(ctx, domain.Site{Title: "Structured cast", Type: "Site", Name: "JavLibrary", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"Surname, Given", "Second Actress"}
	if _, err := s.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "CAST-1", Source: "JavLibrary", Title: "Structured cast", Actresses: want}); err != nil {
		t.Fatal(err)
	}
	rows, err := s.Releases(ctx, domain.ReleaseFilter{Search: "Surname, Given", Limit: 10})
	if err != nil || len(rows) != 1 {
		t.Fatalf("search rows=%d err=%v", len(rows), err)
	}
	if fmt.Sprint(rows[0].Actresses) != fmt.Sprint(want) {
		t.Fatalf("structured actress boundary was lost: got=%v want=%v", rows[0].Actresses, want)
	}
}

// TestReleaseFilterIgnoreTagsAndTitlesHideMatchesByDefault covers the
// Release Library's "ignore rules" feature: IgnoreTags/IgnoreTitles (only
// populated by the HTTP handler when the "Show non-preferred" toggle is
// off) must exclude a matching release from both Releases and
// ReleasesCount, while leaving a non-matching release visible.
func TestReleaseFilterIgnoreTagsAndTitlesHideMatchesByDefault(t *testing.T) {
	ctx := context.Background()
	s, err := OpenSQLite(filepath.Join(t.TempDir(), "ignore-filters.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	site, err := s.SaveSite(ctx, domain.Site{Title: "Feed", Type: "Actress", Name: "JavLibrary", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "IGN-1", Title: `IGN-1 "I Want To Be Insulted Like Garbage"`, Genres: []string{"Drama"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "IGN-2", Title: "IGN-2 A Perfectly Normal Title", Genres: []string{"Comedy"}}); err != nil {
		t.Fatal(err)
	}

	// No ignore rules: both releases are visible.
	rows, err := s.Releases(ctx, domain.ReleaseFilter{Limit: 10})
	if err != nil || len(rows) != 2 {
		t.Fatalf("expected both releases with no ignore rules: rows=%d err=%v", len(rows), err)
	}

	// IgnoreTags excludes the Drama-tagged release only.
	rows, err = s.Releases(ctx, domain.ReleaseFilter{IgnoreTags: []string{"drama"}, Limit: 10})
	if err != nil || len(rows) != 1 || rows[0].VideoID != "IGN-2" {
		t.Fatalf("expected only IGN-2 with ignore_tags=drama: rows=%+v err=%v", rows, err)
	}
	count, err := s.ReleasesCount(ctx, domain.ReleaseFilter{IgnoreTags: []string{"drama"}})
	if err != nil || count != 1 {
		t.Fatalf("expected ReleasesCount to agree with Releases: count=%d err=%v", count, err)
	}

	// IgnoreTitles with a wildcard excludes the matching title only.
	rows, err = s.Releases(ctx, domain.ReleaseFilter{IgnoreTitles: []string{"*Insulted*"}, Limit: 10})
	if err != nil || len(rows) != 1 || rows[0].VideoID != "IGN-2" {
		t.Fatalf("expected only IGN-2 with a title ignore rule: rows=%+v err=%v", rows, err)
	}

	// Both rules combined still return zero results when they match every
	// release, proving the two exclusions compose with AND against the rest
	// of the query rather than one silently overriding the other.
	rows, err = s.Releases(ctx, domain.ReleaseFilter{IgnoreTags: []string{"drama"}, IgnoreTitles: []string{"*Normal Title*"}, Limit: 10})
	if err != nil || len(rows) != 0 {
		t.Fatalf("expected both releases excluded, got rows=%+v err=%v", rows, err)
	}
}

func TestExplicitMonitoringIsClearedWhenReleaseBecomesLocal(t *testing.T) {
	ctx := context.Background()
	s, err := OpenSQLite(filepath.Join(t.TempDir(), "site-monitoring.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	site, err := s.SaveSite(ctx, domain.Site{Title: "Neo Akari", Type: "Actress", Name: "JavLibrary", AutoMonitorFuture: true, Enabled: true})
	if err != nil || !site.AutoMonitorFuture {
		t.Fatalf("site mode: site=%+v err=%v", site, err)
	}
	_, _ = s.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "TEST-1", Title: "Missing", Source: "JavLibrary"})
	rows, _ := s.Releases(ctx, domain.ReleaseFilter{Search: "TEST-1", Limit: 10})
	if err := s.SetReleaseMonitoring(ctx, rows[0].ID, true, "site_future", site.ID); err != nil {
		t.Fatal(err)
	}
	rows, err = s.Releases(ctx, domain.ReleaseFilter{MonitorDownload: true, Limit: 10})
	if err != nil || len(rows) != 1 || !rows[0].MonitorDownload || rows[0].MonitorReason != "site_future" {
		t.Fatalf("site-managed monitoring: rows=%+v err=%v", rows, err)
	}
	releaseID := rows[0].ID
	if err := s.SetStashState(ctx, releaseID, true, "scene-1"); err != nil {
		t.Fatal(err)
	}
	rows, err = s.Releases(ctx, domain.ReleaseFilter{MonitorDownload: true, Limit: 10})
	if err != nil || len(rows) != 0 {
		t.Fatalf("local release remained in inherited missing-release monitoring: rows=%+v err=%v", rows, err)
	}
	stored, err := s.Release(ctx, releaseID)
	if err != nil || stored.MonitorDownload || stored.MonitorReason != "" {
		t.Fatalf("local transition did not clear monitoring: release=%+v err=%v", stored, err)
	}
}

func TestSiteMonitoringRedesignMigration(t *testing.T) {
	ctx := context.Background()
	s, err := OpenSQLite(filepath.Join(t.TempDir(), "site-monitoring-migration.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	future, _ := s.SaveSite(ctx, domain.Site{Title: "Future", Type: "Site", Name: "JavLibrary", Enabled: true})
	all, _ := s.SaveSite(ctx, domain.Site{Title: "All", Type: "Site", Name: "JavLibrary", Enabled: true})
	_, _ = s.UpsertRelease(ctx, domain.Release{SiteID: all.ID, VideoID: "OLD-1", Title: "Old", Source: "JavLibrary"})
	rows, _ := s.Releases(ctx, domain.ReleaseFilter{Search: "OLD-1", Limit: 10})
	if _, err := s.db.ExecContext(ctx, `DELETE FROM settings WHERE key='site_monitoring_redesign_v1'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE sites SET download=1,download_mode='future' WHERE id=?`, future.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE sites SET download=1,download_mode='all' WHERE id=?`, all.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE releases SET site_monitor_download=1 WHERE id=?`, rows[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := s.migrateSiteMonitoringRedesign(ctx); err != nil {
		t.Fatal(err)
	}
	sites, _ := s.Sites(ctx)
	byID := map[int64]domain.Site{}
	for _, site := range sites {
		byID[site.ID] = site
	}
	if !byID[future.ID].AutoMonitorFuture || byID[all.ID].AutoMonitorFuture {
		t.Fatalf("future migration mismatch: future=%+v all=%+v", byID[future.ID], byID[all.ID])
	}
	migrated, err := s.Release(ctx, rows[0].ID)
	if err != nil || !migrated.MonitorDownload || migrated.MonitorReason != "migrated_site" {
		t.Fatalf("existing inherited monitoring was not materialized: release=%+v err=%v", migrated, err)
	}
}

func TestReleaseIdentityDeduplicatesAcrossSitesAndPreservesAssociations(t *testing.T) {
	ctx := context.Background()
	s, err := OpenSQLite(filepath.Join(t.TempDir(), "release-identity.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	first, _ := s.SaveSite(ctx, domain.Site{Title: "Niimura Akari", Type: "Actress", Name: "JavLibrary", Enabled: true})
	second, _ := s.SaveSite(ctx, domain.Site{Title: "Tanaka Nene", Type: "Actress", Name: "JavLibrary", Enabled: true})
	created, err := s.UpsertRelease(ctx, domain.Release{SiteID: first.ID, VideoID: "REAL-966", ScraperID: "javme3j23m", Title: "REAL-966 title", Source: "JavLibrary"})
	if err != nil || !created {
		t.Fatalf("first upsert: created=%v err=%v", created, err)
	}
	created, err = s.UpsertRelease(ctx, domain.Release{SiteID: second.ID, VideoID: "real966", ScraperID: "javme3j23m", Title: "REAL-966 title", Source: "JavLibrary"})
	if err != nil || created {
		t.Fatalf("second upsert created a duplicate: created=%v err=%v", created, err)
	}
	rows, err := s.Releases(ctx, domain.ReleaseFilter{Search: "REAL-966", Limit: 10})
	if err != nil || len(rows) != 1 || len(rows[0].SiteIDs) != 2 || len(rows[0].SiteTitles) != 2 {
		t.Fatalf("deduplicated associations: rows=%+v err=%v", rows, err)
	}
	for _, siteTitle := range []string{first.Title, second.Title} {
		filtered, filterErr := s.Releases(ctx, domain.ReleaseFilter{Site: siteTitle, Limit: 10})
		if filterErr != nil || len(filtered) != 1 {
			t.Fatalf("site %q association missing: rows=%+v err=%v", siteTitle, filtered, filterErr)
		}
	}
	if err := s.SetReleaseMonitoring(ctx, rows[0].ID, true, "site_future", second.ID); err != nil {
		t.Fatal(err)
	}
	monitored, _ := s.Releases(ctx, domain.ReleaseFilter{MonitorDownload: true, Limit: 10})
	if len(monitored) != 1 || monitored[0].MonitorReason != "site_future" || monitored[0].MonitorSiteID != second.ID {
		t.Fatalf("site-origin monitoring not recorded: %+v", monitored)
	}
	if err := s.DeleteSite(ctx, first.ID); err != nil {
		t.Fatal(err)
	}
	rows, err = s.Releases(ctx, domain.ReleaseFilter{Search: "REAL-966", Limit: 10})
	if err != nil || len(rows) != 1 || rows[0].SiteID != second.ID {
		t.Fatalf("release was not retained after deleting its primary site: rows=%+v err=%v", rows, err)
	}
}

func TestDeleteNotificationsIsScopedToTypeAndSelection(t *testing.T) {
	ctx := context.Background()
	s, err := OpenSQLite(filepath.Join(t.TempDir(), "notification-clear.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	site, _ := s.SaveSite(ctx, domain.Site{Title: "Test", Type: "Site", Name: "JavLibrary", Enabled: true})
	for _, videoID := range []string{"ONE-1", "TWO-2"} {
		_, _ = s.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: videoID, Title: videoID, Source: "JavLibrary"})
	}
	releases, _ := s.Releases(ctx, domain.ReleaseFilter{Limit: 10})
	for _, release := range releases {
		_, _ = s.CreateNotification(ctx, release.ID, "new_release", "New")
		_, _ = s.CreateNotification(ctx, release.ID, "downloaded", "Downloaded")
	}
	newRows, _ := s.Notifications(ctx, "new_release")
	deleted, err := s.DeleteNotifications(ctx, "new_release", []int64{newRows[0].ID})
	if err != nil || deleted != 1 {
		t.Fatalf("selected clear: deleted=%d err=%v", deleted, err)
	}
	newRows, _ = s.Notifications(ctx, "new_release")
	downloaded, _ := s.Notifications(ctx, "downloaded")
	if len(newRows) != 1 || len(downloaded) != 2 {
		t.Fatalf("clear leaked across selection/type: new=%d downloaded=%d", len(newRows), len(downloaded))
	}
	deleted, err = s.DeleteNotifications(ctx, "downloaded", nil)
	if err != nil || deleted != 2 {
		t.Fatalf("tab clear: deleted=%d err=%v", deleted, err)
	}
}

func TestStashAvailabilityCreatesAndRemovesLocalNotification(t *testing.T) {
	ctx := context.Background()
	s, err := OpenSQLite(filepath.Join(t.TempDir(), "local-notification.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	site, _ := s.SaveSite(ctx, domain.Site{Title: "Local test", Type: "Site", Name: "JavLibrary", Enabled: true})
	_, _ = s.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "LOCAL-1", Title: "Local release", Source: "JavLibrary"})
	releases, _ := s.Releases(ctx, domain.ReleaseFilter{Limit: 10})
	if err := s.SetStashState(ctx, releases[0].ID, true, "scene-1"); err != nil {
		t.Fatal(err)
	}
	local, err := s.Notifications(ctx, "local_available")
	if err != nil || len(local) != 1 || local[0].Release == nil || !local[0].Release.Local {
		t.Fatalf("local notifications=%+v err=%v", local, err)
	}
	if err := s.SetStashState(ctx, releases[0].ID, false, ""); err != nil {
		t.Fatal(err)
	}
	local, err = s.Notifications(ctx, "local_available")
	if err != nil || len(local) != 0 {
		t.Fatalf("removed local notifications=%+v err=%v", local, err)
	}
}

func TestLocalNotificationBackfillRunsOnce(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "local-notification-backfill.db")
	s, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	site, _ := s.SaveSite(ctx, domain.Site{Title: "Existing local", Type: "Site", Name: "JavLibrary", Enabled: true})
	_, _ = s.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "LOCAL-OLD", Title: "Existing local release", Source: "JavLibrary"})
	if _, err := s.db.Exec(`UPDATE releases SET is_local=1,stash_scene_id='old-scene'; DELETE FROM settings WHERE key='local_available_notifications_v1'`); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := s.Notifications(ctx, "local_available")
	if err != nil || len(rows) != 1 {
		t.Fatalf("backfilled local notifications=%+v err=%v", rows, err)
	}
	if _, err := s.DeleteNotifications(ctx, "local_available", nil); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	rows, err = s.Notifications(ctx, "local_available")
	if err != nil || len(rows) != 0 {
		t.Fatalf("one-time backfill recreated cleared notifications=%+v err=%v", rows, err)
	}
}

func TestLegacyReleaseMetadataMigratesLosslesslyAndDropsDuplicateColumns(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "backfill.db")
	s, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	site, _ := s.SaveSite(ctx, domain.Site{Title: "Backfill", Type: "Site", Name: "JavLibrary", Enabled: true})
	_, err = s.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "BACK-1", Title: "Backfill"})
	if err != nil {
		t.Fatal(err)
	}
	// Recreate the two columns from a pre-normalization database and remove
	// the relationship rows/settings so reopening exercises the real upgrade
	// path rather than the current write path.
	if _, err := s.db.Exec(`ALTER TABLE releases ADD COLUMN actress TEXT NOT NULL DEFAULT '';
		ALTER TABLE releases ADD COLUMN genres TEXT NOT NULL DEFAULT '[]';
		UPDATE releases SET actress='One Actress, Two Actress',genres='["Drama","Best, Omnibus"]' WHERE video_id='BACK-1';
		DELETE FROM release_actresses; DELETE FROM release_tags;
		INSERT INTO release_actresses(release_id,position,name,name_normalized) SELECT id,0,'One Actress','one actress' FROM releases WHERE video_id='BACK-1';
		INSERT INTO release_tags(release_id,position,name,name_normalized) SELECT id,0,'Drama','drama' FROM releases WHERE video_id='BACK-1';
		DELETE FROM settings WHERE key IN ('metadata_text_cleanup_v1','normalized_release_metadata_v1')`); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, present, err := s.ReleaseForSite(ctx, site.ID, "", "BACK-1")
	if err != nil || !present {
		t.Fatalf("migrated release=%+v present=%v err=%v", got, present, err)
	}
	if fmt.Sprint(got.Actresses) != "[One Actress Two Actress]" || fmt.Sprint(got.Genres) != "[Drama Best, Omnibus]" {
		t.Fatalf("lossy normalized metadata: actresses=%v tags=%v", got.Actresses, got.Genres)
	}
	for _, column := range []string{"actress", "genres"} {
		if exists, err := s.columnExists(ctx, "releases", column); err != nil || exists {
			t.Fatalf("legacy column %q still exists=%v err=%v", column, exists, err)
		}
	}
}

func TestReleaseDownloadMonitoringPersistsAndFilters(t *testing.T) {
	ctx := context.Background()
	s, err := OpenSQLite(filepath.Join(t.TempDir(), "monitor.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	site, _ := s.SaveSite(ctx, domain.Site{Title: "Monitored", Type: "Site", Name: "GIGA", Enabled: true})
	_, _ = s.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "MON-1", Title: "Monitor me", Source: "GIGA"})
	releases, _ := s.Releases(ctx, domain.ReleaseFilter{Limit: 10})
	monitor := true
	if err := s.PatchRelease(ctx, releases[0].ID, nil, nil, nil, nil, nil, &monitor, nil, nil); err != nil {
		t.Fatal(err)
	}
	monitored, err := s.Releases(ctx, domain.ReleaseFilter{MonitorDownload: true, Limit: 10})
	if err != nil || len(monitored) != 1 || !monitored[0].MonitorDownload {
		t.Fatalf("monitored releases=%+v err=%v", monitored, err)
	}
}

// TestBulkSetReleaseFlagsAppliesToEverySelectedReleaseAndFilterFindsThem
// covers the "Releases checked by the scheduled job" table's mass-select
// bulk actions: stop monitoring and set the persistent "allow non-preferred
// filenames" override across every selected release id in one call, and the
// AllowNonPreferredFilenames filter that lets the table find them again.
func TestBulkSetReleaseFlagsAppliesToEverySelectedReleaseAndFilterFindsThem(t *testing.T) {
	ctx := context.Background()
	s, err := OpenSQLite(filepath.Join(t.TempDir(), "bulk-flags.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	site, _ := s.SaveSite(ctx, domain.Site{Title: "Bulk", Type: "Site", Name: "GIGA", Enabled: true})
	for _, videoID := range []string{"BULK-1", "BULK-2", "BULK-3"} {
		_, _ = s.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: videoID, Title: "T", Source: "GIGA"})
	}
	rows, err := s.Releases(ctx, domain.ReleaseFilter{Limit: 10})
	if err != nil || len(rows) != 3 {
		t.Fatalf("release setup failed: rows=%+v err=%v", rows, err)
	}
	var ids []int64
	monitor := true
	for _, r := range rows {
		ids = append(ids, r.ID)
		if err := s.PatchRelease(ctx, r.ID, nil, nil, nil, nil, nil, &monitor, nil, nil); err != nil {
			t.Fatal(err)
		}
	}

	allow := true
	n, err := s.BulkSetReleaseFlags(ctx, ids, nil, &allow)
	if err != nil || n != 3 {
		t.Fatalf("expected 3 rows updated, got n=%d err=%v", n, err)
	}
	flagged, err := s.Releases(ctx, domain.ReleaseFilter{AllowNonPreferredFilenames: &allow, Limit: 10})
	if err != nil || len(flagged) != 3 {
		t.Fatalf("expected all 3 releases to match the filter, got %+v err=%v", flagged, err)
	}

	stopMonitoring := false
	n, err = s.BulkSetReleaseFlags(ctx, ids[:2], &stopMonitoring, nil)
	if err != nil || n != 2 {
		t.Fatalf("expected 2 rows updated, got n=%d err=%v", n, err)
	}
	monitored2, err := s.Releases(ctx, domain.ReleaseFilter{MonitorDownload: true, Limit: 10})
	if err != nil || len(monitored2) != 1 || monitored2[0].ID != ids[2] {
		t.Fatalf("expected only the untouched release to still be monitored, got %+v err=%v", monitored2, err)
	}
	// The allow-non-preferred flag must be untouched by the monitor-only
	// bulk call above (nil for that field means "leave it alone").
	stillFlagged, err := s.Releases(ctx, domain.ReleaseFilter{AllowNonPreferredFilenames: &allow, Limit: 10})
	if err != nil || len(stillFlagged) != 3 {
		t.Fatalf("expected the allow-non-preferred flag to survive an unrelated bulk update, got %+v err=%v", stillFlagged, err)
	}
}

func TestNotificationDeduplication(t *testing.T) {
	ctx := context.Background()
	s, err := OpenSQLite(filepath.Join(t.TempDir(), "notify.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	site, _ := s.SaveSite(ctx, domain.Site{Title: "GIGA", Type: "Site", Name: "GIGA", Enabled: true})
	_, _ = s.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "TEST-2", Title: "Test"})
	rows, _ := s.Releases(ctx, domain.ReleaseFilter{Limit: 10})
	first, err := s.CreateNotification(ctx, rows[0].ID, "downloaded", "done")
	if err != nil || !first {
		t.Fatalf("first notification: created=%v err=%v", first, err)
	}
	second, err := s.CreateNotification(ctx, rows[0].ID, "downloaded", "again")
	if err != nil || second {
		t.Fatalf("duplicate notification: created=%v err=%v", second, err)
	}
}

func TestPipelineStepsAndEventRunsPersistTriggerState(t *testing.T) {
	ctx := context.Background()
	s, err := OpenSQLite(filepath.Join(t.TempDir(), "pipeline-events.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	site, _ := s.SaveSite(ctx, domain.Site{Title: "Pipeline", Type: "Site", Name: "JavLibrary", Enabled: true})
	_, _ = s.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "PIPE-1", Title: "Pipeline release", Source: "JavLibrary"})
	releases, _ := s.Releases(ctx, domain.ReleaseFilter{Limit: 10})
	download, err := s.SaveDownload(ctx, domain.Download{ReleaseID: releases[0].ID, Query: "PIPE-1", Status: "completed"})
	if err != nil {
		t.Fatal(err)
	}
	steps := []domain.PipelineStep{{Name: "Move", Type: "shell", Config: []byte(`{"command":"mv"}`), Enabled: true}}
	if err := s.SavePipelineSteps(ctx, steps); err != nil {
		t.Fatal(err)
	}
	saved, err := s.PipelineSteps(ctx)
	if err != nil || len(saved) != 1 || saved[0].Trigger != "download_completed" || !saved[0].Enabled {
		t.Fatalf("pipeline steps=%+v err=%v", saved, err)
	}
	steps = append(steps, domain.PipelineStep{Trigger: "download_completed_removed", Name: "Scan", Type: "stash_graphql", Config: []byte(`{"query":"mutation { scan }"}`), Enabled: true})
	if err := s.SavePipelineSteps(ctx, steps); err != nil {
		t.Fatal(err)
	}
	saved, err = s.PipelineSteps(ctx)
	if err != nil || len(saved) != 2 || saved[1].Trigger != "download_completed_removed" {
		t.Fatalf("multi-event pipeline steps=%+v err=%v", saved, err)
	}
	run := domain.PipelineRun{DownloadID: download.ID, Trigger: "download_completed", State: "completed", StartedAt: time.Now().UTC(), FinishedAt: time.Now().UTC()}
	if err := s.SavePipelineRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	stored, err := s.PipelineRun(ctx, download.ID, "download_completed")
	if err != nil || stored.State != "completed" || stored.Trigger != "download_completed" {
		t.Fatalf("pipeline run=%+v err=%v", stored, err)
	}
}

func TestPipelineStepsPersistPerStepTimeoutAndRejectNegative(t *testing.T) {
	ctx := context.Background()
	s, err := OpenSQLite(filepath.Join(t.TempDir(), "pipeline-step-timeout.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	steps := []domain.PipelineStep{
		{Name: "Slow step", Type: "shell", Config: []byte(`{"command":"mv"}`), Enabled: true, TimeoutSeconds: 90},
		{Name: "Default step", Type: "shell", Config: []byte(`{"command":"mv"}`), Enabled: true},
	}
	if err := s.SavePipelineSteps(ctx, steps); err != nil {
		t.Fatal(err)
	}
	saved, err := s.PipelineSteps(ctx)
	if err != nil || len(saved) != 2 {
		t.Fatalf("pipeline steps=%+v err=%v", saved, err)
	}
	if saved[0].TimeoutSeconds != 90 {
		t.Fatalf("first step TimeoutSeconds=%d, want 90 to have persisted", saved[0].TimeoutSeconds)
	}
	if saved[1].TimeoutSeconds != 0 {
		t.Fatalf("second step TimeoutSeconds=%d, want 0 (use the settings-wide default)", saved[1].TimeoutSeconds)
	}
	if err := s.SavePipelineSteps(ctx, []domain.PipelineStep{{Name: "Bad", Type: "shell", Config: []byte(`{}`), Enabled: true, TimeoutSeconds: -1}}); err == nil {
		t.Fatal("expected an error saving a negative pipeline step timeout")
	}
}

func TestSiteScrapeSummaryPersistsAcrossSiteEdits(t *testing.T) {
	ctx := context.Background()
	s, err := OpenSQLite(filepath.Join(t.TempDir(), "site-scrape-summary.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	site, err := s.SaveSite(ctx, domain.Site{Title: "Neo Akari", Type: "Actress", Name: "JavLibrary", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	finished := time.Date(2026, time.August, 20, 22, 45, 0, 0, time.UTC)
	if err := s.RecordSiteScrape(ctx, site.ID, finished, 5, 3, 17, "completed"); err != nil {
		t.Fatal(err)
	}
	site.Notify = true
	if _, err := s.SaveSite(ctx, site); err != nil {
		t.Fatal(err)
	}
	sites, err := s.Sites(ctx)
	if err != nil || len(sites) != 1 {
		t.Fatalf("sites=%+v err=%v", sites, err)
	}
	got := sites[0]
	if !got.LastScrapedAt.Equal(finished) || got.LastScrapePages != 5 || got.LastScrapeAdded != 3 || got.LastScrapeUpdated != 17 || got.LastScrapeState != "completed" {
		t.Fatalf("scrape summary=%+v", got)
	}
}

// TestOpenSQLiteUpgradesDatabaseContainingScheduledDownloadData covers Phase 1
// of TODO.md: a database created before the retired Scheduled Download
// feature was removed must upgrade gracefully. It hand-builds the pre-upgrade
// downloads table shape (including the now-removed scheduled_for column and
// an older subset of columns later added by ALTER TABLE), seeds it with a
// download row left in the now-impossible "scheduled" status, then opens it
// through the normal store package to exercise the real migration path.
func TestOpenSQLiteUpgradesDatabaseContainingScheduledDownloadData(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy.db")
	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.Exec(`CREATE TABLE downloads (id INTEGER PRIMARY KEY, release_id INTEGER, provider TEXT NOT NULL DEFAULT '', source_type TEXT NOT NULL DEFAULT '', source_reference TEXT NOT NULL DEFAULT '', query TEXT NOT NULL DEFAULT '', torrent_hash TEXT NOT NULL DEFAULT '', name TEXT NOT NULL DEFAULT '', files TEXT NOT NULL DEFAULT '[]', status TEXT NOT NULL, match_reason TEXT NOT NULL DEFAULT '', qb_response TEXT NOT NULL DEFAULT '', post_status TEXT NOT NULL DEFAULT '', error TEXT NOT NULL DEFAULT '', scheduled_for TEXT NOT NULL DEFAULT '', seed_ratio REAL NOT NULL DEFAULT 0, added_at DATETIME NOT NULL, updated_at DATETIME NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if _, err := legacy.Exec(`INSERT INTO downloads(release_id,query,status,scheduled_for,added_at,updated_at) VALUES(NULL,'PRED-777','scheduled','2020-01-01',?,?)`, now, now); err != nil {
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}

	s, err := OpenSQLite(path)
	if err != nil {
		t.Fatalf("upgrade failed to open: %v", err)
	}
	defer s.Close()

	var scheduledForStillPresent int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('downloads') WHERE name='scheduled_for'`).Scan(&scheduledForStillPresent); err != nil {
		t.Fatal(err)
	}
	if scheduledForStillPresent != 0 {
		t.Fatal("scheduled_for column was not dropped during upgrade")
	}

	rows, err := s.Downloads(ctx, "")
	if err != nil {
		t.Fatalf("querying downloads after upgrade: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected the legacy download row to survive the upgrade, got %+v", rows)
	}
	if rows[0].Status != "cancelled" {
		t.Fatalf("legacy scheduled download was not reclassified as cancelled: %+v", rows[0])
	}
}

// TestJavLibraryURLsAreNormalizedToHTTPSOnStartup covers TODO-2.0's
// https-enforcement fix: JavLibrary now reliably 403s plain-http direct
// requests and can trip FlareSolverr into a mid-navigation http->https
// redirect race, so any URL stored before scraper.normalizeJavLibraryURL
// existed (either variant - with or without the "www." host) must be
// rewritten in place on every startup, in both sites.url and
// releases.product_url. A non-JavLibrary http:// URL must be left alone.
func TestJavLibraryURLsAreNormalizedToHTTPSOnStartup(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "javlibrary-https.db")
	s, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if _, err := s.db.ExecContext(ctx, `INSERT INTO sites(id,title,type,name,url,created_at,updated_at) VALUES
		(1,'Legacy www','Site','Legacy www','http://www.javlibrary.com/en/vl_maker.php?m=aqkq',?,?),
		(2,'Legacy bare','Site','Legacy bare','http://javlibrary.com/en/vl_maker.php?m=aqkq',?,?),
		(3,'Other site','Site','Other site','http://example.com/list',?,?)`, now, now, now, now, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO releases(id,site_id,video_id,source,title,product_url,added_at,updated_at) VALUES
		(1,1,'AAA-111','JavLibrary','A','http://www.javlibrary.com/en/javabc.html',?,?),
		(2,2,'BBB-222','JavLibrary','B','http://javlibrary.com/en/javdef.html',?,?)`, now, now, now, now); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	// Re-open: migrate() runs again on every open, which is what a restarted
	// production server actually does - the fix must not require a fresh
	// database to take effect.
	s, err = OpenSQLite(path)
	if err != nil {
		t.Fatalf("re-opening after seeding legacy http URLs: %v", err)
	}
	defer s.Close()

	sites, err := s.Sites(ctx)
	if err != nil {
		t.Fatal(err)
	}
	urls := map[int64]string{}
	for _, site := range sites {
		urls[site.ID] = site.URL
	}
	if urls[1] != "https://www.javlibrary.com/en/vl_maker.php?m=aqkq" {
		t.Fatalf("site 1 url=%q, want normalized to https://www.javlibrary.com", urls[1])
	}
	if urls[2] != "https://www.javlibrary.com/en/vl_maker.php?m=aqkq" {
		t.Fatalf("site 2 url=%q, want normalized to https://www.javlibrary.com", urls[2])
	}
	if urls[3] != "http://example.com/list" {
		t.Fatalf("site 3 (non-JavLibrary) url=%q, must be left untouched", urls[3])
	}

	var productURL1, productURL2 string
	if err := s.db.QueryRowContext(ctx, `SELECT product_url FROM releases WHERE id=1`).Scan(&productURL1); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT product_url FROM releases WHERE id=2`).Scan(&productURL2); err != nil {
		t.Fatal(err)
	}
	if productURL1 != "https://www.javlibrary.com/en/javabc.html" {
		t.Fatalf("release 1 product_url=%q, want normalized to https://www.javlibrary.com", productURL1)
	}
	if productURL2 != "https://www.javlibrary.com/en/javdef.html" {
		t.Fatalf("release 2 product_url=%q, want normalized to https://www.javlibrary.com", productURL2)
	}
}

func TestJavLibraryURLsAreNormalizedWhenSaved(t *testing.T) {
	ctx := context.Background()
	s, err := OpenSQLite(filepath.Join(t.TempDir(), "javlibrary-save-https.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	site, err := s.SaveSite(ctx, domain.Site{Title: "HTTP input", Type: "Site", Name: "JavLibrary", URL: "http://www.javlibrary.com/en/vl_star.php?&mode=2&s=abc", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if site.URL != "https://www.javlibrary.com/en/vl_star.php?&mode=2&s=abc" {
		t.Fatalf("saved site URL=%q", site.URL)
	}
	if _, err := s.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "HTTPS-1", Title: "HTTPS", Source: "JavLibrary", ProductURL: "http://www.javlibrary.com/en/javme3rf2u.html"}); err != nil {
		t.Fatal(err)
	}
	releases, err := s.Releases(ctx, domain.ReleaseFilter{Search: "HTTPS-1", Limit: 1})
	if err != nil || len(releases) != 1 {
		t.Fatalf("release lookup: rows=%d err=%v", len(releases), err)
	}
	if releases[0].ProductURL != "https://www.javlibrary.com/en/javme3rf2u.html" {
		t.Fatalf("saved product URL=%q", releases[0].ProductURL)
	}
}

// TestReleasesCountMatchesTotalIgnoringLimit covers Phase 4A: ReleasesCount
// must report the full matching total regardless of Limit, so a paginated
// UI can show a true total alongside one page of Releases results.
func TestReleasesCountMatchesTotalIgnoringLimit(t *testing.T) {
	ctx := context.Background()
	s, err := OpenSQLite(filepath.Join(t.TempDir(), "count.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	site, err := s.SaveSite(ctx, domain.Site{Title: "GIGA", Type: "Site", Name: "GIGA", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, videoID := range []string{"AAA-1", "AAA-2", "AAA-3"} {
		if _, err := s.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: videoID, Title: videoID, Source: "GIGA"}); err != nil {
			t.Fatal(err)
		}
	}
	total, err := s.ReleasesCount(ctx, domain.ReleaseFilter{Search: "AAA"})
	if err != nil {
		t.Fatal(err)
	}
	if total != 3 {
		t.Fatalf("total=%d, want 3", total)
	}
	page, err := s.Releases(ctx, domain.ReleaseFilter{Search: "AAA", Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 2 {
		t.Fatalf("page length=%d, want 2 (ReleasesCount must not be limited by Limit)", len(page))
	}
	if narrowed, err := s.ReleasesCount(ctx, domain.ReleaseFilter{Search: "AAA-1"}); err != nil || narrowed != 1 {
		t.Fatalf("narrowed total=%d err=%v, want 1", narrowed, err)
	}
}

// TestDownloadActivityPaginatesFiltersAndCounts covers Phase 4B: the
// Download Activity table's status/search filters, pagination, and total
// count, kept in sync with Downloads' unfiltered/unpaginated results.
func TestDownloadActivityPaginatesFiltersAndCounts(t *testing.T) {
	ctx := context.Background()
	s, err := OpenSQLite(filepath.Join(t.TempDir(), "activity.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	site, err := s.SaveSite(ctx, domain.Site{Title: "GIGA", Type: "Site", Name: "GIGA", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "ACT-1", Title: "ACT-1", Source: "GIGA"}); err != nil {
		t.Fatal(err)
	}
	all, err := s.Releases(ctx, domain.ReleaseFilter{Search: "ACT-1"})
	if err != nil || len(all) != 1 {
		t.Fatalf("seed release lookup: items=%d err=%v", len(all), err)
	}
	releaseID := all[0].ID
	for _, x := range []domain.Download{
		{ReleaseID: releaseID, Query: "QUERY-1", Provider: "Sukebei", Status: "downloading"},
		{ReleaseID: releaseID, Query: "QUERY-2", Provider: "Sukebei", Status: "downloading"},
		{ReleaseID: releaseID, Query: "QUERY-3", Provider: "Nyaa", Status: "completed"},
	} {
		if _, err := s.SaveDownload(ctx, x); err != nil {
			t.Fatal(err)
		}
	}
	items, total, err := s.DownloadActivity(ctx, domain.DownloadFilter{Status: "downloading", Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 {
		t.Fatalf("total=%d, want 2 (Limit must not affect the count)", total)
	}
	if len(items) != 1 {
		t.Fatalf("page length=%d, want 1", len(items))
	}
	items, total, err = s.DownloadActivity(ctx, domain.DownloadFilter{Search: "QUERY-2"})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(items) != 1 || items[0].Query != "QUERY-2" {
		t.Fatalf("search filter items=%+v total=%d, want exactly QUERY-2", items, total)
	}
	items, total, err = s.DownloadActivity(ctx, domain.DownloadFilter{Source: "Nyaa"})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(items) != 1 || items[0].Query != "QUERY-3" {
		t.Fatalf("source filter items=%+v total=%d, want exactly QUERY-3", items, total)
	}
}

// TestDownloadFilenamePatternExcludedRoundTripsAndFilters covers TODO-2.0
// Task A's structured filename-pattern-exclusion flag: SaveDownload must
// persist it (both on insert and on a later update, since qBittorrent
// pipeline events re-save the same row), Downloads/DownloadActivity must
// read it back, and DownloadFilter.FilenamePatternExcluded must restrict
// DownloadActivity to only the rows that have it set - mirroring the other
// one-way boolean toggles on DownloadFilter (false means "don't filter on
// this", not "must be false").
func TestDownloadFilenamePatternExcludedRoundTripsAndFilters(t *testing.T) {
	ctx := context.Background()
	s, err := OpenSQLite(filepath.Join(t.TempDir(), "filename-pattern-excluded.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	site, err := s.SaveSite(ctx, domain.Site{Title: "GIGA", Type: "Site", Name: "GIGA", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "FPE-1", Title: "FPE-1", Source: "GIGA"}); err != nil {
		t.Fatal(err)
	}
	releases, err := s.Releases(ctx, domain.ReleaseFilter{Search: "FPE-1"})
	if err != nil || len(releases) != 1 {
		t.Fatalf("seed release lookup: items=%d err=%v", len(releases), err)
	}
	releaseID := releases[0].ID

	excluded, err := s.SaveDownload(ctx, domain.Download{ReleaseID: releaseID, Query: "FPE-EXCLUDED", Provider: "Sukebei", Status: "downloading", FilenamePatternExcluded: true})
	if err != nil {
		t.Fatal(err)
	}
	if !excluded.FilenamePatternExcluded {
		t.Fatalf("SaveDownload's own return value did not round-trip FilenamePatternExcluded: %+v", excluded)
	}
	normal, err := s.SaveDownload(ctx, domain.Download{ReleaseID: releaseID, Query: "FPE-NORMAL", Provider: "Sukebei", Status: "downloading"})
	if err != nil {
		t.Fatal(err)
	}

	downloads, err := s.Downloads(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	var sawExcluded, sawNormalUnset bool
	for _, d := range downloads {
		if d.ID == excluded.ID && d.FilenamePatternExcluded {
			sawExcluded = true
		}
		if d.ID == normal.ID && !d.FilenamePatternExcluded {
			sawNormalUnset = true
		}
	}
	if !sawExcluded {
		t.Fatalf("Downloads() did not read back FilenamePatternExcluded=true for the excluded row: %+v", downloads)
	}
	if !sawNormalUnset {
		t.Fatalf("Downloads() unexpectedly set FilenamePatternExcluded on the normal row: %+v", downloads)
	}

	// An UPDATE (SaveDownload called again with the same ID, as the pipeline
	// does on every status transition) must also persist the flag.
	excluded.Status = "completed"
	if _, err := s.SaveDownload(ctx, excluded); err != nil {
		t.Fatal(err)
	}
	afterUpdate, err := s.Downloads(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	var stillExcluded bool
	for _, d := range afterUpdate {
		if d.ID == excluded.ID && d.FilenamePatternExcluded && d.Status == "completed" {
			stillExcluded = true
		}
	}
	if !stillExcluded {
		t.Fatalf("FilenamePatternExcluded did not survive an UPDATE via SaveDownload: %+v", afterUpdate)
	}

	items, total, err := s.DownloadActivity(ctx, domain.DownloadFilter{FilenamePatternExcluded: true})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(items) != 1 || items[0].ID != excluded.ID {
		t.Fatalf("FilenamePatternExcluded filter items=%+v total=%d, want exactly the excluded row", items, total)
	}

	items, total, err = s.DownloadActivity(ctx, domain.DownloadFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 || len(items) != 2 {
		t.Fatalf("FilenamePatternExcluded=false must not filter at all, got items=%+v total=%d", items, total)
	}
}

func TestDownloadActivitySeenCompleteStalledFilterAndSwarmSort(t *testing.T) {
	ctx := context.Background()
	s, err := OpenSQLite(filepath.Join(t.TempDir(), "download-stalled.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	site, _ := s.SaveSite(ctx, domain.Site{Title: "Test", Type: "Site", Name: "JavLibrary", Enabled: true})
	_, _ = s.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "STALL-1", Title: "STALL-1"})
	releases, _ := s.Releases(ctx, domain.ReleaseFilter{Search: "STALL-1", Limit: 10})
	releaseID := releases[0].ID
	now := time.Now()
	for _, row := range []domain.Download{
		{ReleaseID: releaseID, Query: "NEVER", Status: "downloading", Seeds: 0, SeenComplete: 0},
		{ReleaseID: releaseID, Query: "OLD", Status: "downloading", Seeds: 0, SeenComplete: now.Add(-10 * 24 * time.Hour).Unix()},
		{ReleaseID: releaseID, Query: "RECENT", Status: "downloading", Seeds: 0, SeenComplete: now.Add(-2 * 24 * time.Hour).Unix()},
		{ReleaseID: releaseID, Query: "NO-COMPLETE", Status: "downloading", Seeds: 8, Peers: 2, SeenComplete: 0},
		{ReleaseID: releaseID, Query: "SEEDED", Status: "downloading", Seeds: 8, Peers: 3, SeenComplete: now.Add(-20 * 24 * time.Hour).Unix()},
	} {
		if _, err := s.SaveDownload(ctx, row); err != nil {
			t.Fatal(err)
		}
	}
	stalled, total, err := s.DownloadActivity(ctx, domain.DownloadFilter{Status: "downloading", Stalled: true, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if total != 4 || len(stalled) != 4 {
		t.Fatalf("stalled rows=%+v total=%d, want every zero-seed or never-complete download", stalled, total)
	}
	seen := map[string]bool{}
	for _, row := range stalled {
		seen[row.Query] = true
	}
	if !seen["NEVER"] || !seen["OLD"] || !seen["RECENT"] || !seen["NO-COMPLETE"] {
		t.Fatalf("stalled rows=%+v, want NEVER, OLD, RECENT, and NO-COMPLETE", stalled)
	}
	never, total, err := s.DownloadActivity(ctx, domain.DownloadFilter{SeenComplete: "never", Limit: 10})
	if err != nil || total != 2 || len(never) != 2 {
		t.Fatalf("never-seen-complete rows=%+v total=%d err=%v", never, total, err)
	}
	cutoff := now.Add(-7 * 24 * time.Hour).Unix()
	older, total, err := s.DownloadActivity(ctx, domain.DownloadFilter{SeenComplete: "before", SeenCompleteDate: cutoff, Limit: 10})
	if err != nil || total != 2 || len(older) != 2 {
		t.Fatalf("before-date rows=%+v total=%d err=%v, want OLD and SEEDED", older, total, err)
	}
	newer, total, err := s.DownloadActivity(ctx, domain.DownloadFilter{SeenComplete: "after", SeenCompleteDate: cutoff, Limit: 10})
	if err != nil || total != 1 || len(newer) != 1 || newer[0].Query != "RECENT" {
		t.Fatalf("after-date rows=%+v total=%d err=%v, want RECENT", newer, total, err)
	}
	sorted, _, err := s.DownloadActivity(ctx, domain.DownloadFilter{Status: "downloading", Sort: "seeds", Direction: "desc", Limit: 10})
	if err != nil || len(sorted) != 5 || sorted[0].Query != "SEEDED" || sorted[0].SeenComplete == 0 {
		t.Fatalf("seed sort / seen-complete round trip failed: rows=%+v err=%v", sorted, err)
	}
}

// TestReleasesSortByAddedBreaksTiesByInsertionOrder is a regression test
// for a bug report that "sort by Date added" in the Release library
// appeared not to actually sort by when a release was scraped/inserted.
// The root cause: Releases()'s ORDER BY always appended ",r.added_at DESC"
// as a tiebreaker, which is a no-op when the primary sort column already
// IS r.added_at (sort=added) - ties (multiple releases inserted with the
// same added_at, which happens easily during a fast bulk scrape/RSS
// import) were then left in SQLite's implementation-defined order rather
// than true insertion order. The fix ties on r.id instead, which is a
// SQLite rowid alias that strictly increases with insertion order
// regardless of timestamp precision or ties.
func TestReleasesSortByAddedBreaksTiesByInsertionOrder(t *testing.T) {
	ctx := context.Background()
	s, err := OpenSQLite(filepath.Join(t.TempDir(), "sort-added.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	site, err := s.SaveSite(ctx, domain.Site{Title: "GIGA", Type: "Site", Name: "GIGA", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	// Insert five releases in a known order. Each UpsertRelease call gets
	// its own time.Now() for added_at, but immediately below we collapse
	// them all to one identical timestamp to simulate the tie a fast bulk
	// scrape can produce - the scenario the original bug report actually
	// hits in practice.
	var ids []int64
	for i := range 5 {
		videoID := fmt.Sprintf("TIE-%d", i)
		if _, err := s.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: videoID, Title: videoID, Source: "GIGA"}); err != nil {
			t.Fatal(err)
		}
		var id int64
		if err := s.db.QueryRowContext(ctx, `SELECT id FROM releases WHERE video_id=?`, videoID).Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE releases SET added_at='2024-01-01 00:00:00' WHERE id IN (`+strings.TrimRight(strings.Repeat("?,", len(ids)), ",")+`)`, toAnySlice(ids)...); err != nil {
		t.Fatal(err)
	}

	desc, err := s.Releases(ctx, domain.ReleaseFilter{Search: "TIE-", Sort: "added", Direction: "desc"})
	if err != nil {
		t.Fatal(err)
	}
	if len(desc) != 5 {
		t.Fatalf("desc results=%d, want 5", len(desc))
	}
	for i, r := range desc {
		wantID := ids[len(ids)-1-i] // newest-inserted (highest id) first
		if r.ID != wantID {
			t.Fatalf("desc[%d].ID=%d, want %d (tie-broken by insertion order): got order %v, want reverse of %v", i, r.ID, wantID, idsOf(desc), ids)
		}
	}

	asc, err := s.Releases(ctx, domain.ReleaseFilter{Search: "TIE-", Sort: "added", Direction: "asc"})
	if err != nil {
		t.Fatal(err)
	}
	for i, r := range asc {
		if r.ID != ids[i] {
			t.Fatalf("asc[%d].ID=%d, want %d (tie-broken by insertion order): got order %v, want %v", i, r.ID, ids[i], idsOf(asc), ids)
		}
	}
}

// TestReleasesSortByStashCreatedAtUsesNewestFirstAndKeepsUnknownLast guards
// the Release Library's "Added Locally" sort. That value is StashApp's own
// scene created_at, equivalent to Stash's sortby=created_at. In particular,
// PostgreSQL's default DESC ordering places NULL first unless told otherwise,
// so an unsynchronized scene must not outrank a genuinely recent scene.
func TestReleasesSortByStashCreatedAtUsesNewestFirstAndKeepsUnknownLast(t *testing.T) {
	ctx := context.Background()
	s, err := OpenSQLite(filepath.Join(t.TempDir(), "sort-stash-created.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	site, err := s.SaveSite(ctx, domain.Site{Title: "Stash sort", Type: "Site", Name: "GIGA", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]int64{}
	for _, videoID := range []string{"LOCAL-OLD", "LOCAL-NEW", "LOCAL-UNKNOWN"} {
		if _, err := s.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: videoID, Title: videoID, Source: "GIGA"}); err != nil {
			t.Fatal(err)
		}
		var id int64
		if err := s.db.QueryRowContext(ctx, `SELECT id FROM releases WHERE video_id=?`, videoID).Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids[videoID] = id
		if err := s.SetStashState(ctx, id, true, "stash-"+videoID); err != nil {
			t.Fatal(err)
		}
	}
	oldTime := time.Date(2024, 3, 31, 12, 39, 0, 0, time.UTC)
	newTime := time.Date(2026, 8, 28, 9, 15, 0, 0, time.UTC)
	if err := s.SetStashCreatedAt(ctx, ids["LOCAL-OLD"], oldTime); err != nil {
		t.Fatal(err)
	}
	if err := s.SetStashCreatedAt(ctx, ids["LOCAL-NEW"], newTime); err != nil {
		t.Fatal(err)
	}

	desc, err := s.Releases(ctx, domain.ReleaseFilter{Status: "local", Sort: "local_added", Direction: "desc", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(videoIDsOf(desc), ","), "LOCAL-NEW,LOCAL-OLD,LOCAL-UNKNOWN"; got != want {
		t.Fatalf("descending Added Locally order=%s, want %s", got, want)
	}
	asc, err := s.Releases(ctx, domain.ReleaseFilter{Status: "local", Sort: "local_added", Direction: "asc", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(videoIDsOf(asc), ","), "LOCAL-OLD,LOCAL-NEW,LOCAL-UNKNOWN"; got != want {
		t.Fatalf("ascending Added Locally order=%s, want %s", got, want)
	}
}

func videoIDsOf(releases []domain.Release) []string {
	ids := make([]string, len(releases))
	for i, release := range releases {
		ids[i] = release.VideoID
	}
	return ids
}

func toAnySlice(ids []int64) []any {
	out := make([]any, len(ids))
	for i, id := range ids {
		out[i] = id
	}
	return out
}

func idsOf(releases []domain.Release) []int64 {
	out := make([]int64, len(releases))
	for i, r := range releases {
		out[i] = r.ID
	}
	return out
}

// TestUpsertReleaseKeepUpdatedAtPreservesTimestamp guards the multi-Byparr
// screenshot-backfill feature's core requirement: a backfill run confirming
// or repairing a release's screenshots must not bump updated_at, or it
// would pollute "sort by date updated" every time the maintenance job
// merely re-confirms old releases. UpsertReleaseKeepUpdatedAt must still
// apply the changed metadata itself - only the timestamp is suppressed.
func TestUpsertReleaseKeepUpdatedAtPreservesTimestamp(t *testing.T) {
	ctx := context.Background()
	s, err := OpenSQLite(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	site, err := s.SaveSite(ctx, domain.Site{Title: "JavLibrary", Type: "Site", Name: "JavLibrary", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if created, err := s.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "PRED-901", Title: "Original title", Source: "JavLibrary", Released: true}); err != nil || !created {
		t.Fatalf("initial upsert: created=%v err=%v", created, err)
	}
	before, err := s.Releases(ctx, domain.ReleaseFilter{Search: "PRED-901"})
	if err != nil || len(before) != 1 {
		t.Fatalf("initial lookup: items=%d err=%v", len(before), err)
	}
	initialUpdatedAt := before[0].UpdatedAt
	time.Sleep(10 * time.Millisecond)

	// A screenshots-only change is exactly the case that used to bump
	// updated_at via UpsertRelease's metadataChanged check - it must not,
	// through the KeepUpdatedAt variant.
	changed := domain.Release{SiteID: site.ID, VideoID: "PRED-901", Title: "Original title", Source: "JavLibrary", Released: true, Screenshots: []string{"https://example.test/new-shot.jpg"}}
	if created, err := s.UpsertReleaseKeepUpdatedAt(ctx, changed); err != nil || created {
		t.Fatalf("keep-updated-at upsert: created=%v err=%v", created, err)
	}
	after, err := s.Releases(ctx, domain.ReleaseFilter{Search: "PRED-901"})
	if err != nil || len(after) != 1 {
		t.Fatalf("post-upsert lookup: items=%d err=%v", len(after), err)
	}
	if !after[0].UpdatedAt.Equal(initialUpdatedAt) {
		t.Fatalf("updated_at changed: before=%v after=%v", initialUpdatedAt, after[0].UpdatedAt)
	}
	if len(after[0].Screenshots) != 1 || after[0].Screenshots[0] != "https://example.test/new-shot.jpg" {
		t.Fatalf("screenshots were not applied: %v", after[0].Screenshots)
	}
}

// TestUpsertReleaseDoesNotBumpUpdatedAtOnArtworkOnlyChange covers ordinary
// Full refresh upserts: cover and screenshot URL changes are persisted, but
// are cache maintenance and must not move the release metadata timestamp.
func TestUpsertReleaseDoesNotBumpUpdatedAtOnArtworkOnlyChange(t *testing.T) {
	ctx := context.Background()
	s, err := OpenSQLite(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	site, err := s.SaveSite(ctx, domain.Site{Title: "JavLibrary", Type: "Site", Name: "JavLibrary", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if created, err := s.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "PRED-902", Title: "Original title", Source: "JavLibrary", ImageURL: "https://example.test/old-cover.jpg", Released: true}); err != nil || !created {
		t.Fatalf("initial upsert: created=%v err=%v", created, err)
	}
	before, err := s.Releases(ctx, domain.ReleaseFilter{Search: "PRED-902"})
	if err != nil || len(before) != 1 {
		t.Fatalf("initial lookup: items=%d err=%v", len(before), err)
	}
	initialUpdatedAt := before[0].UpdatedAt
	time.Sleep(10 * time.Millisecond)

	changed := domain.Release{SiteID: site.ID, VideoID: "PRED-902", Title: "Original title", Source: "JavLibrary", ImageURL: "https://example.test/new-cover.jpg", Released: true, Screenshots: []string{"https://example.test/new-shot.jpg"}}
	if created, err := s.UpsertRelease(ctx, changed); err != nil || created {
		t.Fatalf("upsert: created=%v err=%v", created, err)
	}
	after, err := s.Releases(ctx, domain.ReleaseFilter{Search: "PRED-902"})
	if err != nil || len(after) != 1 {
		t.Fatalf("post-upsert lookup: items=%d err=%v", len(after), err)
	}
	if !after[0].UpdatedAt.Equal(initialUpdatedAt) {
		t.Fatalf("artwork-only change advanced updated_at: before=%v after=%v", initialUpdatedAt, after[0].UpdatedAt)
	}
	if after[0].ImageURL != changed.ImageURL || len(after[0].Screenshots) != 1 || after[0].Screenshots[0] != changed.Screenshots[0] {
		t.Fatalf("artwork URLs were not applied: image=%q screenshots=%v", after[0].ImageURL, after[0].Screenshots)
	}
}

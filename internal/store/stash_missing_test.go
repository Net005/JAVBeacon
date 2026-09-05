package store

import (
	"context"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/Net005/JAVBeacon/internal/domain"
)

func TestStashMissingScenesUpsertLinkAndFilter(t *testing.T) {
	ctx := context.Background()
	s, err := OpenSQLite(filepath.Join(t.TempDir(), "stash-missing.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	site, err := s.SaveSite(ctx, domain.Site{Title: "Recovery", Type: "Site", Name: "JavLibrary", Enabled: false})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "ABC-123", Title: "Matched release", Source: "JavLibrary"}); err != nil {
		t.Fatal(err)
	}
	releaseRows, err := s.Releases(ctx, domain.ReleaseFilter{Search: "ABC-123", Limit: 1})
	if err != nil || len(releaseRows) != 1 {
		t.Fatalf("release lookup: rows=%d err=%v", len(releaseRows), err)
	}
	release := releaseRows[0]

	unmatchedID, err := s.UpsertStashMissingScene(ctx, domain.StashMissingScene{
		StashSceneID: "1", Title: "Unmatched scene", Code: "XYZ-999", Path: "/data/XYZ-999.mp4",
		OCounter: 3, PlayCount: 5, LastOCountAt: "2024-05-09T12:00:00Z", Studio: "Some Studio", Tags: []string{"Solowork"},
		URLs: []string{"https://www.javlibrary.com/en/?v=abc123"}, JavLibraryURL: "https://www.javlibrary.com/en/?v=abc123",
	})
	if err != nil {
		t.Fatal(err)
	}
	matchedID, err := s.UpsertStashMissingScene(ctx, domain.StashMissingScene{
		StashSceneID: "2", Title: "Matched scene", Code: "ABC-123", Path: "/data/ABC-123.mp4",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.LinkStashMissingRelease(ctx, matchedID, release.ID); err != nil {
		t.Fatal(err)
	}

	all, err := s.StashMissingScenes(ctx, domain.StashMissingFilter{Limit: 10})
	if err != nil || len(all) != 2 {
		t.Fatalf("all=%+v err=%v", all, err)
	}

	// Status filter: "missing" only matches the unlinked scene.
	missing, err := s.StashMissingScenes(ctx, domain.StashMissingFilter{Status: "missing", Limit: 10})
	if err != nil || len(missing) != 1 || missing[0].ID != unmatchedID {
		t.Fatalf("missing filter returned %+v: %v", missing, err)
	}
	if missing[0].EffectiveStatus != "missing" {
		t.Fatalf("expected effective_status=missing, got %q", missing[0].EffectiveStatus)
	}

	// The linked scene has no monitor_download/downloads activity yet, so
	// its effective status should be "retrieved".
	linked, err := s.StashMissingScenes(ctx, domain.StashMissingFilter{Status: "retrieved", Limit: 10})
	if err != nil || len(linked) != 1 || linked[0].ID != matchedID {
		t.Fatalf("retrieved filter returned %+v: %v", linked, err)
	}
	if linked[0].ReleaseVideoID != "ABC-123" || linked[0].ReleaseID != release.ID {
		t.Fatalf("linked scene missing release join data: %+v", linked[0])
	}
	scraping, err := s.StashMissingScenes(ctx, domain.StashMissingFilter{Status: "scraping", Limit: 10})
	if err != nil || len(scraping) != 1 || scraping[0].ID != matchedID {
		t.Fatalf("scraping activity group returned %+v: %v", scraping, err)
	}

	// has_db_entry condition.
	hasEntry, err := s.StashMissingScenes(ctx, domain.StashMissingFilter{
		SearchExpression: `{"logic":"and","conditions":[{"field":"has_db_entry","value":"true"}]}`, Limit: 10,
	})
	if err != nil || len(hasEntry) != 1 || hasEntry[0].ID != matchedID {
		t.Fatalf("has_db_entry filter returned %+v: %v", hasEntry, err)
	}

	// has_javlibrary_url condition.
	hasURL, err := s.StashMissingScenes(ctx, domain.StashMissingFilter{
		SearchExpression: `{"logic":"and","conditions":[{"field":"has_javlibrary_url","value":"true"}]}`, Limit: 10,
	})
	if err != nil || len(hasURL) != 1 || hasURL[0].ID != unmatchedID {
		t.Fatalf("has_javlibrary_url filter returned %+v: %v", hasURL, err)
	}

	// Path search, substring mode (the default when "wildcard" is not set -
	// matching the existing release Conditions builder's convention that
	// wildcard mode is glob-anchored, e.g. "XYZ*" means "starts with XYZ",
	// while the default is a plain substring match).
	byPath, err := s.StashMissingScenes(ctx, domain.StashMissingFilter{
		SearchExpression: `{"logic":"and","conditions":[{"field":"path","value":"XYZ-999"}]}`, Limit: 10,
	})
	if err != nil || len(byPath) != 1 || byPath[0].ID != unmatchedID {
		t.Fatalf("path wildcard filter returned %+v: %v", byPath, err)
	}

	// Numeric o_count condition with an explicit operator.
	byOCount, err := s.StashMissingScenes(ctx, domain.StashMissingFilter{
		SearchExpression: `{"logic":"and","conditions":[{"field":"o_count","value":"2","op":"gte"}]}`, Limit: 10,
	})
	if err != nil || len(byOCount) != 1 || byOCount[0].ID != unmatchedID {
		t.Fatalf("o_count filter returned %+v: %v", byOCount, err)
	}

	byLastOCount, err := s.StashMissingScenes(ctx, domain.StashMissingFilter{
		SearchExpression: `{"logic":"and","conditions":[{"field":"last_o_count","value":"2024-05-01","op":"after"}]}`, Limit: 10,
	})
	if err != nil || len(byLastOCount) != 1 || byLastOCount[0].ID != unmatchedID || byLastOCount[0].LastOCountAt != "2024-05-09T12:00:00Z" {
		t.Fatalf("last_o_count filter returned %+v: %v", byLastOCount, err)
	}

	// AND/OR combination: studio OR tag, neither of which matches the
	// linked scene, so only the unmatched one should come back.
	orFilter, err := s.StashMissingScenes(ctx, domain.StashMissingFilter{
		SearchExpression: `{"logic":"or","conditions":[{"field":"studio","value":"Some Studio"},{"field":"tag","value":"Solowork"}]}`, Limit: 10,
	})
	if err != nil || len(orFilter) != 1 || orFilter[0].ID != unmatchedID {
		t.Fatalf("or filter returned %+v: %v", orFilter, err)
	}

	count, err := s.StashMissingScenesCount(ctx, domain.StashMissingFilter{})
	if err != nil || count != 2 {
		t.Fatalf("count=%d err=%v", count, err)
	}

	one, err := s.StashMissingScene(ctx, matchedID)
	if err != nil || one.ID != matchedID || one.ReleaseID != release.ID {
		t.Fatalf("StashMissingScene(%d)=%+v err=%v", matchedID, one, err)
	}
}

func TestStashMissingScenesEffectiveStatusReflectsLinkedReleaseDownloads(t *testing.T) {
	ctx := context.Background()
	s, err := OpenSQLite(filepath.Join(t.TempDir(), "stash-missing-status.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	site, _ := s.SaveSite(ctx, domain.Site{Title: "Recovery", Type: "Site", Name: "JavLibrary", Enabled: false})
	if _, err := s.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "DEF-456", Title: "Downloading release", Source: "JavLibrary"}); err != nil {
		t.Fatal(err)
	}
	releaseRows, err := s.Releases(ctx, domain.ReleaseFilter{Search: "DEF-456", Limit: 1})
	if err != nil || len(releaseRows) != 1 {
		t.Fatalf("release lookup: rows=%d err=%v", len(releaseRows), err)
	}
	release := releaseRows[0]
	id, err := s.UpsertStashMissingScene(ctx, domain.StashMissingScene{StashSceneID: "3", Title: "Scene", Code: "DEF-456"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.LinkStashMissingRelease(ctx, id, release.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveDownload(ctx, domain.Download{ReleaseID: release.ID, Provider: "Sukebei", Status: "downloading"}); err != nil {
		t.Fatal(err)
	}
	row, err := s.StashMissingScene(ctx, id)
	if err != nil || row.EffectiveStatus != "downloading" {
		t.Fatalf("expected downloading, got %+v (err=%v)", row, err)
	}

	rows, err := s.StashMissingScenes(ctx, domain.StashMissingFilter{Status: "downloading", Limit: 10})
	if err != nil || len(rows) != 1 || rows[0].ID != id {
		t.Fatalf("downloading status filter returned %+v: %v", rows, err)
	}
	rows, err = s.StashMissingScenes(ctx, domain.StashMissingFilter{Status: "download", Limit: 10})
	if err != nil || len(rows) != 1 || rows[0].ID != id {
		t.Fatalf("download activity group returned %+v: %v", rows, err)
	}
}

// TestStashMissingSceneEffectiveStatusIgnoresStaleIsLocalFlag is a
// regression test for a reported TODO-2.0 Task A bug: a scene the scan just
// confirmed missing was showing effective_status "downloaded" instead of a
// status implying it's still missing/unretrieved. The cause was
// stashMissingEffectiveStatusExpr trusting the release's r.is_local flag,
// which the separate Stash local-sync feature sets purely from whether a
// matching StashApp *scene* still exists in Stash's database - it does not
// re-check the file on disk, so it can still read true for a release whose
// file was lost well after that flag was last set. Every row in
// stash_missing_scenes exists precisely because this app's own scan just
// failed to find the file, so is_local must not override that.
func TestStashMissingSceneEffectiveStatusIgnoresStaleIsLocalFlag(t *testing.T) {
	ctx := context.Background()
	s, err := OpenSQLite(filepath.Join(t.TempDir(), "stash-missing-stale-local.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	site, _ := s.SaveSite(ctx, domain.Site{Title: "Recovery", Type: "Site", Name: "JavLibrary", Enabled: false})
	if _, err := s.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "GHI-789", Title: "Stale local release", Source: "JavLibrary"}); err != nil {
		t.Fatal(err)
	}
	releaseRows, err := s.Releases(ctx, domain.ReleaseFilter{Search: "GHI-789", Limit: 1})
	if err != nil || len(releaseRows) != 1 {
		t.Fatalf("release lookup: rows=%d err=%v", len(releaseRows), err)
	}
	release := releaseRows[0]
	// Simulate a Stash local-sync pass having set is_local=true at some
	// earlier point, before the file went missing - no download activity
	// (downloading/completed/failed) exists for this release at all.
	if err := s.SetStashState(ctx, release.ID, true, "stash-scene-id"); err != nil {
		t.Fatal(err)
	}

	id, err := s.UpsertStashMissingScene(ctx, domain.StashMissingScene{StashSceneID: "4", Title: "Scene", Code: "GHI-789"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.LinkStashMissingRelease(ctx, id, release.ID); err != nil {
		t.Fatal(err)
	}

	row, err := s.StashMissingScene(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if row.EffectiveStatus == "downloaded" {
		t.Fatalf("expected effective_status to NOT be \"downloaded\" for a scene this scan just found missing (even though its release has a stale is_local=true), got %+v", row)
	}
	if row.EffectiveStatus != "retrieved" {
		t.Fatalf("expected effective_status=retrieved (linked, not monitored, no download activity), got %q", row.EffectiveStatus)
	}
}

// TestStashMissingScenesLargeLimitReturnsEveryRow covers TODO-2.0 Task A's
// "All (single page)" page-size option: the UI requests a very large limit
// (matching stashMissingMaxLimit) to fit an entire missing-file backlog on
// one page, so a limit well above the old 500-row cap must not be
// truncated back down to the small 100-row default.
func TestStashMissingScenesLargeLimitReturnsEveryRow(t *testing.T) {
	ctx := context.Background()
	s, err := OpenSQLite(filepath.Join(t.TempDir(), "stash-missing-all-page.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	const total = 600
	for i := 0; i < total; i++ {
		if _, err := s.UpsertStashMissingScene(ctx, domain.StashMissingScene{StashSceneID: strconv.Itoa(i), Title: "Scene"}); err != nil {
			t.Fatal(err)
		}
	}

	rows, err := s.StashMissingScenes(ctx, domain.StashMissingFilter{Limit: stashMissingMaxLimit})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != total {
		t.Fatalf("expected all %d rows with an \"All\"-sized limit, got %d", total, len(rows))
	}
}

// TestStashMissingConditionsSearchIsCaseInsensitiveForPathStudioAndTag
// guards the TODO-2.0 Task A case-insensitivity audit fix to
// stashMissingFilterWhere's Conditions (SearchExpression) builder: the
// path/studio/tag branches used to emit a bare "column LIKE ?" (and the
// function took no Dialect parameter at all) instead of routing through
// Dialect.CaseInsensitiveLike. As with the analogous release-side test,
// this SQLite-backed test won't itself fail if the bug regresses on SQLite
// specifically (its default LIKE collation is already case-insensitive) -
// it documents and locks in the expected behavior here, matched by the
// decisive PostgresDialect{}-based assertion in
// TestStashMissingFilterWhereUsesCaseInsensitiveLikeOnPostgres.
func TestStashMissingConditionsSearchIsCaseInsensitiveForPathStudioAndTag(t *testing.T) {
	ctx := context.Background()
	s, err := OpenSQLite(filepath.Join(t.TempDir(), "stash-missing-conditions-case-insensitive.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	id, err := s.UpsertStashMissingScene(ctx, domain.StashMissingScene{
		StashSceneID: "1", Title: "Scene", Code: "COND-1",
		Path: "/data/Mixed/Case/Path/COND-1.mp4", Studio: "Bright Studio", Tags: []string{"Solowork"},
	})
	if err != nil {
		t.Fatal(err)
	}

	for name, expr := range map[string]string{
		"path mixed":   `{"logic":"and","conditions":[{"field":"path","value":"MIXED/case"}]}`,
		"studio mixed": `{"logic":"and","conditions":[{"field":"studio","value":"bright STUDIO"}]}`,
		"tag mixed":    `{"logic":"and","conditions":[{"field":"tag","value":"SOLOWORK"}]}`,
	} {
		rows, err := s.StashMissingScenes(ctx, domain.StashMissingFilter{SearchExpression: expr, Limit: 10})
		if err != nil || len(rows) != 1 || rows[0].ID != id {
			t.Fatalf("%s: expression %q returned %+v: %v", name, expr, rows, err)
		}
	}
}

// TestStashMissingConditionsSupportsAndOrGroups is the analogous regression
// test to TestReleaseConditionsSupportsAndOrGroups for
// stashMissingFilterWhere's TODO-2.0 Task A "AND/OR condition groups"
// feature, proving both the new grouped shape and continued backward
// compatibility with the legacy flat shape.
func TestStashMissingConditionsSupportsAndOrGroups(t *testing.T) {
	ctx := context.Background()
	s, err := OpenSQLite(filepath.Join(t.TempDir(), "stash-missing-conditions-groups.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	seed := func(stashID, code, studio string, tags []string) int64 {
		id, err := s.UpsertStashMissingScene(ctx, domain.StashMissingScene{
			StashSceneID: stashID, Title: "Scene " + code, Code: code, Studio: studio, Tags: tags,
		})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	seed("1", "GRP-1", "Studio A", []string{"Tag X"}) // both groups true -> matches
	seed("2", "GRP-2", "Studio A", []string{"Tag Z"}) // group 1 true, group 2 false -> no match
	seed("3", "GRP-3", "Studio C", []string{"Tag X"}) // group 1 false, group 2 true -> no match
	seed("4", "GRP-4", "Studio B", []string{"Tag Y"}) // both groups true -> matches

	grouped := `{"logic":"and","groups":[` +
		`{"logic":"or","conditions":[{"field":"studio","value":"Studio A"},{"field":"studio","value":"Studio B"}]},` +
		`{"logic":"or","conditions":[{"field":"tag","value":"Tag X"},{"field":"tag","value":"Tag Y"}]}` +
		`]}`
	rows, err := s.StashMissingScenes(ctx, domain.StashMissingFilter{SearchExpression: grouped, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, r := range rows {
		got[r.Code] = true
	}
	if !got["GRP-1"] || !got["GRP-4"] || got["GRP-2"] || got["GRP-3"] || len(rows) != 2 {
		t.Fatalf("grouped AND-of-ORs expression matched %+v, want exactly GRP-1 and GRP-4", rows)
	}

	orOfAnds := `{"logic":"or","groups":[` +
		`{"logic":"and","conditions":[{"field":"studio","value":"Studio A"},{"field":"tag","value":"Tag X"}]},` +
		`{"logic":"and","conditions":[{"field":"studio","value":"Studio B"},{"field":"tag","value":"Tag Y"}]}` +
		`]}`
	rows, err = s.StashMissingScenes(ctx, domain.StashMissingFilter{SearchExpression: orOfAnds, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	got = map[string]bool{}
	for _, r := range rows {
		got[r.Code] = true
	}
	if !got["GRP-1"] || !got["GRP-4"] || len(rows) != 2 {
		t.Fatalf("OR-of-ANDs expression matched %+v, want exactly GRP-1 and GRP-4", rows)
	}

	legacyFlat := `{"logic":"or","conditions":[{"field":"studio","value":"Studio A"},{"field":"studio","value":"Studio B"}]}`
	rows, err = s.StashMissingScenes(ctx, domain.StashMissingFilter{SearchExpression: legacyFlat, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	got = map[string]bool{}
	for _, r := range rows {
		got[r.Code] = true
	}
	if !got["GRP-1"] || !got["GRP-2"] || !got["GRP-4"] || got["GRP-3"] || len(rows) != 3 {
		t.Fatalf("legacy flat OR expression matched %+v, want GRP-1, GRP-2, GRP-4", rows)
	}
}

func TestPruneStashMissingScenesRemovesUntouchedRows(t *testing.T) {
	ctx := context.Background()
	s, err := OpenSQLite(filepath.Join(t.TempDir(), "stash-missing-prune.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if _, err := s.UpsertStashMissingScene(ctx, domain.StashMissingScene{StashSceneID: "stale", Title: "Stale"}); err != nil {
		t.Fatal(err)
	}
	cutoff := time.Now().UTC().Add(time.Minute)
	if _, err := s.UpsertStashMissingScene(ctx, domain.StashMissingScene{StashSceneID: "fresh", Title: "Fresh"}); err != nil {
		t.Fatal(err)
	}
	// Simulate the "fresh" row being touched by a scan that started after
	// cutoff, and the "stale" row not being touched by it (its file
	// reappeared on disk, or it no longer exists in StashApp).
	if _, err := s.db.ExecContext(ctx, `UPDATE stash_missing_scenes SET last_scan_at=? WHERE stash_scene_id='fresh'`, cutoff.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	removed, err := s.PruneStashMissingScenes(ctx, cutoff)
	if err != nil || removed != 1 {
		t.Fatalf("removed=%d err=%v", removed, err)
	}
	remaining, err := s.StashMissingScenes(ctx, domain.StashMissingFilter{Limit: 10})
	if err != nil || len(remaining) != 1 || remaining[0].StashSceneID != "fresh" {
		t.Fatalf("remaining=%+v err=%v", remaining, err)
	}
}

// TestClearStashMissingScenesRemovesEverything covers the manual "Clear
// results" action: unlike PruneStashMissingScenes, which only drops rows a
// completed scan didn't re-confirm, this must remove every row
// unconditionally regardless of how recently each was last seen.
func TestClearStashMissingScenesRemovesEverything(t *testing.T) {
	ctx := context.Background()
	s, err := OpenSQLite(filepath.Join(t.TempDir(), "stash-missing-clear.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if _, err := s.UpsertStashMissingScene(ctx, domain.StashMissingScene{StashSceneID: "a", Title: "A"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpsertStashMissingScene(ctx, domain.StashMissingScene{StashSceneID: "b", Title: "B"}); err != nil {
		t.Fatal(err)
	}
	removed, err := s.ClearStashMissingScenes(ctx)
	if err != nil || removed != 2 {
		t.Fatalf("removed=%d err=%v", removed, err)
	}
	remaining, err := s.StashMissingScenes(ctx, domain.StashMissingFilter{Limit: 10})
	if err != nil || len(remaining) != 0 {
		t.Fatalf("expected no rows left after clearing, got %+v (err=%v)", remaining, err)
	}
	// Clearing an already-empty table is not an error, and reports 0 removed.
	removed, err = s.ClearStashMissingScenes(ctx)
	if err != nil || removed != 0 {
		t.Fatalf("clearing an empty table: removed=%d err=%v", removed, err)
	}
}

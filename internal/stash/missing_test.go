package stash

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Net005/JAVBeacon/internal/domain"
	"github.com/Net005/JAVBeacon/internal/download"
	"github.com/Net005/JAVBeacon/internal/scraper"
	"github.com/Net005/JAVBeacon/internal/store"
)

// TestMissingScanRecordsMissingScenesAndMatchesExisting covers TODO-2.0
// Phase 2's core scan: a scene whose file still exists on disk is not
// recorded at all, a scene whose file is gone and matches no existing
// release is recorded as an unmatched "missing" row, and a scene whose file
// is gone but whose code matches an existing release is recorded and
// immediately linked to it.
func TestMissingScanRecordsMissingScenesAndMatchesExisting(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	existing := filepath.Join(dir, "exists.mp4")
	if err := os.WriteFile(existing, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "missing-scan.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	site, _ := st.SaveSite(ctx, domain.Site{Title: "GIGA", Type: "Site", Name: "GIGA", Enabled: true})
	if _, err := st.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "DEF-456", Title: "Matched", Source: "GIGA"}); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/graphql", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"findScenes":{"scenes":[
			{"id":"1","title":"Has file","code":"ABC-100","files":[{"path":"` + existing + `"}]},
			{"id":"2","title":"No file, unmatched","code":"XYZ-999","files":[{"path":"` + filepath.Join(dir, "gone1.mp4") + `"}],"urls":["https://www.javlibrary.com/en/?v=xyz999"]},
			{"id":"3","title":"No file, matched","code":"DEF-456","files":[{"path":"` + filepath.Join(dir, "gone2.mp4") + `"}]}
		]}}}`))
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	if err := st.SaveSettings(ctx, map[string]string{"stash_base_url": server.URL}); err != nil {
		t.Fatal(err)
	}

	s := New(st, time.Second, slog.Default(), nil, nil)
	s.runMissingScan(ctx)

	status := s.MissingScanStatus()
	if status.Error != "" {
		t.Fatalf("scan error: %s", status.Error)
	}
	if status.Scenes != 3 || status.Missing != 2 || status.Matched != 1 {
		t.Fatalf("unexpected scan status: %+v", status)
	}
	restored := New(st, time.Second, slog.Default(), nil, nil).MissingScanStatus()
	if restored.FinishedAt.IsZero() || restored.Running || restored.Scenes != 3 || restored.Missing != 2 || restored.Matched != 1 {
		t.Fatalf("scan status was not restored after service restart: %+v", restored)
	}

	rows, err := st.StashMissingScenes(ctx, domain.StashMissingFilter{Limit: 10})
	if err != nil || len(rows) != 2 {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}
	var unmatched, matched *domain.StashMissingScene
	for i := range rows {
		switch rows[i].StashSceneID {
		case "2":
			unmatched = &rows[i]
		case "3":
			matched = &rows[i]
		}
	}
	if unmatched == nil || unmatched.ReleaseID != 0 || unmatched.JavLibraryURL != "https://www.javlibrary.com/en/?v=xyz999" {
		t.Fatalf("unmatched scene wrong: %+v", unmatched)
	}
	if matched == nil || matched.ReleaseVideoID != "DEF-456" {
		t.Fatalf("matched scene wrong: %+v", matched)
	}
}

// TestMissingScanAppliesMultiplePathRemaps covers TODO-2.0's Settings
// overhaul: stash_missing_path_remaps replaced the single
// stash_missing_path_from/stash_missing_path_to pair with a JSON-encoded
// list, since a StashApp instance can report more than one distinct mount
// prefix. Two scenes here use two different StashApp-reported prefixes
// ("/stash-a" and "/stash-b"), each remapped to a different real directory
// on disk; both files actually exist once remapped, so neither scene should
// be recorded as missing - proving every configured pair is tried, not just
// the first.
func TestMissingScanAppliesMultiplePathRemaps(t *testing.T) {
	ctx := context.Background()
	dirA, dirB := t.TempDir(), t.TempDir()
	fileA := filepath.Join(dirA, "aaa.mp4")
	fileB := filepath.Join(dirB, "bbb.mp4")
	if err := os.WriteFile(fileA, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fileB, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "missing-remap.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	mux := http.NewServeMux()
	mux.HandleFunc("/graphql", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"findScenes":{"scenes":[
			{"id":"1","title":"Remapped via pair A","code":"AAA-100","files":[{"path":"/stash-a/aaa.mp4"}]},
			{"id":"2","title":"Remapped via pair B","code":"BBB-200","files":[{"path":"/stash-b/bbb.mp4"}]}
		]}}}`))
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	remaps := `[{"from":"/stash-a","to":"` + dirA + `"},{"from":"/stash-b","to":"` + dirB + `"}]`
	if err := st.SaveSettings(ctx, map[string]string{"stash_base_url": server.URL, "stash_missing_path_remaps": remaps}); err != nil {
		t.Fatal(err)
	}

	s := New(st, time.Second, slog.Default(), nil, nil)
	s.runMissingScan(ctx)

	status := s.MissingScanStatus()
	if status.Error != "" {
		t.Fatalf("scan error: %s", status.Error)
	}
	if status.Scenes != 2 || status.Missing != 0 {
		t.Fatalf("expected both remapped files to be found on disk (missing=0), got %+v", status)
	}
}

// spyStore wraps a store.Store, invoking onUpsertMissing (when set) every
// time UpsertStashMissingScene is called, before delegating to the real
// store - used to observe MissingScanStatus mid-scan, from inside the very
// call that records a missing scene.
type spyStore struct {
	store.Store
	onUpsertMissing func(x domain.StashMissingScene)
}

func (sp *spyStore) UpsertStashMissingScene(ctx context.Context, x domain.StashMissingScene) (int64, error) {
	if sp.onUpsertMissing != nil {
		sp.onUpsertMissing(x)
	}
	return sp.Store.UpsertStashMissingScene(ctx, x)
}

// TestMissingScanReportsLiveProgressBetweenScenes is TODO-2.0 Task A's core
// fix proven for the scan job itself: runMissingScan used to only publish
// Scenes/Missing/Matched once the whole scan finished, so a poller watching
// MissingScanStatus saw a static 0 the entire time it ran. While the second
// missing scene is being recorded, the first scene's outcome must already
// be visible.
func TestMissingScanReportsLiveProgressBetweenScenes(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "missing-scan-progress.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	mux := http.NewServeMux()
	mux.HandleFunc("/graphql", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"findScenes":{"scenes":[
			{"id":"1","title":"First","code":"AAA-100","files":[{"path":"/gone/aaa.mp4"}]},
			{"id":"2","title":"Second","code":"BBB-200","files":[{"path":"/gone/bbb.mp4"}]}
		]}}}`))
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	if err := st.SaveSettings(ctx, map[string]string{"stash_base_url": server.URL}); err != nil {
		t.Fatal(err)
	}

	var s *Service
	var midFlightProcessed, midFlightMissing int
	var midFlightCurrentItem string
	spy := &spyStore{Store: st, onUpsertMissing: func(x domain.StashMissingScene) {
		if x.Code == "BBB-200" {
			status := s.MissingScanStatus()
			midFlightProcessed = status.Processed
			midFlightMissing = status.Missing
			midFlightCurrentItem = status.CurrentItem
		}
	}}
	s = New(spy, time.Second, slog.Default(), nil, nil)
	s.runMissingScan(ctx)

	// While BBB-200 is being recorded, AAA-100's own iteration (Processed=1,
	// Missing=1) must already have been published - not still at zero.
	if midFlightProcessed != 1 || midFlightMissing != 1 {
		t.Fatalf("expected scene AAA-100's progress to already be published while BBB-200 was being recorded, got processed=%d missing=%d", midFlightProcessed, midFlightMissing)
	}
	if midFlightCurrentItem != "AAA-100" {
		t.Fatalf("expected CurrentItem to still show AAA-100 (BBB-200's own flush hasn't happened yet), got %q", midFlightCurrentItem)
	}

	final := s.MissingScanStatus()
	if final.Scenes != 2 || final.Processed != 2 || final.Missing != 2 || final.CurrentItem != "" {
		t.Fatalf("unexpected final scan status: %+v", final)
	}
}

// TestStatWithTimeoutFastPaths covers statWithTimeout's two ordinary
// outcomes - an existing path found before the timeout, and a nonexistent
// path failing before the timeout - which is what filesExist relies on for
// every normal (non-hung) scan. The actual timeout branch (a path on a
// stalled network mount) isn't exercised here: reliably simulating a
// syscall that hangs for a controlled duration isn't practical in a
// portable unit test, so this only pins down the fast paths, and
// missingScanStatTimeout is left at its production default since neither
// case is expected to ever wait for it.
func TestStatWithTimeoutFastPaths(t *testing.T) {
	dir := t.TempDir()
	existing := filepath.Join(dir, "scene.mp4")
	if err := os.WriteFile(existing, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := statWithTimeout(existing, time.Second); err != nil {
		t.Fatalf("expected the existing file to be found, got error: %v", err)
	}
	if _, err := statWithTimeout(filepath.Join(dir, "missing.mp4"), time.Second); err == nil {
		t.Fatal("expected a nonexistent file to report an error")
	}
}

// TestClearMissingScenesRemovesRowsAndRefusesDuringAScan covers the manual
// "Clear results" action end to end through the service: it must wipe every
// recorded row, and it must refuse (rather than race a running scan's
// writes) while StartMissingScan is in progress.
func TestClearMissingScenesRemovesRowsAndRefusesDuringAScan(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "missing-clear-service.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.UpsertStashMissingScene(ctx, domain.StashMissingScene{StashSceneID: "a", Title: "A"}); err != nil {
		t.Fatal(err)
	}

	s := New(st, time.Second, slog.Default(), nil, nil)
	removed, err := s.ClearMissingScenes(ctx)
	if err != nil || removed != 1 {
		t.Fatalf("removed=%d err=%v", removed, err)
	}

	s.missingMu.Lock()
	s.missingStatus = MissingStatus{Running: true}
	s.missingMu.Unlock()
	if _, err := s.ClearMissingScenes(ctx); err == nil {
		t.Fatal("expected ClearMissingScenes to refuse while a scan is running")
	}
}

// TestMissingScanRestrictsToConfiguredFolderScope covers TODO-2.0 Task A's
// "support restricting to one StashApp folder" requirement: with
// stash_missing_folder_scope set, only scenes whose file path contains that
// text (case-insensitively) should be scanned at all - a scene outside the
// scope must not appear in the results, in Scenes, or in Missing, even
// though its file is also gone.
func TestMissingScanRestrictsToConfiguredFolderScope(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "missing-scope.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	mux := http.NewServeMux()
	mux.HandleFunc("/graphql", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"findScenes":{"scenes":[
			{"id":"1","title":"In scope","code":"AAA-100","files":[{"path":"/library/Studio A/aaa.mp4"}]},
			{"id":"2","title":"Out of scope","code":"BBB-200","files":[{"path":"/library/Studio B/bbb.mp4"}]}
		]}}}`))
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	if err := st.SaveSettings(ctx, map[string]string{"stash_base_url": server.URL, "stash_missing_folder_scope": "/studio a"}); err != nil {
		t.Fatal(err)
	}

	s := New(st, time.Second, slog.Default(), nil, nil)
	s.runMissingScan(ctx)

	status := s.MissingScanStatus()
	if status.Error != "" {
		t.Fatalf("scan error: %s", status.Error)
	}
	if status.Scenes != 1 || status.Missing != 1 {
		t.Fatalf("expected the out-of-scope scene to be excluded entirely, got %+v", status)
	}
	rows, err := st.StashMissingScenes(ctx, domain.StashMissingFilter{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Code != "AAA-100" {
		t.Fatalf("expected only the in-scope scene to be recorded, got %+v", rows)
	}
}

// TestMissingScanRestrictsToMultipleConfiguredFolderScopes covers the
// multi-scope support added for the pre-scan scope picker: with
// stash_missing_folder_scope holding more than one line, a scene should be
// included if its path matches ANY of them (not just the first), and a
// scene matching none should still be excluded.
func TestMissingScanRestrictsToMultipleConfiguredFolderScopes(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "missing-multi-scope.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	mux := http.NewServeMux()
	mux.HandleFunc("/graphql", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"findScenes":{"scenes":[
			{"id":"1","title":"Studio A","code":"AAA-100","files":[{"path":"/library/Studio A/aaa.mp4"}]},
			{"id":"2","title":"Studio B","code":"BBB-200","files":[{"path":"/library/Studio B/bbb.mp4"}]},
			{"id":"3","title":"Studio C","code":"CCC-300","files":[{"path":"/library/Studio C/ccc.mp4"}]}
		]}}}`))
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	if err := st.SaveSettings(ctx, map[string]string{"stash_base_url": server.URL, "stash_missing_folder_scope": "/studio a\n/studio c"}); err != nil {
		t.Fatal(err)
	}

	s := New(st, time.Second, slog.Default(), nil, nil)
	s.runMissingScan(ctx)

	status := s.MissingScanStatus()
	if status.Error != "" {
		t.Fatalf("scan error: %s", status.Error)
	}
	if status.Scenes != 2 || status.Missing != 2 {
		t.Fatalf("expected both scoped scenes (A and C) but not B, got %+v", status)
	}
	rows, err := st.StashMissingScenes(ctx, domain.StashMissingFilter{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, r := range rows {
		got[r.Code] = true
	}
	if len(rows) != 2 || !got["AAA-100"] || !got["CCC-300"] || got["BBB-200"] {
		t.Fatalf("expected AAA-100 and CCC-300 recorded, not BBB-200, got %+v", rows)
	}
}

// TestRetrieveMissingScrapesJavLibraryAndLinksNewRelease covers the
// "search and add the release from JavLibrary ... with priority 1" step:
// given a missing scene with no JAVBeacon entry, StartRetrieve/
// runRetrieve should scrape its JavLibrary URL, create a release under the
// synthetic recovery site, and link the scene to it.
func TestRetrieveMissingScrapesJavLibraryAndLinksNewRelease(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "retrieve.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	mux := http.NewServeMux()
	mux.HandleFunc("/javabc123.html", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<html><title>ABC-123 Some Title - JAVLibrary</title><img id="video_jacket_img" src="/x.jpg"></html>`))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	id, err := st.UpsertStashMissingScene(ctx, domain.StashMissingScene{
		StashSceneID: "scn-1", Title: "Some Title", Code: "ABC-123", JavLibraryURL: server.URL + "/javabc123.html",
	})
	if err != nil {
		t.Fatal(err)
	}

	jav := scraper.NewJavLibrary(2*time.Second, "", 0, nil)
	s := New(st, time.Second, slog.Default(), jav, nil)
	s.runRetrieve(ctx, []int64{id})

	status := s.RetrieveScanStatus()
	if status.Retrieved != 1 || status.Failed != 0 {
		t.Fatalf("unexpected retrieve status: %+v", status)
	}

	row, err := st.StashMissingScene(ctx, id)
	if err != nil || row.ReleaseID == 0 || row.ReleaseVideoID != "ABC-123" {
		t.Fatalf("scene not linked to a retrieved release: %+v (err=%v)", row, err)
	}
	release, err := st.Release(ctx, row.ReleaseID)
	if err != nil {
		t.Fatal(err)
	}
	site, err := st.Sites(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var recovery *domain.Site
	for i := range site {
		if site[i].Title == recoverySiteTitle {
			recovery = &site[i]
		}
	}
	if recovery == nil || recovery.Enabled {
		t.Fatalf("expected a disabled synthetic recovery site to be created, got %+v", recovery)
	}
	if release.SiteID != recovery.ID {
		t.Fatalf("retrieved release not attached to the recovery site: %+v", release)
	}
}

// TestApplySelectionMonitorOnlySetsFlagWithoutSearching covers the
// "Monitored only" result action: it must flip MonitorDownload on for the
// scene's linked release without invoking the download service at all.
func TestApplySelectionMonitorOnlySetsFlagWithoutSearching(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "apply-monitor.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	site, _ := st.SaveSite(ctx, domain.Site{Title: "GIGA", Type: "Site", Name: "GIGA", Enabled: true})
	if _, err := st.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "MON-1", Title: "T", Source: "GIGA"}); err != nil {
		t.Fatal(err)
	}
	releases, _ := st.Releases(ctx, domain.ReleaseFilter{Search: "MON-1", Limit: 1})
	id, err := st.UpsertStashMissingScene(ctx, domain.StashMissingScene{StashSceneID: "scn-2", Title: "T", Code: "MON-1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.LinkStashMissingRelease(ctx, id, releases[0].ID); err != nil {
		t.Fatal(err)
	}

	s := New(st, time.Second, slog.Default(), nil, nil)
	s.runApply(ctx, []int64{id}, ApplyModeMonitorOnly, false)

	status := s.ApplyRunStatus()
	if status.Monitored != 1 || status.Found != 0 || status.NotFound != 0 || status.Failed != 0 {
		t.Fatalf("unexpected apply status: %+v", status)
	}
	release, err := st.Release(ctx, releases[0].ID)
	if err != nil || !release.MonitorDownload {
		t.Fatalf("expected MonitorDownload=true, got %+v (err=%v)", release, err)
	}
}

// TestApplySelectionMonitorAndDownloadSearchesInBackground covers the
// "Monitor + Download + search" result action: it must monitor the release
// AND drive download.Service.SearchAndDownloadNow, reporting a "not_found"
// result (rather than silently doing nothing) when no accepted torrent
// match turns up - this is the count the user asked to see reported back
// ("found x releases on sukebei etc..").
func TestApplySelectionMonitorAndDownloadSearchesInBackground(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "apply-download.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	mux := http.NewServeMux()
	mux.HandleFunc("/feed", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<rss><channel></channel></rss>`))
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	if err := st.SaveSettings(ctx, map[string]string{"search_url_template": server.URL + "/feed?q=<release_id>"}); err != nil {
		t.Fatal(err)
	}
	site, _ := st.SaveSite(ctx, domain.Site{Title: "GIGA", Type: "Site", Name: "GIGA", Enabled: false, Download: false})
	if _, err := st.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "MON-2", Title: "T", Source: "GIGA"}); err != nil {
		t.Fatal(err)
	}
	releases, _ := st.Releases(ctx, domain.ReleaseFilter{Search: "MON-2", Limit: 1})
	id, err := st.UpsertStashMissingScene(ctx, domain.StashMissingScene{StashSceneID: "scn-3", Title: "T", Code: "MON-2"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.LinkStashMissingRelease(ctx, id, releases[0].ID); err != nil {
		t.Fatal(err)
	}

	downloads := download.New(st, 2*time.Second, slog.Default())
	s := New(st, time.Second, slog.Default(), nil, downloads)
	s.runApply(ctx, []int64{id}, ApplyModeMonitorDownload, false)

	status := s.ApplyRunStatus()
	if status.Monitored != 1 || status.NotFound != 1 || status.Found != 0 || status.Failed != 0 {
		t.Fatalf("unexpected apply status: %+v", status)
	}
	release, err := st.Release(ctx, releases[0].ID)
	if err != nil || !release.MonitorDownload {
		t.Fatalf("expected MonitorDownload=true even when no torrent match was found: %+v (err=%v)", release, err)
	}
}

// TestApplySelectionSetsIgnoreLocalForceDownloadFlagAutomatically covers the
// second half of the "Missing Library Files" fix: unlike allowNonPreferred
// (an explicit checkbox), IgnoreLocalForceDownload is always set on a
// release the moment runApply marks it monitored, in both apply modes and
// regardless of the allowNonPreferred toggle - because every release
// reachable from Missing Library Files already has a StashApp scene by
// definition of being a "missing file" entry, so download.Service must
// never skip it as an "already in StashApp" duplicate.
func TestApplySelectionSetsIgnoreLocalForceDownloadFlagAutomatically(t *testing.T) {
	ctx := context.Background()

	t.Run("monitor only", func(t *testing.T) {
		st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "apply-ignore-local-monitor-only.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer st.Close()
		site, _ := st.SaveSite(ctx, domain.Site{Title: "GIGA", Type: "Site", Name: "GIGA", Enabled: true})
		if _, err := st.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "ILF-1", Title: "T", Source: "GIGA"}); err != nil {
			t.Fatal(err)
		}
		releases, _ := st.Releases(ctx, domain.ReleaseFilter{Search: "ILF-1", Limit: 1})
		id, err := st.UpsertStashMissingScene(ctx, domain.StashMissingScene{StashSceneID: "scn-ilf-1", Title: "T", Code: "ILF-1"})
		if err != nil {
			t.Fatal(err)
		}
		if err := st.LinkStashMissingRelease(ctx, id, releases[0].ID); err != nil {
			t.Fatal(err)
		}

		s := New(st, time.Second, slog.Default(), nil, nil)
		s.runApply(ctx, []int64{id}, ApplyModeMonitorOnly, false)

		release, err := st.Release(ctx, releases[0].ID)
		if err != nil || !release.IgnoreLocalForceDownload {
			t.Fatalf("expected IgnoreLocalForceDownload=true after a monitor-only apply, got %+v (err=%v)", release, err)
		}
	})

	t.Run("monitor and download, allowNonPreferred off", func(t *testing.T) {
		st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "apply-ignore-local-monitor-download.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer st.Close()
		mux := http.NewServeMux()
		mux.HandleFunc("/feed", func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`<rss><channel></channel></rss>`))
		})
		server := httptest.NewServer(mux)
		defer server.Close()
		if err := st.SaveSettings(ctx, map[string]string{"search_url_template": server.URL + "/feed?q=<release_id>"}); err != nil {
			t.Fatal(err)
		}
		site, _ := st.SaveSite(ctx, domain.Site{Title: "GIGA", Type: "Site", Name: "GIGA", Enabled: false, Download: false})
		if _, err := st.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "ILF-2", Title: "T", Source: "GIGA"}); err != nil {
			t.Fatal(err)
		}
		releases, _ := st.Releases(ctx, domain.ReleaseFilter{Search: "ILF-2", Limit: 1})
		id, err := st.UpsertStashMissingScene(ctx, domain.StashMissingScene{StashSceneID: "scn-ilf-2", Title: "T", Code: "ILF-2"})
		if err != nil {
			t.Fatal(err)
		}
		if err := st.LinkStashMissingRelease(ctx, id, releases[0].ID); err != nil {
			t.Fatal(err)
		}

		downloads := download.New(st, 2*time.Second, slog.Default())
		s := New(st, time.Second, slog.Default(), nil, downloads)
		s.runApply(ctx, []int64{id}, ApplyModeMonitorDownload, false)

		release, err := st.Release(ctx, releases[0].ID)
		if err != nil || !release.IgnoreLocalForceDownload {
			t.Fatalf("expected IgnoreLocalForceDownload=true after a monitor+download apply, got %+v (err=%v)", release, err)
		}
	})
}

// TestApplySelectionMonitorDownloadDownloadsDespiteExistingLocalStashScene
// is the core end-to-end proof of the reported bug and its fix: a release
// recovered through Missing Library Files already has a matched StashApp
// scene (is_local=true - the scene exists, only its file on disk is
// missing), which download.Service.duplicate would otherwise treat as
// "release already exists in StashApp" and silently skip. Because runApply
// now sets IgnoreLocalForceDownload automatically, the immediate
// search-and-download this apply run drives must find and actually queue
// the download instead of reporting not_found.
func TestApplySelectionMonitorDownloadDownloadsDespiteExistingLocalStashScene(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "apply-download-despite-local.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	mux := http.NewServeMux()
	mux.HandleFunc("/feed", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<rss xmlns:nyaa="https://nyaa.si/xmlns/nyaa"><channel>` +
			`<item><title>trusted@ ILF-3 seeded</title><link>magnet:?xt=urn:btih:ilf3hash</link><nyaa:seeders>3</nyaa:seeders></item>` +
			`</channel></rss>`))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	qbMux := http.NewServeMux()
	qbMux.HandleFunc("POST /api/v2/auth/login", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("Ok.")) })
	qbMux.HandleFunc("GET /api/v2/torrents/categories", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{}`)) })
	var added bool
	qbMux.HandleFunc("GET /api/v2/torrents/info", func(w http.ResponseWriter, _ *http.Request) {
		if !added {
			_, _ = w.Write([]byte(`[]`))
			return
		}
		_, _ = w.Write([]byte(`[{"hash":"ilf3hash","name":"trusted@ ILF-3 seeded"}]`))
	})
	qbMux.HandleFunc("POST /api/v2/torrents/add", func(w http.ResponseWriter, _ *http.Request) {
		added = true
		_, _ = w.Write([]byte("Ok."))
	})
	qbServer := httptest.NewServer(qbMux)
	defer qbServer.Close()

	if err := st.SaveSettings(ctx, map[string]string{
		"accepted_patterns":   "trusted@",
		"search_url_template": server.URL + "/feed?q=<release_id>",
		"qb_url":              qbServer.URL,
	}); err != nil {
		t.Fatal(err)
	}
	site, _ := st.SaveSite(ctx, domain.Site{Title: "GIGA", Type: "Site", Name: "GIGA", Enabled: false, Download: false})
	if _, err := st.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "ILF-3", Title: "T", Source: "GIGA"}); err != nil {
		t.Fatal(err)
	}
	releases, _ := st.Releases(ctx, domain.ReleaseFilter{Search: "ILF-3", Limit: 1})
	// The defining trait of a Missing Library Files entry: already linked
	// in StashApp before this apply run ever touches it.
	if err := st.SetStashState(ctx, releases[0].ID, true, "scn-ilf-3"); err != nil {
		t.Fatal(err)
	}
	id, err := st.UpsertStashMissingScene(ctx, domain.StashMissingScene{StashSceneID: "scn-ilf-3", Title: "T", Code: "ILF-3"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.LinkStashMissingRelease(ctx, id, releases[0].ID); err != nil {
		t.Fatal(err)
	}

	downloads := download.New(st, 2*time.Second, slog.Default())
	s := New(st, time.Second, slog.Default(), nil, downloads)
	s.runApply(ctx, []int64{id}, ApplyModeMonitorDownload, false)

	status := s.ApplyRunStatus()
	if status.Found != 1 || status.NotFound != 0 || status.Failed != 0 {
		t.Fatalf("expected found=1 despite the release already being linked in StashApp, got %+v", status)
	}
	rows, err := st.Downloads(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	var downloading bool
	for _, d := range rows {
		if d.Status == "downloading" {
			downloading = true
		}
	}
	if !downloading {
		t.Fatalf("expected a downloading download row despite the release already being local, got %+v", rows)
	}
}

// TestApplySelectionThreadsAllowNonPreferredThroughToSearchAndDownloadNow is

// TODO-2.0 Task A's coverage for StartApply/runApply's new allowNonPreferred
// parameter: given an identical feed carrying only a seeded-but-unaccepted
// torrent, the apply job must report "not_found" when the toggle is off
// (download.Service.SearchAndDownloadNow's original, stricter behavior) and
// must actually find and download that result when the toggle is on - proof
// the bool set by the "Allow non-preferred filenames" checkbox actually
// reaches download.Service rather than being silently dropped somewhere in
// stash.Service.
func TestApplySelectionThreadsAllowNonPreferredThroughToSearchAndDownloadNow(t *testing.T) {
	ctx := context.Background()

	newScene := func(t *testing.T, st store.Store, videoID, sceneID string) (releaseID, sceneRowID int64) {
		t.Helper()
		site, _ := st.SaveSite(ctx, domain.Site{Title: "GIGA", Type: "Site", Name: "GIGA", Enabled: false, Download: false})
		if _, err := st.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: videoID, Title: "T", Source: "GIGA"}); err != nil {
			t.Fatal(err)
		}
		releases, err := st.Releases(ctx, domain.ReleaseFilter{Search: videoID, Limit: 1})
		if err != nil || len(releases) != 1 {
			t.Fatalf("release setup failed: rows=%d err=%v", len(releases), err)
		}
		id, err := st.UpsertStashMissingScene(ctx, domain.StashMissingScene{StashSceneID: sceneID, Title: "T", Code: videoID})
		if err != nil {
			t.Fatal(err)
		}
		if err := st.LinkStashMissingRelease(ctx, id, releases[0].ID); err != nil {
			t.Fatal(err)
		}
		return releases[0].ID, id
	}

	feed := func() *httptest.Server {
		mux := http.NewServeMux()
		mux.HandleFunc("/feed", func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`<rss xmlns:nyaa="https://nyaa.si/xmlns/nyaa"><channel>` +
				`<item><title>rejected@ THREAD-100 seeded but unaccepted</title><link>magnet:?xt=urn:btih:thread</link><nyaa:seeders>3</nyaa:seeders></item>` +
				`</channel></rss>`))
		})
		return httptest.NewServer(mux)
	}

	t.Run("off: reports not_found even though a seeded result exists", func(t *testing.T) {
		st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "allow-non-preferred-off.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer st.Close()
		server := feed()
		defer server.Close()
		if err := st.SaveSettings(ctx, map[string]string{"search_url_template": server.URL + "/feed?q=<release_id>"}); err != nil {
			t.Fatal(err)
		}
		_, sceneID := newScene(t, st, "THREAD-100", "scn-thread-100")

		downloads := download.New(st, 2*time.Second, slog.Default())
		s := New(st, time.Second, slog.Default(), nil, downloads)
		s.runApply(ctx, []int64{sceneID}, ApplyModeMonitorDownload, false)

		status := s.ApplyRunStatus()
		if status.Found != 0 || status.NotFound != 1 || status.Failed != 0 {
			t.Fatalf("expected not_found with allowNonPreferred=false, got %+v", status)
		}
	})

	t.Run("on: downloads the seeded-but-unaccepted result via the fallback chain", func(t *testing.T) {
		st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "allow-non-preferred-on.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer st.Close()
		server := feed()
		defer server.Close()

		qbMux := http.NewServeMux()
		qbMux.HandleFunc("POST /api/v2/auth/login", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("Ok.")) })
		qbMux.HandleFunc("GET /api/v2/torrents/categories", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{}`)) })
		// Service.Download now verifies a torrent actually registered in
		// qBittorrent before trusting the "Ok." /add response (it can be
		// returned for input qBittorrent never actually queues), so this
		// stub must echo the torrent back through /torrents/info the same
		// way a real qBittorrent instance would - but only once /add has
		// actually been called, or the up-front duplicate check would see
		// the torrent "already there" before anything was ever added.
		var added bool
		qbMux.HandleFunc("GET /api/v2/torrents/info", func(w http.ResponseWriter, _ *http.Request) {
			if !added {
				_, _ = w.Write([]byte(`[]`))
				return
			}
			_, _ = w.Write([]byte(`[{"hash":"thread100hash","name":"rejected@ THREAD-100 seeded but unaccepted"}]`))
		})
		qbMux.HandleFunc("POST /api/v2/torrents/add", func(w http.ResponseWriter, _ *http.Request) {
			added = true
			_, _ = w.Write([]byte("Ok."))
		})
		qbServer := httptest.NewServer(qbMux)
		defer qbServer.Close()

		if err := st.SaveSettings(ctx, map[string]string{
			"search_url_template": server.URL + "/feed?q=<release_id>",
			"qb_url":              qbServer.URL,
		}); err != nil {
			t.Fatal(err)
		}
		releaseID, sceneID := newScene(t, st, "THREAD-100", "scn-thread-100")

		downloads := download.New(st, 2*time.Second, slog.Default())
		s := New(st, time.Second, slog.Default(), nil, downloads)
		s.runApply(ctx, []int64{sceneID}, ApplyModeMonitorDownload, true)

		status := s.ApplyRunStatus()
		if status.Found != 1 || status.NotFound != 0 || status.Failed != 0 {
			t.Fatalf("expected found=1 with allowNonPreferred=true and a working qBittorrent, got %+v", status)
		}

		rows, err := st.Downloads(ctx, "")
		if err != nil {
			t.Fatal(err)
		}
		var sawExcluded bool
		for _, d := range rows {
			if d.Name == "rejected@ THREAD-100 seeded but unaccepted" && d.FilenamePatternExcluded && d.Status == "downloading" {
				sawExcluded = true
			}
		}
		if !sawExcluded {
			t.Fatalf("expected the fallback pick to be downloaded and marked FilenamePatternExcluded, got %+v", rows)
		}
		// The override must persist on the release itself, not just this
		// one apply run, so the scheduled download-search job keeps using
		// relaxed matching for it on every future check too.
		release, err := st.Release(ctx, releaseID)
		if err != nil {
			t.Fatal(err)
		}
		if !release.AllowNonPreferredFilenames {
			t.Fatalf("expected allowNonPreferred=true to persist onto the release, got %+v", release)
		}
	})
}

// TestRetrieveReportsLiveProgressBetweenScenes is TODO-2.0 Task A's core
// fix proven directly: runRetrieve used to only publish its result once
// the entire batch finished, so a poller watching RetrieveScanStatus saw
// nothing but a static "0 of N" the whole time it ran ("I see 0/87 but no
// idea how long it will take"). While the second scene's JavLibrary page
// is being fetched, the first scene's outcome must already be visible.
func TestRetrieveReportsLiveProgressBetweenScenes(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "retrieve-progress.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	var s *Service
	var midFlightRetrieved, midFlightResults int
	var midFlightCurrentItem string

	mux := http.NewServeMux()
	mux.HandleFunc("/scene-a.html", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<html><title>AAA-100 First - JAVLibrary</title><img id="video_jacket_img" src="/x.jpg"></html>`))
	})
	mux.HandleFunc("/scene-b.html", func(w http.ResponseWriter, r *http.Request) {
		status := s.RetrieveScanStatus()
		midFlightRetrieved = status.Retrieved
		midFlightResults = len(status.Results)
		midFlightCurrentItem = status.CurrentItem
		_, _ = w.Write([]byte(`<html><title>BBB-200 Second - JAVLibrary</title><img id="video_jacket_img" src="/x.jpg"></html>`))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	idA, err := st.UpsertStashMissingScene(ctx, domain.StashMissingScene{StashSceneID: "scn-a", Title: "First", Code: "AAA-100", JavLibraryURL: server.URL + "/scene-a.html"})
	if err != nil {
		t.Fatal(err)
	}
	idB, err := st.UpsertStashMissingScene(ctx, domain.StashMissingScene{StashSceneID: "scn-b", Title: "Second", Code: "BBB-200", JavLibraryURL: server.URL + "/scene-b.html"})
	if err != nil {
		t.Fatal(err)
	}

	jav := scraper.NewJavLibrary(2*time.Second, "", 0, nil)
	s = New(st, time.Second, slog.Default(), jav, nil)
	s.runRetrieve(ctx, []int64{idA, idB})

	if midFlightResults != 1 || midFlightRetrieved != 1 {
		t.Fatalf("expected scene A's result to already be published while scene B was still being fetched, got retrieved=%d results=%d", midFlightRetrieved, midFlightResults)
	}
	if midFlightCurrentItem != "BBB-200" {
		t.Fatalf("expected CurrentItem to already show scene B (by its code) while it was being fetched, got %q", midFlightCurrentItem)
	}

	final := s.RetrieveScanStatus()
	if final.Retrieved != 2 || final.Failed != 0 || final.CurrentItem != "" {
		t.Fatalf("unexpected final retrieve status: %+v", final)
	}
}

// TestApplyReportsLiveProgressBetweenScenes is the same live-progress fix
// applied to the bulk "Monitor + Download + search" apply job - the one
// most likely to run against a large selection and take a while. While the
// second release's search request is in flight, the first release's
// outcome must already be reflected in ApplyRunStatus.
func TestApplyReportsLiveProgressBetweenScenes(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "apply-progress.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	var s *Service
	var midFlightMonitored, midFlightResults int
	var midFlightCurrentItem string

	mux := http.NewServeMux()
	mux.HandleFunc("/feed", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("q") == "MON-B" {
			status := s.ApplyRunStatus()
			midFlightMonitored = status.Monitored
			midFlightResults = len(status.Results)
			midFlightCurrentItem = status.CurrentItem
		}
		_, _ = w.Write([]byte(`<rss><channel></channel></rss>`))
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	if err := st.SaveSettings(ctx, map[string]string{"search_url_template": server.URL + "/feed?q=<release_id>"}); err != nil {
		t.Fatal(err)
	}

	site, _ := st.SaveSite(ctx, domain.Site{Title: "GIGA", Type: "Site", Name: "GIGA", Enabled: false, Download: false})
	if _, err := st.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "MON-A", Title: "A", Source: "GIGA"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "MON-B", Title: "B", Source: "GIGA"}); err != nil {
		t.Fatal(err)
	}
	releasesA, _ := st.Releases(ctx, domain.ReleaseFilter{Search: "MON-A", Limit: 1})
	releasesB, _ := st.Releases(ctx, domain.ReleaseFilter{Search: "MON-B", Limit: 1})
	idA, err := st.UpsertStashMissingScene(ctx, domain.StashMissingScene{StashSceneID: "scn-a", Title: "A", Code: "MON-A"})
	if err != nil {
		t.Fatal(err)
	}
	idB, err := st.UpsertStashMissingScene(ctx, domain.StashMissingScene{StashSceneID: "scn-b", Title: "B", Code: "MON-B"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.LinkStashMissingRelease(ctx, idA, releasesA[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := st.LinkStashMissingRelease(ctx, idB, releasesB[0].ID); err != nil {
		t.Fatal(err)
	}

	downloads := download.New(st, 2*time.Second, slog.Default())
	s = New(st, time.Second, slog.Default(), nil, downloads)
	s.runApply(ctx, []int64{idA, idB}, ApplyModeMonitorDownload, false)

	// By the time release B's search fires, both releases have already had
	// PatchRelease's monitor flag set (that happens before each release's
	// own search) - so Monitored is 2 - but only release A has an entry in
	// Results, since B's search+download outcome is still in flight.
	if midFlightMonitored != 2 || midFlightResults != 1 {
		t.Fatalf("expected release A's result to already be published while release B was still being searched, got monitored=%d results=%d", midFlightMonitored, midFlightResults)
	}
	if midFlightCurrentItem != "Searching for MON-B…" {
		t.Fatalf("expected CurrentItem to already show release B's search while it was in flight, got %q", midFlightCurrentItem)
	}

	final := s.ApplyRunStatus()
	if final.Monitored != 2 || final.NotFound != 2 || final.Found != 0 || final.Failed != 0 || final.CurrentItem != "" {
		t.Fatalf("unexpected final apply status: %+v", final)
	}
}

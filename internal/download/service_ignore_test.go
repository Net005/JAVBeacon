package download

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/Net005/JAVBeacon/internal/domain"
	"github.com/Net005/JAVBeacon/internal/store"
)

// TestAutoSkipsReleaseIgnoredByTag covers the bug report's "ignore from
// auto-download" requirement: a release tagged with an ignore_tags entry
// must never reach Auto's search/download step even though its site has
// Download enabled (the one gate Auto otherwise checks). The feed handler
// counts hits so the test proves Auto returned before ever searching,
// rather than merely that no download ended up accepted.
func TestAutoSkipsReleaseIgnoredByTag(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "auto-ignore-tag.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	feedHits := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/feed", func(w http.ResponseWriter, _ *http.Request) {
		feedHits++
		_, _ = w.Write([]byte(`<rss><channel><item><title>trusted@ PRED-902</title><link>magnet:?xt=urn:btih:abc</link></item></channel></rss>`))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	if err := st.SaveSettings(ctx, map[string]string{
		"accepted_patterns":   "trusted@",
		"search_url_template": server.URL + "/feed?q=<release_id>",
		"ignore_tags":         "Big Tits\nSolowork",
	}); err != nil {
		t.Fatal(err)
	}
	site, _ := st.SaveSite(ctx, domain.Site{Title: "Test", Type: "Site", Name: "JavLibrary", Enabled: true, Download: true})
	if _, err := st.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "PRED-902", Title: "Test", Source: "JavLibrary", Released: true, Genres: []string{"Solowork"}}); err != nil {
		t.Fatal(err)
	}
	releases, err := st.Releases(ctx, domain.ReleaseFilter{Search: "PRED-902", Limit: 10})
	if err != nil || len(releases) != 1 {
		t.Fatalf("release setup failed: rows=%d err=%v", len(releases), err)
	}

	service := New(st, 2*time.Second, slog.Default())
	service.Auto(ctx, releases[0])

	if feedHits != 0 {
		t.Fatalf("expected Auto to skip the ignored release before searching, but the feed was hit %d time(s)", feedHits)
	}
	downloads, err := st.Downloads(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(downloads) != 0 {
		t.Fatalf("expected no download activity for an ignored release, got %+v", downloads)
	}
}

// TestAutoSkipsReleaseIgnoredByTagContainingComma covers the bug report that
// a tag containing a literal comma (e.g. "Best, Omnibus") could never match
// because ignore_tags used to be split on commas as well as newlines,
// breaking the tag into two useless fragments ("Best" and "Omnibus"). Tags
// are one-per-line now, so a comma inside a tag must survive intact.
func TestAutoSkipsReleaseIgnoredByTagContainingComma(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "auto-ignore-tag-comma.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	feedHits := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/feed", func(w http.ResponseWriter, _ *http.Request) {
		feedHits++
		_, _ = w.Write([]byte(`<rss><channel><item><title>trusted@ PRED-905</title><link>magnet:?xt=urn:btih:abc</link></item></channel></rss>`))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	if err := st.SaveSettings(ctx, map[string]string{
		"accepted_patterns":   "trusted@",
		"search_url_template": server.URL + "/feed?q=<release_id>",
		"ignore_tags":         "Best, Omnibus\nSolowork",
	}); err != nil {
		t.Fatal(err)
	}
	site, _ := st.SaveSite(ctx, domain.Site{Title: "Test", Type: "Site", Name: "JavLibrary", Enabled: true, Download: true})
	if _, err := st.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "PRED-905", Title: "Test", Source: "JavLibrary", Released: true, Genres: []string{"Best, Omnibus"}}); err != nil {
		t.Fatal(err)
	}
	releases, err := st.Releases(ctx, domain.ReleaseFilter{Search: "PRED-905", Limit: 10})
	if err != nil || len(releases) != 1 {
		t.Fatalf("release setup failed: rows=%d err=%v", len(releases), err)
	}

	service := New(st, 2*time.Second, slog.Default())
	service.Auto(ctx, releases[0])

	if feedHits != 0 {
		t.Fatalf("expected Auto to skip the release ignored by a comma-containing tag before searching, but the feed was hit %d time(s)", feedHits)
	}
	downloads, err := st.Downloads(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(downloads) != 0 {
		t.Fatalf("expected no download activity for an ignored release, got %+v", downloads)
	}
}

// TestAutoSkipsReleaseIgnoredByTitle mirrors TestAutoSkipsReleaseIgnoredByTag
// for an ignore_titles wildcard rule instead of a tag rule.
func TestAutoSkipsReleaseIgnoredByTitle(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "auto-ignore-title.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	feedHits := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/feed", func(w http.ResponseWriter, _ *http.Request) {
		feedHits++
		_, _ = w.Write([]byte(`<rss><channel><item><title>trusted@ PRED-903</title><link>magnet:?xt=urn:btih:abc</link></item></channel></rss>`))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	if err := st.SaveSettings(ctx, map[string]string{
		"accepted_patterns":   "trusted@",
		"search_url_template": server.URL + "/feed?q=<release_id>",
		"ignore_titles":       "*Insulted Like Garbage*",
	}); err != nil {
		t.Fatal(err)
	}
	site, _ := st.SaveSite(ctx, domain.Site{Title: "Test", Type: "Site", Name: "JavLibrary", Enabled: true, Download: true})
	if _, err := st.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "PRED-903", Title: `PRED-903 "I Want To Be Insulted Like Garbage"`, Source: "JavLibrary", Released: true}); err != nil {
		t.Fatal(err)
	}
	releases, err := st.Releases(ctx, domain.ReleaseFilter{Search: "PRED-903", Limit: 10})
	if err != nil || len(releases) != 1 {
		t.Fatalf("release setup failed: rows=%d err=%v", len(releases), err)
	}

	service := New(st, 2*time.Second, slog.Default())
	service.Auto(ctx, releases[0])

	if feedHits != 0 {
		t.Fatalf("expected Auto to skip the ignored release before searching, but the feed was hit %d time(s)", feedHits)
	}
}

// TestRunSearchSkipsIgnoredMonitoredRelease covers the "automatic
// monitoring" half of the bug report: a release enrolled in the scheduled
// "monitored search" job (MonitorDownload=true) must be skipped, and
// counted in job.Skipped, when it matches an ignore rule - the same as if
// runSearch had found it were already a duplicate.
func TestRunSearchSkipsIgnoredMonitoredRelease(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "runsearch-ignore.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	feedHits := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/feed", func(w http.ResponseWriter, _ *http.Request) {
		feedHits++
		_, _ = w.Write([]byte(`<rss><channel><item><title>trusted@ PRED-904</title><link>magnet:?xt=urn:btih:abc</link></item></channel></rss>`))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	if err := st.SaveSettings(ctx, map[string]string{
		"accepted_patterns":   "trusted@",
		"search_url_template": server.URL + "/feed?q=<release_id>",
		"ignore_tags":         "Ignored Genre",
	}); err != nil {
		t.Fatal(err)
	}
	site, _ := st.SaveSite(ctx, domain.Site{Title: "Test", Type: "Site", Name: "JavLibrary", Enabled: true})
	if _, err := st.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "PRED-904", Title: "Test", Source: "JavLibrary", Released: true, Genres: []string{"Ignored Genre"}, MonitorDownload: true}); err != nil {
		t.Fatal(err)
	}

	service := New(st, 2*time.Second, slog.Default())
	if err := service.StartSearch(ctx); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for service.SearchStatus().Running && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	status := service.SearchStatus()
	if status.Running {
		t.Fatalf("scheduled search job did not finish in time: %+v", status)
	}
	if feedHits != 0 {
		t.Fatalf("expected runSearch to skip the ignored release before searching, but the feed was hit %d time(s)", feedHits)
	}
	if status.Skipped != 1 || status.Checked != 1 {
		t.Fatalf("expected the ignored release to be checked and skipped, got %+v", status)
	}
}

// TestRunSearchUsesPersistedAllowNonPreferredFlagForFlaggedRelease covers
// the fix for the reported bug: a release recovered via Missing Library
// Files with "allow non-preferred filenames" on (or manually flagged
// through the monitored-releases bulk action) persists that choice on
// domain.Release.AllowNonPreferredFilenames, and the scheduled
// download-search job (runSearch) must honor it the same way
// SearchAndDownloadNow does - applying fallbackSearchCandidate's relaxed
// matching instead of only ever accepting a normal filename-pattern match.
// TestRunSearchSkipsLocalReleaseWithoutIgnoreLocalFlag covers the scheduled
// monitored-search job's default behavior, unchanged: a release already
// linked in StashApp (Local=true) is skipped before ever searching, exactly
// as before IgnoreLocalForceDownload existed, when that flag is not set.
func TestRunSearchSkipsLocalReleaseWithoutIgnoreLocalFlag(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "runsearch-local-noflag.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	feedHits := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/feed", func(w http.ResponseWriter, _ *http.Request) {
		feedHits++
		_, _ = w.Write([]byte(`<rss><channel><item><title>trusted@ PRED-906</title><link>magnet:?xt=urn:btih:abc</link></item></channel></rss>`))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	if err := st.SaveSettings(ctx, map[string]string{
		"accepted_patterns":   "trusted@",
		"search_url_template": server.URL + "/feed?q=<release_id>",
	}); err != nil {
		t.Fatal(err)
	}
	site, _ := st.SaveSite(ctx, domain.Site{Title: "Test", Type: "Site", Name: "JavLibrary", Enabled: true})
	if _, err := st.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "PRED-906", Title: "Test", Source: "JavLibrary", Released: true}); err != nil {
		t.Fatal(err)
	}
	releases, err := st.Releases(ctx, domain.ReleaseFilter{Search: "PRED-906", Limit: 1})
	if err != nil || len(releases) != 1 {
		t.Fatalf("release setup failed: rows=%+v err=%v", releases, err)
	}
	// Simulates a release already matched to a StashApp scene by a prior
	// local-library sync - the ordinary case runMonitoredSearch's early
	// duplicate() check is meant to skip. SetStashState must run before
	// monitor_download is set: it clears monitor_download as a side effect
	// whenever a release transitions from not-local to local (see its own
	// doc comment), matching what a real local-library sync would do to a
	// release nobody has re-monitored yet.
	if err := st.SetStashState(ctx, releases[0].ID, true, "scene-local-noflag"); err != nil {
		t.Fatal(err)
	}
	monitor := true
	if err := st.PatchRelease(ctx, releases[0].ID, nil, nil, nil, nil, nil, &monitor, nil, nil, nil); err != nil {
		t.Fatal(err)
	}

	service := New(st, 2*time.Second, slog.Default())
	if err := service.StartSearch(ctx); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for service.SearchStatus().Running && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	status := service.SearchStatus()
	if status.Running {
		t.Fatalf("scheduled search job did not finish in time: %+v", status)
	}
	if feedHits != 0 {
		t.Fatalf("expected runSearch to skip the already-local release before searching, but the feed was hit %d time(s)", feedHits)
	}
	if status.Skipped != 1 || status.Downloaded != 0 {
		t.Fatalf("expected the already-local release to be skipped, not downloaded, got %+v", status)
	}
}

// TestRunSearchDownloadsLocalReleaseWhenIgnoreLocalFlagSet covers the fix
// for the reported "Missing Library Files" gap: a release already linked in
// StashApp (Local=true) - which normally makes the scheduled job treat it
// as a duplicate and skip it - is downloaded anyway once
// IgnoreLocalForceDownload is set, exactly as runApply now sets it
// automatically for every release it marks monitored (see
// internal/stash/missing.go), or as the "Monitored releases" bulk action
// sets it manually.
func TestRunSearchDownloadsLocalReleaseWhenIgnoreLocalFlagSet(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "runsearch-local-flag.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	feedHits := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/feed", func(w http.ResponseWriter, _ *http.Request) {
		feedHits++
		_, _ = w.Write([]byte(`<rss><channel><item><title>trusted@ PRED-907</title><link>magnet:?xt=urn:btih:fedcba9876543210fedcba9876543210fedcba98</link></item></channel></rss>`))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	qbMux := http.NewServeMux()
	qbMux.HandleFunc("POST /api/v2/auth/login", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("Ok.")) })
	var added bool
	qbMux.HandleFunc("GET /api/v2/torrents/info", func(w http.ResponseWriter, _ *http.Request) {
		if !added {
			_, _ = w.Write([]byte(`[]`))
			return
		}
		_, _ = w.Write([]byte(`[{"hash":"fedcba9876543210fedcba9876543210fedcba98","name":"trusted@ PRED-907"}]`))
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
	site, _ := st.SaveSite(ctx, domain.Site{Title: "Test", Type: "Site", Name: "JavLibrary", Enabled: true})
	if _, err := st.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "PRED-907", Title: "Test", Source: "JavLibrary", Released: true}); err != nil {
		t.Fatal(err)
	}
	releases, err := st.Releases(ctx, domain.ReleaseFilter{Search: "PRED-907", Limit: 1})
	if err != nil || len(releases) != 1 {
		t.Fatalf("release setup failed: rows=%+v err=%v", releases, err)
	}
	// SetStashState before monitor_download/ignoreLocal are set - see the
	// sibling "without flag" test's comment for why the order matters.
	if err := st.SetStashState(ctx, releases[0].ID, true, "scene-local-flag"); err != nil {
		t.Fatal(err)
	}
	monitor, ignore := true, true
	if err := st.PatchRelease(ctx, releases[0].ID, nil, nil, nil, nil, nil, &monitor, nil, nil, &ignore); err != nil {
		t.Fatal(err)
	}

	service := New(st, 2*time.Second, slog.Default())
	if err := service.StartSearch(ctx); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for service.SearchStatus().Running && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	status := service.SearchStatus()
	if status.Running {
		t.Fatalf("scheduled search job did not finish in time: %+v", status)
	}
	if feedHits == 0 {
		t.Fatalf("expected runSearch to still search the flagged release despite it being local, but the feed was never hit")
	}
	if status.Downloaded != 1 {
		t.Fatalf("expected the flagged already-local release to be downloaded anyway, got %+v", status)
	}
	downloads, err := st.Downloads(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	var downloading int
	for _, d := range downloads {
		if d.Status == "downloading" {
			downloading++
		}
	}
	if downloading != 1 {
		t.Fatalf("expected exactly one downloading download row, got %+v", downloads)
	}
}

func TestRunSearchUsesPersistedAllowNonPreferredFlagForFlaggedRelease(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "runsearch-allow-non-preferred.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	mux := http.NewServeMux()
	mux.HandleFunc("/feed", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<rss xmlns:nyaa="https://nyaa.si/xmlns/nyaa"><channel>` +
			`<item><title>rejected@ PRED-905 seeded</title><link>magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567&amp;dn=rejected%40+PRED-905+seeded</link><nyaa:seeders>3</nyaa:seeders></item>` +
			`</channel></rss>`))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	qbMux := http.NewServeMux()
	qbMux.HandleFunc("POST /api/v2/auth/login", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("Ok.")) })
	var added bool
	qbMux.HandleFunc("GET /api/v2/torrents/info", func(w http.ResponseWriter, _ *http.Request) {
		if !added {
			_, _ = w.Write([]byte(`[]`))
			return
		}
		_, _ = w.Write([]byte(`[{"hash":"0123456789abcdef0123456789abcdef01234567","name":"rejected@ PRED-905 seeded"}]`))
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
	site, _ := st.SaveSite(ctx, domain.Site{Title: "Test", Type: "Site", Name: "JavLibrary", Enabled: true})
	if _, err := st.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "PRED-905", Title: "Test", Source: "JavLibrary", Released: true, MonitorDownload: true}); err != nil {
		t.Fatal(err)
	}
	releases, err := st.Releases(ctx, domain.ReleaseFilter{Search: "PRED-905", Limit: 10})
	if err != nil || len(releases) != 1 {
		t.Fatalf("release setup failed: rows=%+v err=%v", releases, err)
	}
	allow := true
	if err := st.PatchRelease(ctx, releases[0].ID, nil, nil, nil, nil, nil, nil, nil, &allow, nil); err != nil {
		t.Fatal(err)
	}

	service := New(st, 2*time.Second, slog.Default())
	if err := service.StartSearch(ctx); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for service.SearchStatus().Running && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	status := service.SearchStatus()
	if status.Running {
		t.Fatalf("scheduled search job did not finish in time: %+v", status)
	}
	if status.Downloaded != 1 {
		t.Fatalf("expected the flagged release's non-preferred match to be downloaded by the scheduled job, got %+v", status)
	}
	downloads, err := st.Downloads(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	var sawExcluded bool
	for _, d := range downloads {
		if d.Status == "downloading" && d.FilenamePatternExcluded {
			sawExcluded = true
		}
	}
	if !sawExcluded {
		t.Fatalf("expected the scheduled job's fallback pick to be recorded as downloading and FilenamePatternExcluded, got %+v", downloads)
	}
}

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

// TestSearchAndDownloadNowIgnoresSiteDownloadGate covers TODO-2.0 Phase 2's
// "Monitor + Download + search" bulk action: unlike Auto, which refuses to
// search/download a release unless one of its sites has the Download flag
// enabled, SearchAndDownloadNow must proceed for a hand-picked release even
// when every attached site has Download disabled - the user is explicitly
// asking for this one, right now.
func TestSearchAndDownloadNowIgnoresSiteDownloadGate(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "search-and-download.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	mux := http.NewServeMux()
	mux.HandleFunc("/feed", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<rss><channel><item><title>trusted@ PRED-901</title><link>magnet:?xt=urn:btih:abc</link></item></channel></rss>`))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	if err := st.SaveSettings(ctx, map[string]string{
		"accepted_patterns":   "trusted@",
		"search_url_template": server.URL + "/feed?q=<release_id>",
	}); err != nil {
		t.Fatal(err)
	}
	// Download disabled: this is exactly the gate Auto would refuse on.
	site, _ := st.SaveSite(ctx, domain.Site{Title: "Test", Type: "Site", Name: "JavLibrary", Enabled: true, Download: false})
	if _, err := st.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "PRED-901", Title: "Test", Source: "JavLibrary", Released: true}); err != nil {
		t.Fatal(err)
	}
	releases, err := st.Releases(ctx, domain.ReleaseFilter{Search: "PRED-901", Limit: 10})
	if err != nil || len(releases) != 1 {
		t.Fatalf("release setup failed: rows=%d err=%v", len(releases), err)
	}

	service := New(st, 2*time.Second, slog.Default())
	accepted, err := service.SearchAndDownloadNow(ctx, releases[0], "Missing Library Recovery", false)
	// qBittorrent is not configured, so the accepted match still fails at the
	// final step - proving the flow reached Download (past the Auto gate that
	// would otherwise have skipped it silently) rather than actually needing
	// a working qBittorrent instance for this test.
	if err == nil {
		t.Fatalf("expected the download to fail at the unconfigured qBittorrent step, got accepted=%v", accepted)
	}
	if accepted {
		t.Fatalf("accepted should be false when Download itself errors")
	}

	downloads, err := st.Downloads(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	var sawAccepted bool
	for _, d := range downloads {
		if d.SourceType == "Missing Library Recovery" && d.Status == "search_accepted" {
			sawAccepted = true
		}
	}
	if !sawAccepted {
		t.Fatalf("expected a search_accepted history row with the caller-supplied trigger label, got %+v", downloads)
	}
}

// TestSearchSortsAcceptedMatchesFirstThenBySeeders covers the Search &
// Download window's default ordering: a release with several candidate
// torrents should surface the one actually likely to finish - accepted by
// the filename patterns, then most seeded - first, rather than whatever
// order the indexer happened to list them in.
func TestSearchSortsAcceptedMatchesFirstThenBySeeders(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "search-sort-order.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	mux := http.NewServeMux()
	mux.HandleFunc("/feed", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<rss xmlns:nyaa="https://nyaa.si/xmlns/nyaa"><channel>` +
			`<item><title>rejected@ SORT-100 low seed rejected</title><link>magnet:?xt=r1</link><nyaa:seeders>99</nyaa:seeders></item>` +
			`<item><title>trusted@ SORT-100 low seed accepted</title><link>magnet:?xt=a-low</link><nyaa:seeders>2</nyaa:seeders></item>` +
			`<item><title>trusted@ SORT-100 high seed accepted</title><link>magnet:?xt=a-high</link><nyaa:seeders>50</nyaa:seeders></item>` +
			`</channel></rss>`))
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
	if _, err := st.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "SORT-100", Title: "Sort order", Source: "JavLibrary"}); err != nil {
		t.Fatal(err)
	}
	releases, err := st.Releases(ctx, domain.ReleaseFilter{Search: "SORT-100", Limit: 1})
	if err != nil || len(releases) != 1 {
		t.Fatalf("release setup failed: rows=%d err=%v", len(releases), err)
	}

	service := New(st, 2*time.Second, slog.Default())
	rows, err := service.Search(ctx, releases[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("expected 3 results, got %+v", rows)
	}
	if !rows[0].Accepted || rows[0].Seeds != 50 {
		t.Fatalf("first result = %+v, want the higher-seeded accepted match first", rows[0])
	}
	if !rows[1].Accepted || rows[1].Seeds != 2 {
		t.Fatalf("second result = %+v, want the lower-seeded accepted match second", rows[1])
	}
	if rows[2].Accepted {
		t.Fatalf("third result = %+v, want the rejected match last despite its higher seed count", rows[2])
	}
}

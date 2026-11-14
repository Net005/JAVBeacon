package stash

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

// Synthetic fixture: five releases and four Stash scenes tagged Watchlist.
func TestWatchlistReportFindsEveryKindOfDifference(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "report.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	site, _ := st.SaveSite(ctx, domain.Site{Title: "Test", Type: "Site", Name: "Test", Enabled: true})
	add := func(id string, watch, local bool, scene string) {
		if _, err := st.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: id, Title: id, Watchlist: watch}); err != nil {
			t.Fatal(err)
		}
		rows, err := st.Releases(ctx, domain.ReleaseFilter{Search: id, Limit: 1})
		if err != nil || len(rows) != 1 {
			t.Fatalf("lookup %s: %v", id, err)
		}
		if local {
			if err := st.SetStashState(ctx, rows[0].ID, true, scene); err != nil {
				t.Fatal(err)
			}
		}
	}
	add("AGREE-1", true, true, "s1")   // watchlist in both
	add("MISSING-2", true, true, "s2") // JAVBeacon only: Stash lacks the tag
	add("PENDING-3", true, false, "")  // watchlist but no linked scene yet
	add("STASHONLY-4", false, true, "s4")
	add("PLAIN-5", false, true, "s5") // not tagged anywhere
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"findScenes":{"scenes":[` +
			`{"id":"s1","updated_at":"2026-01-01T00:00:00Z","tags":[{"id":"wl"}],"files":[{"path":"/m/1.mp4"}]},` +
			`{"id":"s4","updated_at":"2026-01-01T00:00:00Z","tags":[{"id":"wl"}],"files":[{"path":"/m/4.mp4"}]},` +
			`{"id":"s9","updated_at":"2026-01-01T00:00:00Z","tags":[{"id":"wl"}],"files":[{"path":"/m/9.mp4"}]}]}}}`))
	}))
	defer server.Close()
	if err := st.SaveSettings(ctx, map[string]string{"stash_base_url": server.URL, "stash_watchlist_tag_id": "wl"}); err != nil {
		t.Fatal(err)
	}
	svc := New(st, time.Second, slog.Default(), nil, nil)
	got, err := svc.WatchlistReport(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.JAVBeaconWatchlist != 3 || got.StashTagged != 3 || got.InAgreement != 1 {
		t.Fatalf("counts: %+v", got)
	}
	if len(got.MissingInStash) != 1 || got.MissingInStash[0].VideoID != "MISSING-2" {
		t.Fatalf("missing in Stash: %+v", got.MissingInStash)
	}
	if len(got.MissingInJAVBeacon) != 1 || got.MissingInJAVBeacon[0].VideoID != "STASHONLY-4" {
		t.Fatalf("missing in JAVBeacon: %+v", got.MissingInJAVBeacon)
	}
	if got.PendingScene != 1 || got.UnlinkedStashScenes != 1 {
		t.Fatalf("pending/unlinked: %+v", got)
	}
	if got.Consistent {
		t.Fatal("differences must not be reported as consistent")
	}
	after, _ := st.Releases(ctx, domain.ReleaseFilter{Watchlist: true, Limit: 10})
	if len(after) != 3 {
		t.Fatal("the report must not change any release")
	}
}

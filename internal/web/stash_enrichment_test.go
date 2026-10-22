package web

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/Net005/JAVBeacon/internal/domain"
	"github.com/Net005/JAVBeacon/internal/screenshots"
	"github.com/Net005/JAVBeacon/internal/store"
)

func TestStashEnrichmentUsesExactLinkedRelease(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "enrichment.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	site, err := st.SaveSite(ctx, domain.Site{Title: "JavLibrary", URL: "https://example.invalid"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "ATID-705", Title: "Enriched title", Story: "Details", Studio: "Studio", Actress: "Actor", Actresses: []string{"Actor"}, Genres: []string{"Genre"}, ReleaseDate: "2026-10-01", ImageURL: "https://example.invalid/cover.jpg", ProductURL: "https://example.invalid/release", Screenshots: []string{"https://example.invalid/screenshot.jpg"}})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := st.Releases(ctx, domain.ReleaseFilter{VideoID: "ATID-705", Limit: 1})
	if err != nil || len(rows) != 1 {
		t.Fatalf("seed release: %v %v", rows, err)
	}
	if err = st.SetStashState(ctx, rows[0].ID, true, "43250"); err != nil {
		t.Fatal(err)
	}
	cache, err := screenshots.New(filepath.Join(t.TempDir(), "screenshots"), time.Second, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	shotPath := cache.Path("ATID-705", 0)
	if err := os.MkdirAll(filepath.Dir(shotPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(shotPath, []byte("cached screenshot"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := &Server{store: st, screenshots: cache, log: slog.Default()}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/integrations/stash/enrichment/43250", nil)
	req.SetPathValue("sceneId", "43250")
	rec := httptest.NewRecorder()
	s.stashEnrichment(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["scene_id"] != "43250" || got["code"] != "ATID-705" || got["title"] != "Enriched title" || got["poster_path"] != "/covers/"+strconv.FormatInt(rows[0].ID, 10)+"/stash-poster" {
		t.Fatalf("unexpected enrichment: %#v", got)
	}
	backdrops, ok := got["backdrop_paths"].([]any)
	if !ok || len(backdrops) != 2 || backdrops[0] != "/covers/"+strconv.FormatInt(rows[0].ID, 10)+"/original" || backdrops[1] != "/screenshots/"+strconv.FormatInt(rows[0].ID, 10)+"/0" {
		t.Fatalf("unexpected backdrops: %#v", got["backdrop_paths"])
	}
	req.SetPathValue("sceneId", "missing")
	rec = httptest.NewRecorder()
	s.stashEnrichment(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing scene status=%d", rec.Code)
	}
}

// A Stash scene can be linked to the same code from two sites (a studio
// site and an aggregator). Artwork consumers must still get a cover instead
// of a 409, otherwise the poster is never trimmed.
func TestStashEnrichmentPicksOneReleaseWhenSeveralShareAScene(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "enrichment.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ids := []int64{}
	for i, site := range []string{"Studio", "Aggregator"} {
		s, err := st.SaveSite(ctx, domain.Site{Title: site, URL: "https://" + strconv.Itoa(i) + ".example.invalid"})
		if err != nil {
			t.Fatal(err)
		}
		image := "https://example.invalid/" + site + ".jpg"
		if _, err = st.UpsertRelease(ctx, domain.Release{SiteID: s.ID, VideoID: "SYNTH-022", Title: site + " title", ImageURL: image, ScraperID: site}); err != nil {
			t.Fatal(err)
		}
		rows, err := st.Releases(ctx, domain.ReleaseFilter{VideoID: "SYNTH-022", Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range rows {
			if r.SiteID == s.ID {
				ids = append(ids, r.ID)
				if err = st.SetStashState(ctx, r.ID, true, "32595"); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	if len(ids) != 2 {
		t.Fatalf("seeded %v", ids)
	}
	s := &Server{store: st, log: slog.Default()}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/integrations/stash/enrichment/32595", nil)
	req.SetPathValue("sceneId", "32595")
	rec := httptest.NewRecorder()
	s.stashEnrichment(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	first := ids[0]
	if ids[1] < first {
		first = ids[1]
	}
	if got["poster_path"] != "/covers/"+strconv.FormatInt(first, 10)+"/stash-poster" {
		t.Fatalf("expected the oldest linked release's cover, got %#v", got)
	}
}

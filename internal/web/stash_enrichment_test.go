package web

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/Net005/JAVBeacon/internal/domain"
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
	_, err = st.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "ATID-705", Title: "Enriched title", Story: "Details", Studio: "Studio", Actress: "Actor", Actresses: []string{"Actor"}, Genres: []string{"Genre"}, ReleaseDate: "2026-10-01", ImageURL: "https://example.invalid/cover.jpg", ProductURL: "https://example.invalid/release"})
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
	s := &Server{store: st, log: slog.Default()}
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
	req.SetPathValue("sceneId", "missing")
	rec = httptest.NewRecorder()
	s.stashEnrichment(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing scene status=%d", rec.Code)
	}
}

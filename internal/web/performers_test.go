package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/Net005/JAVBeacon/internal/store"
)

func TestPerformerStashRedirectUsesConfiguredStashURL(t *testing.T) {
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "redirect.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.SaveSettings(context.Background(), map[string]string{"stash_base_url": "https://stash.example/"}); err != nil {
		t.Fatal(err)
	}
	s := &Server{store: st}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/integrations/performers/123/stash", nil)
	req.SetPathValue("performerId", "123")
	rec := httptest.NewRecorder()
	s.performerStashRedirect(rec, req)
	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d", rec.Code)
	}
	if got := rec.Header().Get("Location"); got != "https://stash.example/performers/123" {
		t.Fatalf("Location = %q", got)
	}
}

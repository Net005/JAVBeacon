package web

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/Net005/JAVBeacon/internal/domain"
	"github.com/Net005/JAVBeacon/internal/stash"
	"github.com/Net005/JAVBeacon/internal/store"
)

func TestStashRealtimeHookRequiresDedicatedSecret(t *testing.T) {
	st, _ := store.OpenSQLite(filepath.Join(t.TempDir(), "hook.db"))
	defer st.Close()
	_ = st.SaveSettings(context.Background(), map[string]string{"stash_realtime_enabled": "true", "stash_realtime_secret": "hook-secret"})
	s := &Server{
		store: st,
		stash: stash.New(st, time.Second, slog.Default(), nil, nil),
		log:   slog.Default(),
	}
	for _, tc := range []struct {
		secret string
		want   int
	}{{"wrong", http.StatusUnauthorized}, {"hook-secret", http.StatusAccepted}} {
		req := httptest.NewRequest(http.MethodPost, "/api/hooks/stash/scene", bytes.NewBufferString(`{"scene_id":"42","event":"Scene.Update.Post"}`))
		req.Header.Set("Authorization", "Bearer "+tc.secret)
		rec := httptest.NewRecorder()
		s.stashRealtimeEvent(rec, req)
		if rec.Code != tc.want {
			t.Fatalf("secret %q status=%d want=%d body=%s", tc.secret, rec.Code, tc.want, rec.Body.String())
		}
	}
}

func TestStashReleaseLinkResolvesLinkedScene(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "release-link.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err = st.SaveSettings(ctx, map[string]string{"stash_realtime_enabled": "true", "stash_realtime_secret": "hook-secret"}); err != nil {
		t.Fatal(err)
	}
	site, err := st.SaveSite(ctx, domain.Site{Title: "JavLibrary", URL: "https://example.invalid"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = st.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "NSPS-605", Title: "Test"}); err != nil {
		t.Fatal(err)
	}
	releases, err := st.Releases(ctx, domain.ReleaseFilter{VideoID: "NSPS-605", Limit: 1})
	if err != nil || len(releases) != 1 {
		t.Fatalf("seeded release: %#v, %v", releases, err)
	}
	if err = st.SetStashState(ctx, releases[0].ID, true, "39382"); err != nil {
		t.Fatal(err)
	}
	s := &Server{store: st, log: slog.Default()}
	req := httptest.NewRequest(http.MethodPost, "/api/hooks/stash/release-link", bytes.NewBufferString(`{"scene_id":"39382"}`))
	req.Header.Set("Authorization", "Bearer hook-secret")
	rec := httptest.NewRecorder()
	s.stashReleaseLink(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var got struct {
		ReleaseID   int64  `json:"release_id"`
		ReleasePath string `json:"release_path"`
		VideoID     string `json:"video_id"`
	}
	if err = json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.ReleaseID != releases[0].ID || got.ReleasePath != "/release/"+strconv.FormatInt(releases[0].ID, 10) || got.VideoID != "NSPS-605" {
		t.Fatalf("unexpected lookup: %+v", got)
	}
}

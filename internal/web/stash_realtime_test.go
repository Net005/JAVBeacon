package web

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

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

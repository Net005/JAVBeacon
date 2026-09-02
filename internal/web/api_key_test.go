package web

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"

	"github.com/Net005/JAVBeacon/internal/auth"
	"github.com/Net005/JAVBeacon/internal/store"
)

// TestAPIKeyAccessorsReflectLiveUpdates covers the JAVBEACON_API_KEY
// UI-configurability change: Server.key used to be set once at
// construction and read directly; it is now read/written through
// apiKey()/setAPIKey() so a Settings-UI save can change it while the
// server is running, without a restart.
func TestAPIKeyAccessorsReflectLiveUpdates(t *testing.T) {
	s := &Server{key: "initial-key"}
	if got := s.apiKey(); got != "initial-key" {
		t.Fatalf("apiKey() = %q, want %q", got, "initial-key")
	}
	s.setAPIKey("rotated-key")
	if got := s.apiKey(); got != "rotated-key" {
		t.Fatalf("apiKey() after setAPIKey = %q, want %q", got, "rotated-key")
	}
}

// TestAPIKeyAccessorsAreRaceSafe exercises apiKey()/setAPIKey() from
// multiple goroutines concurrently (as the security() middleware reading
// on every request and a Settings save writing would in production) - run
// with -race to catch a regression back to an unsynchronized field.
func TestAPIKeyAccessorsAreRaceSafe(t *testing.T) {
	s := &Server{key: "start"}
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); s.setAPIKey("rotated") }()
		go func() { defer wg.Done(); _ = s.apiKey() }()
	}
	wg.Wait()
}

// TestSecurityMiddlewareHonorsLiveAPIKeyRotation covers the end-to-end
// path: a request authenticated with the old API key must be accepted
// before a rotation and rejected after, while a request carrying the new
// key becomes valid immediately - all without reconstructing the Server,
// matching what happens when the Settings UI saves a new api_key.
func TestSecurityMiddlewareHonorsLiveAPIKeyRotation(t *testing.T) {
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "apikey.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	s := &Server{store: st, auth: auth.New(st), mux: http.NewServeMux(), key: "old-key"}
	s.routes()
	handler := s.security(s.mux)

	req := func(key string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/api/version", nil)
		r.Header.Set("Authorization", "Bearer "+key)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, r)
		return rec
	}

	if rec := req("old-key"); rec.Code != http.StatusOK {
		t.Fatalf("old key before rotation: status=%d, want 200", rec.Code)
	}

	s.setAPIKey("new-key")

	if rec := req("old-key"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("old key after rotation: status=%d, want 401", rec.Code)
	}
	if rec := req("new-key"); rec.Code != http.StatusOK {
		t.Fatalf("new key after rotation: status=%d, want 200", rec.Code)
	}
}

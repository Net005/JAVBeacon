package screenshots

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

func TestCacheStoresScreenshotsSeparatelyAndSkipsExistingFiles(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write([]byte("fake-jpeg"))
	}))
	defer server.Close()

	cache, err := New(t.TempDir(), time.Second, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	urls := []string{server.URL + "/one.jpg", server.URL + "/two.jpg"}
	downloaded, skipped, failed, err := cache.EnsureAll(context.Background(), "FJIN-17", urls)
	if err != nil || downloaded != 2 || skipped != 0 || failed != 0 {
		t.Fatalf("first cache result downloaded=%d skipped=%d failed=%d err=%v", downloaded, skipped, failed, err)
	}
	if !cache.Complete("FJIN-17", urls) {
		t.Fatal("cache should report the release complete")
	}
	if _, err := os.Stat(cache.Path("FJIN-17", 1)); err != nil {
		t.Fatal(err)
	}
	downloaded, skipped, failed, err = cache.EnsureAll(context.Background(), "FJIN-17", urls)
	if err != nil || downloaded != 0 || skipped != 2 || failed != 0 || requests != 2 {
		t.Fatalf("second cache result downloaded=%d skipped=%d failed=%d requests=%d err=%v", downloaded, skipped, failed, requests, err)
	}
}

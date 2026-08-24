package covers

import (
	"context"
	"image"
	"image/color"
	"image/png"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestUnavailableRecognizesNowPrintingLogoAndInvalidatesAfterReplacement(t *testing.T) {
	cache, err := New(t.TempDir(), time.Second, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	path := cache.Path("PENDING-1")
	writeFingerprintImage(t, path, nowPrintingFingerprint[:])
	if !cache.Unavailable(path) {
		t.Fatal("expected the NOW PRINTING visual fingerprint to be recognized")
	}

	realCover := make([]uint8, 64)
	for i := range realCover {
		realCover[i] = uint8(35 + i%8*20)
	}
	writeFingerprintImage(t, path, realCover)
	future := time.Now().Add(time.Second)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatal(err)
	}
	if cache.Unavailable(path) {
		t.Fatal("ordinary replacement artwork must not be treated as unavailable")
	}
}

func writeFingerprintImage(t *testing.T, path string, values []uint8) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 90, 122))
	for y := 0; y < 122; y++ {
		for x := 0; x < 90; x++ {
			v := values[min(y*8/122, 7)*8+min(x*8/90, 7)]
			img.SetRGBA(x, y, color.RGBA{R: v, G: v, B: v, A: 255})
		}
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(f, img); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestRefreshReplacesChangedCoverAndLeavesMatchingCoverAlone(t *testing.T) {
	artwork := []byte("now printing")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(artwork)
	}))
	defer server.Close()

	cache, err := New(t.TempDir(), time.Second, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	path, changed, err := cache.Refresh(context.Background(), "TEST-1", server.URL)
	if err != nil || !changed {
		t.Fatalf("initial refresh: path=%q changed=%v err=%v", path, changed, err)
	}
	if _, changed, err = cache.Refresh(context.Background(), "TEST-1", server.URL); err != nil || changed {
		t.Fatalf("unchanged refresh: changed=%v err=%v", changed, err)
	}

	artwork = []byte("finished release cover")
	if _, changed, err = cache.Refresh(context.Background(), "TEST-1", server.URL); err != nil || !changed {
		t.Fatalf("changed refresh: changed=%v err=%v", changed, err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(artwork) {
		t.Fatalf("cached artwork = %q, want %q", got, artwork)
	}
}

func TestSetDirectoryChangesCoverPathAndCreatesDirectory(t *testing.T) {
	first := filepath.Join(t.TempDir(), "first")
	cache, err := New(first, time.Second, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	if got := cache.Directory(); got != first {
		t.Fatalf("initial directory = %q, want %q", got, first)
	}

	second := filepath.Join(t.TempDir(), "nested", "covers")
	if err := cache.SetDirectory(second); err != nil {
		t.Fatal(err)
	}
	if got := cache.Directory(); got != second {
		t.Fatalf("updated directory = %q, want %q", got, second)
	}
	if got := filepath.Dir(cache.Path("TEST-1")); got != second {
		t.Fatalf("cover path directory = %q, want %q", got, second)
	}
	if info, err := os.Stat(second); err != nil || !info.IsDir() {
		t.Fatalf("configured directory was not created: info=%v err=%v", info, err)
	}
}

func TestSetDirectoryUsesExistingDefaultForBlankValue(t *testing.T) {
	working := t.TempDir()
	oldWorking, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(working); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWorking) })

	cache, err := New("", time.Second, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	if got := cache.Directory(); got != filepath.Clean("data/covers") {
		t.Fatalf("default directory = %q", got)
	}
}

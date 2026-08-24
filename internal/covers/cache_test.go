package covers

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

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

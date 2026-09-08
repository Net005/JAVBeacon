package covers

import (
	"bytes"
	"image"
	"image/jpeg"
	"math"
	"os"
	"path/filepath"
	"testing"
)

func TestGIGACoverShape(t *testing.T) {
	spread := syntheticSpread(800, 536)
	singlePanel := syntheticSpread(480, 680)
	tooWide := syntheticSpread(500, 400) // ratio 1.25 - between the two known shapes
	cases := []struct {
		name string
		url  string
		img  image.Image
		want string
	}{
		{"giga type 1 spread matches", "https://www.akiba-web.com/cover.jpg", spread, "spread"},
		{"giga type 2 single panel matches", "https://www.akiba-web.com/cover.jpg", singlePanel, "single_panel"},
		{"non-giga source untouched", "https://pics.javlibrary.com/8/abc12345.jpg", spread, ""},
		{"giga source but ratio matches neither shape", "https://www.akiba-web.com/cover.jpg", tooWide, ""},
		{"giga type 1 near-reference size still matches", "https://www.akiba-web.com/cover.jpg", syntheticSpread(1600, 1072), "spread"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := gigaCoverShape(tc.url, tc.img); got != tc.want {
				t.Fatalf("gigaCoverShape(%q) = %q, want %q", tc.url, got, tc.want)
			}
		})
	}
}

func TestConformGIGACoverProducesExactPosterSize(t *testing.T) {
	cases := []struct {
		name  string
		img   *image.RGBA
		shape string
	}{
		{"type 1 spread", syntheticSpread(800, 536), "spread"},
		{"type 2 single panel", syntheticSpread(480, 680), "single_panel"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := conformGIGACover(tc.img, tc.shape)
			if len(out) == 0 {
				t.Fatal("conformGIGACover returned no bytes")
			}
			img, _, err := image.Decode(bytes.NewReader(out))
			if err != nil {
				t.Fatalf("decode conformed image: %v", err)
			}
			b := img.Bounds()
			if b.Dx() != jellyfinPosterWidth || b.Dy() != jellyfinPosterHeight {
				t.Fatalf("got %dx%d, want %dx%d", b.Dx(), b.Dy(), jellyfinPosterWidth, jellyfinPosterHeight)
			}
		})
	}
}

func TestConformGIGASpreadKeepsFrontPanelOnly(t *testing.T) {
	spread := syntheticSpread(800, 536)
	front := sliceFrontPanel(spread, gigaFrontFraction)
	b := front.Bounds()
	wantWidth := int(math.Round(800 * gigaFrontFraction))
	if b.Dx() != wantWidth {
		t.Fatalf("front panel width = %d, want %d", b.Dx(), wantWidth)
	}
	// Every pixel should have come from the right (colorful) half, never the
	// dark left half.
	r, _, _, _ := front.At(0, 0).RGBA()
	if r>>8 < 50 {
		t.Fatalf("front panel leaked left-half pixels: r=%d", r>>8)
	}
}

func TestConformCoverFileForServingPreservesOriginalForMatchingGIGACover(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cover.img")
	originalPath := filepath.Join(dir, "cover.orig.img")

	spread := syntheticSpread(800, 536)
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := jpeg.Encode(f, spread, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
	f.Close()
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	if !conformCoverFileForServing(path, originalPath, "https://www.akiba-web.com/cover.jpg") {
		t.Fatal("expected ok=true for a GIGA type 1 spread cover")
	}

	// path must now hold the conformed poster, at the exact Jellyfin size.
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	img, _, err := image.Decode(bytes.NewReader(after))
	if err != nil {
		t.Fatalf("decode conformed image: %v", err)
	}
	b := img.Bounds()
	if b.Dx() != jellyfinPosterWidth || b.Dy() != jellyfinPosterHeight {
		t.Fatalf("got %dx%d, want %dx%d", b.Dx(), b.Dy(), jellyfinPosterWidth, jellyfinPosterHeight)
	}

	// originalPath must hold the exact untouched bytes that were downloaded,
	// so a Backdrop request always has a non-cropped version available.
	original, err := os.ReadFile(originalPath)
	if err != nil {
		t.Fatalf("read originalPath: %v", err)
	}
	if string(original) != string(before) {
		t.Fatal("originalPath does not match the untouched pre-conform bytes")
	}
}

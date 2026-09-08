package covers

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"math"
	"os"
	"path/filepath"
	"testing"
)

func syntheticSpread(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			// left half looks different from right half so slicing is
			// verifiable, and rows vary so blur/pad changes are visible.
			if x < w/2 {
				img.Set(x, y, color.RGBA{R: 10, G: 10, B: 10, A: 255})
			} else {
				img.Set(x, y, color.RGBA{R: 200, G: uint8(y % 256), B: 50, A: 255})
			}
		}
	}
	return img
}

func TestIsJavLibrarySpreadCover(t *testing.T) {
	spread := syntheticSpread(800, 538)
	square := syntheticSpread(400, 400)
	cases := []struct {
		name string
		url  string
		img  image.Image
		want bool
	}{
		{"javlibrary spread matches", "https://pics.javlibrary.com/8/abc12345.jpg", spread, true},
		{"non-javlibrary source untouched", "https://www.akiba-web.com/cover.jpg", spread, false},
		{"javlibrary but wrong ratio untouched", "https://pics.javlibrary.com/8/abc12345.jpg", square, false},
		{"javlibrary near-reference size still matches", "https://pics.javlibrary.com/8/abc12345.jpg", syntheticSpread(1600, 1080), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isJavLibrarySpreadCover(tc.url, tc.img); got != tc.want {
				t.Fatalf("isJavLibrarySpreadCover(%q) = %v, want %v", tc.url, got, tc.want)
			}
		})
	}
}

func TestConformJavLibraryCoverProducesExactPosterSize(t *testing.T) {
	spread := syntheticSpread(800, 538)
	out := conformJavLibraryCover(spread)
	if len(out) == 0 {
		t.Fatal("conformJavLibraryCover returned no bytes")
	}
	img, _, err := image.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("decode conformed image: %v", err)
	}
	b := img.Bounds()
	if b.Dx() != jellyfinPosterWidth || b.Dy() != jellyfinPosterHeight {
		t.Fatalf("got %dx%d, want %dx%d", b.Dx(), b.Dy(), jellyfinPosterWidth, jellyfinPosterHeight)
	}
}

func TestSliceJavLibraryFrontPanelKeepsRightPortion(t *testing.T) {
	spread := syntheticSpread(800, 538)
	front := sliceFrontPanel(spread, javLibraryFrontFraction)
	b := front.Bounds()
	wantWidth := int(math.Round(800 * javLibraryFrontFraction))
	if b.Dx() != wantWidth {
		t.Fatalf("front panel width = %d, want %d", b.Dx(), wantWidth)
	}
	if b.Dy() != 538 {
		t.Fatalf("front panel height = %d, want 538 (no vertical crop)", b.Dy())
	}
	// Every pixel should have come from the right (colorful) half, never the
	// dark left half.
	r, _, _, _ := front.At(0, 0).RGBA()
	if r>>8 < 50 {
		t.Fatalf("front panel leaked left-half pixels: r=%d", r>>8)
	}
}

func TestConformForServingLeavesNonMatchingImageUntouchedOnDisk(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cover.img")
	// A perfectly square cover from a GIGA source matches neither JavLibrary's
	// shape (wrong source) nor either known GIGA shape (spread ratio is way
	// off, and 1.0 falls outside the single-panel band), so it must never be
	// touched.
	square := syntheticSpread(500, 500)
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := jpeg.Encode(f, square, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
	f.Close()
	before, _ := os.ReadFile(path)

	if _, ok := ConformForServing(path, "https://www.akiba-web.com/cover.jpg"); ok {
		t.Fatal("expected ok=false for a GIGA source that doesn't match either known shape")
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("file was modified even though source did not match - ConformForServing must never write to disk")
	}
}

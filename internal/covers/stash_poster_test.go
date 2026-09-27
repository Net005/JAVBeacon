package covers

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"testing"
)

func TestConformStashPoster(t *testing.T) {
	for _, size := range []image.Point{{800, 535}, {800, 538}} {
		src := image.NewRGBA(image.Rect(0, 0, size.X, size.Y))
		for y := 0; y < size.Y; y++ {
			for x := 0; x < size.X; x++ {
				if x < 420 {
					src.Set(x, y, color.RGBA{R: 20, A: 255})
				} else {
					src.Set(x, y, color.RGBA{G: 220, A: 255})
				}
			}
		}
		var buf bytes.Buffer
		if err := jpeg.Encode(&buf, src, nil); err != nil {
			t.Fatal(err)
		}
		out, ok := ConformStashPoster(buf.Bytes())
		if !ok {
			t.Fatalf("%v not transformed", size)
		}
		poster, _, err := image.Decode(bytes.NewReader(out))
		if err != nil {
			t.Fatal(err)
		}
		if poster.Bounds().Dx() != 1000 || poster.Bounds().Dy() != 1500 {
			t.Fatalf("size %v", poster.Bounds())
		}
		red, green, _, _ := poster.At(500, 750).RGBA()
		if green <= red {
			t.Fatalf("front panel not selected for %v", size)
		}
	}
	src := image.NewRGBA(image.Rect(0, 0, 800, 450))
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, src, nil); err != nil {
		t.Fatal(err)
	}
	if _, ok := ConformStashPoster(buf.Bytes()); ok {
		t.Fatal("ordinary widescreen screenshot should stay unchanged")
	}
}

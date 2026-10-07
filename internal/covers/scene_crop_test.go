package covers

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"testing"
)

func TestSceneCropRejectsInvalidAndOversizedSourcesBeforeLaunching(t *testing.T) {
	for _, raw := range [][]byte{[]byte("invalid"), make([]byte, (16<<20)+1)} {
		if _, err := RenderSceneCrop(context.Background(), raw); err == nil {
			t.Fatal("accepted invalid source")
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	if _, err := RenderSceneCrop(context.Background(), b.Bytes()); err == nil {
		t.Fatal("accepted image smaller than portrait crop")
	}
}

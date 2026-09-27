package covers

import (
	"bytes"
	"image"
	"math"
)

// ConformStashPoster selects the front panel from a two-panel Stash scene
// cover. The original bytes remain available for backdrop use. Other image
// shapes are returned unchanged so ordinary screenshots are never sliced.
func ConformStashPoster(raw []byte) ([]byte, bool) {
	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, false
	}
	b := img.Bounds()
	if b.Dx() == 0 || b.Dy() == 0 {
		return nil, false
	}
	ratio := float64(b.Dx()) / float64(b.Dy())
	if math.Abs(ratio-javLibrarySpreadRatio) > javLibrarySpreadRatio*0.06 {
		return nil, false
	}
	out := encodeConformedPoster(sliceFrontPanel(img, javLibraryFrontFraction))
	return out, len(out) > 0
}

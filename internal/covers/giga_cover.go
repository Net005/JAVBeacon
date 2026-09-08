package covers

import (
	"image"
	"math"
	"strings"
)

// GIGA's own site (Akiba-Web, JAVBeacon's native scraper) serves release art
// in two different shapes depending on how old the release is:
//
//   - "Type 1" (older releases): the same kind of two-panel spread
//     JavLibrary serves - back cover/text on the left, front cover on the
//     right - just at a very slightly different reference ratio.
//   - "Type 2" (newer releases): already a single front-cover panel, just
//     somewhat wider-per-height than Jellyfin's 2:3 poster ratio, needing
//     only the same pad-and-resize step JavLibrary's sliced panel gets, with
//     no slicing at all.
//
// Both are gated on the source URL being Akiba-Web, exactly like
// isJavLibrarySpreadCover gates on "javlibrary" - every other source passes
// through untouched.

// gigaSpreadRatio is the width/height ratio of GIGA's older two-panel Type 1
// scan, derived from a reference 800x536 example release cover.
const gigaSpreadRatio = 800.0 / 536.0

// gigaSpreadRatioTolerance mirrors javLibrarySpreadRatioTolerance: real
// scrapes vary somewhat from the reference pixel size while still being the
// same two-panel shape.
const gigaSpreadRatioTolerance = 0.12

// gigaFrontFraction is the fraction of a Type 1 spread's total width that
// belongs to the front-cover panel on the right. Measured independently
// from JavLibrary's reference image, it happens to land on the same
// fraction, but is kept as its own named constant so the two sources can be
// retuned independently if a future example shows they should differ.
const gigaFrontFraction = 379.0 / 800.0

// gigaSinglePanelRatioMin and gigaSinglePanelRatioMax bound the portrait
// aspect ratios recognized as a Type 2 single-panel cover, derived from a
// reference 480x680 example (ratio ~0.706). The band is wide enough to
// cover minor size variation between scrapes while staying clear of
// gigaSpreadRatio's landscape shape entirely.
const (
	gigaSinglePanelRatioMin = 0.55
	gigaSinglePanelRatioMax = 0.95
)

// isGIGASource reports whether sourceURL - the release's own product/
// detail-page URL, not the cover image's hosting URL, since GIGA's actual
// cover images are hosted on giga-web.jp rather than akiba-web.com - is a
// GIGA/Akiba-Web release.
func isGIGASource(sourceURL string) bool {
	return strings.Contains(strings.ToLower(sourceURL), "akiba-web")
}

// gigaCoverShape classifies a GIGA-sourced cover as "spread" (Type 1,
// needs slicing), "single_panel" (Type 2, pad-and-resize only), or ""
// (leave untouched - not a GIGA source, or its shape does not match either
// known type).
func gigaCoverShape(sourceURL string, img image.Image) string {
	if !isGIGASource(sourceURL) {
		return ""
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w == 0 || h == 0 {
		return ""
	}
	ratio := float64(w) / float64(h)
	switch {
	case math.Abs(ratio-gigaSpreadRatio) <= gigaSpreadRatio*gigaSpreadRatioTolerance:
		return "spread"
	case ratio >= gigaSinglePanelRatioMin && ratio <= gigaSinglePanelRatioMax:
		return "single_panel"
	default:
		return ""
	}
}

// conformGIGACover conforms img to Jellyfin's 1000x1500 poster size
// according to shape ("spread" or "single_panel"), encoded as JPEG.
func conformGIGACover(img image.Image, shape string) []byte {
	if shape == "spread" {
		img = sliceFrontPanel(img, gigaFrontFraction)
	}
	return encodeConformedPoster(img)
}

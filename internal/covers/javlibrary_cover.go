package covers

import (
	"bytes"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"math"
	"strings"
)

// JavLibrary serves its release art as a single two-panel scan: a back-cover
// panel and text on the left, a thin spine, and the actual front-cover
// artwork on the right. Jellyfin's Primary/Poster/Cover image wants just the
// front cover at a 1000x1500 (2:3) size, so JavLibrary-sourced covers whose
// shape matches that two-panel spread are sliced down to the front panel and
// conformed to that size before being cached. Every other source (GIGA,
// PikPak restores, manually set covers, an already-single-panel JavLibrary
// image, etc.) passes through untouched.

// javLibrarySpreadRatio is the width/height ratio of JavLibrary's two-panel
// scan, derived from a reference 800x538 example release cover.
const javLibrarySpreadRatio = 800.0 / 538.0

// javLibrarySpreadRatioTolerance allows real scrapes to vary somewhat from
// the reference pixel size while still being recognized as the same
// two-panel shape ("this dimension or very close").
const javLibrarySpreadRatioTolerance = 0.12

// javLibraryFrontFraction is the fraction of the spread's total width that
// belongs to the front-cover panel on the right, measured from the same
// 800x538 reference image (a 379px-wide front panel out of 800px total). It
// is kept as a ratio, not a fixed pixel count, so it scales to whatever
// resolution a given scrape actually serves.
const javLibraryFrontFraction = 379.0 / 800.0

const (
	jellyfinPosterWidth  = 1000
	jellyfinPosterHeight = 1500
)

var jellyfinPosterRatio = float64(jellyfinPosterWidth) / float64(jellyfinPosterHeight)

// isJavLibrarySpreadCover reports whether sourceURL is a JavLibrary cover
// and img's aspect ratio is close enough to the known two-panel spread shape
// to safely slice down to the front panel.
func isJavLibrarySpreadCover(sourceURL string, img image.Image) bool {
	if !strings.Contains(strings.ToLower(sourceURL), "javlibrary") {
		return false
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w == 0 || h == 0 {
		return false
	}
	ratio := float64(w) / float64(h)
	return math.Abs(ratio-javLibrarySpreadRatio) <= javLibrarySpreadRatio*javLibrarySpreadRatioTolerance
}

// conformJavLibraryCover slices the front-cover panel out of a JavLibrary
// two-panel release spread and pads (never further crops) it to Jellyfin's
// required Primary/Poster/Cover size of 1000x1500 (2:3), encoded as JPEG.
func conformJavLibraryCover(img image.Image) []byte {
	front := sliceFrontPanel(img, javLibraryFrontFraction)
	return encodeConformedPoster(front)
}

// encodeConformedPoster pads (never crops) img to Jellyfin's required
// Primary/Poster/Cover ratio, resizes it to the exact 1000x1500 size, and
// encodes it as JPEG. Shared by every source's conform path (JavLibrary and
// both GIGA cover shapes).
func encodeConformedPoster(img image.Image) []byte {
	padded := padToRatio(img, jellyfinPosterRatio)
	resized := resizeBilinear(padded, jellyfinPosterWidth, jellyfinPosterHeight)
	var buf bytes.Buffer
	// Quality 92 keeps poster art visually lossless-ish while staying well
	// under maxCoverSize even for the largest reasonable source scans.
	if err := jpeg.Encode(&buf, resized, &jpeg.Options{Quality: 92}); err != nil {
		return nil
	}
	return buf.Bytes()
}

// sliceFrontPanel returns the rightmost fraction of img's width, at full
// height - the shape shared by JavLibrary's two-panel scan and GIGA's older
// "Type 1" spread covers (back cover/text, spine, front cover on the
// right).
func sliceFrontPanel(img image.Image, fraction float64) image.Image {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	frontWidth := int(math.Round(float64(w) * fraction))
	if frontWidth < 1 {
		frontWidth = 1
	}
	if frontWidth > w {
		frontWidth = w
	}
	src := image.Rect(b.Max.X-frontWidth, b.Min.Y, b.Max.X, b.Max.Y)
	out := image.NewRGBA(image.Rect(0, 0, frontWidth, h))
	draw.Draw(out, out.Bounds(), img, src.Min, draw.Src)
	return out
}

// padToRatio pads img with a soft blurred extension of its own edge pixels,
// growing whichever dimension is short, until it matches targetRatio
// (width/height). It never crops - only ever adds pixels.
func padToRatio(img image.Image, targetRatio float64) image.Image {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w == 0 || h == 0 {
		return img
	}
	ratio := float64(w) / float64(h)
	switch {
	case ratio > targetRatio+1e-9:
		newH := int(math.Round(float64(w) / targetRatio))
		return padVertical(toRGBA(img), newH)
	case ratio < targetRatio-1e-9:
		newW := int(math.Round(float64(h) * targetRatio))
		return padHorizontal(toRGBA(img), newW)
	default:
		return img
	}
}

func toRGBA(img image.Image) *image.RGBA {
	if rgba, ok := img.(*image.RGBA); ok {
		return rgba
	}
	b := img.Bounds()
	out := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(out, out.Bounds(), img, b.Min, draw.Src)
	return out
}

// padVertical grows img to newH by adding a blurred stretch of its own top
// and bottom edge rows above and below the original content.
func padVertical(img *image.RGBA, newH int) *image.RGBA {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if newH <= h {
		return img
	}
	total := newH - h
	top := total / 2
	bottom := total - top

	out := image.NewRGBA(image.Rect(0, 0, w, newH))
	if top > 0 {
		strip := edgeStrip(img, w, top, true)
		draw.Draw(out, image.Rect(0, 0, w, top), strip, image.Point{}, draw.Src)
	}
	draw.Draw(out, image.Rect(0, top, w, top+h), img, b.Min, draw.Src)
	if bottom > 0 {
		strip := edgeStrip(img, w, bottom, false)
		draw.Draw(out, image.Rect(0, top+h, w, newH), strip, image.Point{}, draw.Src)
	}
	return out
}

// padHorizontal grows img to newW by adding a blurred stretch of its own
// left and right edge columns beside the original content.
func padHorizontal(img *image.RGBA, newW int) *image.RGBA {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if newW <= w {
		return img
	}
	total := newW - w
	left := total / 2
	right := total - left

	out := image.NewRGBA(image.Rect(0, 0, newW, h))
	if left > 0 {
		strip := rotate90(edgeStrip(rotate90(img), h, left, true))
		draw.Draw(out, image.Rect(0, 0, left, h), strip, image.Point{}, draw.Src)
	}
	draw.Draw(out, image.Rect(left, 0, left+w, h), img, b.Min, draw.Src)
	if right > 0 {
		strip := rotate90(edgeStrip(rotate90(img), h, right, false))
		draw.Draw(out, image.Rect(left+w, 0, newW, h), strip, image.Point{}, draw.Src)
	}
	return out
}

// rotate90 rotates img 90 degrees clockwise, letting padHorizontal reuse the
// same edge-strip-and-blur logic padVertical uses.
func rotate90(img *image.RGBA) *image.RGBA {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	out := image.NewRGBA(image.Rect(0, 0, h, w))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			out.Set(h-1-y, x, img.At(b.Min.X+x, b.Min.Y+y))
		}
	}
	return out
}

// edgeStrip builds a width x length fill by stretching img's top-most (or
// bottom-most, when !fromTop) row across length pixels and blurring it, so a
// pad region reads as a soft continuation of the cover's edge rather than a
// hard bar.
func edgeStrip(img *image.RGBA, width, length int, fromTop bool) *image.RGBA {
	b := img.Bounds()
	w := b.Dx()
	edgeY := b.Min.Y
	if !fromTop {
		edgeY = b.Max.Y - 1
	}
	out := image.NewRGBA(image.Rect(0, 0, width, length))
	for y := 0; y < length; y++ {
		for x := 0; x < width; x++ {
			sx := b.Min.X
			if w > 1 {
				sx = b.Min.X + x*w/width
			}
			out.Set(x, y, img.At(sx, edgeY))
		}
	}
	return boxBlur(out, 6, 2)
}

// boxBlur applies a separable box blur of the given radius, repeated pass
// times (a small number of box-blur passes approximates a Gaussian blur
// closely enough for a soft pad fill, without pulling in an extra
// dependency).
func boxBlur(img *image.RGBA, radius, passes int) *image.RGBA {
	cur := img
	for i := 0; i < passes; i++ {
		cur = boxBlurPass(cur, radius, true)
		cur = boxBlurPass(cur, radius, false)
	}
	return cur
}

func boxBlurPass(img *image.RGBA, radius int, horizontal bool) *image.RGBA {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	out := image.NewRGBA(b)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var rSum, gSum, bSum, aSum, count uint32
			if horizontal {
				for dx := -radius; dx <= radius; dx++ {
					sx := clampInt(x+dx, 0, w-1)
					r, g, bl, a := img.RGBAAt(b.Min.X+sx, b.Min.Y+y).RGBA()
					rSum += r >> 8
					gSum += g >> 8
					bSum += bl >> 8
					aSum += a >> 8
					count++
				}
			} else {
				for dy := -radius; dy <= radius; dy++ {
					sy := clampInt(y+dy, 0, h-1)
					r, g, bl, a := img.RGBAAt(b.Min.X+x, b.Min.Y+sy).RGBA()
					rSum += r >> 8
					gSum += g >> 8
					bSum += bl >> 8
					aSum += a >> 8
					count++
				}
			}
			out.SetRGBA(b.Min.X+x, b.Min.Y+y, color.RGBA{
				R: uint8(rSum / count),
				G: uint8(gSum / count),
				B: uint8(bSum / count),
				A: uint8(aSum / count),
			})
		}
	}
	return out
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// resizeBilinear resizes src to exactly dstW x dstH using bilinear
// interpolation.
func resizeBilinear(src image.Image, dstW, dstH int) *image.RGBA {
	b := src.Bounds()
	srcW, srcH := b.Dx(), b.Dy()
	dst := image.NewRGBA(image.Rect(0, 0, dstW, dstH))
	if srcW == 0 || srcH == 0 || dstW == 0 || dstH == 0 {
		return dst
	}
	xRatio := float64(srcW) / float64(dstW)
	yRatio := float64(srcH) / float64(dstH)
	for y := 0; y < dstH; y++ {
		sy := (float64(y)+0.5)*yRatio - 0.5
		y0 := int(math.Floor(sy))
		fy := sy - float64(y0)
		y1 := clampInt(y0+1, 0, srcH-1)
		y0 = clampInt(y0, 0, srcH-1)
		for x := 0; x < dstW; x++ {
			sx := (float64(x)+0.5)*xRatio - 0.5
			x0 := int(math.Floor(sx))
			fx := sx - float64(x0)
			x1 := clampInt(x0+1, 0, srcW-1)
			x0 = clampInt(x0, 0, srcW-1)

			c00 := src.At(b.Min.X+x0, b.Min.Y+y0)
			c10 := src.At(b.Min.X+x1, b.Min.Y+y0)
			c01 := src.At(b.Min.X+x0, b.Min.Y+y1)
			c11 := src.At(b.Min.X+x1, b.Min.Y+y1)
			dst.Set(x, y, bilerp(c00, c10, c01, c11, fx, fy))
		}
	}
	return dst
}

func bilerp(c00, c10, c01, c11 color.Color, fx, fy float64) color.RGBA {
	r00, g00, b00, a00 := c00.RGBA()
	r10, g10, b10, a10 := c10.RGBA()
	r01, g01, b01, a01 := c01.RGBA()
	r11, g11, b11, a11 := c11.RGBA()

	lerp := func(a, b uint32, f float64) float64 { return float64(a) + (float64(b)-float64(a))*f }
	rTop, rBot := lerp(r00, r10, fx), lerp(r01, r11, fx)
	gTop, gBot := lerp(g00, g10, fx), lerp(g01, g11, fx)
	bTop, bBot := lerp(b00, b10, fx), lerp(b01, b11, fx)
	aTop, aBot := lerp(a00, a10, fx), lerp(a01, a11, fx)

	r := lerp(uint32(rTop), uint32(rBot), fy)
	g := lerp(uint32(gTop), uint32(gBot), fy)
	bl := lerp(uint32(bTop), uint32(bBot), fy)
	a := lerp(uint32(aTop), uint32(aBot), fy)
	return color.RGBA{R: uint8(uint32(r) >> 8), G: uint8(uint32(g) >> 8), B: uint8(uint32(bl) >> 8), A: uint8(uint32(a) >> 8)}
}

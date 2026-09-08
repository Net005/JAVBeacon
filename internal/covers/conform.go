package covers

import (
	"bytes"
	"image"
	"os"
)

// ConformForServing reads the raw, as-downloaded cover at path and, if it
// matches a known JavLibrary or GIGA cover shape, returns it sliced/padded
// to Jellyfin's 1000x1500 Primary/Poster/Cover size, re-encoded as JPEG.
//
// sourceURL identifies which scraper/site the release came from, and must
// be the release's own product/detail-page URL (domain.Release.ProductURL:
// javlibrary.com for JavLibrary, akiba-web.com for GIGA) - never the cover
// image's own hosting URL. JavLibrary often hotlinks a release's cover from
// DMM's CDN (pics.dmm.co.jp) instead of hosting it itself, and GIGA's own
// covers are hosted on giga-web.jp rather than akiba-web.com, so gating on
// where the image happens to be hosted misses real matches; the release's
// product URL is what actually says which site scraped it.
//
// This is computed entirely in memory on every call and never writes
// anything back to path or anywhere else on disk - the cache on disk always
// stays exactly what was downloaded, and conforming only happens live, at
// the moment a caller (Jellyfin) actually asks for the Primary image. That
// keeps a single cached file serving both purposes: unconformed for the
// Backdrop image, conformed for Primary, with nothing to keep in sync and
// nothing that can go stale relative to a newer conforming pipeline.
//
// It reports ok=false for any non-matching or undecodable image, so every
// other source - GIGA covers that are neither known shape, PikPak restores,
// manually set covers, an already-single-panel JavLibrary image, etc. -
// should just be served as downloaded.
func ConformForServing(path, sourceURL string) (conformed []byte, ok bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	img, _, decodeErr := image.Decode(bytes.NewReader(raw))
	if decodeErr != nil {
		return nil, false
	}

	switch {
	case isJavLibrarySpreadCover(sourceURL, img):
		conformed = conformJavLibraryCover(img)
	default:
		if shape := gigaCoverShape(sourceURL, img); shape != "" {
			conformed = conformGIGACover(img, shape)
		}
	}
	if len(conformed) == 0 {
		return nil, false
	}
	return conformed, true
}

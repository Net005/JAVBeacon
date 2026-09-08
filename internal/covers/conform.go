package covers

import (
	"bytes"
	"image"
	"os"
)

// conformCoverFileForServing reads the just-downloaded image at path and, if
// it matches a known JavLibrary or GIGA cover shape, conforms it to
// Jellyfin's 1000x1500 Primary/Poster/Cover size:
//
//   - The untouched original bytes are preserved at originalPath first, so
//     a caller that specifically wants the non-cropped/non-padded cover
//     (Jellyfin's Backdrop image, in particular - a cropped poster makes a
//     poor background) always has it available, even though the standard
//     cover file is about to become the conformed version.
//   - path is then overwritten with the conformed, re-encoded JPEG.
//
// It reports ok=false (leaving path, and any existing originalPath, alone)
// for any non-matching or undecodable image, so every other source - GIGA
// covers that are neither known shape, PikPak restores, manually set
// covers, an already-single-panel JavLibrary image, etc. - passes through
// exactly as downloaded and never gets an originalPath file at all.
func conformCoverFileForServing(path, originalPath, sourceURL string) (ok bool) {
	original, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	img, _, decodeErr := image.Decode(bytes.NewReader(original))
	if decodeErr != nil {
		return false
	}

	var conformed []byte
	switch {
	case isJavLibrarySpreadCover(sourceURL, img):
		conformed = conformJavLibraryCover(img)
	default:
		if shape := gigaCoverShape(sourceURL, img); shape != "" {
			conformed = conformGIGACover(img, shape)
		}
	}
	if len(conformed) == 0 {
		return false
	}

	// Preserve the original before overwriting the standard path, so a
	// backdrop request still has something non-cropped to fall back to.
	if err := os.WriteFile(originalPath, original, 0o644); err != nil {
		return false
	}
	if err := os.WriteFile(path, conformed, 0o644); err != nil {
		return false
	}
	return true
}

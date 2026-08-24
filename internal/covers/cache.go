package covers

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/color"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const maxCoverSize = 20 << 20

type Cache struct {
	dirMu       sync.RWMutex
	dir         string
	client      *http.Client
	log         *slog.Logger
	artworkMu   sync.Mutex
	artworkScan map[string]artworkScan
}

type artworkScan struct {
	size        int64
	modified    time.Time
	unavailable bool
}

func New(dir string, timeout time.Duration, log *slog.Logger) (*Cache, error) {
	cache := &Cache{client: &http.Client{Timeout: timeout}, log: log, artworkScan: map[string]artworkScan{}}
	if err := cache.SetDirectory(dir); err != nil {
		return nil, err
	}
	return cache, nil
}

// Unavailable reports whether a cached image matches JavLibrary's small,
// grayscale "NOW PRINTING" document logo. It uses a tolerant visual
// fingerprint rather than a URL or exact-file hash because the same logo can
// be resized or recompressed by the upstream site. Results are cached until
// the cover file's size or modification time changes.
func (c *Cache) Unavailable(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	c.artworkMu.Lock()
	if scan, ok := c.artworkScan[path]; ok && scan.size == info.Size() && scan.modified.Equal(info.ModTime()) {
		c.artworkMu.Unlock()
		return scan.unavailable
	}
	c.artworkMu.Unlock()

	f, err := os.Open(path)
	if err != nil {
		return false
	}
	img, _, decodeErr := image.Decode(f)
	_ = f.Close()
	unavailable := decodeErr == nil && resemblesNowPrinting(img)
	c.artworkMu.Lock()
	c.artworkScan[path] = artworkScan{size: info.Size(), modified: info.ModTime(), unavailable: unavailable}
	c.artworkMu.Unlock()
	return unavailable
}

var nowPrintingFingerprint = [...]uint8{
	255, 255, 255, 255, 255, 255, 255, 255,
	255, 222, 239, 241, 240, 222, 222, 255,
	255, 224, 243, 243, 242, 241, 224, 255,
	255, 228, 244, 244, 244, 244, 228, 255,
	254, 233, 238, 238, 238, 238, 233, 255,
	255, 240, 240, 243, 167, 240, 240, 254,
	255, 249, 247, 251, 247, 216, 238, 255,
	255, 255, 255, 255, 255, 255, 255, 255,
}

func resemblesNowPrinting(img image.Image) bool {
	bounds := img.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	if w < 24 || h < 32 || float64(w)/float64(h) < 0.62 || float64(w)/float64(h) > 0.84 {
		return false
	}
	var squaredError, brightness, colorful int64
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			px := bounds.Min.X + (2*x+1)*w/16
			py := bounds.Min.Y + (2*y+1)*h/16
			r16, g16, b16, _ := img.At(px, py).RGBA()
			r, g, b := int(r16>>8), int(g16>>8), int(b16>>8)
			if max(r, g, b)-min(r, g, b) > 12 {
				colorful++
			}
			gray := int(color.GrayModel.Convert(img.At(px, py)).(color.Gray).Y)
			delta := gray - int(nowPrintingFingerprint[y*8+x])
			squaredError += int64(delta * delta)
			brightness += int64(gray)
		}
	}
	return colorful <= 2 && brightness/64 >= 220 && squaredError/64 <= 18*18
}

func (c *Cache) SetDirectory(dir string) error {
	if strings.TrimSpace(dir) == "" {
		dir = "data/covers"
	}
	dir = filepath.Clean(strings.TrimSpace(dir))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create cover cache: %w", err)
	}
	c.dirMu.Lock()
	c.dir = dir
	c.dirMu.Unlock()
	return nil
}

func (c *Cache) Directory() string {
	c.dirMu.RLock()
	defer c.dirMu.RUnlock()
	return c.dir
}

func (c *Cache) Path(videoID string) string {
	sum := sha256.Sum256([]byte(strings.ToUpper(strings.TrimSpace(videoID))))
	return filepath.Join(c.Directory(), hex.EncodeToString(sum[:16])+".img")
}

func (c *Cache) Ensure(ctx context.Context, videoID, sourceURL string) (string, bool, error) {
	dir := c.Directory()
	sum := sha256.Sum256([]byte(strings.ToUpper(strings.TrimSpace(videoID))))
	path := filepath.Join(dir, hex.EncodeToString(sum[:16])+".img")
	if info, err := os.Stat(path); err == nil && info.Size() > 0 {
		return path, false, nil
	}
	return c.download(ctx, videoID, sourceURL, path, false)
}

// Refresh downloads the current upstream artwork even when a local cover is
// already cached. The cached file is replaced only when its bytes changed, so
// recurring Quick and Full refreshes can replace temporary artwork (for
// example JavLibrary's "Now Printing" image) without needlessly rewriting
// every unchanged cover.
func (c *Cache) Refresh(ctx context.Context, videoID, sourceURL string) (string, bool, error) {
	dir := c.Directory()
	sum := sha256.Sum256([]byte(strings.ToUpper(strings.TrimSpace(videoID))))
	path := filepath.Join(dir, hex.EncodeToString(sum[:16])+".img")
	return c.download(ctx, videoID, sourceURL, path, true)
}

func (c *Cache) download(ctx context.Context, videoID, sourceURL, path string, compareExisting bool) (string, bool, error) {
	dir := filepath.Dir(path)
	if strings.TrimSpace(sourceURL) == "" {
		return "", false, errors.New("release has no cover URL")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, sourceURL, nil)
	if err != nil {
		return "", false, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/124 Safari/537.36")
	req.Header.Set("Accept", "image/avif,image/webp,image/apng,image/svg+xml,image/*,*/*;q=0.8")
	req.Header.Set("Referer", referer(sourceURL))
	resp, err := c.client.Do(req)
	if err != nil {
		return "", false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return "", false, fmt.Errorf("cover request returned HTTP %d", resp.StatusCode)
	}
	contentType := strings.ToLower(resp.Header.Get("Content-Type"))
	if contentType != "" && !strings.HasPrefix(contentType, "image/") {
		return "", false, fmt.Errorf("cover request returned %q", contentType)
	}
	tmp, err := os.CreateTemp(dir, ".cover-*")
	if err != nil {
		return "", false, err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	n, copyErr := io.Copy(tmp, io.LimitReader(resp.Body, maxCoverSize+1))
	closeErr := tmp.Close()
	if copyErr != nil {
		return "", false, copyErr
	}
	if closeErr != nil {
		return "", false, closeErr
	}
	if n == 0 || n > maxCoverSize {
		return "", false, fmt.Errorf("invalid cover size: %d bytes", n)
	}
	if compareExisting {
		candidate, readErr := os.ReadFile(tmpName)
		if readErr != nil {
			return "", false, readErr
		}
		if current, readErr := os.ReadFile(path); readErr == nil && bytes.Equal(current, candidate) {
			return path, false, nil
		} else if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
			return "", false, readErr
		}
	}
	if err := os.Rename(tmpName, path); err != nil {
		return "", false, err
	}
	c.log.Info("cover cached locally", "video_id", videoID, "source_url", sourceURL, "path", path, "bytes", n, "refreshed", compareExisting)
	return path, true, nil
}

func referer(raw string) string {
	if strings.Contains(raw, "javlibrary") || strings.Contains(raw, "pics.dmm") {
		return "https://www.javlibrary.com/"
	}
	if strings.Contains(raw, "akiba-web") {
		return "https://www.akiba-web.com/"
	}
	return raw
}

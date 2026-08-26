package screenshots

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const maxScreenshotSize = 25 << 20

type Cache struct {
	dir    string
	client *http.Client
	log    *slog.Logger
}

func New(dir string, timeout time.Duration, log *slog.Logger) (*Cache, error) {
	if strings.TrimSpace(dir) == "" {
		dir = "data/screenshots"
	}
	dir = filepath.Clean(strings.TrimSpace(dir))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create screenshot cache: %w", err)
	}
	if log == nil {
		log = slog.Default()
	}
	return &Cache{dir: dir, client: &http.Client{Timeout: timeout}, log: log}, nil
}

func (c *Cache) Directory() string { return c.dir }

func (c *Cache) Path(videoID string, index int) string {
	sum := sha256.Sum256([]byte(strings.ToUpper(strings.TrimSpace(videoID))))
	return filepath.Join(c.dir, hex.EncodeToString(sum[:16]), fmt.Sprintf("%03d.img", index))
}

func (c *Cache) Complete(videoID string, urls []string) bool {
	if len(urls) == 0 {
		return false
	}
	for index := range urls {
		if info, err := os.Stat(c.Path(videoID, index)); err != nil || info.Size() == 0 {
			return false
		}
	}
	return true
}

// Available returns the indexes that are already present in the local cache.
// UI callers use this manifest instead of probing remote source URLs, so a
// hover or detail view never turns into an implicit screenshot download.
func (c *Cache) Available(videoID string, urls []string) []int {
	available := make([]int, 0, len(urls))
	for index := range urls {
		if info, err := os.Stat(c.Path(videoID, index)); err == nil && info.Size() > 0 {
			available = append(available, index)
		}
	}
	return available
}

func (c *Cache) EnsureAll(ctx context.Context, videoID string, urls []string) (downloaded, skipped, failed int, lastErr error) {
	for index, raw := range urls {
		cached, err := c.Ensure(ctx, videoID, index, raw)
		switch {
		case err != nil:
			failed++
			lastErr = err
		case cached:
			downloaded++
		default:
			skipped++
		}
	}
	return
}

func (c *Cache) Ensure(ctx context.Context, videoID string, index int, sourceURL string) (bool, error) {
	path := c.Path(videoID, index)
	if info, err := os.Stat(path); err == nil && info.Size() > 0 {
		return false, nil
	}
	if strings.TrimSpace(sourceURL) == "" {
		return false, errors.New("screenshot URL is empty")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, sourceURL, nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/124 Safari/537.36")
	req.Header.Set("Accept", "image/avif,image/webp,image/apng,image/*,*/*;q=0.8")
	req.Header.Set("Referer", "https://www.javlibrary.com/")
	resp, err := c.client.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return false, fmt.Errorf("screenshot request returned HTTP %d", resp.StatusCode)
	}
	if contentType := strings.ToLower(resp.Header.Get("Content-Type")); contentType != "" && !strings.HasPrefix(contentType, "image/") {
		return false, fmt.Errorf("screenshot request returned %q", contentType)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".screenshot-*")
	if err != nil {
		return false, err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	n, copyErr := io.Copy(tmp, io.LimitReader(resp.Body, maxScreenshotSize+1))
	closeErr := tmp.Close()
	if copyErr != nil {
		return false, copyErr
	}
	if closeErr != nil {
		return false, closeErr
	}
	if n == 0 || n > maxScreenshotSize {
		return false, fmt.Errorf("invalid screenshot size: %d bytes", n)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return false, err
	}
	c.log.Debug("screenshot cached locally", "video_id", videoID, "index", index, "path", path, "bytes", n)
	return true, nil
}

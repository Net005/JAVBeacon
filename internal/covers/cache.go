package covers

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
	"sync"
	"time"
)

const maxCoverSize = 20 << 20

type Cache struct {
	dirMu  sync.RWMutex
	dir    string
	client *http.Client
	log    *slog.Logger
}

func New(dir string, timeout time.Duration, log *slog.Logger) (*Cache, error) {
	cache := &Cache{client: &http.Client{Timeout: timeout}, log: log}
	if err := cache.SetDirectory(dir); err != nil {
		return nil, err
	}
	return cache, nil
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
	if err := os.Rename(tmpName, path); err != nil {
		return "", false, err
	}
	c.log.Info("cover cached locally", "video_id", videoID, "source_url", sourceURL, "path", path, "bytes", n)
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

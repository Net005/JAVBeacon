package domain

import (
	"encoding/base32"
	"encoding/hex"
	"errors"
	"net/url"
	"strings"
	"time"
)

// BlacklistedRelease excludes one acquisition source, not the catalog title.
// Entries survive download-history deletion and changes to safeguard settings.
type BlacklistedRelease struct {
	URL             string    `json:"url"`
	ReleaseID       int64     `json:"release_id"`
	VideoID         string    `json:"video_id"`
	Title           string    `json:"title"`
	Transport       string    `json:"transport"`
	Reason          string    `json:"reason"`
	SizeBytes       int64     `json:"size_bytes"`
	DurationSeconds float64   `json:"duration_seconds"`
	CreatedAt       time.Time `json:"created_at"`
}

// BlacklistURLKey keeps HTTP paths/queries exact. Magnet tracker, display-name,
// case and base32/hex differences must not allow the same torrent back in.
func BlacklistURLKey(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || u == nil || u.User != nil {
		return "", errors.New("a valid HTTP, HTTPS or magnet download URL is required")
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
		if u.Host == "" {
			break
		}
		u.Scheme = strings.ToLower(u.Scheme)
		u.Host = strings.ToLower(u.Host)
		u.Fragment = ""
		if u.Hostname() == "mypikpak.com" || strings.HasSuffix(u.Hostname(), ".mypikpak.com") {
			parts := strings.Split(strings.Trim(u.Path, "/"), "/")
			if len(parts) >= 2 && parts[0] == "s" && parts[1] != "" {
				u.Host = "mypikpak.com"
				u.Path = "/s/" + parts[1]
				u.RawPath = ""
				u.RawQuery = ""
				u.ForceQuery = false
			}
		}
		return u.String(), nil
	case "magnet":
		for _, xt := range u.Query()["xt"] {
			if !strings.HasPrefix(strings.ToLower(xt), "urn:btih:") {
				continue
			}
			hash := xt[len("urn:btih:"):]
			if len(hash) == 32 {
				if b, e := base32.StdEncoding.DecodeString(strings.ToUpper(hash)); e == nil {
					hash = hex.EncodeToString(b)
				}
			}
			if b, e := hex.DecodeString(hash); e == nil && len(b) == 20 {
				return "magnet:?xt=urn:btih:" + strings.ToLower(hash), nil
			}
		}
	}
	return "", errors.New("a valid HTTP, HTTPS or BTIH magnet download URL is required")
}

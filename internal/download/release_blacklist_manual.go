package download

import (
	"context"
	"net/url"
	"strings"
	"time"

	"github.com/Net005/JAVBeacon/internal/domain"
)

// BlacklistSource records a manually excluded source and, for supported share
// redirects, its stable PikPak alias without restoring or transferring media.
// A redirect outage does not prevent the original URL from being blocked.
func (s *Service) BlacklistSource(ctx context.Context, raw, reason string) (string, error) {
	if _, err := domain.BlacklistURLKey(raw); err != nil {
		return "", err
	}
	transport := "http"
	if strings.HasPrefix(strings.ToLower(raw), "magnet:") {
		transport = "torrent"
	}
	warning := ""
	if u, err := url.Parse(raw); err == nil && isJavDBShareURL(u) {
		lookupCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		shareID, err := discoverPikPakShareID(lookupCtx, s.client, raw)
		cancel()
		if err != nil {
			warning = "Original URL blacklisted; PikPak alias could not be resolved yet. Known aliases remain blocked and newly discovered aliases will be checked."
		} else if err = s.store.RememberReleaseSourceAliases(ctx, raw, []string{"https://mypikpak.com/s/" + shareID}); err != nil {
			return "", err
		}
	}
	err := s.store.SaveBlacklistedRelease(ctx, domain.BlacklistedRelease{URL: raw, Reason: reason, Transport: transport})
	return warning, err
}

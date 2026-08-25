package domain

import (
	"net/url"
	"strings"
)

// NormalizeJavLibraryURL canonicalizes every JavLibrary URL to its HTTPS www
// origin. Keeping this in domain lets storage and every scraper transport use
// the same rule instead of relying on one caller to remember a redirect-prone
// conversion before handing the URL to Byparr/FlareSolverr.
func NormalizeJavLibraryURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return raw
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if host != "javlibrary.com" && host != "www.javlibrary.com" {
		return raw
	}
	u.Scheme = "https"
	u.Host = "www.javlibrary.com"
	u.User = nil
	return u.String()
}

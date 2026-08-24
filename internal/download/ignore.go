package download

import (
	"regexp"
	"strings"

	"github.com/Net005/JAVBeacon/internal/domain"
)

// releaseIgnored reports whether r matches one of the configured
// ignore_tags/ignore_titles settings (see domain.ParseIgnoreList), and a
// human-readable reason for logging/skip-tallying if so. It is consulted
// by both Auto (the per-scrape auto-download gate fired as new releases
// are detected) and runSearch (the scheduled "monitored" search job), so a
// release the user has asked to ignore is skipped by both automatic
// pathways - matching automatically or otherwise - rather than just one.
// It intentionally has no effect on a manual "Search" / "Search &
// Download" action: those stay available even for an ignored release, the
// same way "Hide Local" only hides local releases from the default view
// without blocking anything a user does to one by hand.
func releaseIgnored(r domain.Release, ignoreTags, ignoreTitles []string) (bool, string) {
	for _, tag := range ignoreTags {
		tag = strings.TrimSpace(tag)
		if tag == "" {
			continue
		}
		for _, genre := range r.Genres {
			if strings.EqualFold(strings.TrimSpace(genre), tag) {
				return true, "tag \"" + tag + "\" is on the ignore list"
			}
		}
	}
	for _, title := range ignoreTitles {
		if titleMatchesIgnorePattern(r.Title, title) {
			return true, "title matches ignore pattern \"" + strings.TrimSpace(title) + "\""
		}
	}
	return false, ""
}

// titleMatchesIgnorePattern mirrors the frontend's wildcardMatch (app.js):
// a pattern with no "*"/"?" is a plain case-insensitive substring match; a
// pattern containing them is translated to an unanchored, case-insensitive
// regular expression ("*" -> any run of characters, "?" -> any single
// character) rather than a full-string match, so "*insulted*" and
// "insulted" behave the same way an ignore-by-title rule reads intuitively.
func titleMatchesIgnorePattern(title, pattern string) bool {
	pattern = strings.ToLower(strings.TrimSpace(pattern))
	if pattern == "" {
		return false
	}
	text := strings.ToLower(title)
	if !strings.ContainsAny(pattern, "*?") {
		return strings.Contains(text, pattern)
	}
	quoted := regexp.QuoteMeta(pattern)
	quoted = strings.ReplaceAll(quoted, `\*`, `.*`)
	quoted = strings.ReplaceAll(quoted, `\?`, `.`)
	re, err := regexp.Compile(quoted)
	if err != nil {
		return strings.Contains(text, strings.NewReplacer("*", "", "?", "").Replace(pattern))
	}
	return re.MatchString(text)
}

package scraper

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"golang.org/x/net/html"
)

// StatusError wraps a scrape failure with the ScrapeStatus that produced it,
// so a caller that needs to distinguish "blocked" from "invalid" from a
// generic transport/parse failure (Phase 12's per-release update-details
// outcome) can do so without parsing the error text. Plain transport/parse
// errors (a network failure, a non-2xx response, malformed HTML) are left
// as ordinary errors and are treated as ScrapeError by any caller that
// checks with errors.As and finds no *StatusError.
type StatusError struct {
	Status  ScrapeStatus
	Message string
}

func (e *StatusError) Error() string { return e.Message }

func statusErrorf(status ScrapeStatus, format string, args ...any) *StatusError {
	return &StatusError{Status: status, Message: fmt.Sprintf(format, args...)}
}

// ScrapeStatus classifies a fetched, parsed page before it is allowed to be
// mined for release data. It is the shared vocabulary used across normal
// scraping, FlareSolverr scraping, manual scraping, scheduled scraping, and
// release detail updates, so log lines and error messages read the same way
// regardless of which code path produced them.
type ScrapeStatus string

const (
	ScrapeValid   ScrapeStatus = "VALID"
	ScrapeInvalid ScrapeStatus = "INVALID"
	ScrapeBlocked ScrapeStatus = "BLOCKED"
	ScrapeError   ScrapeStatus = "ERROR"
)

// cloudflareReason reports why a parsed page looks like a Cloudflare (or
// similar interstitial/anti-bot) challenge page rather than real site
// content, or "" if it does not. It intentionally does not depend on any
// single site's markup so it applies equally to every provider and to both
// directly fetched and FlareSolverr-solved responses -- a FlareSolverr
// solve can itself still return the challenge page when the solve failed or
// timed out, and that response must be caught the same way a direct one is.
func cloudflareReason(doc *html.Node) string {
	title := ""
	if n := first(doc, func(n *html.Node) bool { return n.Data == "title" }); n != nil {
		title = strings.TrimSpace(nodeText(n))
	}
	lower := strings.ToLower(title)
	switch {
	case strings.Contains(lower, "just a moment"):
		return `Cloudflare interstitial title ("Just a moment...")`
	case strings.Contains(lower, "attention required"):
		return `Cloudflare interstitial title ("Attention Required")`
	case strings.Contains(lower, "checking your browser"):
		return `Cloudflare interstitial title ("Checking your browser")`
	}
	if first(doc, func(n *html.Node) bool { return attr(n, "id") == "challenge-form" }) != nil {
		return "Cloudflare challenge-form present"
	}
	if first(doc, func(n *html.Node) bool {
		return attr(n, "id") == "cf-wrapper" || hasClass(n, "cf-browser-verification")
	}) != nil {
		return "Cloudflare verification wrapper present"
	}
	return ""
}

// pagePresence reports whether a parsed page contains the minimum structural
// signal expected for one provider/page-kind combination, e.g. the listing
// card class JavLibrary and GIGA each render, or the detail-page container
// each site's product page uses. A page that returns HTTP 200 with an
// unrelated body -- a login wall, a "no results" page, a changed layout --
// fails this even though it is not a Cloudflare challenge.
func pagePresence(provider, kind string, doc *html.Node) (ok bool, hint string) {
	switch {
	case provider == "JavLibrary" && kind == "listing":
		hint = "a .video or .id listing entry"
		return first(doc, func(n *html.Node) bool { return hasClass(n, "video") || hasClass(n, "id") }) != nil, hint
	case provider == "JavLibrary" && kind == "detail":
		hint = "a #video_jacket_img cover or a td.header metadata row"
		if first(doc, func(n *html.Node) bool { return attr(n, "id") == "video_jacket_img" }) != nil {
			return true, hint
		}
		return first(doc, func(n *html.Node) bool { return n.Data == "td" && hasClass(n, "header") }) != nil, hint
	case provider == "GIGA" && kind == "listing":
		hint = "a .search_sam_box or .sam_box listing card"
		return first(doc, func(n *html.Node) bool { return hasClass(n, "search_sam_box") || hasClass(n, "sam_box") }) != nil, hint
	case provider == "GIGA" && kind == "detail":
		hint = "a #works_pic or #works_txt product section"
		return first(doc, func(n *html.Node) bool { return hasAncestorID(n, "works_pic") || hasAncestorID(n, "works_txt") }) != nil, hint
	default:
		return true, ""
	}
}

// validatePage centrally checks a parsed page before a caller is allowed to
// mine it for release data. It never performs the HTTP fetch itself, so the
// exact same check applies whether the HTML came from a direct request or a
// FlareSolverr solve -- callers are expected to run every fetched, parsed
// document through this before treating it as usable, and to log the
// resulting status with enough context (provider/kind/url) to diagnose a bad
// scrape from the live log alone.
func validatePage(provider, kind string, doc *html.Node) (ScrapeStatus, string) {
	if reason := cloudflareReason(doc); reason != "" {
		return ScrapeBlocked, reason
	}
	if ok, hint := pagePresence(provider, kind, doc); !ok {
		return ScrapeInvalid, "page did not contain the expected " + hint
	}
	return ScrapeValid, ""
}

// scrapeRetryAttempts is how many additional attempts a Cloudflare-blocked or
// otherwise failed fetch gets before giving up, on top of the first try -
// "at least 2 retries" per TODO-2.0. ScrapeInvalid is deliberately excluded
// from retries everywhere this is used: a structurally wrong page (changed
// layout, wrong content, a "no results" page) will not fix itself by asking
// again, so retrying it only wastes time and requests.
const scrapeRetryAttempts = 2

// RetryFirstBackoff and RetrySecondBackoff are the delays before
// the first and second (and any later) retry, respectively. They are
// deliberately moderate and increasing rather than instant or aggressive:
// recent Cloudflare anti-bot/Captcha protections are more likely to escalate
// a client that hammers it with identical requests in quick succession, so a
// few seconds of breathing room between attempts is both kinder to the site
// and more likely to actually get through than retrying immediately. They
// are package-level vars, rather than constants, purely so tests can shrink
// them instead of a scraper test taking real wall-clock seconds.
var (
	RetryFirstBackoff  = 4 * time.Second
	RetrySecondBackoff = 9 * time.Second
)

// scrapeRetryBackoff returns how long to wait before retry attempt n
// (1-based: the delay before the first retry, then the delay before the
// second and any later one).
func scrapeRetryBackoff(attempt int) time.Duration {
	if attempt <= 1 {
		return RetryFirstBackoff
	}
	return RetrySecondBackoff
}

// shouldRetryScrape reports whether a failed fetch is worth retrying: a
// Cloudflare/interstitial block or a transport/parse-level error, but not a
// structurally-wrong page (ScrapeInvalid), and never once the context has
// already been cancelled or timed out.
func shouldRetryScrape(ctx context.Context, err error) bool {
	if err == nil || ctx.Err() != nil {
		return false
	}
	var statusErr *StatusError
	if errors.As(err, &statusErr) {
		return statusErr.Status == ScrapeBlocked || statusErr.Status == ScrapeError
	}
	return true
}

// withScrapeRetry runs fetch, retrying it (with scrapeRetryBackoff between
// attempts) up to scrapeRetryAttempts additional times when the failure
// looks transient - a Cloudflare challenge or a transport/parse error - per
// TODO-2.0's scraping reliability requirement. logRetry, when non-nil, is
// called before each retry so the caller can log it with its own
// provider/kind/url context.
func withScrapeRetry(ctx context.Context, fetch func() (*html.Node, error), logRetry func(attempt int, wait time.Duration, err error)) (*html.Node, error) {
	for attempt := 0; ; attempt++ {
		doc, err := fetch()
		if err == nil {
			return doc, nil
		}
		if attempt >= scrapeRetryAttempts || !shouldRetryScrape(ctx, err) {
			return nil, err
		}
		wait := scrapeRetryBackoff(attempt + 1)
		if logRetry != nil {
			logRetry(attempt+1, wait, err)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(wait):
		}
	}
}

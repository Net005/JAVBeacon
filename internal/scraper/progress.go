package scraper

import (
	"net/url"
	"strconv"

	"golang.org/x/net/html"
)

// Progress reports the release currently being processed on a provider listing page.
type Progress func(page, pageLimit, item, pageItems int, videoID string)

// DetailStage reports a single-release detail refresh's progress as it
// moves through fetching/validating, parsing, so a caller polling a job's
// status (Phase 12) can show something more useful than a single opaque
// "running" state for however long the underlying HTTP round trip takes.
// It mirrors Progress's role for paginated listing scrapes but is shaped
// for the much simpler one-page detail flow instead.
type DetailStage func(stage string)

// Detail refresh stage names. StageConnecting covers the whole
// fetch-then-validate span (including a Cloudflare/anti-bot rejection, if
// any) since validation happens synchronously right after the fetch with no
// separately observable duration of its own; StageConnectingFlareSolverr
// marks the FlareSolverr fallback specifically, since that round trip can
// run tens of seconds longer than a direct fetch. StageParsing covers
// extracting fields from the now-validated page. "comparing" and
// "updating" are reported by the caller in internal/monitor, which is the
// only place that has both the previous and newly scraped release to
// compare.
const (
	StageConnecting             = "connecting"
	StageConnectingFlareSolverr = "connecting_flaresolverr"
	StageParsing                = "parsing"
)

func report(stage []DetailStage, name string) {
	if len(stage) > 0 && stage[0] != nil {
		stage[0](name)
	}
}

func listingPageLimit(doc *html.Node, queryKey string, ceiling, current int) int {
	onlineMax := 0
	for _, link := range findAll(doc, func(node *html.Node) bool { return node.Data == "a" }) {
		href := attr(link, "href")
		parsed, err := url.Parse(href)
		if err != nil {
			continue
		}
		page, err := strconv.Atoi(parsed.Query().Get(queryKey))
		if err == nil && page > onlineMax {
			onlineMax = page
		}
	}
	if onlineMax <= 0 {
		return ceiling
	}
	if ceiling <= 0 {
		return max(current, onlineMax)
	}
	return max(current, min(ceiling, onlineMax))
}

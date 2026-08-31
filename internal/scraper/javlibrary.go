package scraper

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Net005/JAVBeacon/internal/domain"
	"golang.org/x/net/html"
)

// JavLibrary is the general-purpose monitoring provider. It accepts the
// actress/director/genre/maker/label/series/listing URLs managed by the client.
type JavLibrary struct {
	client *http.Client
	pool   *SolverPool
	log    *slog.Logger
}

var parenthesizedActress = regexp.MustCompile(`\(([^()]+)\)`)

// NewJavLibrary constructs a JavLibrary scraper with a single legacy solver
// URL/cooldown, for backward-compatible callers (tests, any direct
// construction that hasn't moved to the multi-instance settings model yet).
// An empty flareSolverr yields a pool with zero instances - documentOnce
// then fetches directly, exactly as before Byparr pooling existed.
func NewJavLibrary(timeout time.Duration, flareSolverr string, cooldown float64, log *slog.Logger) *JavLibrary {
	jar, _ := cookiejar.New(nil)
	if log == nil {
		log = slog.Default()
	}
	j := &JavLibrary{client: &http.Client{Timeout: timeout, Jar: jar}, pool: NewSolverPool(), log: log}
	if raw := strings.TrimRight(flareSolverr, "/"); raw != "" {
		j.pool.Configure([]Instance{{URL: raw, Priority: 1, Enabled: true}}, time.Duration(cooldown*float64(time.Second)))
	}
	return j
}

// Configure hot-swaps the pool of configured Byparr/FlareSolverr instances
// and their shared cooldown. An empty/all-disabled instances list means "no
// solver configured" - documentOnce then fetches directly.
func (j *JavLibrary) Configure(instances []Instance, cooldown time.Duration) {
	j.pool.Configure(instances, cooldown)
}

// Pool exposes the scraper's solver pool so callers (e.g. the monitor
// package, when sizing a concurrent worker batch) can check EnabledCount()
// without needing their own reference to the pool.
func (j *JavLibrary) Pool() *SolverPool { return j.pool }

// PoolEnabledCount reports how many configured Byparr/FlareSolverr
// instances are currently enabled, or 0 if none are configured. It's a
// thin wrapper around Pool().EnabledCount() so packages that only depend on
// JavLibrary through a narrow interface (e.g. internal/backfill's
// historicalScraper) can size a concurrent worker batch without importing
// *SolverPool into their interface.
func (j *JavLibrary) PoolEnabledCount() int { return j.pool.EnabledCount() }

// HistoricalIndex is a durable discovery edge used by the manual catalog
// backfill. Releases are unioned across these indexes, because no single
// rolling JavLibrary list represents the full historical catalog.
type HistoricalIndex struct{ URL, Kind, Name string }

// HistoricalIndexes discovers the site's genre, performer and maker graph.
// A directory may be absent on a mirror; successful directories are still
// returned, but genres are required because they are the broadest index.
func (j *JavLibrary) HistoricalIndexes(ctx context.Context) ([]HistoricalIndex, error) {
	roots := []struct{ url, kind, needle string }{
		{"https://www.javlibrary.com/en/genres.php", "genre", "vl_genre.php"},
		{"https://www.javlibrary.com/en/star_list.php", "star", "vl_star.php"},
		{"https://www.javlibrary.com/en/makers.php", "maker", "vl_maker.php"},
	}
	seen := map[string]bool{}
	var out []HistoricalIndex
	for _, root := range roots {
		directoryQueue, directorySeen := []string{root.url}, map[string]bool{}
		for len(directoryQueue) > 0 && len(directorySeen) < 500 {
			directoryURL := directoryQueue[0]
			directoryQueue = directoryQueue[1:]
			if directorySeen[directoryURL] {
				continue
			}
			directorySeen[directoryURL] = true
			doc, err := j.document(ctx, directoryURL, "index")
			if err != nil {
				if root.kind == "genre" && len(directorySeen) == 1 {
					return nil, err
				}
				j.log.Warn("historical index directory unavailable", "kind", root.kind, "url", directoryURL, "error", err)
				continue
			}
			for _, a := range findAll(doc, func(n *html.Node) bool { return n.Data == "a" }) {
				href := attr(a, "href")
				if strings.Contains(href, root.needle) {
					u := normalizeJavLibraryURL(resolve(directoryURL, href))
					parsed, err := url.Parse(u)
					if err != nil {
						continue
					}
					q := parsed.Query()
					q.Set("mode", "2")
					parsed.RawQuery = q.Encode()
					u = parsed.String()
					if !seen[u] {
						seen[u] = true
						name := strings.TrimSpace(nodeText(a))
						if name == "" {
							name = u
						}
						out = append(out, HistoricalIndex{URL: u, Kind: root.kind, Name: name})
					}
				}
				candidate := normalizeJavLibraryURL(resolve(directoryURL, href))
				parsedCandidate, e1 := url.Parse(candidate)
				parsedRoot, e2 := url.Parse(root.url)
				if e1 == nil {
					parsedCandidate.Fragment = ""
					candidate = parsedCandidate.String()
				}
				if e1 == nil && e2 == nil && parsedCandidate.Host == parsedRoot.Host && path.Base(parsedCandidate.Path) == path.Base(parsedRoot.Path) && candidate != directoryURL && candidate != root.url && !directorySeen[candidate] {
					directoryQueue = append(directoryQueue, candidate)
				}
			}
		}
	}
	if len(out) == 0 {
		return nil, errors.New("JavLibrary historical directories contained no graph indexes")
	}
	return out, nil
}

// HistoricalPage fetches exactly one date-sorted index page and enriches
// only candidates accepted by include. IDs reports every listing entry,
// including already-known ones, so callers can locate a saved date boundary
// without re-fetching its detail pages. concurrency bounds how many detail
// pages within this one listing page are fetched at once (see
// ScrapeConcurrency) - the caller sizes it against the configured Byparr
// pool so the backfill actually uses every enabled instance instead of
// fetching one detail page at a time.
func (j *JavLibrary) HistoricalPage(ctx context.Context, source string, page int, include func(string) bool, concurrency ScrapeConcurrency) (items []domain.Release, ids []string, pageLimit int, err error) {
	u, err := url.Parse(source)
	if err != nil {
		return nil, nil, 0, err
	}
	q := u.Query()
	q.Set("mode", "2")
	q.Set("page", strconv.Itoa(max(page, 1)))
	u.RawQuery = q.Encode()
	items, err = j.ScrapeFiltered(ctx, u.String(), 1, include, concurrency, func(_ int, limit, item, _ int, videoID string) {
		if limit > pageLimit {
			pageLimit = limit
		}
		if item > 0 && videoID != "" {
			ids = append(ids, videoID)
		}
	})
	return
}

// normalizeJavLibraryURL forces any javlibrary.com/www.javlibrary.com URL to
// https://www.javlibrary.com, whatever scheme or host variant it arrived
// with. Older stored data (sites.url, releases.product_url) and links
// resolved from a stale cached page can still carry http:// or the bare
// "javlibrary.com" host; both are known to trigger 403s on direct fetches and
// mid-navigation http->https redirects that break FlareSolverr. URLs for any
// other host pass through unchanged.
func normalizeJavLibraryURL(raw string) string {
	return domain.NormalizeJavLibraryURL(raw)
}

// document fetches and parses one page and validates it before returning it,
// so a Cloudflare interstitial or an otherwise-wrong page (login wall,
// changed layout, error page) is never handed to a caller as if it were real
// site content. kind identifies what shape of page is expected ("listing" or
// "detail") for validatePage. When a FlareSolverr solver is configured, every
// request goes through it directly - JavLibrary's Cloudflare check reliably
// 403s a subset of direct requests, and mixing direct and solved requests to
// the same host can even trip a mid-navigation http->https redirect race
// inside FlareSolverr itself, so once a solver is configured it is used
// exclusively rather than only as a fallback. Only when no solver is
// configured does documentOnce fetch directly.
//
// document itself is a thin retry wrapper around documentOnce: a Cloudflare
// block, transport/parse error, or structurally-empty listing response gets
// scrapeRetryAttempts more tries (with a short backoff between them) before
// giving up. JavLibrary occasionally returns a partial/anti-bot response with
// none of the expected listing entries even through a solver; detail-page
// validation failures remain terminal because those more often indicate a
// genuinely changed page shape.
func (j *JavLibrary) document(ctx context.Context, raw, kind string, stage ...DetailStage) (*html.Node, error) {
	return withScrapeRetry(ctx, func() (*html.Node, error) {
		return j.documentOnce(ctx, raw, kind, stage...)
	}, func(attempt int, wait time.Duration, err error) {
		j.log.Info("scrape retry", "provider", "JavLibrary", "kind", kind, "url", raw, "attempt", attempt, "wait", wait.String(), "reason", err.Error())
	})
}

func (j *JavLibrary) documentOnce(ctx context.Context, raw, kind string, stage ...DetailStage) (*html.Node, error) {
	raw = normalizeJavLibraryURL(raw)
	report(stage, StageConnecting)

	if j.pool.EnabledCount() == 0 {
		body, e := j.direct(ctx, raw)
		if e != nil {
			j.log.Info("scrape response rejected", "provider", "JavLibrary", "kind", kind, "url", raw, "status", string(ScrapeError), "reason", e.Error(), "via", "direct")
			return nil, statusErrorf(ScrapeError, "JavLibrary requires Byparr (or a compatible FlareSolverr service); configure the solver URL in Settings > Scraping (%s)", e.Error())
		}
		doc, parseErr := html.Parse(bytes.NewReader(body))
		if parseErr != nil {
			j.log.Info("scrape response rejected", "provider", "JavLibrary", "kind", kind, "url", raw, "status", string(ScrapeError), "reason", parseErr.Error(), "via", "direct")
			return nil, statusErrorf(ScrapeError, "JavLibrary requires Byparr (or a compatible FlareSolverr service); configure the solver URL in Settings > Scraping (%s)", parseErr.Error())
		}
		status, reason := validatePage("JavLibrary", kind, doc)
		if status == ScrapeValid {
			j.log.Info("scrape response valid", "provider", "JavLibrary", "kind", kind, "url", raw, "status", string(ScrapeValid), "bytes", len(body))
			return doc, nil
		}
		j.log.Info("scrape response rejected", "provider", "JavLibrary", "kind", kind, "url", raw, "status", string(status), "reason", reason, "via", "direct")
		if status == ScrapeInvalid && kind == "listing" {
			return nil, retryableStatusErrorf(status, "JavLibrary response %s: %s", status, reason)
		}
		if status == ScrapeBlocked || status == ScrapeInvalid {
			return nil, statusErrorf(status, "JavLibrary response %s: %s", status, reason)
		}
		return nil, statusErrorf(ScrapeError, "JavLibrary requires Byparr (or a compatible FlareSolverr service); configure the solver URL in Settings > Scraping (%s)", reason)
	}

	report(stage, StageConnectingFlareSolverr)
	lease, e := j.pool.Acquire(ctx, solverPriorityFromContext(ctx))
	if e != nil {
		return nil, e
	}
	defer lease.Release()
	j.log.Info("using anti-bot solver", "provider", "JavLibrary", "kind", kind, "url", raw, "solver", lease.URL())
	body, e := j.flare(ctx, raw, lease.URL())
	if e != nil {
		j.log.Info("scrape response rejected", "provider", "JavLibrary", "kind", kind, "url", raw, "status", string(ScrapeError), "reason", e.Error(), "via", "flaresolverr")
		return nil, statusErrorf(ScrapeError, "%s", e.Error())
	}
	doc, parseErr := html.Parse(bytes.NewReader(body))
	if parseErr != nil {
		j.log.Info("scrape response rejected", "provider", "JavLibrary", "kind", kind, "url", raw, "status", string(ScrapeError), "reason", parseErr.Error(), "via", "flaresolverr")
		return nil, statusErrorf(ScrapeError, "%s", parseErr.Error())
	}
	if status, reason := validatePage("JavLibrary", kind, doc); status != ScrapeValid {
		j.log.Info("scrape response rejected", "provider", "JavLibrary", "kind", kind, "url", raw, "status", string(status), "reason", reason, "via", "flaresolverr")
		if status == ScrapeInvalid && kind == "listing" {
			return nil, retryableStatusErrorf(status, "JavLibrary FlareSolverr response %s: %s", status, reason)
		}
		return nil, statusErrorf(status, "JavLibrary FlareSolverr response %s: %s", status, reason)
	}
	j.log.Info("scrape response valid", "provider", "JavLibrary", "kind", kind, "url", raw, "status", string(ScrapeValid), "via", "flaresolverr", "bytes", len(body))
	return doc, nil
}
func (j *JavLibrary) direct(ctx context.Context, raw string) ([]byte, error) {
	raw = normalizeJavLibraryURL(raw)
	req, e := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if e != nil {
		return nil, e
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/132 Safari/537.36")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	resp, e := j.client.Do(req)
	if e != nil {
		return nil, e
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("JavLibrary returned %s", resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 8<<20))
}
func (j *JavLibrary) flare(ctx context.Context, raw, solver string) ([]byte, error) {
	// This is the last boundary before a URL leaves JAVBeacon. Normalize here
	// even though documentOnce already does so, preventing a future or legacy
	// caller from sending redirect-prone plain HTTP to the solver.
	raw = normalizeJavLibraryURL(raw)
	// The per-request cooldown that used to be a fixed sleep here is now
	// enforced by SolverPool itself: it doesn't hand the leased instance back
	// out to another caller until the configured cooldown has elapsed since
	// this request's Release. With multiple instances, requests to different
	// instances still proceed concurrently - only reuse of the same instance
	// is throttled.
	// FlareSolverr uses maxTimeout in milliseconds; Byparr uses max_timeout in
	// seconds. Sending both keeps the provider endpoint interchangeable.
	payload, _ := json.Marshal(map[string]any{"cmd": "request.get", "url": raw, "maxTimeout": 75000, "max_timeout": 75})
	req, e := http.NewRequestWithContext(ctx, http.MethodPost, solver, bytes.NewReader(payload))
	if e != nil {
		return nil, e
	}
	req.Header.Set("Content-Type", "application/json")
	resp, e := j.client.Do(req)
	if e != nil {
		return nil, fmt.Errorf("Byparr/FlareSolverr solver: %w", e)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		message := strings.TrimSpace(string(body))
		if message == "" {
			message = http.StatusText(resp.StatusCode)
		}
		return nil, fmt.Errorf("Byparr/FlareSolverr solver returned HTTP %d: %s", resp.StatusCode, message)
	}
	var result struct {
		Status   string `json:"status"`
		Message  string `json:"message"`
		Solution struct {
			Response string `json:"response"`
		} `json:"solution"`
	}
	if e = json.NewDecoder(io.LimitReader(resp.Body, 10<<20)).Decode(&result); e != nil {
		return nil, fmt.Errorf("Byparr/FlareSolverr solver returned invalid JSON: %w", e)
	}
	if result.Status != "ok" || result.Solution.Response == "" {
		return nil, fmt.Errorf("Byparr/FlareSolverr solver: %s", result.Message)
	}
	return []byte(result.Solution.Response), nil
}
func (j *JavLibrary) Scrape(ctx context.Context, base string, pages int, progress ...Progress) ([]domain.Release, error) {
	return j.ScrapeFiltered(ctx, base, pages, nil, ScrapeConcurrency{}, progress...)
}

func (j *JavLibrary) ScrapeFiltered(ctx context.Context, base string, pages int, include func(string) bool, concurrency ScrapeConcurrency, progress ...Progress) ([]domain.Release, error) {
	return j.scrapeFiltered(ctx, base, pages, include, true, concurrency, progress...)
}

// ScrapeFilteredThroughEnd keeps traversing when include rejects an entire
// page, and stops only when the site's online pagination ends.
func (j *JavLibrary) ScrapeFilteredThroughEnd(ctx context.Context, base string, pages int, include func(string) bool, concurrency ScrapeConcurrency, progress ...Progress) ([]domain.Release, error) {
	return j.scrapeFiltered(ctx, base, pages, include, false, concurrency, progress...)
}

func (j *JavLibrary) scrapeFiltered(ctx context.Context, base string, pages int, include func(string) bool, stopWhenNoIncluded bool, concurrency ScrapeConcurrency, progress ...Progress) ([]domain.Release, error) {
	started := time.Now()
	if strings.TrimSpace(base) == "" {
		return nil, fmt.Errorf("JavLibrary site URL is required")
	}
	base = normalizeJavLibraryURL(base)
	unlimited := pages <= 0 && !stopWhenNoIncluded
	if pages < 1 && !unlimited {
		pages = 1
	}
	seen := map[string]bool{}
	var out []domain.Release
	for page := 1; unlimited || page <= pages; page++ {
		u, e := url.Parse(base)
		if e != nil {
			return nil, e
		}
		requestedPage := page
		if page == 1 {
			if existing, parseErr := strconv.Atoi(u.Query().Get("page")); parseErr == nil && existing > 1 && pages == 1 {
				requestedPage = existing
			}
		}
		if page > 1 {
			q := u.Query()
			q.Set("page", fmt.Sprint(page))
			u.RawQuery = q.Encode()
		}
		j.log.Info("scraping listing page", "provider", "JavLibrary", "page", page, "page_limit", pages, "url", u.String())
		doc, e := j.document(ctx, u.String(), "listing")
		if e != nil {
			return nil, e
		}
		items := findAll(doc, func(n *html.Node) bool { return hasClass(n, "video") })
		if len(items) == 0 {
			items = findAll(doc, func(n *html.Node) bool { return hasClass(n, "id") })
		}
		ceiling := pages
		if requestedPage != page {
			ceiling = 0
		}
		reportedPageLimit := listingPageLimit(doc, "page", ceiling, requestedPage)
		j.log.Info("listing page parsed", "provider", "JavLibrary", "page", page, "entries", len(items), "url", u.String())
		if len(items) > 0 && len(progress) > 0 && progress[0] != nil {
			progress[0](requestedPage, reportedPageLimit, 0, len(items), "")
		}
		discovered := 0
		type javCandidate struct {
			r    domain.Release
			href string
		}
		var candidates []javCandidate
		for itemIndex, n := range items {
			container := n
			if hasClass(n, "id") && n.Parent != nil {
				container = n.Parent
			}
			idNode := first(container, func(x *html.Node) bool { return hasClass(x, "id") })
			if idNode == nil {
				continue
			}
			videoID := strings.TrimSpace(nodeText(idNode))
			if videoID == "" || seen[strings.ToLower(videoID)] {
				continue
			}
			link := first(container, func(x *html.Node) bool { return x.Data == "a" && strings.Contains(attr(x, "href"), "jav") })
			if link == nil {
				continue
			}
			href := normalizeJavLibraryURL(resolve(u.String(), attr(link, "href")))
			title := attr(link, "title")
			if title == "" {
				title = nodeText(link)
			}
			img := first(container, func(x *html.Node) bool { return x.Data == "img" })
			image := ""
			if img != nil && !isNowPrintingImage(img) {
				image = resolve(u.String(), attr(img, "src"))
				image = strings.Replace(image, "ps.jpg", "pl.jpg", 1)
			}
			r := domain.Release{VideoID: normalizeVideoID(videoID), ScraperID: strings.TrimSuffix(lastPath(href), ".html"), Title: title, ImageURL: image, ProductURL: href, Source: "JavLibrary"}
			seen[strings.ToLower(videoID)] = true
			discovered++
			if len(progress) > 0 && progress[0] != nil {
				progress[0](requestedPage, reportedPageLimit, itemIndex+1, len(items), r.VideoID)
			}
			if include != nil && !include(r.VideoID) {
				continue
			}
			candidates = append(candidates, javCandidate{r: r, href: href})
		}
		// The parsing pass above (fast, no network) still reports progress and
		// runs the include filter strictly one item at a time, exactly as
		// before concurrency existed. Only the actual detail-page fetches -
		// the slow, network-bound part this feature is about - run in
		// concurrent batches, with concurrency.Checkpoint (when set) giving a
		// higher-priority job a chance to preempt between batches.
		fetched := make([]domain.Release, len(candidates))
		fetchDetailsConcurrently(len(candidates), concurrency, func(i int) {
			c := candidates[i]
			r := c.r
			detail, e := j.detail(ctx, c.href)
			// j.detail already retried internally (document -> withScrapeRetry,
			// scrapeRetryAttempts more tries with RetryFirstBackoff/
			// RetrySecondBackoff) before returning this error, so a candidate
			// reaching here has already failed a whole fetch-and-retry cycle -
			// retrying it again immediately would very likely just repeat the
			// same failure. Give it exactly one more try after a longer,
			// separate cooldown (RetrySecondBackoff) instead: long enough that a
			// transiently overloaded solver (the common real-world cause - see
			// OnDetailFailure's doc comment) has a real chance to have recovered
			// by the time this fires, without compounding into the multi-minute
			// worst case a second full scrapeRetryAttempts cycle would cost.
			retried := false
			if e != nil && shouldRetryScrape(ctx, e) {
				retried = true
				j.log.Info("scrape retry", "provider", "JavLibrary", "kind", "detail-item", "page", page, "video_id", r.VideoID, "url", c.href, "wait", RetrySecondBackoff.String(), "reason", e.Error())
				select {
				case <-ctx.Done():
				case <-time.After(RetrySecondBackoff):
				}
				if ctx.Err() == nil {
					detail, e = j.detail(ctx, c.href)
				}
			}
			if e == nil {
				mergeJav(&r, detail)
			} else {
				j.log.Warn("product detail failed", "provider", "JavLibrary", "page", page, "video_id", r.VideoID, "url", c.href, "retried", retried, "error", e)
				if concurrency.OnDetailFailure != nil {
					concurrency.OnDetailFailure(r.VideoID, e)
				}
			}
			fetched[i] = r
		})
		out = append(out, fetched...)
		added := len(candidates)
		if discovered == 0 && page > 1 {
			if len(progress) > 0 && progress[0] != nil {
				progress[0](page-1, page-1, 0, 0, "")
			}
			j.log.Info("online listing end reached", "provider", "JavLibrary", "last_page", page-1, "reason", "empty or repeated page")
			break
		}
		if added == 0 {
			if include != nil && stopWhenNoIncluded {
				j.log.Info("listing page contained no new releases", "provider", "JavLibrary", "page", page)
				break
			}
			if len(items) == 0 && page == 1 {
				return nil, fmt.Errorf("JavLibrary layout changed: no video entries found")
			}
			if len(items) == 0 || include == nil {
				j.log.Info("online listing end reached", "provider", "JavLibrary", "last_page", page-1)
				break
			}
		}
		j.log.Info("listing page completed", "provider", "JavLibrary", "page", page, "releases", added, "total", len(out))
		if reportedPageLimit > 0 && (unlimited || reportedPageLimit < pages) && page >= reportedPageLimit {
			j.log.Info("online listing end reached", "provider", "JavLibrary", "last_page", page, "reason", "reported pagination maximum")
			break
		}
	}
	j.log.Info("provider scrape completed", "provider", "JavLibrary", "releases", len(out), "duration", time.Since(started).Round(time.Millisecond))
	return out, nil
}
func (j *JavLibrary) detail(ctx context.Context, raw string, stage ...DetailStage) (domain.Release, error) {
	doc, e := j.document(ctx, raw, "detail", stage...)
	if e != nil {
		return domain.Release{}, e
	}
	report(stage, StageParsing)
	return parseJavLibraryDetail(doc, raw), nil
}

func parseJavLibraryDetail(doc *html.Node, raw string) domain.Release {
	r := domain.Release{}
	if n := first(doc, func(n *html.Node) bool { return n.Data == "title" }); n != nil {
		r.Title = strings.TrimSpace(strings.TrimSuffix(nodeText(n), "- JAVLibrary"))
	}
	if n := first(doc, func(n *html.Node) bool { return attr(n, "id") == "video_jacket_img" }); n != nil && !isNowPrintingImage(n) {
		r.ImageURL = resolve(raw, attr(n, "src"))
	}
	for _, preview := range findAll(doc, func(n *html.Node) bool { return hasClass(n, "previewthumbs") }) {
		links := findAll(preview, func(n *html.Node) bool { return n.Data == "a" })
		for _, link := range links {
			shot := resolve(raw, attr(link, "href"))
			if shot == "" {
				if image := first(link, func(n *html.Node) bool { return n.Data == "img" }); image != nil {
					shot = resolve(raw, attr(image, "src"))
				}
			}
			if isScreenshotURL(shot) {
				r.Screenshots = appendUnique(r.Screenshots, shot)
			}
		}
		// Some JavLibrary pages expose some or all preview thumbnails as direct
		// children instead of wrapping them in full-size links. In that form
		// the thumbnail filename is the only source available; DMM's full-size
		// preview convention inserts "jp" immediately before the shot number.
		for _, image := range findAll(preview, func(n *html.Node) bool { return n.Data == "img" && n.Parent == preview }) {
			shot := fullSizeScreenshotURL(resolve(raw, attr(image, "src")))
			if isScreenshotURL(shot) {
				r.Screenshots = appendUnique(r.Screenshots, shot)
			}
		}
	}
	for _, h := range findAll(doc, func(n *html.Node) bool { return n.Data == "td" && hasClass(n, "header") }) {
		label := strings.ToLower(nodeText(h))
		vnode := nextElement(h)
		if vnode == nil {
			continue
		}
		v := nodeText(vnode)
		switch {
		case strings.Contains(label, "release date"):
			r.ReleaseDate = normalizeDate(v)
		case strings.Contains(label, "length"):
			r.Duration = strings.TrimSpace(strings.ReplaceAll(v, "min(s)", " min"))
		case strings.Contains(label, "director"):
			r.Director = v
		case strings.Contains(label, "maker") || strings.Contains(label, "studio"):
			r.Studio = v
		case strings.Contains(label, "label"):
			r.Label = v
		case strings.Contains(label, "genre") || strings.Contains(label, "categor"):
			for _, a := range findAll(vnode, func(x *html.Node) bool { return x.Data == "a" }) {
				r.Genres = appendUnique(r.Genres, nodeText(a))
			}
		}
	}
	genreRoot := first(doc, func(n *html.Node) bool { return attr(n, "id") == "video_genres" })
	if genreRoot == nil {
		genreRoot = doc
	}
	for _, n := range findAll(genreRoot, func(n *html.Node) bool { return hasClass(n, "genre") }) {
		for _, a := range findAll(n, func(x *html.Node) bool { return x.Data == "a" }) {
			r.Genres = appendUnique(r.Genres, nodeText(a))
		}
	}
	if r.Studio == "" {
		if n := first(doc, func(n *html.Node) bool { return hasClass(n, "maker") }); n != nil {
			r.Studio = nodeText(n)
		}
	}
	if n := first(doc, func(n *html.Node) bool { return attr(n, "id") == "video_cast" }); n != nil {
		r.Actresses = javLibraryCastNames(n)
		r.Actress = strings.Join(r.Actresses, ", ")
	} else {
		var names []string
		for _, n := range findAll(doc, func(n *html.Node) bool { return hasClass(n, "cast") }) {
			for _, name := range javLibraryCastNames(n) {
				names = appendUnique(names, name)
			}
		}
		r.Actresses = names
		r.Actress = strings.Join(r.Actresses, ", ")
	}
	if r.Director == "" {
		if n := first(doc, func(n *html.Node) bool { return hasClass(n, "director") }); n != nil {
			r.Director = nodeText(n)
		}
	}
	r.Released = isReleased(r.ReleaseDate, "")
	return r
}

func isNowPrintingImage(n *html.Node) bool {
	for _, value := range []string{attr(n, "alt"), attr(n, "title"), attr(n, "src")} {
		normalized := strings.NewReplacer("_", " ", "-", " ").Replace(strings.ToLower(value))
		if strings.Contains(normalized, "now printing") {
			return true
		}
	}
	return false
}

func javLibraryCastNames(root *html.Node) []string {
	var names []string
	castNodes := findAll(root, func(n *html.Node) bool { return hasClass(n, "cast") })
	if len(castNodes) == 0 {
		castNodes = []*html.Node{root}
	}
	for _, cast := range castNodes {
		for _, a := range findAll(cast, func(n *html.Node) bool { return n.Data == "a" }) {
			names = appendUnique(names, nodeText(a))
		}
		for _, alias := range findAll(cast, func(n *html.Node) bool { return hasClass(n, "alias") }) {
			names = appendUnique(names, strings.Trim(nodeText(alias), "() "))
		}
		for _, match := range parenthesizedActress.FindAllStringSubmatch(nodeText(cast), -1) {
			names = appendUnique(names, strings.TrimSpace(match[1]))
		}
	}
	return names
}

// Refresh fetches only the stored product page and merges its current details.
func (j *JavLibrary) Refresh(ctx context.Context, release domain.Release, stage ...DetailStage) (domain.Release, error) {
	if strings.TrimSpace(release.ProductURL) == "" {
		return release, errors.New("release product URL is missing")
	}
	release.ProductURL = normalizeJavLibraryURL(release.ProductURL)
	detail, err := j.detail(ctx, release.ProductURL, stage...)
	if err != nil {
		return release, err
	}
	mergeJav(&release, detail)
	return release, nil
}

// AddByURL scrapes a single JavLibrary product page directly - not from a
// listing page - and returns a ready-to-UpsertRelease domain.Release with
// VideoID/ScraperID/ProductURL/Source populated. Used by TODO-2.0 Phase 2's
// "Missing Library Files" recovery flow, which only has a raw product URL
// (from StashApp's URL list) to work from and no listing page - the normal
// scrapeFiltered path (see above) derives VideoID from the listing page's
// ".id" element, which does not exist here, so this derives it instead
// from an id-shaped token in the detail page's own title, falling back to
// fallbackVideoID (typically extracted from the StashApp scene's own code/
// title by the caller) and finally the URL's own last path segment.
func (j *JavLibrary) AddByURL(ctx context.Context, productURL, fallbackVideoID string, stage ...DetailStage) (domain.Release, error) {
	productURL = normalizeJavLibraryURL(strings.TrimSpace(productURL))
	if productURL == "" {
		return domain.Release{}, errors.New("JavLibrary product URL is required")
	}
	detail, err := j.detail(ctx, productURL, stage...)
	if err != nil {
		return domain.Release{}, err
	}
	videoID := ""
	if m := idPattern.FindStringSubmatch(detail.Title); len(m) > 0 {
		videoID = strings.ToUpper(m[1]) + "-" + m[2]
	}
	if videoID == "" {
		videoID = normalizeVideoID(fallbackVideoID)
	}
	if videoID == "" {
		videoID = normalizeVideoID(strings.TrimSuffix(lastPath(productURL), ".html"))
	}
	if videoID == "" {
		return domain.Release{}, fmt.Errorf("could not determine a video ID for %s", productURL)
	}
	detail.VideoID = videoID
	detail.ScraperID = strings.TrimSuffix(lastPath(productURL), ".html")
	detail.ProductURL = productURL
	detail.Source = "JavLibrary"
	return detail, nil
}

func mergeJav(dst *domain.Release, src domain.Release) {
	if src.Title != "" {
		dst.Title = src.Title
	}
	if src.ImageURL != "" {
		dst.ImageURL = src.ImageURL
	}
	dst.ReleaseDate = src.ReleaseDate
	dst.Duration = src.Duration
	dst.Director = src.Director
	dst.Actress = src.Actress
	dst.Actresses = src.Actresses
	dst.Studio = src.Studio
	dst.Label = src.Label
	dst.Genres = src.Genres
	dst.Screenshots = src.Screenshots
	dst.Released = src.Released
}

func isScreenshotURL(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return false
	}
	path := strings.ToLower(u.Path)
	return strings.HasSuffix(path, ".jpg") || strings.HasSuffix(path, ".jpeg") || strings.HasSuffix(path, ".png") || strings.HasSuffix(path, ".webp")
}

func fullSizeScreenshotURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return raw
	}
	if !strings.EqualFold(u.Hostname(), "pics.dmm.co.jp") {
		return u.String()
	}
	name := path.Base(u.Path)
	ext := path.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	if i := strings.LastIndex(stem, "-"); i > 0 {
		if _, err := strconv.Atoi(stem[i+1:]); err == nil && !strings.HasSuffix(stem[:i], "jp") {
			u.Path = path.Join(path.Dir(u.Path), stem[:i]+"jp"+stem[i:]+ext)
		}
	}
	return u.String()
}
func lastPath(raw string) string {
	u, e := url.Parse(raw)
	if e != nil {
		return ""
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) == 0 {
		return ""
	}
	return parts[len(parts)-1]
}
func normalizeVideoID(v string) string {
	v = strings.ToUpper(strings.TrimSpace(v))
	if m := idPattern.FindStringSubmatch(v); len(m) > 0 {
		return m[1] + "-" + m[2]
	}
	return v
}

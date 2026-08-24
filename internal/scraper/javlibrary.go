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
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/Net005/JAVBeacon/internal/domain"
	"golang.org/x/net/html"
)

// JavLibrary is the general-purpose monitoring provider. It accepts the
// actress/director/genre/maker/label/series/listing URLs managed by the client.
type JavLibrary struct {
	client       *http.Client
	flareSolverr string
	cooldown     time.Duration
	log          *slog.Logger
	mu           sync.RWMutex
}

var parenthesizedActress = regexp.MustCompile(`\(([^()]+)\)`)

func NewJavLibrary(timeout time.Duration, flareSolverr string, cooldown float64, log *slog.Logger) *JavLibrary {
	jar, _ := cookiejar.New(nil)
	if log == nil {
		log = slog.Default()
	}
	return &JavLibrary{client: &http.Client{Timeout: timeout, Jar: jar}, flareSolverr: strings.TrimRight(flareSolverr, "/"), cooldown: time.Duration(cooldown * float64(time.Second)), log: log}
}
func (j *JavLibrary) Configure(raw string, cooldown float64) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.flareSolverr = strings.TrimRight(raw, "/")
	j.cooldown = time.Duration(cooldown * float64(time.Second))
}

// normalizeJavLibraryURL forces any javlibrary.com/www.javlibrary.com URL to
// https://www.javlibrary.com, whatever scheme or host variant it arrived
// with. Older stored data (sites.url, releases.product_url) and links
// resolved from a stale cached page can still carry http:// or the bare
// "javlibrary.com" host; both are known to trigger 403s on direct fetches and
// mid-navigation http->https redirects that break FlareSolverr. URLs for any
// other host pass through unchanged.
func normalizeJavLibraryURL(raw string) string {
	u, e := url.Parse(raw)
	if e != nil {
		return raw
	}
	switch strings.ToLower(u.Host) {
	case "javlibrary.com", "www.javlibrary.com":
	default:
		return raw
	}
	u.Scheme = "https"
	u.Host = "www.javlibrary.com"
	return u.String()
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
// block or a transport/parse error gets scrapeRetryAttempts more tries (with
// a short backoff between them) before giving up, since both are commonly
// transient; a structurally-wrong page (ScrapeInvalid) is not retried.
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
	j.mu.RLock()
	solver, cooldown := j.flareSolverr, j.cooldown
	j.mu.RUnlock()

	if solver == "" {
		body, e := j.direct(ctx, raw)
		if e != nil {
			j.log.Info("scrape response rejected", "provider", "JavLibrary", "kind", kind, "url", raw, "status", string(ScrapeError), "reason", e.Error(), "via", "direct")
			return nil, statusErrorf(ScrapeError, "JavLibrary returned a Cloudflare challenge or unexpected page; configure flaresolverr_url (%s)", e.Error())
		}
		doc, parseErr := html.Parse(bytes.NewReader(body))
		if parseErr != nil {
			j.log.Info("scrape response rejected", "provider", "JavLibrary", "kind", kind, "url", raw, "status", string(ScrapeError), "reason", parseErr.Error(), "via", "direct")
			return nil, statusErrorf(ScrapeError, "JavLibrary returned a Cloudflare challenge or unexpected page; configure flaresolverr_url (%s)", parseErr.Error())
		}
		status, reason := validatePage("JavLibrary", kind, doc)
		if status == ScrapeValid {
			j.log.Info("scrape response valid", "provider", "JavLibrary", "kind", kind, "url", raw, "status", string(ScrapeValid), "bytes", len(body))
			return doc, nil
		}
		j.log.Info("scrape response rejected", "provider", "JavLibrary", "kind", kind, "url", raw, "status", string(status), "reason", reason, "via", "direct")
		if status == ScrapeBlocked || status == ScrapeInvalid {
			return nil, statusErrorf(status, "JavLibrary response %s: %s", status, reason)
		}
		return nil, statusErrorf(ScrapeError, "JavLibrary returned a Cloudflare challenge or unexpected page; configure flaresolverr_url (%s)", reason)
	}

	report(stage, StageConnectingFlareSolverr)
	j.log.Info("using FlareSolverr", "provider", "JavLibrary", "kind", kind, "url", raw, "solver", solver, "cooldown", cooldown)
	body, e := j.flare(ctx, raw, solver, cooldown)
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
		return nil, statusErrorf(status, "JavLibrary FlareSolverr response %s: %s", status, reason)
	}
	j.log.Info("scrape response valid", "provider", "JavLibrary", "kind", kind, "url", raw, "status", string(ScrapeValid), "via", "flaresolverr", "bytes", len(body))
	return doc, nil
}
func (j *JavLibrary) direct(ctx context.Context, raw string) ([]byte, error) {
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
func (j *JavLibrary) flare(ctx context.Context, raw, solver string, cooldown time.Duration) ([]byte, error) {
	if cooldown > 0 {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(cooldown):
		}
	}
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
		return nil, fmt.Errorf("FlareSolverr: %w", e)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		message := strings.TrimSpace(string(body))
		if message == "" {
			message = http.StatusText(resp.StatusCode)
		}
		return nil, fmt.Errorf("FlareSolverr returned HTTP %d: %s", resp.StatusCode, message)
	}
	var result struct {
		Status   string `json:"status"`
		Message  string `json:"message"`
		Solution struct {
			Response string `json:"response"`
		} `json:"solution"`
	}
	if e = json.NewDecoder(io.LimitReader(resp.Body, 10<<20)).Decode(&result); e != nil {
		return nil, fmt.Errorf("FlareSolverr returned invalid JSON: %w", e)
	}
	if result.Status != "ok" || result.Solution.Response == "" {
		return nil, fmt.Errorf("FlareSolverr: %s", result.Message)
	}
	return []byte(result.Solution.Response), nil
}
func (j *JavLibrary) Scrape(ctx context.Context, base string, pages int, progress ...Progress) ([]domain.Release, error) {
	return j.ScrapeFiltered(ctx, base, pages, nil, progress...)
}

func (j *JavLibrary) ScrapeFiltered(ctx context.Context, base string, pages int, include func(string) bool, progress ...Progress) ([]domain.Release, error) {
	return j.scrapeFiltered(ctx, base, pages, include, true, progress...)
}

// ScrapeFilteredThroughEnd keeps traversing when include rejects an entire
// page, and stops only when the site's online pagination ends.
func (j *JavLibrary) ScrapeFilteredThroughEnd(ctx context.Context, base string, pages int, include func(string) bool, progress ...Progress) ([]domain.Release, error) {
	return j.scrapeFiltered(ctx, base, pages, include, false, progress...)
}

func (j *JavLibrary) scrapeFiltered(ctx context.Context, base string, pages int, include func(string) bool, stopWhenNoIncluded bool, progress ...Progress) ([]domain.Release, error) {
	started := time.Now()
	if strings.TrimSpace(base) == "" {
		return nil, fmt.Errorf("JavLibrary site URL is required")
	}
	base = normalizeJavLibraryURL(base)
	if pages < 1 {
		pages = 1
	}
	seen := map[string]bool{}
	var out []domain.Release
	for page := 1; page <= pages; page++ {
		u, e := url.Parse(base)
		if e != nil {
			return nil, e
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
		reportedPageLimit := listingPageLimit(doc, "page", pages, page)
		j.log.Info("listing page parsed", "provider", "JavLibrary", "page", page, "entries", len(items), "url", u.String())
		if len(items) > 0 && len(progress) > 0 && progress[0] != nil {
			progress[0](page, reportedPageLimit, 0, len(items), "")
		}
		added := 0
		discovered := 0
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
				progress[0](page, reportedPageLimit, itemIndex+1, len(items), r.VideoID)
			}
			if include != nil && !include(r.VideoID) {
				continue
			}
			if detail, e := j.detail(ctx, href); e == nil {
				mergeJav(&r, detail)
			} else {
				j.log.Warn("product detail failed", "provider", "JavLibrary", "page", page, "video_id", r.VideoID, "url", href, "error", e)
			}
			out = append(out, r)
			added++
		}
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
		r.Actress = strings.Join(javLibraryCastNames(n), ", ")
	} else {
		var names []string
		for _, n := range findAll(doc, func(n *html.Node) bool { return hasClass(n, "cast") }) {
			for _, name := range javLibraryCastNames(n) {
				names = appendUnique(names, name)
			}
		}
		r.Actress = strings.Join(names, ", ")
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
	dst.Studio = src.Studio
	dst.Genres = src.Genres
	dst.Released = src.Released
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

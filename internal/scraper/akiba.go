package scraper

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	stdhtml "html"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/Net005/JAVBeacon/internal/domain"
	"golang.org/x/net/html"
)

type Akiba struct {
	base, path string
	client     *http.Client
	lastURL    string
	gateState  string
	log        *slog.Logger
}

var idPattern = regexp.MustCompile(`(?i)\b([A-Z]{2,8})\s*[-‐‑‒–—]\s*(\d{1,5})\b`)
var compactImageIDPattern = regexp.MustCompile(`(?i)(?:^|/)([a-z]{2,8})0*(\d{1,5})(?:[_-][^/]*)?\.(?:jpe?g|webp)(?:\?|$)`)
var datePattern = regexp.MustCompile(`(?i)(?:realease|release)\s*(?:day|date)?[^0-9]*(\d{4})[-/](\d{1,2})[-/](\d{1,2})`)
var productPattern = regexp.MustCompile(`(?i)[?&]product_id=([^&#]+)`)
var pagePattern = regexp.MustCompile(`([?&]count=)\d+`)

func NewAkiba(base, path string, timeout time.Duration, log *slog.Logger) *Akiba {
	jar, _ := cookiejar.New(nil)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ForceAttemptHTTP2 = false
	transport.TLSNextProto = map[string]func(string, *tls.Conn) http.RoundTripper{}
	if log == nil {
		log = slog.Default()
	}
	return &Akiba{base: strings.TrimRight(base, "/"), path: path, client: &http.Client{Timeout: timeout, Jar: jar, Transport: transport}, log: log}
}
func (a *Akiba) prepare(req *http.Request) {
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/132 Safari/537.36")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	req.Header.Set("Upgrade-Insecure-Requests", "1")
}
func (a *Akiba) prime(ctx context.Context) error {
	a.gateState = ""
	jar, _ := cookiejar.New(nil)
	a.client.Jar = jar

	noRedirect := *a.client
	noRedirect.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, a.base+"/cookie_set.php", nil)
	a.prepare(req)
	resp, err := noRedirect.Do(req)
	if err != nil {
		return err
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	req, _ = http.NewRequestWithContext(ctx, http.MethodGet, a.base+"/top.php", nil)
	a.prepare(req)
	resp, err = a.client.Do(req)
	if err != nil {
		return err
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	baseURL, _ := url.Parse(a.base + "/")
	for _, cookie := range a.client.Jar.Cookies(baseURL) {
		if cookie.Name == "old_check" || cookie.Name == "layout" {
			a.gateState += cookie.Name + "=" + cookie.Value + ";"
		}
		if cookie.Name == "PHPSESSID" {
			a.gateState += "session=present;"
		}
	}
	return nil
}

// fetch retrieves and parses one page and validates it before returning it,
// so a Cloudflare/anti-bot interstitial or an otherwise-wrong page (gate
// redirect, error page, changed layout) is never handed to a caller as if it
// were real site content. kind identifies what shape of page is expected
// ("listing" or "detail") for validatePage. GIGA has no FlareSolverr
// integration, so a blocked or invalid page is a hard error here.
//
// fetch itself is a thin retry wrapper around fetchOnce: a Cloudflare block
// or a transport/parse error gets scrapeRetryAttempts more tries (with a
// short backoff between them) before giving up, since both are commonly
// transient; a structurally-wrong page (ScrapeInvalid) is not retried.
func (a *Akiba) fetch(ctx context.Context, raw, kind string, stage ...DetailStage) (*html.Node, error) {
	return withScrapeRetry(ctx, func() (*html.Node, error) {
		return a.fetchOnce(ctx, raw, kind, stage...)
	}, func(attempt int, wait time.Duration, err error) {
		a.log.Info("scrape retry", "provider", "GIGA", "kind", kind, "url", raw, "attempt", attempt, "wait", wait.String(), "reason", err.Error())
	})
}

func (a *Akiba) fetchOnce(ctx context.Context, raw, kind string, stage ...DetailStage) (*html.Node, error) {
	report(stage, StageConnecting)
	req, e := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if e != nil {
		return nil, e
	}
	a.prepare(req)
	req.Header.Set("Referer", a.base)
	resp, e := a.client.Do(req)
	if e != nil {
		return nil, e
	}
	defer resp.Body.Close()
	a.lastURL = resp.Request.URL.String()
	if resp.StatusCode/100 != 2 {
		a.log.Info("scrape response rejected", "provider", "GIGA", "kind", kind, "url", raw, "final_url", a.lastURL, "status", string(ScrapeError), "reason", resp.Status)
		return nil, statusErrorf(ScrapeError, "akiba returned %s", resp.Status)
	}
	doc, parseErr := html.Parse(resp.Body)
	if parseErr != nil {
		a.log.Info("scrape response rejected", "provider", "GIGA", "kind", kind, "url", raw, "final_url", a.lastURL, "status", string(ScrapeError), "reason", parseErr.Error())
		return nil, statusErrorf(ScrapeError, "%s", parseErr.Error())
	}
	if status, reason := validatePage("GIGA", kind, doc); status != ScrapeValid {
		a.log.Info("scrape response rejected", "provider", "GIGA", "kind", kind, "url", raw, "final_url", a.lastURL, "status", string(status), "reason", reason)
		return nil, statusErrorf(status, "GIGA response %s: %s (final_url: %s, gate: %s)", status, reason, a.lastURL, a.gateState)
	}
	a.log.Info("scrape response valid", "provider", "GIGA", "kind", kind, "url", raw, "final_url", a.lastURL, "status", string(ScrapeValid))
	return doc, nil
}

func (a *Akiba) Scrape(ctx context.Context, pages int, progress ...Progress) ([]domain.Release, error) {
	return a.ScrapeFiltered(ctx, pages, nil, progress...)
}

func (a *Akiba) ScrapeFiltered(ctx context.Context, pages int, include func(string) bool, progress ...Progress) ([]domain.Release, error) {
	return a.scrapeFiltered(ctx, pages, include, true, progress...)
}

// ScrapeFilteredThroughEnd continues across listing pages even when a page only
// contains releases rejected by include. Pagination still stops at the site's
// actual empty/repeated end, with pages acting only as a safety ceiling.
func (a *Akiba) ScrapeFilteredThroughEnd(ctx context.Context, pages int, include func(string) bool, progress ...Progress) ([]domain.Release, error) {
	return a.scrapeFiltered(ctx, pages, include, false, progress...)
}

func (a *Akiba) scrapeFiltered(ctx context.Context, pages int, include func(string) bool, stopWhenNoIncluded bool, progress ...Progress) ([]domain.Release, error) {
	started := time.Now()
	if pages < 1 {
		pages = 1
	}
	if err := a.prime(ctx); err != nil {
		return nil, fmt.Errorf("prime session: %w", err)
	}
	a.log.Info("scrape session ready", "provider", "GIGA", "pages", pages, "gate", a.gateState)
	seen := map[string]bool{}
	var out []domain.Release
	for page := 1; page <= pages; page++ {
		rawURL := pagePattern.ReplaceAllString(a.base+a.path, "${1}"+fmt.Sprint(page))
		a.log.Info("scraping listing page", "provider", "GIGA", "page", page, "page_limit", pages, "url", rawURL)
		doc, e := a.fetch(ctx, rawURL, "listing")
		if e != nil {
			return nil, e
		}
		cards := findAll(doc, func(n *html.Node) bool { return hasClass(n, "search_sam_box") || hasClass(n, "sam_box") })
		reportedPageLimit := listingPageLimit(doc, "count", pages, page)
		a.log.Info("listing page parsed", "provider", "GIGA", "page", page, "cards", len(cards), "final_url", a.lastURL)
		if len(cards) == 0 {
			if page > 1 {
				if len(progress) > 0 && progress[0] != nil {
					progress[0](page-1, page-1, 0, 0, "")
				}
				a.log.Info("online listing end reached", "provider", "GIGA", "last_page", page-1)
				break
			}
			title := first(doc, func(n *html.Node) bool { return n.Data == "title" })
			pageTitle := "unknown page"
			if title != nil {
				pageTitle = nodeText(title)
			}
			return nil, fmt.Errorf("Akiba listing redirected or changed: .search_sam_box missing (page: %s, final_url: %s, gate: %s)", pageTitle, a.lastURL, a.gateState)
		}
		if len(progress) > 0 && progress[0] != nil {
			progress[0](page, reportedPageLimit, 0, len(cards), "")
		}
		added := 0
		discovered := 0
		for cardIndex, card := range cards {
			r, ok := a.card(card)
			if !ok || seen[r.ProductURL] {
				continue
			}
			seen[r.ProductURL] = true
			discovered++
			if len(progress) > 0 && progress[0] != nil {
				progress[0](page, reportedPageLimit, cardIndex+1, len(cards), r.VideoID)
			}
			if include != nil && !include(r.VideoID) {
				continue
			}
			detail, e := a.detail(ctx, r.ProductURL)
			if e == nil {
				merge(&r, detail)
			} else {
				a.log.Warn("product detail failed", "provider", "GIGA", "page", page, "video_id", r.VideoID, "url", r.ProductURL, "error", e)
			}
			out = append(out, r)
			added++
		}
		if discovered == 0 && page > 1 {
			if len(progress) > 0 && progress[0] != nil {
				progress[0](page-1, page-1, 0, 0, "")
			}
			a.log.Info("online listing end reached", "provider", "GIGA", "last_page", page-1, "reason", "repeated page")
			break
		}
		if added == 0 && (stopWhenNoIncluded || include == nil) {
			a.log.Info("listing page contained no new releases", "provider", "GIGA", "page", page)
			break
		}
		a.log.Info("listing page completed", "provider", "GIGA", "page", page, "releases", added, "total", len(out))
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ReleaseDate > out[j].ReleaseDate })
	a.log.Info("provider scrape completed", "provider", "GIGA", "releases", len(out), "duration", time.Since(started).Round(time.Millisecond))
	return out, nil
}
func (a *Akiba) card(card *html.Node) (domain.Release, bool) {
	text := nodeText(card)
	m := idPattern.FindStringSubmatch(text)
	r := domain.Release{Source: "GIGA"}
	if len(m) > 0 {
		r.VideoID = strings.ToUpper(m[1]) + "-" + strings.TrimLeft(m[2], "0")
		if strings.HasSuffix(r.VideoID, "-") {
			r.VideoID += "0"
		}
	}
	for _, n := range findAll(card, func(n *html.Node) bool { return n.Type == html.ElementNode }) {
		if n.Data == "a" {
			href := attr(n, "href")
			if strings.Contains(href, "product_id") {
				r.ProductURL = resolve(a.base, href)
				r.Title = strings.TrimSpace(nodeText(n))
				if pm := productPattern.FindStringSubmatch(href); len(pm) > 1 {
					r.ScraperID = pm[1]
				}
			}
		}
		if n.Data == "img" && r.ImageURL == "" {
			r.ImageURL = resolve(a.base, attr(n, "src"))
			if r.VideoID == "" {
				if match := compactImageIDPattern.FindStringSubmatch(attr(n, "src")); len(match) > 0 {
					number := strings.TrimLeft(match[2], "0")
					if number == "" {
						number = "0"
					}
					r.VideoID = strings.ToUpper(match[1]) + "-" + number
				}
			}
		}
	}
	if dm := datePattern.FindStringSubmatch(text); len(dm) > 0 {
		r.ReleaseDate = fmt.Sprintf("%s-%02s-%02s", dm[1], dm[2], dm[3])
	}
	r.Released = isReleased(r.ReleaseDate, text)
	return r, r.ProductURL != "" && r.VideoID != ""
}
func (a *Akiba) detail(ctx context.Context, raw string, stage ...DetailStage) (domain.Release, error) {
	doc, e := a.fetch(ctx, raw, "detail", stage...)
	if e != nil {
		return domain.Release{}, e
	}
	report(stage, StageParsing)
	r := domain.Release{Studio: "GIGA", Genres: []string{"Fighting Action", "Fighters"}}
	if n := first(doc, func(n *html.Node) bool {
		return (n.Data == "h5" && hasAncestorID(n, "works_pic")) || (n.Data == "b" && hasAncestorID(n, "works_txt")) || n.Data == "h1"
	}); n != nil {
		r.Title = nodeText(n)
	}
	if n := first(doc, func(n *html.Node) bool { return n.Data == "img" && hasAncestorID(n, "works_pic") }); n != nil {
		r.ImageURL = resolve(a.base, attr(n, "src"))
	}
	for _, dt := range findAll(doc, func(n *html.Node) bool { return n.Data == "dt" && hasAncestorID(n, "works_txt") }) {
		label := strings.ToLower(nodeText(dt))
		dd := nextElement(dt)
		if dd == nil {
			continue
		}
		v := nodeText(dd)
		switch {
		case strings.Contains(label, "actress") || hasClass(dd, "yaku"):
			r.Actress = v
		case strings.Contains(label, "director"):
			r.Director = v
		case strings.Contains(label, "time"):
			r.Duration = v
		case strings.Contains(label, "release date"):
			r.ReleaseDate = normalizeDate(v)
		}
	}
	if n := first(doc, func(n *html.Node) bool {
		return hasClass(n, "story_window") && (hasAncestorID(n, "story_list2") || hasAncestorID(n, "story_list1"))
	}); n != nil {
		r.Story = nodeText(n)
	}
	for _, n := range findAll(doc, func(n *html.Node) bool {
		return n.Data == "a" && strings.Contains(attr(n, "href"), "_l.jpg") && (hasAncestorID(n, "sample_list") || hasClassAncestor(n, "gasatsu_images_pc"))
	}) {
		r.Screenshots = appendUnique(r.Screenshots, resolve(a.base, attr(n, "href")))
	}
	return r, nil
}

// Refresh fetches only the stored product page and merges its current details.
func (a *Akiba) Refresh(ctx context.Context, release domain.Release, stage ...DetailStage) (domain.Release, error) {
	if strings.TrimSpace(release.ProductURL) == "" {
		return release, errors.New("release product URL is missing")
	}
	report(stage, StageConnecting)
	if err := a.prime(ctx); err != nil {
		return release, statusErrorf(ScrapeError, "prime session: %s", err.Error())
	}
	detail, err := a.detail(ctx, release.ProductURL, stage...)
	if err != nil {
		return release, err
	}
	merge(&release, detail)
	return release, nil
}
func merge(dst *domain.Release, src domain.Release) {
	if src.Title != "" {
		dst.Title = src.Title
	}
	if src.ImageURL != "" {
		dst.ImageURL = src.ImageURL
	}
	if src.ReleaseDate != "" {
		dst.ReleaseDate = src.ReleaseDate
	}
	dst.Actress = src.Actress
	dst.Director = src.Director
	dst.Studio = src.Studio
	dst.Genres = src.Genres
	dst.Duration = src.Duration
	dst.Story = src.Story
	dst.Screenshots = src.Screenshots
	dst.Released = isReleased(dst.ReleaseDate, "")
}
func isReleased(date, text string) bool {
	low := strings.ToLower(text)
	if strings.Contains(low, "download.php?product_id") || strings.Contains(low, "now on sale") {
		return true
	}
	t, e := time.Parse("2006-01-02", date)
	return e == nil && !t.After(time.Now())
}
func normalizeDate(v string) string {
	v = strings.TrimSpace(strings.ReplaceAll(v, "/", "-"))
	if t, e := time.Parse("2006-1-2", v); e == nil {
		return t.Format("2006-01-02")
	}
	return v
}
func resolve(base, ref string) string {
	u, e := url.Parse(strings.TrimSpace(ref))
	if e != nil || ref == "" {
		return ""
	}
	b, _ := url.Parse(base + "/")
	return b.ResolveReference(u).String()
}
func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return stdhtml.UnescapeString(a.Val)
		}
	}
	return ""
}
func hasClass(n *html.Node, c string) bool {
	for _, v := range strings.Fields(attr(n, "class")) {
		if v == c {
			return true
		}
	}
	return false
}
func hasAncestorID(n *html.Node, id string) bool {
	for p := n.Parent; p != nil; p = p.Parent {
		if attr(p, "id") == id {
			return true
		}
	}
	return false
}
func hasClassAncestor(n *html.Node, c string) bool {
	for p := n.Parent; p != nil; p = p.Parent {
		if hasClass(p, c) {
			return true
		}
	}
	return false
}
func findAll(n *html.Node, p func(*html.Node) bool) []*html.Node {
	var out []*html.Node
	var walk func(*html.Node)
	walk = func(x *html.Node) {
		if p(x) {
			out = append(out, x)
		}
		for c := x.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return out
}
func first(n *html.Node, p func(*html.Node) bool) *html.Node {
	x := findAll(n, p)
	if len(x) > 0 {
		return x[0]
	}
	return nil
}
func nodeText(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(x *html.Node) {
		if x.Type == html.TextNode {
			b.WriteString(x.Data)
			b.WriteByte(' ')
		}
		for c := x.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return strings.Join(strings.Fields(stdhtml.UnescapeString(b.String())), " ")
}
func nextElement(n *html.Node) *html.Node {
	for x := n.NextSibling; x != nil; x = x.NextSibling {
		if x.Type == html.ElementNode {
			return x
		}
	}
	return nil
}
func appendUnique(xs []string, v string) []string {
	for _, x := range xs {
		if x == v {
			return xs
		}
	}
	return append(xs, v)
}

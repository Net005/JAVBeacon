package download

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Net005/JAVBeacon/internal/domain"
	"golang.org/x/net/html"
)

const (
	defaultJavDBURL      = "https://javdb.com"
	pikPakAPIHost        = "https://api-drive.mypikpak.com"
	pikPakUserHost       = "https://user.mypikpak.com"
	pikPakClientID       = "YUMx5nI8ZU8Ap8pm"
	pikPakClientVersion  = "2.0.0"
	pikPakPackageName    = "mypikpak.com"
	publicShareUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/117.0.0.0 Safari/537.36"
)

var pikPakAlgorithms = []string{
	"C9qPpZLN8ucRTaTiUMWYS9cQvWOE", "+r6CQVxjzJV6LCV", "F", "pFJRC",
	"9WXYIDGrwTCz2OiVlgZa90qpECPD6olt", "/750aCr4lm/Sly/c", "RB+DT/gZCrbV", "",
	"CyLsf7hdkIRxRm215hl", "7xHvLi2tOYP0Y92b", "ZGTXXxu8E/MIWaEDB+Sm/", "1UI3",
	"E7fP5Pfijd+7K+t6Tg/NhuLq0eEUVChpJSkrKxpO", "ihtqpG6FMt65+Xk+tWUH2", "NhXXU9rg4XXdzo7u5o",
}

type javDBProvider struct {
	client           *http.Client
	baseURL          string
	acceptedPatterns []string
}

// HTTPSourceProvider is the extension point for direct-download sources.
// Provider modules own discovery and link resolution; the shared download
// service owns queuing, concurrency, progress, retry, naming, and pipelines.
type HTTPSourceProvider interface {
	Name() string
	Search(context.Context, domain.Release) ([]domain.SearchResult, error)
	CanResolve(domain.Download) bool
	Resolve(context.Context, domain.Download) (resolvedHTTPFile, error)
}

type resolvedHTTPFile struct {
	URL     string
	Name    string
	Size    int64
	Headers map[string]string
}

func httpSourceProviders(client *http.Client, settings map[string]string) []HTTPSourceProvider {
	patterns := strings.FieldsFunc(settings["accepted_patterns"], func(r rune) bool { return r == '\n' || r == ',' })
	return []HTTPSourceProvider{&javDBProvider{client: client, baseURL: settings["javdb_url"], acceptedPatterns: patterns}}
}

func (p *javDBProvider) Name() string { return "JavDB / Keepshare" }

func (p *javDBProvider) CanResolve(download domain.Download) bool {
	if download.Provider == p.Name() {
		return true
	}
	u, err := url.Parse(download.SourceReference)
	return err == nil && strings.EqualFold(u.Hostname(), "keepshare.org")
}

func (p *javDBProvider) Resolve(ctx context.Context, download domain.Download) (resolvedHTTPFile, error) {
	direct, name, size, err := resolvePikPakShare(ctx, p.client, download.SourceReference, download.Query, p.acceptedPatterns)
	return resolvedHTTPFile{URL: direct, Name: name, Size: size, Headers: map[string]string{"User-Agent": publicShareUserAgent, "Referer": "https://mypikpak.com/"}}, err
}

func normalizeReleaseID(s string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(s) {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func releaseIDMatchesText(text, releaseID string) bool {
	parts := regexp.MustCompile(`[A-Z]+|[0-9]+`).FindAllString(strings.ToUpper(normalizeReleaseID(releaseID)), -1)
	if len(parts) == 0 {
		return false
	}
	pattern := `(?i)(?:^|[^A-Z0-9])` + strings.Join(parts, `[-_ .]*`) + `(?:[-_ .]*U)?(?:[^A-Z0-9]|$)`
	return regexp.MustCompile(pattern).MatchString(text)
}

func (p *javDBProvider) Search(ctx context.Context, release domain.Release) ([]domain.SearchResult, error) {
	base := strings.TrimRight(strings.TrimSpace(p.baseURL), "/")
	if base == "" {
		base = defaultJavDBURL
	}
	searchURL := base + "/search?q=" + url.QueryEscape(release.VideoID) + "&f=all"
	doc, err := p.getHTML(ctx, searchURL)
	if err != nil {
		return nil, fmt.Errorf("JavDB search failed: %w", err)
	}
	type hit struct{ href, id, date string }
	var hits []hit
	for _, item := range descendantsWithClass(doc, "item") {
		a := firstDescendant(item, "a", "box")
		strong := firstDescendant(item, "strong", "")
		meta := firstDescendant(item, "div", "meta")
		if a == nil || strong == nil || normalizeReleaseID(nodeText(strong)) != normalizeReleaseID(release.VideoID) {
			continue
		}
		href := attrValue(a, "href")
		if href == "" {
			continue
		}
		hits = append(hits, hit{href: resolveURL(base, href), id: strings.TrimSpace(nodeText(strong)), date: strings.TrimSpace(nodeText(meta))})
	}
	if len(hits) == 0 {
		return []domain.SearchResult{}, nil
	}
	knownDate, _ := time.Parse("2006-01-02", release.ReleaseDate)
	var rows []domain.SearchResult
	for _, h := range hits {
		pageDate := parseJavDBDate(h.date)
		if !knownDate.IsZero() && !pageDate.IsZero() {
			delta := pageDate.Sub(knownDate)
			if delta < -60*24*time.Hour || delta > 60*24*time.Hour {
				continue
			}
		}
		page, getErr := p.getHTML(ctx, h.href)
		if getErr != nil {
			continue
		}
		rows = append(rows, parseJavDBDownloadCandidates(page, h.href, release.VideoID)...)
	}
	// JavDB's visible row title is not always the actual video filename in
	// the Keepshare/PikPak share. Inspect every distinct candidate now so
	// preferred filename patterns rank the real downloadable file. A blocked
	// or expired share retains its JavDB metadata and naturally falls through
	// to the established non-U/filesize ordering instead of hiding the result.
	var wg sync.WaitGroup
	for i := range rows {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			_, _, selected, inspectErr := inspectPikPakShare(ctx, p.client, rows[index].Link, release.VideoID, p.acceptedPatterns)
			if inspectErr != nil {
				return
			}
			rows[index].Title = selected.Name
			if size, parseErr := strconv.ParseInt(selected.Size, 10, 64); parseErr == nil && size > 0 {
				rows[index].SizeBytes = size
			}
		}(i)
	}
	wg.Wait()
	sortJavDBDownloadCandidates(rows, release.VideoID, p.acceptedPatterns)
	return rows, nil
}

func sortJavDBDownloadCandidates(rows []domain.SearchResult, releaseID string, patterns []string) {
	sort.SliceStable(rows, func(i, j int) bool {
		iPreferred, _ := matchesAcceptedHTTPPattern(rows[i].Title, patterns)
		jPreferred, _ := matchesAcceptedHTTPPattern(rows[j].Title, patterns)
		if iPreferred != jPreferred {
			return iPreferred
		}
		iU, jU := hasUVariant(rows[i].Title, releaseID), hasUVariant(rows[j].Title, releaseID)
		if iU != jU {
			return !iU
		}
		return rows[i].SizeBytes > rows[j].SizeBytes
	})
	for i := range rows {
		if preferred, pattern := matchesAcceptedHTTPPattern(rows[i].Title, patterns); preferred {
			rows[i].Reason = "preferred HTTP filename matched accepted pattern " + pattern
		}
	}
}

func matchesAcceptedHTTPPattern(name string, patterns []string) (bool, string) {
	if len(patterns) == 0 {
		patterns = []string{"4k688.com@", "hhd800.com@"}
	}
	name = strings.ToLower(name)
	for _, pattern := range patterns {
		pattern = strings.TrimSpace(pattern)
		if pattern != "" && strings.Contains(name, strings.ToLower(pattern)) {
			return true, pattern
		}
	}
	return false, ""
}

func (p *javDBProvider) getHTML(ctx context.Context, raw string) (*html.Node, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", publicShareUserAgent)
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return html.Parse(io.LimitReader(resp.Body, 8<<20))
}

func parseJavDBDownloadCandidates(doc *html.Node, sourceURL, releaseID string) []domain.SearchResult {
	var out []domain.SearchResult
	seenLinks := map[string]bool{}
	for _, item := range descendantsWithClass(doc, "item") {
		nameNode := firstDescendant(item, "span", "name")
		if nameNode == nil {
			continue
		}
		name := strings.TrimSpace(nodeText(nameNode))
		if !releaseIDMatchesText(name, releaseID) {
			continue
		}
		var keeps []string
		for _, a := range descendants(item, "a") {
			href := html.UnescapeString(attrValue(a, "href"))
			if u, err := url.Parse(href); err == nil && strings.EqualFold(u.Hostname(), "keepshare.org") && !seenLinks[href] {
				seenLinks[href] = true
				keeps = append(keeps, href)
			}
		}
		if len(keeps) == 0 {
			continue
		}
		meta := nodeText(firstDescendant(item, "span", "meta"))
		size := parseHumanBytes(meta)
		if raw := attrValue(item, "data-size"); size == 0 && raw != "" {
			mb, _ := strconv.ParseInt(raw, 10, 64)
			size = mb << 20
		}
		published := strings.TrimSpace(nodeText(firstDescendant(item, "span", "time")))
		for _, keep := range keeps {
			out = append(out, domain.SearchResult{Provider: "JavDB / Keepshare", Title: name, Link: keep, SourceURL: sourceURL, Transport: "http", SizeBytes: size, PublishedAt: published, Accepted: true, Reason: "exact release ID match available as HTTP download"})
		}
	}
	return out
}

var humanSizePattern = regexp.MustCompile(`(?i)([0-9]+(?:\.[0-9]+)?)\s*(TB|GB|MB|KB)`)

func parseHumanBytes(s string) int64 {
	m := humanSizePattern.FindStringSubmatch(s)
	if m == nil {
		return 0
	}
	v, _ := strconv.ParseFloat(m[1], 64)
	power := map[string]int{"KB": 10, "MB": 20, "GB": 30, "TB": 40}[strings.ToUpper(m[2])]
	return int64(v * float64(int64(1)<<power))
}

func parseJavDBDate(s string) time.Time {
	for _, layout := range []string{"01/02/2006", "2006-01-02", "2006/01/02"} {
		if t, err := time.Parse(layout, strings.TrimSpace(s)); err == nil {
			return t
		}
	}
	return time.Time{}
}

func hasUVariant(name, releaseID string) bool {
	n, id := normalizeReleaseID(name), normalizeReleaseID(releaseID)
	return strings.Contains(n, id+"U")
}

func resolveURL(base, ref string) string {
	b, e1 := url.Parse(base)
	r, e2 := url.Parse(ref)
	if e1 != nil || e2 != nil {
		return ref
	}
	return b.ResolveReference(r).String()
}

func descendants(root *html.Node, tag string) []*html.Node {
	var out []*html.Node
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == tag {
			out = append(out, n)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	if root != nil {
		walk(root)
	}
	return out
}
func descendantsWithClass(root *html.Node, class string) []*html.Node {
	var out []*html.Node
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && hasClass(n, class) {
			out = append(out, n)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	if root != nil {
		walk(root)
	}
	return out
}
func firstDescendant(root *html.Node, tag, class string) *html.Node {
	if root == nil {
		return nil
	}
	if root.Type == html.ElementNode && root.Data == tag && (class == "" || hasClass(root, class)) {
		return root
	}
	for c := root.FirstChild; c != nil; c = c.NextSibling {
		if n := firstDescendant(c, tag, class); n != nil {
			return n
		}
	}
	return nil
}
func hasClass(n *html.Node, want string) bool {
	for _, c := range strings.Fields(attrValue(n, "class")) {
		if c == want {
			return true
		}
	}
	return false
}
func attrValue(n *html.Node, key string) string {
	if n == nil {
		return ""
	}
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}
func nodeText(n *html.Node) string {
	if n == nil {
		return ""
	}
	if n.Type == html.TextNode {
		return n.Data
	}
	var b strings.Builder
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		b.WriteString(nodeText(c))
		b.WriteByte(' ')
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

type pikPakClient struct {
	http                   *http.Client
	deviceID, captchaToken string
}
type pikPakFile struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Kind           string `json:"kind"`
	Size           string `json:"size"`
	WebContentLink string `json:"web_content_link"`
	Medias         []struct {
		Link struct {
			URL string `json:"url"`
		} `json:"link"`
		IsOrigin bool `json:"is_origin"`
	} `json:"medias"`
}
type pikPakResponse struct {
	ErrorCode        int          `json:"error_code"`
	Error            string       `json:"error"`
	ErrorDescription string       `json:"error_description"`
	ShareStatus      string       `json:"share_status"`
	ShareStatusText  string       `json:"share_status_text"`
	NextPageToken    string       `json:"next_page_token"`
	Files            []pikPakFile `json:"files"`
	FileInfo         pikPakFile   `json:"file_info"`
}

func newPikPakClient(client *http.Client) *pikPakClient {
	now := strconv.FormatInt(time.Now().UnixNano(), 16)
	sum := md5.Sum([]byte(now))
	return &pikPakClient{http: client, deviceID: hex.EncodeToString(sum[:])}
}
func (p *pikPakClient) captchaSign(ts string) string {
	s := pikPakClientID + pikPakClientVersion + pikPakPackageName + p.deviceID + ts
	for _, salt := range pikPakAlgorithms {
		x := md5.Sum([]byte(s + salt))
		s = hex.EncodeToString(x[:])
	}
	return "1." + s
}
func (p *pikPakClient) refreshCaptcha(ctx context.Context, action string) error {
	ts := strconv.FormatInt(time.Now().UnixMilli(), 10)
	body := map[string]any{"action": action, "captcha_token": p.captchaToken, "client_id": pikPakClientID, "device_id": p.deviceID, "meta": map[string]string{"captcha_sign": p.captchaSign(ts), "client_version": pikPakClientVersion, "package_name": pikPakPackageName, "timestamp": ts, "user_id": ""}, "redirect_uri": ""}
	b, _ := json.Marshal(body)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, pikPakUserHost+"/v1/shield/captcha/init", strings.NewReader(string(b)))
	p.setHeaders(req)
	var out struct {
		CaptchaToken     string `json:"captcha_token"`
		ErrorDescription string `json:"error_description"`
	}
	if err := p.doJSON(req, &out); err != nil {
		return err
	}
	if out.CaptchaToken == "" {
		return errors.New("PikPak did not issue an anonymous CAPTCHA token: " + out.ErrorDescription)
	}
	p.captchaToken = out.CaptchaToken
	return nil
}
func (p *pikPakClient) setHeaders(req *http.Request) {
	req.Header.Set("User-Agent", publicShareUserAgent)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Client-ID", pikPakClientID)
	req.Header.Set("X-Device-ID", p.deviceID)
	req.Header.Set("X-Captcha-Token", p.captchaToken)
	req.Header.Set("Referer", "https://mypikpak.com/")
}
func (p *pikPakClient) doJSON(req *http.Request, out any) error {
	resp, err := p.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(out)
}
func (p *pikPakClient) request(ctx context.Context, path string, q url.Values) (pikPakResponse, error) {
	action := "GET:" + path
	if p.captchaToken == "" {
		if err := p.refreshCaptcha(ctx, action); err != nil {
			return pikPakResponse{}, err
		}
	}
	do := func() (pikPakResponse, error) {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, pikPakAPIHost+path+"?"+q.Encode(), nil)
		p.setHeaders(req)
		var out pikPakResponse
		err := p.doJSON(req, &out)
		return out, err
	}
	out, err := do()
	if err == nil && out.ErrorCode == 9 {
		if err = p.refreshCaptcha(ctx, action); err == nil {
			out, err = do()
		}
	}
	if err != nil {
		return out, err
	}
	if out.ErrorCode != 0 {
		return out, fmt.Errorf("PikPak error %d: %s", out.ErrorCode, firstNonEmpty(out.ErrorDescription, out.Error))
	}
	return out, nil
}
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return "unknown error"
}

var publicShareURLPattern = regexp.MustCompile(`https://mypikpak\.com/s/([A-Za-z0-9_-]+)(?:/([A-Za-z0-9_-]+))?`)

func (p *pikPakClient) listShareFiles(ctx context.Context, shareID, parentID string) ([]pikPakFile, error) {
	var all []pikPakFile
	page := ""
	for {
		q := url.Values{"share_id": {shareID}, "parent_id": {parentID}, "thumbnail_size": {"SIZE_LARGE"}, "with_audit": {"true"}, "limit": {"100"}, "page_token": {page}, "filters": {`{"phase":{"eq":"PHASE_TYPE_COMPLETE"},"trashed":{"eq":false}}`}}
		detail, err := p.request(ctx, "/drive/v1/share/detail", q)
		if err != nil {
			return nil, err
		}
		if detail.ShareStatus != "" && detail.ShareStatus != "OK" {
			return nil, fmt.Errorf("PikPak share unavailable: %s %s", detail.ShareStatus, detail.ShareStatusText)
		}
		for _, file := range detail.Files {
			if file.Kind == "drive#folder" {
				children, childErr := p.listShareFiles(ctx, shareID, file.ID)
				if childErr != nil {
					return nil, childErr
				}
				all = append(all, children...)
				continue
			}
			all = append(all, file)
		}
		page = detail.NextPageToken
		if page == "" {
			return all, nil
		}
	}
}

func inspectPikPakShare(ctx context.Context, client *http.Client, keepshareURL, releaseID string, preferredPatterns []string) (*pikPakClient, string, pikPakFile, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, keepshareURL, nil)
	req.Header.Set("User-Agent", publicShareUserAgent)
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", pikPakFile{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, "", pikPakFile{}, err
	}
	shareURL := resp.Request.URL.String()
	m := publicShareURLPattern.FindStringSubmatch(shareURL)
	if m == nil {
		m = publicShareURLPattern.FindStringSubmatch(string(body))
	}
	if m == nil {
		return nil, "", pikPakFile{}, errors.New("Keepshare did not resolve to a PikPak public share")
	}
	shareID := m[1]
	pp := newPikPakClient(client)
	all, err := pp.listShareFiles(ctx, shareID, "")
	if err != nil {
		return nil, "", pikPakFile{}, err
	}
	selected, found := selectPikPakFile(all, releaseID, preferredPatterns)
	if !found {
		return nil, "", pikPakFile{}, fmt.Errorf("PikPak share contained no file matching %s", releaseID)
	}
	return pp, shareID, selected, nil
}

func selectPikPakFile(files []pikPakFile, releaseID string, preferredPatterns []string) (pikPakFile, bool) {
	var selected pikPakFile
	found := false
	for i := range files {
		f := files[i]
		if f.Kind == "drive#folder" || !releaseIDMatchesText(f.Name, releaseID) {
			continue
		}
		size, _ := strconv.ParseInt(f.Size, 10, 64)
		preferred, _ := matchesAcceptedHTTPPattern(f.Name, preferredPatterns)
		oldPreferred, _ := matchesAcceptedHTTPPattern(selected.Name, preferredPatterns)
		oldSize, _ := strconv.ParseInt(selected.Size, 10, 64)
		if !found || (preferred && !oldPreferred) || preferred == oldPreferred && size > oldSize {
			selected, found = f, true
		}
	}
	return selected, found
}

func resolvePikPakShare(ctx context.Context, client *http.Client, keepshareURL, releaseID string, preferredPatterns []string) (string, string, int64, error) {
	pp, shareID, selected, err := inspectPikPakShare(ctx, client, keepshareURL, releaseID, preferredPatterns)
	if err != nil {
		return "", "", 0, err
	}
	info, err := pp.request(ctx, "/drive/v1/share/file_info", url.Values{"share_id": {shareID}, "file_id": {selected.ID}})
	if err != nil {
		return "", "", 0, err
	}
	file := info.FileInfo
	// web_content_link is PikPak's original-file stream (the same URL exposed
	// when the public player is opened). Always prefer it over the media array,
	// whose entries may be bandwidth-saving transcodes. If it is absent, prefer
	// an explicitly marked original media before the provider's best fallback.
	direct := file.WebContentLink
	if direct == "" {
		for _, media := range file.Medias {
			if media.IsOrigin && media.Link.URL != "" {
				direct = media.Link.URL
				break
			}
		}
		if direct == "" && len(file.Medias) > 0 {
			direct = file.Medias[0].Link.URL
		}
	}
	if direct == "" {
		return "", "", 0, errors.New("PikPak did not return a downloadable URL for the matching file")
	}
	size, _ := strconv.ParseInt(selected.Size, 10, 64)
	return direct, selected.Name, size, nil
}

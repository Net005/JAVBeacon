package download

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
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
	log              *slog.Logger
	inspectCandidate func(context.Context, string, string) (pikPakFile, []pikPakFile, error)
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

func httpSourceProviders(client *http.Client, settings map[string]string, logger *slog.Logger) []HTTPSourceProvider {
	patterns := strings.FieldsFunc(settings["accepted_patterns"], func(r rune) bool { return r == '\n' || r == ',' })
	return []HTTPSourceProvider{&javDBProvider{client: client, baseURL: settings["javdb_url"], acceptedPatterns: patterns, log: logger}}
}

func (p *javDBProvider) Name() string { return "JavDB / Keepshare" }

func (p *javDBProvider) CanResolve(download domain.Download) bool {
	if download.Provider == p.Name() {
		return true
	}
	u, err := url.Parse(download.SourceReference)
	return err == nil && isJavDBShareURL(u)
}

func (p *javDBProvider) Resolve(ctx context.Context, download domain.Download) (resolvedHTTPFile, error) {
	const attempts = 3
	var direct, name string
	var size int64
	var err error
	for attempt := 0; attempt < attempts; attempt++ {
		if attempt > 0 {
			timer := time.NewTimer(time.Duration(attempt) * 500 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return resolvedHTTPFile{}, ctx.Err()
			case <-timer.C:
			}
		}
		direct, name, size, err = resolvePikPakShare(ctx, p.client, download.SourceReference, download.Query, p.acceptedPatterns)
		if err == nil {
			return resolvedHTTPFile{URL: direct, Name: name, Size: size, Headers: map[string]string{"User-Agent": publicShareUserAgent, "Referer": "https://mypikpak.com/"}}, nil
		}
	}
	return resolvedHTTPFile{}, fmt.Errorf("PikPak resolution failed after %d attempts: %w", attempts, err)
}

func normalizeReleaseID(s string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(strings.TrimSpace(s)) {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func releaseIDsEqual(a, b string) bool {
	a, b = normalizeReleaseID(a), normalizeReleaseID(b)
	return a != "" && a == b
}

var javDBLabeledIDPattern = regexp.MustCompile(`(?i)\bID\s*[:：]\s*([A-Z]{2,}[-_ .]*[0-9]{2,}(?:[-_ .]*U)?)`)

func extractJavDBReleaseID(text string) string {
	text = strings.TrimSpace(text)
	if match := javDBLabeledIDPattern.FindStringSubmatch(text); len(match) > 1 {
		return strings.TrimSpace(match[1])
	}
	return text
}

func releaseIDMatchesText(text, releaseID string) bool {
	parts := regexp.MustCompile(`[A-Z]+|[0-9]+`).FindAllString(strings.ToUpper(normalizeReleaseID(releaseID)), -1)
	if len(parts) == 0 {
		return false
	}
	pattern := `(?i)(?:^|[^A-Z0-9])` + strings.Join(parts, `[-_ .]*`) + `(?:[-_ .]*U)?(?:[^A-Z0-9]|$)`
	return regexp.MustCompile(pattern).MatchString(text)
}

type javDBSearchHit struct {
	href string
	id   string
	date string
}

type javDBDownloadDiscovery struct {
	rows                 []domain.SearchResult
	downloadSectionFound bool
	shareLinkCount       int
	pikPakLinkCount      int
	actionURLs           []string
}

func (p *javDBProvider) Search(ctx context.Context, release domain.Release) ([]domain.SearchResult, error) {
	base := strings.TrimRight(strings.TrimSpace(p.baseURL), "/")
	if base == "" {
		base = defaultJavDBURL
	}
	searchURL := base + "/search?q=" + url.QueryEscape(release.VideoID) + "&f=all"
	doc, searchStatus, err := p.getHTML(ctx, searchURL)
	if err != nil {
		return nil, fmt.Errorf("JavDB search request failed (video_id=%s search_url=%s status=%d): %w", release.VideoID, searchURL, searchStatus, err)
	}
	hits := parseJavDBSearchHits(doc, base)
	if len(hits) == 0 {
		return nil, fmt.Errorf("JavDB search page returned no parsable release results (video_id=%s normalized_video_id=%s search_url=%s status=%d)", release.VideoID, normalizeReleaseID(release.VideoID), searchURL, searchStatus)
	}
	exact := make([]javDBSearchHit, 0, len(hits))
	for _, hit := range hits {
		if releaseIDsEqual(hit.id, release.VideoID) {
			exact = append(exact, hit)
			if p.log != nil && hit.id != release.VideoID {
				p.log.Info("JavDB HTTP search matched canonical release ID", "requested_id", release.VideoID, "normalized_requested_id", normalizeReleaseID(release.VideoID), "javdb_id", hit.id, "normalized_javdb_id", normalizeReleaseID(hit.id))
			}
		}
	}
	if len(exact) == 0 {
		return nil, fmt.Errorf("JavDB returned search results but no exact release ID match (requested_id=%s normalized_requested_id=%s search_results=%d)", release.VideoID, normalizeReleaseID(release.VideoID), len(hits))
	}
	knownDate, _ := time.Parse("2006-01-02", release.ReleaseDate)
	compatible := make([]javDBSearchHit, 0, len(exact))
	var mismatchReason string
	for _, hit := range exact {
		pageDate := parseJavDBDate(hit.date)
		deltaDays := calendarDeltaDays(knownDate, pageDate)
		if !knownDate.IsZero() && !pageDate.IsZero() && (deltaDays < -60 || deltaDays > 60) {
			mismatchReason = fmt.Sprintf("JavDB exact release ID matched but release date is incompatible (requested_id=%s matched_id=%s stored_release_date=%s javdb_release_date=%s date_delta_days=%d allowed_delta_days=60)", release.VideoID, hit.id, release.ReleaseDate, pageDate.Format("2006-01-02"), deltaDays)
			continue
		}
		compatible = append(compatible, hit)
	}
	if len(compatible) == 0 {
		return nil, errors.New(mismatchReason)
	}
	var rows []domain.SearchResult
	var stageErrors []string
	for _, h := range compatible {
		pageDate := parseJavDBDate(h.date)
		page, detailStatus, getErr := p.getHTML(ctx, h.href)
		if getErr != nil {
			stageErrors = append(stageErrors, fmt.Sprintf("JavDB exact release found but detail page fetch failed (requested_id=%s matched_id=%s detail_url=%s status=%d): %v", release.VideoID, h.id, h.href, detailStatus, getErr))
			continue
		}
		detailID := parseJavDBDetailID(page)
		if detailID != "" && !releaseIDsEqual(detailID, release.VideoID) {
			stageErrors = append(stageErrors, fmt.Sprintf("JavDB detail page release ID did not match (requested_id=%s normalized_requested_id=%s detail_page_id=%s normalized_detail_page_id=%s detail_url=%s)", release.VideoID, normalizeReleaseID(release.VideoID), detailID, normalizeReleaseID(detailID), h.href))
			continue
		}
		discovery := discoverJavDBDownloads(page, h.href, release.VideoID)
		for _, actionURL := range discovery.actionURLs {
			actionPage, actionStatus, actionErr := p.getHTML(ctx, actionURL)
			if actionErr != nil {
				stageErrors = append(stageErrors, fmt.Sprintf("JavDB exact release found but download action fetch failed (requested_id=%s matched_id=%s download_url=%s status=%d): %v", release.VideoID, h.id, actionURL, actionStatus, actionErr))
				continue
			}
			mergeJavDBDiscovery(&discovery, discoverJavDBDownloads(actionPage, actionURL, release.VideoID))
		}
		if len(discovery.rows) == 0 {
			if discovery.downloadSectionFound {
				stageErrors = append(stageErrors, fmt.Sprintf("JavDB exact release found but no downloadable HTTP links were found (requested_id=%s matched_id=%s detail_url=%s detail_status=%d)", release.VideoID, h.id, h.href, detailStatus))
			} else {
				stageErrors = append(stageErrors, fmt.Sprintf("JavDB exact release found but download section could not be parsed (requested_id=%s matched_id=%s detail_url=%s detail_status=%d)", release.VideoID, h.id, h.href, detailStatus))
			}
			continue
		}
		rows = appendUniqueJavDBRows(rows, discovery.rows...)
		if p.log != nil {
			p.log.Info("JavDB HTTP search matched release", "requested_id", release.VideoID, "normalized_id", normalizeReleaseID(release.VideoID), "search_url", searchURL, "search_status", searchStatus, "search_results", len(hits), "exact_matches", len(exact), "matched_id", h.id, "stored_date", release.ReleaseDate, "javdb_date", formatOptionalDate(pageDate), "date_delta_days", calendarDeltaDays(knownDate, pageDate), "date_compatible", true, "detail_url", h.href, "detail_status", detailStatus, "detail_page_id", detailID, "download_section_found", discovery.downloadSectionFound, "keepshare_links", discovery.shareLinkCount, "pikpak_links", discovery.pikPakLinkCount, "candidates", len(discovery.rows))
		}
	}
	if len(rows) == 0 {
		return nil, errors.New(strings.Join(stageErrors, "; "))
	}
	// JavDB's visible row title is not always the actual video filename in
	// the Keepshare/PikPak share. Inspect every distinct candidate now so
	// preferred filename patterns rank the real downloadable file. A blocked
	// or expired share retains its JavDB metadata and naturally falls through
	// to the established non-U/filesize ordering instead of hiding the result.
	// Inspect shares serially. Each inspection creates an anonymous PikPak
	// session/CAPTCHA token; firing every share at once can make otherwise valid
	// public shares fail transiently and leaves the search card with only its
	// JavDB row title. A later download resolution would then appear to
	// "discover" the preferred filename after the search had already missed it.
	// Keeping this phase ordered also makes the final HTTP ranking deterministic.
	inspectionFailures := 0
	for i := range rows {
		inspect := p.inspectSearchCandidate
		if p.inspectCandidate != nil {
			inspect = p.inspectCandidate
		}
		selected, files, inspectErr := inspect(ctx, rows[i].Link, release.VideoID)
		if inspectErr != nil {
			inspectionFailures++
			rows[i].Reason = "release ID matched; Keepshare filename inspection failed: " + inspectErr.Error()
			continue
		}
		rows[i].Title = selected.Name
		rows[i].MatchedFile = selected.Name
		rows[i].PreferredFilenameMatch, _ = matchesAcceptedHTTPPattern(selected.Name, p.acceptedPatterns)
		rows[i].Files, rows[i].FileDetails = pikPakSearchFiles(files, selected)
		if size, parseErr := strconv.ParseInt(selected.Size, 10, 64); parseErr == nil && size > 0 {
			rows[i].SizeBytes = size
		}
	}
	sortJavDBDownloadCandidates(rows, release.VideoID, p.acceptedPatterns)
	if p.log != nil {
		p.log.Info("JavDB HTTP candidate inspection completed", "requested_id", release.VideoID, "normalized_id", normalizeReleaseID(release.VideoID), "candidate_inspection_count", len(rows), "candidate_inspection_failures", inspectionFailures, "final_candidate_count", len(rows))
	}
	return rows, nil
}

func (p *javDBProvider) inspectSearchCandidate(ctx context.Context, link, releaseID string) (pikPakFile, []pikPakFile, error) {
	_, _, selected, files, err := inspectPikPakShare(ctx, p.client, link, releaseID, p.acceptedPatterns)
	if err == nil || ctx.Err() != nil {
		return selected, files, err
	}
	// Anonymous share endpoints occasionally reject a freshly-created token.
	// One short, context-aware retry is enough to recover without making a
	// genuinely blocked or expired share stall the whole search.
	timer := time.NewTimer(250 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return pikPakFile{}, nil, ctx.Err()
	case <-timer.C:
	}
	_, _, selected, files, err = inspectPikPakShare(ctx, p.client, link, releaseID, p.acceptedPatterns)
	return selected, files, err
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
			rows[i].PreferredFilenameMatch = true
			rows[i].Reason = "preferred HTTP filename matched pattern " + pattern
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

func (p *javDBProvider) getHTML(ctx context.Context, raw string) (*html.Node, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("User-Agent", publicShareUserAgent)
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, resp.StatusCode, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	if looksLikeAccessChallenge(body) {
		return nil, resp.StatusCode, errors.New("JavDB request was blocked or returned a challenge page")
	}
	doc, err := html.Parse(strings.NewReader(string(body)))
	return doc, resp.StatusCode, err
}

func parseJavDBDownloadCandidates(doc *html.Node, sourceURL, releaseID string) []domain.SearchResult {
	return discoverJavDBDownloads(doc, sourceURL, releaseID).rows
}

func looksLikeAccessChallenge(body []byte) bool {
	text := strings.ToLower(string(body))
	normalPage := strings.Contains(text, `class="movie-list`) || strings.Contains(text, `class="video-detail`) || strings.Contains(text, `id="video-search"`)
	if normalPage {
		return false
	}
	for _, signal := range []string{"cf-chl-", "cloudflare ray id", "checking your browser", "just a moment...", "attention required!", "captcha", "challenge-platform"} {
		if strings.Contains(text, signal) {
			return true
		}
	}
	return false
}

func parseJavDBSearchHits(doc *html.Node, base string) []javDBSearchHit {
	seen := map[string]bool{}
	var hits []javDBSearchHit
	for _, anchor := range descendants(doc, "a") {
		href := resolveURL(base, html.UnescapeString(strings.TrimSpace(attrValue(anchor, "href"))))
		u, err := url.Parse(href)
		if err != nil || !strings.Contains(u.Path, "/v/") || seen[href] {
			continue
		}
		container := javDBResultContainer(anchor)
		idNode := firstDescendant(anchor, "strong", "")
		if idNode == nil {
			idNode = firstDescendant(container, "strong", "")
		}
		id := extractJavDBReleaseID(nodeText(idNode))
		if normalizeReleaseID(id) == "" {
			continue
		}
		meta := firstDescendant(container, "div", "meta")
		if meta == nil {
			meta = firstDescendant(container, "span", "meta")
		}
		seen[href] = true
		hits = append(hits, javDBSearchHit{href: href, id: id, date: strings.TrimSpace(nodeText(meta))})
	}
	return hits
}

func javDBResultContainer(node *html.Node) *html.Node {
	var fallback *html.Node
	for current, depth := node, 0; current != nil && depth < 8; current, depth = current.Parent, depth+1 {
		if hasClass(current, "item") || hasClass(current, "movie-list") {
			return current
		}
		if fallback == nil && hasClass(current, "box") {
			fallback = current
		}
	}
	if fallback != nil {
		return fallback
	}
	return node.Parent
}

func parseJavDBDetailID(doc *html.Node) string {
	if match := javDBLabeledIDPattern.FindStringSubmatch(nodeText(doc)); len(match) > 1 {
		return strings.TrimSpace(match[1])
	}
	for _, node := range descendants(doc, "a") {
		if value := strings.TrimSpace(attrValue(node, "data-clipboard-text")); releaseLikeTextPattern.MatchString(value) {
			return value
		}
	}
	for _, heading := range descendants(doc, "h2") {
		if value := strings.TrimSpace(nodeText(firstDescendant(heading, "strong", ""))); releaseLikeTextPattern.MatchString(value) {
			return value
		}
	}
	return ""
}

func discoverJavDBDownloads(doc *html.Node, sourceURL, releaseID string) javDBDownloadDiscovery {
	discovery := javDBDownloadDiscovery{}
	seenShares, seenActions := map[string]bool{}, map[string]bool{}
	for _, anchor := range descendants(doc, "a") {
		raw := html.UnescapeString(strings.TrimSpace(attrValue(anchor, "href")))
		if raw == "" {
			continue
		}
		href := resolveURL(sourceURL, raw)
		u, err := url.Parse(href)
		if err != nil {
			continue
		}
		if isJavDBShareURL(u) {
			discovery.downloadSectionFound = true
			if seenShares[href] {
				continue
			}
			seenShares[href] = true
			discovery.shareLinkCount++
			if strings.EqualFold(u.Hostname(), "mypikpak.com") || strings.HasSuffix(strings.ToLower(u.Hostname()), ".mypikpak.com") {
				discovery.pikPakLinkCount++
			}
			container := javDBDownloadContainer(anchor)
			name, matchesRelease := javDBCandidateName(container, anchor, releaseID)
			if !matchesRelease {
				continue
			}
			size := parseHumanBytes(nodeText(container))
			if rawSize := attrValue(container, "data-size"); size == 0 && rawSize != "" {
				mb, _ := strconv.ParseInt(rawSize, 10, 64)
				size = mb << 20
			}
			published := strings.TrimSpace(nodeText(firstDescendant(container, "span", "time")))
			discovery.rows = append(discovery.rows, domain.SearchResult{Provider: "JavDB / Keepshare", Title: name, Link: href, SourceURL: sourceURL, Transport: "http", SizeBytes: size, PublishedAt: published, Accepted: true, Reason: "exact release ID match available as HTTP download"})
			continue
		}
		if javDBDownloadAction(anchor, sourceURL, href) {
			discovery.downloadSectionFound = true
			if !seenActions[href] {
				seenActions[href] = true
				discovery.actionURLs = append(discovery.actionURLs, href)
			}
		}
	}
	if !discovery.downloadSectionFound {
		for _, tag := range []string{"button", "section", "div"} {
			for _, node := range descendants(doc, tag) {
				marker := strings.ToLower(strings.Join([]string{attrValue(node, "id"), attrValue(node, "class"), attrValue(node, "title"), nodeText(node)}, " "))
				if strings.Contains(marker, "download") || strings.Contains(marker, "magnet") {
					discovery.downloadSectionFound = true
					break
				}
			}
			if discovery.downloadSectionFound {
				break
			}
		}
	}
	return discovery
}

func isJavDBShareHost(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	return host == "keepshare.org" || strings.HasSuffix(host, ".keepshare.org") || host == "keepshare.cc" || strings.HasSuffix(host, ".keepshare.cc") || host == "mypikpak.com" || strings.HasSuffix(host, ".mypikpak.com")
}

func isJavDBShareURL(value *url.URL) bool {
	if value == nil || !isJavDBShareHost(value.Hostname()) {
		return false
	}
	host := strings.ToLower(value.Hostname())
	path := strings.Trim(value.EscapedPath(), "/")
	if host == "mypikpak.com" || strings.HasSuffix(host, ".mypikpak.com") {
		return strings.HasPrefix(path, "s/") && len(strings.Split(path, "/")) >= 2
	}
	return path != ""
}

func javDBDownloadAction(anchor *html.Node, sourceURL, href string) bool {
	source, sourceErr := url.Parse(sourceURL)
	target, targetErr := url.Parse(href)
	if sourceErr != nil || targetErr != nil || !strings.EqualFold(source.Hostname(), target.Hostname()) || source.String() == target.String() {
		return false
	}
	marker := strings.ToLower(strings.Join([]string{nodeText(anchor), attrValue(anchor, "class"), attrValue(anchor, "id"), attrValue(anchor, "title"), target.Path}, " "))
	return strings.Contains(marker, "download")
}

func javDBDownloadContainer(anchor *html.Node) *html.Node {
	for current, depth := anchor, 0; current != nil && depth < 6; current, depth = current.Parent, depth+1 {
		if hasClass(current, "item") || hasClass(current, "download") || current.Data == "li" || current.Data == "tr" {
			return current
		}
	}
	return anchor.Parent
}

var releaseLikeTextPattern = regexp.MustCompile(`(?i)(?:^|[^A-Z0-9])[A-Z]{2,}[-_ .]*[0-9]{2,}(?:[-_ .]*U)?(?:[^A-Z0-9]|$)`)

func javDBCandidateName(container, anchor *html.Node, releaseID string) (string, bool) {
	for _, candidate := range []string{nodeText(firstDescendant(container, "span", "name")), attrValue(anchor, "download"), attrValue(anchor, "title"), nodeText(anchor), nodeText(container)} {
		candidate = strings.TrimSpace(candidate)
		if candidate != "" && releaseIDMatchesText(candidate, releaseID) {
			return candidate, true
		}
		if candidate != "" && releaseLikeTextPattern.MatchString(candidate) {
			return candidate, false
		}
	}
	return releaseID, true
}

func mergeJavDBDiscovery(dst *javDBDownloadDiscovery, src javDBDownloadDiscovery) {
	dst.downloadSectionFound = dst.downloadSectionFound || src.downloadSectionFound
	dst.shareLinkCount += src.shareLinkCount
	dst.pikPakLinkCount += src.pikPakLinkCount
	dst.rows = appendUniqueJavDBRows(dst.rows, src.rows...)
}

func appendUniqueJavDBRows(rows []domain.SearchResult, candidates ...domain.SearchResult) []domain.SearchResult {
	seen := make(map[string]bool, len(rows)+len(candidates))
	for _, row := range rows {
		seen[row.Link] = true
	}
	for _, candidate := range candidates {
		if candidate.Link != "" && !seen[candidate.Link] {
			seen[candidate.Link] = true
			rows = append(rows, candidate)
		}
	}
	return rows
}

var humanSizePattern = regexp.MustCompile(`(?i)([0-9]+(?:\.[0-9]+)?)\s*(TIB|TB|GIB|GB|MIB|MB|KIB|KB)`)

func parseHumanBytes(s string) int64 {
	m := humanSizePattern.FindStringSubmatch(s)
	if m == nil {
		return 0
	}
	v, _ := strconv.ParseFloat(m[1], 64)
	unit := strings.ReplaceAll(strings.ToUpper(m[2]), "I", "")
	power := map[string]int{"KB": 10, "MB": 20, "GB": 30, "TB": 40}[unit]
	return int64(v * float64(int64(1)<<power))
}

func parseJavDBDate(s string) time.Time {
	match := regexp.MustCompile(`\b(?:\d{4}[-/]\d{1,2}[-/]\d{1,2}|\d{1,2}/\d{1,2}/\d{4})\b`).FindString(strings.TrimSpace(s))
	if match != "" {
		s = match
	}
	for _, layout := range []string{"01/02/2006", "2006-01-02", "2006/01/02"} {
		if t, err := time.Parse(layout, strings.TrimSpace(s)); err == nil {
			return t
		}
	}
	return time.Time{}
}

func calendarDeltaDays(from, to time.Time) int {
	if from.IsZero() || to.IsZero() {
		return 0
	}
	return int(to.Sub(from).Hours() / 24)
}

func formatOptionalDate(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.Format("2006-01-02")
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

func inspectPikPakShare(ctx context.Context, client *http.Client, keepshareURL, releaseID string, preferredPatterns []string) (*pikPakClient, string, pikPakFile, []pikPakFile, error) {
	shareID, err := discoverPikPakShareID(ctx, client, keepshareURL)
	if err != nil {
		return nil, "", pikPakFile{}, nil, err
	}
	pp := newPikPakClient(client)
	all, err := pp.listShareFiles(ctx, shareID, "")
	if err != nil {
		return nil, "", pikPakFile{}, nil, err
	}
	selected, found := selectPikPakFile(all, releaseID, preferredPatterns)
	if !found {
		return nil, "", pikPakFile{}, all, fmt.Errorf("PikPak share contained no file matching %s", releaseID)
	}
	return pp, shareID, selected, all, nil
}

// discoverPikPakShareID follows Keepshare's own intermediate redirects but
// deliberately stops before requesting the public PikPak player. Keepshare
// currently redirects keepshare.org -> keepshare.cc -> mypikpak.com; stopping
// at the first hop loses the share ID, while loading the final ?act=play page
// can hang until Client.Timeout even though the share API remains healthy.
// Direct PikPak URLs and HTML/JS-based responses remain supported fallbacks.
func discoverPikPakShareID(ctx context.Context, client *http.Client, sourceURL string) (string, error) {
	if match := publicShareURLPattern.FindStringSubmatch(sourceURL); match != nil {
		return match[1], nil
	}
	noPlayer := *client
	noPlayer.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if publicShareURLPattern.MatchString(req.URL.String()) {
			return http.ErrUseLastResponse
		}
		if len(via) >= 10 {
			return errors.New("too many Keepshare redirects")
		}
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, sourceURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", publicShareUserAgent)
	resp, err := noPlayer.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if location := strings.TrimSpace(resp.Header.Get("Location")); location != "" {
		location = resolveURL(resp.Request.URL.String(), location)
		if match := publicShareURLPattern.FindStringSubmatch(location); match != nil {
			return match[1], nil
		}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return "", err
	}
	if match := publicShareURLPattern.FindStringSubmatch(string(body)); match != nil {
		return match[1], nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 400 {
		return "", fmt.Errorf("Keepshare returned HTTP %d without a PikPak share redirect", resp.StatusCode)
	}
	return "", errors.New("Keepshare did not resolve to a PikPak public share")
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

func pikPakSearchFiles(files []pikPakFile, selected pikPakFile) ([]string, []domain.SearchFile) {
	names := make([]string, 0, len(files))
	details := make([]domain.SearchFile, 0, len(files))
	for _, file := range files {
		size, _ := strconv.ParseInt(file.Size, 10, 64)
		names = append(names, file.Name)
		details = append(details, domain.SearchFile{Name: file.Name, SizeBytes: size, Matched: file.ID == selected.ID})
	}
	return names, details
}

func resolvePikPakShare(ctx context.Context, client *http.Client, keepshareURL, releaseID string, preferredPatterns []string) (string, string, int64, error) {
	pp, shareID, selected, _, err := inspectPikPakShare(ctx, client, keepshareURL, releaseID, preferredPatterns)
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

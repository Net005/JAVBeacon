package download

import (
	"context"
	"encoding/xml"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/Net005/JAVBeacon/internal/domain"
)

type SearchProvider interface {
	Name() string
	Search(context.Context, string) ([]domain.SearchResult, error)
}

type Nyaa struct {
	Client              *http.Client
	URLTemplate         string
	AcceptedPatterns    []string
	PreferredPatterns   []PreferredFilenamePattern
	BlacklistedPatterns []string
}

var (
	nyaaPanelTitle  = regexp.MustCompile(`(?is)<h3[^>]*class=["'][^"']*panel-title[^"']*["'][^>]*>(.*?)</h3>`)
	nyaaPageTitle   = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
	nyaaMagnetLink  = regexp.MustCompile(`(?is)href=["'](magnet:\?[^"']+)["']`)
	nyaaTorrentLink = regexp.MustCompile(`(?is)href=["']([^"']+/download/[^"']+\.torrent[^"']*)["']`)
	nyaaFileEntry   = regexp.MustCompile(`(?is)<li[^>]*>\s*<i[^>]*fa-file[^>]*></i>\s*(.*?)\s*<span[^>]*class=["'][^"']*file-size[^"']*["'][^>]*>\s*\(?\s*([^<)]+?)\s*\)?\s*</span>`)
	htmlElement     = regexp.MustCompile(`(?is)<[^>]+>`)
)

func (n *Nyaa) Name() string { return "Sukebei/Nyaa" }
func (n *Nyaa) Search(ctx context.Context, releaseID string) ([]domain.SearchResult, error) {
	template := n.URLTemplate
	if template == "" {
		template = "https://sukebei.nyaa.si/?page=rss&f=0&c=2_0&q=<release_id>"
	}
	raw := strings.ReplaceAll(strings.ReplaceAll(template, "<release_id>", url.QueryEscape(releaseID)), "{release_id}", url.QueryEscape(releaseID))
	req, e := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if e != nil {
		return nil, e
	}
	req.Header.Set("User-Agent", "JAVBeacon/1.0")
	resp, e := n.Client.Do(req)
	if e != nil {
		return nil, e
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("Nyaa returned %s", resp.Status)
	}
	body, e := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if e != nil {
		return nil, e
	}
	type item struct {
		Title     string `xml:"title"`
		Link      string `xml:"link"`
		GUID      string `xml:"guid"`
		Enclosure struct {
			URL string `xml:"url,attr"`
		} `xml:"enclosure"`
		// Seeders/Leechers match Nyaa's namespaced <nyaa:seeders>/
		// <nyaa:leechers> elements - encoding/xml matches by local name
		// when the struct tag carries no namespace, so the "nyaa:" prefix
		// is transparent here.
		Seeders  int    `xml:"seeders"`
		Leechers int    `xml:"leechers"`
		Size     string `xml:"size"`
	}
	var feed struct {
		Items []item `xml:"channel>item"`
	}
	_ = xml.Unmarshal(body, &feed)
	results := []domain.SearchResult{}
	for _, x := range feed.Items {
		detailURL, directURL := x.GUID, x.Enclosure.URL
		if directURL == "" {
			directURL = x.Link
		}
		result := n.resolveResult(ctx, x.Title, detailURL, directURL)
		result.Seeds = x.Seeders
		result.Peers = x.Leechers
		result.Size = strings.TrimSpace(x.Size)
		if !strings.Contains(canonical(result.Title), canonical(releaseID)) {
			continue
		}
		results = append(results, result)
	}
	if len(results) == 0 {
		re := regexp.MustCompile(`(?is)<a[^>]+href=["']([^"']+(?:\.torrent|magnet:\?)[^"']*)["'][^>]*>(.*?)</a>`)
		for _, m := range re.FindAllSubmatch(body, -1) {
			title := strings.TrimSpace(regexp.MustCompile(`<[^>]+>`).ReplaceAllString(html.UnescapeString(string(m[2])), ""))
			if !strings.Contains(canonical(title), canonical(releaseID)) {
				continue
			}
			preferredMatch, preferredReason, priority, matchedFile := n.matchFiles(title, nil)
			blacklisted, _, _ := n.blacklistMatch(title, nil)
			// Accepted no longer requires a preferred-filename match (see
			// matchFiles's doc comment) - only a blacklist match rejects a
			// torrent result outright. The release-ID match that gated this
			// result into `results` in the first place already stands in for
			// the old ID-containment check.
			accepted, reason := !blacklisted, preferredReason
			if blacklisted {
				reason = "torrent filename matched a blacklisted pattern"
			}
			results = append(results, domain.SearchResult{Provider: n.Name(), Title: title, MatchedFile: matchedFile, PreferredFilenameMatch: preferredMatch, PreferredFilenamePriority: priority, BlacklistedFilenameMatch: blacklisted, Link: html.UnescapeString(string(m[1])), Accepted: accepted, Reason: reason})
		}
	}
	return results, nil
}

func (n *Nyaa) resolveResult(ctx context.Context, title, detailURL, directURL string) domain.SearchResult {
	title = cleanHTMLText(title)
	link := strings.TrimSpace(directURL)
	files := []string{}
	fileDetails := []domain.SearchFile{}
	isDetail := detailURL != "" && strings.Contains(detailURL, "/view/")
	if isDetail {
		if resolvedTitle, resolvedLink, resolvedFiles, resolvedDetails, err := n.resolveDetail(ctx, detailURL); err == nil {
			if resolvedTitle != "" {
				title = resolvedTitle
			}
			if resolvedLink != "" {
				link = resolvedLink
			}
			files = resolvedFiles
			fileDetails = resolvedDetails
		}
	}
	if link == "" && !isDetail {
		link = detailURL
	}
	if len(files) == 0 {
		if magnetName := magnetDisplayName(link); magnetName != "" {
			files = []string{magnetName}
			fileDetails = []domain.SearchFile{{Name: magnetName}}
		}
	}
	preferredMatch, preferredReason, priority, matchedFile := n.matchFiles(title, files)
	blacklisted, _, _ := n.blacklistMatch(title, files)
	// Accepted no longer requires a preferred-filename match (see
	// matchFiles's doc comment) - only a blacklist match or a missing
	// download link rejects a torrent result outright.
	accepted, reason := !blacklisted, preferredReason
	if blacklisted {
		reason = "torrent filename matched a blacklisted pattern"
	}
	if link == "" {
		accepted = false
		reason = "torrent detail did not expose a magnet or .torrent link"
	}
	sourceURL := ""
	if isDetail {
		sourceURL = detailURL
	}
	for i := range fileDetails {
		fileDetails[i].Matched = fileDetails[i].Name == matchedFile
	}
	return domain.SearchResult{Provider: n.Name(), Title: title, Files: files, FileDetails: fileDetails, MatchedFile: matchedFile, PreferredFilenameMatch: preferredMatch, PreferredFilenamePriority: priority, BlacklistedFilenameMatch: blacklisted, Link: link, SourceURL: sourceURL, Accepted: accepted, Reason: reason}
}

func (n *Nyaa) resolveDetail(ctx context.Context, rawURL string) (string, string, []string, []domain.SearchFile, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", "", nil, nil, err
	}
	req.Header.Set("User-Agent", "JAVBeacon/1.0")
	resp, err := n.Client.Do(req)
	if err != nil {
		return "", "", nil, nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return "", "", nil, nil, fmt.Errorf("Nyaa detail returned %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return "", "", nil, nil, err
	}
	text := string(body)
	title := ""
	if match := nyaaPanelTitle.FindStringSubmatch(text); len(match) > 1 {
		title = cleanHTMLText(match[1])
	} else if match := nyaaPageTitle.FindStringSubmatch(text); len(match) > 1 {
		title = strings.TrimSpace(strings.TrimSuffix(cleanHTMLText(match[1]), " - Sukebei"))
	}
	link := ""
	if match := nyaaMagnetLink.FindStringSubmatch(text); len(match) > 1 {
		link = html.UnescapeString(match[1])
	} else if match := nyaaTorrentLink.FindStringSubmatch(text); len(match) > 1 {
		link = absoluteURL(rawURL, html.UnescapeString(match[1]))
	}
	files := []string{}
	details := []domain.SearchFile{}
	for _, match := range nyaaFileEntry.FindAllStringSubmatch(text, -1) {
		if name := cleanHTMLText(match[1]); name != "" {
			files = append(files, name)
			details = append(details, domain.SearchFile{Name: name, SizeBytes: parseHumanBytes(match[2])})
		}
	}
	return title, link, files, details, nil
}

func cleanHTMLText(value string) string {
	return strings.Join(strings.Fields(html.UnescapeString(htmlElement.ReplaceAllString(value, " "))), " ")
}

func absoluteURL(base, ref string) string {
	b, bErr := url.Parse(base)
	r, rErr := url.Parse(ref)
	if bErr != nil || rErr != nil {
		return ref
	}
	return b.ResolveReference(r).String()
}

func magnetDisplayName(link string) string {
	parsed, err := url.Parse(html.UnescapeString(link))
	if err != nil || !strings.EqualFold(parsed.Scheme, "magnet") {
		return ""
	}
	return strings.TrimSpace(parsed.Query().Get("dn"))
}

// matchFiles scores title/files against the configured (or legacy/default)
// preferred filename patterns, in priority order, and reports the highest-
// priority match. It is a PURE preference signal, not an accept/reject
// gate: callers no longer treat "no pattern matched" as a reason to reject
// a result - a preferred match is simply tried/ranked first, and download
// falls back to the best remaining candidate otherwise (see
// fallbackSearchCandidate in service.go). A blacklist match is a separate,
// still-hard exclusion handled by blacklistMatch, not by this function.
func (n *Nyaa) matchFiles(title string, files []string) (bool, string, int, string) {
	patterns := normalizePreferredFilenamePatterns(n.PreferredPatterns)
	if len(patterns) == 0 {
		patterns = legacyPreferredFilenamePatterns(n.AcceptedPatterns)
	}
	if len(patterns) == 0 {
		patterns = defaultPreferredFilenamePatternRows()
	}
	candidates := files
	if len(candidates) == 0 {
		candidates = []string{title}
	}
	for _, item := range patterns {
		p := strings.TrimSpace(item.Pattern)
		for _, candidate := range candidates {
			if p != "" && strings.Contains(strings.ToLower(candidate), strings.ToLower(p)) {
				return true, fmt.Sprintf("torrent file matched priority %d pattern %s: %s", item.Priority, p, candidate), item.Priority, candidate
			}
		}
	}
	return false, "no preferred filename pattern matched", 0, ""
}

func (n *Nyaa) blacklistMatch(title string, files []string) (bool, string, string) {
	candidates := files
	if len(candidates) == 0 {
		candidates = []string{title}
	}
	for _, candidate := range candidates {
		if matched, pattern := matchesBlacklistedFilename(candidate, n.BlacklistedPatterns); matched {
			return true, pattern, candidate
		}
	}
	return false, "", ""
}

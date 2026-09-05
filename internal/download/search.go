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
	Client           *http.Client
	URLTemplate      string
	AcceptedPatterns []string
}

var (
	nyaaPanelTitle  = regexp.MustCompile(`(?is)<h3[^>]*class=["'][^"']*panel-title[^"']*["'][^>]*>(.*?)</h3>`)
	nyaaPageTitle   = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
	nyaaMagnetLink  = regexp.MustCompile(`(?is)href=["'](magnet:\?[^"']+)["']`)
	nyaaTorrentLink = regexp.MustCompile(`(?is)href=["']([^"']+/download/[^"']+\.torrent[^"']*)["']`)
	nyaaFileName    = regexp.MustCompile(`(?is)<li[^>]*>\s*<i[^>]*fa-file[^>]*></i>\s*(.*?)\s*<span[^>]*class=["'][^"']*file-size`)
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
			accepted, reason := n.accept(title)
			results = append(results, domain.SearchResult{Provider: n.Name(), Title: title, Link: html.UnescapeString(string(m[1])), Accepted: accepted, Reason: reason})
		}
	}
	return results, nil
}

func (n *Nyaa) resolveResult(ctx context.Context, title, detailURL, directURL string) domain.SearchResult {
	title = cleanHTMLText(title)
	link := strings.TrimSpace(directURL)
	files := []string{}
	isDetail := detailURL != "" && strings.Contains(detailURL, "/view/")
	if isDetail {
		if resolvedTitle, resolvedLink, resolvedFiles, err := n.resolveDetail(ctx, detailURL); err == nil {
			if resolvedTitle != "" {
				title = resolvedTitle
			}
			if resolvedLink != "" {
				link = resolvedLink
			}
			files = resolvedFiles
		}
	}
	if link == "" && !isDetail {
		link = detailURL
	}
	if len(files) == 0 {
		if magnetName := magnetDisplayName(link); magnetName != "" {
			files = []string{magnetName}
		}
	}
	accepted, reason := n.acceptFiles(title, files)
	if link == "" {
		accepted = false
		reason = "torrent detail did not expose a magnet or .torrent link"
	}
	sourceURL := ""
	if isDetail {
		sourceURL = detailURL
	}
	return domain.SearchResult{Provider: n.Name(), Title: title, Files: files, Link: link, SourceURL: sourceURL, Accepted: accepted, Reason: reason}
}

func (n *Nyaa) resolveDetail(ctx context.Context, rawURL string) (string, string, []string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", "", nil, err
	}
	req.Header.Set("User-Agent", "JAVBeacon/1.0")
	resp, err := n.Client.Do(req)
	if err != nil {
		return "", "", nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return "", "", nil, fmt.Errorf("Nyaa detail returned %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return "", "", nil, err
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
	for _, match := range nyaaFileName.FindAllStringSubmatch(text, -1) {
		if name := cleanHTMLText(match[1]); name != "" {
			files = append(files, name)
		}
	}
	return title, link, files, nil
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
func (n *Nyaa) accept(title string) (bool, string) {
	return n.acceptFiles(title, nil)
}

func (n *Nyaa) acceptFiles(title string, files []string) (bool, string) {
	patterns := n.AcceptedPatterns
	if len(patterns) == 0 {
		patterns = []string{"4k688.com@", "hhd800.com@"}
	}
	candidates := files
	if len(candidates) == 0 {
		candidates = []string{title}
	}
	for _, p := range patterns {
		p = strings.TrimSpace(p)
		for _, candidate := range candidates {
			if p != "" && strings.Contains(strings.ToLower(candidate), strings.ToLower(p)) {
				return true, "torrent file matched " + p + ": " + candidate
			}
		}
	}
	return false, "filename did not match preferred patterns"
}

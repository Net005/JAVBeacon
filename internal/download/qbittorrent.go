package download

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

type Torrent struct {
	Hash         string  `json:"hash"`
	Name         string  `json:"name"`
	State        string  `json:"state"`
	ContentPath  string  `json:"content_path"`
	Ratio        float64 `json:"ratio"`
	Progress     float64 `json:"progress"`
	Seeds        int     `json:"num_seeds"`
	Peers        int     `json:"num_leechs"`
	ETA          int64   `json:"eta"`
	SeenComplete int64   `json:"seen_complete"`
}
type TorrentFile struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
}
type QBittorrent interface {
	Torrents(context.Context) ([]Torrent, error)
	Add(context.Context, string, string) (string, error)
	Remove(context.Context, string) error
}
type QBClient struct {
	BaseURL, Username, Password string
	Client                      *http.Client
}

func NewQB(base, user, password string) *QBClient {
	jar, _ := cookiejar.New(nil)
	return &QBClient{BaseURL: strings.TrimRight(base, "/"), Username: user, Password: password, Client: &http.Client{Jar: jar}}
}
func (q *QBClient) request(ctx context.Context, method, path string, form url.Values) (*http.Response, error) {
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, e := http.NewRequestWithContext(ctx, method, q.BaseURL+path, body)
	if e != nil {
		return nil, e
	}
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	return q.Client.Do(req)
}
func (q *QBClient) login(ctx context.Context) error {
	if q.BaseURL == "" {
		return errors.New("qBittorrent URL is not configured")
	}
	r, e := q.request(ctx, http.MethodPost, "/api/v2/auth/login", url.Values{"username": {q.Username}, "password": {q.Password}})
	if e != nil {
		return e
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(r.Body)
	// Standard qBittorrent replies with 200 and "Ok.", while some
	// authentication-bypass/reverse-proxy setups reply with an empty 204.
	// Treat every 2xx response as a successful login; the authenticated API
	// request that follows still verifies that the session can access qBittorrent.
	if r.StatusCode/100 != 2 {
		return fmt.Errorf("qBittorrent login failed: HTTP %d %s", r.StatusCode, strings.TrimSpace(string(b)))
	}
	return nil
}
func (q *QBClient) Torrents(ctx context.Context) ([]Torrent, error) {
	if e := q.login(ctx); e != nil {
		return nil, e
	}
	r, e := q.request(ctx, http.MethodGet, "/api/v2/torrents/info", nil)
	if e != nil {
		return nil, e
	}
	defer r.Body.Close()
	if r.StatusCode/100 != 2 {
		return nil, fmt.Errorf("qBittorrent torrents returned HTTP %d", r.StatusCode)
	}
	var x []Torrent
	e = json.NewDecoder(r.Body).Decode(&x)
	return x, e
}
func (q *QBClient) Version(ctx context.Context) (string, error) {
	if e := q.login(ctx); e != nil {
		return "", e
	}
	r, e := q.request(ctx, http.MethodGet, "/api/v2/app/version", nil)
	if e != nil {
		return "", e
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(r.Body, 1024))
	if r.StatusCode/100 != 2 {
		return "", fmt.Errorf("qBittorrent version returned HTTP %d", r.StatusCode)
	}
	version := strings.TrimSpace(string(b))
	if version == "" {
		return "", errors.New("qBittorrent returned an empty version")
	}
	return version, nil
}

func (q *QBClient) Categories(ctx context.Context) ([]string, error) {
	if e := q.login(ctx); e != nil {
		return nil, e
	}
	return q.categories(ctx)
}

func (q *QBClient) categories(ctx context.Context) ([]string, error) {
	r, e := q.request(ctx, http.MethodGet, "/api/v2/torrents/categories", nil)
	if e != nil {
		return nil, e
	}
	defer r.Body.Close()
	if r.StatusCode/100 != 2 {
		return nil, fmt.Errorf("qBittorrent categories returned HTTP %d", r.StatusCode)
	}
	var rows map[string]json.RawMessage
	if e = json.NewDecoder(r.Body).Decode(&rows); e != nil {
		return nil, e
	}
	names := make([]string, 0, len(rows))
	for name := range rows {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool { return strings.ToLower(names[i]) < strings.ToLower(names[j]) })
	return names, nil
}

func resolveCategory(configured string, available []string) (string, error) {
	configured = strings.TrimSpace(configured)
	if configured == "" {
		return "", nil
	}
	for _, name := range available {
		if strings.EqualFold(strings.TrimSpace(name), configured) {
			return name, nil
		}
	}
	return "", fmt.Errorf("qBittorrent category %q does not exist", configured)
}
func (q *QBClient) Files(ctx context.Context, hash string) ([]TorrentFile, error) {
	if e := q.login(ctx); e != nil {
		return nil, e
	}
	r, e := q.request(ctx, http.MethodGet, "/api/v2/torrents/files?hash="+url.QueryEscape(hash), nil)
	if e != nil {
		return nil, e
	}
	defer r.Body.Close()
	if r.StatusCode/100 != 2 {
		return nil, fmt.Errorf("qBittorrent files returned HTTP %d", r.StatusCode)
	}
	var out []TorrentFile
	e = json.NewDecoder(r.Body).Decode(&out)
	return out, e
}
func (q *QBClient) Add(ctx context.Context, link, category string) (string, error) {
	if e := q.login(ctx); e != nil {
		return "", e
	}
	form := url.Values{"urls": {link}}
	if strings.TrimSpace(category) != "" {
		categories, e := q.categories(ctx)
		if e != nil {
			return "", e
		}
		resolved, e := resolveCategory(category, categories)
		if e != nil {
			return "", e
		}
		form.Set("category", resolved)
	}
	r, e := q.request(ctx, http.MethodPost, "/api/v2/torrents/add", form)
	if e != nil {
		return "", e
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(r.Body)
	if r.StatusCode/100 != 2 {
		return string(b), fmt.Errorf("qBittorrent add returned HTTP %d", r.StatusCode)
	}
	return strings.TrimSpace(string(b)), nil
}
func (q *QBClient) Remove(ctx context.Context, hash string) error {
	return q.remove(ctx, hash, false)
}
func (q *QBClient) DeleteFiles(ctx context.Context, hash string) error {
	return q.remove(ctx, hash, true)
}
func (q *QBClient) remove(ctx context.Context, hash string, deleteFiles bool) error {
	if e := q.login(ctx); e != nil {
		return e
	}
	r, e := q.request(ctx, http.MethodPost, "/api/v2/torrents/delete", url.Values{"hashes": {hash}, "deleteFiles": {strconv.FormatBool(deleteFiles)}})
	if e != nil {
		return e
	}
	defer r.Body.Close()
	if r.StatusCode/100 != 2 {
		return fmt.Errorf("qBittorrent delete returned HTTP %d", r.StatusCode)
	}
	return nil
}

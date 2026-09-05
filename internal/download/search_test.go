package download

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestNyaaSearchAppliesFilenameRules(t *testing.T) {
	client := &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		if !strings.Contains(r.URL.RawQuery, "PRED-888") {
			t.Fatalf("query did not include release ID: %s", r.URL.String())
		}
		body := `<rss><channel><item><title>4k688.com@ PRED-888</title><link>magnet:?xt=accepted</link></item><item><title>PRED-888 other</title><link>magnet:?xt=rejected</link></item><item><title>4k688.com@ OTHER-999</title><link>magnet:?xt=wrong-release</link></item></channel></rss>`
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}
	p := &Nyaa{Client: client, URLTemplate: "https://example.test/?q=<release_id>", AcceptedPatterns: []string{"4k688.com@"}}
	rows, err := p.Search(context.Background(), "PRED-888")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || !rows[0].Accepted || rows[1].Accepted {
		t.Fatalf("unexpected results: %+v", rows)
	}
}

func TestNyaaSearchParsesSeedersAndLeechersFromNamespacedRSSFields(t *testing.T) {
	client := &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		body := `<rss xmlns:nyaa="https://nyaa.si/xmlns/nyaa"><channel>` +
			`<item><title>4k688.com@ PRED-777 high seeds</title><link>magnet:?xt=high</link><nyaa:seeders>42</nyaa:seeders><nyaa:leechers>3</nyaa:leechers><nyaa:size>12.5 GiB</nyaa:size></item>` +
			`<item><title>4k688.com@ PRED-777 low seeds</title><link>magnet:?xt=low</link><nyaa:seeders>1</nyaa:seeders><nyaa:leechers>0</nyaa:leechers></item>` +
			`</channel></rss>`
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}
	p := &Nyaa{Client: client, URLTemplate: "https://example.test/?q=<release_id>", AcceptedPatterns: []string{"4k688.com@"}}
	rows, err := p.Search(context.Background(), "PRED-777")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("unexpected results: %+v", rows)
	}
	if rows[0].Seeds != 42 || rows[0].Peers != 3 {
		t.Fatalf("first result seeds/peers = %d/%d, want 42/3", rows[0].Seeds, rows[0].Peers)
	}
	if rows[0].Size != "12.5 GiB" {
		t.Fatalf("first result size = %q, want 12.5 GiB", rows[0].Size)
	}
	if rows[1].Seeds != 1 || rows[1].Peers != 0 {
		t.Fatalf("second result seeds/peers = %d/%d, want 1/0", rows[1].Seeds, rows[1].Peers)
	}
}

func TestNyaaSearchResolvesActualFilenameAndMagnetFromDetailPage(t *testing.T) {
	mux := http.NewServeMux()
	server := httptest.NewServer(mux)
	defer server.Close()
	mux.HandleFunc("/feed", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<rss><channel><item><title>PRED-888 listing label</title><link>` + server.URL + `/download/123.torrent</link><guid>` + server.URL + `/view/123</guid></item></channel></rss>`))
	})
	mux.HandleFunc("/view/123", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<html><h3 class="panel-title">PRED-888 torrent title</h3><a href="magnet:?xt=urn:btih:abc&amp;dn=PRED-888">Magnet</a><div class="torrent-file-list"><ul><li><i class="fa fa-file"></i>4k688.com@PRED-888.mp4 <span class="file-size">(1 GiB)</span></li></ul></div></html>`))
	})
	p := &Nyaa{Client: server.Client(), URLTemplate: server.URL + "/feed?q=<release_id>", AcceptedPatterns: []string{"4k688.com@"}}
	rows, err := p.Search(context.Background(), "PRED-888")
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows=%v err=%v", rows, err)
	}
	if !rows[0].Accepted || rows[0].Title != "PRED-888 torrent title" || len(rows[0].Files) != 1 || rows[0].Files[0] != "4k688.com@PRED-888.mp4" || !strings.HasPrefix(rows[0].Link, "magnet:?xt=urn:btih:abc") {
		t.Fatalf("detail result was not resolved and vetted: %+v", rows[0])
	}
	// Phase 5A: SourceURL is the human-facing detail page, kept separate
	// from Link (the magnet actually submitted to qBittorrent).
	if rows[0].SourceURL != server.URL+"/view/123" {
		t.Fatalf("SourceURL was not set to the torrent detail page: %+v", rows[0])
	}
	if len(rows[0].FileDetails) != 1 || rows[0].FileDetails[0].SizeBytes != 1<<30 || !rows[0].FileDetails[0].Matched || rows[0].MatchedFile != "4k688.com@PRED-888.mp4" || !rows[0].PreferredFilenameMatch {
		t.Fatalf("torrent file details did not retain size and preferred match: %+v", rows[0])
	}
}

func TestNyaaSearchUsesMagnetDisplayNameWhenNoFileListExists(t *testing.T) {
	client := &http.Client{Transport: transportFunc(func(_ *http.Request) (*http.Response, error) {
		body := `<rss><channel><item><title>PRED-888 listing title</title><link>magnet:?xt=urn:btih:abc&amp;dn=trusted%40PRED-888.mp4</link></item></channel></rss>`
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}
	p := &Nyaa{Client: client, URLTemplate: "https://example.test/?q=<release_id>", AcceptedPatterns: []string{"trusted@"}}
	rows, err := p.Search(context.Background(), "PRED-888")
	if err != nil || len(rows) != 1 || !rows[0].Accepted || len(rows[0].Files) != 1 || rows[0].Files[0] != "trusted@PRED-888.mp4" {
		t.Fatalf("magnet filename fallback: rows=%+v err=%v", rows, err)
	}
}

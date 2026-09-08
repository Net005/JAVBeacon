package scraper

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	xhtml "golang.org/x/net/html"
)

func TestAkibaLiveSession(t *testing.T) {
	if os.Getenv("JAVBEACON_LIVE_SCRAPER_TEST") != "1" {
		t.Skip("set JAVBEACON_LIVE_SCRAPER_TEST=1")
	}
	a := NewAkiba("https://www.akiba-web.com", "/search/index.php?count=1&year=&month=&day=&narrow=&salesform_id=&tag_id=&actor_id=&series_id=&label_id=&sort=1&s_type=&keyword=", 30*time.Second, nil)
	items, err := a.Scrape(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) == 0 {
		t.Fatal("live scrape returned no releases")
	}
	t.Logf("live scrape returned %d releases; first=%s", len(items), items[0].VideoID)
}

func TestAkibaCardRedesignedLayout(t *testing.T) {
	doc, err := xhtml.Parse(strings.NewReader(`<div class="search_sam_box"><div class="pac_thum_box"><img src="/img/spsf52_pac_s.jpg"></div><a href="/product/?product_id=123"><span>Heroine Story</span></a><p>（SPSF-52） Realease Day: 2099/08/15</p></div>`))
	if err != nil {
		t.Fatal(err)
	}
	card := first(doc, func(n *xhtml.Node) bool { return hasClass(n, "search_sam_box") })
	r, ok := NewAkiba("https://www.akiba-web.com", "/search/", 0, nil).card(card)
	if !ok {
		t.Fatal("card was not parsed")
	}
	if r.VideoID != "SPSF-52" || r.ScraperID != "123" || r.Title != "Heroine Story" || r.ReleaseDate != "2099-08-15" {
		t.Fatalf("unexpected release: %+v", r)
	}
}

func TestAkibaCardCurrentSamBoxLayout(t *testing.T) {
	doc, err := xhtml.Parse(strings.NewReader(`<div class="sam_box"><div class="genre145">Naked Heroines</div><a href="/product/index.php?product_id=7759"><img src="/common/spsf57.jpg" width="100%"></a><a href="/product/index.php?product_id=7759"><span>Wonder Lady vs. Glamour Mask</span></a><br><span>Release Date　2026-08-19</span></div>`))
	if err != nil {
		t.Fatal(err)
	}
	card := first(doc, func(n *xhtml.Node) bool { return hasClass(n, "sam_box") })
	r, ok := NewAkiba("https://www.akiba-web.com", "/search/", 0, nil).card(card)
	if !ok {
		t.Fatal("current live card was not parsed")
	}
	if r.VideoID != "SPSF-57" || r.ScraperID != "7759" || r.Title != "Wonder Lady vs. Glamour Mask" || r.ReleaseDate != "2026-08-19" || r.Studio != "GIGA" {
		t.Fatalf("unexpected release: %+v", r)
	}
}

func TestNormalizeAkibaProductURLRepairsLegacyPath(t *testing.T) {
	got := normalizeAkibaProductURL("https://www.akiba-web.com/product/product.php?product_id=6631")
	want := "https://www.akiba-web.com/product/index.php?product_id=6631"
	if got != want {
		t.Fatalf("normalizeAkibaProductURL() = %q, want %q", got, want)
	}
}

func TestAkibaPageEstimateUsesTitleCount(t *testing.T) {
	for _, test := range []struct {
		html string
		want int
	}{
		{`<main><b>4385 Titles</b></main>`, 220},
		{`<main><b>4,400 Titles</b></main>`, 220},
		{`<main><b>4401 Titles</b></main>`, 221},
		{`<main><b>Titles unavailable</b></main>`, 0},
	} {
		doc, err := xhtml.Parse(strings.NewReader(test.html))
		if err != nil {
			t.Fatal(err)
		}
		if got := akibaPageEstimate(doc); got != test.want {
			t.Fatalf("akibaPageEstimate(%q)=%d, want %d", test.html, got, test.want)
		}
	}
}

func TestAkibaFetchRebuildsSessionAfterInvalidHTTP200Page(t *testing.T) {
	var detailCalls, primeCalls int32
	mux := http.NewServeMux()
	mux.HandleFunc("/cookie_set.php", func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&primeCalls, 1)
	})
	mux.HandleFunc("/top.php", func(w http.ResponseWriter, _ *http.Request) {})
	mux.HandleFunc("/product/index.php", func(w http.ResponseWriter, _ *http.Request) {
		if atomic.AddInt32(&detailCalls, 1) == 1 {
			_, _ = w.Write([]byte(`<html><body>Temporary Akiba landing page</body></html>`))
			return
		}
		_, _ = w.Write([]byte(`<html><div id="works_pic"><h5>GHMT-36</h5></div><div id="works_txt">Product detail</div></html>`))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	a := NewAkiba(server.URL, "/search/", 2*time.Second, nil)
	doc, err := a.fetch(context.Background(), server.URL+"/product/index.php?product_id=6541", "detail")
	if err != nil {
		t.Fatal(err)
	}
	if nodeText(doc) == "" || atomic.LoadInt32(&detailCalls) != 2 || atomic.LoadInt32(&primeCalls) != 1 {
		t.Fatalf("detail calls=%d prime calls=%d text=%q", detailCalls, primeCalls, nodeText(doc))
	}
}

func TestAkibaStoryPrefersExpandedTextAndDropsToggleControls(t *testing.T) {
	doc, err := xhtml.Parse(strings.NewReader(`<div id="works_txt">
		<div id="story_list1" style="display:block"><li class="story_window">
			There exists an inhumane organization that treats women as commodities and sells them to the world’s…<br>
			<span><a href="#story_list2">▼More</a></span>
		</li></div>
		<div id="story_list2" style="display:none"><li class="story_window">
			There exists an inhumane organization that treats women as commodities and sells them to the world’s most powerful people. I had no connection to that world... [The Bad End]<br>
			<span><a href="#story_list1">▲Close</a></span>
		</li></div>
	</div>`))
	if err != nil {
		t.Fatal(err)
	}
	want := "There exists an inhumane organization that treats women as commodities and sells them to the world’s most powerful people. I had no connection to that world... [The Bad End]"
	if got := akibaStory(doc); got != want {
		t.Fatalf("story = %q, want %q", got, want)
	}
}

func TestAkibaStoryFallsBackToCollapsedTextWithoutMoreControl(t *testing.T) {
	doc, err := xhtml.Parse(strings.NewReader(`<div id="story_list1"><div class="story_window">Short story… <a href="#story_list2">▼More</a></div></div>`))
	if err != nil {
		t.Fatal(err)
	}
	if got := akibaStory(doc); got != "Short story…" {
		t.Fatalf("story = %q, want collapsed story without toggle control", got)
	}
}

// TestAkibaScrapeFilteredRetriesTransientDetailFetchFailureOnce mirrors
// TestScrapeFilteredRetriesTransientDetailFetchFailureOnce in
// javlibrary_test.go: a.detail already retries internally (fetch ->
// withScrapeRetry, one initial attempt plus scrapeRetryAttempts=2 more), so
// this fails the detail endpoint on all three of those internal attempts
// and only succeeds on the fourth call, which only scrapeFiltered's own
// separate, extra item-level retry (after RetrySecondBackoff) can reach.
func TestAkibaScrapeFilteredRetriesTransientDetailFetchFailureOnce(t *testing.T) {
	withFastRetryBackoff(t)
	var detailCalls int32
	mux := http.NewServeMux()
	mux.HandleFunc("/cookie_set.php", func(w http.ResponseWriter, _ *http.Request) {})
	mux.HandleFunc("/top.php", func(w http.ResponseWriter, _ *http.Request) {})
	mux.HandleFunc("/search/", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<div class="search_sam_box"><div class="pac_thum_box"><img src="/img/spsf52_pac_s.jpg"></div><a href="/product/?product_id=123"><span>Heroine Story</span></a><p>（SPSF-52） Realease Day: 2099/08/15</p></div>`))
	})
	mux.HandleFunc("/product/", func(w http.ResponseWriter, _ *http.Request) {
		if atomic.AddInt32(&detailCalls, 1) <= 3 {
			http.Error(w, "gate error", http.StatusBadGateway)
			return
		}
		_, _ = w.Write([]byte(`<html><div id="works_pic"><h5>Recovered Title</h5></div><div id="works_txt"><dt>Director</dt><dd>Someone</dd></div></html>`))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	var failed []string
	concurrency := ScrapeConcurrency{OnDetailFailure: func(videoID string, _ error) { failed = append(failed, videoID) }}
	items, err := NewAkiba(server.URL, "/search/?count=1", 2*time.Second, nil).ScrapeFiltered(context.Background(), 1, nil, concurrency)
	if err != nil {
		t.Fatal(err)
	}
	if got := atomic.LoadInt32(&detailCalls); got != 4 {
		t.Fatalf("detail endpoint called %d times, want exactly 4 (fetch()'s 3 internal attempts + scrapeFiltered's 1 extra item-level retry)", got)
	}
	if len(failed) != 0 {
		t.Fatalf("OnDetailFailure fired for %v, want the item-level retry to have recovered the release before giving up", failed)
	}
	if len(items) != 1 || items[0].Director != "Someone" {
		t.Fatalf("items=%+v, want the recovered detail page's Director merged in", items)
	}
}

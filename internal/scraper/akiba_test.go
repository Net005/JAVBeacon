package scraper

import (
	"context"
	"os"
	"strings"
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
	if r.VideoID != "SPSF-57" || r.ScraperID != "7759" || r.Title != "Wonder Lady vs. Glamour Mask" || r.ReleaseDate != "2026-08-19" {
		t.Fatalf("unexpected release: %+v", r)
	}
}

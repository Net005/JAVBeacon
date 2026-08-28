package scraper

import (
	"strings"
	"testing"

	"golang.org/x/net/html"
)

func TestListingPageLimitReportsOnlineMaximumWithinCeiling(t *testing.T) {
	doc, err := html.Parse(strings.NewReader(`<nav><a href="?page=2">2</a><a href="?page=47">Last</a><a href="?other=900">Ignore</a></nav>`))
	if err != nil {
		t.Fatal(err)
	}
	if got := listingPageLimit(doc, "page", 500, 3); got != 47 {
		t.Fatalf("online max=%d, want 47", got)
	}
	if got := listingPageLimit(doc, "page", 20, 3); got != 20 {
		t.Fatalf("capped max=%d, want 20", got)
	}
	if got := listingPageLimit(doc, "page", 0, 3); got != 47 {
		t.Fatalf("unlimited online max=%d, want 47", got)
	}
}

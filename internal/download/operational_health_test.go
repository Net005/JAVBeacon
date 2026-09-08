package download

import (
	"testing"

	"github.com/Net005/JAVBeacon/internal/logging"
)

func TestByparrHealthURL(t *testing.T) {
	tests := map[string]string{
		"http://byparr:8191/v1":  "http://byparr:8191/health",
		"http://byparr:8191/v1/": "http://byparr:8191/health",
		"http://byparr:8191":     "http://byparr:8191/health",
	}
	for input, want := range tests {
		if got := byparrHealthURL(input); got != want {
			t.Errorf("byparrHealthURL(%q)=%q, want %q", input, got, want)
		}
	}
}

func TestOperationalErrorCategoryIncludesJavLibraryScrapingFailures(t *testing.T) {
	tests := []struct {
		name  string
		entry logging.Entry
		want  string
	}{
		{
			name:  "JavLibrary product detail warning",
			entry: logging.Entry{Level: "WARN", Message: "product detail failed", Fields: map[string]any{"provider": "JavLibrary"}},
			want:  "scraping",
		},
		{
			name:  "JavLibrary historical directory warning",
			entry: logging.Entry{Level: "WARN", Message: "historical index directory unavailable", Fields: map[string]any{"provider": "JavLibrary"}},
			want:  "scraping",
		},
		{
			name:  "site refresh error",
			entry: logging.Entry{Level: "ERROR", Message: "refresh failed", Fields: map[string]any{"site": "JavLibrary"}},
			want:  "scraping",
		},
		{
			name:  "ordinary JavLibrary information",
			entry: logging.Entry{Level: "INFO", Message: "JavLibrary scrape completed", Fields: map[string]any{"provider": "JavLibrary"}},
			want:  "",
		},
		{
			name:  "HTTP search remains separate",
			entry: logging.Entry{Level: "WARN", Message: "HTTP provider search failed", Fields: map[string]any{"provider": "JavDB / Keepshare"}},
			want:  "http_search",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := operationalErrorCategory(test.entry); got != test.want {
				t.Fatalf("category=%q, want %q", got, test.want)
			}
		})
	}
}

func TestOperationalErrorIdentityCollapsesRetriesForSameRelease(t *testing.T) {
	first := logging.Entry{Message: "product detail failed", Fields: map[string]any{"provider": "JavLibrary", "video_id": "ATID-803", "error": "Cloudflare 403"}}
	retry := logging.Entry{Message: "product detail failed", Fields: map[string]any{"provider": "JavLibrary", "video_id": "ATID-803", "error": "Byparr timeout"}}
	other := logging.Entry{Message: "product detail failed", Fields: map[string]any{"provider": "JavLibrary", "video_id": "ATID-804", "error": "Cloudflare 403"}}
	if operationalErrorIdentity("scraping", first) != operationalErrorIdentity("scraping", retry) {
		t.Fatal("retry errors for the same release should have one incident identity")
	}
	if operationalErrorIdentity("scraping", first) == operationalErrorIdentity("scraping", other) {
		t.Fatal("different releases should have different incident identities")
	}
}

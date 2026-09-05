package download

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Net005/JAVBeacon/internal/domain"
	"golang.org/x/net/html"
)

func TestDiscoverPikPakShareIDFollowsKeepshareIntermediateAndStopsBeforePikPak(t *testing.T) {
	intermediate := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://mypikpak.com/s/VP-YhHbopMQjt_gRbkB6ORjZo2/AAAAAAfNVsQqoHTeqzJ8lh0Yo2_VP-?act=play", http.StatusFound)
	}))
	defer intermediate.Close()
	entry := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, intermediate.URL+"/forwarded-share", http.StatusMovedPermanently)
	}))
	defer entry.Close()
	shareID, err := discoverPikPakShareID(context.Background(), entry.Client(), entry.URL)
	if err != nil {
		t.Fatal(err)
	}
	if shareID != "VP-YhHbopMQjt_gRbkB6ORjZo2" {
		t.Fatalf("share ID=%q", shareID)
	}
}

func TestDiscoverPikPakShareIDParsesDirectPlayerURLWithoutRequest(t *testing.T) {
	shareID, err := discoverPikPakShareID(context.Background(), &http.Client{Timeout: time.Nanosecond}, "https://mypikpak.com/s/direct-share/direct-file?act=play")
	if err != nil {
		t.Fatal(err)
	}
	if shareID != "direct-share" {
		t.Fatalf("share ID=%q", shareID)
	}
}

func TestReleaseIDMatchesTextIsCaseInsensitiveAndRejectsHalfMatches(t *testing.T) {
	for _, value := range []string{"ADN-803", "adn803.mp4", "[source] AdN_803-U.mp4"} {
		if !releaseIDMatchesText(value, "ADN-803") {
			t.Fatalf("expected %q to match", value)
		}
	}
	for _, value := range []string{"ADN-8030.mp4", "XADN-803.mp4", "ADN-80.mp4"} {
		if releaseIDMatchesText(value, "ADN-803") {
			t.Fatalf("expected half-match %q to be rejected", value)
		}
	}
}

func TestJavDBCandidatesRequireExactIDAndCarryFileSize(t *testing.T) {
	doc, err := html.Parse(strings.NewReader(`<html><body>
		<div class="item"><span class="name">ADN-803-U.torrent</span><span class="meta">4.43GB, 2 files</span><a href="https://keepshare.org/u">Download</a><a href="https://keepshare.org/u-alternate">Alternate</a></div>
		<div class="item"><span class="name">ADN803.torrent</span><span class="meta">3.45GB</span><a href="https://keepshare.org/plain">Download</a></div>
		<div class="item"><span class="name">ADN-8030.torrent</span><span class="meta">9.99GB</span><a href="https://keepshare.org/wrong">Download</a></div>
	</body></html>`))
	if err != nil {
		t.Fatal(err)
	}
	rows := parseJavDBDownloadCandidates(doc, "https://javdb.com/v/example", "adn-803")
	if len(rows) != 3 {
		t.Fatalf("got %d candidates, want every distinct Keepshare link (3)", len(rows))
	}
	if rows[0].SizeBytes == 0 || rows[1].SizeBytes == 0 {
		t.Fatal("expected parsed byte sizes")
	}
}

func TestJavDBSortingPrefersConfiguredFilenamePatternsBeforeNormalHTTPOrder(t *testing.T) {
	rows := []domain.SearchResult{
		{Title: "ADN-803.mp4", SizeBytes: 9 << 30},
		{Title: "trusted@ ADN-803-U.mp4", SizeBytes: 5 << 30},
		{Title: "trusted@ ADN-803.mp4", SizeBytes: 3 << 30},
	}
	sortJavDBDownloadCandidates(rows, "ADN-803", []string{"trusted@"})
	if rows[0].Title != "trusted@ ADN-803.mp4" || rows[1].Title != "trusted@ ADN-803-U.mp4" || rows[2].Title != "ADN-803.mp4" {
		t.Fatalf("unexpected preferred HTTP order: %+v", rows)
	}
	if !strings.Contains(rows[0].Reason, "trusted@") || !strings.Contains(rows[1].Reason, "trusted@") {
		t.Fatalf("preferred candidates should explain their matching pattern: %+v", rows)
	}
}

func TestPikPakFileSelectionUsesPreferredPatternsThenLargestFallback(t *testing.T) {
	files := []pikPakFile{
		{ID: "large", Name: "ADN-803.mp4", Size: "9000"},
		{ID: "preferred", Name: "trusted@ ADN-803.mp4", Size: "3000"},
		{ID: "other", Name: "ADN-803 sample.mp4", Size: "1000"},
	}
	selected, found := selectPikPakFile(files, "ADN-803", []string{"trusted@"})
	if !found || selected.ID != "preferred" {
		t.Fatalf("preferred file was not selected: %+v, found=%v", selected, found)
	}
	selected, found = selectPikPakFile(files, "ADN-803", []string{"does-not-match"})
	if !found || selected.ID != "large" {
		t.Fatalf("largest fallback file was not selected: %+v, found=%v", selected, found)
	}
}

func TestPikPakSearchFilesExposeSizesAndMatchedFile(t *testing.T) {
	files := []pikPakFile{
		{ID: "noise", Name: "sample.mp4", Size: "10485760"},
		{ID: "matched", Name: "hhd800.com@DLDSS-530.mp4", Size: "4294967296"},
	}
	names, details := pikPakSearchFiles(files, files[1])
	if len(names) != 2 || len(details) != 2 {
		t.Fatalf("unexpected search file metadata: names=%+v details=%+v", names, details)
	}
	if details[0].Matched || details[0].SizeBytes != 10485760 || !details[1].Matched || details[1].SizeBytes != 4294967296 {
		t.Fatalf("file sizes or matched marker were not preserved: %+v", details)
	}
}

func TestNextHTTPDestinationUsesStableCollisionSuffixes(t *testing.T) {
	dir := t.TempDir()
	if got := nextHTTPDestination(dir, "ADN-803"); got != filepath.Join(dir, "ADN-803.mp4") {
		t.Fatalf("first path = %q", got)
	}
	for _, name := range []string{"ADN-803.mp4", "ADN-803-0.mp4"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if got := nextHTTPDestination(dir, "ADN-803"); got != filepath.Join(dir, "ADN-803-1.mp4") {
		t.Fatalf("collision path = %q", got)
	}
}

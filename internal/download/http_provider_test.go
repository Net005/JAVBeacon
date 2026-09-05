package download

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/net/html"
)

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
		<div class="item"><span class="name">ADN-803-U.torrent</span><span class="meta">4.43GB, 2 files</span><a href="https://keepshare.org/u">Download</a></div>
		<div class="item"><span class="name">ADN803.torrent</span><span class="meta">3.45GB</span><a href="https://keepshare.org/plain">Download</a></div>
		<div class="item"><span class="name">ADN-8030.torrent</span><span class="meta">9.99GB</span><a href="https://keepshare.org/wrong">Download</a></div>
	</body></html>`))
	if err != nil {
		t.Fatal(err)
	}
	rows := parseJavDBDownloadCandidates(doc, "https://javdb.com/v/example", "adn-803")
	if len(rows) != 2 {
		t.Fatalf("got %d candidates, want 2", len(rows))
	}
	if rows[0].SizeBytes == 0 || rows[1].SizeBytes == 0 {
		t.Fatal("expected parsed byte sizes")
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

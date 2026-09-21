package discovery

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// TestTruncateForLogPreservesUTF8Boundary guards the debug logging added so a
// rejected Ollama response is diagnosable from Live Logs: the snippet must
// never split a multi-byte rune, even when the cut point lands mid-character
// and even when the string is already short enough to pass through as-is.
func TestTruncateForLogPreservesUTF8Boundary(t *testing.T) {
	short := "Match: short reason."
	if got := truncateForLog(short, 1000); got != short {
		t.Fatalf("short string was altered: %q", got)
	}
	long := strings.Repeat("あ", 2000) // each rune is 3 bytes in UTF-8
	got := truncateForLog(long, 1000)
	if !utf8.ValidString(got) {
		t.Fatalf("truncated snippet is not valid UTF-8: %q", got)
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("truncated snippet missing ellipsis marker: %q", got)
	}
	if len(got) > 1000+len("…") {
		t.Fatalf("truncated snippet exceeds requested bound: %d bytes", len(got))
	}
}

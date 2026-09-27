package filterpreset

import (
	"strings"
	"testing"
)

// TestSanitizeTagNameCleansUpBeforeBecomingAGenreTag guards the
// "Collection: <name>" / Watchlist genre-tag substitute against a saved
// filter set's name - free text an admin can type into the web UI -
// producing a garbled or unbounded tag. A real Jellyfin collection name is
// never sanitized this way; only the flat-string tag value is.
func TestSanitizeTagNameCleansUpBeforeBecomingAGenreTag(t *testing.T) {
	for _, test := range []struct{ name, in, want string }{
		{"unchanged", "My Favorites", "My Favorites"},
		{"collapses internal whitespace", "My\t\tFavorites\n\nList", "My Favorites List"},
		{"trims ends", "  Padded  ", "Padded"},
		{"strips control characters", "Weird\x00Name", "Weird Name"},
		{"empty after cleanup", "\x00\x01\x02", ""},
		{"unicode preserved", "お気に入り", "お気に入り"},
	} {
		if got := SanitizeTagName(test.in); got != test.want {
			t.Errorf("%s: SanitizeTagName(%q) = %q, want %q", test.name, test.in, got, test.want)
		}
	}
	long := strings.Repeat("a", MaxTagNameRunes+50)
	got := SanitizeTagName(long)
	if runes := []rune(got); len(runes) != MaxTagNameRunes {
		t.Fatalf("long name not capped: got %d runes, want %d", len(runes), MaxTagNameRunes)
	}
}

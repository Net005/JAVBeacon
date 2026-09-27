package filterpreset

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Net005/JAVBeacon/internal/domain"
	"github.com/Net005/JAVBeacon/internal/store"
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

func TestResolveReleaseIDsIncludesLocalWhenSavedFilterHidesIt(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "presets.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	site, err := st.SaveSite(ctx, domain.Site{Title: "Test", Type: "Site", Name: "Test", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, code := range []string{"LOCAL-1", "REMOTE-2"} {
		if _, err := st.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: code, Title: "Substitute " + code, Source: "Test"}); err != nil {
			t.Fatal(err)
		}
	}
	local, err := st.Releases(ctx, domain.ReleaseFilter{VideoID: "LOCAL-1", Limit: 1})
	if err != nil || len(local) != 1 {
		t.Fatalf("local release: %v, %v", local, err)
	}
	if err := st.SetStashState(ctx, local[0].ID, true, "scene-1"); err != nil {
		t.Fatal(err)
	}
	filter := domain.ReleaseFilter{Search: "Substitute", HideLocal: true, HideMonitored: true, Sort: "release", Direction: "desc", SearchExpression: `{"logic":"and","groups":[{"logic":"or","conditions":[{"field":"title","value":"Substitute"}]},{"logic":"and","conditions":[{"field":"monitored","value":"false"},{"field":"local","value":"false"}]}]}`}
	ids, err := ResolveReleaseIDs(ctx, st, filter)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != local[0].ID {
		t.Fatalf("collection members = %v, want [%d]", ids, local[0].ID)
	}
}

func TestCollectionSearchExpressionPreservesContentFilters(t *testing.T) {
	raw := `{"logic":"and","groups":[{"logic":"or","conditions":[{"field":"title","value":"prison"},{"field":"tag","value":"Confinement"}]},{"logic":"and","conditions":[{"field":"monitored","value":"false"},{"field":"local","value":"false"}]}]}`
	got := collectionSearchExpression(raw)
	if strings.Contains(got, `"field":"local"`) || strings.Contains(got, `"field":"monitored"`) || !strings.Contains(got, `"field":"title"`) || !strings.Contains(got, `"field":"tag"`) {
		t.Fatalf("collection expression lost content filters or kept availability filters: %s", got)
	}
	if got := collectionSearchExpression(`{"logic":"and","conditions":[{"field":"local","value":"false"}]}`); got != "" {
		t.Fatalf("availability-only expression = %s, want empty", got)
	}
}

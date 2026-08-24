package download

import (
	"testing"

	"github.com/Net005/JAVBeacon/internal/domain"
)

func TestReleaseIgnoredByTagIsCaseInsensitiveAndTrimmed(t *testing.T) {
	r := domain.Release{Title: "Some Title", Genres: []string{"Drama", " Big Tits "}}
	ignored, reason := releaseIgnored(r, []string{"big tits"}, nil)
	if !ignored {
		t.Fatalf("expected release with genre %q to be ignored by tag rule %q", r.Genres, "big tits")
	}
	if reason == "" {
		t.Fatalf("expected a non-empty skip reason")
	}
}

func TestReleaseIgnoredByTagDoesNotMatchSubstring(t *testing.T) {
	// A tag ignore rule is a whole-tag match, not a substring one - ignoring
	// "Drama" must not also ignore a release merely tagged "Dramatic" or
	// similar, unlike title rules which are intentionally substring-based.
	r := domain.Release{Title: "Some Title", Genres: []string{"Dramatic Reunion"}}
	if ignored, reason := releaseIgnored(r, []string{"Drama"}, nil); ignored {
		t.Fatalf("did not expect substring tag match, got ignored with reason %q", reason)
	}
}

func TestReleaseIgnoredByTitlePlainSubstring(t *testing.T) {
	r := domain.Release{Title: `EYAN-228 "I Want To Be Insulted Like Garbage..." Married For 4 Years`}
	if ignored, _ := releaseIgnored(r, nil, []string{"insulted like garbage"}); !ignored {
		t.Fatalf("expected plain substring ignore-title match")
	}
	if ignored, _ := releaseIgnored(r, nil, []string{"not in this title"}); ignored {
		t.Fatalf("did not expect a match for unrelated text")
	}
}

func TestReleaseIgnoredByTitleWildcard(t *testing.T) {
	r := domain.Release{Title: "Married For 4 Years, 29 Years Old, Occupation: Resignation Assistance"}
	cases := []struct {
		pattern string
		want    bool
	}{
		{"Married*Occupation*", true},
		{"Married ?or 4 Years*", true},
		{"Single*", false},
	}
	for _, c := range cases {
		if got, _ := releaseIgnored(r, nil, []string{c.pattern}); got != c.want {
			t.Fatalf("pattern %q: got ignored=%v, want %v", c.pattern, got, c.want)
		}
	}
}

func TestReleaseIgnoredEmptyRulesNeverMatch(t *testing.T) {
	r := domain.Release{Title: "Anything", Genres: []string{"Anything"}}
	if ignored, _ := releaseIgnored(r, nil, nil); ignored {
		t.Fatalf("expected no rules to mean never ignored")
	}
	if ignored, _ := releaseIgnored(r, []string{""}, []string{"  "}); ignored {
		t.Fatalf("expected blank rule entries to be skipped rather than matching everything")
	}
}

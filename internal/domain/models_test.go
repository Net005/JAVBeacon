package domain

import (
	"reflect"
	"testing"
)

// TestParseIgnoreList covers the bug report that tags like "Best, Omnibus"
// never matched because ignore lists used to be split on commas as well as
// newlines, breaking a single comma-containing tag into two useless
// fragments. Entries are one-per-line now, so a comma (or any other
// character, including spaces, as in "Big Tits") inside an entry must
// survive intact.
func TestParseIgnoreList(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want []string
	}{
		{
			name: "newline separated",
			raw:  "Solowork\nCreampie",
			want: []string{"Solowork", "Creampie"},
		},
		{
			name: "comma preserved within one entry",
			raw:  "Best, Omnibus\nSolowork",
			want: []string{"Best, Omnibus", "Solowork"},
		},
		{
			name: "space preserved within one entry",
			raw:  "Big Tits\nSolowork",
			want: []string{"Big Tits", "Solowork"},
		},
		{
			name: "blank lines and surrounding whitespace are skipped/trimmed",
			raw:  "  Big Tits  \n\n\nSolowork\n   \n",
			want: []string{"Big Tits", "Solowork"},
		},
		{
			name: "empty string yields no entries",
			raw:  "",
			want: nil,
		},
		{
			name: "only whitespace yields no entries",
			raw:  "   \n\t\n  ",
			want: nil,
		},
		{
			name: "single entry with no trailing newline",
			raw:  "Best, Omnibus",
			want: []string{"Best, Omnibus"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseIgnoreList(tc.raw)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("ParseIgnoreList(%q) = %#v, want %#v", tc.raw, got, tc.want)
			}
		})
	}
}

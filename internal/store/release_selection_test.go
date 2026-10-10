package store

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/Net005/JAVBeacon/internal/domain"
)

func TestReleaseIDsMatchesFullSelectionFiltersAndOrdering(t *testing.T) {
	s := syntheticSearchCatalog(t, 520)
	ctx := context.Background()
	if _, err := s.db.Exec(`UPDATE releases SET watchlist=id%2,is_local=id%3=0,release_date=CASE WHEN id%4=0 THEN '' ELSE '2026-01-01' END`); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveDiscoveryScores(ctx, map[int64]float64{1: 90, 2: 10, 3: 75}); err != nil {
		t.Fatal(err)
	}
	for _, f := range []domain.ReleaseFilter{
		{Sort: "added"}, {Sort: "name", Direction: "asc"}, {Sort: "release_score"}, {Sort: "release_score", Direction: "asc"},
		{Watchlist: true}, {HideLocal: true}, {Search: "EXAMPLE-52"},
		{Category: "label", Entries: "Example Label 03"},
		{SearchExpression: `{"conditions":[{"field":"local","value":"true"}]}`},
		{Search: "No matching synthetic record"},
	} {
		t.Run(f.Sort+f.Search+f.Entries+f.SearchExpression, func(t *testing.T) {
			// Display pagination must not cap an all-matching selection.
			f.Limit = 1
			f.Offset = 7
			got, err := s.ReleaseIDs(ctx, f)
			if err != nil {
				t.Fatal(err)
			}
			want := make([]int64, 0)
			f.Limit = 500
			f.Offset = 0
			for {
				rows, err := s.Releases(ctx, f)
				if err != nil {
					t.Fatal(err)
				}
				for _, r := range rows {
					want = append(want, r.ID)
				}
				if len(rows) < 500 {
					break
				}
				f.Offset += len(rows)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("ID-only selection differs: got %v, want %v", got, want)
			}
		})
	}
}

func TestLabelSuggestionsOnlyIncludeLinkedSitesAndDeduplicate(t *testing.T) {
	s := syntheticSearchCatalog(t, 12)
	now := time.Now()
	if _, err := s.db.Exec(`INSERT INTO sites(id,name,title,url,created_at,updated_at) VALUES(100,'extra','Synthetic Optional Label','https://example.com',?,?)`, now, now); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	values, err := s.ReleaseFilterOptions(ctx, "label", "Optional")
	if err != nil {
		t.Fatal(err)
	}
	if len(values) != 0 {
		t.Fatalf("unlinked site suggested: %v", values)
	}
	for _, id := range []int{1, 2, 3} {
		if _, err := s.db.Exec(`INSERT INTO release_sites(release_id,site_id) VALUES(?,100)`, id); err != nil {
			t.Fatal(err)
		}
	}
	values, err = s.ReleaseFilterOptions(ctx, "label", "optional")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(values, []string{"Synthetic Optional Label"}) {
		t.Fatalf("duplicate or missing linked label: %v", values)
	}
}

func TestTextSearchMetadataMembershipPreservesLogic(t *testing.T) {
	s := syntheticSearchCatalog(t, 12)
	if _, err := s.db.Exec(`UPDATE release_actresses SET name='Mina Example',name_normalized='mina example' WHERE release_id=2`); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		search    string
		wildcards bool
		logic     string
		want      []int64
	}{
		{"Genre 07", false, "", []int64{7}},
		{"Example Mina", false, "", []int64{2}},
		{"Example Label 16", false, "", []int64{12}},
		{"Genre 07,Performer 03", true, "or", []int64{3, 7}},
		{"Genre 07,Performer 03", true, "and", []int64{}},
		{"Genre 03,Performer 03", true, "and", []int64{3}},
	} {
		t.Run(tc.search+tc.logic, func(t *testing.T) {
			f := domain.ReleaseFilter{Search: tc.search, SearchWildcards: tc.wildcards, WildcardLogic: tc.logic, Sort: "added", Direction: "asc"}
			ids, err := s.ReleaseIDs(context.Background(), f)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(ids, tc.want) {
				t.Fatalf("got %v, want %v", ids, tc.want)
			}
			count, err := s.ReleasesCount(context.Background(), f)
			if err != nil || count != len(tc.want) {
				t.Fatalf("count=%d err=%v", count, err)
			}
		})
	}
}

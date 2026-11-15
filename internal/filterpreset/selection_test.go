package filterpreset

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/Net005/JAVBeacon/internal/domain"
	"github.com/Net005/JAVBeacon/internal/store"
)

// Embedding only Store deliberately hides optional selection capabilities.
type fullSelectionStore struct {
	store.Store
	calls []domain.ReleaseFilter
}

func (s *fullSelectionStore) Releases(ctx context.Context, f domain.ReleaseFilter) ([]domain.Release, error) {
	s.calls = append(s.calls, f)
	return s.Store.Releases(ctx, f)
}

type idSelectionStore struct {
	*fullSelectionStore
	selectIDs func(context.Context, domain.ReleaseFilter) ([]int64, error)
	filters   []domain.ReleaseFilter
}

func (s *idSelectionStore) ReleaseIDs(ctx context.Context, f domain.ReleaseFilter) ([]int64, error) {
	s.filters = append(s.filters, f)
	return s.selectIDs(ctx, f)
}

func TestResolveReleaseIDsDiscardsPartialIDSelectionOnError(t *testing.T) {
	ctx := context.WithValue(context.Background(), struct{}{}, "selection context")
	wantErr := errors.New("synthetic selection failure")
	st := &idSelectionStore{
		fullSelectionStore: &fullSelectionStore{},
		selectIDs: func(gotCtx context.Context, _ domain.ReleaseFilter) ([]int64, error) {
			if gotCtx != ctx {
				t.Fatal("selection did not receive caller context")
			}
			return []int64{42}, wantErr
		},
	}
	ids, err := ResolveReleaseIDs(ctx, st, domain.ReleaseFilter{})
	if !errors.Is(err, wantErr) || ids != nil {
		t.Fatalf("selection = %v, %v; want nil IDs and original error", ids, err)
	}
	if len(st.calls) != 0 || len(st.filters) != 1 {
		t.Fatalf("full Releases calls = %d, ReleaseIDs calls = %d; want 0, 1", len(st.calls), len(st.filters))
	}
}

func TestResolveReleaseIDsUsesIDSelectionWithFullSelectionParity(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "selection.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	site, err := st.SaveSite(ctx, domain.Site{Title: "Synthetic", Name: "Synthetic", Type: "Site", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 506; i++ {
		genres := []string{"Action"}
		if i%7 == 0 {
			genres = []string{"Drama"}
		}
		title := fmt.Sprintf("Synthetic %02d", i%13)
		if i%11 == 0 {
			title += " ignored"
		}
		code := fmt.Sprintf("SYNTH-%04d", i)
		if _, err := st.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: code, Title: title, Genres: genres, ReleaseDate: "2026-01-01", Source: "Synthetic"}); err != nil {
			t.Fatal(err)
		}
		rows, err := st.Releases(ctx, domain.ReleaseFilter{VideoID: code, Limit: 1})
		if err != nil || len(rows) != 1 {
			t.Fatalf("seed release %s: rows=%v err=%v", code, rows, err)
		}
		r := rows[0]
		local, scene := i < 504, fmt.Sprintf("scene-%d", i)
		if i == 502 {
			scene = " " // Nonempty is the contract, not trimmed nonempty.
		}
		if i == 503 || i == 505 {
			scene = ""
		}
		if err := st.SetStashState(ctx, r.ID, local, scene); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.SaveSettings(ctx, map[string]string{"ignore_tags": "Drama", "ignore_titles": "ignored"}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name      string
		filter    domain.ReleaseFilter
		wantCount int
	}{
		{"all across page boundary", domain.ReleaseFilter{Sort: "name", Direction: "asc"}, 503},
		{"descending ties", domain.ReleaseFilter{Sort: "release", Direction: "desc"}, 503},
		{"preferred", domain.ReleaseFilter{UsePreferred: true, IgnoreTags: []string{"Drama"}, IgnoreTitles: []string{"ignored"}, Sort: "name", Direction: "desc"}, -1},
		{"direct ignores", domain.ReleaseFilter{IgnoreTags: []string{"Drama"}, IgnoreTitles: []string{"ignored"}, Sort: "release_score", Direction: "asc"}, -1},
		{"structured content", domain.ReleaseFilter{SearchExpression: `{"logic":"and","conditions":[{"field":"local","value":"false"},{"field":"monitored","value":"false"},{"field":"title","value":"Synthetic 01"}]}`, Sort: "name"}, -1},
		{"no matches", domain.ReleaseFilter{Search: "No synthetic match"}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := tc.filter
			f.HideLocal, f.HideMonitored = true, true
			f.Limit, f.Offset = 1, 900
			legacy := &fullSelectionStore{Store: st}
			want, err := ResolveReleaseIDs(ctx, legacy, f)
			if err != nil {
				t.Fatal(err)
			}
			if tc.wantCount >= 0 && len(want) != tc.wantCount {
				t.Fatalf("full selection count = %d, want %d", len(want), tc.wantCount)
			}
			if tc.wantCount < 0 && (len(want) == 0 || len(want) >= 503) {
				t.Fatalf("content/preference filter did not narrow selection: %d", len(want))
			}
			fast := &idSelectionStore{fullSelectionStore: &fullSelectionStore{Store: st}, selectIDs: st.ReleaseIDs}
			got, err := ResolveReleaseIDs(ctx, fast, f)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("ID-only selection = %v, full selection = %v", got, want)
			}
			if len(fast.calls) != 0 || len(fast.filters) != 1 {
				t.Fatalf("full Releases calls = %d, ReleaseIDs calls = %d; want 0, 1", len(fast.calls), len(fast.filters))
			}
			if !reflect.DeepEqual(fast.filters[0], legacy.calls[0]) {
				t.Fatalf("ID selection filter = %+v, full selection filter = %+v", fast.filters[0], legacy.calls[0])
			}
		})
	}
}

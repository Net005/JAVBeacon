package backfill

import (
	"context"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Net005/JAVBeacon/internal/domain"
	"github.com/Net005/JAVBeacon/internal/scraper"
	"github.com/Net005/JAVBeacon/internal/store"
)

type fakeHistoricalScraper struct {
	mu       sync.Mutex
	releases []domain.Release
}

func (f *fakeHistoricalScraper) HistoricalIndexes(context.Context) ([]scraper.HistoricalIndex, error) {
	return []scraper.HistoricalIndex{{URL: "https://www.javlibrary.com/en/vl_genre.php?g=test&mode=2", Kind: "genre", Name: "Test"}}, nil
}
func (f *fakeHistoricalScraper) HistoricalPage(_ context.Context, _ string, page int, include func(string) bool) ([]domain.Release, []string, int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if page > 1 {
		return nil, nil, 1, nil
	}
	ids := make([]string, 0, len(f.releases))
	var out []domain.Release
	for _, r := range f.releases {
		ids = append(ids, r.VideoID)
		if include(r.VideoID) {
			out = append(out, r)
		}
	}
	return out, ids, 1, nil
}

func waitStopped(t *testing.T, s *Service) Status {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		x := s.Status(context.Background())
		if !x.Running {
			return x
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("backfill did not stop")
	return Status{}
}

func TestResumeCatchesNewHeadWithoutReprocessingKnownDetails(t *testing.T) {
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "backfill.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	fake := &fakeHistoricalScraper{releases: []domain.Release{{VideoID: "OLD-1", Title: "Old", ReleaseDate: "2020-01-01", Source: "JavLibrary", ProductURL: "https://www.javlibrary.com/en/?v=old"}}}
	s := New(st, fake, slog.Default())
	if err = s.Start(context.Background(), false, 0); err != nil {
		t.Fatal(err)
	}
	first := waitStopped(t, s)
	if first.State != "completed" || first.Historical.ReleasesCompleted != 1 {
		t.Fatalf("first status: %+v", first)
	}
	fake.mu.Lock()
	fake.releases = []domain.Release{{VideoID: "NEW-2", Title: "New", ReleaseDate: "2024-01-01", Source: "JavLibrary", ProductURL: "https://www.javlibrary.com/en/?v=new"}, {VideoID: "OLD-1", Title: "Old", ReleaseDate: "2020-01-01", Source: "JavLibrary"}}
	fake.mu.Unlock()
	if err = s.Start(context.Background(), true, 0); err != nil {
		t.Fatal(err)
	}
	second := waitStopped(t, s)
	if second.State != "completed" || second.Historical.ReleasesCompleted != 2 {
		t.Fatalf("resume status: %+v", second)
	}
	if second.RunCompleted != 1 || second.RunSkipped < 1 {
		t.Fatalf("current-run counters: %+v", second)
	}
}

func TestFreshBackfillSkipsReleaseAlreadyInMainLibrary(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "known.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	site, err := st.SaveSite(ctx, domain.Site{Title: "Existing", Type: "Genre", Name: "JavLibrary", URL: "https://www.javlibrary.com/en/vl_genre.php?g=old", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = st.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "KNOWN-1", Title: "Already here", ReleaseDate: "2021-03-04", Source: "JavLibrary"}); err != nil {
		t.Fatal(err)
	}
	fake := &fakeHistoricalScraper{releases: []domain.Release{{VideoID: "KNOWN-1", Title: "Would be fetched", ReleaseDate: "2021-03-04", Source: "JavLibrary"}}}
	s := New(st, fake, slog.Default())
	if err = s.Start(ctx, false, 0); err != nil {
		t.Fatal(err)
	}
	status := waitStopped(t, s)
	if status.RunCompleted != 0 || status.RunSkipped != 1 {
		t.Fatalf("existing release was not skipped: %+v", status)
	}
	item, ok, err := st.HistoricalBackfillItem(ctx, "KNOWN-1")
	if err != nil || !ok || item.State != "completed" || item.ReleaseDate != "2021-03-04" {
		t.Fatalf("durable known item: %+v ok=%v err=%v", item, ok, err)
	}
}

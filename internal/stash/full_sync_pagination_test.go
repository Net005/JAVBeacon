package stash

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/Net005/JAVBeacon/internal/domain"
	"github.com/Net005/JAVBeacon/internal/store"
)

type fullSyncPagedStore struct {
	store.Store
	cursors              []int64
	snapshotErr, pageErr error
	snapshots            int
}

func (s *fullSyncPagedStore) Settings(context.Context) (map[string]string, error) {
	return map[string]string{"stash_base_url": "https://stash.example"}, nil
}
func (s *fullSyncPagedStore) SaveSettings(context.Context, map[string]string) error { return nil }
func (s *fullSyncPagedStore) Releases(context.Context, domain.ReleaseFilter) ([]domain.Release, error) {
	return nil, errors.New("full hydration forbidden")
}
func (s *fullSyncPagedStore) ReleasesCount(context.Context, domain.ReleaseFilter) (int, error) {
	return 0, errors.New("full count forbidden")
}
func (s *fullSyncPagedStore) StashSyncSnapshot(context.Context, domain.ReleaseFilter) (int64, int, error) {
	s.snapshots++
	return 1004, 502, s.snapshotErr
}
func (s *fullSyncPagedStore) StashSyncReleases(_ context.Context, _ domain.ReleaseFilter, after, high int64, limit int) ([]domain.Release, error) {
	s.cursors = append(s.cursors, after)
	if s.pageErr != nil {
		return nil, s.pageErr
	}
	if high != 1004 || limit != 500 {
		return nil, fmt.Errorf("bounds changed: high=%d limit=%d", high, limit)
	}
	var rows []domain.Release
	// Include a concurrently inserted row above the snapshot. IDs have gaps,
	// so an offset or row-count cursor cannot accidentally pass this test.
	for id := int64(2); id <= 1006; id += 2 {
		if id > after && id <= high && len(rows) < limit {
			rows = append(rows, domain.Release{ID: id, VideoID: fmt.Sprintf("TEST-%d", id)})
		}
	}
	return rows, nil
}

func TestFullSyncKeysetCrossesPageBoundary(t *testing.T) {
	st := &fullSyncPagedStore{}
	svc := fullSyncPagedService(st)
	svc.run(context.Background())
	status := svc.Status()
	if status.Error != "" || status.Total != 502 || status.Processed != 502 {
		t.Fatalf("status=%+v", status)
	}
	if st.snapshots != 1 || !reflect.DeepEqual(st.cursors, []int64{0, 1000}) {
		t.Fatalf("snapshots=%d cursors=%v", st.snapshots, st.cursors)
	}
}

func TestFullSyncKeysetErrorsDoNotFallBack(t *testing.T) {
	for _, phase := range []string{"snapshot", "page"} {
		t.Run(phase, func(t *testing.T) {
			st := &fullSyncPagedStore{}
			expected := errors.New("reader unavailable")
			if phase == "snapshot" {
				st.snapshotErr = expected
			} else {
				st.pageErr = expected
			}
			svc := fullSyncPagedService(st)
			svc.run(context.Background())
			if status := svc.Status(); status.Error != expected.Error() || status.Phase != "Failed" || status.Processed != 0 {
				t.Fatalf("status=%+v", status)
			}
		})
	}
}

func fullSyncPagedService(st store.Store) *Service {
	return &Service{store: st, log: slog.New(slog.NewTextHandler(io.Discard, nil)), client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"data":{"findScenes":{"count":0,"scenes":[]}}}`)), Header: make(http.Header)}, nil
	})}}
}

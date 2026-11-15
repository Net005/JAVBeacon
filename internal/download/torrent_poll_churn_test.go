package download

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/Net005/JAVBeacon/internal/domain"
	"github.com/Net005/JAVBeacon/internal/store"
)

type torrentPollCountingStore struct {
	store.Store
	settings                      map[string]string
	rows                          []domain.Download
	settingsCalls, progressWrites int
	states                        []domain.Download
}

func (st *torrentPollCountingStore) Settings(context.Context) (map[string]string, error) {
	st.settingsCalls++
	return st.settings, nil
}
func (st *torrentPollCountingStore) Downloads(_ context.Context, status string) ([]domain.Download, error) {
	var rows []domain.Download
	for _, d := range st.rows {
		if d.Status == status {
			rows = append(rows, d)
		}
	}
	return rows, nil
}
func (st *torrentPollCountingStore) UpdateTorrentProgress(context.Context, domain.Download) error {
	st.progressWrites++
	return nil
}
func (st *torrentPollCountingStore) SaveTorrentState(_ context.Context, d domain.Download) error {
	st.states = append(st.states, d)
	return nil
}

func torrentPollBatch(t *testing.T) (*Service, *torrentPollCountingStore) {
	t.Helper()
	server, fake := newFakeQBittorrentServer(t)
	st := &torrentPollCountingStore{settings: map[string]string{"qb_url": server.URL, "http_fallback_delay": "90m"}}
	for _, hash := range []string{"synthetic-one", "synthetic-two", "synthetic-three"} {
		st.rows = append(st.rows, domain.Download{ID: int64(len(st.rows) + 1), TorrentHash: hash, Status: "downloading", Files: []byte(`[]`), AddedAt: time.Now()})
		fake.torrents = append(fake.torrents, Torrent{Hash: hash, State: "downloading", Progress: .4})
	}
	return &Service{store: st, log: slog.New(slog.NewTextHandler(io.Discard, nil))}, st
}

func TestTorrentPollLoadsFallbackSettingsOncePerBatch(t *testing.T) {
	service, st := torrentPollBatch(t)
	service.pollTorrents(context.Background())
	if st.settingsCalls != 1 {
		t.Fatalf("batch loaded Settings %d times, want 1", st.settingsCalls)
	}
}

func TestSlowTorrentPollLeavesProgressPersistenceToFastPoll(t *testing.T) {
	service, st := torrentPollBatch(t)
	service.pollTorrents(context.Background())
	if st.progressWrites != 0 {
		t.Fatalf("slow maintenance duplicated %d progress writes", st.progressWrites)
	}
	if len(st.states) != 3 {
		t.Fatalf("maintenance skipped state handling: %d", len(st.states))
	}
	service.pollTorrentProgress(context.Background())
	if st.progressWrites != 3 {
		t.Fatalf("independent fast poll wrote %d telemetry snapshots, want 3", st.progressWrites)
	}
}

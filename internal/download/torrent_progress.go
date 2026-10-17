package download

import (
	"context"
	"time"

	"github.com/Net005/JAVBeacon/internal/domain"
)

func applyTorrentProgress(d *domain.Download, t Torrent) {
	d.Name, d.SeedRatio, d.Progress = t.Name, t.Ratio, t.Progress
	d.Seeds, d.Peers, d.ETASeconds, d.SeenComplete = t.Seeds, t.Peers, t.ETA, t.SeenComplete
}

func (s *Service) rememberTorrentSnapshot(torrents []Torrent) {
	s.qbSnapshotMu.Lock()
	s.qbSnapshot = torrents
	s.qbSnapshotMu.Unlock()
}

func (s *Service) latestTorrentSnapshot(fallback []Torrent) []Torrent {
	s.qbSnapshotMu.RLock()
	defer s.qbSnapshotMu.RUnlock()
	if s.qbSnapshot != nil {
		return s.qbSnapshot
	}
	return fallback
}

// Keep the cheap status poll independent of slow fallback searches and complete
// packet-table validation. The existing worker still serializes those actions.
func (s *Service) scheduleTorrentProgress(ctx context.Context) {
	for {
		s.pollTorrentProgress(ctx)
		timer := time.NewTimer(s.qbPollInterval(ctx))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (s *Service) pollTorrentProgress(ctx context.Context) {
	settings, err := s.store.Settings(ctx)
	if err != nil || settings["qb_url"] == "" {
		return
	}
	qb := NewQB(settings["qb_url"], settings["qb_username"], settings["qb_password"])
	qb.Client.Timeout = 30 * time.Second
	torrents, err := qb.Torrents(ctx)
	if err != nil {
		s.logQBPollFailure(err)
		return
	}
	s.rememberTorrentSnapshot(torrents)
	byHash := make(map[string]Torrent, len(torrents))
	for _, t := range torrents {
		byHash[t.Hash] = t
	}
	for _, status := range []string{"downloading", "completed"} {
		rows, err := s.store.Downloads(ctx, status)
		if err != nil {
			return
		}
		for _, d := range rows {
			if d.Transport == "http" || d.TorrentHash == "" || downloadGoneFromQBHandled(d.PostStatus) {
				continue
			}
			if t, ok := byHash[d.TorrentHash]; ok {
				applyTorrentProgress(&d, t)
				if err := s.store.UpdateTorrentProgress(ctx, d); err != nil {
					s.log.Warn("could not save qBittorrent progress", "download_id", d.ID, "error", err)
				}
			}
		}
	}
}

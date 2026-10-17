package store

import (
	"context"
	"time"

	"github.com/Net005/JAVBeacon/internal/domain"
)

// UpdateTorrentProgress cannot resurrect removed downloads or overwrite work
// being done by validation, fallback, or post-processing.
func (s *SQLite) UpdateTorrentProgress(ctx context.Context, d domain.Download) error {
	_, err := s.db.ExecContext(ctx, `UPDATE downloads SET name=?,seed_ratio=?,progress=?,seeds=?,peers=?,eta_seconds=?,seen_complete=?,updated_at=? WHERE id=? AND torrent_hash=? AND transport<>'http' AND status IN ('downloading','completed')`, d.Name, d.SeedRatio, d.Progress, d.Seeds, d.Peers, d.ETASeconds, d.SeenComplete, time.Now().UTC(), d.ID, d.TorrentHash)
	return err
}

// SaveTorrentState leaves transfer telemetry to the independent progress poll.
// A slow operation must not write its old progress over a newer snapshot.
func (s *SQLite) SaveTorrentState(ctx context.Context, d domain.Download) error {
	if len(d.Files) == 0 {
		d.Files = []byte(`[]`)
	}
	_, err := s.db.ExecContext(ctx, `UPDATE downloads SET files=?,status=?,match_reason=?,qb_response=?,post_status=?,error=?,updated_at=? WHERE id=? AND torrent_hash=? AND transport<>'http' AND status IN ('downloading','completed')`, string(d.Files), d.Status, d.MatchReason, d.QBResponse, d.PostStatus, d.Error, time.Now().UTC(), d.ID, d.TorrentHash)
	return err
}

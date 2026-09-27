package store

import (
	"context"
	"database/sql"

	"github.com/Net005/JAVBeacon/internal/domain"
)

// SiloPlaybackSession/SaveSiloPlaybackSession are Silo's own independent
// twin of JellyfinPlaybackSession/SaveJellyfinPlaybackSession in jellyfin.go
// - same shape, same query pattern, backed by the separate
// silo_playback_sessions table (see domain.SiloPlaybackSession's doc comment
// for why this isn't just reused).
func (s *SQLite) SiloPlaybackSession(ctx context.Context, sessionID string) (domain.SiloPlaybackSession, error) {
	var x domain.SiloPlaybackSession
	var paused, counted int
	var releaseID sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT session_id,release_id,stash_scene_id,silo_item_id,silo_user_id,started_at,last_event_at,last_position_seconds,runtime_seconds,accumulated_seconds,forwarded_seconds,was_paused,play_counted,status,updated_at FROM silo_playback_sessions WHERE session_id=?`, sessionID).Scan(
		&x.SessionID, &releaseID, &x.StashSceneID, &x.SiloItemID, &x.SiloUserID, &x.StartedAt, &x.LastEventAt, &x.LastPosition, &x.RuntimeSeconds, &x.Accumulated, &x.Forwarded, &paused, &counted, &x.Status, &x.UpdatedAt)
	x.ReleaseID = releaseID.Int64
	x.WasPaused, x.PlayCounted = paused != 0, counted != 0
	return x, err
}

func (s *SQLite) SaveSiloPlaybackSession(ctx context.Context, x domain.SiloPlaybackSession) error {
	// release_id is stored as NULL, never 0, for a Stash-only session with no
	// JAVBeacon release row - 0 is not a valid releases.id and the column's FK
	// constraint would reject it outright.
	var releaseID sql.NullInt64
	if x.ReleaseID > 0 {
		releaseID = sql.NullInt64{Int64: x.ReleaseID, Valid: true}
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO silo_playback_sessions(session_id,release_id,stash_scene_id,silo_item_id,silo_user_id,started_at,last_event_at,last_position_seconds,runtime_seconds,accumulated_seconds,forwarded_seconds,was_paused,play_counted,status,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(session_id) DO UPDATE SET release_id=excluded.release_id,stash_scene_id=excluded.stash_scene_id,silo_item_id=excluded.silo_item_id,silo_user_id=excluded.silo_user_id,last_event_at=excluded.last_event_at,last_position_seconds=excluded.last_position_seconds,runtime_seconds=excluded.runtime_seconds,accumulated_seconds=excluded.accumulated_seconds,forwarded_seconds=excluded.forwarded_seconds,was_paused=excluded.was_paused,play_counted=excluded.play_counted,status=excluded.status,updated_at=excluded.updated_at`,
		x.SessionID, releaseID, x.StashSceneID, x.SiloItemID, x.SiloUserID, x.StartedAt, x.LastEventAt, x.LastPosition, x.RuntimeSeconds, x.Accumulated, x.Forwarded, x.WasPaused, x.PlayCounted, x.Status, x.UpdatedAt)
	return err
}

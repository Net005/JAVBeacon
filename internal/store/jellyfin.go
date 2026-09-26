package store

import (
	"context"
	"database/sql"
	"strings"

	"github.com/Net005/JAVBeacon/internal/domain"
)

// JellyfinReleaseByPath performs the authoritative exact-path lookup. The
// higher-level integration adds release-code fallback only when no stored
// Stash path matches.
func (s *SQLite) JellyfinReleaseByPath(ctx context.Context, path string) (domain.Release, error) {
	return scanRelease(s.db.QueryRowContext(ctx, releaseSelect(s.dialect)+` WHERE LOWER(r.stash_file_path)=LOWER(?) LIMIT 1`, strings.TrimSpace(path)))
}

func (s *SQLite) JellyfinPlaybackSession(ctx context.Context, sessionID string) (domain.JellyfinPlaybackSession, error) {
	var x domain.JellyfinPlaybackSession
	var paused, counted int
	var releaseID sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT session_id,release_id,stash_scene_id,jellyfin_item_id,jellyfin_user_id,started_at,last_event_at,last_position_seconds,runtime_seconds,accumulated_seconds,forwarded_seconds,was_paused,play_counted,status,updated_at FROM jellyfin_playback_sessions WHERE session_id=?`, sessionID).Scan(
		&x.SessionID, &releaseID, &x.StashSceneID, &x.JellyfinItemID, &x.JellyfinUserID, &x.StartedAt, &x.LastEventAt, &x.LastPosition, &x.RuntimeSeconds, &x.Accumulated, &x.Forwarded, &paused, &counted, &x.Status, &x.UpdatedAt)
	x.ReleaseID = releaseID.Int64
	x.WasPaused, x.PlayCounted = paused != 0, counted != 0
	return x, err
}

func (s *SQLite) SaveJellyfinPlaybackSession(ctx context.Context, x domain.JellyfinPlaybackSession) error {
	// release_id is stored as NULL, never 0, for a Stash-only session with no
	// JAVBeacon release row - 0 is not a valid releases.id and the column's FK
	// constraint would reject it outright.
	var releaseID sql.NullInt64
	if x.ReleaseID > 0 {
		releaseID = sql.NullInt64{Int64: x.ReleaseID, Valid: true}
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO jellyfin_playback_sessions(session_id,release_id,stash_scene_id,jellyfin_item_id,jellyfin_user_id,started_at,last_event_at,last_position_seconds,runtime_seconds,accumulated_seconds,forwarded_seconds,was_paused,play_counted,status,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(session_id) DO UPDATE SET release_id=excluded.release_id,stash_scene_id=excluded.stash_scene_id,jellyfin_item_id=excluded.jellyfin_item_id,jellyfin_user_id=excluded.jellyfin_user_id,last_event_at=excluded.last_event_at,last_position_seconds=excluded.last_position_seconds,runtime_seconds=excluded.runtime_seconds,accumulated_seconds=excluded.accumulated_seconds,forwarded_seconds=excluded.forwarded_seconds,was_paused=excluded.was_paused,play_counted=excluded.play_counted,status=excluded.status,updated_at=excluded.updated_at`,
		x.SessionID, releaseID, x.StashSceneID, x.JellyfinItemID, x.JellyfinUserID, x.StartedAt, x.LastEventAt, x.LastPosition, x.RuntimeSeconds, x.Accumulated, x.Forwarded, x.WasPaused, x.PlayCounted, x.Status, x.UpdatedAt)
	return err
}

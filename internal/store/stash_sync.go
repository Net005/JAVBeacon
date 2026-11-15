package store

import (
	"context"
	"database/sql"

	"github.com/Net005/JAVBeacon/internal/domain"
)

// StashSyncSnapshot bounds a full sync by the highest currently matching ID.
// It is a high-water mark, not a long-running transaction: later inserts are
// deferred to the next sync, while updates/deletions of existing rows remain
// visible. Use the same filter for every page.
func (s *SQLite) StashSyncSnapshot(ctx context.Context, filter domain.ReleaseFilter) (int64, int, error) {
	where, args := releaseFilterWhere(s.dialect, filter)
	var high int64
	var total int
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(r.id),0),COUNT(*)`+releaseFrom+where, args...).Scan(&high, &total)
	return high, total, err
}

// StashSyncReleases reads only matching/playback/history fields. Primary-key
// keysets avoid OFFSET drift and date sorting; no UI metadata or download
// aggregates are hydrated. SQLite's store also backs PostgreSQL via its dialect
// and query-rewriting connection, just like Releases.
func (s *SQLite) StashSyncReleases(ctx context.Context, filter domain.ReleaseFilter, after, through int64, limit int) ([]domain.Release, error) {
	if limit <= 0 || limit > 500 {
		limit = 500
	}
	where, args := releaseFilterWhere(s.dialect, filter)
	query := `SELECT r.id,r.video_id,r.title,r.product_url,r.is_local,r.watchlist,r.stash_scene_id,r.stash_file_path,r.stash_created_at,r.stash_release_date,r.release_date,r.o_counter,r.play_count,r.last_played_at,r.last_o_count_at` + releaseFrom + where + ` AND r.id>? AND r.id<=? ORDER BY r.id ASC LIMIT ?`
	args = append(args, after, through, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]domain.Release, 0)
	for rows.Next() {
		var r domain.Release
		var created sql.NullTime
		if err := rows.Scan(&r.ID, &r.VideoID, &r.Title, &r.ProductURL, &r.Local, &r.Watchlist, &r.StashSceneID, &r.StashFilePath, &created, &r.StashReleaseDate, &r.ReleaseDate, &r.OCounter, &r.PlayCount, &r.LastPlayedAt, &r.LastOCountAt); err != nil {
			return nil, err
		}
		if created.Valid {
			r.StashCreatedAt = created.Time
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

package store

import (
	"context"

	"github.com/Net005/JAVBeacon/internal/domain"
)

// StashReleaseIdentities supplies canonical scene matching without hydrating
// catalog metadata or running per-release correlated aggregate queries.
func (s *SQLite) StashReleaseIdentities(ctx context.Context) ([]domain.ReleaseIdentity, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT id,video_id FROM releases ORDER BY release_date DESC NULLS LAST,id DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.ReleaseIdentity
	for rows.Next() {
		var r domain.ReleaseIdentity
		if err := rows.Scan(&r.ID, &r.VideoID); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

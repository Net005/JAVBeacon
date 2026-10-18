package store

import (
	"context"
	"database/sql"
	"github.com/Net005/JAVBeacon/internal/domain"
	"strings"
	"time"
)

// A missing requested file is a release/source exclusion, not a global ban on
// every release that might legitimately exist in a shared folder.
func (s *SQLite) SaveSourceRejection(ctx context.Context, videoID, raw, reason string, expires time.Time) error {
	refs, err := s.ReleaseSourceAliases(ctx, raw)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, ref := range refs {
		key, err := domain.BlacklistURLKey(ref)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO release_source_rejections(video_id,url_key,reason,expires_at) VALUES(?,?,?,?) ON CONFLICT(video_id,url_key) DO UPDATE SET reason=excluded.reason,expires_at=excluded.expires_at`, strings.ToUpper(strings.TrimSpace(videoID)), key, reason, expires.UTC())
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}
func (s *SQLite) SourceRejection(ctx context.Context, videoID, raw string) (string, error) {
	refs, err := s.ReleaseSourceAliases(ctx, raw)
	if err != nil {
		return "", err
	}
	for _, ref := range refs {
		key, err := domain.BlacklistURLKey(ref)
		if err != nil {
			continue
		}
		var reason string
		err = s.db.QueryRowContext(ctx, `SELECT reason FROM release_source_rejections WHERE video_id=? AND url_key=? AND expires_at>?`, strings.ToUpper(strings.TrimSpace(videoID)), key, time.Now().UTC()).Scan(&reason)
		if err == sql.ErrNoRows {
			continue
		}
		if err != nil {
			return "", err
		}
		return reason, nil
	}
	return "", nil
}

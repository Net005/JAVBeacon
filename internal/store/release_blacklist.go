package store

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/Net005/JAVBeacon/internal/domain"
)

const blacklistColumns = `url,release_id,video_id,title,transport,reason,size_bytes,duration_seconds,created_at`

func (s *SQLite) SaveBlacklistedRelease(ctx context.Context, entry domain.BlacklistedRelease) error {
	if _, err := domain.BlacklistURLKey(entry.URL); err != nil {
		return err
	}
	aliases, err := s.ReleaseSourceAliases(ctx, entry.URL)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, raw := range aliases {
		key, err := domain.BlacklistURLKey(raw)
		if err != nil {
			return err
		}
		// Preserve the first measured rejection, including its original timestamp.
		_, err = tx.ExecContext(ctx, `INSERT INTO blacklisted_releases (url_key,`+blacklistColumns+`) VALUES (?,?,?,?,?,?,?,?,?,?) ON CONFLICT(url_key) DO NOTHING`, key, strings.TrimSpace(raw), entry.ReleaseID, entry.VideoID, entry.Title, entry.Transport, entry.Reason, entry.SizeBytes, entry.DurationSeconds, time.Now().UTC())
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *SQLite) RememberReleaseSourceAliases(ctx context.Context, raw string, aliases []string) error {
	if len(aliases) == 0 {
		return nil
	}
	all := append([]string{raw}, aliases...)
	urls := map[string]string{}
	for _, value := range all {
		if key, err := domain.BlacklistURLKey(value); err == nil {
			urls[key] = value
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for source := range urls {
		for alias, value := range urls {
			if source == alias {
				continue
			}
			if _, err = tx.ExecContext(ctx, `INSERT INTO release_source_aliases (source_key,alias_key,alias_url) VALUES (?,?,?) ON CONFLICT(source_key,alias_key) DO NOTHING`, source, alias, value); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

func (s *SQLite) ReleaseSourceAliases(ctx context.Context, raw string) ([]string, error) {
	key, err := domain.BlacklistURLKey(raw)
	if err != nil {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx, `WITH RECURSIVE linked(k) AS (SELECT CAST(? AS TEXT) UNION SELECT a.alias_key FROM release_source_aliases a JOIN linked ON a.source_key=linked.k) SELECT DISTINCT alias_url FROM release_source_aliases WHERE alias_key IN (SELECT k FROM linked)`, key)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{raw}
	seen := map[string]bool{key: true}
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		k, _ := domain.BlacklistURLKey(value)
		if !seen[k] {
			out = append(out, value)
			seen[k] = true
		}
	}
	return out, rows.Err()
}

func scanBlacklistedRelease(row interface{ Scan(...any) error }) (domain.BlacklistedRelease, error) {
	var e domain.BlacklistedRelease
	err := row.Scan(&e.URL, &e.ReleaseID, &e.VideoID, &e.Title, &e.Transport, &e.Reason, &e.SizeBytes, &e.DurationSeconds, &e.CreatedAt)
	return e, err
}

func (s *SQLite) BlacklistedRelease(ctx context.Context, raw string) (domain.BlacklistedRelease, bool, error) {
	key, err := domain.BlacklistURLKey(raw)
	// Search providers may have empty/non-download references for unavailable rows.
	if err != nil {
		return domain.BlacklistedRelease{}, false, nil
	}
	e, err := scanBlacklistedRelease(s.db.QueryRowContext(ctx, `SELECT `+blacklistColumns+` FROM blacklisted_releases WHERE url_key=?`, key))
	if err == sql.ErrNoRows {
		return e, false, nil
	}
	return e, err == nil, err
}

func (s *SQLite) BlacklistedReleases(ctx context.Context, search string, limit, offset int) ([]domain.BlacklistedRelease, int, error) {
	if limit <= 0 || limit > 250 {
		limit = 25
	}
	if offset < 0 {
		offset = 0
	}
	where := ` WHERE LOWER(url) LIKE ? OR LOWER(video_id) LIKE ? OR LOWER(title) LIKE ? OR LOWER(reason) LIKE ?`
	q := "%" + strings.ToLower(strings.TrimSpace(search)) + "%"
	var total int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM blacklisted_releases`+where, q, q, q, q).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+blacklistColumns+` FROM blacklisted_releases`+where+` ORDER BY created_at DESC,url_key LIMIT ? OFFSET ?`, q, q, q, q, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	entries := []domain.BlacklistedRelease{}
	for rows.Next() {
		e, err := scanBlacklistedRelease(rows)
		if err != nil {
			return nil, 0, err
		}
		entries = append(entries, e)
	}
	return entries, total, rows.Err()
}

func (s *SQLite) DeleteBlacklistedRelease(ctx context.Context, raw string) error {
	if _, err := domain.BlacklistURLKey(raw); err != nil {
		return err
	}
	aliases, err := s.ReleaseSourceAliases(ctx, raw)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, alias := range aliases {
		key, _ := domain.BlacklistURLKey(alias)
		if _, err = tx.ExecContext(ctx, `DELETE FROM blacklisted_releases WHERE url_key=?`, key); err != nil {
			return err
		}
	}
	return tx.Commit()
}

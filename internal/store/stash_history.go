package store

import (
	"context"
	"database/sql"
	"sort"
	"strings"
	"time"

	"github.com/Net005/JAVBeacon/internal/domain"
)

// UpsertStashHistory replaces the observed activity set for one scene while
// preserving records for scenes that no longer exist in StashApp.
func (s *SQLite) UpsertStashHistory(ctx context.Context, scene domain.StashHistoryScene, plays, orgasms []time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := scene.ObservedAt.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	// A rebuilt Stash database may assign a new scene ID to the same linked
	// JAVBeacon release. Consolidate that identity instead of double-counting
	// the old archive and the newly observed scene.
	var replacedSceneIDs []string
	var replacedPlayTotal, replacedPlayMax float64
	if scene.ReleaseID > 0 {
		rows, queryErr := tx.QueryContext(ctx, `SELECT stash_scene_id,total_play_seconds FROM stash_history_scenes WHERE release_id=? AND stash_scene_id<>?`, scene.ReleaseID, scene.StashSceneID)
		if queryErr != nil {
			return queryErr
		}
		for rows.Next() {
			var oldID string
			var oldTotal float64
			if queryErr = rows.Scan(&oldID, &oldTotal); queryErr != nil {
				rows.Close()
				return queryErr
			}
			replacedSceneIDs = append(replacedSceneIDs, oldID)
			replacedPlayTotal += oldTotal
			if oldTotal > replacedPlayMax {
				replacedPlayMax = oldTotal
			}
		}
		if queryErr = rows.Err(); queryErr != nil {
			rows.Close()
			return queryErr
		}
		rows.Close()
		overlaps := false
		for _, oldID := range replacedSceneIDs {
			for _, play := range plays {
				var found int
				scanErr := tx.QueryRowContext(ctx, `SELECT 1 FROM stash_history_events WHERE stash_scene_id=? AND event_type='play' AND occurred_at=? LIMIT 1`, oldID, play.UTC()).Scan(&found)
				if scanErr == nil {
					overlaps = true
					break
				}
				if scanErr != nil && scanErr != sql.ErrNoRows {
					return scanErr
				}
			}
			if overlaps {
				break
			}
		}
		if overlaps {
			if replacedPlayMax > scene.TotalPlaySeconds {
				scene.TotalPlaySeconds = replacedPlayMax
			}
		} else {
			scene.TotalPlaySeconds += replacedPlayTotal
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO stash_history_scenes(stash_scene_id,release_id,video_id,title,javlibrary_url,file_path,total_play_seconds,play_count,orgasm_count,observed_at)
		VALUES(?,?,?,?,?,?,?,?,?,?) ON CONFLICT(stash_scene_id) DO UPDATE SET release_id=excluded.release_id,video_id=excluded.video_id,title=excluded.title,javlibrary_url=excluded.javlibrary_url,file_path=excluded.file_path,total_play_seconds=excluded.total_play_seconds,play_count=excluded.play_count,orgasm_count=excluded.orgasm_count,observed_at=excluded.observed_at`,
		scene.StashSceneID, scene.ReleaseID, scene.VideoID, scene.Title, scene.JavLibraryURL, scene.FilePath, scene.TotalPlaySeconds, len(plays), len(orgasms), now)
	if err != nil {
		return err
	}
	for _, oldID := range replacedSceneIDs {
		_, err = tx.ExecContext(ctx, `INSERT INTO stash_history_events(stash_scene_id,event_type,occurred_at,duration_seconds,duration_estimated) SELECT ?,event_type,occurred_at,duration_seconds,duration_estimated FROM stash_history_events WHERE stash_scene_id=? ON CONFLICT(stash_scene_id,event_type,occurred_at) DO NOTHING`, scene.StashSceneID, oldID)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM stash_history_scenes WHERE stash_scene_id=?`, oldID); err != nil {
			return err
		}
	}
	// Stash exposes only a scene-wide play duration. Spread it across exact
	// play events so date buckets remain useful, and mark the values estimated.
	perPlay := float64(0)
	if len(plays) > 0 {
		perPlay = scene.TotalPlaySeconds / float64(len(plays))
	}
	for _, event := range []struct {
		kind   string
		values []time.Time
	}{{"play", plays}, {"orgasm", orgasms}} {
		for _, at := range event.values {
			duration, estimated := float64(0), 0
			if event.kind == "play" {
				duration, estimated = perPlay, 1
			}
			_, err = tx.ExecContext(ctx, `INSERT INTO stash_history_events(stash_scene_id,event_type,occurred_at,duration_seconds,duration_estimated) VALUES(?,?,?,?,?) ON CONFLICT(stash_scene_id,event_type,occurred_at) DO UPDATE SET duration_seconds=excluded.duration_seconds,duration_estimated=excluded.duration_estimated`, scene.StashSceneID, event.kind, at.UTC(), duration, estimated)
			if err != nil {
				return err
			}
		}
	}
	var storedPlayCount int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM stash_history_events WHERE stash_scene_id=? AND event_type='play'`, scene.StashSceneID).Scan(&storedPlayCount); err != nil {
		return err
	}
	if storedPlayCount > 0 {
		if _, err = tx.ExecContext(ctx, `UPDATE stash_history_events SET duration_seconds=?,duration_estimated=1 WHERE stash_scene_id=? AND event_type='play'`, scene.TotalPlaySeconds/float64(storedPlayCount), scene.StashSceneID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *SQLite) StashHistory(ctx context.Context, kind string, from, to time.Time) ([]domain.StashHistoryItem, error) {
	where, args := []string{"1=1"}, []any{}
	if !from.IsZero() {
		where = append(where, "e.occurred_at>=?")
		args = append(args, from.UTC())
	}
	if !to.IsZero() {
		where = append(where, "e.occurred_at<?")
		args = append(args, to.UTC())
	}
	rows, err := s.db.QueryContext(ctx, `SELECT e.stash_scene_id,e.event_type,e.occurred_at,e.duration_seconds,e.duration_estimated,s.release_id,s.video_id,s.title,s.javlibrary_url,s.file_path FROM stash_history_events e JOIN stash_history_scenes s ON s.stash_scene_id=e.stash_scene_id WHERE `+strings.Join(where, " AND ")+` ORDER BY e.occurred_at DESC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type key struct{ date, scene string }
	items := map[key]*domain.StashHistoryItem{}
	for rows.Next() {
		var sceneID, eventType, videoID, title, javURL, path string
		var at time.Time
		var seconds float64
		var estimated int
		var releaseID int64
		if err := rows.Scan(&sceneID, &eventType, &at, &seconds, &estimated, &releaseID, &videoID, &title, &javURL, &path); err != nil {
			return nil, err
		}
		date := at.Local().Format("2006-01-02")
		k := key{date, sceneID}
		item := items[k]
		if item == nil {
			item = &domain.StashHistoryItem{Date: date, StashSceneID: sceneID, ReleaseID: releaseID, VideoID: videoID, Title: title, JavLibraryURL: javURL, FilePath: path, LatestEventAt: at}
			items[k] = item
		}
		if at.After(item.LatestEventAt) {
			item.LatestEventAt = at
		}
		if eventType == "play" {
			item.PlayCount++
			item.PlaySeconds += seconds
			item.DurationEstimated = item.DurationEstimated || estimated != 0
			if at.After(item.LatestPlayAt) {
				item.LatestPlayAt = at
			}
		} else {
			item.OrgasmCount++
			if at.After(item.LatestOrgasmAt) {
				item.LatestOrgasmAt = at
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]domain.StashHistoryItem, 0, len(items))
	for _, item := range items {
		out = append(out, *item)
	}
	if kind == "play" || kind == "orgasm" {
		filtered := out[:0]
		for _, item := range out {
			if (kind == "play" && item.PlayCount > 0) || (kind == "orgasm" && item.OrgasmCount > 0) {
				filtered = append(filtered, item)
			}
		}
		out = filtered
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].LatestEventAt.After(out[j].LatestEventAt) })
	return out, nil
}

func (s *SQLite) StashHistoryExport(ctx context.Context) (domain.StashHistoryExport, error) {
	out := domain.StashHistoryExport{Format: "javbeacon-stash-history", Version: 1, ExportedAt: time.Now().UTC()}
	rows, err := s.db.QueryContext(ctx, `SELECT stash_scene_id,release_id,video_id,title,javlibrary_url,file_path,total_play_seconds,play_count,orgasm_count,observed_at FROM stash_history_scenes ORDER BY stash_scene_id`)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var x domain.StashHistoryScene
		if err = rows.Scan(&x.StashSceneID, &x.ReleaseID, &x.VideoID, &x.Title, &x.JavLibraryURL, &x.FilePath, &x.TotalPlaySeconds, &x.PlayCount, &x.OrgasmCount, &x.ObservedAt); err != nil {
			rows.Close()
			return out, err
		}
		out.Scenes = append(out.Scenes, x)
	}
	rows.Close()
	if err != nil {
		return out, err
	}
	rows, err = s.db.QueryContext(ctx, `SELECT id,stash_scene_id,event_type,occurred_at,duration_seconds,duration_estimated FROM stash_history_events ORDER BY occurred_at`)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var x domain.StashHistoryEvent
		var estimated int
		if err = rows.Scan(&x.ID, &x.StashSceneID, &x.Type, &x.OccurredAt, &x.DurationSeconds, &estimated); err != nil {
			return out, err
		}
		x.Estimated = estimated != 0
		out.Events = append(out.Events, x)
	}
	return out, rows.Err()
}

func (s *SQLite) StashHistoryScenes(ctx context.Context) ([]domain.StashHistoryScene, error) {
	x, err := s.StashHistoryExport(ctx)
	return x.Scenes, err
}

// StashHistoryForRelease returns the durable history attached to one release.
// Release Details calls this on every open, so keep it as a pair of indexed
// database queries instead of exporting and scanning the complete archive.
func (s *SQLite) StashHistoryForRelease(ctx context.Context, releaseID int64) ([]domain.StashHistoryScene, []domain.StashHistoryEvent, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT stash_scene_id,release_id,video_id,title,javlibrary_url,file_path,total_play_seconds,play_count,orgasm_count,observed_at FROM stash_history_scenes WHERE release_id=? ORDER BY stash_scene_id`, releaseID)
	if err != nil {
		return nil, nil, err
	}
	var scenes []domain.StashHistoryScene
	for rows.Next() {
		var x domain.StashHistoryScene
		if err = rows.Scan(&x.StashSceneID, &x.ReleaseID, &x.VideoID, &x.Title, &x.JavLibraryURL, &x.FilePath, &x.TotalPlaySeconds, &x.PlayCount, &x.OrgasmCount, &x.ObservedAt); err != nil {
			rows.Close()
			return nil, nil, err
		}
		scenes = append(scenes, x)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return nil, nil, err
	}
	rows.Close()

	rows, err = s.db.QueryContext(ctx, `SELECT e.id,e.stash_scene_id,e.event_type,e.occurred_at,e.duration_seconds,e.duration_estimated FROM stash_history_events e JOIN stash_history_scenes s ON s.stash_scene_id=e.stash_scene_id WHERE s.release_id=? ORDER BY e.occurred_at DESC`, releaseID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var events []domain.StashHistoryEvent
	for rows.Next() {
		var x domain.StashHistoryEvent
		var estimated int
		if err = rows.Scan(&x.ID, &x.StashSceneID, &x.Type, &x.OccurredAt, &x.DurationSeconds, &estimated); err != nil {
			return nil, nil, err
		}
		x.Estimated = estimated != 0
		events = append(events, x)
	}
	return scenes, events, rows.Err()
}

func (s *SQLite) StashHistoryEventsForScene(ctx context.Context, sceneID string) ([]domain.StashHistoryEvent, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,stash_scene_id,event_type,occurred_at,duration_seconds,duration_estimated FROM stash_history_events WHERE stash_scene_id=? ORDER BY occurred_at`, sceneID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.StashHistoryEvent
	for rows.Next() {
		var x domain.StashHistoryEvent
		var estimated int
		if err = rows.Scan(&x.ID, &x.StashSceneID, &x.Type, &x.OccurredAt, &x.DurationSeconds, &estimated); err != nil {
			return nil, err
		}
		x.Estimated = estimated != 0
		out = append(out, x)
	}
	return out, rows.Err()
}

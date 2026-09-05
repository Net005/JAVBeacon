package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/Net005/JAVBeacon/internal/domain"
)

// stashMissingEffectiveStatusExpr computes the single "what should the UI
// show for this row" status live at read time, folding the recovery
// workflow's own status column (missing/retrieving/retrieve_failed, only
// meaningful before a release is linked) together with the linked release's
// live download state (TODO-2.0 Phase 2: "show releases still missing /
// downloading / failed"). It assumes the query it is embedded in joins
// "stash_missing_scenes m LEFT JOIN releases r ON r.id=m.release_id" - the
// m.release_id=0 branch is evaluated first specifically so the later
// branches (which reference r.*, NULL through the LEFT JOIN when
// release_id=0) are never reached for an unlinked row; both SQLite and
// PostgreSQL guarantee CASE WHEN branches short-circuit in order.
//
// Deliberately NOT using r.is_local here (TODO-2.0 Task A bug fix): every
// row in stash_missing_scenes exists BECAUSE the most recent scan could not
// find that scene's file(s) on disk - that is the one thing this table is
// certain of. r.is_local is set by the separate "Stash local sync" feature,
// which only checks whether a matching scene still exists in StashApp's own
// database, not whether its underlying file is still on disk - so it can
// (and, per a user report, did) still read true for a release whose file
// had since been lost, showing "Downloaded" for a row that this exact scan
// just proved was missing. "downloaded" here is only earned by a completed
// download this app itself ran through the recovery flow, which is actual
// proof the file exists again; once that happens (or the file reappears by
// any other means) the next scan simply stops finding the scene missing at
// all and prunes its row, which is the correct way for this table to learn
// "it's back."
const stashMissingEffectiveStatusExpr = `(CASE
	WHEN m.release_id=0 THEN (CASE WHEN m.status IN ('retrieving','retrieve_failed') THEN m.status ELSE 'missing' END)
	WHEN EXISTS (SELECT 1 FROM downloads d WHERE d.release_id=m.release_id AND d.status='downloading') THEN 'downloading'
	WHEN EXISTS (SELECT 1 FROM downloads d WHERE d.release_id=m.release_id AND d.status='completed') THEN 'downloaded'
	WHEN EXISTS (SELECT 1 FROM downloads d WHERE d.release_id=m.release_id AND d.status='failed') THEN 'failed'
	WHEN r.monitor_download=1 THEN 'monitored'
	ELSE 'retrieved'
END)`

const stashMissingSelect = `SELECT m.id,m.stash_scene_id,m.title,m.code,m.date,m.path,m.paths,m.o_counter,m.play_count,m.last_played_at,m.last_o_count_at,m.studio,m.tags,m.urls,m.javlibrary_url,m.release_id,COALESCE(r.video_id,''),COALESCE(r.title,''),COALESCE(r.monitor_download,0),m.status,m.message,` + stashMissingEffectiveStatusExpr + `,m.first_seen_at,m.last_scan_at,m.updated_at FROM stash_missing_scenes m LEFT JOIN releases r ON r.id=m.release_id`

func scanStashMissingScene(scanner interface{ Scan(...any) error }) (domain.StashMissingScene, error) {
	var x domain.StashMissingScene
	var paths, tags, urls string
	err := scanner.Scan(&x.ID, &x.StashSceneID, &x.Title, &x.Code, &x.Date, &x.Path, &paths, &x.OCounter, &x.PlayCount, &x.LastPlayedAt, &x.LastOCountAt, &x.Studio, &tags, &urls, &x.JavLibraryURL, &x.ReleaseID, &x.ReleaseVideoID, &x.ReleaseTitle, &x.ReleaseMonitorDownload, &x.Status, &x.Message, &x.EffectiveStatus, &x.FirstSeenAt, &x.LastScanAt, &x.UpdatedAt)
	if err == nil {
		_ = json.Unmarshal([]byte(paths), &x.Paths)
		_ = json.Unmarshal([]byte(tags), &x.Tags)
		_ = json.Unmarshal([]byte(urls), &x.URLs)
	}
	return x, err
}

// numericConditionOp maps a StashMissingFilter condition's "op" field to a
// SQL comparison operator, defaulting to ">=" (matching how the frontend's
// numeric condition rows are labeled "at least") when op is empty/unknown.
func numericConditionOp(op string) string {
	switch op {
	case "lte":
		return "<="
	case "eq":
		return "="
	case "gt":
		return ">"
	case "lt":
		return "<"
	default:
		return ">="
	}
}

// stashMissingFilterCondition is one row of the Missing Library Files
// Conditions dialog. Op is an optional numeric/date comparison operator
// ("gte" default, "lte", "eq", "gt", "lt" for o_count/play_count; "before"/
// "after" (default) for last_played/last_o_count) - fields the release Conditions
// builder's Exact/Wildcard toggle has no use for.
type stashMissingFilterCondition struct {
	Field    string `json:"field"`
	Value    string `json:"value"`
	Op       string `json:"op"`
	Exact    bool   `json:"exact"`
	Wildcard bool   `json:"wildcard"`
}

// stashMissingFilterConditionGroup is one AND/OR group of conditions
// (TODO-2.0 Task A "AND/OR condition groups"), mirroring
// releaseFilterConditionGroup: its own Logic combines only its own
// Conditions, independent of the top-level logic that combines groups.
type stashMissingFilterConditionGroup struct {
	Logic      string                        `json:"logic"`
	Conditions []stashMissingFilterCondition `json:"conditions"`
}

// stashMissingSearchExpression is the JSON shape of
// domain.StashMissingFilter.SearchExpression, extended (TODO-2.0 Task A)
// with an optional "groups" list exactly as releaseSearchExpression is - see
// that type's doc comment for the full rationale and the legacy-flat-shape
// backward-compatibility behavior, which applies here identically.
type stashMissingSearchExpression struct {
	Logic      string                             `json:"logic"`
	Conditions []stashMissingFilterCondition      `json:"conditions"`
	Groups     []stashMissingFilterConditionGroup `json:"groups"`
}

// stashMissingConditionGroupClause builds one parenthesized "(... AND/OR
// ...)" clause for a single condition group, joining its own conditions
// with its own logic. Returns "", nil if the group has no matchable
// conditions, so the caller can skip it entirely.
func stashMissingConditionGroupClause(d Dialect, conditions []stashMissingFilterCondition, logic string) (string, []any) {
	innerLogic := " AND "
	if strings.EqualFold(logic, "or") {
		innerLogic = " OR "
	}
	var parts []string
	var a []any
	for _, c := range conditions {
		value := strings.TrimSpace(c.Value)
		switch strings.ToLower(c.Field) {
		case "path":
			if value == "" {
				continue
			}
			if c.Wildcard {
				value = strings.ReplaceAll(value, "*", "%")
			} else {
				value = "%" + value + "%"
			}
			parts = append(parts, `(`+d.CaseInsensitiveLike("m.path")+` OR `+d.CaseInsensitiveLike("m.paths")+`)`)
			a = append(a, value, value)
		case "studio":
			if value == "" {
				continue
			}
			if c.Exact {
				parts = append(parts, `LOWER(m.studio)=LOWER(?)`)
				a = append(a, value)
				continue
			}
			if c.Wildcard {
				value = strings.ReplaceAll(value, "*", "%")
			} else {
				value = "%" + value + "%"
			}
			parts = append(parts, d.CaseInsensitiveLike("m.studio"))
			a = append(a, value)
		case "tag":
			if value == "" {
				continue
			}
			if c.Wildcard {
				value = strings.ReplaceAll(value, "*", "%")
			} else {
				value = "%" + value + "%"
			}
			parts = append(parts, d.CaseInsensitiveLike("m.tags"))
			a = append(a, value)
		case "o_count", "o_counter":
			n, convErr := strconv.Atoi(value)
			if convErr != nil {
				continue
			}
			parts = append(parts, `m.o_counter `+numericConditionOp(c.Op)+` ?`)
			a = append(a, n)
		case "play_count":
			n, convErr := strconv.Atoi(value)
			if convErr != nil {
				continue
			}
			parts = append(parts, `m.play_count `+numericConditionOp(c.Op)+` ?`)
			a = append(a, n)
		case "last_played":
			if value == "" {
				continue
			}
			if strings.EqualFold(c.Op, "before") {
				parts = append(parts, `m.last_played_at<>'' AND m.last_played_at < ?`)
			} else {
				parts = append(parts, `m.last_played_at<>'' AND m.last_played_at > ?`)
			}
			a = append(a, value)
		case "last_o_count":
			if value == "" {
				continue
			}
			if strings.EqualFold(c.Op, "before") {
				parts = append(parts, `m.last_o_count_at<>'' AND m.last_o_count_at < ?`)
			} else {
				parts = append(parts, `m.last_o_count_at<>'' AND m.last_o_count_at > ?`)
			}
			a = append(a, value)
		case "has_javlibrary_url":
			if value == "false" {
				parts = append(parts, `m.javlibrary_url=''`)
			} else {
				parts = append(parts, `m.javlibrary_url<>''`)
			}
		case "has_db_entry":
			if value == "false" {
				parts = append(parts, `m.release_id=0`)
			} else {
				parts = append(parts, `m.release_id<>0`)
			}
		}
	}
	if len(parts) == 0 {
		return "", nil
	}
	return `(` + strings.Join(parts, innerLogic) + `)`, a
}

// stashMissingFilterWhere builds the "WHERE ..." fragment and bind
// arguments shared by StashMissingScenes and StashMissingScenesCount. It
// assumes the same "stash_missing_scenes m LEFT JOIN releases r" shape as
// stashMissingSelect. f.SearchExpression uses the same shape as
// releaseSearchExpression (see that type's doc comment), against this
// filter's own field set - see stashMissingConditionGroupClause.
//
// The path/studio/tag substring and wildcard matches go through
// Dialect.CaseInsensitiveLike (TODO-2.0 Task A's case-insensitivity audit,
// mirroring the same fix in releaseFilterWhere): this function previously
// took no Dialect at all and used a bare "column LIKE ?", which is
// case-insensitive on SQLite only by accident of its default collation and
// silently case-sensitive on PostgreSQL, whose LIKE always is.
func stashMissingFilterWhere(d Dialect, f domain.StashMissingFilter) (string, []any) {
	q := ` WHERE 1=1`
	var a []any
	if f.Status != "" {
		switch f.Status {
		case "scraping":
			q += ` AND ` + stashMissingEffectiveStatusExpr + ` IN ('retrieving','retrieve_failed','retrieved')`
		case "download":
			q += ` AND ` + stashMissingEffectiveStatusExpr + ` IN ('monitored','downloading','downloaded','failed')`
		default:
			q += ` AND ` + stashMissingEffectiveStatusExpr + ` = ?`
			a = append(a, f.Status)
		}
	}
	if f.SearchExpression != "" {
		var expression stashMissingSearchExpression
		if json.Unmarshal([]byte(f.SearchExpression), &expression) == nil {
			groups := expression.Groups
			if len(groups) == 0 && len(expression.Conditions) > 0 {
				// Legacy flat shape: treat as a single implicit group using
				// the top-level logic - see releaseSearchExpression's doc
				// comment for the full rationale.
				groups = []stashMissingFilterConditionGroup{{Logic: expression.Logic, Conditions: expression.Conditions}}
			}
			outerLogic := " AND "
			if strings.EqualFold(expression.Logic, "or") {
				outerLogic = " OR "
			}
			var groupClauses []string
			for _, group := range groups {
				clause, args := stashMissingConditionGroupClause(d, group.Conditions, group.Logic)
				if clause == "" {
					continue
				}
				groupClauses = append(groupClauses, clause)
				a = append(a, args...)
			}
			if len(groupClauses) > 0 {
				q += ` AND (` + strings.Join(groupClauses, outerLogic) + `)`
			}
		}
	}
	return q, a
}

func stashMissingOrderBy(f domain.StashMissingFilter) string {
	order := " ORDER BY "
	switch f.Sort {
	case "title":
		order += "m.title"
	case "o_counter":
		order += "m.o_counter"
	case "play_count":
		order += "m.play_count"
	case "last_played":
		order += "m.last_played_at"
	case "date":
		order += "m.date"
	default:
		order += "m.last_scan_at"
	}
	if strings.EqualFold(f.Direction, "asc") {
		return order + " ASC"
	}
	return order + " DESC"
}

// stashMissingMaxLimit caps a single StashMissingScenes page, including the
// UI's "All" page-size option (TODO-2.0 Task A) which requests this exact
// value to fit every missing scene on one page - generous enough for any
// realistic missing-file backlog while still bounding one query's cost.
const stashMissingMaxLimit = 5000

func (s *SQLite) StashMissingScenes(ctx context.Context, f domain.StashMissingFilter) ([]domain.StashMissingScene, error) {
	where, args := stashMissingFilterWhere(s.dialect, f)
	limit := f.Limit
	if limit <= 0 {
		limit = 100
	}
	if limit > stashMissingMaxLimit {
		limit = stashMissingMaxLimit
	}
	offset := f.Offset
	if offset < 0 {
		offset = 0
	}
	args = append(append([]any{}, args...), limit, offset)
	rows, err := s.db.QueryContext(ctx, stashMissingSelect+where+stashMissingOrderBy(f)+" LIMIT ? OFFSET ?", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.StashMissingScene{}
	for rows.Next() {
		x, err := scanStashMissingScene(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

func (s *SQLite) StashMissingScenesCount(ctx context.Context, f domain.StashMissingFilter) (int, error) {
	where, args := stashMissingFilterWhere(s.dialect, f)
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM stash_missing_scenes m LEFT JOIN releases r ON r.id=m.release_id`+where, args...).Scan(&n)
	return n, err
}

// UpsertStashMissingScene inserts or refreshes one scanned scene, keyed on
// StashSceneID. It never touches release_id/status/message - those belong
// to the recovery workflow (LinkStashMissingRelease/SetStashMissingStatus)
// and must survive an unrelated rescan untouched.
func (s *SQLite) UpsertStashMissingScene(ctx context.Context, x domain.StashMissingScene) (int64, error) {
	now := time.Now().UTC()
	paths, _ := json.Marshal(x.Paths)
	tags, _ := json.Marshal(x.Tags)
	urls, _ := json.Marshal(x.URLs)
	if paths == nil {
		paths = []byte("[]")
	}
	if tags == nil {
		tags = []byte("[]")
	}
	if urls == nil {
		urls = []byte("[]")
	}
	var id int64
	err := s.db.QueryRowContext(ctx, `SELECT id FROM stash_missing_scenes WHERE stash_scene_id=?`, x.StashSceneID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return s.dialect.InsertReturningID(ctx, s.db, `INSERT INTO stash_missing_scenes(stash_scene_id,title,code,date,path,paths,o_counter,play_count,last_played_at,last_o_count_at,studio,tags,urls,javlibrary_url,status,first_seen_at,last_scan_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			x.StashSceneID, x.Title, x.Code, x.Date, x.Path, string(paths), x.OCounter, x.PlayCount, x.LastPlayedAt, x.LastOCountAt, x.Studio, string(tags), string(urls), x.JavLibraryURL, "missing", now, now, now)
	}
	if err != nil {
		return 0, err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE stash_missing_scenes SET title=?,code=?,date=?,path=?,paths=?,o_counter=?,play_count=?,last_played_at=?,last_o_count_at=?,studio=?,tags=?,urls=?,javlibrary_url=?,last_scan_at=?,updated_at=? WHERE id=?`,
		x.Title, x.Code, x.Date, x.Path, string(paths), x.OCounter, x.PlayCount, x.LastPlayedAt, x.LastOCountAt, x.Studio, string(tags), string(urls), x.JavLibraryURL, now, now, id)
	return id, err
}

// LinkStashMissingRelease records that a JAVBeacon release now exists
// for this scene - either because it already existed and was matched by
// video ID, or because it was just retrieved from JavLibrary. It clears any
// stale retrieval status/message (the row's disk-missing state is
// unaffected: EffectiveStatus derives "downloading"/"downloaded"/"failed"/
// "monitored" from the linked release from here on, see
// stashMissingEffectiveStatusExpr).
func (s *SQLite) LinkStashMissingRelease(ctx context.Context, id, releaseID int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE stash_missing_scenes SET release_id=?,status='',message='',updated_at=? WHERE id=?`, releaseID, time.Now().UTC(), id)
	return err
}

// SetStashMissingStatus records the outcome of a retrieval attempt
// (status is "retrieving" while in flight, or "retrieve_failed" with
// message set to the error on failure).
func (s *SQLite) SetStashMissingStatus(ctx context.Context, id int64, status, message string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE stash_missing_scenes SET status=?,message=?,updated_at=? WHERE id=?`, status, message, time.Now().UTC(), id)
	return err
}

// PruneStashMissingScenes removes rows a scan starting at or after
// scanStartedAt did not touch - the file reappeared on disk (or the scene
// was deleted from StashApp entirely) since the previous scan, so it no
// longer belongs in the missing list regardless of whether it was linked to
// a release.
func (s *SQLite) PruneStashMissingScenes(ctx context.Context, scanStartedAt time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM stash_missing_scenes WHERE last_scan_at<?`, scanStartedAt)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ClearStashMissingScenes wipes every recorded Missing Library Files result,
// for the manual "Clear results" action - unlike PruneStashMissingScenes
// (which only drops rows a completed scan didn't re-confirm as still
// missing), this removes the whole list unconditionally, regardless of
// when each row was last seen.
func (s *SQLite) ClearStashMissingScenes(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM stash_missing_scenes`)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// StashMissingScene looks up a single row by id, joined the same way as
// StashMissingScenes, for handlers that act on one scene at a time (e.g.
// after a retrieval attempt, to report its fresh EffectiveStatus).
func (s *SQLite) StashMissingScene(ctx context.Context, id int64) (domain.StashMissingScene, error) {
	row := s.db.QueryRowContext(ctx, stashMissingSelect+` WHERE m.id=?`, id)
	return scanStashMissingScene(row)
}

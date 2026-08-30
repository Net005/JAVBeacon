package legacyimport

import (
	"context"
	"database/sql"
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Net005/JAVBeacon/internal/store"
)

type SiteOptions struct {
	DatabasePath string
	SitesPath    string
	Apply        bool
}

type SiteReport struct {
	Applied           bool           `json:"applied"`
	StartedAt         time.Time      `json:"started_at"`
	FinishedAt        time.Time      `json:"finished_at"`
	FileRows          int            `json:"file_rows"`
	ImportedSites     int            `json:"imported_sites"`
	SkippedGIGA       int            `json:"skipped_giga"`
	MatchedSites      int            `json:"matched_sites"`
	ReleasesRemoved   int            `json:"releases_removed"`
	ReleasesPreserved int            `json:"releases_preserved"`
	SitesAfter        int            `json:"sites_after"`
	EnabledAfter      int            `json:"enabled_after"`
	Categories        map[string]int `json:"categories"`
}

type legacySite struct {
	Title, Type, Name, URL string
	Updated                time.Time
	Notify                 bool
}

// ReplaceSites replaces only non-GIGA database sites named in the legacy TSV.
// Releases owned by all sites not named in the file are intentionally preserved.
func ReplaceSites(ctx context.Context, options SiteOptions) (SiteReport, error) {
	report := SiteReport{Applied: options.Apply, StartedAt: time.Now().UTC(), Categories: map[string]int{}}
	rows, err := loadSitesFile(options.SitesPath)
	if err != nil {
		return report, err
	}
	report.FileRows = len(rows)
	selected := make([]legacySite, 0, len(rows))
	seen := map[string]bool{}
	for _, row := range rows {
		if strings.EqualFold(row.Title, "GIGA") {
			report.SkippedGIGA++
			continue
		}
		key := strings.ToLower(row.Title)
		if seen[key] {
			return report, fmt.Errorf("duplicate site title %q", row.Title)
		}
		seen[key] = true
		selected = append(selected, row)
		report.Categories[row.Type]++
	}
	report.ImportedSites = len(selected)

	migrated, err := store.OpenSQLite(options.DatabasePath)
	if err != nil {
		return report, err
	}
	if err = migrated.Close(); err != nil {
		return report, err
	}
	db, err := sql.Open("sqlite", options.DatabasePath+"?_pragma=busy_timeout(30000)&_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)")
	if err != nil {
		return report, err
	}
	defer db.Close()

	type existingSite struct{ ID int64 }
	var matches []existingSite
	matchedIDs := map[int64]bool{}
	for _, row := range selected {
		found, err := db.QueryContext(ctx, `SELECT id FROM sites WHERE title=? COLLATE NOCASE`, row.Title)
		if err != nil {
			return report, err
		}
		for found.Next() {
			var match existingSite
			if err := found.Scan(&match.ID); err != nil {
				found.Close()
				return report, err
			}
			if !matchedIDs[match.ID] {
				matchedIDs[match.ID] = true
				matches = append(matches, match)
			}
		}
		if err := found.Close(); err != nil {
			return report, err
		}
	}
	for _, match := range matches {
		var releaseCount int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM releases WHERE site_id=?`, match.ID).Scan(&releaseCount); err != nil {
			return report, err
		}
		report.ReleasesRemoved += releaseCount
	}
	report.MatchedSites = len(matches)
	var totalReleases, existingSiteCount int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM releases`).Scan(&totalReleases); err != nil {
		return report, err
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sites`).Scan(&existingSiteCount); err != nil {
		return report, err
	}
	report.ReleasesPreserved = totalReleases - report.ReleasesRemoved
	report.SitesAfter = existingSiteCount - report.MatchedSites + report.ImportedSites
	report.EnabledAfter = report.SitesAfter

	if options.Apply {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return report, err
		}
		defer tx.Rollback()
		for _, match := range matches {
			if _, err := tx.ExecContext(ctx, `DELETE FROM sites WHERE id=?`, match.ID); err != nil {
				return report, err
			}
		}
		now := time.Now().UTC()
		for _, row := range selected {
			updated := row.Updated
			if updated.IsZero() {
				updated = now
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO sites(title,type,name,url,notify,download,watchlist,rss_url,enabled,created_at,updated_at) VALUES(?,?,?,?,?,0,0,'',1,?,?)`, row.Title, row.Type, row.Name, row.URL, row.Notify, now, updated); err != nil {
				return report, err
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE sites SET enabled=1,updated_at=? WHERE enabled<>1`, now); err != nil {
			return report, err
		}
		if err := tx.Commit(); err != nil {
			return report, err
		}
	}
	report.FinishedAt = time.Now().UTC()
	return report, nil
}

func loadSitesFile(path string) ([]legacySite, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	reader := csv.NewReader(f)
	reader.Comma = '\t'
	reader.FieldsPerRecord = -1
	header, err := reader.Read()
	if err != nil {
		return nil, err
	}
	want := []string{"Title", "Type", "SiteName", "SiteUrl", "SiteUpdated", "ReleaseNotification"}
	if len(header) != len(want) {
		return nil, fmt.Errorf("unexpected sites header: got %d columns, want %d", len(header), len(want))
	}
	for i := range want {
		if strings.TrimSpace(header[i]) != want[i] {
			return nil, fmt.Errorf("unexpected sites header column %d: got %q, want %q", i+1, header[i], want[i])
		}
	}
	var out []legacySite
	for line := 2; ; line++ {
		fields, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("sites line %d: %w", line, err)
		}
		if len(fields) != len(want) {
			return nil, fmt.Errorf("sites line %d: got %d columns, want %d", line, len(fields), len(want))
		}
		title, provider := strings.TrimSpace(fields[0]), strings.TrimSpace(fields[2])
		if title == "" || provider == "" {
			return nil, fmt.Errorf("sites line %d: title and provider are required", line)
		}
		typ := strings.TrimSpace(fields[1])
		if strings.EqualFold(typ, "Genre") {
			typ = "Tag"
		}
		notify, err := strconv.ParseBool(strings.TrimSpace(fields[5]))
		if err != nil {
			return nil, fmt.Errorf("sites line %d notification value: %w", line, err)
		}
		out = append(out, legacySite{Title: title, Type: typ, Name: provider, URL: strings.TrimSpace(fields[3]), Updated: parseLegacyTime(fields[4]), Notify: notify})
	}
	return out, nil
}

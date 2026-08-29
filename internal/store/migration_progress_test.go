package store

import (
	"strings"
	"testing"
)

func TestDescribePostgresMigrationStatement(t *testing.T) {
	for _, tc := range []struct {
		statement string
		want      string
	}{
		{`CREATE EXTENSION IF NOT EXISTS pg_trgm`, "extension pg_trgm"},
		{`CREATE TABLE IF NOT EXISTS releases (id BIGINT)`, "table releases"},
		{`CREATE INDEX IF NOT EXISTS idx_releases_date ON releases(release_date)`, "index idx_releases_date"},
		{"-- compatibility index\nCREATE INDEX IF NOT EXISTS idx_release_sites_site ON release_sites(site_id)", "index idx_release_sites_site"},
		{`ALTER TABLE releases ADD COLUMN IF NOT EXISTS desired_at TIMESTAMPTZ`, "table releases"},
	} {
		if got := describePostgresMigrationStatement(tc.statement); !strings.Contains(got, tc.want) {
			t.Fatalf("describePostgresMigrationStatement(%q)=%q, want it to contain %q", tc.statement, got, tc.want)
		}
	}
}

func TestReportMigrationAllowsNoCallback(t *testing.T) {
	reportMigration(nil, MigrationProgress{Phase: "schema", Step: "test"})
}

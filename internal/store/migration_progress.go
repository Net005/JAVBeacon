package store

import (
	"regexp"
	"strings"
)

// MigrationProgress describes the database work JAVBeacon performs before the
// normal application can start. Current and Total refer to the current phase;
// a zero Total means the phase cannot provide a useful numeric estimate.
type MigrationProgress struct {
	Phase   string `json:"phase"`
	Step    string `json:"step"`
	Current int    `json:"current,omitempty"`
	Total   int    `json:"total,omitempty"`
}

type MigrationProgressFunc func(MigrationProgress)

var migrationObjectName = regexp.MustCompile(`(?i)^(?:CREATE\s+(?:UNIQUE\s+)?(?:TABLE|INDEX|EXTENSION)|ALTER\s+TABLE)\s+(?:IF\s+NOT\s+EXISTS\s+)?([^\s(]+)`)

func describePostgresMigrationStatement(statement string) string {
	statement = strings.TrimSpace(statement)
	for strings.HasPrefix(statement, "--") {
		if newline := strings.IndexByte(statement, '\n'); newline >= 0 {
			statement = strings.TrimSpace(statement[newline+1:])
			continue
		}
		break
	}
	if match := migrationObjectName.FindStringSubmatch(statement); len(match) == 2 {
		name := strings.Trim(match[1], `"`)
		switch {
		case strings.HasPrefix(strings.ToUpper(statement), "CREATE EXTENSION"):
			return "Enabling PostgreSQL extension " + name
		case strings.Contains(strings.ToUpper(statement), "INDEX"):
			return "Preparing database index " + name
		case strings.HasPrefix(strings.ToUpper(statement), "ALTER TABLE"):
			return "Updating database table " + name
		default:
			return "Preparing database table " + name
		}
	}
	return "Applying database schema update"
}

func reportMigration(report MigrationProgressFunc, progress MigrationProgress) {
	if report != nil {
		report(progress)
	}
}

package version

import (
	"context"
	_ "embed"
	"fmt"
	"strconv"
	"strings"
)

//go:generate cp ../../CHANGELOG.md CHANGELOG.md

//go:embed CHANGELOG.md
var changelogSource string

const (
	installedVersionKey = "app_installed_version"
	pendingFromKey      = "app_changelog_pending_from"
	pendingToKey        = "app_changelog_pending_to"
)

type ChangeSection struct {
	Title   string   `json:"title"`
	Changes []string `json:"changes"`
}

type ReleaseNotes struct {
	Version  string          `json:"version"`
	Date     string          `json:"date"`
	Sections []ChangeSection `json:"sections"`
}

type PendingChangelog struct {
	Available bool           `json:"available"`
	From      string         `json:"from,omitempty"`
	To        string         `json:"to,omitempty"`
	Releases  []ReleaseNotes `json:"releases,omitempty"`
}

type settingsStore interface {
	Settings(context.Context) (map[string]string, error)
	SaveSettings(context.Context, map[string]string) error
}

// InitializeTracking records the running version once per application
// startup. A fresh database establishes a baseline without showing release
// notes. An existing installation records one pending old-to-new transition,
// which remains available until the frontend displays and acknowledges it.
func InitializeTracking(ctx context.Context, store settingsStore, freshInstall bool) error {
	settings, err := store.Settings(ctx)
	if err != nil {
		return err
	}
	current := normalizeVersion(Current())
	if current == "" || current == "dev" {
		return nil
	}
	installed := normalizeVersion(settings[installedVersionKey])
	if installed == "" {
		values := map[string]string{installedVersionKey: current}
		if !freshInstall {
			if previous := previousRelease(current); previous != "" {
				values[pendingFromKey] = previous
				values[pendingToKey] = current
			}
		}
		return store.SaveSettings(ctx, values)
	}
	if installed == current {
		return nil
	}
	values := map[string]string{installedVersionKey: current}
	if compareVersions(installed, current) < 0 && len(NotesBetween(installed, current)) > 0 {
		values[pendingFromKey] = installed
		values[pendingToKey] = current
	} else {
		values[pendingFromKey] = ""
		values[pendingToKey] = ""
	}
	return store.SaveSettings(ctx, values)
}

func PendingChange(ctx context.Context, store settingsStore) (PendingChangelog, error) {
	settings, err := store.Settings(ctx)
	if err != nil {
		return PendingChangelog{}, err
	}
	from, to := normalizeVersion(settings[pendingFromKey]), normalizeVersion(settings[pendingToKey])
	if from == "" || to == "" || compareVersions(from, to) >= 0 {
		return PendingChangelog{}, nil
	}
	notes := NotesBetween(from, to)
	if len(notes) == 0 {
		return PendingChangelog{}, nil
	}
	return PendingChangelog{Available: true, From: "v" + from, To: "v" + to, Releases: notes}, nil
}

// AcknowledgeChange clears only the pending transition the caller actually
// displayed. A stale browser cannot accidentally acknowledge a newer upgrade.
func AcknowledgeChange(ctx context.Context, store settingsStore, from, to string) (bool, error) {
	settings, err := store.Settings(ctx)
	if err != nil {
		return false, err
	}
	from, to = normalizeVersion(from), normalizeVersion(to)
	if from == "" || to == "" || normalizeVersion(settings[pendingFromKey]) != from || normalizeVersion(settings[pendingToKey]) != to {
		return false, nil
	}
	if err := store.SaveSettings(ctx, map[string]string{pendingFromKey: "", pendingToKey: ""}); err != nil {
		return false, err
	}
	return true, nil
}

func NotesBetween(from, to string) []ReleaseNotes {
	from, to = normalizeVersion(from), normalizeVersion(to)
	if compareVersions(from, to) >= 0 {
		return nil
	}
	var out []ReleaseNotes
	for _, release := range parseChangelog(changelogSource) {
		version := normalizeVersion(release.Version)
		if compareVersions(version, from) > 0 && compareVersions(version, to) <= 0 {
			out = append(out, release)
		}
	}
	return out
}

func previousRelease(current string) string {
	current = normalizeVersion(current)
	for _, release := range parseChangelog(changelogSource) {
		version := normalizeVersion(release.Version)
		if compareVersions(version, current) < 0 {
			return version
		}
	}
	return ""
}

func parseChangelog(source string) []ReleaseNotes {
	var releases []ReleaseNotes
	var release *ReleaseNotes
	var section *ChangeSection
	for _, raw := range strings.Split(source, "\n") {
		line := strings.TrimSpace(raw)
		if strings.HasPrefix(line, "## [") {
			end := strings.Index(line, "]")
			if end < 4 || line[4:end] == "Unreleased" {
				release = nil
				section = nil
				continue
			}
			version := line[4:end]
			date := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line[end+1:]), "-"))
			releases = append(releases, ReleaseNotes{Version: version, Date: date})
			release = &releases[len(releases)-1]
			section = nil
			continue
		}
		if release == nil {
			continue
		}
		if strings.HasPrefix(line, "### ") {
			release.Sections = append(release.Sections, ChangeSection{Title: strings.TrimSpace(strings.TrimPrefix(line, "### "))})
			section = &release.Sections[len(release.Sections)-1]
			continue
		}
		if section == nil {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.Contains(line, "]:") {
			continue
		}
		if strings.HasPrefix(line, "- ") {
			section.Changes = append(section.Changes, cleanMarkdown(strings.TrimPrefix(line, "- ")))
		} else if line != "" && len(section.Changes) > 0 {
			last := len(section.Changes) - 1
			section.Changes[last] += " " + cleanMarkdown(line)
		}
	}
	return releases
}

func cleanMarkdown(value string) string {
	return strings.ReplaceAll(strings.TrimSpace(value), "`", "")
}

func normalizeVersion(value string) string {
	return strings.TrimPrefix(strings.TrimSpace(value), "v")
}

func compareVersions(a, b string) int {
	av, aok := semanticParts(a)
	bv, bok := semanticParts(b)
	if !aok || !bok {
		return strings.Compare(normalizeVersion(a), normalizeVersion(b))
	}
	for i := range av {
		if av[i] < bv[i] {
			return -1
		}
		if av[i] > bv[i] {
			return 1
		}
	}
	return 0
}

func semanticParts(value string) ([3]int, bool) {
	var parts [3]int
	fields := strings.Split(strings.SplitN(normalizeVersion(value), "-", 2)[0], ".")
	if len(fields) != len(parts) {
		return parts, false
	}
	for i, field := range fields {
		n, err := strconv.Atoi(field)
		if err != nil || n < 0 {
			return parts, false
		}
		parts[i] = n
	}
	return parts, true
}

func ValidateEmbeddedChangelog() error {
	current := normalizeVersion(Current())
	for _, release := range parseChangelog(changelogSource) {
		if normalizeVersion(release.Version) == current {
			return nil
		}
	}
	return fmt.Errorf("embedded changelog has no release section for %s", Current())
}

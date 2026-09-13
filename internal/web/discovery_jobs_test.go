package web

import (
	"testing"
	"time"
)

func TestDiscoveryNextRunsUsesConfiguredScheduleMode(t *testing.T) {
	now := time.Date(2026, time.September, 14, 10, 30, 0, 0, time.Local) // Monday
	base := map[string]string{
		"discoveries_enabled":          "true",
		"discoveries_refresh_enabled":  "true",
		"discoveries_refresh_interval": "24h",
	}

	advanced := cloneStringMap(base)
	advanced["discoveries_schedule_mode"] = "advanced"
	advanced["discoveries_start_time"] = "07:15"
	advanced["discoveries_weekdays"] = "Wed,Fri"
	runs := discoveryNextRuns(advanced, now, 2)
	if len(runs) != 2 || runs[0].Weekday() != time.Wednesday || runs[0].Hour() != 7 || runs[0].Minute() != 15 || runs[1].Weekday() != time.Friday {
		t.Fatalf("unexpected advanced runs: %v", runs)
	}

	cron := cloneStringMap(base)
	cron["discoveries_schedule_mode"] = "cron"
	cron["discoveries_cron"] = "0 3 * * 1-5"
	runs = discoveryNextRuns(cron, now, 2)
	if len(runs) != 2 || runs[0].Hour() != 3 || runs[0].Minute() != 0 || runs[0].Weekday() != time.Tuesday {
		t.Fatalf("unexpected cron runs: %v", runs)
	}
}

func TestDiscoveryNextRunsDisabledHasNoForecast(t *testing.T) {
	settings := map[string]string{
		"discoveries_enabled":          "true",
		"discoveries_refresh_enabled":  "false",
		"discoveries_refresh_interval": "24h",
	}
	if runs := discoveryNextRuns(settings, time.Now(), 3); len(runs) != 0 {
		t.Fatalf("disabled schedule returned runs: %v", runs)
	}
}

func cloneStringMap(source map[string]string) map[string]string {
	clone := make(map[string]string, len(source))
	for key, value := range source {
		clone[key] = value
	}
	return clone
}

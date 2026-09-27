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

func TestDiscoveryJobSnapshotRestoresSubtitleIndexAfterRestart(t *testing.T) {
	discoveryJobs.Lock()
	previous := discoveryJobs.status
	discoveryJobs.status = discoveryJobStatus{}
	discoveryJobs.Unlock()
	t.Cleanup(func() { discoveryJobs.Lock(); discoveryJobs.status = previous; discoveryJobs.Unlock() })
	indexedAt := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339Nano)
	status := discoveryJobSnapshot(map[string]string{
		"discoveries_subtitle_index_updated_at":      indexedAt,
		"discoveries_subtitle_index_available":       "42",
		"discoveries_subtitle_index_usable":          "37",
		"discoveries_subtitle_index_scanned":         "100",
		"discoveries_subtitle_index_missing_paths":   "3",
		"discoveries_subtitle_index_unreadable_dirs": "2",
	})
	if status.SubtitleCount != 42 || status.SubtitleUsable != 37 || status.SubtitleScanned != 100 || status.SubtitleMissingPath != 3 || status.SubtitleUnreadableDirs != 2 || status.SubtitleIndexUpdatedAt.IsZero() {
		t.Fatalf("restored subtitle index = %+v", status)
	}
}

func TestDiscoveryJobSnapshotKeepsCompletedRunTiming(t *testing.T) {
	discoveryJobs.Lock()
	previous := discoveryJobs.status
	started := time.Now().UTC().Add(-10 * time.Second)
	discoveryJobs.status = discoveryJobStatus{Stage: "Synchronized", StartedAt: started, FinishedAt: started.Add(10 * time.Second), Completed: 50, Total: 50}
	discoveryJobs.Unlock()
	t.Cleanup(func() {
		discoveryJobs.Lock()
		discoveryJobs.status = previous
		discoveryJobs.Unlock()
	})

	status := discoveryJobSnapshot(map[string]string{})
	if status.ElapsedSeconds != 10 || status.ItemsPerSecond != 5 {
		t.Fatalf("completed timing = %.2fs at %.2f items/s, want 10s at 5 items/s", status.ElapsedSeconds, status.ItemsPerSecond)
	}
}

func cloneStringMap(source map[string]string) map[string]string {
	clone := make(map[string]string, len(source))
	for key, value := range source {
		clone[key] = value
	}
	return clone
}

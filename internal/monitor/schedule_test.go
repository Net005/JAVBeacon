package monitor

import (
	"testing"
	"time"
)

func TestCalendarScheduleMatchesStartTimeAndWeekdays(t *testing.T) {
	monday := time.Date(2026, 8, 24, 3, 15, 0, 0, time.Local)
	matched, err := calendarScheduleMatches(monday, "03:15", "Mon,Wed,Fri", "")
	if err != nil || !matched {
		t.Fatalf("matched=%v err=%v, want Monday 03:15 to match", matched, err)
	}
	matched, err = calendarScheduleMatches(monday.Add(24*time.Hour), "03:15", "Mon,Wed,Fri", "")
	if err != nil || matched {
		t.Fatalf("matched=%v err=%v, want Tuesday not to match", matched, err)
	}
}

func TestCronScheduleOverridesCalendarTiming(t *testing.T) {
	now := time.Date(2026, 8, 24, 4, 30, 0, 0, time.Local)
	matched, err := calendarScheduleMatches(now, "22:00", "Sun", "30 4 * * 1-5")
	if err != nil || !matched {
		t.Fatalf("matched=%v err=%v, want cron override to match", matched, err)
	}
	if err := ValidateCronSchedule("not cron"); err == nil {
		t.Fatal("invalid cron expression was accepted")
	}
}

func TestSettingsScheduleCombinationFromUIIsValid(t *testing.T) {
	for _, schedule := range []struct {
		name      string
		startTime string
		weekdays  string
	}{
		{name: "quick", startTime: "14:00", weekdays: "Mon"},
		{name: "new releases", startTime: "12:00"},
		{name: "full", startTime: "20:00"},
	} {
		t.Run(schedule.name, func(t *testing.T) {
			if err := ValidateCalendarSchedule(schedule.startTime, schedule.weekdays); err != nil {
				t.Fatalf("ValidateCalendarSchedule(%q, %q): %v", schedule.startTime, schedule.weekdays, err)
			}
			if err := ValidateCronSchedule(""); err != nil {
				t.Fatalf("blank cron placeholder must not become an override: %v", err)
			}
		})
	}
}

func TestScheduleModesAreExplicitAndBasicStartAnchorsInterval(t *testing.T) {
	if got := normalizeScheduleMode("basic", "14:00", "Mon", "0 3 * * 1-5"); got != "basic" {
		t.Fatalf("explicit mode = %q, want basic", got)
	}
	now := time.Date(2026, 8, 27, 10, 0, 0, 0, time.Local)
	first := nextBasicRun(now, 12*time.Hour, "12:00")
	if want := time.Date(2026, 8, 27, 12, 0, 0, 0, time.Local); !first.Equal(want) {
		t.Fatalf("first basic run = %v, want %v", first, want)
	}
	if second := first.Add(12 * time.Hour); second.Hour() != 0 || second.Day() != 28 {
		t.Fatalf("second basic run = %v, want midnight next day", second)
	}
}

func TestAdvancedScheduleAppliesIntervalAsMinimumGap(t *testing.T) {
	now := time.Date(2026, 8, 23, 13, 0, 0, 0, time.Local) // Sunday
	runs := nextAdvancedRuns(now, "14:00", "Mon,Wed,Fri", 7*24*time.Hour, 3)
	if len(runs) != 3 {
		t.Fatalf("runs = %v, want 3", runs)
	}
	for i := 1; i < len(runs); i++ {
		if runs[i].Sub(runs[i-1]) < 7*24*time.Hour {
			t.Fatalf("runs %v and %v are closer than the configured interval", runs[i-1], runs[i])
		}
	}
}

func TestDefaultScheduledPrioritiesPutNewBeforeQuickBeforeFull(t *testing.T) {
	queue := []RefreshOptions{}
	for _, item := range []RefreshOptions{
		{Mode: "full", Scheduled: true, Priority: jobPriorityDefault(PriorityKindScheduledFull)},
		{Mode: "quick", Scheduled: true, Priority: jobPriorityDefault(PriorityKindScheduledQuick)},
		{Mode: "new", Scheduled: true, Priority: jobPriorityDefault(PriorityKindScheduledNew)},
	} {
		queue = enqueueRefresh(queue, item)
	}
	for i, mode := range []string{"new", "quick", "full"} {
		if queue[i].Mode != mode {
			t.Fatalf("queue[%d]=%s, want %s", i, queue[i].Mode, mode)
		}
	}
}

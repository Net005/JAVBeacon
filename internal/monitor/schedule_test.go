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

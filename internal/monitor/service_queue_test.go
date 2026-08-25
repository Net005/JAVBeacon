package monitor

import (
	"context"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/Net005/JAVBeacon/internal/domain"
	"github.com/Net005/JAVBeacon/internal/store"
)

// TestRefreshQueueOrdersPriorityAndPreservesFIFO exercises enqueueRefresh in
// isolation: lower Priority values run first, and jobs sharing a
// priority keep FIFO order among themselves. Priority is set explicitly here
// as StartOptions (via resolvePriority) would have already resolved it
// before a job reaches the queue.
func TestRefreshQueueOrdersPriorityAndPreservesFIFO(t *testing.T) {
	queue := []RefreshOptions{}
	for _, options := range []RefreshOptions{
		{SiteID: 30, Scheduled: true, Priority: 15},
		{SiteID: 20, Priority: 8},
		{ReleaseID: 10, Priority: 5},
		{SiteID: 21, Priority: 8},
		{ReleaseID: 11, Priority: 5},
	} {
		queue = enqueueRefresh(queue, options)
	}
	want := []struct {
		priority  int
		releaseID int64
		siteID    int64
	}{
		{5, 10, 0},
		{5, 11, 0},
		{8, 0, 20},
		{8, 0, 21},
		{15, 0, 30},
	}
	if len(queue) != len(want) {
		t.Fatalf("queue length=%d, want %d", len(queue), len(want))
	}
	for index, expected := range want {
		if got := refreshPriority(queue[index]); got != expected.priority || queue[index].ReleaseID != expected.releaseID || queue[index].SiteID != expected.siteID {
			t.Fatalf("queue[%d]=%+v priority=%d, want priority=%d release=%d site=%d", index, queue[index], got, expected.priority, expected.releaseID, expected.siteID)
		}
	}
}

func TestScheduledScrapeCoordinatorQueuesDueScansByConfiguredPriority(t *testing.T) {
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "scheduled-priority.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Millisecond)
	defer cancel()
	if err := st.SaveSettings(ctx, map[string]string{
		"full_enabled": "true", "new_enabled": "true", "quick_enabled": "true",
		JobPrioritySettingKey(PriorityKindScheduledFull):  "20",
		JobPrioritySettingKey(PriorityKindScheduledNew):   "40",
		JobPrioritySettingKey(PriorityKindScheduledQuick): "30",
	}); err != nil {
		t.Fatal(err)
	}
	service := &Service{store: st, log: slog.Default(), worker: true}
	service.runScrapeSchedules(ctx, []scrapeSchedule{
		{mode: "full", enabledKey: "full_enabled", priorityKind: PriorityKindScheduledFull, fallback: 10 * time.Millisecond},
		{mode: "new", enabledKey: "new_enabled", priorityKind: PriorityKindScheduledNew, fallback: 10 * time.Millisecond},
		{mode: "quick", enabledKey: "quick_enabled", priorityKind: PriorityKindScheduledQuick, fallback: 10 * time.Millisecond},
	})
	if len(service.queue) != 3 {
		t.Fatalf("queued scans = %+v, want exactly three distinct scheduled modes", service.queue)
	}
	for i, want := range []struct {
		mode     string
		priority int
	}{{"full", 20}, {"quick", 30}, {"new", 40}} {
		if got := service.queue[i]; got.Mode != want.mode || got.Priority != want.priority {
			t.Fatalf("queue[%d] = %+v, want mode=%s priority=%d", i, got, want.mode, want.priority)
		}
	}
}

// TestResolvePriorityUsesOverrideThenSettingThenBuiltinDefault covers Phase
// 3's single shared priority mechanism: an explicit per-call override always
// wins, otherwise a configured "job_priority_<kind>" setting is used, and
// the built-in default table applies only when neither is present.
func TestResolvePriorityUsesOverrideThenSettingThenBuiltinDefault(t *testing.T) {
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "priority.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	service := &Service{store: st, log: slog.Default()}
	ctx := context.Background()

	if got := service.resolvePriority(ctx, PriorityKindStartSource, 0); got != 10 {
		t.Fatalf("built-in default = %d, want 10", got)
	}
	if got := service.resolvePriority(ctx, PriorityKindStartSource, 42); got != 42 {
		t.Fatalf("explicit override = %d, want 42", got)
	}
	if err := st.SaveSettings(ctx, map[string]string{JobPrioritySettingKey(PriorityKindStartSource): "99"}); err != nil {
		t.Fatal(err)
	}
	if got := service.resolvePriority(ctx, PriorityKindStartSource, 0); got != 99 {
		t.Fatalf("configured default = %d, want 99", got)
	}
	if got := service.resolvePriority(ctx, PriorityKindStartSource, 7); got != 7 {
		t.Fatalf("explicit override still wins over configured default = %d, want 7", got)
	}
	if err := st.SaveSettings(ctx, map[string]string{JobPrioritySettingKey(PriorityKindStartSource): "1000"}); err != nil {
		t.Fatal(err)
	}
	if got := service.resolvePriority(ctx, PriorityKindStartSource, 0); got != 10 {
		t.Fatalf("out-of-range configured priority = %d, want built-in default 10", got)
	}
}

func TestStartOptionsAcceptsPriorityRangeAndRejectsValuesOutsideIt(t *testing.T) {
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "priority-range.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	service := &Service{store: st, log: slog.Default(), worker: true}
	for _, priority := range []int{1, 999} {
		if err := service.StartOptions(context.Background(), RefreshOptions{Priority: priority}); err != nil {
			t.Fatalf("priority %d rejected: %v", priority, err)
		}
	}
	for _, priority := range []int{-1, 1000} {
		if err := service.StartOptions(context.Background(), RefreshOptions{Priority: priority}); err == nil {
			t.Fatalf("priority %d was accepted", priority)
		}
	}
	if got := service.queue; len(got) != 2 || got[0].Priority != 1 || got[1].Priority != 999 {
		t.Fatalf("queue = %+v, want priority 1 before priority 999", got)
	}
}

// TestStartOptionsAllPagesPromotesStartSourceToManualFull covers the Phase 3
// rule that selecting All Pages on a manual site scrape uses the same
// underlying behavior/priority as a manual full-page scrape, while still
// letting an explicit Priority override win.
func TestStartOptionsAllPagesPromotesStartSourceToManualFull(t *testing.T) {
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "priority.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	service := &Service{store: st, log: slog.Default(), worker: true, queue: []RefreshOptions{{SiteID: 999, Priority: 50}}}
	ctx := context.Background()

	if e := service.StartOptions(ctx, RefreshOptions{SiteID: 1, AllPages: true}); e != nil {
		t.Fatal(e)
	}
	if service.queue[0].Kind != PriorityKindManualFull || service.queue[0].Priority != 20 {
		t.Fatalf("all_pages request = %+v, want kind=%s priority=20", service.queue[0], PriorityKindManualFull)
	}

	if e := service.StartOptions(ctx, RefreshOptions{SiteID: 2, AllPages: true, Priority: 77}); e != nil {
		t.Fatal(e)
	}
	if got := service.queue[len(service.queue)-1]; got.SiteID != 2 || got.Priority != 77 {
		t.Fatalf("explicit override = %+v, want site 2 to retain priority 77", got)
	}
}

func TestStopCancelsActiveJobAndClearsQueue(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	service := &Service{
		worker: true,
		cancel: cancel,
		queue:  []RefreshOptions{{SiteID: 2, Title: "Neo Akari", Mode: "new", Priority: 8}, {ReleaseID: 3, Title: "Release TEST-3", Mode: "full", Priority: 5}},
		job:    domain.Job{State: "running", Running: true, QueueDepth: 2},
		log:    slog.Default(),
	}
	queued := service.Status()
	if len(queued.QueuedJobs) != 2 || queued.QueuedJobs[0].Position != 1 || queued.QueuedJobs[0].Title != "Neo Akari" || queued.QueuedJobs[1].Priority != 5 {
		t.Fatalf("unexpected queued job snapshot: %+v", queued.QueuedJobs)
	}
	cleared, stopped := service.Stop()
	if !stopped || cleared != 2 {
		t.Fatalf("stopped=%v cleared=%d, want true and 2", stopped, cleared)
	}
	select {
	case <-ctx.Done():
	default:
		t.Fatal("active scrape context was not cancelled")
	}
	status := service.Status()
	if status.State != "stopping" || status.QueueDepth != 0 || len(service.queue) != 0 {
		t.Fatalf("unexpected stopped status: %+v queue=%+v", status, service.queue)
	}
}

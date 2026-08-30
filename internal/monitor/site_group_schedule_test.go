package monitor

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/Net005/JAVBeacon/internal/domain"
	"github.com/Net005/JAVBeacon/internal/store"
)

// newTestServiceWithSites returns a Service backed by a fresh SQLite store
// seeded with the given sites (each saved and returned with its assigned
// ID), for the site-group-schedule tests below.
func newTestServiceWithSites(t *testing.T, sites ...domain.Site) (*Service, []domain.Site) {
	t.Helper()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "site-group-schedules.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	saved := make([]domain.Site, 0, len(sites))
	for _, site := range sites {
		x, err := st.SaveSite(context.Background(), site)
		if err != nil {
			t.Fatal(err)
		}
		saved = append(saved, x)
	}
	return &Service{store: st, log: slog.Default()}, saved
}

// TestExpandSiteGroupSchedulesHasNoOpFastPathWhenUnconfigured covers the
// copy-on-write fast path: with no site_group_schedules setting at all,
// expandSiteGroupSchedules must return the original settings map itself
// (not a copy) and no extra schedules, so the common case (no group
// schedules configured) costs nothing on every scheduler tick.
func TestExpandSiteGroupSchedulesHasNoOpFastPathWhenUnconfigured(t *testing.T) {
	service, _ := newTestServiceWithSites(t)
	settings := map[string]string{"quick_refresh_enabled": "true"}
	augmented, extra := service.expandSiteGroupSchedules(context.Background(), settings)
	if len(extra) != 0 {
		t.Fatalf("extra schedules = %+v, want none", extra)
	}
	// Same underlying map (not a defensive copy) - mutate through augmented
	// and confirm settings sees it too.
	augmented["probe"] = "x"
	if settings["probe"] != "x" {
		t.Fatal("expandSiteGroupSchedules copied settings even though no groups were configured")
	}
}

// TestExpandSiteGroupSchedulesBuildsOnePerEnabledSiteAndSkipsInvalidEntries
// is the core structural test for expandSiteGroupSchedules: an enabled
// group with a valid site and an unknown site should expand to exactly one
// synthetic scrapeSchedule (skipping the unknown site rather than erroring,
// since the site may have been deleted after the schedule was configured),
// with its title, mode, siteID, and priorityOverride all carried through
// from the group/site configuration, and its synthetic settings keys
// written into the augmented map. A second, disabled group must contribute
// nothing at all.
func TestExpandSiteGroupSchedulesBuildsOnePerEnabledSiteAndSkipsInvalidEntries(t *testing.T) {
	service, sites := newTestServiceWithSites(t,
		domain.Site{Title: "Actress A", Type: "Actress", Name: "JavLibrary", URL: "https://x.example/a", Enabled: true},
		domain.Site{Title: "Actress B", Type: "Actress", Name: "JavLibrary", URL: "https://x.example/b", Enabled: true},
	)
	groups := []domain.SiteGroupSchedule{
		{
			ID: 1, Name: "Favorites", Enabled: true, Priority: 42, ScheduleMode: "basic", Interval: "2h", Pages: 3,
			Sites: []domain.SiteGroupScheduleSite{
				{SiteID: sites[0].ID, Mode: "full"},
				{SiteID: sites[1].ID, Mode: "new"},
				{SiteID: 999999, Mode: "quick"},      // unknown site - must be skipped, not error
				{SiteID: sites[0].ID, Mode: "bogus"}, // invalid mode - must be skipped too
			},
		},
		{ID: 2, Name: "Disabled group", Enabled: false, Priority: 5, Sites: []domain.SiteGroupScheduleSite{{SiteID: sites[0].ID, Mode: "quick"}}},
	}
	raw, err := json.Marshal(groups)
	if err != nil {
		t.Fatal(err)
	}
	settings := map[string]string{"site_group_schedules": string(raw)}
	augmented, extra := service.expandSiteGroupSchedules(context.Background(), settings)
	if len(extra) != 2 {
		t.Fatalf("expanded schedules = %d, want 2 (unknown site, invalid mode, and disabled group all excluded): %+v", len(extra), extra)
	}
	byID := map[string]scrapeSchedule{}
	for _, sched := range extra {
		byID[sched.id] = sched
	}
	wantIDA := fmt.Sprintf("sitegroup:1:%d", sites[0].ID)
	first, ok := byID[wantIDA]
	if !ok {
		t.Fatalf("missing schedule %q: %+v", wantIDA, extra)
	}
	if first.mode != "full" || first.siteID != sites[0].ID || first.priorityOverride != 42 || first.title != "Favorites · Actress A" || first.priorityKind != PriorityKindScheduledSiteGroup {
		t.Fatalf("unexpected schedule for site A: %+v", first)
	}
	if augmented[first.enabledKey] != "true" {
		t.Fatalf("synthetic enabled key not populated: %q=%q", first.enabledKey, augmented[first.enabledKey])
	}
	if augmented[first.intervalKey] != "2h" {
		t.Fatalf("synthetic interval key not populated: %q=%q", first.intervalKey, augmented[first.intervalKey])
	}
	if first.pagesKey == "" || augmented[first.pagesKey] != "3" {
		t.Fatalf("synthetic pages key not populated: key=%q value=%q", first.pagesKey, augmented[first.pagesKey])
	}
	wantIDB := fmt.Sprintf("sitegroup:1:%d", sites[1].ID)
	second, ok := byID[wantIDB]
	if !ok || second.mode != "new" || second.title != "Favorites · Actress B" {
		t.Fatalf("missing/incorrect schedule %q: %+v", wantIDB, extra)
	}
}

// TestRunScrapeSchedulesKeysStateByIDNotMode is the regression guard for
// the scrapeSchedule.id field: before it existed, every per-schedule map in
// runScrapeSchedules (lastAttempt, basicNext, basicSignature, and
// s.scheduleNextAttempt) was keyed by schedule.mode, which is fine for the
// three built-in schedules (one mode each) but breaks the moment two
// independent schedules can share the same mode - exactly what happens once
// two site-group schedules both scrape different sites in "quick" mode.
// Without distinct ids, the second schedule's due-check would silently
// read/write the first's timing state and the two would collapse into one.
// Both schedules here use mode "quick" but distinct ids and distinct
// siteIDs (as every real site-group-schedule-derived scrapeSchedule does -
// see expandSiteGroupSchedules) and should each be queued independently
// once due; siteID must differ so this doesn't instead exercise
// StartOptions' own separate same-site-and-mode dedup, which is unrelated
// to the id-keyed-maps bug this test targets.
func TestRunScrapeSchedulesKeysStateByIDNotMode(t *testing.T) {
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "id-keyed-schedules.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Millisecond)
	defer cancel()
	if err := st.SaveSettings(ctx, map[string]string{"a_enabled": "true", "b_enabled": "true"}); err != nil {
		t.Fatal(err)
	}
	service := &Service{store: st, log: slog.Default(), worker: true}
	service.runScrapeSchedules(ctx, []scrapeSchedule{
		{id: "a", mode: "quick", title: "A", enabledKey: "a_enabled", priorityKind: PriorityKindScheduledQuick, fallback: 10 * time.Millisecond, siteID: 1},
		{id: "b", mode: "quick", title: "B", enabledKey: "b_enabled", priorityKind: PriorityKindScheduledQuick, fallback: 10 * time.Millisecond, siteID: 2},
	})
	if len(service.queue) != 2 {
		t.Fatalf("queued scans = %+v, want both same-mode schedules queued independently", service.queue)
	}
	titles := map[string]bool{}
	for _, queued := range service.queue {
		titles[queued.Title] = true
	}
	if !titles["A"] || !titles["B"] {
		t.Fatalf("queued scans = %+v, want both A and B", service.queue)
	}
}

// TestScheduleForecastIncludesEnabledSiteGroupSchedules covers
// ScheduleForecast's expandSiteGroupSchedules wiring: an enabled group
// schedule's per-site forecast entries should appear in the returned list,
// grouped separately from the three built-in Quick/Full/New forecasts.
func TestScheduleForecastIncludesEnabledSiteGroupSchedules(t *testing.T) {
	service, sites := newTestServiceWithSites(t,
		domain.Site{Title: "Actress A", Type: "Actress", Name: "JavLibrary", URL: "https://x.example/a", Enabled: true},
	)
	groups := []domain.SiteGroupSchedule{
		{ID: 7, Name: "Favorites", Enabled: true, Priority: 10, ScheduleMode: "basic", Interval: "3h", Sites: []domain.SiteGroupScheduleSite{{SiteID: sites[0].ID, Mode: "quick"}}},
	}
	raw, err := json.Marshal(groups)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.store.SaveSettings(context.Background(), map[string]string{"site_group_schedules": string(raw)}); err != nil {
		t.Fatal(err)
	}
	forecasts := service.ScheduleForecast(context.Background())
	var found *domain.ScheduleForecast
	for i := range forecasts {
		if forecasts[i].Name == "Favorites · Actress A" {
			found = &forecasts[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("forecast for the site group schedule is missing: %+v", forecasts)
	}
	if found.Group != "Site group schedules" || !found.Enabled {
		t.Fatalf("unexpected forecast entry: %+v", found)
	}
	if found.SiteGroupScheduleID != groups[0].ID {
		t.Fatalf("site group schedule id = %d, want %d", found.SiteGroupScheduleID, groups[0].ID)
	}
	if len(found.NextRuns) != scheduleForecastRunCount {
		t.Fatalf("next runs = %d, want %d: %+v", len(found.NextRuns), scheduleForecastRunCount, found.NextRuns)
	}
}

package monitor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Net005/JAVBeacon/internal/covers"
	"github.com/Net005/JAVBeacon/internal/domain"
	"github.com/Net005/JAVBeacon/internal/scraper"
	"github.com/Net005/JAVBeacon/internal/screenshots"
	"github.com/Net005/JAVBeacon/internal/store"
)

type Service struct {
	store       store.Store
	akiba       *scraper.Akiba
	javlibrary  *scraper.JavLibrary
	covers      *covers.Cache
	screenshots *screenshots.Cache
	pages       int
	log         *slog.Logger
	mu          sync.RWMutex
	job         domain.Job
	queue       []RefreshOptions
	worker      bool
	cancel      context.CancelFunc
	details     map[int64]domain.Job
	// releaseJobs tracks every release-scoped job currently in flight via
	// StartRelease, keyed by release ID - see StartRelease's doc comment for
	// why these run independently of queue/worker/cancel above instead of
	// going through the single-flight scan queue. Each entry's job field is
	// mutated only while s.mu is held (mirroring the job/setJob pattern
	// used by the scan worker), so concurrent reads via Status/
	// StatusForRelease are always consistent.
	releaseJobs map[int64]*releaseJobEntry
	listeners   []func(domain.Release)
	// scheduleNextAttempt tracks, per interval-configured scrapeSchedule.mode,
	// the wall-clock time runScrapeSchedules's loop will next actually check
	// whether that schedule is due - kept live (updated every loop iteration,
	// not just when a scan fires) so ScheduleForecast can report an accurate
	// "next run" without re-deriving the scheduler's own phase/anchor
	// separately, which could drift out of sync with the real running loop.
	// Calendar-configured schedules (a start_time/weekdays or cron override
	// set) are not tracked here - ScheduleForecast simulates those directly
	// with calendarScheduleMatches instead, which needs no running-loop state.
	scheduleNextAttempt map[string]time.Time
	// scrapeFallback is the interval Quick refresh and New releases only
	// scan on when their own "refresh_interval"/"new_release_refresh_interval"
	// setting is unset or invalid - the same value ScheduleScrapes is started
	// with (app.cfg.RefreshEvery) - kept here too so ScheduleForecast can
	// report the schedule's real effective interval instead of always
	// assuming a fallback of 0, which previously made an unset interval
	// display as "Every 0s · Next run not yet known" even though the actual
	// running scheduler loop was ticking along fine on this fallback.
	scrapeFallback time.Duration
}

type RefreshOptions struct {
	SiteID    int64
	ReleaseID int64
	Title     string
	Mode      string
	Pages     int
	AllPages  bool
	Scheduled bool
	// Kind identifies which configurable scrape-job operation
	// this request represents, for priority-default lookup. Left empty, it
	// is inferred from the other fields (see StartOptions) so existing
	// callers keep working without change.
	Kind string
	// Priority is the resolved queue priority once StartOptions has run;
	// callers may also set it themselves as an explicit one-off override,
	// in which case it takes precedence over the configured Kind default.
	Priority int
}

// Job priority kinds. Lower priority values run before higher ones (see
// enqueueRefresh); each has a built-in default that a matching
// "job_priority_<kind>" setting can override.
const (
	PriorityKindManualFull         = "manual_full"
	PriorityKindScheduled          = "scheduled"
	PriorityKindScheduledFull      = "scheduled_full"
	PriorityKindScheduledNew       = "scheduled_new"
	PriorityKindScheduledQuick     = "scheduled_quick"
	PriorityKindStartSource        = "start_source"
	PriorityKindSiteRefresh        = "site_refresh"
	PriorityKindUpdateDetails      = "update_details"
	PriorityKindScreenshotBackfill = "screenshot_backfill"
	// PriorityKindScheduledSiteGroup labels a site-group schedule's queued/
	// running jobs for job-history and log purposes. It has no settings-
	// driven default (not listed in JobPriorityKinds, so Settings never
	// exposes a "job_priority_scheduled_site_group" field) because a
	// site-group schedule's priority always comes from its own configured
	// Priority via resolvePriority's override argument - see
	// expandSiteGroupSchedules.
	PriorityKindScheduledSiteGroup = "scheduled_site_group"
)

var priorityKindDefaults = map[string]int{
	PriorityKindManualFull:         20,
	PriorityKindScheduled:          15,
	PriorityKindScheduledFull:      17,
	PriorityKindScheduledNew:       15,
	PriorityKindScheduledQuick:     16,
	PriorityKindStartSource:        10,
	PriorityKindSiteRefresh:        8,
	PriorityKindUpdateDetails:      5,
	PriorityKindScreenshotBackfill: 75,
}

// JobPriorityKinds lists the known priority kinds for building the Settings UI
// and validating requests.
var JobPriorityKinds = []string{PriorityKindManualFull, PriorityKindScheduledFull, PriorityKindScheduledNew, PriorityKindScheduledQuick, PriorityKindStartSource, PriorityKindSiteRefresh, PriorityKindUpdateDetails, PriorityKindScreenshotBackfill}

// JobPrioritySettingKey returns the settings key holding the configured
// default priority for kind, e.g. "job_priority_manual_full".
func JobPrioritySettingKey(kind string) string { return "job_priority_" + kind }

func jobPriorityDefault(kind string) int {
	if v, ok := priorityKindDefaults[kind]; ok {
		return v
	}
	return priorityKindDefaults[PriorityKindSiteRefresh]
}

// resolvePriority is the single shared priority mechanism every entry point
// (manual full-page scrape, scheduled scrape, start source scraping,
// Monitoring Sites manual refresh, and Release Details Update Details) goes
// through, so priority handling is not duplicated per action. An explicit
// override always wins; otherwise the configured default for kind is read
// from settings, falling back to the built-in default when unset or
// invalid.
func (s *Service) resolvePriority(ctx context.Context, kind string, override int) int {
	if override != 0 {
		return override
	}
	def := jobPriorityDefault(kind)
	settings, e := s.store.Settings(ctx)
	if e != nil {
		return def
	}
	raw, ok := settings[JobPrioritySettingKey(kind)]
	if !ok {
		return def
	}
	v, e := strconv.Atoi(strings.TrimSpace(raw))
	if e != nil || v < 1 || v > 999 {
		return def
	}
	return v
}

func releaseHasSite(release domain.Release, siteID int64) bool {
	if release.SiteID == siteID {
		return true
	}
	for _, id := range release.SiteIDs {
		if id == siteID {
			return true
		}
	}
	return false
}

func New(s store.Store, a *scraper.Akiba, j *scraper.JavLibrary, covers *covers.Cache, pages int, l *slog.Logger, scrapeFallback time.Duration, screenshotCaches ...*screenshots.Cache) *Service {
	service := &Service{store: s, akiba: a, javlibrary: j, covers: covers, pages: pages, log: l, details: map[int64]domain.Job{}, scheduleNextAttempt: map[string]time.Time{}, scrapeFallback: scrapeFallback}
	if len(screenshotCaches) > 0 {
		service.screenshots = screenshotCaches[0]
	}
	return service
}

// activeReleaseJobsLocked returns a stable-ordered (oldest first) snapshot
// of every release job currently tracked in s.releaseJobs. Callers must
// already hold s.mu (read or write).
func (s *Service) activeReleaseJobsLocked() []domain.Job {
	if len(s.releaseJobs) == 0 {
		return nil
	}
	jobs := make([]domain.Job, 0, len(s.releaseJobs))
	for _, entry := range s.releaseJobs {
		jobs = append(jobs, entry.job)
	}
	sort.Slice(jobs, func(i, j int) bool { return jobs[i].StartedAt.Before(jobs[j].StartedAt) })
	return jobs
}

func (s *Service) Status() domain.Job {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var job domain.Job
	if s.job.State == "" {
		job = domain.Job{State: "idle"}
	} else {
		job = s.job
		job.QueueDepth = len(s.queue)
		job.QueuedJobs = make([]domain.QueuedJob, 0, len(s.queue))
		for index, options := range s.queue {
			job.QueuedJobs = append(job.QueuedJobs, domain.QueuedJob{
				Position: index + 1, SiteID: options.SiteID, ReleaseID: options.ReleaseID,
				Title: options.Title, Mode: options.Mode, Priority: refreshPriority(options),
				AllPages: options.AllPages, Scheduled: options.Scheduled,
			})
		}
	}
	// ActiveReleaseJobs surfaces every concurrently-running StartRelease job
	// (see its doc comment) regardless of whether a scan job above is also
	// running - manual "Update details" clicks no longer depend on one.
	job.ActiveReleaseJobs = s.activeReleaseJobsLocked()
	return job
}
func (s *Service) StatusForRelease(releaseID int64) domain.Job {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if entry, ok := s.releaseJobs[releaseID]; ok {
		return entry.job
	}
	if s.job.ReleaseID == releaseID && s.job.State != "" {
		return s.job
	}
	for index, options := range s.queue {
		if options.ReleaseID == releaseID {
			return domain.Job{Kind: "scrape", State: "queued", Mode: options.Mode, Running: true, Priority: refreshPriority(options), QueueDepth: index + 1, ReleaseID: releaseID}
		}
	}
	if job, ok := s.details[releaseID]; ok {
		return job
	}
	return domain.Job{State: "idle", ReleaseID: releaseID}
}
func (s *Service) OnRelease(fn func(domain.Release)) {
	s.mu.Lock()
	s.listeners = append(s.listeners, fn)
	s.mu.Unlock()
}

// releaseJobEntry backs Service.releaseJobs - job is a live progress
// snapshot (see StartRelease/runReleaseJob) and cancel lets Stop() end this
// release's refresh early alongside the scan queue below.
type releaseJobEntry struct {
	job    domain.Job
	cancel context.CancelFunc
}

// StartRelease begins an immediate, independent refresh of one release's
// details (manual "Update details"). Unlike StartOptions below, it never
// queues: every call spawns its own goroutine right away, and any
// contention between it, other concurrent StartRelease calls, and an
// in-progress scan job's own per-item detail fetches is resolved entirely
// by the shared SolverPool's priority-ordered Acquire (see scraper.pool.go)
// rather than by this service. That lets several "Update details" clicks -
// or one alongside a running scheduled scan - actually use multiple idle
// Byparr instances at once, instead of serializing behind the single scan
// worker the checkpoint-based preemption below relies on.
//
// Calling StartRelease again for a release already being refreshed just
// returns its current live status rather than starting a redundant second
// refresh of the same row.
func (s *Service) StartRelease(ctx context.Context, id int64, kind string, priorityOverride int) (domain.Job, error) {
	if priorityOverride < 0 || priorityOverride > 999 {
		return domain.Job{}, errors.New("priority must be between 1 and 999, or 0 to use the configured default")
	}
	if kind == "" {
		kind = PriorityKindUpdateDetails
	}
	priority := s.resolvePriority(ctx, kind, priorityOverride)
	workerContext, cancel := context.WithCancel(context.WithoutCancel(ctx))
	job := domain.Job{Kind: "scrape", State: "running", Mode: "manual", Running: true, StartedAt: time.Now().UTC(), ReleaseID: id, Priority: priority}
	s.mu.Lock()
	if entry, ok := s.releaseJobs[id]; ok {
		s.mu.Unlock()
		cancel()
		return entry.job, nil
	}
	if s.releaseJobs == nil {
		s.releaseJobs = map[int64]*releaseJobEntry{}
	}
	s.releaseJobs[id] = &releaseJobEntry{job: job, cancel: cancel}
	delete(s.details, id)
	s.mu.Unlock()
	s.log.Info("release update job started", "release_id", id, "priority", priority)
	go s.runReleaseJob(workerContext, id, priority, job.StartedAt)
	return job, nil
}

// runReleaseJob is StartRelease's goroutine body. It shares doRefreshRelease
// with the queued path (refreshRelease) and RefreshReleaseNow, but reports
// progress into s.releaseJobs[id] instead of the single shared s.job, and
// always bumps updated_at normally (keepUpdatedAt=false) since a manual
// Update Details is a deliberate, meaningful change - unlike a screenshot
// backfill pass, it should be reflected in "sort by date updated".
func (s *Service) runReleaseJob(ctx context.Context, id int64, priority int, startedAt time.Time) {
	jobStarted := time.Now()
	ctx = scraper.WithSolverPriority(ctx, priority)
	// job reuses StartRelease's already-published StartedAt (rather than
	// taking its own time.Now() snapshot) so a caller polling
	// StatusForRelease sees a stable StartedAt across the whole job, instead
	// of it silently shifting forward by a few microseconds the moment this
	// goroutine's first report() call overwrites s.releaseJobs[id].
	job := domain.Job{Kind: "scrape", State: "running", Mode: "manual", Running: true, StartedAt: startedAt, ReleaseID: id, Priority: priority}
	report := func() {
		s.mu.Lock()
		if entry, ok := s.releaseJobs[id]; ok {
			entry.job = job
		}
		s.mu.Unlock()
	}
	s.doRefreshRelease(ctx, id, &job, false, report)
	job.Running = false
	job.FinishedAt = time.Now().UTC()
	if errors.Is(ctx.Err(), context.Canceled) {
		job.State = "cancelled"
		job.Error = "cancelled by user"
	} else if job.Error != "" {
		job.State = "failed"
	} else {
		job.State = "completed"
	}
	_, _ = s.store.SaveJob(context.Background(), job)
	s.mu.Lock()
	s.details[id] = job
	delete(s.releaseJobs, id)
	s.mu.Unlock()
	s.log.Info("release update job completed", "release_id", id, "state", job.State, "outcome", job.Outcome, "error", job.Error, "duration", time.Since(jobStarted).Round(time.Millisecond))
}

// SolverPoolEnabledCount reports how many Byparr/FlareSolverr instances are
// currently configured and enabled, for callers outside this package that
// need to size their own concurrent work against the pool (the screenshot
// backfill's worker pool in internal/web/server.go) without reaching into
// the JavLibrary scraper directly.
func (s *Service) SolverPoolEnabledCount() int {
	return s.javlibrary.Pool().EnabledCount()
}

func (s *Service) ApplySettings(ctx context.Context) {
	if settings, e := s.store.Settings(ctx); e == nil {
		s.javlibrary.Configure(byparrInstancesFromSettings(settings))
	}
}

// byparrInstancesFromSettings parses the byparr_instances/flaresolverr_cooldown
// settings pair into the form JavLibrary.Configure wants, shared by
// ApplySettings and the per-job re-Configure in run().
func byparrInstancesFromSettings(settings map[string]string) ([]scraper.Instance, time.Duration) {
	cooldown, _ := strconv.ParseFloat(settings["flaresolverr_cooldown"], 64)
	return scraper.ParseInstances(settings["byparr_instances"]), time.Duration(cooldown * float64(time.Second))
}

// capForSchedule returns the configured byparr_max_instances_<mode> setting
// for a quick/full/new-releases scan, or 0 ("no cap") for any other mode or
// an unset/invalid value.
func capForSchedule(settings map[string]string, mode string) int {
	if mode != "quick" && mode != "full" && mode != "new" {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(settings["byparr_max_instances_"+mode]))
	if err != nil || n < 0 {
		return 0
	}
	return n
}
func (s *Service) Start(ctx context.Context, siteID int64) error {
	return s.StartOptions(ctx, RefreshOptions{SiteID: siteID, Mode: "quick"})
}
func (s *Service) StartOptions(ctx context.Context, options RefreshOptions) error {
	if options.Priority < 0 || options.Priority > 999 {
		return errors.New("priority must be between 1 and 999, or 0 to use the configured default")
	}
	if options.Mode == "" {
		options.Mode = "quick"
	}
	if options.Kind == "" {
		switch {
		case options.ReleaseID != 0:
			options.Kind = PriorityKindUpdateDetails
		case options.Scheduled && options.Mode == "full":
			options.Kind = PriorityKindScheduledFull
		case options.Scheduled && options.Mode == "new":
			options.Kind = PriorityKindScheduledNew
		case options.Scheduled:
			options.Kind = PriorityKindScheduledQuick
		case options.AllPages:
			options.Kind = PriorityKindManualFull
		default:
			options.Kind = PriorityKindSiteRefresh
		}
	}
	options.Priority = s.resolvePriority(ctx, options.Kind, options.Priority)
	if options.Title == "" {
		switch {
		case options.ReleaseID != 0:
			if release, err := s.store.Release(ctx, options.ReleaseID); err == nil {
				options.Title = "Release " + release.VideoID
			} else {
				options.Title = fmt.Sprintf("Release #%d", options.ReleaseID)
			}
		case options.SiteID == 0:
			options.Title = "All enabled sites"
		default:
			options.Title = fmt.Sprintf("Site #%d", options.SiteID)
			if sites, err := s.store.Sites(ctx); err == nil {
				for _, site := range sites {
					if site.ID == options.SiteID {
						options.Title = site.Title
						break
					}
				}
			}
		}
	}
	s.mu.Lock()
	if options.ReleaseID != 0 {
		delete(s.details, options.ReleaseID)
	}
	if s.worker {
		if options.Scheduled {
			for _, queued := range s.queue {
				if queued.Scheduled && queued.SiteID == options.SiteID && queued.Mode == options.Mode {
					s.mu.Unlock()
					s.log.Info("duplicate scheduled refresh already queued", "site_id", options.SiteID, "mode", options.Mode)
					return nil
				}
			}
		}
		position := len(s.queue) + 1
		for index, queued := range s.queue {
			if queuePriority(options) > queuePriority(queued) {
				position = index + 1
				break
			}
		}
		s.queue = enqueueRefresh(s.queue, options)
		s.job.QueueDepth = len(s.queue)
		depth := len(s.queue)
		s.mu.Unlock()
		s.log.Info("refresh job queued", "site_id", options.SiteID, "release_id", options.ReleaseID, "mode", options.Mode, "priority", refreshPriority(options), "queue_position", position, "queue_depth", depth)
		return nil
	}
	s.worker = true
	workerContext, cancel := context.WithCancel(context.WithoutCancel(ctx))
	s.cancel = cancel
	s.job = newRefreshJob(options, 0)
	s.mu.Unlock()
	go s.runQueue(workerContext, options)
	return nil
}

// Stop cancels the active scan job (if any) and clears its queue, and also
// cancels every StartRelease job currently in flight (see releaseJobs) -
// those run independently of the scan worker, so stopping "the" job needs
// to reach them too for a single Stop control to actually halt everything
// the Jobs page shows as active. Returns the number of queued *scan* jobs
// cleared (unchanged meaning from before release jobs existed) and whether
// anything was actually running to stop.
func (s *Service) Stop() (int, bool) {
	s.mu.Lock()
	cleared := len(s.queue)
	var releaseCancels []context.CancelFunc
	for _, entry := range s.releaseJobs {
		releaseCancels = append(releaseCancels, entry.cancel)
	}
	stoppingScan := s.worker
	var scanCancel context.CancelFunc
	if stoppingScan {
		s.queue = nil
		scanCancel = s.cancel
		s.job.State = "stopping"
		s.job.QueueDepth = 0
	}
	s.mu.Unlock()
	if scanCancel != nil {
		scanCancel()
	}
	for _, c := range releaseCancels {
		c()
	}
	stopped := stoppingScan || len(releaseCancels) > 0
	if stopped {
		s.log.Info("scrape job stop requested", "cleared_queued_jobs", cleared, "cancelled_release_jobs", len(releaseCancels))
	}
	return cleared, stopped
}

// refreshPriority returns options' effective queue priority. StartOptions
// resolves this (via resolvePriority) before a job is enqueued or run, so
// this is normally just an accessor.
func refreshPriority(options RefreshOptions) int {
	return options.Priority
}

// queuePriority gives every scrape entry point the same lower-number-first
// ordering shown in Settings (including manual jobs and scheduled scans).
func queuePriority(options RefreshOptions) int {
	return -refreshPriority(options)
}

func enqueueRefresh(queue []RefreshOptions, options RefreshOptions) []RefreshOptions {
	priority := queuePriority(options)
	index := len(queue)
	for i, queued := range queue {
		if priority > queuePriority(queued) {
			index = i
			break
		}
	}
	queue = append(queue, RefreshOptions{})
	copy(queue[index+1:], queue[index:])
	queue[index] = options
	return queue
}

func newRefreshJob(options RefreshOptions, queued int) domain.Job {
	return domain.Job{Kind: "scrape", Title: options.Title, Scheduled: options.Scheduled, State: "queued", Mode: options.Mode, Running: true, StartedAt: time.Now().UTC(), AllPages: options.AllPages, Priority: refreshPriority(options), QueueDepth: queued, ReleaseID: options.ReleaseID}
}

func (s *Service) setJob(job domain.Job) {
	s.mu.Lock()
	job.QueueDepth = len(s.queue)
	s.job = job
	s.mu.Unlock()
}

func (s *Service) runQueue(ctx context.Context, options RefreshOptions) {
	for {
		s.run(ctx, options)
		s.mu.Lock()
		if len(s.queue) == 0 {
			s.worker = false
			s.cancel = nil
			s.job.Running = false
			s.job.QueueDepth = 0
			s.mu.Unlock()
			return
		}
		options = s.queue[0]
		s.queue = s.queue[1:]
		s.job = newRefreshJob(options, len(s.queue))
		s.mu.Unlock()
		s.log.Info("queued refresh job promoted", "site_id", options.SiteID, "release_id", options.ReleaseID, "mode", options.Mode, "priority", refreshPriority(options), "queue_remaining", s.Status().QueueDepth)
	}
}

// checkpoint is called from run() at points where pausing is safe - between
// one release's detail-page fetch finishing and the next one starting, and
// between sites in a multi-site scan - to let a higher-priority job that was
// queued while this one was running go ahead of it. Site-monitor jobs can
// touch many pages/releases and run for a while, so without this a job like
// "update this one release's details" or "new releases only" queued behind
// a long-running full scan would otherwise have to wait for the whole scan
// to finish first.
//
// It runs synchronously on the same goroutine as the paused job: as long as
// the queue's head outranks myPriority, it pops that entry and runs it via
// run() itself (so a preempting job can in turn be preempted by an even
// higher-priority job through its own nested checkpoint call), then loops
// to re-check. Because nothing else touches s.queue's head or calls run()
// concurrently, only one job is ever actually scraping at a time; the
// caller's job/options/ctx (including whatever the scraper was mid-fetch
// of) simply sit parked on this call stack until checkpoint returns, so
// resuming continues exactly where the caller left off - no restart, no
// lost progress. job.Paused/PausedFor are set for the duration so the UI
// can show the pause; they're always cleared again before returning.
func (s *Service) checkpoint(ctx context.Context, job *domain.Job, myPriority int) {
	for {
		if ctx.Err() != nil {
			return
		}
		s.mu.Lock()
		if len(s.queue) == 0 || refreshPriority(s.queue[0]) >= myPriority {
			s.mu.Unlock()
			return
		}
		next := s.queue[0]
		s.queue = s.queue[1:]
		s.mu.Unlock()

		pausedFor := next.Title
		if pausedFor == "" {
			pausedFor = next.Mode
		}
		job.Paused, job.PausedFor = true, pausedFor
		s.setJob(*job)
		s.log.Info("scrape job paused for higher-priority job", "priority", myPriority, "paused_for", pausedFor, "preempting_priority", refreshPriority(next))

		s.run(ctx, next)

		job.Paused, job.PausedFor = false, ""
		s.setJob(*job)
		s.log.Info("scrape job resumed", "priority", myPriority)
	}
}

func (s *Service) run(ctx context.Context, options RefreshOptions) {
	jobStarted := time.Now()
	job := s.Status()
	job.State = "running"
	job.StartedAt = jobStarted.UTC()
	job.Title = options.Title
	job.Scheduled = options.Scheduled
	defer func() {
		job.Running = false
		job.FinishedAt = time.Now().UTC()
		if errors.Is(ctx.Err(), context.Canceled) {
			job.State = "cancelled"
			job.Error = "cancelled by user"
		} else if job.Error != "" {
			job.State = "failed"
		} else {
			job.State = "completed"
		}
		_, _ = s.store.SaveJob(context.Background(), job)
		s.mu.Lock()
		if options.ReleaseID != 0 {
			s.details[options.ReleaseID] = job
		}
		job.QueueDepth = len(s.queue)
		job.Running = len(s.queue) > 0
		s.job = job
		s.mu.Unlock()
		s.log.Info("refresh job completed", "site_id", options.SiteID, "release_id", options.ReleaseID, "state", job.State, "priority", refreshPriority(options), "queued", job.QueueDepth, "added", job.Added, "updated", job.Updated, "skipped", job.Skipped, "error", job.Error, "duration", time.Since(jobStarted).Round(time.Millisecond))
	}()
	if options.ReleaseID != 0 {
		s.refreshRelease(ctx, options.ReleaseID, &job)
		return
	}
	sites, e := s.store.Sites(ctx)
	if e != nil {
		job.Error = e.Error()
		return
	}
	pages := options.Pages
	if options.AllPages {
		// Zero is the scraper's explicit "through the online end" value. The
		// provider's reported last page plus empty/repeated-page detection are
		// the termination guards; there is no arbitrary numeric ceiling.
		pages = 0
	} else if pages <= 0 {
		pages = s.pages
	}
	// javConcurrency/akibaConcurrency bound how many detail-page fetches this
	// job's scan loop runs at once for each provider (see
	// scraper.ScrapeConcurrency). JavLibrary defaults to using every enabled
	// Byparr pool instance (capped by the configured
	// byparr_max_instances_<mode> setting, if any); with no solver
	// configured at all, it stays at 1 - concurrent *direct* requests to
	// JavLibrary (bypassing Byparr entirely) is not something this feature
	// is about and is likely to get blocked. GIGA has no solver pool to size
	// against, so it only goes concurrent when the operator explicitly
	// configures a cap greater than 1 for this schedule type; unset/0 keeps
	// today's one-at-a-time behavior.
	javConcurrency, akibaConcurrency := 1, 1
	if settings, err := s.store.Settings(ctx); err == nil {
		if n, err := strconv.Atoi(settings["page_limit"]); options.Pages <= 0 && !options.AllPages && err == nil && n > 0 {
			pages = n
		}
		s.javlibrary.Configure(byparrInstancesFromSettings(settings))
		cap := capForSchedule(settings, options.Mode)
		if enabled := s.javlibrary.Pool().EnabledCount(); enabled > 0 {
			javConcurrency = enabled
			if cap > 0 && cap < javConcurrency {
				javConcurrency = cap
			}
		}
		if cap > 0 {
			akibaConcurrency = cap
		}
	}
	// Every detail-page fetch this job makes (including nested checkpoint
	// preemptions, which call s.run with this same ctx) carries this job's
	// resolved priority through to SolverPool.Acquire, so a manual
	// "Update details" job's requests jump ahead of a lower-priority
	// scheduled scan's when both are contending for the same pool.
	ctx = scraper.WithSolverPriority(ctx, options.Priority)
	// scanSites is the ordered subset of sites this job actually scans -
	// resolved once, up front, so job.SiteCount (and each site's
	// job.SiteIndex, set below) reports a real "site N of total" figure for
	// the whole job's remaining work instead of an estimate that could
	// drift if a site's enabled flag changed mid-run. Left unset (both stay
	// zero) for a single-site job (options.SiteID != 0, e.g. a manual
	// per-site refresh or a release-scoped job never reaches here) - see
	// domain.Job.SiteCount's doc comment.
	scanSites := make([]domain.Site, 0, len(sites))
	for _, site := range sites {
		if !site.Enabled || (options.SiteID != 0 && site.ID != options.SiteID) {
			continue
		}
		scanSites = append(scanSites, site)
	}
	if options.SiteID == 0 {
		job.SiteCount = len(scanSites)
	}
	s.log.Info("refresh job started", "site_id", options.SiteID, "sites_available", len(scanSites), "page_limit", pages, "mode", options.Mode)
	for i, site := range scanSites {
		s.checkpoint(ctx, &job, options.Priority)
		if ctx.Err() != nil {
			return
		}
		siteStarted := time.Now()
		s.log.Info("site scrape started", "site", site.Title, "site_id", site.ID, "type", site.Type, "provider", site.Name, "url", site.URL, "page_limit", pages)
		if options.SiteID == 0 {
			job.SiteIndex = i + 1
		}
		job.SiteTitle, job.Provider = site.Title, site.Name
		job.Page, job.PageLimit, job.Item, job.PageItems, job.Remaining, job.VideoID, job.Error = 0, pages, 0, 0, 0, "", ""
		sitePages, siteAdded, siteUpdated := 0, 0, 0
		// A site's first completed page scrape establishes its baseline only.
		// Automatic future monitoring is eligible from the following scrape,
		// using the newest release date that existed before this run began.
		baselineDate, hasBaseline, baselineErr := s.store.LatestReleaseDateForSite(ctx, site.ID)
		baselineRunUsable := site.LastScrapeState == "completed" || site.LastScrapeState == "partial"
		futureMonitoringReady := site.AutoMonitorFuture && !site.LastScrapedAt.IsZero() && site.LastScrapePages >= 1 && baselineRunUsable && hasBaseline
		if baselineErr != nil {
			futureMonitoringReady = false
			s.log.Warn("future release monitoring baseline unavailable", "site", site.Title, "site_id", site.ID, "error", baselineErr)
		}
		recordSiteScrape := func(state string) {
			if recordErr := s.store.RecordSiteScrape(context.Background(), site.ID, time.Now().UTC(), sitePages, siteAdded, siteUpdated, state); recordErr != nil {
				s.log.Warn("site scrape summary could not be saved", "site", site.Title, "site_id", site.ID, "state", state, "error", recordErr)
			}
		}
		if options.AllPages {
			job.PageLimitSource = "discovering online end"
		} else {
			job.PageLimitSource = "configured limit"
		}
		s.setJob(job)
		progress := func(page, pageLimit, item, pageItems int, videoID string) {
			// item is 1-based and this fires just before that item's detail
			// page is fetched (see scrapeFiltered in the scraper package), so
			// item > 0 here means the previous item's (item-1's) detail page
			// finished. That's the checkpoint: pause here, before starting
			// the next detail fetch, never mid-fetch.
			if item > 0 {
				s.checkpoint(ctx, &job, options.Priority)
			}
			job.Page, job.PageLimit, job.Item, job.PageItems = page, pageLimit, item, pageItems
			sitePages = max(sitePages, page)
			job.Remaining, job.VideoID = max(pageItems-item, 0), videoID
			switch {
			case item == 0 && pageItems == 0:
				job.PageLimitSource = "online end"
			case options.AllPages && pageLimit > 0:
				job.PageLimitSource = "online max found"
			case !options.AllPages && pageLimit < pages:
				job.PageLimitSource = "online max found"
			case options.AllPages:
				job.PageLimitSource = "discovering online end"
			default:
				job.PageLimitSource = "configured limit"
			}
			s.setJob(job)
			s.log.Info("scrape progress", "site", site.Title, "provider", site.Name, "page", fmt.Sprintf("%d/%d", page, pageLimit), "page_limit_source", job.PageLimitSource, "item", fmt.Sprintf("%d/%d", item, pageItems), "remaining", job.Remaining, "video_id", videoID)
		}
		var items []domain.Release
		var include func(string) bool
		if options.Mode == "new" {
			include = func(videoID string) bool {
				exists, checkErr := s.store.ReleaseExistsForSite(ctx, site.ID, site.Name, videoID)
				if checkErr != nil {
					job.Error = checkErr.Error()
					return false
				}
				if exists {
					job.Skipped++
					return false
				}
				return true
			}
		}
		// concurrency.Checkpoint gives a higher-priority job a chance to
		// preempt between batches of concurrent detail fetches - the same
		// role the per-item progress callback's checkpoint call plays for
		// the fast, sequential listing-page parse above.
		scanCheckpoint := func() { s.checkpoint(ctx, &job, options.Priority) }
		// detailFailures/detailFailureSample surface what used to be a
		// silent per-item WARN log ("product detail failed"): a release
		// whose detail-page fetch fails still gets added/"updated" from
		// listing-page data alone (title/cover only - no Label, Studio,
		// Genres, release date, or screenshots), so a scan job could
		// report a normal-looking "N updated" while a struggling solver
		// (Byparr/FlareSolverr overloaded, misconfigured for concurrent
		// use, etc.) meant none of those releases actually got refreshed.
		// Guarded by detailFailuresMu since OnDetailFailure fires from the
		// concurrent detail-fetch goroutines in fetchDetailsConcurrently.
		var detailFailuresMu sync.Mutex
		var detailFailures int
		var detailFailureSample []string
		onDetailFailure := func(videoID string, _ error) {
			detailFailuresMu.Lock()
			detailFailures++
			if len(detailFailureSample) < 5 {
				detailFailureSample = append(detailFailureSample, videoID)
			}
			detailFailuresMu.Unlock()
		}
		switch {
		case strings.EqualFold(site.Name, "GIGA"):
			concurrency := scraper.ScrapeConcurrency{Max: akibaConcurrency, Checkpoint: scanCheckpoint, OnDetailFailure: onDetailFailure}
			if options.AllPages {
				items, e = s.akiba.ScrapeFilteredThroughEnd(ctx, pages, include, concurrency, progress)
			} else {
				items, e = s.akiba.ScrapeFiltered(ctx, pages, include, concurrency, progress)
			}
		case strings.EqualFold(site.Name, "JavLibrary"):
			concurrency := scraper.ScrapeConcurrency{Max: javConcurrency, Checkpoint: scanCheckpoint, OnDetailFailure: onDetailFailure}
			if options.AllPages {
				items, e = s.javlibrary.ScrapeFilteredThroughEnd(ctx, site.URL, pages, include, concurrency, progress)
			} else {
				items, e = s.javlibrary.ScrapeFiltered(ctx, site.URL, pages, include, concurrency, progress)
			}
		default:
			s.log.Warn("unsupported scraper skipped", "site", site.Title, "scraper", site.Name)
			continue
		}
		if e != nil {
			job.Error = e.Error()
			s.log.Error("refresh failed", "site", site.Title, "error", e)
			if ctx.Err() != nil {
				recordSiteScrape("cancelled")
				return
			}
			recordSiteScrape("failed")
			continue
		}
		if detailFailures > 0 {
			summary := fmt.Sprintf("%d of %d release detail pages could not be fetched this run (e.g. %s) - those releases were added/updated from listing-page data only, so Label/Studio/Genres/screenshots were not refreshed for them; check the solver (Byparr/FlareSolverr) and server logs", detailFailures, len(items), strings.Join(detailFailureSample, ", "))
			s.log.Warn("release detail pages failed during scan", "site", site.Title, "provider", site.Name, "failed", detailFailures, "total", len(items), "sample", detailFailureSample)
			if job.Error == "" {
				job.Error = summary
			}
		}
		s.log.Info("site scrape returned releases", "site", site.Title, "provider", site.Name, "releases", len(items), "duration", time.Since(siteStarted).Round(time.Millisecond))
		for _, r := range items {
			// Same checkpoint as the scraper-side progress callback above, but
			// for this second pass: ScrapeFiltered/ScrapeFilteredThroughEnd
			// already returned every item for this site (detail pages fully
			// fetched), and this loop is what actually downloads each item's
			// cover/screenshots and writes it to the store. That can still be
			// slow for a page full of new releases with several screenshots
			// each, so it needs its own between-releases pause point too -
			// otherwise a higher-priority job queued mid-scrape would have to
			// wait out this entire write/download pass before getting a turn.
			s.checkpoint(ctx, &job, options.Priority)
			if ctx.Err() != nil {
				return
			}
			r.SiteID = site.ID
			knownForSite, knownErr := s.store.ReleaseExistsForSite(ctx, site.ID, r.Source, r.VideoID)
			if knownErr != nil {
				job.Error = knownErr.Error()
				s.log.Warn("release/site association check failed", "site", site.Title, "video_id", r.VideoID, "error", knownErr)
			}
			newForSite := knownErr == nil && !knownForSite
			if newForSite && site.Notify {
				r.NotifyOnRelease = true
			}
			if shouldAutoMonitorFutureRelease(futureMonitoringReady, newForSite, baselineDate, r.ReleaseDate) {
				r.MonitorDownload = true
				r.MonitorReason = "site_future"
				r.MonitorSiteID = site.ID
			}
			if options.ReleaseID != 0 {
				existing, e := s.store.Release(ctx, options.ReleaseID)
				if e != nil || !strings.EqualFold(existing.VideoID, r.VideoID) {
					continue
				}
			}
			// Quick refresh (see StartOptions/Schedule) never re-processes a
			// release it already has on file for this site - it only ever
			// adds releases it hasn't seen before. Any other mode (chiefly
			// "full", used by ScheduleFull and the manual "Full Refresh"
			// option) falls through to the UpsertRelease call below, which
			// updates an existing release in place - that is the entire
			// mechanism behind Full refresh's "adds and updates" behavior.
			//
			// This used to be gated on r.ReleaseDate/old.ReleaseDate both being
			// non-empty, which meant a scraped item with no parsed release
			// date (or an existing row that had never had one recorded) fell
			// straight through to UpsertRelease below and got updated anyway
			// - the exact "Quick Refresh keeps updating existing releases"
			// bug report. The existence check is now unconditional, matching
			// how Mode=="new"'s include() filter above decides the same
			// question before ever fetching a detail page.
			if options.Mode == "quick" {
				existingRelease, exists, existsErr := s.store.ReleaseForSite(ctx, site.ID, site.Name, r.VideoID)
				if existsErr != nil {
					job.Error = existsErr.Error()
				} else if exists {
					if r.ImageURL != "" {
						if _, changed, coverErr := s.covers.Refresh(ctx, r.VideoID, r.ImageURL); coverErr != nil {
							s.log.Warn("release cover refresh failed", "site", site.Title, "provider", site.Name, "video_id", r.VideoID, "image_url", r.ImageURL, "error", coverErr)
						} else if changed {
							s.log.Info("existing release cover updated", "site", site.Title, "provider", site.Name, "mode", options.Mode, "video_id", r.VideoID)
						}
					}
					// Quick refresh still never overwrites a metadata field the
					// release already has - see the "Quick Refresh keeps updating
					// existing releases" precedent above - but it has already
					// fetched the detail page, so it now backfills whichever of
					// these fields are still blank (most commonly Label, on a
					// release added before JavLibrary's Label parsing existed)
					// instead of leaving them blank forever until a manual Update
					// Details or a Full refresh happens to reach the same release.
					// Each field is only set on fill when existingRelease's own
					// value is empty; UpsertReleaseKeepUpdatedAt's
					// selective scalar/relationship updates then leave every other
					// value exactly as it already was. Screenshots are always
					// added/repaired unconditionally to match the current page -
					// a missing screenshot is an asset gap, not a metadata
					// judgment call - and the cover above is likewise always
					// refreshed when JavLibrary now shows a different one.
					// preserveUpdatedAt (below) keeps all of this, backfill
					// included, from turning Quick into a full metadata refresh
					// for "sort by date updated" purposes.
					fill := domain.Release{SiteID: site.ID, VideoID: r.VideoID, Source: r.Source, Screenshots: r.Screenshots}
					if existingRelease.Label == "" {
						fill.Label = r.Label
					}
					if existingRelease.Studio == "" {
						fill.Studio = r.Studio
					}
					if existingRelease.Director == "" {
						fill.Director = r.Director
					}
					if existingRelease.Actress == "" {
						fill.Actress = r.Actress
						fill.Actresses = r.Actresses
					}
					if existingRelease.ReleaseDate == "" {
						fill.ReleaseDate = r.ReleaseDate
					}
					if existingRelease.Duration == "" {
						fill.Duration = r.Duration
					}
					if existingRelease.Story == "" {
						fill.Story = r.Story
					}
					if len(existingRelease.Genres) == 0 {
						fill.Genres = r.Genres
					}
					if s.screenshots != nil && len(r.Screenshots) > 0 {
						_, _, failed, screenshotErr := s.screenshots.EnsureAll(ctx, r.VideoID, r.Screenshots)
						if screenshotErr != nil {
							s.log.Warn("release screenshot cache incomplete", "site", site.Title, "video_id", r.VideoID, "failed", failed, "error", screenshotErr)
						}
					}
					if _, fillErr := s.store.UpsertReleaseKeepUpdatedAt(ctx, fill); fillErr != nil {
						job.Error = fillErr.Error()
						s.log.Error("release metadata backfill could not be saved", "site", site.Title, "provider", site.Name, "video_id", r.VideoID, "error", fillErr)
					}
					job.Skipped++
					s.log.Info("quick refresh backfilled existing release", "site", site.Title, "provider", site.Name, "video_id", r.VideoID)
					continue
				}
			}
			if r.ImageURL != "" {
				var cached bool
				var coverErr error
				if options.Mode == "full" || options.Mode == "quick" {
					_, cached, coverErr = s.covers.Refresh(ctx, r.VideoID, r.ImageURL)
				} else {
					_, cached, coverErr = s.covers.Ensure(ctx, r.VideoID, r.ImageURL)
				}
				if coverErr != nil {
					s.log.Warn("cover cache failed", "site", site.Title, "provider", site.Name, "video_id", r.VideoID, "image_url", r.ImageURL, "error", coverErr)
				} else if cached {
					s.log.Debug("release cover downloaded", "site", site.Title, "video_id", r.VideoID)
				}
			}
			if s.screenshots != nil && len(r.Screenshots) > 0 {
				_, _, failed, screenshotErr := s.screenshots.EnsureAll(ctx, r.VideoID, r.Screenshots)
				if screenshotErr != nil {
					s.log.Warn("release screenshot cache incomplete", "site", site.Title, "video_id", r.VideoID, "failed", failed, "error", screenshotErr)
				}
			}
			created, e := s.store.UpsertRelease(ctx, r)
			if e != nil {
				job.Error = e.Error()
				s.log.Error("release upsert failed", "site", site.Title, "provider", site.Name, "video_id", r.VideoID, "error", e)
				continue
			}
			if created {
				job.Added++
				siteAdded++
				s.log.Info("release added", "site", site.Title, "provider", site.Name, "mode", options.Mode, "video_id", r.VideoID)
				if site.Watchlist {
					releaseID := int64(0)
					if savedRows, findErr := s.store.Releases(ctx, domain.ReleaseFilter{Search: r.VideoID, Limit: 10}); findErr == nil {
						for _, saved := range savedRows {
							if releaseHasSite(saved, site.ID) && strings.EqualFold(saved.VideoID, r.VideoID) {
								releaseID = saved.ID
								break
							}
						}
					}
					value := true
					if releaseID == 0 {
						job.Error = "new release could not be reloaded for Watchlist marking"
						s.log.Error("future release Watchlist marking failed", "site", site.Title, "video_id", r.VideoID, "error", job.Error)
					} else if patchErr := s.store.PatchRelease(ctx, releaseID, nil, nil, nil, nil, &value, nil, nil, nil); patchErr != nil {
						job.Error = patchErr.Error()
						s.log.Error("future release Watchlist marking failed", "site", site.Title, "release_id", releaseID, "video_id", r.VideoID, "error", patchErr)
					} else {
						s.log.Info("new release added to Watchlist by site rule", "site", site.Title, "release_id", releaseID, "video_id", r.VideoID, "future_only", true)
					}
				}
			} else {
				job.Updated++
				siteUpdated++
				s.log.Info("release updated", "site", site.Title, "provider", site.Name, "mode", options.Mode, "video_id", r.VideoID)
			}
			if newForSite && r.MonitorDownload && r.MonitorReason == "site_future" {
				s.log.Info("new release enrolled in monitoring by site rule", "site", site.Title, "site_id", site.ID, "video_id", r.VideoID, "release_date", r.ReleaseDate, "baseline_date", baselineDate)
			}
			s.mu.RLock()
			listeners := append([]func(domain.Release){}, s.listeners...)
			s.mu.RUnlock()
			if len(listeners) > 0 {
				if saved, err := s.store.Releases(ctx, domain.ReleaseFilter{Search: r.VideoID, Limit: 10}); err == nil {
					for _, item := range saved {
						if releaseHasSite(item, site.ID) && strings.EqualFold(item.VideoID, r.VideoID) {
							for _, listener := range listeners {
								listener(item)
							}
							break
						}
					}
				}
			}
		}
		siteState := "completed"
		if job.Error != "" {
			siteState = "partial"
		}
		recordSiteScrape(siteState)
		s.log.Info("site refresh completed", "site", site.Title, "provider", site.Name, "added", siteAdded, "updated", siteUpdated, "duration", time.Since(siteStarted).Round(time.Millisecond))
	}
}

func shouldAutoMonitorFutureRelease(ready, newForSite bool, baselineDate, releaseDate string) bool {
	return ready && newForSite && baselineDate != "" && releaseDate != "" && releaseDate >= baselineDate
}

// refreshRelease is the queued path: manual "Update details" and any other
// ReleaseID-scoped job (RefreshOptions.ReleaseID != 0) reaches here via
// run(), on the single worker goroutine, exactly as before - it updates the
// shared s.job status (via the report callback) so the Jobs page's "ACTIVE
// SCRAPE" panel and per-release polling reflect its live progress.
func (s *Service) refreshRelease(ctx context.Context, id int64, job *domain.Job) {
	s.doRefreshRelease(ctx, id, job, false, func() { s.setJob(*job) })
}

// RefreshReleaseNow refreshes one release's full details immediately,
// bypassing the single global job queue/worker entirely - safe to call
// concurrently from multiple goroutines (this is what the screenshot
// backfill's concurrent worker pool uses), since unlike refreshRelease it
// touches no shared job/queue/s.job state, only the shared solver pool
// (already concurrency-safe on its own) and this one release's independent
// DB row. Always preserves the release's updated_at (see
// UpsertReleaseKeepUpdatedAt) so a backfill run that merely confirms or
// repairs screenshots never pollutes "sort by date updated." ctx is
// wrapped with the screenshot-backfill priority kind so its solver-pool
// requests still fairly lose out to a manual Update Details or a real
// scan's requests when contending for the same instances.
func (s *Service) RefreshReleaseNow(ctx context.Context, id int64) domain.Job {
	ctx = scraper.WithSolverPriority(ctx, s.resolvePriority(ctx, PriorityKindScreenshotBackfill, 0))
	job := domain.Job{Kind: "scrape", State: "running", Mode: "screenshots", Running: true, StartedAt: time.Now().UTC(), ReleaseID: id}
	s.doRefreshRelease(ctx, id, &job, true, nil)
	job.Running = false
	job.FinishedAt = time.Now().UTC()
	if job.Error != "" {
		job.State = "failed"
	} else {
		job.State = "completed"
	}
	return job
}

// doRefreshRelease holds the actual scrape-refresh-and-save logic shared by
// refreshRelease (queued, single-worker path) and RefreshReleaseNow
// (direct, concurrency-safe path). report, when non-nil, is called after
// every job field mutation so a caller tracking this refresh through the
// shared job-status system (refreshRelease's use) sees live progress;
// RefreshReleaseNow passes nil since it has nothing shared to publish into
// until it returns the finished job.Whether keepUpdatedAt is set decides
// both which Store.UpsertRelease variant persists the result and whether a
// screenshot-cache failure is treated as a hard job failure - both were
// keyed off job.Mode == "screenshots" before this was split into two
// callers, and keepUpdatedAt is true for exactly the same case (the
// backfill path) that Mode == "screenshots" used to identify.
func (s *Service) doRefreshRelease(ctx context.Context, id int64, job *domain.Job, keepUpdatedAt bool, report func()) {
	setJob := func() {
		if report != nil {
			report()
		}
	}
	existing, err := s.store.Release(ctx, id)
	if err != nil {
		job.Error = err.Error()
		job.Outcome = "failed"
		return
	}
	job.SiteTitle, job.Provider, job.VideoID = existing.SiteTitle, existing.Source, existing.VideoID
	job.Page, job.PageLimit, job.Item, job.PageItems = 1, 1, 1, 1
	job.Stage = scraper.StageConnecting
	setJob()
	// stage pushes a live progress update for this job's poller (Phase 12)
	// each time the scraper reaches a new, genuinely distinguishable point
	// in the refresh - see scraper.DetailStage's doc comment for exactly
	// what each name covers and why.
	stage := func(name string) { job.Stage = name; setJob() }
	s.log.Info("release detail refresh started", "release_id", id, "video_id", existing.VideoID, "provider", existing.Source, "url", existing.ProductURL)
	var updated domain.Release
	switch {
	case strings.EqualFold(existing.Source, "GIGA"):
		updated, err = s.akiba.Refresh(ctx, existing, stage)
	case strings.EqualFold(existing.Source, "JavLibrary"):
		updated, err = s.javlibrary.Refresh(ctx, existing, stage)
	default:
		err = fmt.Errorf("unsupported release provider %q", existing.Source)
	}
	if err != nil {
		job.Error = err.Error()
		job.Outcome = "failed"
		var statusErr *scraper.StatusError
		if errors.As(err, &statusErr) {
			switch statusErr.Status {
			case scraper.ScrapeBlocked:
				job.Outcome = "blocked"
			case scraper.ScrapeInvalid:
				job.Outcome = "invalid"
			}
		}
		s.log.Error("release detail refresh failed", "release_id", id, "video_id", existing.VideoID, "outcome", job.Outcome, "error", err)
		return
	}
	stage("comparing")
	if updated.ImageURL != "" {
		if _, _, coverErr := s.covers.Refresh(ctx, updated.VideoID, updated.ImageURL); coverErr != nil {
			s.log.Warn("release cover refresh failed", "release_id", id, "video_id", existing.VideoID, "error", coverErr)
		}
	}
	if s.screenshots != nil && len(updated.Screenshots) > 0 {
		_, _, failed, screenshotErr := s.screenshots.EnsureAll(ctx, updated.VideoID, updated.Screenshots)
		if screenshotErr != nil {
			s.log.Warn("release screenshot cache incomplete", "release_id", id, "video_id", existing.VideoID, "failed", failed, "error", screenshotErr)
			if keepUpdatedAt {
				job.Error = screenshotErr.Error()
				job.Outcome = "failed"
				return
			}
		}
	}
	stage("updating")
	upsert := s.store.UpsertRelease
	if keepUpdatedAt {
		upsert = s.store.UpsertReleaseKeepUpdatedAt
	}
	if _, err = upsert(ctx, updated); err != nil {
		job.Error = err.Error()
		job.Outcome = "failed"
		return
	}
	job.Updated = 1
	// Compare against the release as re-read from the store, not the
	// in-memory `updated` value: UpsertRelease normalizes text (HTML entity
	// decoding, tag stripping, whitespace collapsing - see cleanText in
	// internal/store), so a raw-scraped-vs-normalized-stored comparison
	// would spuriously report "updated" on every refresh even when nothing
	// actually changed. Comparing two store-normalized reads (existing vs
	// saved) is the only apples-to-apples comparison.
	saved, err := s.store.Release(ctx, id)
	if err != nil {
		job.Error = err.Error()
		job.Outcome = "failed"
		return
	}
	if releaseDetailChanged(existing, saved) {
		job.Outcome = "updated"
	} else {
		job.Outcome = "no_change"
	}
	stage("completed")
	s.emit(saved)
	s.log.Info("release detail refresh completed", "release_id", id, "video_id", existing.VideoID, "outcome", job.Outcome)
}

// releaseDetailChanged reports whether a fresh Update Details scrape
// actually turned up anything different from what was already stored, so
// the job's final Outcome (Phase 12) can distinguish "updated" from "no new
// information found" instead of always claiming success. It only compares
// the fields a detail-page Refresh can populate (see mergeJav/merge in the
// scraper package); fields Refresh never touches (Watchlist, Notified, local
// state, and so on) are intentionally excluded. Callers should pass two
// store-normalized reads (see refreshRelease) rather than comparing a raw
// scrape result against a stored value.
func releaseDetailChanged(before, after domain.Release) bool {
	if before.Title != after.Title ||
		before.ImageURL != after.ImageURL ||
		before.ReleaseDate != after.ReleaseDate ||
		before.Duration != after.Duration ||
		before.Director != after.Director ||
		before.Actress != after.Actress ||
		before.Studio != after.Studio ||
		before.Story != after.Story ||
		before.Released != after.Released {
		return true
	}
	if !slices.Equal(before.Genres, after.Genres) {
		return true
	}
	if !slices.Equal(before.Screenshots, after.Screenshots) {
		return true
	}
	return false
}

func (s *Service) emit(release domain.Release) {
	s.mu.RLock()
	listeners := append([]func(domain.Release){}, s.listeners...)
	s.mu.RUnlock()
	for _, listener := range listeners {
		listener(release)
	}
}

// scheduleMaxSleepChunk bounds how long Schedule/ScheduleFull's loop ever
// sleeps in one stretch before waking up to re-read current settings, so a
// shortened interval, or a flipped quick_refresh_enabled/full_refresh_enabled
// flag, takes effect within this window instead of only once whatever wait
// was already in progress finishes on its own - previously a single
// time.NewTimer(wait) could not be interrupted early, so e.g. lowering a
// 24h interval to 15m only actually took effect up to 24h later, which is
// indistinguishable from "requires a restart" to the user. A package var
// (not a const) purely so tests can shrink it instead of actually waiting
// out the real-world default.
var scheduleMaxSleepChunk = 30 * time.Second

type scrapeSchedule struct {
	// id uniquely identifies this schedule for every internal per-schedule
	// map (lastAttempt, lastCalendarMinute, basicNext, basicSignature,
	// s.scheduleNextAttempt) - the three built-in schedules default it to
	// their mode (quick/full/new), preserving those maps' existing keys
	// exactly. A site-group schedule (see expandSiteGroupSchedules) sets it
	// to something unique per group+site instead, since several of those
	// can share the same mode (e.g. two different named schedules both
	// scraping the same site in "quick" mode) - using mode as the map key
	// for those would silently merge their independent timing state.
	id              string
	mode            string
	title           string
	enabledKey      string
	scheduleModeKey string
	intervalKey     string
	startTimeKey    string
	weekdaysKey     string
	cronKey         string
	pagesKey        string
	priorityKind    string
	fallback        time.Duration
	defaultEnabled  bool
	// siteID scopes this schedule to one monitoring site (site-group
	// schedules only - see expandSiteGroupSchedules); zero means "every
	// enabled site", matching the three built-in schedules' existing
	// behavior.
	siteID int64
	// siteGroupScheduleID links per-site synthetic schedules back to their
	// parent Settings card so the UI can show one live next-three-runs
	// forecast for the whole named group.
	siteGroupScheduleID int64
	// priorityOverride, when non-zero, is passed as resolvePriority's
	// override argument (which always short-circuits its settings lookup -
	// see resolvePriority's doc comment) instead of 0, so a site-group
	// schedule's configured priority is used verbatim rather than falling
	// through to a job_priority_* setting. Zero for the three built-ins,
	// which keep using their configured/default priorityKind lookup.
	priorityOverride int
}

type dueScrape struct {
	options  RefreshOptions
	priority int
}

// quickScrapeSchedule, fullScrapeSchedule, and newReleaseScrapeSchedule are
// the single source of truth for each configurable scrape schedule's
// settings keys - shared by Schedule/ScheduleFull/ScheduleNew/
// ScheduleScrapes (which actually run them) and ScheduleForecast (which
// predicts their next run times), so the two can never disagree about which
// setting keys or defaults a given schedule uses.
func quickScrapeSchedule(fallback time.Duration) scrapeSchedule {
	return scrapeSchedule{id: "quick", mode: "quick", title: "Quick refresh · all enabled sites", enabledKey: "quick_refresh_enabled", scheduleModeKey: "quick_refresh_schedule_mode", intervalKey: "refresh_interval", startTimeKey: "quick_refresh_start_time", weekdaysKey: "quick_refresh_weekdays", cronKey: "quick_refresh_cron", priorityKind: PriorityKindScheduledQuick, fallback: fallback, defaultEnabled: true}
}
func fullScrapeSchedule(fallback time.Duration) scrapeSchedule {
	return scrapeSchedule{id: "full", mode: "full", title: "Full refresh · all enabled sites", enabledKey: "full_refresh_enabled", scheduleModeKey: "full_refresh_schedule_mode", intervalKey: "full_refresh_interval", startTimeKey: "full_refresh_start_time", weekdaysKey: "full_refresh_weekdays", cronKey: "full_refresh_cron", pagesKey: "full_refresh_page_limit", priorityKind: PriorityKindScheduledFull, fallback: fallback}
}
func newReleaseScrapeSchedule(fallback time.Duration) scrapeSchedule {
	return scrapeSchedule{id: "new", mode: "new", title: "New releases only · all enabled sites", enabledKey: "new_release_refresh_enabled", scheduleModeKey: "new_release_refresh_schedule_mode", intervalKey: "new_release_refresh_interval", startTimeKey: "new_release_refresh_start_time", weekdaysKey: "new_release_refresh_weekdays", cronKey: "new_release_refresh_cron", pagesKey: "new_release_refresh_page_limit", priorityKind: PriorityKindScheduledNew, fallback: fallback, defaultEnabled: true}
}

// siteGroupSchedulePrefix + groupID + ":" + siteID is both the synthetic
// schedule id expandSiteGroupSchedules assigns each site-group×site pair
// and the prefix for that pair's synthetic settings keys, so the two can
// never drift apart.
const siteGroupSchedulePrefix = "sitegroup:"

// expandSiteGroupSchedules parses the site_group_schedules setting and
// turns every enabled group's every configured site into one synthetic
// scrapeSchedule, so runScrapeSchedules/ScheduleForecast's existing,
// already-in-production timing logic (calendarScheduleMatches/
// nextBasicRun/nextAdvancedRuns/nextCalendarRuns/normalizeScheduleMode)
// handles them exactly like the three built-in schedules, with zero
// special-casing: each synthetic schedule's enabledKey/scheduleModeKey/
// intervalKey/etc simply point at synthetic keys written into augmented,
// a copy-on-write clone of settings (so the common case of zero group
// schedules costs nothing beyond the ParseSiteGroupSchedules call). A
// group's site referencing a monitoring site that no longer exists is
// silently skipped rather than erroring, since the site may have been
// deleted after the schedule was configured.
func (s *Service) expandSiteGroupSchedules(ctx context.Context, settings map[string]string) (map[string]string, []scrapeSchedule) {
	groups := domain.ParseSiteGroupSchedules(settings["site_group_schedules"])
	if len(groups) == 0 {
		return settings, nil
	}
	sites, err := s.store.Sites(ctx)
	if err != nil {
		s.log.Error("site group schedules could not be expanded", "error", err)
		return settings, nil
	}
	siteTitles := make(map[int64]string, len(sites))
	for _, site := range sites {
		siteTitles[site.ID] = site.Title
	}
	augmented := make(map[string]string, len(settings))
	for k, v := range settings {
		augmented[k] = v
	}
	var extra []scrapeSchedule
	for _, group := range groups {
		if !group.Enabled {
			continue
		}
		for _, groupSite := range group.Sites {
			siteTitle, known := siteTitles[groupSite.SiteID]
			if !known {
				continue
			}
			mode := groupSite.Mode
			if mode != "quick" && mode != "full" && mode != "new" {
				continue
			}
			id := fmt.Sprintf("%s%d:%d", siteGroupSchedulePrefix, group.ID, groupSite.SiteID)
			prefix := id + ":"
			augmented[prefix+"enabled"] = "true"
			augmented[prefix+"schedule_mode"] = group.ScheduleMode
			augmented[prefix+"interval"] = group.Interval
			augmented[prefix+"start_time"] = group.StartTime
			augmented[prefix+"weekdays"] = group.Weekdays
			augmented[prefix+"cron"] = group.Cron
			extra = append(extra, scrapeSchedule{
				id:                  id,
				mode:                mode,
				title:               group.Name + " · " + siteTitle,
				enabledKey:          prefix + "enabled",
				scheduleModeKey:     prefix + "schedule_mode",
				intervalKey:         prefix + "interval",
				startTimeKey:        prefix + "start_time",
				weekdaysKey:         prefix + "weekdays",
				cronKey:             prefix + "cron",
				priorityKind:        PriorityKindScheduledSiteGroup,
				fallback:            time.Hour,
				defaultEnabled:      true,
				siteID:              groupSite.SiteID,
				siteGroupScheduleID: group.ID,
				priorityOverride:    group.Priority,
			})
			if group.Pages > 0 {
				augmented[prefix+"pages"] = strconv.Itoa(group.Pages)
				extra[len(extra)-1].pagesKey = prefix + "pages"
			}
		}
	}
	return augmented, extra
}

// runScrapeSchedules is the one coordinator for scheduled scrape scans. A
// single loop is important here: when multiple schedules become due together,
// it resolves all priorities first and enqueues them lowest-number-first. The
// Service's existing single refresh worker then guarantees they execute one at
// a time, in queue order, rather than three timer goroutines racing to start.
func (s *Service) runScrapeSchedules(ctx context.Context, schedules []scrapeSchedule) {
	lastAttempt := make(map[string]time.Time, len(schedules))
	lastCalendarMinute := make(map[string]string, len(schedules))
	basicNext := make(map[string]time.Time, len(schedules))
	basicSignature := make(map[string]string, len(schedules))
	for {
		settings, _ := s.store.Settings(ctx)
		settings, groupSchedules := s.expandSiteGroupSchedules(ctx, settings)
		allSchedules := schedules
		if len(groupSchedules) > 0 {
			allSchedules = append(append([]scrapeSchedule{}, schedules...), groupSchedules...)
		}
		now := time.Now()
		due := make([]dueScrape, 0, len(allSchedules))
		nextSleep := scheduleMaxSleepChunk
		for _, schedule := range allSchedules {
			// A schedule literal that leaves id unset (as every caller did
			// before id existed - see quickScrapeSchedule and friends, and
			// this package's own tests that build scrapeSchedule literals
			// directly) still behaves exactly as before: id defaults to
			// mode, which is what every per-schedule map below used as its
			// key prior to expandSiteGroupSchedules needing a key that can
			// be unique even when several schedules share the same mode.
			if schedule.id == "" {
				schedule.id = schedule.mode
			}
			enabled := schedule.defaultEnabled
			if raw, ok := settings[schedule.enabledKey]; ok {
				enabled = raw == "true"
			}
			interval := schedule.fallback
			if parsed, err := domain.ParseScheduleDuration(settings[schedule.intervalKey]); err == nil && parsed >= time.Minute {
				interval = parsed
			}
			if interval <= 0 {
				continue
			}
			mode := normalizeScheduleMode(settings[schedule.scheduleModeKey], settings[schedule.startTimeKey], settings[schedule.weekdaysKey], settings[schedule.cronKey])
			queue := func() {
				if !enabled {
					return
				}
				pages := 0
				if schedule.pagesKey != "" {
					pages, _ = strconv.Atoi(settings[schedule.pagesKey])
					if pages <= 0 {
						pages = s.pages
					}
				}
				priority := s.resolvePriority(ctx, schedule.priorityKind, schedule.priorityOverride)
				due = append(due, dueScrape{priority: priority, options: RefreshOptions{SiteID: schedule.siteID, Mode: schedule.mode, Title: schedule.title, Pages: pages, Scheduled: true, Kind: schedule.priorityKind, Priority: priority}})
			}
			if mode == "cron" || mode == "advanced" {
				minuteKey := now.Format("200601021504")
				cronText := ""
				if mode == "cron" {
					cronText = settings[schedule.cronKey]
				}
				matches, err := calendarScheduleMatches(now, settings[schedule.startTimeKey], settings[schedule.weekdaysKey], cronText)
				if err != nil {
					s.log.Error("invalid scheduled scrape timing", "schedule", schedule.id, "mode", schedule.mode, "error", err)
				} else if matches && lastCalendarMinute[schedule.id] != minuteKey {
					lastCalendarMinute[schedule.id] = minuteKey
					if mode == "cron" || lastAttempt[schedule.id].IsZero() || now.Sub(lastAttempt[schedule.id]) >= interval {
						lastAttempt[schedule.id] = now
						queue()
					}
				}
				continue
			}
			signature := interval.String() + "|" + strings.TrimSpace(settings[schedule.startTimeKey])
			if basicSignature[schedule.id] != signature || basicNext[schedule.id].IsZero() {
				basicSignature[schedule.id] = signature
				basicNext[schedule.id] = nextBasicRun(now, interval, settings[schedule.startTimeKey])
			}
			if !now.Before(basicNext[schedule.id]) {
				queue()
				for !basicNext[schedule.id].After(now) {
					basicNext[schedule.id] = basicNext[schedule.id].Add(interval)
				}
			}
			remaining := basicNext[schedule.id].Sub(now)
			s.mu.Lock()
			if s.scheduleNextAttempt == nil {
				s.scheduleNextAttempt = map[string]time.Time{}
			}
			s.scheduleNextAttempt[schedule.id] = basicNext[schedule.id]
			s.mu.Unlock()
			if remaining < nextSleep {
				nextSleep = remaining
			}
		}
		slices.SortStableFunc(due, func(a, b dueScrape) int {
			switch {
			case a.priority < b.priority:
				return -1
			case a.priority > b.priority:
				return 1
			default:
				return 0
			}
		})
		for _, scan := range due {
			if err := s.StartOptions(ctx, scan.options); err != nil {
				s.log.Error("scheduled scrape could not be queued", "mode", scan.options.Mode, "priority", scan.priority, "error", err)
			}
		}
		if nextSleep <= 0 || nextSleep > scheduleMaxSleepChunk {
			nextSleep = scheduleMaxSleepChunk
		}
		timer := time.NewTimer(nextSleep)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

// Schedule runs the Quick refresh scheduled scan: it only ever adds
// releases it hasn't seen before for a site, and never re-scrapes or
// updates a release already in the database (see the Mode=="quick" check
// in run()). Its own interval comes from the "refresh_interval" setting,
// falling back to every when unset/invalid, and it can be disabled
// entirely via "quick_refresh_enabled" (absent/unset means enabled, since
// Quick refresh has always run by default - unlike Full refresh below,
// which is opt-in).
func (s *Service) Schedule(ctx context.Context, every time.Duration) {
	if every <= 0 {
		return
	}
	s.runScrapeSchedules(ctx, []scrapeSchedule{quickScrapeSchedule(every)})
}

// ScheduleFull runs the Full refresh scheduled scan: a second, independent
// schedule from Quick refresh's Schedule above, using its own
// "full_refresh_interval" and "full_refresh_page_limit" settings, that both
// adds new releases and updates every existing release found on the pages
// it scans (Mode: "full" - see the Mode=="quick" check in run(), which
// "full" simply does not match, so nothing is skipped and every scraped
// item is upserted). Disabled by default: it only actually starts a scan
// when "full_refresh_enabled" is "true", since re-scraping every existing
// release is considerably heavier than Quick refresh and worth an explicit
// opt-in. every is the fallback interval used until a valid
// "full_refresh_interval" setting is configured.
func (s *Service) ScheduleFull(ctx context.Context, every time.Duration) {
	if every <= 0 {
		return
	}
	s.runScrapeSchedules(ctx, []scrapeSchedule{fullScrapeSchedule(every)})
}

func (s *Service) ScheduleNew(ctx context.Context, every time.Duration) {
	if every <= 0 {
		return
	}
	s.runScrapeSchedules(ctx, []scrapeSchedule{newReleaseScrapeSchedule(every)})
}

// ScheduleScrapes runs all three configurable scrape schedules through one
// coordinator so due scans are ordered by their per-schedule priorities before
// any of them reaches the shared serial worker.
func (s *Service) ScheduleScrapes(ctx context.Context, quickEvery time.Duration) {
	if quickEvery <= 0 {
		return
	}
	s.runScrapeSchedules(ctx, []scrapeSchedule{
		fullScrapeSchedule(defaultFullRefreshFallback),
		newReleaseScrapeSchedule(quickEvery),
		quickScrapeSchedule(quickEvery),
	})
}

// scheduleForecastRunCount is how many upcoming run times ScheduleForecast
// predicts per schedule.
const scheduleForecastRunCount = 3

// defaultFullRefreshFallback is the full-refresh schedule's built-in interval
// when its own setting isn't configured - shared by ScheduleScrapes (the real
// running scheduler) and ScheduleForecast (its display), so the two can never
// disagree about what "no interval configured" actually falls back to.
const defaultFullRefreshFallback = 24 * time.Hour

// ScheduleForecast reports each configurable scrape schedule's current
// enabled/interval state plus its next scheduleForecastRunCount predicted
// run times. Interval-mode schedules read the live scheduleNextAttempt time
// the running scheduler loop last computed (so it can never drift from what
// the loop will actually do) and extrapolate the remaining runs by adding
// the interval repeatedly; calendar-mode schedules (a cron expression or a
// start-time/weekdays override configured) are simulated directly with
// nextCalendarRuns instead, since the running loop doesn't track those with
// scheduleNextAttempt. A disabled schedule is reported with an empty
// NextRuns, even though the loop may still be internally tracking a next
// check time for it.
func (s *Service) ScheduleForecast(ctx context.Context) []domain.ScheduleForecast {
	settings, _ := s.store.Settings(ctx)
	settings, groupSchedules := s.expandSiteGroupSchedules(ctx, settings)
	now := time.Now()
	schedules := []scrapeSchedule{
		quickScrapeSchedule(s.scrapeFallback),
		newReleaseScrapeSchedule(s.scrapeFallback),
		fullScrapeSchedule(defaultFullRefreshFallback),
	}
	forecasts := make([]domain.ScheduleForecast, 0, len(schedules)+len(groupSchedules))
	allSchedules := append(append([]scrapeSchedule{}, schedules...), groupSchedules...)
	for _, schedule := range allSchedules {
		if schedule.id == "" {
			schedule.id = schedule.mode
		}
		enabled := schedule.defaultEnabled
		if raw, ok := settings[schedule.enabledKey]; ok {
			enabled = raw == "true"
		}
		group := "Scheduled scrapes"
		if strings.HasPrefix(schedule.id, siteGroupSchedulePrefix) {
			group = "Site group schedules"
		}
		forecast := domain.ScheduleForecast{Group: group, Name: schedule.title, Enabled: enabled, SiteGroupScheduleID: schedule.siteGroupScheduleID}
		interval := schedule.fallback
		if parsed, err := domain.ParseScheduleDuration(settings[schedule.intervalKey]); err == nil && parsed >= time.Minute {
			interval = parsed
		}
		mode := normalizeScheduleMode(settings[schedule.scheduleModeKey], settings[schedule.startTimeKey], settings[schedule.weekdaysKey], settings[schedule.cronKey])
		if mode == "cron" {
			forecast.Interval = "cron: " + strings.TrimSpace(settings[schedule.cronKey])
			if enabled {
				forecast.NextRuns = nextCalendarRuns(now, "", "", settings[schedule.cronKey], scheduleForecastRunCount)
			}
			forecasts = append(forecasts, forecast)
			continue
		}
		if mode == "advanced" {
			forecast.Interval = "advanced: " + interval.String()
			if enabled {
				forecast.NextRuns = nextAdvancedRuns(now, settings[schedule.startTimeKey], settings[schedule.weekdaysKey], interval, scheduleForecastRunCount)
			}
			forecasts = append(forecasts, forecast)
			continue
		}
		forecast.Interval = "basic: " + interval.String()
		if enabled && interval > 0 {
			s.mu.RLock()
			next, tracked := s.scheduleNextAttempt[schedule.id]
			s.mu.RUnlock()
			if tracked {
				runs := make([]time.Time, 0, scheduleForecastRunCount)
				for len(runs) < scheduleForecastRunCount {
					runs = append(runs, next)
					next = next.Add(interval)
				}
				forecast.NextRuns = runs
			} else {
				next := nextBasicRun(now, interval, settings[schedule.startTimeKey])
				for len(forecast.NextRuns) < scheduleForecastRunCount {
					forecast.NextRuns = append(forecast.NextRuns, next)
					next = next.Add(interval)
				}
			}
		}
		forecasts = append(forecasts, forecast)
	}
	return forecasts
}

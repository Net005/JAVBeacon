package monitor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Net005/JAVBeacon/internal/covers"
	"github.com/Net005/JAVBeacon/internal/domain"
	"github.com/Net005/JAVBeacon/internal/scraper"
	"github.com/Net005/JAVBeacon/internal/store"
)

type Service struct {
	store      store.Store
	akiba      *scraper.Akiba
	javlibrary *scraper.JavLibrary
	covers     *covers.Cache
	pages      int
	log        *slog.Logger
	mu         sync.RWMutex
	job        domain.Job
	queue      []RefreshOptions
	worker     bool
	cancel     context.CancelFunc
	details    map[int64]domain.Job
	listeners  []func(domain.Release)
}

type RefreshOptions struct {
	SiteID    int64
	ReleaseID int64
	Title     string
	Mode      string
	Pages     int
	AllPages  bool
	Scheduled bool
	// Kind identifies which of the five configurable scrape-job operations
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
	PriorityKindManualFull     = "manual_full"
	PriorityKindScheduled      = "scheduled"
	PriorityKindScheduledFull  = "scheduled_full"
	PriorityKindScheduledNew   = "scheduled_new"
	PriorityKindScheduledQuick = "scheduled_quick"
	PriorityKindStartSource    = "start_source"
	PriorityKindSiteRefresh    = "site_refresh"
	PriorityKindUpdateDetails  = "update_details"
)

var priorityKindDefaults = map[string]int{
	PriorityKindManualFull:     20,
	PriorityKindScheduled:      15,
	PriorityKindScheduledFull:  17,
	PriorityKindScheduledNew:   15,
	PriorityKindScheduledQuick: 16,
	PriorityKindStartSource:    10,
	PriorityKindSiteRefresh:    8,
	PriorityKindUpdateDetails:  5,
}

// JobPriorityKinds lists the known priority kinds for building the Settings UI
// and validating requests.
var JobPriorityKinds = []string{PriorityKindManualFull, PriorityKindScheduledFull, PriorityKindScheduledNew, PriorityKindScheduledQuick, PriorityKindStartSource, PriorityKindSiteRefresh, PriorityKindUpdateDetails}

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

func New(s store.Store, a *scraper.Akiba, j *scraper.JavLibrary, covers *covers.Cache, pages int, l *slog.Logger) *Service {
	return &Service{store: s, akiba: a, javlibrary: j, covers: covers, pages: pages, log: l, details: map[int64]domain.Job{}}
}
func (s *Service) Status() domain.Job {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.job.State == "" {
		return domain.Job{State: "idle"}
	}
	job := s.job
	job.QueueDepth = len(s.queue)
	job.QueuedJobs = make([]domain.QueuedJob, 0, len(s.queue))
	for index, options := range s.queue {
		job.QueuedJobs = append(job.QueuedJobs, domain.QueuedJob{
			Position: index + 1, SiteID: options.SiteID, ReleaseID: options.ReleaseID,
			Title: options.Title, Mode: options.Mode, Priority: refreshPriority(options),
			AllPages: options.AllPages, Scheduled: options.Scheduled,
		})
	}
	return job
}
func (s *Service) StatusForRelease(releaseID int64) domain.Job {
	s.mu.RLock()
	defer s.mu.RUnlock()
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
func (s *Service) ApplySettings(ctx context.Context) {
	if settings, e := s.store.Settings(ctx); e == nil {
		cooldown, _ := strconv.ParseFloat(settings["flaresolverr_cooldown"], 64)
		s.javlibrary.Configure(settings["flaresolverr_url"], cooldown)
	}
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

func (s *Service) Stop() (int, bool) {
	s.mu.Lock()
	if !s.worker {
		s.mu.Unlock()
		return 0, false
	}
	cleared := len(s.queue)
	s.queue = nil
	cancel := s.cancel
	s.job.State = "stopping"
	s.job.QueueDepth = 0
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	s.log.Info("scrape job stop requested", "release_id", s.Status().ReleaseID, "cleared_queued_jobs", cleared)
	return cleared, true
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
	return domain.Job{Kind: "scrape", State: "queued", Mode: options.Mode, Running: true, StartedAt: time.Now().UTC(), AllPages: options.AllPages, Priority: refreshPriority(options), QueueDepth: queued, ReleaseID: options.ReleaseID}
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

func (s *Service) run(ctx context.Context, options RefreshOptions) {
	jobStarted := time.Now()
	job := s.Status()
	job.State = "running"
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
	if pages <= 0 {
		pages = s.pages
	}
	if options.AllPages {
		pages = 500
	}
	if settings, err := s.store.Settings(ctx); err == nil {
		if n, err := strconv.Atoi(settings["page_limit"]); options.Pages <= 0 && !options.AllPages && err == nil && n > 0 {
			pages = n
		}
		cooldown, _ := strconv.ParseFloat(settings["flaresolverr_cooldown"], 64)
		s.javlibrary.Configure(settings["flaresolverr_url"], cooldown)
	}
	s.log.Info("refresh job started", "site_id", options.SiteID, "sites_available", len(sites), "page_limit", pages, "mode", options.Mode)
	for _, site := range sites {
		if !site.Enabled || (options.SiteID != 0 && site.ID != options.SiteID) {
			continue
		}
		siteStarted := time.Now()
		s.log.Info("site scrape started", "site", site.Title, "site_id", site.ID, "type", site.Type, "provider", site.Name, "url", site.URL, "page_limit", pages)
		job.SiteTitle, job.Provider = site.Title, site.Name
		job.Page, job.PageLimit, job.Item, job.PageItems, job.Remaining, job.VideoID, job.Error = 0, pages, 0, 0, 0, "", ""
		sitePages, siteAdded, siteUpdated := 0, 0, 0
		recordSiteScrape := func(state string) {
			if recordErr := s.store.RecordSiteScrape(context.Background(), site.ID, time.Now().UTC(), sitePages, siteAdded, siteUpdated, state); recordErr != nil {
				s.log.Warn("site scrape summary could not be saved", "site", site.Title, "site_id", site.ID, "state", state, "error", recordErr)
			}
		}
		if options.AllPages {
			job.PageLimitSource = "safety limit"
		} else {
			job.PageLimitSource = "configured limit"
		}
		s.setJob(job)
		progress := func(page, pageLimit, item, pageItems int, videoID string) {
			job.Page, job.PageLimit, job.Item, job.PageItems = page, pageLimit, item, pageItems
			sitePages = max(sitePages, page)
			job.Remaining, job.VideoID = max(pageItems-item, 0), videoID
			switch {
			case item == 0 && pageItems == 0:
				job.PageLimitSource = "online end"
			case pageLimit < pages:
				job.PageLimitSource = "online max found"
			case options.AllPages:
				job.PageLimitSource = "safety limit"
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
		switch {
		case strings.EqualFold(site.Name, "GIGA"):
			if options.AllPages {
				items, e = s.akiba.ScrapeFilteredThroughEnd(ctx, pages, include, progress)
			} else {
				items, e = s.akiba.ScrapeFiltered(ctx, pages, include, progress)
			}
		case strings.EqualFold(site.Name, "JavLibrary"):
			if options.AllPages {
				items, e = s.javlibrary.ScrapeFilteredThroughEnd(ctx, site.URL, pages, include, progress)
			} else {
				items, e = s.javlibrary.ScrapeFiltered(ctx, site.URL, pages, include, progress)
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
		s.log.Info("site scrape returned releases", "site", site.Title, "provider", site.Name, "releases", len(items), "duration", time.Since(siteStarted).Round(time.Millisecond))
		for _, r := range items {
			r.SiteID = site.ID
			r.SiteMonitorDownload = site.DownloadMode == "all"
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
				exists, existsErr := s.store.ReleaseExistsForSite(ctx, site.ID, site.Name, r.VideoID)
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
					job.Skipped++
					s.log.Info("quick refresh skipped existing release metadata", "site", site.Title, "provider", site.Name, "video_id", r.VideoID)
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
				if site.Desired {
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
						job.Error = "new release could not be reloaded for Desired marking"
						s.log.Error("future release Desired marking failed", "site", site.Title, "video_id", r.VideoID, "error", job.Error)
					} else if patchErr := s.store.PatchRelease(ctx, releaseID, nil, nil, nil, nil, &value, nil, nil, nil); patchErr != nil {
						job.Error = patchErr.Error()
						s.log.Error("future release Desired marking failed", "site", site.Title, "release_id", releaseID, "video_id", r.VideoID, "error", patchErr)
					} else {
						s.log.Info("new release marked Desired by site rule", "site", site.Title, "release_id", releaseID, "video_id", r.VideoID, "future_only", true)
					}
				}
			} else {
				job.Updated++
				siteUpdated++
				s.log.Info("release updated", "site", site.Title, "provider", site.Name, "mode", options.Mode, "video_id", r.VideoID)
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

func (s *Service) refreshRelease(ctx context.Context, id int64, job *domain.Job) {
	existing, err := s.store.Release(ctx, id)
	if err != nil {
		job.Error = err.Error()
		job.Outcome = "failed"
		return
	}
	job.SiteTitle, job.Provider, job.VideoID = existing.SiteTitle, existing.Source, existing.VideoID
	job.Page, job.PageLimit, job.Item, job.PageItems = 1, 1, 1, 1
	job.Stage = scraper.StageConnecting
	s.setJob(*job)
	// stage pushes a live progress update for this job's poller (Phase 12)
	// each time the scraper reaches a new, genuinely distinguishable point
	// in the refresh - see scraper.DetailStage's doc comment for exactly
	// what each name covers and why.
	stage := func(name string) { job.Stage = name; s.setJob(*job) }
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
	stage("updating")
	if _, err = s.store.UpsertRelease(ctx, updated); err != nil {
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
// scraper package); fields Refresh never touches (Desired, Notified, local
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
	mode           string
	title          string
	enabledKey     string
	intervalKey    string
	startTimeKey   string
	weekdaysKey    string
	cronKey        string
	pagesKey       string
	priorityKind   string
	fallback       time.Duration
	defaultEnabled bool
}

type dueScrape struct {
	options  RefreshOptions
	priority int
}

// runScrapeSchedules is the one coordinator for scheduled scrape scans. A
// single loop is important here: when multiple schedules become due together,
// it resolves all priorities first and enqueues them lowest-number-first. The
// Service's existing single refresh worker then guarantees they execute one at
// a time, in queue order, rather than three timer goroutines racing to start.
func (s *Service) runScrapeSchedules(ctx context.Context, schedules []scrapeSchedule) {
	lastAttempt := make(map[string]time.Time, len(schedules))
	lastCalendarMinute := make(map[string]string, len(schedules))
	started := time.Now()
	for _, schedule := range schedules {
		lastAttempt[schedule.mode] = started
	}
	for {
		settings, _ := s.store.Settings(ctx)
		now := time.Now()
		due := make([]dueScrape, 0, len(schedules))
		nextSleep := scheduleMaxSleepChunk
		for _, schedule := range schedules {
			enabled := schedule.defaultEnabled
			if raw, ok := settings[schedule.enabledKey]; ok {
				enabled = raw == "true"
			}
			calendarConfigured := strings.TrimSpace(settings[schedule.cronKey]) != "" || strings.TrimSpace(settings[schedule.startTimeKey]) != ""
			if calendarConfigured {
				minuteKey := now.Format("200601021504")
				matches, err := calendarScheduleMatches(now, settings[schedule.startTimeKey], settings[schedule.weekdaysKey], settings[schedule.cronKey])
				if err != nil {
					s.log.Error("invalid scheduled scrape timing", "mode", schedule.mode, "error", err)
				} else if enabled && matches && lastCalendarMinute[schedule.mode] != minuteKey {
					lastCalendarMinute[schedule.mode] = minuteKey
					pages := 0
					if schedule.pagesKey != "" {
						pages, _ = strconv.Atoi(settings[schedule.pagesKey])
						if pages <= 0 {
							pages = s.pages
						}
					}
					priority := s.resolvePriority(ctx, schedule.priorityKind, 0)
					due = append(due, dueScrape{priority: priority, options: RefreshOptions{Mode: schedule.mode, Title: schedule.title, Pages: pages, Scheduled: true, Kind: schedule.priorityKind, Priority: priority}})
				}
				continue
			}
			interval := schedule.fallback
			if parsed, err := time.ParseDuration(settings[schedule.intervalKey]); err == nil && parsed >= time.Minute {
				interval = parsed
			}
			if interval <= 0 {
				continue
			}
			elapsed := now.Sub(lastAttempt[schedule.mode])
			remaining := interval - elapsed
			if remaining <= 0 {
				lastAttempt[schedule.mode] = now
				if enabled {
					pages := 0
					if schedule.pagesKey != "" {
						pages, _ = strconv.Atoi(settings[schedule.pagesKey])
						if pages <= 0 {
							pages = s.pages
						}
					}
					priority := s.resolvePriority(ctx, schedule.priorityKind, 0)
					due = append(due, dueScrape{priority: priority, options: RefreshOptions{Mode: schedule.mode, Title: schedule.title, Pages: pages, Scheduled: true, Kind: schedule.priorityKind, Priority: priority}})
				}
				remaining = interval
			}
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
	s.runScrapeSchedules(ctx, []scrapeSchedule{{mode: "quick", title: "Quick refresh · all enabled sites", enabledKey: "quick_refresh_enabled", intervalKey: "refresh_interval", startTimeKey: "quick_refresh_start_time", weekdaysKey: "quick_refresh_weekdays", cronKey: "quick_refresh_cron", priorityKind: PriorityKindScheduledQuick, fallback: every, defaultEnabled: true}})
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
	s.runScrapeSchedules(ctx, []scrapeSchedule{{mode: "full", title: "Full refresh · all enabled sites", enabledKey: "full_refresh_enabled", intervalKey: "full_refresh_interval", startTimeKey: "full_refresh_start_time", weekdaysKey: "full_refresh_weekdays", cronKey: "full_refresh_cron", pagesKey: "full_refresh_page_limit", priorityKind: PriorityKindScheduledFull, fallback: every}})
}

func (s *Service) ScheduleNew(ctx context.Context, every time.Duration) {
	if every <= 0 {
		return
	}
	s.runScrapeSchedules(ctx, []scrapeSchedule{{mode: "new", title: "New releases only · all enabled sites", enabledKey: "new_release_refresh_enabled", intervalKey: "new_release_refresh_interval", startTimeKey: "new_release_refresh_start_time", weekdaysKey: "new_release_refresh_weekdays", cronKey: "new_release_refresh_cron", pagesKey: "new_release_refresh_page_limit", priorityKind: PriorityKindScheduledNew, fallback: every, defaultEnabled: true}})
}

// ScheduleScrapes runs all three configurable scrape schedules through one
// coordinator so due scans are ordered by their per-schedule priorities before
// any of them reaches the shared serial worker.
func (s *Service) ScheduleScrapes(ctx context.Context, quickEvery time.Duration) {
	if quickEvery <= 0 {
		return
	}
	s.runScrapeSchedules(ctx, []scrapeSchedule{
		{mode: "full", title: "Full refresh · all enabled sites", enabledKey: "full_refresh_enabled", intervalKey: "full_refresh_interval", startTimeKey: "full_refresh_start_time", weekdaysKey: "full_refresh_weekdays", cronKey: "full_refresh_cron", pagesKey: "full_refresh_page_limit", priorityKind: PriorityKindScheduledFull, fallback: 24 * time.Hour},
		{mode: "new", title: "New releases only · all enabled sites", enabledKey: "new_release_refresh_enabled", intervalKey: "new_release_refresh_interval", startTimeKey: "new_release_refresh_start_time", weekdaysKey: "new_release_refresh_weekdays", cronKey: "new_release_refresh_cron", pagesKey: "new_release_refresh_page_limit", priorityKind: PriorityKindScheduledNew, fallback: quickEvery, defaultEnabled: true},
		{mode: "quick", title: "Quick refresh · all enabled sites", enabledKey: "quick_refresh_enabled", intervalKey: "refresh_interval", startTimeKey: "quick_refresh_start_time", weekdaysKey: "quick_refresh_weekdays", cronKey: "quick_refresh_cron", priorityKind: PriorityKindScheduledQuick, fallback: quickEvery, defaultEnabled: true},
	})
}

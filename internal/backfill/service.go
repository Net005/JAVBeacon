package backfill

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Net005/JAVBeacon/internal/domain"
	"github.com/Net005/JAVBeacon/internal/scraper"
	"github.com/Net005/JAVBeacon/internal/store"
)

const DefaultPriority = 500

type historicalScraper interface {
	HistoricalIndexes(context.Context) ([]scraper.HistoricalIndex, error)
	HistoricalPage(context.Context, string, int, func(string) bool, scraper.ScrapeConcurrency) ([]domain.Release, []string, int, error)
	// PoolEnabledCount reports how many configured Byparr/FlareSolverr
	// instances are currently enabled (0 if none configured), so run() can
	// size how many detail pages it fetches concurrently per listing page.
	PoolEnabledCount() int
}

type Status struct {
	Running       bool                           `json:"running"`
	State         string                         `json:"state"`
	Resume        bool                           `json:"resume"`
	Priority      int                            `json:"priority"`
	StartedAt     time.Time                      `json:"started_at,omitempty"`
	FinishedAt    time.Time                      `json:"finished_at,omitempty"`
	SourceKind    string                         `json:"source_kind,omitempty"`
	SourceName    string                         `json:"source_name,omitempty"`
	SourceURL     string                         `json:"source_url,omitempty"`
	SourceIndex   int                            `json:"source_index"`
	SourceCount   int                            `json:"source_count"`
	Page          int                            `json:"page"`
	PageLimit     int                            `json:"page_limit"`
	VideoID       string                         `json:"video_id,omitempty"`
	RunDiscovered int                            `json:"run_discovered"`
	RunCompleted  int                            `json:"run_completed"`
	RunAdded      int                            `json:"run_added"`
	RunUpdated    int                            `json:"run_updated"`
	RunSkipped    int                            `json:"run_skipped"`
	RunFailed     int                            `json:"run_failed"`
	LastError     string                         `json:"last_error,omitempty"`
	Historical    domain.HistoricalBackfillStats `json:"historical"`
}

type Service struct {
	store   store.Store
	scraper historicalScraper
	log     *slog.Logger
	mu      sync.RWMutex
	status  Status
	cancel  context.CancelFunc
}

func New(st store.Store, jav historicalScraper, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	s := &Service{store: st, scraper: jav, log: log, status: Status{State: "idle", Priority: DefaultPriority}}
	if stats, err := st.HistoricalBackfillStats(context.Background()); err == nil {
		if stats.State == "running" {
			stats.State = "interrupted"
			_ = st.SetHistoricalBackfillState(context.Background(), stats.State)
		}
		s.status.State = stats.State
		s.status.Historical = stats
	}
	return s
}

func (s *Service) Status(ctx context.Context) Status {
	stats, err := s.store.HistoricalBackfillStats(ctx)
	s.mu.Lock()
	if err == nil {
		s.status.Historical = stats
	}
	out := s.status
	s.mu.Unlock()
	return out
}

func (s *Service) Start(ctx context.Context, resume bool, priority int) error {
	if priority == 0 {
		priority = DefaultPriority
		if settings, err := s.store.Settings(ctx); err == nil {
			if n, ok := parsePriority(settings["job_priority_historical_backfill"]); ok {
				priority = n
			}
		}
	}
	if priority < 1 || priority > 999 {
		return errors.New("priority must be between 1 and 999")
	}
	s.mu.Lock()
	if s.status.Running {
		s.mu.Unlock()
		return errors.New("JavLibrary historical backfill is already running")
	}
	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	s.cancel = cancel
	s.status = Status{Running: true, State: "starting", Resume: resume, Priority: priority, StartedAt: time.Now().UTC()}
	s.mu.Unlock()
	go s.run(runCtx)
	return nil
}

func parsePriority(raw string) (int, bool) {
	var n int
	if _, e := fmt.Sscan(strings.TrimSpace(raw), &n); e != nil || n < 1 || n > 999 {
		return 0, false
	}
	return n, true
}

// historicalConcurrency bounds how many detail pages within one listing
// page are fetched at once, mirroring internal/monitor's capForSchedule +
// javConcurrency pattern: default to every enabled Byparr/FlareSolverr
// instance, further capped by the byparr_max_instances_historical setting
// if one is configured (blank/0/invalid means "no cap"). With no solver
// configured at all (enabled == 0), it stays at 1 - concurrent *direct*
// requests to JavLibrary bypassing Byparr is not what this is for and is
// likely to get blocked.
func historicalConcurrency(settings map[string]string, enabled int) int {
	if enabled <= 0 {
		return 1
	}
	n := enabled
	if cap, err := strconv.Atoi(strings.TrimSpace(settings["byparr_max_instances_historical"])); err == nil && cap > 0 && cap < n {
		n = cap
	}
	return n
}

func (s *Service) Stop() bool {
	s.mu.Lock()
	if !s.status.Running {
		s.mu.Unlock()
		return false
	}
	cancel := s.cancel
	s.status.State = "stopping"
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return true
}

func (s *Service) update(fn func(*Status)) { s.mu.Lock(); fn(&s.status); s.mu.Unlock() }

func (s *Service) run(ctx context.Context) {
	s.mu.RLock()
	resume, priority, started := s.status.Resume, s.status.Priority, s.status.StartedAt
	s.mu.RUnlock()
	job := domain.Job{Kind: "javlibrary_historical_backfill", Mode: "historical", Provider: "JavLibrary", SiteTitle: "Historical catalog", StartedAt: started, Running: true}
	finalState := "completed"
	var runErr error
	defer func() {
		if errors.Is(ctx.Err(), context.Canceled) {
			finalState = "cancelled"
			runErr = context.Canceled
		} else if runErr != nil {
			finalState = "failed"
		}
		_ = s.store.SetHistoricalBackfillState(context.Background(), finalState)
		job.State = finalState
		job.Running = false
		job.FinishedAt = time.Now().UTC()
		if runErr != nil {
			job.Error = runErr.Error()
		}
		s.mu.RLock()
		job.Added = s.status.RunAdded
		job.Updated = s.status.RunUpdated
		job.Skipped = s.status.RunSkipped
		s.mu.RUnlock()
		_, _ = s.store.SaveJob(context.Background(), job)
		s.update(func(x *Status) {
			x.Running = false
			x.State = finalState
			x.FinishedAt = job.FinishedAt
			x.cancelFields()
			if runErr != nil {
				x.LastError = runErr.Error()
			}
		})
	}()
	if err := s.store.PrepareHistoricalBackfill(ctx, resume); err != nil {
		runErr = err
		return
	}
	indexes, err := s.scraper.HistoricalIndexes(scraper.WithSolverPriority(ctx, priority))
	if err != nil {
		runErr = err
		return
	}
	sources := make([]domain.HistoricalBackfillSource, 0, len(indexes))
	for _, x := range indexes {
		sources = append(sources, domain.HistoricalBackfillSource{URL: x.URL, Kind: x.Kind, Name: x.Name})
	}
	if err = s.store.UpsertHistoricalBackfillSources(ctx, sources); err != nil {
		runErr = err
		return
	}
	sources, err = s.store.HistoricalBackfillSources(ctx)
	if err != nil {
		runErr = err
		return
	}
	siteID, err := s.catalogSite(ctx)
	if err != nil {
		runErr = err
		return
	}
	ctx = scraper.WithSolverPriority(ctx, priority)
	// Sized once per run, not per page: settings/pool membership can change
	// mid-run via a later Configure call, but re-reading them on every page
	// would just add noise for a value that only needs a coarse refresh.
	concurrency := scraper.ScrapeConcurrency{Max: 1}
	if settings, e := s.store.Settings(ctx); e == nil {
		concurrency.Max = historicalConcurrency(settings, s.scraper.PoolEnabledCount())
	}
	for si := range sources {
		source := sources[si]
		if source.State == "completed" {
			continue
		}
		s.update(func(x *Status) {
			x.State = "running"
			x.SourceIndex = si + 1
			x.SourceCount = len(sources)
			x.SourceKind = source.Kind
			x.SourceName = source.Name
			x.SourceURL = source.URL
		})
		for page := max(source.NextPage, 1); ; page++ {
			if ctx.Err() != nil {
				return
			}
			known := map[string]domain.HistoricalBackfillItem{}
			var includeErr error
			include := func(id string) bool {
				item, ok, e := s.store.HistoricalBackfillItem(ctx, id)
				if e != nil {
					includeErr = e
					return false
				}
				if ok && item.State == "completed" {
					known[strings.ToLower(id)] = item
					return false
				}
				releaseDate, exists, e := s.store.ReleaseKnown(ctx, "JavLibrary", id)
				if e != nil {
					includeErr = e
					return false
				}
				if exists {
					item = domain.HistoricalBackfillItem{VideoID: id, ReleaseDate: releaseDate, State: "completed", SourceURL: source.URL}
					if e = s.store.SaveHistoricalBackfillItem(ctx, item); e != nil {
						includeErr = e
						return false
					}
					known[strings.ToLower(id)] = item
					return false
				}
				return true
			}
			s.update(func(x *Status) { x.Page = page; x.PageLimit = source.PageLimit; x.VideoID = "" })
			items, ids, limit, e := s.scraper.HistoricalPage(ctx, source.URL, page, include, concurrency)
			if includeErr != nil {
				runErr = includeErr
				return
			}
			if e != nil {
				runErr = e
				return
			}
			if limit > 0 {
				source.PageLimit = limit
			}
			oldest := ""
			boundaryReached := false
			for _, id := range ids {
				if item, ok := known[strings.ToLower(id)]; ok {
					if item.ReleaseDate != "" && (oldest == "" || item.ReleaseDate < oldest) {
						oldest = item.ReleaseDate
					}
					if source.ResumeDate != "" && item.ReleaseDate != "" && item.ReleaseDate <= source.ResumeDate {
						boundaryReached = true
					}
					s.update(func(x *Status) { x.RunSkipped++ })
				}
			}
			for _, r := range items {
				s.update(func(x *Status) { x.VideoID = r.VideoID; x.RunDiscovered++ })
				r.SiteID = siteID
				created, saveErr := s.store.UpsertRelease(ctx, r)
				state, itemErr := "completed", ""
				if saveErr != nil {
					state = "failed"
					itemErr = saveErr.Error()
				}
				if markErr := s.store.SaveHistoricalBackfillItem(ctx, domain.HistoricalBackfillItem{VideoID: r.VideoID, ReleaseDate: r.ReleaseDate, State: state, SourceURL: source.URL, Error: itemErr}); markErr != nil {
					runErr = markErr
					return
				}
				if r.ReleaseDate != "" && (oldest == "" || r.ReleaseDate < oldest) {
					oldest = r.ReleaseDate
				}
				if saveErr != nil {
					s.update(func(x *Status) { x.RunFailed++; x.LastError = saveErr.Error() })
				} else {
					s.update(func(x *Status) {
						x.RunCompleted++
						if created {
							x.RunAdded++
						} else {
							x.RunUpdated++
						}
					})
				}
			}
			if oldest != "" && (source.CursorDate == "" || oldest < source.CursorDate) {
				source.CursorDate = oldest
			}
			source.NextPage = page + 1
			// Replaying the date-sorted head during resume relocates the saved
			// boundary but is not new historical depth, so never double-count it.
			source.PagesCompleted = max(source.PagesCompleted, page)
			source.State = "running"
			done := len(ids) == 0 || (source.PageLimit > 0 && page >= source.PageLimit) || (source.CatchupOnly && boundaryReached)
			if done {
				source.State = "completed"
				source.CatchupOnly = false
				source.ResumeDate = ""
			}
			if err = s.store.SaveHistoricalBackfillSource(ctx, source); err != nil {
				runErr = err
				return
			}
			if stats, e := s.store.HistoricalBackfillStats(ctx); e == nil {
				s.update(func(x *Status) { x.Historical = stats })
			}
			if done {
				break
			}
		}
	}
}

func (x *Status) cancelFields() { x.VideoID = "" }

func (s *Service) catalogSite(ctx context.Context) (int64, error) {
	const title = "JavLibrary Historical Catalog"
	sites, err := s.store.Sites(ctx)
	if err != nil {
		return 0, err
	}
	for _, x := range sites {
		if x.Title == title {
			return x.ID, nil
		}
	}
	x, err := s.store.SaveSite(ctx, domain.Site{Title: title, Type: "Catalog", Name: "JavLibrary", URL: "https://www.javlibrary.com/en/main.php", Enabled: false})
	return x.ID, err
}

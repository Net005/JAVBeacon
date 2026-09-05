package download

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Net005/JAVBeacon/internal/domain"
	"github.com/Net005/JAVBeacon/internal/store"
)

type Service struct {
	store  store.Store
	client *http.Client
	log    *slog.Logger
	mu     sync.RWMutex
	job    domain.DownloadSearchJob
	// olderJob is the "older releases" monitored-search schedule's own job
	// status, tracked separately from job (the "recent releases" schedule)
	// so the two can run independently and be polled/started independently
	// from the Monitored releases UI - see StartSearch/StartSearchOlder and
	// SearchSchedule/OlderSearchSchedule.
	olderJob domain.DownloadSearchJob
	// replacementJob is the current or most recently completed bulk
	// Download Activity delete/replacement operation.
	replacementJob domain.DownloadReplacementJob

	cleanupRetryMu      sync.Mutex
	cleanupRetryAt      map[int64]time.Time
	httpFallbackMu      sync.Mutex
	httpFallbackRetryAt map[int64]time.Time

	// pipelineInFlightMu/pipelineInFlight track which (download ID,
	// trigger) event pipelines runEventPipelineAsync currently has running
	// in the background, so pollTorrents can tell "our own goroutine is
	// still working on this one - leave it alone" apart from "the DB says
	// running but nothing is actually running it" (e.g. the app restarted
	// mid-run) - see runEventPipelineAsync and pollDownload.
	pipelineInFlightMu sync.Mutex
	pipelineInFlight   map[string]bool

	// scheduleNextAttempt tracks, per schedule loop below ("search",
	// "older_search", "notification", "rss"), the wall-clock time that
	// loop will next actually check whether it's due to run - kept live
	// (updated every loop iteration, not just when the schedule fires) so
	// SearchScheduleForecast can report an accurate "next run" without
	// re-deriving the loop's own timing separately. Guarded by mu like the
	// rest of this struct's mutable fields.
	scheduleNextAttempt map[string]time.Time

	pipelineJobs chan pipelineJob
	httpMu       sync.Mutex
	httpActive   int
}

// scheduleMaxSleepChunk bounds how long any schedule loop below ever sleeps
// in one time.NewTimer wait, so a settings change (interval edited, or a
// schedule enabled/disabled) is picked up within this long at worst instead
// of only after whatever stale interval the loop last computed - mirrors
// monitor.scheduleMaxSleepChunk, which documents the same "otherwise a
// changed schedule is indistinguishable from requires a restart" problem.
var scheduleMaxSleepChunk = 30 * time.Second

// pipelineJob is one request to run a func serially through the shared
// pipeline worker - see runPipelineSerialized.
type pipelineJob struct {
	run  func() error
	done chan error
}

// cleanupRetryInterval bounds how often a completed torrent whose cleanup
// previously failed (e.g. a transient qBittorrent API error, or the
// post-removal pipeline erroring) gets another removal attempt. Without this,
// a single failure used to permanently strand the torrent in "cleanup_failed"
// until someone noticed and intervened by hand.
const cleanupRetryInterval = 5 * time.Minute

// defaultTorrentHTTPFallbackDelay lets a newly submitted magnet resolve
// metadata and establish peers before its first health decision. Installations
// can override it with http_fallback_delay under Downloads -> HTTP.
const defaultTorrentHTTPFallbackDelay = 8 * time.Hour
const httpFallbackRetryInterval = 30 * time.Minute

func (s *Service) torrentHTTPFallbackDelay(ctx context.Context) time.Duration {
	settings, err := s.store.Settings(ctx)
	if err != nil {
		return defaultTorrentHTTPFallbackDelay
	}
	delay, err := time.ParseDuration(strings.TrimSpace(settings["http_fallback_delay"]))
	if err != nil || delay <= 0 {
		return defaultTorrentHTTPFallbackDelay
	}
	return delay
}

// cleanupDue reports whether it's time to retry a previously failed cleanup
// for this download, and if so reserves the next retry slot so concurrent
// polls don't hammer qBittorrent with duplicate removal attempts.
func (s *Service) cleanupDue(downloadID int64) bool {
	s.cleanupRetryMu.Lock()
	defer s.cleanupRetryMu.Unlock()
	if next, ok := s.cleanupRetryAt[downloadID]; ok && time.Now().Before(next) {
		return false
	}
	if s.cleanupRetryAt == nil {
		s.cleanupRetryAt = map[int64]time.Time{}
	}
	s.cleanupRetryAt[downloadID] = time.Now().Add(cleanupRetryInterval)
	return true
}

// clearCleanupRetry drops any retry backoff bookkeeping once a download's
// torrent has been removed (or otherwise no longer needs retrying).
func (s *Service) clearCleanupRetry(downloadID int64) {
	s.cleanupRetryMu.Lock()
	defer s.cleanupRetryMu.Unlock()
	delete(s.cleanupRetryAt, downloadID)
}

func (s *Service) httpFallbackDue(downloadID int64) bool {
	s.httpFallbackMu.Lock()
	defer s.httpFallbackMu.Unlock()
	if next, ok := s.httpFallbackRetryAt[downloadID]; ok && time.Now().Before(next) {
		return false
	}
	if s.httpFallbackRetryAt == nil {
		s.httpFallbackRetryAt = map[int64]time.Time{}
	}
	s.httpFallbackRetryAt[downloadID] = time.Now().Add(httpFallbackRetryInterval)
	return true
}

func New(st store.Store, timeout time.Duration, log *slog.Logger) *Service {
	s := &Service{store: st, client: &http.Client{Timeout: timeout}, log: log, pipelineJobs: make(chan pipelineJob, 64), scheduleNextAttempt: map[string]time.Time{}}
	if rows, err := st.DownloadSearchRuns(context.Background(), "recent", 1); err == nil && len(rows) > 0 {
		s.job = searchJobFromRun(rows[0])
	}
	if rows, err := st.DownloadSearchRuns(context.Background(), "older", 1); err == nil && len(rows) > 0 {
		s.olderJob = searchJobFromRun(rows[0])
	}
	go s.runPipelineWorker()
	go s.resumeHTTPDownloads()
	return s
}

func searchJobFromRun(run domain.DownloadSearchRun) domain.DownloadSearchJob {
	return domain.DownloadSearchJob{StartedAt: run.StartedAt, FinishedAt: run.FinishedAt, Checked: run.Checked, Found: run.Found, Downloaded: run.Downloaded, Skipped: run.Skipped, Failed: run.Failed, Error: run.Error}
}

// runPipelineWorker is the single goroutine that ever actually executes an
// ordered event pipeline run. Every trigger (a download completing, a
// torrent being confirmed removed, ...) funnels through
// runPipelineSerialized, which enqueues a job here and blocks for its
// result - so no matter how many triggers fire, or from where, only one
// pipeline run is ever in flight at a time, and they always run in the
// order they were triggered rather than being launched concurrently or
// reordered.
func (s *Service) runPipelineWorker() {
	for job := range s.pipelineJobs {
		job.done <- job.run()
	}
}

// runPipelineSerialized enqueues fn to run on the shared pipeline worker and
// blocks until it has actually run, returning its result. Callers pass a
// closure rather than calling their work directly so "the order they were
// triggered" is exactly "the order they run in" - the queue never
// reorders or parallelizes what's handed to it, it just makes the caller
// wait its turn.
func (s *Service) runPipelineSerialized(fn func() error) error {
	job := pipelineJob{run: fn, done: make(chan error, 1)}
	s.pipelineJobs <- job
	return <-job.done
}

// pipelineInFlightKey identifies one (download, trigger) event pipeline for
// the pipelineInFlight bookkeeping below.
func pipelineInFlightKey(downloadID int64, trigger string) string {
	return strconv.FormatInt(downloadID, 10) + ":" + trigger
}
func (s *Service) markPipelineInFlight(downloadID int64, trigger string) {
	s.pipelineInFlightMu.Lock()
	if s.pipelineInFlight == nil {
		s.pipelineInFlight = map[string]bool{}
	}
	s.pipelineInFlight[pipelineInFlightKey(downloadID, trigger)] = true
	s.pipelineInFlightMu.Unlock()
}
func (s *Service) clearPipelineInFlight(downloadID int64, trigger string) {
	s.pipelineInFlightMu.Lock()
	delete(s.pipelineInFlight, pipelineInFlightKey(downloadID, trigger))
	s.pipelineInFlightMu.Unlock()
}
func (s *Service) isPipelineInFlight(downloadID int64, trigger string) bool {
	s.pipelineInFlightMu.Lock()
	defer s.pipelineInFlightMu.Unlock()
	return s.pipelineInFlight[pipelineInFlightKey(downloadID, trigger)]
}

// downloadByID re-fetches one download row by ID from the given status
// bucket ("downloading" or "completed" - a download's Status field is one
// of the two for as long as pollTorrents still looks at it). Used by
// runEventPipelineAsync to apply a finished pipeline's outcome against the
// row's current state rather than whatever copy was captured when the
// pipeline was kicked off, which later poll ticks keep updating in the
// meantime (progress, seeds, peers, ratio...) while the pipeline runs.
func (s *Service) downloadByID(ctx context.Context, id int64, status string) (domain.Download, bool) {
	rows, e := s.store.Downloads(ctx, status)
	if e != nil {
		return domain.Download{}, false
	}
	for _, x := range rows {
		if x.ID == id {
			return x, true
		}
	}
	return domain.Download{}, false
}

// runEventPipelineAsync runs trigger's event pipeline for d on the shared
// serialized pipeline worker (runPipelineWorker - so pipelines still never
// run concurrently with each other, and still run in the order they were
// triggered) without blocking the caller. pollTorrents used to call
// runPipelineSerialized directly and block on it, which meant a single
// slow or stuck pipeline step (a large file move, a remux, a StashApp
// call...) stalled qBittorrent status polling for every download, not just
// the one whose pipeline was running - this is what runEventPipelineAsync
// exists to avoid. Once the pipeline finishes, only its own outcome fields
// (PostStatus/Error/QBResponse, plus whatever onFailure sets when the
// pipeline errored) are applied, against a freshly reloaded copy of the
// row - see downloadByID - never the copy captured at kickoff time.
// onFinished, when non-nil, is called after every run (success or failure)
// with the reloaded row (already carrying the pipeline's own PostStatus -
// "pipeline_completed"/"pipeline_failed" - and QBResponse/Error) so the
// caller can override PostStatus with something more specific, the same way
// the pre-async code did inline. pipelineErr is the pipeline's own returned
// error, nil on success.
func (s *Service) runEventPipelineAsync(ctx context.Context, d domain.Download, t Torrent, trigger string, onFinished func(cur *domain.Download, pipelineErr error)) {
	s.markPipelineInFlight(d.ID, trigger)
	go func() {
		defer s.clearPipelineInFlight(d.ID, trigger)
		e := s.runPipelineSerialized(func() error { return s.runPipelineEvent(ctx, &d, t, trigger) })
		current, ok := s.downloadByID(ctx, d.ID, "completed")
		if !ok {
			return
		}
		current.QBResponse = d.QBResponse
		current.PostStatus = d.PostStatus
		current.Error = d.Error
		if onFinished != nil {
			onFinished(&current, e)
		}
		_, _ = s.store.SaveDownload(ctx, current)
	}()
}
func (s *Service) provider(ctx context.Context) (SearchProvider, error) {
	settings, e := s.store.Settings(ctx)
	if e != nil {
		return nil, e
	}
	patterns := strings.FieldsFunc(settings["accepted_patterns"], func(r rune) bool { return r == '\n' || r == ',' })
	return &Nyaa{Client: s.client, URLTemplate: settings["search_url_template"], AcceptedPatterns: patterns}, nil
}
func (s *Service) Search(ctx context.Context, release domain.Release) ([]domain.SearchResult, error) {
	return s.search(ctx, release, "Manual Search")
}
func (s *Service) SearchHTTP(ctx context.Context, release domain.Release) ([]domain.SearchResult, error) {
	return s.searchHTTP(ctx, release, "Manual HTTP Search")
}
func (s *Service) searchHTTP(ctx context.Context, release domain.Release, sourceType string) ([]domain.SearchResult, error) {
	settings, err := s.store.Settings(ctx)
	if err != nil {
		return nil, err
	}
	rows := make([]domain.SearchResult, 0)
	var providerErrors []string
	for _, provider := range httpSourceProviders(s.client, settings) {
		found, searchErr := provider.Search(ctx, release)
		history := domain.Download{ReleaseID: release.ID, Provider: provider.Name(), SourceType: sourceType, Query: release.VideoID, Status: "searched", Transport: "http"}
		if searchErr != nil {
			history.Status, history.Error = "failed", searchErr.Error()
			_, _ = s.store.SaveDownload(ctx, history)
			providerErrors = append(providerErrors, provider.Name()+": "+searchErr.Error())
			continue
		}
		for _, result := range found {
			item := history
			item.Name = result.Title
			item.SourceReference = result.Link
			item.SourcePageURL = result.SourceURL
			item.BytesTotal = result.SizeBytes
			item.MatchReason = result.Reason
			if result.Accepted {
				item.Status = "search_accepted"
			} else {
				item.Status = "search_rejected"
			}
			_, _ = s.store.SaveDownload(ctx, item)
		}
		rows = append(rows, found...)
	}
	if len(rows) == 0 && len(providerErrors) > 0 {
		return nil, errors.New(strings.Join(providerErrors, "; "))
	}
	return rows, nil
}

func (s *Service) SearchAll(ctx context.Context, release domain.Release) ([]domain.SearchResult, error) {
	torrent, torrentErr := s.Search(ctx, release)
	httpRows, httpErr := s.SearchHTTP(ctx, release)
	if torrentErr != nil && httpErr != nil {
		return nil, fmt.Errorf("torrent: %v; HTTP: %v", torrentErr, httpErr)
	}
	if release.HTTPDownloadPrimary {
		return append(append(make([]domain.SearchResult, 0, len(httpRows)+len(torrent)), httpRows...), torrent...), nil
	}
	return append(append(make([]domain.SearchResult, 0, len(torrent)+len(httpRows)), torrent...), httpRows...), nil
}
func (s *Service) search(ctx context.Context, release domain.Release, sourceType string) ([]domain.SearchResult, error) {
	rows, e := s.searchNative(ctx, release, sourceType)
	return sortSearchResults(rows), e
}

// searchNative fetches one release's search results from the configured
// provider in whatever order the provider itself returned them (typically
// newest-first for an RSS-based indexer like Nyaa/Sukebei), and records
// search history for each result (search_accepted/search_rejected, or a
// single "failed" row if the search itself errored) - exactly what search
// always did, before it also sorted the results for display. Kept separate
// from search's sorting so a caller that needs the provider's original
// order - SearchAndDownloadNow's "most recent" fallback tier, specifically
// - can see it before sortSearchResults reshuffles a copy for display.
func (s *Service) searchNative(ctx context.Context, release domain.Release, sourceType string) ([]domain.SearchResult, error) {
	p, e := s.provider(ctx)
	if e != nil {
		return nil, e
	}
	rows, e := p.Search(ctx, release.VideoID)
	history := domain.Download{ReleaseID: release.ID, Provider: p.Name(), SourceType: sourceType, Query: release.VideoID, Status: "searched"}
	if e != nil {
		history.Status = "failed"
		history.Error = e.Error()
		_, _ = s.store.SaveDownload(ctx, history)
	} else {
		for _, result := range rows {
			item := history
			item.Name = result.Title
			item.Seeds = result.Seeds
			item.Peers = result.Peers
			item.SourceReference = result.SourceURL
			if item.SourceReference == "" {
				item.SourceReference = result.Link
			}
			item.MatchReason = result.Reason
			if result.Accepted {
				item.Status = "search_accepted"
			} else {
				item.Status = "search_rejected"
			}
			_, _ = s.store.SaveDownload(ctx, item)
		}
	}
	return rows, e
}

// sortSearchResults returns a new slice - the input is never mutated, so a
// caller holding the provider's native order (see searchNative) keeps it -
// with preferred matches (accepted by the configured filename patterns)
// first, then within each group the torrent most likely to actually
// finish - the one with more seeders - first. This is only a sensible
// default ordering: the UI re-groups/re-filters on top of it, but a caller
// that just takes rows[0] (or displays them unsorted) still gets the best
// candidate first.
func sortSearchResults(rows []domain.SearchResult) []domain.SearchResult {
	sorted := append([]domain.SearchResult{}, rows...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].Accepted != sorted[j].Accepted {
			return sorted[i].Accepted
		}
		return sorted[i].Seeds > sorted[j].Seeds
	})
	return sorted
}
func canonical(raw string) string {
	return strings.ToUpper(strings.NewReplacer("-", "", "_", "", " ", "").Replace(raw))
}

const (
	completedTorrentKeep          = "keep"
	completedTorrentRemove        = "remove_completed"
	completedTorrentRemoveAtRatio = "remove_at_ratio"
	pipelineDownloadCompleted     = "download_completed"
	pipelineDownloadRemoved       = "download_completed_removed"
)

// statusRemoved is the terminal Download.Status a download is moved to once
// pollTorrents discovers its torrent is gone from qBittorrent for a reason
// this app doesn't know about (see postStatusRemovedUnknown). Every Download
// Activity tab filters on d.status ("downloading"/"completed"/"failed", plus
// the Stalled tab's downloading+seeds=0), and none of them query "removed",
// so flipping Status here - not just PostStatus - is what actually removes
// the row from Download Activity, instead of leaving it stuck forever
// showing stale progress under its last known status. It also matters for
// duplicate() (the duplicate-download guard above), which only treats
// "downloading"/"completed"/"processing" as an active/finished download for
// a release - once a row is statusRemoved, that release becomes eligible to
// be searched/downloaded again instead of being silently blocked forever.
const statusRemoved = "removed"

// postStatusRemovedUnknown marks a download whose torrent is no longer
// present in qBittorrent at all, without this app having removed it itself -
// deleted directly in qBittorrent, evicted by qBittorrent, or any other
// reason this app has no visibility into ("removed - unknown reason"). Unlike
// postStatusCompletedRemoved (this app called qb.Remove() itself, typically
// once the configured seed ratio was met) there is nothing left to retry, so
// a download reaching this state is never re-scheduled for cleanup again,
// and - unlike that case - Status is also moved to statusRemoved so the row
// stops showing in Download Activity entirely; see statusRemoved.
const postStatusRemovedUnknown = "removed_unknown_reason"

// postStatusCompletedRemoved marks a download this app itself removed from
// qBittorrent (rule "remove immediately" or the configured seed ratio being
// met). Kept as its own named constant alongside postStatusRemovedUnknown so
// the two "the torrent is gone" outcomes stay easy to tell apart in code,
// even though it was already used as a bare string literal below.
const postStatusCompletedRemoved = "completed_removed"

// downloadGoneFromQBHandled reports whether postStatus already reflects a
// torrent this app knows is gone from qBittorrent for a good reason (it
// removed it itself, or a post-removal pipeline step failed after a
// successful removal) - so pollTorrents's "torrent vanished" detection
// should not re-flag it as postStatusRemovedUnknown just because it no
// longer appears in qBittorrent's torrent list, which is expected for all of
// these. A row already at postStatusRemovedUnknown also short-circuits here,
// though in practice its Status is no longer "downloading"/"completed" by
// then so pollTorrents won't fetch it again anyway - kept as a defensive
// fallback.
func downloadGoneFromQBHandled(postStatus string) bool {
	switch postStatus {
	case postStatusCompletedRemoved, "removed_pipeline_failed", postStatusRemovedUnknown:
		return true
	default:
		return false
	}
}

// defaultPipelineTimeout bounds how long a single ordered event pipeline
// run (runPipelineEvent, and a one-off "Test step" run in TestPipelineStep)
// is allowed to take before it's treated as failed, so a hung shell command
// or an unresponsive StashApp instance can no longer block download
// post-processing indefinitely. It only applies to the actual step work
// (the shell command / StashApp GraphQL call) - not to reading pipeline
// configuration or persisting run/log rows, so a run that times out is
// still recorded rather than silently lost. This is only the fallback used
// when the persisted pipeline_timeout_seconds setting is empty or invalid;
// see pipelineTimeout.
const defaultPipelineTimeout = 30 * time.Second

// pipelineKillGrace bounds how much longer a shell step's CombinedOutput()
// call will wait, once the pipeline timeout has killed the shell, for a
// straggling grandchild process (one the shell spawned that didn't exit
// with it, e.g. something the command backgrounded or forked) to release
// the stdout/stderr pipes it inherited. Without this, os/exec's Wait keeps
// draining those pipes until every process holding them open exits on its
// own - which for a genuinely hung grandchild means the configured
// pipeline timeout would not actually bound anything.
const pipelineKillGrace = 5 * time.Second

// pipelineTimeout reads the persisted pipeline_timeout_seconds setting,
// falling back to defaultPipelineTimeout when it is unset, non-numeric, or
// not a positive number of seconds. This is the DEFAULT step timeout, used
// by stepTimeout for any step that has not set its own override.
func (s *Service) pipelineTimeout(ctx context.Context) time.Duration {
	settings, e := s.store.Settings(ctx)
	if e != nil {
		return defaultPipelineTimeout
	}
	if secs, err := strconv.Atoi(strings.TrimSpace(settings["pipeline_timeout_seconds"])); err == nil && secs > 0 {
		return time.Duration(secs) * time.Second
	}
	return defaultPipelineTimeout
}

// stepTimeout resolves the timeout to use for one pipeline step: the
// step's own TimeoutSeconds if it has set one (a positive value saved to
// its persisted PipelineStep row - see domain.PipelineStep.TimeoutSeconds),
// otherwise the settings-wide default from pipelineTimeout. This is what
// lets each command in the pipeline be tuned individually - a step whose
// script legitimately takes longer than the rest doesn't require raising
// every other step's budget too, and a slow step no longer eats into how
// long later steps that depend on it get to run (each step now gets its
// own full budget rather than sharing one deadline across the whole run).
func (s *Service) stepTimeout(ctx context.Context, step domain.PipelineStep) time.Duration {
	if step.TimeoutSeconds > 0 {
		return time.Duration(step.TimeoutSeconds) * time.Second
	}
	return s.pipelineTimeout(ctx)
}

// clarifyPipelineTimeout replaces a generic "signal: killed" or "context
// deadline exceeded" error with an explicit, human-readable one once ctx's
// deadline has actually elapsed, so pipeline logs and the Settings UI's
// "Test step" result say what actually happened instead of a cryptic
// underlying error.
func clarifyPipelineTimeout(ctx context.Context, e error, timeout time.Duration) error {
	if e != nil && ctx.Err() == context.DeadlineExceeded {
		return fmt.Errorf("step timed out after %s (raise this step's Timeout, or the default step timeout in Settings)", timeout)
	}
	return e
}

func completedTorrentRule(raw string) string {
	switch strings.TrimSpace(raw) {
	case completedTorrentKeep, completedTorrentRemove, completedTorrentRemoveAtRatio:
		return strings.TrimSpace(raw)
	default:
		// Preserve the behavior used before the cleanup rule became configurable.
		return completedTorrentRemoveAtRatio
	}
}

func completedTorrentReady(rule string, ratio, minimumRatio float64) bool {
	return rule == completedTorrentRemove || (rule == completedTorrentRemoveAtRatio && ratio >= minimumRatio)
}
func hasReleaseSite(release domain.Release, siteID int64) bool {
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
func normalizedDownloadTransport(transport string) string {
	if strings.EqualFold(strings.TrimSpace(transport), "http") {
		return "http"
	}
	return "torrent"
}

func (s *Service) duplicateStored(ctx context.Context, r domain.Release, allowLocal, force bool, transport string) (string, int64, bool, error) {
	if r.Local && !allowLocal {
		return "release already exists in StashApp", 0, false, nil
	}
	downloads, e := s.store.Downloads(ctx, "")
	if e != nil {
		return "", 0, false, e
	}
	requestedTransport := normalizedDownloadTransport(transport)
	for _, d := range downloads {
		if d.ReleaseID != r.ID {
			continue
		}
		active := d.Status == "queued" || d.Status == "downloading" || d.Status == "processing"
		if force {
			if active && normalizedDownloadTransport(d.Transport) == requestedTransport {
				return "release already has an active " + requestedTransport + " download in state " + d.Status, d.ID, false, nil
			}
			continue
		}
		if active || d.Status == "completed" {
			return "release already has download history in state " + d.Status, d.ID, active, nil
		}
	}
	return "", 0, false, nil
}
func (s *Service) duplicate(ctx context.Context, r domain.Release, allowLocal, force bool, transport string) (string, int64, bool, error) {
	transport = normalizedDownloadTransport(transport)
	if reason, existingID, replaceable, err := s.duplicateStored(ctx, r, allowLocal, force, transport); err != nil || reason != "" {
		return reason, existingID, replaceable, err
	}
	if transport != "torrent" {
		return "", 0, false, nil
	}
	settings, e := s.store.Settings(ctx)
	if e != nil {
		return "", 0, false, e
	}
	if settings["qb_url"] != "" {
		torrents, e := NewQB(settings["qb_url"], settings["qb_username"], settings["qb_password"]).Torrents(ctx)
		if e != nil {
			return "", 0, false, e
		}
		for _, t := range torrents {
			if strings.Contains(canonical(t.Name), canonical(r.VideoID)) {
				if !force || t.Progress < 1 {
					return "release already has an active torrent in qBittorrent", 0, !force, nil
				}
			}
		}
	}
	return "", 0, false, nil
}
func (s *Service) Download(ctx context.Context, r domain.Release, result domain.SearchResult, sourceType, sourceRef string) (domain.Download, error) {
	if strings.EqualFold(result.Transport, "http") {
		return s.queueHTTPDownload(ctx, r, result, sourceType, sourceRef)
	}
	provider, providerErr := s.provider(ctx)
	if providerErr != nil {
		return domain.Download{}, providerErr
	}
	forced := result.Forced
	// excluded marks a result chosen by the Missing Library Files "allow
	// non-preferred filenames" fallback chain (TODO-2.0 Task A -
	// fallbackSearchCandidate) rather than a normal accepted-pattern
	// match. Like forced, it is an explicit, intentional bypass of
	// automatic filename matching, so it is folded into the same
	// structured domain.Download.FilenamePatternExcluded flag forced sets
	// - the Download Activity view filters on that one flag regardless of
	// which of the two paths produced it.
	excluded := result.FilenamePatternExcluded
	result.Accepted = false
	if !strings.Contains(canonical(result.Title), canonical(r.VideoID)) {
		result.Reason = "torrent filename did not contain release ID"
	} else if nyaa, ok := provider.(*Nyaa); ok {
		result.Accepted, result.Reason = nyaa.acceptFiles(result.Title, result.Files)
	}
	matchReason := result.Reason
	// A forced or fallback-excluded download is an explicit, intentional
	// override of automatic filename matching (Phase 5B; TODO-2.0 Task A):
	// the real match/reject outcome is still computed above and kept in
	// history so it is never confused with a normal accepted match.
	switch {
	case forced:
		matchReason = "manually forced despite automatic match result: " + result.Reason
	case excluded:
		matchReason = "non-preferred filename allowed by Missing Library Files fallback search despite automatic match result: " + result.Reason
	}
	if result.SourceURL != "" {
		sourceRef = result.SourceURL
	} else if sourceRef == "" {
		sourceRef = result.Link
	}
	x := domain.Download{ReleaseID: r.ID, Provider: result.Provider, SourceType: sourceType, SourceReference: sourceRef, Query: r.VideoID, Name: result.Title, Status: "queued", MatchReason: matchReason, Seeds: result.Seeds, Peers: result.Peers, FilenamePatternExcluded: forced || excluded}
	if !result.Accepted && !forced && !excluded {
		x.Status = "failed"
		x.Error = "result rejected by filename rules"
		return s.store.SaveDownload(ctx, x)
	}
	if result.ReplaceExisting {
		if _, e := s.removeReleaseDownloads(ctx, r.ID, r.VideoID, true); e != nil {
			x.Status = "failed"
			x.Error = "existing download could not be deleted before replacement: " + e.Error()
			x, _ = s.store.SaveDownload(ctx, x)
			return x, e
		}
	}
	if result.IgnoreLocal {
		forceReason := "manually forced redownload"
		if r.Local {
			forceReason += " despite existing StashApp match"
		}
		if matchReason != "" {
			matchReason = forceReason + ": " + matchReason
		} else {
			matchReason = forceReason
		}
		x.MatchReason = matchReason
	}
	forceRequested := result.Forced || result.IgnoreLocal || r.IgnoreLocalForceDownload
	if reason, existingID, replaceable, e := s.duplicate(ctx, r, result.IgnoreLocal || r.IgnoreLocalForceDownload, forceRequested, "torrent"); e != nil {
		x.Status = "failed"
		x.Error = e.Error()
		x, _ = s.store.SaveDownload(ctx, x)
		return x, e
	} else if reason != "" {
		x.Status = "skipped"
		x.MatchReason = reason
		x.CanReplace = replaceable
		x.ExistingDownloadID = existingID
		return s.store.SaveDownload(ctx, x)
	}
	settings, e := s.store.Settings(ctx)
	if e != nil {
		return x, e
	}
	qb := NewQB(settings["qb_url"], settings["qb_username"], settings["qb_password"])
	response, e := qb.Add(ctx, result.Link, settings["qb_category"])
	x.QBResponse = response
	if e != nil {
		x.Status = "failed"
		x.Error = e.Error()
		x, _ = s.store.SaveDownload(ctx, x)
		_, _ = s.store.CreateNotification(ctx, r.ID, "download_failed", e.Error())
		return x, e
	}
	// qBittorrent's /torrents/add replies HTTP 200 "Ok." for a lot of input
	// it never actually queues - a magnet it can't parse, a .torrent URL it
	// can't fetch, a category it silently drops the add for - so that
	// response alone is not proof the torrent exists. Confirm it actually
	// registered before telling the user (and this app's own history) that
	// it is downloading; otherwise the record sat at "downloading" forever
	// with nothing to show for it, which is indistinguishable from "Force
	// Download did nothing" (the reported bug this guards against).
	if hash, ok := s.verifyAddedToQBittorrent(ctx, qb, result.Link, r.VideoID); ok {
		x.TorrentHash = hash
		x.Status = "downloading"
		x, _ = s.store.SaveDownload(ctx, x)
		_, _ = s.store.CreateNotification(ctx, r.ID, "download_started", "Download sent to qBittorrent")
		return x, nil
	}
	x.Status = "failed"
	x.Error = "qBittorrent accepted the request but the torrent never appeared in its list - check the category, the magnet/torrent link, and qBittorrent's own logs"
	x, _ = s.store.SaveDownload(ctx, x)
	_, _ = s.store.CreateNotification(ctx, r.ID, "download_failed", x.Error)
	return x, nil
}

// magnetHashPattern pulls the BitTorrent info-hash out of a magnet URI's
// btih parameter when it's hex-encoded (the form Sukebei/Nyaa and most
// public trackers use). A base32-encoded hash is left unmatched here - name
// matching in verifyAddedToQBittorrent below still covers that case.
var magnetHashPattern = regexp.MustCompile(`(?i)btih:([0-9a-f]{40})`)

func magnetInfoHash(link string) (string, bool) {
	m := magnetHashPattern.FindStringSubmatch(link)
	if m == nil {
		return "", false
	}
	return strings.ToLower(m[1]), true
}

// verifyAddedToQBittorrent confirms a just-submitted torrent actually
// registered in qBittorrent's own torrent list, matching it the same two
// ways the periodic reconciliation in pollTorrents does: by info-hash
// parsed straight out of the magnet link when available, falling back to
// the torrent's reported name containing the release's video ID. It gives
// qBittorrent a handful of short retries so a slower non-magnet (.torrent
// URL) add - which has to be fetched and parsed server-side before it shows
// up - isn't mistaken for a silent failure.
func (s *Service) verifyAddedToQBittorrent(ctx context.Context, qb QBittorrent, link, videoID string) (string, bool) {
	wantHash, _ := magnetInfoHash(link)
	wantVideo := canonical(videoID)
	const attempts = 5
	for attempt := 0; attempt < attempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return "", false
			case <-time.After(500 * time.Millisecond):
			}
		}
		torrents, e := qb.Torrents(ctx)
		if e != nil {
			continue
		}
		for _, t := range torrents {
			if (wantHash != "" && strings.EqualFold(t.Hash, wantHash)) || (wantVideo != "" && strings.Contains(canonical(t.Name), wantVideo)) {
				return t.Hash, true
			}
		}
	}
	return "", false
}

func (s *Service) queueHTTPDownload(ctx context.Context, r domain.Release, result domain.SearchResult, sourceType, sourceRef string) (domain.Download, error) {
	if !result.Accepted && !result.Forced {
		return s.store.SaveDownload(ctx, domain.Download{ReleaseID: r.ID, Provider: result.Provider, SourceType: sourceType, SourceReference: result.Link, SourcePageURL: result.SourceURL, Query: r.VideoID, Name: result.Title, Transport: "http", Status: "failed", Error: "HTTP result did not exactly match the release ID"})
	}
	if result.ReplaceExisting {
		if _, err := s.removeReleaseDownloads(ctx, r.ID, r.VideoID, true); err != nil {
			return domain.Download{}, err
		}
	}
	// HTTP downloads have no qBittorrent dependency. Only inspect JAVBeacon's
	// stored local/download state here; contacting qBittorrent would make a
	// healthy direct download fail merely because the unrelated torrent client
	// is offline.
	forceRequested := result.Forced || result.IgnoreLocal || r.IgnoreLocalForceDownload
	if reason, existingID, replaceable, err := s.duplicateStored(ctx, r, result.IgnoreLocal || r.IgnoreLocalForceDownload, forceRequested, "http"); err != nil {
		return domain.Download{}, err
	} else if reason != "" {
		return s.store.SaveDownload(ctx, domain.Download{ReleaseID: r.ID, Provider: result.Provider, SourceType: sourceType, SourceReference: result.Link, SourcePageURL: result.SourceURL, Query: r.VideoID, Name: result.Title, Transport: "http", Status: "skipped", MatchReason: reason, CanReplace: replaceable, ExistingDownloadID: existingID})
	}
	if sourceRef == "" {
		sourceRef = result.Link
	}
	matchReason := result.Reason
	if result.IgnoreLocal {
		matchReason = "manually forced redownload"
		if r.Local {
			matchReason += " despite existing StashApp match"
		}
		if result.Reason != "" {
			matchReason += ": " + result.Reason
		}
	}
	x, err := s.store.SaveDownload(ctx, domain.Download{ReleaseID: r.ID, Provider: firstNonEmpty(result.Provider, "JavDB / Keepshare"), SourceType: sourceType, SourceReference: sourceRef, SourcePageURL: result.SourceURL, Query: r.VideoID, Name: result.Title, Transport: "http", Status: "queued", MatchReason: matchReason, BytesTotal: result.SizeBytes})
	if err != nil {
		return x, err
	}
	go s.runHTTPDownload(context.Background(), x)
	_, _ = s.store.CreateNotification(ctx, r.ID, "download_started", "HTTP download queued")
	return x, nil
}

func (s *Service) httpConcurrency(ctx context.Context) int {
	settings, err := s.store.Settings(ctx)
	if err != nil {
		return 1
	}
	n, _ := strconv.Atoi(settings["http_download_concurrency"])
	if n < 1 {
		n = 1
	}
	if n > 64 {
		n = 64
	}
	return n
}
func (s *Service) acquireHTTPSlot(ctx context.Context) bool {
	for {
		limit := s.httpConcurrency(ctx)
		s.httpMu.Lock()
		if s.httpActive < limit {
			s.httpActive++
			s.httpMu.Unlock()
			return true
		}
		s.httpMu.Unlock()
		select {
		case <-ctx.Done():
			return false
		case <-time.After(300 * time.Millisecond):
		}
	}
}
func (s *Service) releaseHTTPSlot() {
	s.httpMu.Lock()
	if s.httpActive > 0 {
		s.httpActive--
	}
	s.httpMu.Unlock()
}

func (s *Service) resumeHTTPDownloads() {
	time.Sleep(500 * time.Millisecond)
	rows, err := s.store.Downloads(context.Background(), "")
	if err != nil {
		return
	}
	for _, row := range rows {
		if row.Transport == "http" && (row.Status == "queued" || row.Status == "downloading") {
			row.Status = "queued"
			row.Error = ""
			row.Progress = 0
			row.BytesDownloaded = 0
			row.BytesPerSecond = 0
			row, _ = s.store.SaveDownload(context.Background(), row)
			go s.runHTTPDownload(context.Background(), row)
		}
	}
}

func (s *Service) runHTTPDownload(ctx context.Context, d domain.Download) {
	if !s.acquireHTTPSlot(ctx) {
		return
	}
	defer s.releaseHTTPSlot()
	settings, err := s.store.Settings(ctx)
	dir := ""
	if err == nil {
		dir = strings.TrimSpace(settings["http_download_directory"])
	}
	fail := func(e error) {
		d.Status = "failed"
		d.Error = e.Error()
		d.ETASeconds = 0
		d.BytesPerSecond = 0
		_, _ = s.store.SaveDownload(context.Background(), d)
		_, _ = s.store.CreateNotification(context.Background(), d.ReleaseID, "download_failed", e.Error())
	}
	if err != nil {
		fail(err)
		return
	}
	if dir == "" {
		fail(errors.New("HTTP download folder is not configured under Settings → Downloads → HTTP"))
		return
	}
	if err = os.MkdirAll(dir, 0o755); err != nil {
		fail(fmt.Errorf("create HTTP download folder: %w", err))
		return
	}
	d.Status = "downloading"
	d.Error = ""
	d, _ = s.store.SaveDownload(ctx, d)
	var resolved resolvedHTTPFile
	var resolver HTTPSourceProvider
	for _, provider := range httpSourceProviders(s.client, settings) {
		if provider.CanResolve(d) {
			resolver = provider
			break
		}
	}
	if resolver == nil {
		fail(fmt.Errorf("no HTTP provider can resolve %s", d.Provider))
		return
	}
	resolved, err = resolver.Resolve(ctx, d)
	if err != nil {
		fail(fmt.Errorf("resolve %s download: %w", resolver.Name(), err))
		return
	}
	if resolved.Size > 0 {
		d.BytesTotal = resolved.Size
	}
	files, _ := json.Marshal([]string{resolved.Name})
	d.Files = files
	finalPath := nextHTTPDestination(dir, strings.ToUpper(strings.TrimSpace(d.Query)))
	tempPath := finalPath + ".part"
	out, err := os.OpenFile(tempPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		fail(err)
		return
	}
	downloadClient := *s.client
	downloadClient.Timeout = 0
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, resolved.URL, nil)
	for key, value := range resolved.Headers {
		req.Header.Set(key, value)
	}
	resp, err := downloadClient.Do(req)
	if err != nil {
		out.Close()
		_ = os.Remove(tempPath)
		fail(err)
		return
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		resp.Body.Close()
		out.Close()
		_ = os.Remove(tempPath)
		fail(fmt.Errorf("HTTP download returned %d", resp.StatusCode))
		return
	}
	if resp.ContentLength > 0 {
		d.BytesTotal = resp.ContentLength
	}
	started, lastSave := time.Now(), time.Now()
	lastSavedBytes := d.BytesDownloaded
	buf := make([]byte, 256*1024)
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if _, err = out.Write(buf[:n]); err != nil {
				readErr = err
			}
			d.BytesDownloaded += int64(n)
		}
		if sinceSave := time.Since(lastSave); sinceSave >= time.Second {
			d.BytesPerSecond = int64(float64(d.BytesDownloaded-lastSavedBytes) / sinceSave.Seconds())
			if d.BytesTotal > 0 {
				d.Progress = float64(d.BytesDownloaded) / float64(d.BytesTotal)
			}
			elapsed := time.Since(started).Seconds()
			if d.BytesTotal > 0 && elapsed > 0 && d.BytesDownloaded > 0 {
				remaining := float64(d.BytesTotal-d.BytesDownloaded) / (float64(d.BytesDownloaded) / elapsed)
				if remaining > 0 {
					d.ETASeconds = int64(remaining)
				}
			}
			d, _ = s.store.SaveDownload(context.Background(), d)
			lastSave = time.Now()
			lastSavedBytes = d.BytesDownloaded
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			err = readErr
			break
		}
	}
	resp.Body.Close()
	closeErr := out.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(tempPath)
		fail(err)
		return
	}
	if err = os.Rename(tempPath, finalPath); err != nil {
		_ = os.Remove(tempPath)
		fail(err)
		return
	}
	d.DestinationPath = finalPath
	d.BytesDownloaded = d.BytesTotal
	d.Progress = 1
	d.ETASeconds = 0
	d.BytesPerSecond = 0
	d.Status = "completed"
	d.MatchReason = "HTTP file downloaded from Keepshare"
	d, _ = s.store.SaveDownload(context.Background(), d)
	_, _ = s.store.CreateNotification(context.Background(), d.ReleaseID, "download_completed", "HTTP download completed")
	s.runEventPipelineAsync(context.Background(), d, Torrent{Name: filepath.Base(finalPath), ContentPath: finalPath, Progress: 1}, pipelineDownloadCompleted, nil)
}

func nextHTTPDestination(dir, releaseID string) string {
	base := filepath.Join(dir, releaseID+".mp4")
	if _, err := os.Stat(base); errors.Is(err, os.ErrNotExist) {
		return base
	}
	for i := 0; ; i++ {
		candidate := filepath.Join(dir, fmt.Sprintf("%s-%d.mp4", releaseID, i))
		if _, err := os.Stat(candidate); errors.Is(err, os.ErrNotExist) {
			return candidate
		}
	}
}

func (s *Service) RetryHTTPDownload(ctx context.Context, downloadID int64) (domain.Download, error) {
	rows, err := s.store.Downloads(ctx, "")
	if err != nil {
		return domain.Download{}, err
	}
	for _, row := range rows {
		if row.ID == downloadID && row.Transport == "http" {
			if row.Status != "failed" {
				return domain.Download{}, errors.New("only failed HTTP downloads can be retried")
			}
			release, e := s.store.Release(ctx, row.ReleaseID)
			if e != nil {
				return domain.Download{}, e
			}
			return s.queueHTTPDownload(ctx, release, domain.SearchResult{Provider: row.Provider, Title: row.Name, Link: row.SourceReference, SourceURL: row.SourcePageURL, Transport: "http", SizeBytes: row.BytesTotal, Accepted: true, Reason: "retried HTTP download"}, "Manual HTTP Retry", row.SourceReference)
		}
	}
	return domain.Download{}, errors.New("HTTP download not found")
}

// RetryFailedHTTPDownloads queues either the selected failed HTTP rows or all
// failed HTTP rows. Releases are deduplicated so stale history cannot start the
// same media download more than once in a single bulk action.
func (s *Service) RetryFailedHTTPDownloads(ctx context.Context, downloadIDs []int64, all bool) (map[string]any, error) {
	if !all && len(downloadIDs) == 0 {
		return nil, errors.New("select at least one failed HTTP download")
	}
	rows, err := s.store.Downloads(ctx, "failed")
	if err != nil {
		return nil, err
	}
	wanted := make(map[int64]bool, len(downloadIDs))
	for _, id := range downloadIDs {
		wanted[id] = true
	}
	selected := make([]domain.Download, 0, len(rows))
	seenReleases := map[int64]bool{}
	for _, row := range rows {
		if row.Transport != "http" || (!all && !wanted[row.ID]) || seenReleases[row.ReleaseID] {
			continue
		}
		seenReleases[row.ReleaseID] = true
		selected = append(selected, row)
	}
	if len(selected) == 0 {
		return nil, errors.New("no failed HTTP downloads matched this request")
	}
	retried := 0
	failures := make([]string, 0)
	for _, row := range selected {
		if _, err := s.RetryHTTPDownload(ctx, row.ID); err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", firstNonEmpty(row.Query, row.Name), err))
			continue
		}
		retried++
	}
	return map[string]any{"matched": len(selected), "retried": retried, "failed": len(failures), "errors": failures}, nil
}

func (s *Service) TestQB(ctx context.Context, baseURL, username, password string) (string, []string, error) {
	qb := NewQB(baseURL, username, password)
	qb.Client.Timeout = s.client.Timeout
	version, err := qb.Version(ctx)
	if err != nil {
		return "", nil, err
	}
	categories, err := qb.Categories(ctx)
	return version, categories, err
}

func (s *Service) RemoveDownload(ctx context.Context, downloadID int64) (int64, error) {
	rows, err := s.store.Downloads(ctx, "")
	if err != nil {
		return 0, err
	}
	var selected *domain.Download
	for i := range rows {
		if rows[i].ID == downloadID {
			selected = &rows[i]
			break
		}
	}
	if selected == nil {
		return 0, errors.New("download not found")
	}
	return s.removeReleaseDownloads(ctx, selected.ReleaseID, selected.Query, false)
}

func (s *Service) removeReleaseDownloads(ctx context.Context, releaseID int64, query string, deleteFiles bool) (int64, error) {
	rows, err := s.store.Downloads(ctx, "")
	if err != nil {
		return 0, err
	}
	settings, err := s.store.Settings(ctx)
	if err != nil {
		return 0, err
	}
	if settings["qb_url"] != "" {
		qb := NewQB(settings["qb_url"], settings["qb_username"], settings["qb_password"])
		qb.Client.Timeout = s.client.Timeout
		hashes := map[string]bool{}
		activeHistory := false
		for _, row := range rows {
			if row.ReleaseID != releaseID {
				continue
			}
			if row.TorrentHash != "" {
				hashes[row.TorrentHash] = true
			}
			activeHistory = activeHistory || row.Status == "downloading" || row.Status == "processing"
		}
		if torrents, torrentErr := qb.Torrents(ctx); torrentErr != nil {
			if activeHistory || len(hashes) > 0 {
				return 0, torrentErr
			}
		} else {
			query := canonical(query)
			for _, torrent := range torrents {
				if query != "" && strings.Contains(canonical(torrent.Name), query) {
					hashes[torrent.Hash] = true
				}
			}
		}
		for hash := range hashes {
			var removeErr error
			if deleteFiles {
				removeErr = qb.DeleteFiles(ctx, hash)
			} else {
				removeErr = qb.Remove(ctx, hash)
			}
			if removeErr != nil {
				return 0, removeErr
			}
		}
	}

	deleted, err := s.store.DeleteDownloadsForRelease(ctx, releaseID)
	if err == nil {
		s.log.Info("download removed and history cleared", "release_id", releaseID, "video_id", query, "delete_files", deleteFiles, "history_rows", deleted)
	}
	return deleted, err
}

// StartBulkRemoveAndReplace resolves the selected Download Activity rows now,
// then performs destructive qBittorrent cleanup and optional replacement
// searches on a detached background context. Releases are deduplicated so
// selecting more than one history row for the same release never starts more
// than one replacement.
func (s *Service) ReplacementStatus() domain.DownloadReplacementJob {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.replacementJob
}

func (s *Service) setReplacementJob(job domain.DownloadReplacementJob) {
	s.mu.Lock()
	s.replacementJob = job
	s.mu.Unlock()
}

func (s *Service) searchAndDownloadBestSeeded(ctx context.Context, release domain.Release, trigger string, allowNonPreferred bool) (bool, error) {
	results, err := s.searchNative(ctx, release, trigger)
	if err != nil {
		return false, err
	}
	candidate, found := bestSeededCandidate(results, allowNonPreferred)
	if !found {
		return false, nil
	}
	downloaded, err := s.Download(ctx, release, candidate, trigger, candidate.Link)
	return err == nil && downloaded.Status == "downloading", err
}

func bestSeededCandidate(results []domain.SearchResult, allowNonPreferred bool) (domain.SearchResult, bool) {
	var candidate domain.SearchResult
	found := false
	for _, result := range results {
		if !allowNonPreferred && !result.Accepted {
			continue
		}
		if !found || result.Seeds > candidate.Seeds {
			candidate, found = result, true
		}
	}
	if !found {
		return domain.SearchResult{}, false
	}
	if !candidate.Accepted {
		candidate.FilenamePatternExcluded = true
	}
	return candidate, true
}

func (s *Service) StartBulkRemoveAndReplace(ctx context.Context, downloadIDs []int64, replace, allowNonPreferred bool) (domain.DownloadReplacementJob, error) {
	rows, err := s.store.Downloads(ctx, "")
	if err != nil {
		return domain.DownloadReplacementJob{}, err
	}
	wanted := make(map[int64]bool, len(downloadIDs))
	for _, id := range downloadIDs {
		wanted[id] = true
	}
	type selectedRelease struct {
		id    int64
		query string
	}
	selected := map[int64]selectedRelease{}
	for _, row := range rows {
		if wanted[row.ID] && row.ReleaseID != 0 {
			selected[row.ReleaseID] = selectedRelease{id: row.ReleaseID, query: row.Query}
		}
	}
	if len(selected) == 0 {
		return domain.DownloadReplacementJob{}, errors.New("no matching downloads selected")
	}
	job := domain.DownloadReplacementJob{Running: true, Replace: replace, NonPreferred: allowNonPreferred, StartedAt: time.Now().UTC(), Total: len(selected)}
	s.mu.Lock()
	if s.replacementJob.Running {
		existing := s.replacementJob
		s.mu.Unlock()
		return existing, errors.New("a bulk download replacement job is already running")
	}
	s.replacementJob = job
	s.mu.Unlock()
	go func(items map[int64]selectedRelease) {
		background := context.Background()
		defer func() {
			job.Running = false
			job.CurrentItem = ""
			job.FinishedAt = time.Now().UTC()
			s.setReplacementJob(job)
			s.log.Info("bulk download cleanup completed", "removed", job.Removed, "downloaded", job.Downloaded, "not_found", job.NotFound, "failed", job.Failed)
		}()
		for _, item := range items {
			job.CurrentItem = item.query
			s.setReplacementJob(job)
			if _, err := s.removeReleaseDownloads(background, item.id, item.query, true); err != nil {
				s.log.Error("bulk download removal failed", "release_id", item.id, "video_id", item.query, "error", err)
				job.Failed++
				job.LastError = err.Error()
				job.Processed++
				s.setReplacementJob(job)
				continue
			}
			job.Removed++
			if !replace {
				job.Processed++
				s.setReplacementJob(job)
				continue
			}
			release, err := s.store.Release(background, item.id)
			if err != nil {
				s.log.Error("bulk replacement release lookup failed", "release_id", item.id, "error", err)
				job.Failed++
				job.LastError = err.Error()
				job.Processed++
				s.setReplacementJob(job)
				continue
			}
			started, err := s.searchAndDownloadBestSeeded(background, release, "Download Activity replacement", allowNonPreferred)
			if err != nil || !started {
				s.log.Warn("bulk replacement search did not start a download", "release_id", item.id, "video_id", item.query, "started", started, "error", err)
				if err != nil {
					job.Failed++
					job.LastError = err.Error()
				} else {
					job.NotFound++
				}
			} else {
				job.Downloaded++
			}
			job.Processed++
			s.setReplacementJob(job)
		}
	}(selected)
	return job, nil
}

// Auto is intentionally inert. It remains only as source compatibility for
// extensions compiled against older releases; site discovery no longer
// searches or downloads anything automatically.
func (s *Service) Auto(context.Context, domain.Release) {}

// SearchAndDownloadDetailed searches immediately and downloads a result,
// for hand-picked actions (e.g. TODO-2.0 Phase 2's StashApp
// missing-library recovery "Monitor + Download + search" action) where the
// user has explicitly asked, release by release, for search and download
// right now rather than enrolling the release in the periodic scheduled
// sweep. It returns whether a download was actually
// started, so a caller driving a bulk run can tally "found X releases"
// results for the person without polling download history.
//
// allowNonPreferred is TODO-2.0 Task A's "allow non-preferred filenames"
// toggle: false preserves this function's original behavior exactly -
// download the best accepted-filename-pattern match, or nothing at all if
// there isn't one. true additionally applies fallbackSearchCandidate's
// three-tier fallback chain whenever the best accepted match has no seeds
// (or there is no accepted match at all): prefer any other result that has
// seeds, and failing that, the single most recent result. A candidate
// chosen by that fallback is marked
// domain.SearchResult.FilenamePatternExcluded so the resulting download's
// history is never confused with a normal accepted match.
// SearchAndDownloadOutcome preserves the useful detail from an immediate
// search/download attempt for callers that present a background task view.
// Found means a torrent candidate was selected; Download records whether it
// was actually queued, skipped, or failed at the qBittorrent step.
type SearchAndDownloadOutcome struct {
	Found    bool
	Reason   string
	Result   domain.SearchResult
	Download domain.Download
}

func (s *Service) SearchAndDownloadDetailed(ctx context.Context, r domain.Release, trigger string, allowNonPreferred bool) (SearchAndDownloadOutcome, error) {
	if r.HTTPDownloadPrimary {
		if outcome, attempted, err := s.searchAndDownloadHTTP(ctx, r, trigger); attempted || err != nil {
			return outcome, err
		}
	}
	native, e := s.searchNative(ctx, r, trigger)
	if e != nil {
		if outcome, attempted, httpErr := s.searchAndDownloadHTTP(ctx, r, trigger); attempted {
			return outcome, httpErr
		} else if httpErr != nil {
			return SearchAndDownloadOutcome{Reason: "Search providers failed: torrent: " + e.Error() + "; HTTP: " + httpErr.Error()}, e
		}
		return SearchAndDownloadOutcome{Reason: "Search provider lookup failed: " + e.Error()}, e
	}
	sorted := sortSearchResults(native)
	candidate, found := fallbackSearchCandidate(sorted, native, allowNonPreferred)
	if !found {
		if outcome, attempted, httpErr := s.searchAndDownloadHTTP(ctx, r, trigger); attempted || httpErr != nil {
			return outcome, httpErr
		}
		reason := "Search provider returned no results"
		if len(native) > 0 {
			reason = "Results were found, but none matched the preferred filename rules"
		}
		return SearchAndDownloadOutcome{Reason: reason}, nil
	}
	// A filename match with no seeders is not a useful primary download when
	// the direct provider is configured. Prefer the HTTP fallback immediately
	// instead of leaving an inert torrent queued indefinitely.
	if candidate.Seeds <= 0 {
		if outcome, attempted, httpErr := s.searchAndDownloadHTTP(ctx, r, trigger); attempted || httpErr != nil {
			return outcome, httpErr
		}
	}
	downloaded, e := s.Download(ctx, r, candidate, trigger, "")
	outcome := SearchAndDownloadOutcome{Found: true, Result: candidate, Download: downloaded}
	if e != nil {
		if httpOutcome, attempted, httpErr := s.searchAndDownloadHTTP(ctx, r, trigger); attempted {
			return httpOutcome, httpErr
		}
		outcome.Reason = downloaded.Error
		if outcome.Reason == "" {
			outcome.Reason = e.Error()
		}
		return outcome, e
	}
	if downloaded.Status == "failed" {
		if httpOutcome, attempted, httpErr := s.searchAndDownloadHTTP(ctx, r, trigger); attempted {
			return httpOutcome, httpErr
		}
	}
	if downloaded.Error != "" {
		outcome.Reason = downloaded.Error
	} else if downloaded.MatchReason != "" {
		outcome.Reason = downloaded.MatchReason
	} else {
		outcome.Reason = candidate.Reason
	}
	return outcome, nil
}

func (s *Service) searchAndDownloadHTTP(ctx context.Context, r domain.Release, trigger string) (SearchAndDownloadOutcome, bool, error) {
	settings, err := s.store.Settings(ctx)
	if err != nil {
		return SearchAndDownloadOutcome{}, false, err
	}
	// An empty destination keeps HTTP disabled. This makes the new provider a
	// safe fallback by default without turning every existing installation's
	// failed torrent search into a predictable configuration error.
	if strings.TrimSpace(settings["http_download_directory"]) == "" {
		return SearchAndDownloadOutcome{}, false, nil
	}
	rows, err := s.searchHTTP(ctx, r, trigger)
	if err != nil {
		return SearchAndDownloadOutcome{Reason: "HTTP provider lookup failed: " + err.Error()}, false, err
	}
	if len(rows) == 0 {
		return SearchAndDownloadOutcome{Reason: "JavDB returned no exact, date-compatible HTTP result"}, false, nil
	}
	d, err := s.Download(ctx, r, rows[0], trigger, rows[0].Link)
	out := SearchAndDownloadOutcome{Found: true, Result: rows[0], Download: d, Reason: d.MatchReason}
	if err != nil {
		out.Reason = err.Error()
	}
	return out, true, err
}

// SearchAndDownloadNow keeps the original compact API for callers that only
// need to know whether a candidate was found.
func (s *Service) SearchAndDownloadNow(ctx context.Context, r domain.Release, trigger string, allowNonPreferred bool) (bool, error) {
	outcome, err := s.SearchAndDownloadDetailed(ctx, r, trigger, allowNonPreferred)
	return outcome.Found && err == nil, err
}

// fallbackSearchCandidate picks the single search result SearchAndDownloadNow
// should download, from sorted (see sortSearchResults - accepted matches
// first, then by seed count) and native (the provider's own original
// order, used only for the "most recent" fallback tier below).
//
// allowNonPreferred false reproduces this selection's original, simpler
// behavior exactly: the best accepted match if there is one, regardless of
// its seed count, otherwise nothing.
//
// allowNonPreferred true applies TODO-2.0 Task A's three-tier fallback
// chain instead:
//  1. the best accepted match, but only if it has at least one seed -
//     sorted's ordering means sorted[0] is that match whenever one exists;
//  2. otherwise, whichever result (accepted or not) has the most seeds,
//     as long as it has at least one;
//  3. otherwise, the single most recent result - native[0], the provider's
//     own first-returned result, before display sorting reordered it.
//
// The returned bool reports whether any candidate was found at all. Tiers
// 2 and 3 set the returned SearchResult.FilenamePatternExcluded so callers
// never confuse that pick with a normal accepted match.
func fallbackSearchCandidate(sorted, native []domain.SearchResult, allowNonPreferred bool) (domain.SearchResult, bool) {
	if len(sorted) > 0 && sorted[0].Accepted && (!allowNonPreferred || sorted[0].Seeds > 0) {
		return sorted[0], true
	}
	if !allowNonPreferred {
		return domain.SearchResult{}, false
	}
	var best domain.SearchResult
	bestFound := false
	for _, result := range sorted {
		if result.Seeds > 0 && (!bestFound || result.Seeds > best.Seeds) {
			best, bestFound = result, true
		}
	}
	if bestFound {
		best.FilenamePatternExcluded = true
		return best, true
	}
	if len(native) > 0 {
		candidate := native[0]
		candidate.FilenamePatternExcluded = true
		return candidate, true
	}
	return domain.SearchResult{}, false
}

func (s *Service) SearchStatus() domain.DownloadSearchJob {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.job
}

// SearchStatusOlder is SearchStatus's counterpart for the "older releases"
// monitored-search schedule (see olderJob's doc comment on Service).
func (s *Service) SearchStatusOlder() domain.DownloadSearchJob {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.olderJob
}

// defaultMonitoredRecentDays/defaultMonitoredOlderDays are the "Monitored
// releases" settings area's fallback day thresholds (task 38's two-schedule
// split) when monitor_recent_days/monitor_older_days are unset or
// unparsable. They default to the same value deliberately: with both equal,
// isRecentRelease/isOlderRelease partition every release with a known
// release date cleanly in half with no gap - a release older than 30 days
// falls to the older/infrequent schedule, everything else (including an
// unknown release date) stays on the recent/frequent one. An operator who
// then diverges the two values does so knowingly, once each one is exposed
// as its own field in the Monitored releases UI.
const defaultMonitoredRecentDays = 30
const defaultMonitoredOlderDays = 30

func monitoredDaysSetting(settings map[string]string, key string, fallback int) int {
	if n, err := strconv.Atoi(strings.TrimSpace(settings[key])); err == nil && n >= 0 {
		return n
	}
	return fallback
}

// releaseAgeDays returns how many whole days old release's ReleaseDate is as
// of now, and whether that could be determined at all. ReleaseDate uses the
// scrapers' normalized "YYYY-MM-DD" format (see normalizeDate in
// internal/scraper); a blank or unparsable value returns ok=false, which
// isRecentRelease/isOlderRelease each treat differently - see their own doc
// comments.
func releaseAgeDays(now time.Time, release domain.Release) (days int, ok bool) {
	t, err := time.Parse("2006-01-02", strings.TrimSpace(release.ReleaseDate))
	if err != nil {
		return 0, false
	}
	return int(now.UTC().Truncate(24*time.Hour).Sub(t.UTC().Truncate(24*time.Hour)).Hours() / 24), true
}

// isRecentRelease reports whether release belongs to the "recent releases"
// monitored-search schedule's bucket: its release date is recentDays old or
// newer. A release with no confirmed release date yet always counts as
// recent, so it keeps getting checked frequently until a date is known
// rather than falling through to (or being skipped by) the older schedule.
func isRecentRelease(now time.Time, release domain.Release, recentDays int) bool {
	days, ok := releaseAgeDays(now, release)
	if !ok {
		return true
	}
	return days <= recentDays
}

// isOlderRelease reports whether release belongs to the "older releases"
// monitored-search schedule's bucket: its release date is known and more
// than olderDays old. A release with no confirmed release date is never in
// this bucket (see isRecentRelease) - it is only ever picked up by the
// recent schedule until its release date is filled in.
func isOlderRelease(now time.Time, release domain.Release, olderDays int) bool {
	days, ok := releaseAgeDays(now, release)
	if !ok {
		return false
	}
	return days > olderDays
}

func (s *Service) StartSearch(ctx context.Context) error {
	s.mu.Lock()
	if s.job.Running {
		s.mu.Unlock()
		return errors.New("download search job already running")
	}
	s.job = domain.DownloadSearchJob{Running: true, StartedAt: time.Now().UTC()}
	s.mu.Unlock()
	go s.runSearch(context.WithoutCancel(ctx))
	return nil
}

// StartSearchOlder is StartSearch's counterpart for the "older releases"
// schedule, so it can also be run on demand from the Monitored releases UI
// independently of (and even while) the recent schedule is running.
func (s *Service) StartSearchOlder(ctx context.Context) error {
	s.mu.Lock()
	if s.olderJob.Running {
		s.mu.Unlock()
		return errors.New("older-release download search job already running")
	}
	s.olderJob = domain.DownloadSearchJob{Running: true, StartedAt: time.Now().UTC()}
	s.mu.Unlock()
	go s.runSearchOlder(context.WithoutCancel(ctx))
	return nil
}

func (s *Service) runSearch(ctx context.Context) {
	settings, _ := s.store.Settings(ctx)
	recentDays := monitoredDaysSetting(settings, "monitor_recent_days", defaultMonitoredRecentDays)
	now := time.Now()
	s.runMonitoredSearch(ctx, "recent", s.SearchStatus, func(j domain.DownloadSearchJob) { s.mu.Lock(); s.job = j; s.mu.Unlock() },
		func(r domain.Release) bool { return isRecentRelease(now, r, recentDays) }, "Monitored Search", "scheduled download search")
}

// runSearchOlder is runSearch's counterpart for the "older releases"
// schedule (task 38): same search/fallback/Download chain, restricted to
// releases isOlderRelease considers old enough, so a slower, independent
// schedule can sweep through them without the frequent recent-releases
// schedule re-searching them (and re-hitting the download provider, e.g.
// NYAA) every run.
func (s *Service) runSearchOlder(ctx context.Context) {
	settings, _ := s.store.Settings(ctx)
	olderDays := monitoredDaysSetting(settings, "monitor_older_days", defaultMonitoredOlderDays)
	now := time.Now()
	s.runMonitoredSearch(ctx, "older", s.SearchStatusOlder, func(j domain.DownloadSearchJob) { s.mu.Lock(); s.olderJob = j; s.mu.Unlock() },
		func(r domain.Release) bool { return isOlderRelease(now, r, olderDays) }, "Monitored Search (Older)", "scheduled older-release download search")
}

// runMonitoredSearch is runSearch/runSearchOlder's shared core: fetch every
// monitored release, keep only the ones the caller's keep bucket-test
// selects (see isRecentRelease/isOlderRelease), and run each through the
// same ignore/duplicate/search/fallback/Download chain, publishing live
// progress via setJob after every step exactly as this loop always has.
// getJob/setJob read and publish the job status under s.mu rather than
// through a shared pointer, since domain.DownloadSearchJob is copied by
// value in and out of the Service struct throughout this package (see
// SearchStatus/SearchStatusOlder) - job and olderJob must stay independently
// lockable so the two schedules can run concurrently without racing on each
// other's progress.
func (s *Service) runMonitoredSearch(ctx context.Context, schedule string, getJob func() domain.DownloadSearchJob, setJob func(domain.DownloadSearchJob), keep func(domain.Release) bool, sourceType, logLabel string) {
	job := getJob()
	defer func() {
		job.Running = false
		job.FinishedAt = time.Now().UTC()
		setJob(job)
		_, saveErr := s.store.SaveDownloadSearchRun(context.WithoutCancel(ctx), domain.DownloadSearchRun{Schedule: schedule, StartedAt: job.StartedAt, FinishedAt: job.FinishedAt, Checked: job.Checked, Found: job.Found, Downloaded: job.Downloaded, Skipped: job.Skipped, Failed: job.Failed, Error: job.Error})
		if saveErr != nil {
			s.log.Error("could not persist download search run", "schedule", schedule, "error", saveErr)
		}
		s.log.Info(logLabel+" completed", "checked", job.Checked, "found", job.Found, "downloaded", job.Downloaded, "skipped", job.Skipped, "failed", job.Failed, "error", job.Error)
	}()
	rows, e := s.store.Releases(ctx, domain.ReleaseFilter{MonitorDownload: true, Limit: 5000})
	if e != nil {
		job.Error = e.Error()
		return
	}
	var ignoreTags, ignoreTitles []string
	if settings, e := s.store.Settings(ctx); e == nil {
		ignoreTags = domain.ParseIgnoreList(settings["ignore_tags"])
		ignoreTitles = domain.ParseIgnoreList(settings["ignore_titles"])
	}
	s.log.Info(logLabel+" started", "releases", len(rows))
	for _, release := range rows {
		if !keep(release) {
			continue
		}
		job.Checked++
		job.VideoID = release.VideoID
		setJob(job)
		if ignored, reason := releaseIgnored(release, ignoreTags, ignoreTitles); ignored {
			s.log.Info("skipping monitored search of ignored release", "release_id", release.ID, "video_id", release.VideoID, "reason", reason)
			job.Skipped++
			continue
		}
		// A forced monitored search must reach provider selection so the
		// transport-specific guard can ignore history but retain an active
		// download of the same category.
		if !release.IgnoreLocalForceDownload {
			if reason, _, _, err := s.duplicate(ctx, release, false, false, "torrent"); err != nil {
				job.Failed++
				job.Error = err.Error()
				continue
			} else if reason != "" {
				job.Skipped++
				continue
			}
		}
		// Scheduled monitoring deliberately uses the same provider-selection
		// path as immediate/manual background actions. That keeps HTTP-primary
		// releases HTTP-first and makes the default torrent-first path fall
		// back to HTTP for lookup failures, no acceptable torrent, zero
		// seeders, or a failed qBittorrent submission.
		outcome, err := s.SearchAndDownloadDetailed(ctx, release, sourceType, release.AllowNonPreferredFilenames)
		if outcome.Found {
			job.Found++
		}
		if err != nil {
			job.Failed++
			job.Error = err.Error()
		} else if outcome.Download.Status == "queued" || outcome.Download.Status == "downloading" {
			job.Downloaded++
		} else {
			job.Skipped++
		}
	}
}

func (s *Service) SearchSchedule(ctx context.Context) {
	lastAttempt := time.Now()
	for {
		settings, _ := s.store.Settings(ctx)
		wait := time.Hour
		if parsed, err := domain.ParseScheduleDuration(settings["download_search_interval"]); err == nil && parsed >= time.Minute {
			wait = parsed
		}
		now := time.Now()
		remaining := wait - now.Sub(lastAttempt)
		if remaining <= 0 {
			lastAttempt = now
			if settings["download_search_enabled"] == "true" {
				_ = s.StartSearch(ctx)
			}
			remaining = wait
		}
		s.mu.Lock()
		if s.scheduleNextAttempt == nil {
			s.scheduleNextAttempt = map[string]time.Time{}
		}
		s.scheduleNextAttempt["search"] = now.Add(remaining)
		s.mu.Unlock()
		sleep := remaining
		if sleep <= 0 || sleep > scheduleMaxSleepChunk {
			sleep = scheduleMaxSleepChunk
		}
		timer := time.NewTimer(sleep)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

// OlderSearchSchedule is SearchSchedule's counterpart for the "older
// releases" schedule, driven by its own enabled flag/interval settings so it
// can run far less often (the whole point of the split - task 38 - is to
// stop re-searching old, unlikely-to-appear releases as often as brand new
// ones and overloading the download provider, e.g. NYAA).
func (s *Service) OlderSearchSchedule(ctx context.Context) {
	lastAttempt := time.Now()
	for {
		settings, _ := s.store.Settings(ctx)
		wait := 24 * time.Hour
		if parsed, err := domain.ParseScheduleDuration(settings["download_search_older_interval"]); err == nil && parsed >= time.Minute {
			wait = parsed
		}
		now := time.Now()
		remaining := wait - now.Sub(lastAttempt)
		if remaining <= 0 {
			lastAttempt = now
			if settings["download_search_older_enabled"] == "true" {
				_ = s.StartSearchOlder(ctx)
			}
			remaining = wait
		}
		s.mu.Lock()
		if s.scheduleNextAttempt == nil {
			s.scheduleNextAttempt = map[string]time.Time{}
		}
		s.scheduleNextAttempt["older_search"] = now.Add(remaining)
		s.mu.Unlock()
		sleep := remaining
		if sleep <= 0 || sleep > scheduleMaxSleepChunk {
			sleep = scheduleMaxSleepChunk
		}
		timer := time.NewTimer(sleep)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

// qbPollIntervalDefault is how often pollTorrents checks qBittorrent when
// qb_poll_interval_seconds is unset or invalid. It's short enough that
// Download Activity tracks qBittorrent close to real time, while staying
// cheap regardless of how frequently it runs: each poll is a single batched
// qb.Torrents() call no matter how many downloads are active, and
// pollDownload only re-fetches a torrent's file list (a second, per-torrent
// call) the first time it sees that torrent and again once when it
// completes - not on every tick - so a fast interval doesn't multiply
// qBittorrent's request load the way it would if every field were re-synced
// per download on every poll.
const qbPollIntervalDefault = 5 * time.Second

// qbPollIntervalFloor is the fastest qb_poll_interval_seconds is ever
// allowed to run at, no matter what's configured, so a mistyped or
// deliberately tiny value can't hammer qBittorrent's HTTP API.
const qbPollIntervalFloor = 2 * time.Second

// qbPollInterval resolves the currently configured qBittorrent poll
// interval, re-read from settings on every call (like every other schedule
// loop's interval below) so an edit in Settings takes effect on the very
// next tick instead of requiring a restart.
func (s *Service) qbPollInterval(ctx context.Context) time.Duration {
	settings, e := s.store.Settings(ctx)
	if e != nil {
		return qbPollIntervalDefault
	}
	secs, e := strconv.Atoi(strings.TrimSpace(settings["qb_poll_interval_seconds"]))
	if e != nil || secs <= 0 {
		return qbPollIntervalDefault
	}
	interval := time.Duration(secs) * time.Second
	if interval < qbPollIntervalFloor {
		return qbPollIntervalFloor
	}
	return interval
}

func (s *Service) Schedule(ctx context.Context) {
	for {
		s.tick(ctx)
		timer := time.NewTimer(s.qbPollInterval(ctx))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}
func (s *Service) tick(ctx context.Context) {
	// pollTorrents runs on this single dedicated goroutine, once per
	// configured interval - if anything inside it were ever to panic
	// unrecovered, the whole process would go down (an unrecovered panic
	// in any goroutine terminates the entire program, not just that
	// goroutine), and Download Activity would silently stop updating for
	// everyone until something noticed and restarted it. Recovering here
	// means a bug in one poll cycle is logged and the next tick still
	// runs on schedule instead of taking the whole app down with it.
	defer func() {
		if r := recover(); r != nil {
			s.log.Error("qBittorrent poll panicked; recovered so Download Activity keeps updating on the next tick", "panic", r)
		}
	}()
	s.pollTorrents(ctx)
}
func (s *Service) NotificationSchedule(ctx context.Context) {
	lastAttempt := time.Now()
	s.releaseNotifications(ctx)
	for {
		wait := 15 * time.Minute
		settings, _ := s.store.Settings(ctx)
		if d, e := time.ParseDuration(settings["notification_interval"]); e == nil && d >= time.Minute {
			wait = d
		}
		now := time.Now()
		remaining := wait - now.Sub(lastAttempt)
		if remaining <= 0 {
			lastAttempt = now
			s.releaseNotifications(ctx)
			remaining = wait
		}
		s.mu.Lock()
		if s.scheduleNextAttempt == nil {
			s.scheduleNextAttempt = map[string]time.Time{}
		}
		s.scheduleNextAttempt["notification"] = now.Add(remaining)
		s.mu.Unlock()
		sleep := remaining
		if sleep <= 0 || sleep > scheduleMaxSleepChunk {
			sleep = scheduleMaxSleepChunk
		}
		timer := time.NewTimer(sleep)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

// scheduleForecastRunCount is how many upcoming run times
// SearchScheduleForecast predicts per schedule - matches
// monitor.scheduleForecastRunCount so every panel shows the same count.
const scheduleForecastRunCount = 3

// SearchScheduleForecast reports the Download monitoring recent- and
// older-releases search schedules' current enabled/interval state plus
// their next scheduleForecastRunCount predicted run times, reading the live
// scheduleNextAttempt time SearchSchedule/OlderSearchSchedule last computed
// so it can never drift from what those loops will actually do next.
func (s *Service) SearchScheduleForecast(ctx context.Context) []domain.ScheduleForecast {
	settings, _ := s.store.Settings(ctx)
	forecasts := []domain.ScheduleForecast{
		s.intervalScheduleForecast("search", "Monitored releases · recent", settings["download_search_enabled"] == "true", settings["download_search_interval"], time.Hour),
		s.intervalScheduleForecast("older_search", "Monitored releases · older", settings["download_search_older_enabled"] == "true", settings["download_search_older_interval"], 24*time.Hour),
	}
	return forecasts
}

// intervalScheduleForecast builds one ScheduleForecast entry for a
// live-tracked, plain-interval (non-calendar) schedule loop keyed by mode in
// s.scheduleNextAttempt, extrapolating scheduleForecastRunCount future runs
// by repeatedly adding its interval to the loop's live next-check time.
func (s *Service) intervalScheduleForecast(mode, name string, enabled bool, rawInterval string, fallback time.Duration) domain.ScheduleForecast {
	interval := fallback
	if parsed, err := domain.ParseScheduleDuration(rawInterval); err == nil && parsed >= time.Minute {
		interval = parsed
	}
	forecast := domain.ScheduleForecast{Group: "Download monitoring", Name: name, Enabled: enabled, Interval: interval.String()}
	if !enabled {
		return forecast
	}
	s.mu.RLock()
	next, tracked := s.scheduleNextAttempt[mode]
	s.mu.RUnlock()
	if !tracked {
		return forecast
	}
	runs := make([]time.Time, 0, scheduleForecastRunCount)
	for len(runs) < scheduleForecastRunCount {
		runs = append(runs, next)
		next = next.Add(interval)
	}
	forecast.NextRuns = runs
	return forecast
}
func (s *Service) releaseNotifications(ctx context.Context) {
	provider, _ := s.provider(ctx)
	for offset := 0; ; offset += 500 {
		rows, e := s.store.Releases(ctx, domain.ReleaseFilter{Limit: 500, Offset: offset})
		if e != nil {
			return
		}
		for _, r := range rows {
			if r.NotifyOnRelease && !r.Released && provider != nil {
				if results, e := provider.Search(ctx, r.VideoID); e == nil {
					for _, result := range results {
						if result.Accepted {
							v := true
							_ = s.store.PatchRelease(ctx, r.ID, &v, nil, nil, nil, nil, nil, nil, nil, nil)
							r.Released = true
							break
						}
					}
				}
			}
			if r.NotifyOnRelease && r.Released {
				_, _ = s.store.CreateNotification(ctx, r.ID, "new_release", "Release date has passed")
			}
		}
		if len(rows) < 500 {
			return
		}
	}
}
func (s *Service) pollTorrents(ctx context.Context) {
	settings, e := s.store.Settings(ctx)
	if e != nil || settings["qb_url"] == "" {
		return
	}
	qb := NewQB(settings["qb_url"], settings["qb_username"], settings["qb_password"])
	torrents, e := qb.Torrents(ctx)
	if e != nil {
		s.log.Warn("qBittorrent poll failed", "error", e)
		return
	}
	downloads, e := s.store.Downloads(ctx, "downloading")
	if e != nil {
		return
	}
	completed, e := s.store.Downloads(ctx, "completed")
	if e != nil {
		return
	}
	downloads = append(downloads, completed...)
	minRatio, _ := strconv.ParseFloat(settings["minimum_seed_ratio"], 64)
	rule := completedTorrentRule(settings["qb_completed_action"])
	for _, d := range downloads {
		s.safePollDownload(ctx, qb, d, torrents, minRatio, rule)
	}
}

// safePollDownload runs pollDownload for one row with its own panic
// recovery, so a bug triggered by one specific download (a bad file list,
// an unusual pipeline config, ...) can't take down the rest of this tick's
// batch - every other download still gets its qBittorrent status update
// this cycle even if one row's processing panics.
func (s *Service) safePollDownload(ctx context.Context, qb *QBClient, d domain.Download, torrents []Torrent, minRatio float64, rule string) {
	defer func() {
		if r := recover(); r != nil {
			s.log.Error("qBittorrent poll panicked while processing one download; other downloads still updated this tick", "download_id", d.ID, "release_id", d.ReleaseID, "video_id", d.Query, "panic", r)
		}
	}()
	s.pollDownload(ctx, qb, d, torrents, minRatio, rule)
}

func torrentHTTPFallbackReason(d domain.Download, torrent Torrent, now time.Time, fallbackDelay time.Duration) string {
	if d.Transport == "http" || d.Status != "downloading" || torrent.Progress >= 1 || d.AddedAt.IsZero() || now.Sub(d.AddedAt) < fallbackDelay {
		return ""
	}
	// Do not replace a torrent that advanced since the previous poll even if
	// its instantaneous seed count happens to be zero.
	if torrent.Progress > d.Progress+0.000001 {
		return ""
	}
	state := strings.ToLower(torrent.State)
	if !strings.Contains(state, "stalled") && !strings.Contains(state, "meta") && !strings.Contains(state, "error") {
		return ""
	}
	if torrent.Seeds <= 0 {
		return "torrent has no seeders and is not progressing"
	}
	if torrent.SeenComplete <= 0 {
		return "torrent has never been seen complete and is not progressing"
	}
	return ""
}

// tryTorrentHTTPFallback resolves HTTP before replacing anything. Once an
// exact HTTP candidate exists, ReplaceExisting atomically follows the normal
// removal path (including deleting the unusable partial torrent files) and
// queues the HTTP transfer. A missing/temporarily unavailable HTTP result
// leaves the torrent in place and is retried with a bounded cooldown.
func (s *Service) tryTorrentHTTPFallback(ctx context.Context, d *domain.Download, reason string) bool {
	settings, err := s.store.Settings(ctx)
	if err != nil || strings.TrimSpace(settings["http_download_directory"]) == "" {
		return false
	}
	release, err := s.store.Release(ctx, d.ReleaseID)
	if err != nil {
		d.PostStatus = "http_fallback_unavailable"
		d.Error = "HTTP fallback could not load release: " + err.Error()
		return false
	}
	rows, err := s.searchHTTP(ctx, release, "Automatic HTTP fallback")
	if err != nil || len(rows) == 0 {
		d.PostStatus = "http_fallback_unavailable"
		if err != nil {
			d.Error = "HTTP fallback lookup failed: " + err.Error()
		} else {
			d.Error = "HTTP fallback unavailable: JavDB returned no exact, date-compatible result"
		}
		return false
	}
	candidate := rows[0]
	candidate.ReplaceExisting = true
	downloaded, err := s.Download(ctx, release, candidate, "Automatic HTTP fallback", candidate.Link)
	if err != nil || (downloaded.Status != "queued" && downloaded.Status != "downloading") {
		d.PostStatus = "http_fallback_failed"
		if err != nil {
			d.Error = "HTTP fallback failed: " + err.Error()
		} else {
			d.Error = "HTTP fallback failed: " + firstNonEmpty(downloaded.Error, downloaded.MatchReason, downloaded.Status)
		}
		s.log.Warn("stalled torrent HTTP fallback failed", "release_id", d.ReleaseID, "video_id", d.Query, "torrent_hash", d.TorrentHash, "reason", reason, "error", d.Error)
		// A candidate was found and the replacement path was attempted. Stop
		// this poll here: it may already have removed the old history row, and
		// saving d again below would resurrect stale torrent state.
		return true
	}
	s.log.Info("stalled torrent replaced by HTTP download", "release_id", d.ReleaseID, "video_id", d.Query, "torrent_hash", d.TorrentHash, "reason", reason, "http_download_id", downloaded.ID)
	return true
}

func (s *Service) pollDownload(ctx context.Context, qb *QBClient, d domain.Download, torrents []Torrent, minRatio float64, rule string) {
	matched := false
	for _, t := range torrents {
		// A download's torrent hash is qBittorrent's own unique ID for
		// that torrent (its info-hash) - Download() always records it
		// (via verifyAddedToQBittorrent) before a row's Status ever
		// becomes "downloading" or "completed", so every row this loop
		// sees normally already has one. Once a hash is known, matching
		// MUST be by that hash alone: falling back to a name-substring
		// match even when the hash disagreed used to let this download
		// get silently re-pointed at a completely different torrent
		// whose name happened to contain the same query text (e.g. a
		// different release sharing part of a video ID) - the actual
		// torrent could have been removed from qBittorrent while this
		// row kept "monitoring" whatever unrelated torrent matched by
		// name, showing its stale/unrelated progress forever. The
		// name-substring fallback is now used only for the one
		// legitimate case where no hash has been recorded yet (a
		// brand-new row - see the comment below the loop).
		if d.TorrentHash != "" {
			if d.TorrentHash != t.Hash {
				continue
			}
		} else if !strings.Contains(canonical(t.Name), canonical(d.Query)) {
			continue
		}
		matched = true
		fallbackReason := torrentHTTPFallbackReason(d, t, time.Now(), s.torrentHTTPFallbackDelay(ctx))
		wasCompleted := d.Status == "completed"
		d.TorrentHash = t.Hash
		d.Name = t.Name
		d.SeedRatio = t.Ratio
		d.Progress = t.Progress
		d.Seeds = t.Seeds
		d.Peers = t.Peers
		d.ETASeconds = t.ETA
		d.SeenComplete = t.SeenComplete
		state := strings.ToLower(t.State)
		isCompleteState := strings.Contains(state, "upload") || strings.HasSuffix(state, "up")
		if fallbackReason != "" && s.httpFallbackDue(d.ID) {
			d.PostStatus = "http_fallback_searching"
			d.Error = ""
			_, _ = s.store.SaveDownload(ctx, d)
			if s.tryTorrentHTTPFallback(ctx, &d, fallbackReason) {
				return
			}
		}
		// The file list rarely changes once a torrent is added, so
		// it's only worth its own qBittorrent API call the first time
		// this row sees it (d.Files still empty) and once more right
		// when the torrent finishes (in case files were
		// renamed/reorganized on completion) - not unconditionally on
		// every single tick, which would multiply qBittorrent's
		// request load by however much faster qb_poll_interval_seconds
		// runs than the old fixed 1-minute ticker did.
		if len(d.Files) == 0 || (isCompleteState && !wasCompleted) {
			if files, e := qb.Files(ctx, t.Hash); e == nil {
				if raw, e := json.Marshal(files); e == nil {
					d.Files = raw
				}
			}
		}
		if isCompleteState {
			d.Status = "completed"
			_, _ = s.store.CreateNotification(ctx, d.ReleaseID, "downloaded", "Download completed")
			if !wasCompleted {
				s.log.Info("qBittorrent download completed", "download_id", d.ID, "release_id", d.ReleaseID, "video_id", d.Query, "torrent_hash", t.Hash, "state", t.State, "seed_ratio", t.Ratio, "cleanup_rule", rule)
			}
		}
		if d.Status == "completed" {
			// isPipelineInFlight is checked first, before ever looking at
			// the stored PipelineRun state, and is what actually prevents
			// the same (download, trigger) pipeline from being started
			// twice - see the race this closes below. Once the
			// in-flight goroutine finishes, it saves this row with the
			// pipeline's own terminal state, so the very next tick reads
			// a non-empty run.State and falls through the switch below
			// like normal - queued, one at a time, never blocking this
			// poll loop.
			if s.isPipelineInFlight(d.ID, pipelineDownloadCompleted) {
				// Our own background goroutine is still working on it -
				// leave PostStatus as whatever it already is
				// ("processing", set below the first time this was
				// kicked off) and just let this tick's fresh qBittorrent
				// fields above get saved as usual below.
				_, _ = s.store.SaveDownload(ctx, d)
				return
			}
			run, lookupErr := s.store.PipelineRun(ctx, d.ID, pipelineDownloadCompleted)
			switch {
			case lookupErr != nil:
				d.PostStatus = "pipeline_failed"
				d.Error = lookupErr.Error()
			case run.State == "":
				// Never started (and, per the isPipelineInFlight check
				// above, nothing already has it in flight either) - hand
				// it to the shared pipeline worker in the background
				// (see runEventPipelineAsync) instead of blocking this
				// poll tick on it, and persist the "processing"
				// transition immediately below so Download Activity
				// reflects it right away rather than staying stuck
				// showing this row's last pre-completion progress for
				// however long the pipeline takes to run. Without the
				// isPipelineInFlight guard above, a poll tick landing in
				// the window between this goroutine being kicked off and
				// runPipelineEvent actually persisting its own "running"
				// PipelineRun row would see run.State still "" here and
				// start a second one for the exact same download and
				// trigger - the shared worker would still run them one
				// at a time (never concurrently), but the event's steps
				// would still end up executing twice.
				d.PostStatus = "processing"
				s.runEventPipelineAsync(ctx, d, t, pipelineDownloadCompleted, nil)
			case run.State == "running":
				// Not in flight per the check above, so nothing in this
				// process is actually running it - most likely the app
				// restarted mid-run - so there's nothing left to wait on.
				d.PostStatus = "pipeline_interrupted"
				d.Error = "post-processing was interrupted; torrent cleanup was withheld"
			default:
				// "completed" or "failed" both mean this trigger's
				// pipeline has finished running for this download -
				// a failed step must not block ratio-based cleanup,
				// only a still-running or never-run pipeline should.
				// The failure itself remains fully recorded in the
				// pipeline run/step logs for troubleshooting; the
				// cleanup-derived status below is more useful to
				// show here than a stale "pipeline_failed".
				switch {
				case rule == completedTorrentKeep:
					if d.PostStatus != "completed_retained" {
						s.log.Info("completed qBittorrent torrent retained by cleanup rule", "download_id", d.ID, "release_id", d.ReleaseID, "video_id", d.Query, "torrent_hash", t.Hash, "pipeline_trigger", pipelineDownloadCompleted, "files_retained", true)
					}
					d.PostStatus = "completed_retained"
				case d.PostStatus == "cleanup_failed":
					// A previous removal attempt failed. Don't hammer qBittorrent
					// every poll, but do retry periodically instead of leaving the
					// torrent stranded forever - the failure may well have been
					// transient (a dropped qBittorrent session, a flaky pipeline
					// step, etc).
					if s.cleanupDue(d.ID) {
						s.cleanupTorrent(ctx, qb, &d, t, rule)
					}
				case !completedTorrentReady(rule, t.Ratio, minRatio):
					if d.PostStatus != "completed_waiting_ratio" {
						s.log.Info("completed qBittorrent torrent waiting for seed ratio", "download_id", d.ID, "release_id", d.ReleaseID, "video_id", d.Query, "torrent_hash", t.Hash, "seed_ratio", t.Ratio, "minimum_seed_ratio", minRatio, "pipeline_trigger", pipelineDownloadCompleted)
					}
					d.PostStatus = "completed_waiting_ratio"
				default:
					s.cleanupTorrent(ctx, qb, &d, t, rule)
				}
			}
		}
		_, _ = s.store.SaveDownload(ctx, d)
		return
	}
	// The torrent this download expects is gone from qBittorrent
	// entirely - not matched by hash or name against anything currently
	// listed. Only treat this as a real removal once a hash was
	// previously confirmed (d.TorrentHash != ""): a brand new download
	// can briefly show no match while qBittorrent is still resolving
	// its magnet/metadata, and that transient gap must not be mistaken
	// for someone deleting it. Skip anything this app already knows is
	// gone for a good reason (it removed it itself).
	if !matched && d.TorrentHash != "" && !downloadGoneFromQBHandled(d.PostStatus) {
		previousStatus, previousPostStatus := d.Status, d.PostStatus
		s.clearCleanupRetry(d.ID)
		d.Status = statusRemoved
		d.PostStatus = postStatusRemovedUnknown
		d.Error = ""
		s.log.Info("torrent no longer present in qBittorrent; removing from Download Activity as removed (unknown reason)", "download_id", d.ID, "release_id", d.ReleaseID, "video_id", d.Query, "torrent_hash", d.TorrentHash, "previous_status", previousStatus, "previous_post_status", previousPostStatus)
		_, _ = s.store.SaveDownload(ctx, d)
	}
}
func (s *Service) runPipelineEvent(ctx context.Context, d *domain.Download, t Torrent, trigger string) error {
	steps, e := s.store.PipelineSteps(ctx)
	if e != nil {
		s.log.Error("pipeline event could not load steps", "download_id", d.ID, "release_id", d.ReleaseID, "video_id", d.Query, "torrent_hash", t.Hash, "pipeline_trigger", trigger, "error", e)
		return e
	}
	// Each step below gets its own timeout, resolved per-step via
	// stepTimeout (that step's own TimeoutSeconds if it set one, otherwise
	// the settings-wide default) - not one timeout shared across the whole
	// ordered pipeline run. That way a step whose command legitimately runs
	// long doesn't force every other step's budget to be raised too, and a
	// slow step no longer eats into how long a later step that depends on
	// it gets to run. Everything that persists run/log state keeps using
	// the caller's ctx, so a run that times out is still recorded rather
	// than left looking like it never happened.
	run := domain.PipelineRun{DownloadID: d.ID, Trigger: trigger, State: "running", StartedAt: time.Now().UTC()}
	if e := s.store.SavePipelineRun(ctx, run); e != nil {
		return e
	}
	d.PostStatus = "processing"
	s.log.Info("pipeline event started", "download_id", d.ID, "release_id", d.ReleaseID, "video_id", d.Query, "torrent_hash", t.Hash, "pipeline_trigger", trigger)
	outputs := []string{}
	enabledSteps := 0
	failedSteps := 0
	var lastErr error
	var lastFailedStep string
	mappedPath := s.mapPath(ctx, t.ContentPath)
	for _, step := range steps {
		if !step.Enabled || step.Trigger != trigger {
			continue
		}
		enabledSteps++
		var cfg struct {
			Command    string `json:"command"`
			Query      string `json:"query"`
			OutputPath string `json:"output_path"`
		}
		_ = json.Unmarshal(step.Config, &cfg)
		stepLog, _ := s.store.SavePipelineLog(ctx, domain.PipelineLog{DownloadID: d.ID, StepID: step.ID, State: "running", Configuration: step.Config})
		timeout := s.stepTimeout(ctx, step)
		runCtx, cancel := context.WithTimeout(ctx, timeout)
		var output []byte
		if step.Type == "shell" {
			targetPath := renderPipelineValueTemplate(cfg.OutputPath, trigger, mappedPath, *d, t)
			cmd := exec.CommandContext(runCtx, "sh", "-c", cfg.Command)
			cmd.WaitDelay = pipelineKillGrace
			cmd.Env = append(cmd.Environ(), "JAVBEACON_PIPELINE_EVENT="+trigger, "JAVBEACON_DOWNLOAD_PATH="+mappedPath, "JAVBEACON_TARGET_PATH="+targetPath, "JAVBEACON_RELEASE_ID="+d.Query, "JAVBEACON_RELEASE_DB_ID="+strconv.FormatInt(d.ReleaseID, 10), "JAVBEACON_TORRENT_HASH="+t.Hash)
			output, e = cmd.CombinedOutput()
			e = clarifyPipelineTimeout(runCtx, e, timeout)
			if e == nil && targetPath != "" {
				mappedPath = targetPath
			}
		} else {
			output, e = s.stashOperation(runCtx, renderPipelineTemplate(cfg.Query, trigger, mappedPath, *d, t))
			e = clarifyPipelineTimeout(runCtx, e, timeout)
		}
		cancel()
		outputs = append(outputs, step.Name+": "+string(output))
		if e != nil {
			// A single failed step (e.g. a curl call that errors) must not
			// abort the whole run - the remaining enabled steps for this
			// trigger still get a chance to execute. The failure is logged
			// per-step here (and recorded in the overall run below) so it
			// stays fully visible for troubleshooting; runPipelineEvent's
			// caller decides separately whether a failed run should still
			// block anything downstream (pollTorrents's ratio-based
			// qBittorrent cleanup deliberately does not).
			stepLog.State = "failed"
			stepLog.Output = string(output)
			stepLog.Error = e.Error()
			_, _ = s.store.SavePipelineLog(ctx, stepLog)
			failedSteps++
			lastErr = e
			lastFailedStep = step.Name
			s.log.Error("pipeline step failed; continuing with remaining steps", "download_id", d.ID, "release_id", d.ReleaseID, "video_id", d.Query, "torrent_hash", t.Hash, "pipeline_trigger", trigger, "pipeline_step", step.Name, "pipeline_step_type", step.Type, "pipeline_step_timeout", timeout.String(), "error", e, "output", truncatePipelineOutput(string(output)))
			continue
		}
		stepLog.State = "completed"
		stepLog.Output = string(output)
		_, _ = s.store.SavePipelineLog(ctx, stepLog)
		s.log.Info("pipeline step completed", "download_id", d.ID, "release_id", d.ReleaseID, "video_id", d.Query, "torrent_hash", t.Hash, "pipeline_trigger", trigger, "pipeline_step", step.Name, "pipeline_step_type", step.Type, "pipeline_step_timeout", timeout.String(), "output", truncatePipelineOutput(string(output)))
	}
	d.QBResponse = strings.Join(outputs, "\n")
	if failedSteps > 0 {
		d.PostStatus = "pipeline_failed"
		d.Error = fmt.Sprintf("%d of %d pipeline step(s) failed; last failure in %s: %v", failedSteps, enabledSteps, lastFailedStep, lastErr)
		run.State, run.Error, run.FinishedAt = "failed", d.Error, time.Now().UTC()
		if e := s.store.SavePipelineRun(ctx, run); e != nil {
			return e
		}
		s.log.Error("pipeline event finished with failures", "download_id", d.ID, "release_id", d.ReleaseID, "video_id", d.Query, "torrent_hash", t.Hash, "pipeline_trigger", trigger, "pipeline_steps", enabledSteps, "failed_steps", failedSteps)
		return errors.New(d.Error)
	}
	run.State, run.FinishedAt = "completed", time.Now().UTC()
	if e := s.store.SavePipelineRun(ctx, run); e != nil {
		return e
	}
	d.PostStatus = "pipeline_completed"
	d.Error = ""
	s.log.Info("pipeline event completed", "download_id", d.ID, "release_id", d.ReleaseID, "video_id", d.Query, "torrent_hash", t.Hash, "pipeline_trigger", trigger, "pipeline_steps", enabledSteps)
	return nil
}
func (s *Service) cleanupTorrent(ctx context.Context, qb QBittorrent, d *domain.Download, t Torrent, cleanupRule string) {
	if e := qb.Remove(ctx, t.Hash); e != nil {
		d.PostStatus = "cleanup_failed"
		d.Error = "remove completed torrent: " + e.Error()
		s.log.Error("completed qBittorrent torrent removal failed", "download_id", d.ID, "release_id", d.ReleaseID, "video_id", d.Query, "torrent_hash", t.Hash, "cleanup_rule", cleanupRule, "files_retained", true, "error", e)
		return
	}
	s.clearCleanupRetry(d.ID)
	d.PostStatus = "completed_removed"
	s.log.Info("completed qBittorrent torrent removed", "download_id", d.ID, "release_id", d.ReleaseID, "video_id", d.Query, "torrent_hash", t.Hash, "cleanup_rule", cleanupRule, "seed_ratio", t.Ratio, "files_retained", true)
	// The post-removal pipeline runs on the shared serialized worker in the
	// background (see runEventPipelineAsync) rather than blocking here -
	// the torrent is already gone from qBittorrent at this point, so no
	// further poll tick will touch this row's qBittorrent-derived fields
	// regardless of how long the pipeline takes; only its own outcome
	// fields get applied once it finishes. cleanupTorrent is never actually
	// re-entered for the same download while its own post-removal pipeline
	// is still running (qb.Remove already succeeded above, so this row
	// stops matching any live torrent from here on), but the
	// isPipelineInFlight guard is kept anyway so this trigger is never
	// started twice for the same download regardless of how this function
	// ends up called in the future.
	if s.isPipelineInFlight(d.ID, pipelineDownloadRemoved) {
		return
	}
	hash := t.Hash
	s.runEventPipelineAsync(ctx, *d, t, pipelineDownloadRemoved, func(cur *domain.Download, e error) {
		// Unlike the completed-download pipeline (pollDownload), there is
		// no later poll tick that revisits this row's PostStatus - the
		// torrent is already gone from qBittorrent, so once this callback
		// runs it's the last word. Explicitly restore "completed_removed"
		// on success rather than leaving whatever runPipelineEvent itself
		// set (its own generic "pipeline_completed"), since
		// downloadGoneFromQBHandled specifically checks for
		// "completed_removed" to know this row's disappearance from
		// qBittorrent was expected - anything else would make the very
		// next poll tick misclassify it as removed for an unknown reason.
		if e != nil {
			cur.PostStatus = "removed_pipeline_failed"
			s.log.Error("post-removal pipeline failed after qBittorrent torrent removal", "download_id", cur.ID, "release_id", cur.ReleaseID, "video_id", cur.Query, "torrent_hash", hash, "cleanup_rule", cleanupRule, "pipeline_trigger", pipelineDownloadRemoved, "files_retained", true, "error", e)
			return
		}
		cur.PostStatus = "completed_removed"
	})
}
func renderPipelineTemplate(query, trigger, path string, d domain.Download, t Torrent) string {
	escape := func(value string) string {
		quoted, _ := json.Marshal(value)
		if len(quoted) >= 2 {
			return string(quoted[1 : len(quoted)-1])
		}
		return value
	}
	return strings.NewReplacer("{{event}}", escape(trigger), "{{download_path}}", escape(path), "{{release_id}}", escape(d.Query), "{{release_db_id}}", strconv.FormatInt(d.ReleaseID, 10), "{{torrent_hash}}", escape(t.Hash)).Replace(query)
}
func renderPipelineValueTemplate(value, trigger, path string, d domain.Download, t Torrent) string {
	return strings.NewReplacer("{{event}}", trigger, "{{download_path}}", path, "{{release_id}}", d.Query, "{{release_db_id}}", strconv.FormatInt(d.ReleaseID, 10), "{{torrent_hash}}", t.Hash).Replace(value)
}
func (s *Service) mapPath(ctx context.Context, path string) string {
	rows, _ := s.store.PathMappings(ctx)
	for _, m := range rows {
		if strings.HasPrefix(path, m.DownloadPrefix) {
			return m.LocalPrefix + strings.TrimPrefix(path, m.DownloadPrefix)
		}
	}
	return path
}
func (s *Service) stashOperation(ctx context.Context, query string) ([]byte, error) {
	settings, e := s.store.Settings(ctx)
	if e != nil {
		return nil, e
	}
	base := strings.TrimRight(settings["stash_base_url"], "/")
	if base == "" {
		return nil, errors.New("StashApp Base URL is not configured")
	}
	body, _ := json.Marshal(map[string]string{"query": query})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, base+"/graphql", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if key := settings["stash_api_key"]; key != "" {
		req.Header.Set("ApiKey", key)
	}
	resp, e := s.client.Do(req)
	if e != nil {
		return nil, e
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return out, fmt.Errorf("StashApp returned HTTP %d", resp.StatusCode)
	}
	return out, nil
}

// truncatePipelineOutput bounds step output before it is written to the
// application log, so a chatty shell command or a large StashApp response
// cannot flood the Live Application Log.
func truncatePipelineOutput(output string) string {
	const max = 2000
	if len(output) <= max {
		return output
	}
	return output[:max] + fmt.Sprintf("... (truncated, %d bytes total)", len(output))
}

// TestPipelineStep runs a single Ordered event pipeline step in isolation,
// using synthetic sample values instead of a real download/torrent, so a
// user can verify a step (shell or StashApp) from the Settings UI without
// waiting for a real download event. It reuses the exact same execution
// path as runPipelineEvent (same env vars / template placeholders) so a
// passing test is representative of the real run. It does not persist a
// PipelineRun/PipelineLog row - it is not tied to a real download - but the
// result is recorded in the application log for troubleshooting, matching
// runPipelineEvent's logging.
func (s *Service) TestPipelineStep(ctx context.Context, step domain.PipelineStep) (string, error) {
	var cfg struct {
		Command    string `json:"command"`
		Query      string `json:"query"`
		OutputPath string `json:"output_path"`
	}
	_ = json.Unmarshal(step.Config, &cfg)
	trigger := step.Trigger
	if trigger == "" {
		trigger = pipelineDownloadCompleted
	}
	sample := domain.Download{ReleaseID: 0, Query: "TEST-001"}
	torrent := Torrent{Hash: "0000000000000000000000000000000000000000000000000000000000000000000000000000"}
	mappedPath := s.mapPath(ctx, "/downloads/TEST-001/TEST-001.mkv")
	timeout := s.stepTimeout(ctx, step)
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var output []byte
	var e error
	switch step.Type {
	case "shell":
		if strings.TrimSpace(cfg.Command) == "" {
			return "", errors.New("this shell step has no command configured")
		}
		targetPath := renderPipelineValueTemplate(cfg.OutputPath, trigger, mappedPath, sample, torrent)
		cmd := exec.CommandContext(runCtx, "sh", "-c", cfg.Command)
		cmd.WaitDelay = pipelineKillGrace
		cmd.Env = append(cmd.Environ(), "JAVBEACON_PIPELINE_EVENT="+trigger, "JAVBEACON_DOWNLOAD_PATH="+mappedPath, "JAVBEACON_TARGET_PATH="+targetPath, "JAVBEACON_RELEASE_ID="+sample.Query, "JAVBEACON_RELEASE_DB_ID="+strconv.FormatInt(sample.ReleaseID, 10), "JAVBEACON_TORRENT_HASH="+torrent.Hash)
		output, e = cmd.CombinedOutput()
		e = clarifyPipelineTimeout(runCtx, e, timeout)
	default:
		if strings.TrimSpace(cfg.Query) == "" {
			return "", errors.New("this StashApp step has no query configured")
		}
		output, e = s.stashOperation(runCtx, renderPipelineTemplate(cfg.Query, trigger, mappedPath, sample, torrent))
		e = clarifyPipelineTimeout(runCtx, e, timeout)
	}
	result := truncatePipelineOutput(string(output))
	if e != nil {
		s.log.Error("pipeline step test failed", "pipeline_step", step.Name, "pipeline_step_type", step.Type, "pipeline_trigger", trigger, "error", e, "output", result)
		return string(output), e
	}
	s.log.Info("pipeline step test passed", "pipeline_step", step.Name, "pipeline_step_type", step.Type, "pipeline_trigger", trigger, "output", result)
	return string(output), nil
}

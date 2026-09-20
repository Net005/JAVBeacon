package download

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode"

	"github.com/Net005/JAVBeacon/internal/domain"
	"github.com/Net005/JAVBeacon/internal/scraper"
	"golang.org/x/net/html"
)

const (
	defaultJavDBURL      = "https://javdb.com"
	pikPakAPIHost        = "https://api-drive.mypikpak.com"
	pikPakUserHost       = "https://user.mypikpak.com"
	pikPakClientID       = "YUMx5nI8ZU8Ap8pm"
	pikPakClientSecret   = "dbw2OtmVEeuUvIptb1Coyg"
	pikPakClientVersion  = "2.0.0"
	pikPakPackageName    = "mypikpak.com"
	publicShareUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/117.0.0.0 Safari/537.36"
)

var pikPakAlgorithms = []string{
	"C9qPpZLN8ucRTaTiUMWYS9cQvWOE", "+r6CQVxjzJV6LCV", "F", "pFJRC",
	"9WXYIDGrwTCz2OiVlgZa90qpECPD6olt", "/750aCr4lm/Sly/c", "RB+DT/gZCrbV", "",
	"CyLsf7hdkIRxRm215hl", "7xHvLi2tOYP0Y92b", "ZGTXXxu8E/MIWaEDB+Sm/", "1UI3",
	"E7fP5Pfijd+7K+t6Tg/NhuLq0eEUVChpJSkrKxpO", "ihtqpG6FMt65+Xk+tWUH2", "NhXXU9rg4XXdzo7u5o",
}

var pikPakRestoreRetryDelay = time.Second

type javDBProvider struct {
	client              *http.Client
	baseURL             string
	acceptedPatterns    []PreferredFilenamePattern
	blacklistedPatterns []string
	pikPakUsername      string
	pikPakPassword      string
	cleanupRestored     bool
	allowFolderMatch    bool
	authenticate        func(context.Context, string, string) (*pikPakClient, error)
	log                 *slog.Logger
	inspectCandidate    func(context.Context, string, string) (pikPakFile, []pikPakFile, error)
	solverPool          *scraper.SolverPool
	gluetun             *gluetunRotator
}

// HTTPSourceProvider is the extension point for direct-download sources.
// Provider modules own discovery and link resolution; the shared download
// service owns queuing, concurrency, progress, retry, naming, and pipelines.
type HTTPSourceProvider interface {
	Name() string
	Search(context.Context, domain.Release) ([]domain.SearchResult, error)
	CanResolve(domain.Download) bool
	Resolve(context.Context, domain.Download) (resolvedHTTPFile, error)
}

type resolvedHTTPFile struct {
	URL              string
	Name             string
	Size             int64
	Headers          map[string]string
	Checksum         string
	ChecksumType     string
	Authenticated    bool
	RestoredFileID   string
	RestoredParentID string
	NewlyRestored    bool
	Cleanup          func(context.Context) error
}

func httpSourceProviders(client *http.Client, settings map[string]string, logger *slog.Logger, authenticate func(context.Context, string, string) (*pikPakClient, error), solverPool *scraper.SolverPool, rotationMu *sync.Mutex) []HTTPSourceProvider {
	patterns := ParsePreferredFilenamePatterns(settings["accepted_patterns"])
	blacklist := ParseBlacklistedFilenamePatterns(settings["blacklisted_filename_patterns"])
	return []HTTPSourceProvider{&javDBProvider{
		client:              client,
		baseURL:             settings["javdb_url"],
		acceptedPatterns:    patterns,
		blacklistedPatterns: blacklist,
		pikPakUsername:      strings.TrimSpace(settings["pikpak_username"]),
		pikPakPassword:      settings["pikpak_password"],
		cleanupRestored:     settings["pikpak_cleanup_restored"] == "true",
		allowFolderMatch:    settings["pikpak_release_id_folder_fallback"] == "true",
		authenticate:        authenticate,
		log:                 logger,
		solverPool:          solverPool,
		gluetun:             newGluetunRotator(client, settings, logger, rotationMu),
	}}
}

func (p *javDBProvider) Name() string { return "JavDB / Keepshare" }

func (p *javDBProvider) CanResolve(download domain.Download) bool {
	if download.Provider == p.Name() {
		return true
	}
	u, err := url.Parse(download.SourceReference)
	return err == nil && isJavDBShareURL(u)
}

func (p *javDBProvider) Resolve(ctx context.Context, download domain.Download) (resolvedHTTPFile, error) {
	if (p.pikPakUsername == "") != (p.pikPakPassword == "") {
		return resolvedHTTPFile{}, errors.New("PikPak account configuration is incomplete: configure both username and password, or clear both")
	}
	if err := validateJavDBShareReference(download.SourceReference); err != nil {
		return resolvedHTTPFile{}, err
	}
	resolveCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	ctx = resolveCtx
	// Restoring a share mutates the user's drive, so this used to cap the
	// authenticated path at a single attempt to avoid repeating that
	// mutation blindly on every transient failure (a timed-out API call,
	// not necessarily a failed restore). restoreSharedFile now checks the
	// restore area's existing files for an exact name+size match before ever
	// calling PikPak's restore endpoint again, so a retry here reuses an
	// already-restored file instead of duplicating it - the same 3 attempts
	// as the unauthenticated path is safe.
	attempts := 3
	var resolved resolvedHTTPFile
	var err error
resolveAttempts:
	for attempt := 0; attempt < attempts; attempt++ {
		if attempt > 0 {
			timer := time.NewTimer(time.Duration(attempt) * 500 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				break resolveAttempts
			case <-timer.C:
			}
		}
		if p.pikPakUsername != "" && p.pikPakPassword != "" {
			resolved, err = resolveAuthenticatedPikPakShareWithFolderFallback(ctx, p.client, download.SourceReference, download.Query, p.acceptedPatterns, download.ProviderFileID, download.Name, download.BytesTotal, p.pikPakUsername, p.pikPakPassword, p.cleanupRestored, p.authenticate, p.allowFolderMatch)
		} else {
			resolved, err = resolvePikPakShareWithFolderFallback(ctx, p.client, download.SourceReference, download.Query, p.acceptedPatterns, download.ProviderFileID, download.Name, download.BytesTotal, p.allowFolderMatch)
		}
		if err == nil {
			return resolved, nil
		}
		if ctx.Err() != nil {
			break
		}
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return resolvedHTTPFile{}, fmt.Errorf("PikPak resolution timed out after 2 minutes: %w", ctx.Err())
	}
	return resolvedHTTPFile{}, fmt.Errorf("PikPak resolution failed after %d attempts: %w", attempts, err)
}

// validateJavDBShareReference rejects a download's SourceReference before any
// PikPak API call is attempted. A JavDB release detail page sometimes
// publishes only a magnet/torrent link with no Keepshare/PikPak mirror at
// all - discoverJavDBDownloads correctly leaves such a candidate
// unaccepted/unlinked, but a queued Download row can still end up here with
// an empty, magnet:, or otherwise non-HTTP SourceReference (for example a
// retried "not available" placeholder). Without this check, Resolve fell
// through to discoverPikPakShareID issuing a raw http.Client request against
// that reference, which surfaced as a baffling three-attempt "PikPak
// resolution failed ... unsupported protocol scheme" error instead of a
// clear one - and burned the same 3-attempt/backoff loop on a failure that
// is never transient. Rejecting it immediately here also lets the existing
// failed-HTTP-download Torrent fallback (tryFailedHTTPTorrentFallback) kick
// in right away instead of waiting out that pointless retry loop. A
// well-formed Keepshare/PikPak share link is unaffected and continues on to
// the normal resolution attempts below.
func validateJavDBShareReference(raw string) error {
	ref := strings.TrimSpace(raw)
	if ref == "" {
		return errors.New("no Keepshare/PikPak share link is available for this download - JavDB likely only lists a magnet/torrent link for this release, or has not published a download mirror yet; use Torrent download instead")
	}
	u, err := url.Parse(ref)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return fmt.Errorf("download source reference %q is not an HTTP(S) Keepshare/PikPak share link (likely a magnet/torrent link) - use Torrent download instead", ref)
	}
	if !isJavDBShareURL(u) {
		return fmt.Errorf("download source reference %q is not a recognized Keepshare/PikPak share link", ref)
	}
	return nil
}

func normalizeReleaseID(s string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(strings.TrimSpace(s)) {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func releaseIDsEqual(a, b string) bool {
	a, b = normalizeReleaseID(a), normalizeReleaseID(b)
	return a != "" && a == b
}

var javDBLabeledIDPattern = regexp.MustCompile(`(?i)\bID\s*[:：]\s*([A-Z]{2,}[-_ .]*[0-9]{2,}(?:[-_ .]*U)?)`)

func extractJavDBReleaseID(text string) string {
	text = strings.TrimSpace(text)
	if match := javDBLabeledIDPattern.FindStringSubmatch(text); len(match) > 1 {
		return strings.TrimSpace(match[1])
	}
	return text
}

func releaseIDMatchesText(text, releaseID string) bool {
	parts := regexp.MustCompile(`[A-Z]+|[0-9]+`).FindAllString(strings.ToUpper(normalizeReleaseID(releaseID)), -1)
	if len(parts) == 0 {
		return false
	}
	pattern := `(?i)(?:^|[^A-Z0-9])` + strings.Join(parts, `[-_ .]*`) + `(?:[-_ .]*U)?(?:[^A-Z0-9]|$)`
	return regexp.MustCompile(pattern).MatchString(text)
}

type javDBSearchHit struct {
	href string
	id   string
	date string
}

type javDBDownloadDiscovery struct {
	rows                 []domain.SearchResult
	downloadSectionFound bool
	shareLinkCount       int
	pikPakLinkCount      int
	actionURLs           []string
}

// httpCandidateInspectionConcurrency caps how many JavDB HTTP search
// candidates are inspected against PikPak at once (see the comment above
// the inspection loop in Search for why this is bounded rather than
// unlimited or 1).
const httpCandidateInspectionConcurrency = 3

// httpSearchProgress tracks the live "N of M candidates inspected" state for
// an in-flight JavDB HTTP search, keyed by release ID. A single HTTP search
// is one blocking call (Service.SearchHTTP / the /releases/{id}/search?
// provider=http endpoint) that can legitimately take over a minute when a
// release has a dozen-plus mirrors, each needing its own PikPak round trips
// - this lets the frontend poll HTTPSearchProgress while that request is
// still in flight instead of showing a static "Searching…" the whole time.
var httpSearchProgress = struct {
	mu   sync.Mutex
	byID map[int64]*httpSearchProgressState
}{byID: map[int64]*httpSearchProgressState{}}

type httpSearchProgressState struct {
	total     int
	completed int32 // accessed atomically
}

func startHTTPSearchProgress(releaseID int64, total int) {
	httpSearchProgress.mu.Lock()
	httpSearchProgress.byID[releaseID] = &httpSearchProgressState{total: total}
	httpSearchProgress.mu.Unlock()
}

func advanceHTTPSearchProgress(releaseID int64) {
	httpSearchProgress.mu.Lock()
	state := httpSearchProgress.byID[releaseID]
	httpSearchProgress.mu.Unlock()
	if state != nil {
		atomic.AddInt32(&state.completed, 1)
	}
}

func finishHTTPSearchProgress(releaseID int64) {
	httpSearchProgress.mu.Lock()
	delete(httpSearchProgress.byID, releaseID)
	httpSearchProgress.mu.Unlock()
}

// HTTPSearchProgress reports live candidate-inspection progress for release
// ID's in-flight JavDB HTTP search, if one is currently running. active is
// false once the search has finished (or none is running), at which point
// the caller already has - or is about to have - the real, final results.
func HTTPSearchProgress(releaseID int64) (completed, total int, active bool) {
	httpSearchProgress.mu.Lock()
	state := httpSearchProgress.byID[releaseID]
	httpSearchProgress.mu.Unlock()
	if state == nil {
		return 0, 0, false
	}
	return int(atomic.LoadInt32(&state.completed)), state.total, true
}

func (p *javDBProvider) Search(ctx context.Context, release domain.Release) ([]domain.SearchResult, error) {
	base := strings.TrimRight(strings.TrimSpace(p.baseURL), "/")
	if base == "" {
		base = defaultJavDBURL
	}
	searchURL := base + "/search?q=" + url.QueryEscape(release.VideoID) + "&f=all"
	doc, searchStatus, err := p.getHTML(ctx, searchURL)
	if err != nil {
		return nil, fmt.Errorf("JavDB search request failed (video_id=%s search_url=%s status=%d): %w", release.VideoID, searchURL, searchStatus, err)
	}
	hits := parseJavDBSearchHits(doc, base)
	if len(hits) == 0 {
		return nil, fmt.Errorf("JavDB search page returned no parsable release results (video_id=%s normalized_video_id=%s search_url=%s status=%d)", release.VideoID, normalizeReleaseID(release.VideoID), searchURL, searchStatus)
	}
	exact := make([]javDBSearchHit, 0, len(hits))
	for _, hit := range hits {
		if releaseIDsEqual(hit.id, release.VideoID) {
			exact = append(exact, hit)
			if p.log != nil && hit.id != release.VideoID {
				p.log.Info("JavDB HTTP search matched canonical release ID", "requested_id", release.VideoID, "normalized_requested_id", normalizeReleaseID(release.VideoID), "javdb_id", hit.id, "normalized_javdb_id", normalizeReleaseID(hit.id))
			}
		}
	}
	if len(exact) == 0 {
		return nil, fmt.Errorf("JavDB returned search results but no exact release ID match (requested_id=%s normalized_requested_id=%s search_results=%d)", release.VideoID, normalizeReleaseID(release.VideoID), len(hits))
	}
	var rows []domain.SearchResult
	var unavailable []domain.SearchResult
	var stageErrors []string
	for _, h := range exact {
		pageDate := parseJavDBDate(h.date)
		page, detailStatus, getErr := p.getHTML(ctx, h.href)
		if getErr != nil {
			stageErrors = append(stageErrors, fmt.Sprintf("JavDB exact release found but detail page fetch failed (requested_id=%s matched_id=%s detail_url=%s status=%d): %v", release.VideoID, h.id, h.href, detailStatus, getErr))
			continue
		}
		detailID := parseJavDBDetailID(page)
		if detailID != "" && !releaseIDsEqual(detailID, release.VideoID) {
			stageErrors = append(stageErrors, fmt.Sprintf("JavDB detail page release ID did not match (requested_id=%s normalized_requested_id=%s detail_page_id=%s normalized_detail_page_id=%s detail_url=%s)", release.VideoID, normalizeReleaseID(release.VideoID), detailID, normalizeReleaseID(detailID), h.href))
			continue
		}
		discovery := discoverJavDBDownloads(page, h.href, release.VideoID)
		for _, actionURL := range discovery.actionURLs {
			actionPage, actionStatus, actionErr := p.getHTML(ctx, actionURL)
			if actionErr != nil {
				stageErrors = append(stageErrors, fmt.Sprintf("JavDB exact release found but download action fetch failed (requested_id=%s matched_id=%s download_url=%s status=%d): %v", release.VideoID, h.id, actionURL, actionStatus, actionErr))
				continue
			}
			mergeJavDBDiscovery(&discovery, discoverJavDBDownloads(actionPage, actionURL, release.VideoID))
		}
		if len(discovery.rows) == 0 {
			if discovery.downloadSectionFound {
				reason := "Exact JavDB release ID matched, but no Keepshare/PikPak download link is currently published"
				unavailable = append(unavailable, domain.SearchResult{
					Provider:    p.Name(),
					Title:       h.id,
					SourceURL:   h.href,
					Transport:   "http",
					PublishedAt: formatOptionalDate(pageDate),
					Accepted:    false,
					Reason:      reason,
					Unavailable: true,
				})
				if p.log != nil {
					p.log.Warn("JavDB exact release has no downloadable HTTP share", "requested_id", release.VideoID, "normalized_id", normalizeReleaseID(release.VideoID), "matched_id", h.id, "stored_date", release.ReleaseDate, "javdb_date", formatOptionalDate(pageDate), "detail_url", h.href, "detail_status", detailStatus, "download_section_found", true, "keepshare_links", discovery.shareLinkCount, "pikpak_links", discovery.pikPakLinkCount, "reason", reason)
				}
			} else {
				stageErrors = append(stageErrors, fmt.Sprintf("JavDB exact release found but download section could not be parsed (requested_id=%s matched_id=%s detail_url=%s detail_status=%d)", release.VideoID, h.id, h.href, detailStatus))
			}
			continue
		}
		rows = appendUniqueJavDBRows(rows, discovery.rows...)
		if p.log != nil {
			p.log.Info("JavDB HTTP search matched release", "requested_id", release.VideoID, "normalized_id", normalizeReleaseID(release.VideoID), "search_url", searchURL, "search_status", searchStatus, "search_results", len(hits), "exact_matches", len(exact), "matched_id", h.id, "stored_date", release.ReleaseDate, "javdb_date", formatOptionalDate(pageDate), "detail_url", h.href, "detail_status", detailStatus, "detail_page_id", detailID, "download_section_found", discovery.downloadSectionFound, "keepshare_links", discovery.shareLinkCount, "pikpak_links", discovery.pikPakLinkCount, "candidates", len(discovery.rows))
		}
	}
	if len(rows) == 0 {
		if len(unavailable) > 0 {
			return unavailable, nil
		}
		return nil, errors.New(strings.Join(stageErrors, "; "))
	}
	// JavDB's visible row title is not always the actual video filename in
	// the Keepshare/PikPak share. Inspect every distinct candidate now so
	// preferred filename patterns rank the real downloadable file. A blocked,
	// expired, or otherwise uninspectable share remains visible for diagnosis,
	// but is never left accepted or queued as a placeholder folder.
	// Inspect shares with bounded concurrency (httpCandidateInspectionConcurrency
	// at a time), not one at a time and not all at once. Each inspection
	// creates its own anonymous PikPak session/CAPTCHA token, and firing every
	// share at once made otherwise-valid public shares fail transiently -
	// leaving the search card with only its JavDB row title, and a later
	// download resolution would then appear to "discover" the preferred
	// filename after the search had already missed it. A handful of releases
	// legitimately publish a dozen-plus mirrors, and each one is a handful of
	// real network round trips (discover the PikPak share ID, mint a CAPTCHA
	// token, list the share), so doing them fully serially could leave a
	// user's Search & Download dialog watching a plain "Searching…" for a
	// minute or more; pikPakRequestThrottle (see below) still paces the
	// actual PikPak request issuance regardless of how many of these run
	// concurrently, so this does not defeat the anti-abuse spacing that
	// motivated the original serial design - it only lets the network
	// round-trip time of up to httpCandidateInspectionConcurrency inspections
	// overlap instead of queuing behind each other. Each goroutine only ever
	// writes rows[i] for its own index, so no two goroutines touch the same
	// row; the shared failure counter and progress counter are the only
	// state they contend over. Sorting afterward does not depend on
	// inspection order, so the final ranking stays deterministic.
	startHTTPSearchProgress(release.ID, len(rows))
	defer finishHTTPSearchProgress(release.ID)
	var (
		inspectionFailures int32
		wg                 sync.WaitGroup
		sem                = make(chan struct{}, httpCandidateInspectionConcurrency)
	)
	for i := range rows {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			defer advanceHTTPSearchProgress(release.ID)
			inspect := p.inspectSearchCandidate
			if p.inspectCandidate != nil {
				inspect = p.inspectCandidate
			}
			selected, files, inspectErr := inspect(ctx, rows[i].Link, release.VideoID)
			if inspectErr != nil {
				atomic.AddInt32(&inspectionFailures, 1)
				rows[i].Accepted = false
				rows[i].ProviderFileID = ""
				rows[i].Reason = "release ID matched; Keepshare filename inspection failed: " + inspectErr.Error()
				return
			}
			rows[i].Title = selected.Name
			rows[i].MatchedFile = selected.Name
			rows[i].ProviderFileID = selected.ID
			rows[i].PreferredFilenameMatch, _, rows[i].PreferredFilenamePriority = matchesAcceptedHTTPPattern(selected.Name, p.acceptedPatterns)
			if blacklisted, pattern := matchesBlacklistedFilename(selected.Name, p.blacklistedPatterns); blacklisted {
				rows[i].Accepted = false
				rows[i].BlacklistedFilenameMatch = true
				rows[i].PreferredFilenameMatch = false
				rows[i].PreferredFilenamePriority = 0
				rows[i].Reason = fmt.Sprintf("filename matched blacklist pattern %s: %s", pattern, selected.Name)
			}
			if selected.FolderReleaseMatch {
				rows[i].Reason = "exact release-ID PikPak folder fallback selected the highest-priority/largest child video"
			}
			rows[i].Files, rows[i].FileDetails = pikPakSearchFiles(files, selected)
			if size, parseErr := strconv.ParseInt(selected.Size, 10, 64); parseErr == nil && size > 0 {
				rows[i].SizeBytes = size
			}
		}(i)
	}
	wg.Wait()
	sortJavDBDownloadCandidates(rows, release.VideoID, p.acceptedPatterns)
	rows = append(rows, unavailable...)
	if p.log != nil {
		p.log.Info("JavDB HTTP candidate inspection completed", "requested_id", release.VideoID, "normalized_id", normalizeReleaseID(release.VideoID), "candidate_inspection_count", len(rows), "candidate_inspection_failures", inspectionFailures, "final_candidate_count", len(rows))
	}
	return rows, nil
}

func (p *javDBProvider) inspectSearchCandidate(ctx context.Context, link, releaseID string) (pikPakFile, []pikPakFile, error) {
	_, _, selected, files, err := inspectPikPakShareWithFolderFallback(ctx, p.client, link, releaseID, p.acceptedPatterns, "", "", 0, p.allowFolderMatch)
	if err == nil || ctx.Err() != nil {
		return selected, files, err
	}
	// Anonymous share endpoints occasionally reject a freshly-created token.
	// One short, context-aware retry is enough to recover without making a
	// genuinely blocked or expired share stall the whole search.
	timer := time.NewTimer(250 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return pikPakFile{}, nil, ctx.Err()
	case <-timer.C:
	}
	_, _, selected, files, err = inspectPikPakShareWithFolderFallback(ctx, p.client, link, releaseID, p.acceptedPatterns, "", "", 0, p.allowFolderMatch)
	return selected, files, err
}

func sortJavDBDownloadCandidates(rows []domain.SearchResult, releaseID string, patterns []PreferredFilenamePattern) {
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].BlacklistedFilenameMatch != rows[j].BlacklistedFilenameMatch {
			return !rows[i].BlacklistedFilenameMatch
		}
		iPreferred, _, iPriority := matchesAcceptedHTTPPattern(rows[i].Title, patterns)
		jPreferred, _, jPriority := matchesAcceptedHTTPPattern(rows[j].Title, patterns)
		if iPreferred != jPreferred {
			return iPreferred
		}
		if iPreferred && iPriority != jPriority {
			return iPriority < jPriority
		}
		iU, jU := hasUVariant(rows[i].Title, releaseID), hasUVariant(rows[j].Title, releaseID)
		if iU != jU {
			return !iU
		}
		return rows[i].SizeBytes > rows[j].SizeBytes
	})
	for i := range rows {
		if rows[i].BlacklistedFilenameMatch {
			continue
		}
		if preferred, pattern, priority := matchesAcceptedHTTPPattern(rows[i].Title, patterns); preferred {
			rows[i].PreferredFilenameMatch = true
			rows[i].PreferredFilenamePriority = priority
			preferredReason := fmt.Sprintf("preferred HTTP filename matched priority %d pattern %s", priority, pattern)
			if strings.Contains(rows[i].Reason, "release-ID PikPak folder fallback") {
				rows[i].Reason += "; " + preferredReason
			} else {
				rows[i].Reason = preferredReason
			}
		}
	}
}

func matchesAcceptedHTTPPattern(name string, patterns []PreferredFilenamePattern) (bool, string, int) {
	patterns = normalizePreferredFilenamePatterns(patterns)
	if len(patterns) == 0 {
		patterns = defaultPreferredFilenamePatternRows()
	}
	name = strings.ToLower(name)
	for _, item := range patterns {
		pattern := strings.TrimSpace(item.Pattern)
		if pattern != "" && strings.Contains(name, strings.ToLower(pattern)) {
			return true, pattern, item.Priority
		}
	}
	return false, "", 0
}

// javDBRequestThrottle enforces a randomized 3-7s cooldown between
// consecutive requests to JavDB, across every release/search goroutine that
// shares this process. A single release's exact-match search alone can hit
// the search page, a detail page per exact hit, and a download-action page
// per detail page - back to back with no throttle, that reads to JavDB as a
// scripted hammering pattern. The cooldown is process-global (not per
// release) so a Release Library bulk run, which searches one release after
// another, keeps the same minimum spacing between every JavDB request it
// makes, not just within a single release's search. It backs off further,
// automatically, when JavDB itself starts timing out or blocking requests.
var javDBRequestThrottle = newRequestThrottle(3*time.Second, 7*time.Second, 45*time.Second)

// pikPakRequestThrottle applies the same idea to PikPak's own API. Its base
// window is deliberately much lighter than JavDB's: a single restore poll
// (findRestoredFile) can legitimately walk dozens of account folders one API
// call at a time, and a flat 3-7s per call there would turn a few-second
// poll into many minutes. The adaptive backoff is where this actually earns
// its keep - it grows sharply once PikPak's API starts timing out under load
// (the "context deadline exceeded" failures this was added to reduce),
// spacing out a Release Library bulk run's back-to-back resolutions without
// slowing normal single-download traffic, then relaxes back toward the
// light base window once requests are succeeding again.
var pikPakRequestThrottle = newRequestThrottle(400*time.Millisecond, 1200*time.Millisecond, 30*time.Second)

func newRequestThrottle(min, max, backoffCeiling time.Duration) *requestThrottle {
	return &requestThrottle{min: min, max: max, backoffCeiling: backoffCeiling}
}

// requestThrottle enforces a randomized cooldown between consecutive calls
// to a rate-sensitive upstream, and adapts that cooldown upward when recent
// calls are timing out or being throttled - the clearest available signal
// that the current pace is too aggressive - then relaxes it back down once
// calls succeed again. All fields are guarded by mu; safe for concurrent use.
type requestThrottle struct {
	mu             sync.Mutex
	next           time.Time
	min, max       time.Duration
	backoffCeiling time.Duration
	backoff        time.Duration
}

// wait blocks until this call is allowed to proceed, honoring an existing
// cooldown left by the previous caller, then reserves a fresh randomized
// window (min-max, plus any active backoff) for the caller after it.
func (t *requestThrottle) wait(ctx context.Context) error {
	// The unit/integration test suite exercises these same code paths with
	// mocked HTTP round trippers, often hundreds of calls in one test run;
	// actually sleeping out the cooldown there would turn a fast test suite
	// into one that takes minutes without testing anything the throttle's
	// own tests (which construct a *requestThrottle directly) don't already
	// cover. testing.Testing() is only true inside a test binary, never in
	// the shipped program, so production behavior is unaffected.
	if testing.Testing() {
		return nil
	}
	t.mu.Lock()
	now := time.Now()
	var delay time.Duration
	if t.next.After(now) {
		delay = t.next.Sub(now)
	}
	spanMillis := (t.max - t.min).Milliseconds()
	if spanMillis < 0 {
		spanMillis = 0
	}
	cooldown := t.min + time.Duration(rand.Int63n(spanMillis+1))*time.Millisecond + t.backoff
	t.next = now.Add(delay + cooldown)
	t.mu.Unlock()
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// reportResult grows the backoff after a request fails in a way that looks
// like throttling (a timeout or an HTTP 403/429), and decays it back toward
// zero after a clean request, so sustained trouble slows this throttle down
// while a healthy run gradually returns to the plain min-max cooldown.
func (t *requestThrottle) reportResult(err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if err != nil && looksThrottled(err) {
		switch {
		case t.backoff <= 0:
			t.backoff = t.min
		default:
			t.backoff *= 2
		}
		if t.backoff > t.backoffCeiling {
			t.backoff = t.backoffCeiling
		}
		return
	}
	if err == nil && t.backoff > 0 {
		t.backoff -= t.backoff / 2
		if t.backoff < 200*time.Millisecond {
			t.backoff = 0
		}
	}
}

// looksThrottled reports whether err has the shape of an upstream telling us
// to slow down: a context deadline/timeout (the dominant symptom observed
// against both JavDB and PikPak under load), or an explicit 403/429 status.
func looksThrottled(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "deadline exceeded") ||
		strings.Contains(msg, "timeout") ||
		strings.Contains(msg, "429") ||
		strings.Contains(msg, "too many requests") ||
		strings.Contains(msg, "http 403")
}

func (p *javDBProvider) getHTML(ctx context.Context, raw string) (*html.Node, int, error) {
	if err := javDBRequestThrottle.wait(ctx); err != nil {
		return nil, 0, err
	}
	doc, status, err := p.getHTMLDirectWithRetry(ctx, raw)
	javDBRequestThrottle.reportResult(err)
	if status == http.StatusForbidden && p.gluetun != nil {
		oldIP, newIP, attempts, rotateErr := p.gluetun.rotateUntilIPChanges(ctx)
		if p.log != nil {
			if rotateErr != nil {
				p.log.Warn("JavDB HTTP 403 VPN rotation did not produce a verified new IP", "url", raw, "attempts", attempts, "old_ip", oldIP, "current_ip", newIP, "error", rotateErr)
			} else {
				p.log.Info("JavDB HTTP 403 rotated Gluetun VPN IP", "url", raw, "attempts", attempts, "old_ip", oldIP, "new_ip", newIP)
			}
		}
		// Retry direct exactly once after the rotation sequence. Even when all
		// configured rotations returned the same observable IP, the renewed
		// tunnel may have cleared a transient block; only another 403 proceeds
		// to the existing priority-aware solver pool.
		doc, status, err = p.getHTMLDirect(ctx, raw)
		if status != http.StatusForbidden {
			if err == nil && p.log != nil {
				p.log.Info("JavDB HTTP 403 recovered after Gluetun rotation", "url", raw, "rotation_attempts", attempts, "public_ip", newIP)
			}
			return doc, status, err
		}
	}
	if status == http.StatusForbidden && p.solverPool != nil && p.solverPool.EnabledCount() > 0 {
		doc, solverErr := p.getHTMLThroughSolver(ctx, raw)
		if solverErr == nil {
			if p.log != nil {
				p.log.Info("JavDB HTTP 403 recovered through anti-bot solver", "url", raw, "direct_status", status)
			}
			return doc, http.StatusOK, nil
		}
		if p.log != nil {
			p.log.Warn("JavDB HTTP 403 solver fallback failed", "url", raw, "direct_status", status, "error", solverErr)
		}
		return nil, status, fmt.Errorf("HTTP 403; Byparr/FlareSolverr fallback failed: %w", solverErr)
	}
	return doc, status, err
}

// javDBTransientNetworkAttempts is how many times getHTMLDirectWithRetry
// tries a request that never got a response at all before giving up.
const javDBTransientNetworkAttempts = 3

// javDBTransientRetryDelay is the base backoff between those attempts
// (attempt*javDBTransientRetryDelay); a test seam like
// pikPakRestoreRetryDelay so retry tests don't have to wait out real time.
var javDBTransientRetryDelay = 500 * time.Millisecond

// getHTMLDirectWithRetry retries getHTMLDirect when the request never
// reached JavDB at all - status stays 0, meaning p.client.Do itself failed:
// a dial timeout, a connection reset or refused, a DNS hiccup, or any other
// transport-level error before an HTTP response existed to read a status
// from. A one-off network blip like that usually clears a moment later, so
// it is worth a couple of quick retries rather than failing the whole
// search/detail-page fetch (and, on a search request, the whole scrape of
// that release) immediately. It never retries once JavDB has actually
// responded with something, even an error status - status 403 already has
// its own recovery path (Gluetun rotation, then the solver pool) in
// getHTML above, and any other non-zero status is returned as-is.
func (p *javDBProvider) getHTMLDirectWithRetry(ctx context.Context, raw string) (*html.Node, int, error) {
	var doc *html.Node
	var status int
	var err error
	for attempt := 0; attempt < javDBTransientNetworkAttempts; attempt++ {
		if attempt > 0 {
			timer := time.NewTimer(time.Duration(attempt) * javDBTransientRetryDelay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return doc, status, err
			case <-timer.C:
			}
		}
		doc, status, err = p.getHTMLDirect(ctx, raw)
		if status != 0 || err == nil {
			return doc, status, err
		}
	}
	return doc, status, err
}

func (p *javDBProvider) getHTMLDirect(ctx context.Context, raw string) (*html.Node, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("User-Agent", publicShareUserAgent)
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, resp.StatusCode, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	if looksLikeAccessChallenge(body) {
		return nil, resp.StatusCode, errors.New("JavDB request was blocked or returned a challenge page")
	}
	doc, err := html.Parse(strings.NewReader(string(body)))
	return doc, resp.StatusCode, err
}

func (p *javDBProvider) getHTMLThroughSolver(ctx context.Context, raw string) (*html.Node, error) {
	attempts := p.solverPool.EnabledCount()
	var failures []string
	for attempt := 0; attempt < attempts; attempt++ {
		lease, err := p.solverPool.Acquire(ctx, 100)
		if err != nil {
			return nil, err
		}
		solverURL := lease.URL()
		body, err := p.solveHTML(ctx, solverURL, raw)
		lease.Release()
		if err != nil {
			failures = append(failures, solverURL+": "+err.Error())
			continue
		}
		if looksLikeAccessChallenge(body) {
			failures = append(failures, solverURL+": returned a challenge page")
			continue
		}
		doc, err := html.Parse(strings.NewReader(string(body)))
		if err == nil {
			return doc, nil
		}
		failures = append(failures, solverURL+": parse response: "+err.Error())
	}
	if len(failures) == 0 {
		return nil, errors.New("no enabled Byparr/FlareSolverr instance is available")
	}
	return nil, errors.New(strings.Join(failures, "; "))
}

func (p *javDBProvider) solveHTML(ctx context.Context, solverURL, raw string) ([]byte, error) {
	payload, _ := json.Marshal(map[string]any{"cmd": "request.get", "url": raw, "maxTimeout": 75000, "max_timeout": 75})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, solverURL, strings.NewReader(string(payload)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		message, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(message)))
	}
	var result struct {
		Status   string `json:"status"`
		Message  string `json:"message"`
		Solution struct {
			Response string `json:"response"`
			Status   int    `json:"status"`
		} `json:"solution"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 10<<20)).Decode(&result); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}
	if result.Status != "ok" || result.Solution.Response == "" {
		return nil, fmt.Errorf("solver response: %s", firstNonEmpty(result.Message, result.Status))
	}
	if result.Solution.Status != 0 && (result.Solution.Status < 200 || result.Solution.Status >= 300) {
		return nil, fmt.Errorf("target returned HTTP %d through solver", result.Solution.Status)
	}
	return []byte(result.Solution.Response), nil
}

func parseJavDBDownloadCandidates(doc *html.Node, sourceURL, releaseID string) []domain.SearchResult {
	return discoverJavDBDownloads(doc, sourceURL, releaseID).rows
}

func looksLikeAccessChallenge(body []byte) bool {
	text := strings.ToLower(string(body))
	normalPage := strings.Contains(text, `class="movie-list`) || strings.Contains(text, `class="video-detail`) || strings.Contains(text, `id="video-search"`)
	if normalPage {
		return false
	}
	for _, signal := range []string{"cf-chl-", "cloudflare ray id", "checking your browser", "just a moment...", "attention required!", "captcha", "challenge-platform"} {
		if strings.Contains(text, signal) {
			return true
		}
	}
	return false
}

func parseJavDBSearchHits(doc *html.Node, base string) []javDBSearchHit {
	seen := map[string]bool{}
	var hits []javDBSearchHit
	for _, anchor := range descendants(doc, "a") {
		href := resolveURL(base, html.UnescapeString(strings.TrimSpace(attrValue(anchor, "href"))))
		u, err := url.Parse(href)
		if err != nil || !strings.Contains(u.Path, "/v/") || seen[href] {
			continue
		}
		container := javDBResultContainer(anchor)
		idNode := firstDescendant(anchor, "strong", "")
		if idNode == nil {
			idNode = firstDescendant(container, "strong", "")
		}
		id := extractJavDBReleaseID(nodeText(idNode))
		if normalizeReleaseID(id) == "" {
			continue
		}
		meta := firstDescendant(container, "div", "meta")
		if meta == nil {
			meta = firstDescendant(container, "span", "meta")
		}
		seen[href] = true
		hits = append(hits, javDBSearchHit{href: href, id: id, date: strings.TrimSpace(nodeText(meta))})
	}
	return hits
}

func javDBResultContainer(node *html.Node) *html.Node {
	var fallback *html.Node
	for current, depth := node, 0; current != nil && depth < 8; current, depth = current.Parent, depth+1 {
		if hasClass(current, "item") || hasClass(current, "movie-list") {
			return current
		}
		if fallback == nil && hasClass(current, "box") {
			fallback = current
		}
	}
	if fallback != nil {
		return fallback
	}
	return node.Parent
}

func parseJavDBDetailID(doc *html.Node) string {
	if match := javDBLabeledIDPattern.FindStringSubmatch(nodeText(doc)); len(match) > 1 {
		return strings.TrimSpace(match[1])
	}
	for _, node := range descendants(doc, "a") {
		if value := strings.TrimSpace(attrValue(node, "data-clipboard-text")); releaseLikeTextPattern.MatchString(value) {
			return value
		}
	}
	for _, heading := range descendants(doc, "h2") {
		if value := strings.TrimSpace(nodeText(firstDescendant(heading, "strong", ""))); releaseLikeTextPattern.MatchString(value) {
			return value
		}
	}
	return ""
}

func discoverJavDBDownloads(doc *html.Node, sourceURL, releaseID string) javDBDownloadDiscovery {
	discovery := javDBDownloadDiscovery{}
	seenShares, seenActions := map[string]bool{}, map[string]bool{}
	for _, anchor := range descendants(doc, "a") {
		raw := html.UnescapeString(strings.TrimSpace(attrValue(anchor, "href")))
		if raw == "" {
			continue
		}
		href := resolveURL(sourceURL, raw)
		u, err := url.Parse(href)
		if err != nil {
			continue
		}
		if isJavDBShareURL(u) {
			discovery.downloadSectionFound = true
			if seenShares[href] {
				continue
			}
			seenShares[href] = true
			discovery.shareLinkCount++
			if strings.EqualFold(u.Hostname(), "mypikpak.com") || strings.HasSuffix(strings.ToLower(u.Hostname()), ".mypikpak.com") {
				discovery.pikPakLinkCount++
			}
			container := javDBDownloadContainer(anchor)
			name, matchesRelease := javDBCandidateName(container, anchor, releaseID)
			if !matchesRelease {
				continue
			}
			size := parseHumanBytes(nodeText(container))
			if rawSize := attrValue(container, "data-size"); size == 0 && rawSize != "" {
				mb, _ := strconv.ParseInt(rawSize, 10, 64)
				size = mb << 20
			}
			published := strings.TrimSpace(nodeText(firstDescendant(container, "span", "time")))
			discovery.rows = append(discovery.rows, domain.SearchResult{Provider: "JavDB / Keepshare", Title: name, Link: href, SourceURL: sourceURL, Transport: "http", SizeBytes: size, PublishedAt: published, Accepted: true, Reason: "exact release ID match available as HTTP download"})
			continue
		}
		if javDBDownloadAction(anchor, sourceURL, href) {
			discovery.downloadSectionFound = true
			if !seenActions[href] {
				seenActions[href] = true
				discovery.actionURLs = append(discovery.actionURLs, href)
			}
		}
	}
	if !discovery.downloadSectionFound {
		for _, tag := range []string{"button", "section", "div"} {
			for _, node := range descendants(doc, tag) {
				marker := strings.ToLower(strings.Join([]string{attrValue(node, "id"), attrValue(node, "class"), attrValue(node, "title"), nodeText(node)}, " "))
				if strings.Contains(marker, "download") || strings.Contains(marker, "magnet") {
					discovery.downloadSectionFound = true
					break
				}
			}
			if discovery.downloadSectionFound {
				break
			}
		}
	}
	return discovery
}

func isJavDBShareHost(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	return host == "keepshare.org" || strings.HasSuffix(host, ".keepshare.org") || host == "keepshare.cc" || strings.HasSuffix(host, ".keepshare.cc") || host == "mypikpak.com" || strings.HasSuffix(host, ".mypikpak.com")
}

func isJavDBShareURL(value *url.URL) bool {
	if value == nil || !isJavDBShareHost(value.Hostname()) {
		return false
	}
	host := strings.ToLower(value.Hostname())
	path := strings.Trim(value.EscapedPath(), "/")
	if host == "mypikpak.com" || strings.HasSuffix(host, ".mypikpak.com") {
		return strings.HasPrefix(path, "s/") && len(strings.Split(path, "/")) >= 2
	}
	return path != ""
}

func javDBDownloadAction(anchor *html.Node, sourceURL, href string) bool {
	source, sourceErr := url.Parse(sourceURL)
	target, targetErr := url.Parse(href)
	if sourceErr != nil || targetErr != nil || !strings.EqualFold(source.Hostname(), target.Hostname()) || source.String() == target.String() {
		return false
	}
	marker := strings.ToLower(strings.Join([]string{nodeText(anchor), attrValue(anchor, "class"), attrValue(anchor, "id"), attrValue(anchor, "title"), target.Path}, " "))
	return strings.Contains(marker, "download")
}

func javDBDownloadContainer(anchor *html.Node) *html.Node {
	for current, depth := anchor, 0; current != nil && depth < 6; current, depth = current.Parent, depth+1 {
		if hasClass(current, "item") || hasClass(current, "download") || current.Data == "li" || current.Data == "tr" {
			return current
		}
	}
	return anchor.Parent
}

var releaseLikeTextPattern = regexp.MustCompile(`(?i)(?:^|[^A-Z0-9])[A-Z]{2,}[-_ .]*[0-9]{2,}(?:[-_ .]*U)?(?:[^A-Z0-9]|$)`)

func javDBCandidateName(container, anchor *html.Node, releaseID string) (string, bool) {
	for _, candidate := range []string{nodeText(firstDescendant(container, "span", "name")), attrValue(anchor, "download"), attrValue(anchor, "title"), nodeText(anchor), nodeText(container)} {
		candidate = strings.TrimSpace(candidate)
		if candidate != "" && releaseIDMatchesText(candidate, releaseID) {
			return candidate, true
		}
		if candidate != "" && releaseLikeTextPattern.MatchString(candidate) {
			return candidate, false
		}
	}
	return releaseID, true
}

func mergeJavDBDiscovery(dst *javDBDownloadDiscovery, src javDBDownloadDiscovery) {
	dst.downloadSectionFound = dst.downloadSectionFound || src.downloadSectionFound
	dst.shareLinkCount += src.shareLinkCount
	dst.pikPakLinkCount += src.pikPakLinkCount
	dst.rows = appendUniqueJavDBRows(dst.rows, src.rows...)
}

func appendUniqueJavDBRows(rows []domain.SearchResult, candidates ...domain.SearchResult) []domain.SearchResult {
	seen := make(map[string]bool, len(rows)+len(candidates))
	for _, row := range rows {
		seen[row.Link] = true
	}
	for _, candidate := range candidates {
		if candidate.Link != "" && !seen[candidate.Link] {
			seen[candidate.Link] = true
			rows = append(rows, candidate)
		}
	}
	return rows
}

var humanSizePattern = regexp.MustCompile(`(?i)([0-9]+(?:\.[0-9]+)?)\s*(TIB|TB|GIB|GB|MIB|MB|KIB|KB)`)

func parseHumanBytes(s string) int64 {
	m := humanSizePattern.FindStringSubmatch(s)
	if m == nil {
		return 0
	}
	v, _ := strconv.ParseFloat(m[1], 64)
	unit := strings.ReplaceAll(strings.ToUpper(m[2]), "I", "")
	power := map[string]int{"KB": 10, "MB": 20, "GB": 30, "TB": 40}[unit]
	return int64(v * float64(int64(1)<<power))
}

func parseJavDBDate(s string) time.Time {
	match := regexp.MustCompile(`\b(?:\d{4}[-/]\d{1,2}[-/]\d{1,2}|\d{1,2}/\d{1,2}/\d{4})\b`).FindString(strings.TrimSpace(s))
	if match != "" {
		s = match
	}
	for _, layout := range []string{"01/02/2006", "2006-01-02", "2006/01/02"} {
		if t, err := time.Parse(layout, strings.TrimSpace(s)); err == nil {
			return t
		}
	}
	return time.Time{}
}

func formatOptionalDate(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.Format("2006-01-02")
}

func hasUVariant(name, releaseID string) bool {
	n, id := normalizeReleaseID(name), normalizeReleaseID(releaseID)
	return strings.Contains(n, id+"U")
}

func resolveURL(base, ref string) string {
	b, e1 := url.Parse(base)
	r, e2 := url.Parse(ref)
	if e1 != nil || e2 != nil {
		return ref
	}
	return b.ResolveReference(r).String()
}

func descendants(root *html.Node, tag string) []*html.Node {
	var out []*html.Node
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == tag {
			out = append(out, n)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	if root != nil {
		walk(root)
	}
	return out
}
func descendantsWithClass(root *html.Node, class string) []*html.Node {
	var out []*html.Node
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && hasClass(n, class) {
			out = append(out, n)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	if root != nil {
		walk(root)
	}
	return out
}
func firstDescendant(root *html.Node, tag, class string) *html.Node {
	if root == nil {
		return nil
	}
	if root.Type == html.ElementNode && root.Data == tag && (class == "" || hasClass(root, class)) {
		return root
	}
	for c := root.FirstChild; c != nil; c = c.NextSibling {
		if n := firstDescendant(c, tag, class); n != nil {
			return n
		}
	}
	return nil
}
func hasClass(n *html.Node, want string) bool {
	for _, c := range strings.Fields(attrValue(n, "class")) {
		if c == want {
			return true
		}
	}
	return false
}
func attrValue(n *html.Node, key string) string {
	if n == nil {
		return ""
	}
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}
func nodeText(n *html.Node) string {
	if n == nil {
		return ""
	}
	if n.Type == html.TextNode {
		return n.Data
	}
	var b strings.Builder
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		b.WriteString(nodeText(c))
		b.WriteByte(' ')
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

type pikPakClient struct {
	http                                *http.Client
	deviceID, captchaToken, accessToken string
	refreshToken, userID, username      string
	tokenIssuedAt, tokenExpiresAt       time.Time
	verificationURL                     string
}
type pikPakFile struct {
	ID       string `json:"id"`
	ParentID string `json:"parent_id"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	Size     string `json:"size"`
	// Hash is PikPak's resource/torrent identity. Despite being 40 hexadecimal
	// characters, it is not the SHA-1 digest of the bytes returned by the
	// download URL and must not be used for downloaded-file verification.
	Hash               string `json:"hash"`
	MD5Checksum        string `json:"md5_checksum"`
	MimeType           string `json:"mime_type"`
	WebContentLink     string `json:"web_content_link"`
	FolderReleaseMatch bool   `json:"-"`
	Links              struct {
		ApplicationOctetStream struct {
			URL string `json:"url"`
		} `json:"application/octet-stream"`
	} `json:"links"`
	Medias []struct {
		Link struct {
			URL string `json:"url"`
		} `json:"link"`
		IsOrigin bool `json:"is_origin"`
	} `json:"medias"`
}
type pikPakResponse struct {
	ErrorCode        int          `json:"error_code"`
	Error            string       `json:"error"`
	ErrorDescription string       `json:"error_description"`
	ShareStatus      string       `json:"share_status"`
	ShareStatusText  string       `json:"share_status_text"`
	NextPageToken    string       `json:"next_page_token"`
	Files            []pikPakFile `json:"files"`
	FileInfo         pikPakFile   `json:"file_info"`
}

func newPikPakClient(client *http.Client) *pikPakClient {
	now := strconv.FormatInt(time.Now().UnixNano(), 16)
	sum := md5.Sum([]byte(now))
	return &pikPakClient{http: client, deviceID: hex.EncodeToString(sum[:])}
}
func (p *pikPakClient) captchaSign(ts string) string {
	s := pikPakClientID + pikPakClientVersion + pikPakPackageName + p.deviceID + ts
	for _, salt := range pikPakAlgorithms {
		x := md5.Sum([]byte(s + salt))
		s = hex.EncodeToString(x[:])
	}
	return "1." + s
}
func (p *pikPakClient) refreshCaptcha(ctx context.Context, action string) error {
	ts := strconv.FormatInt(time.Now().UnixMilli(), 10)
	redirectURI := ""
	if p.accessToken != "" || action == "POST:/v1/auth/signin" {
		redirectURI = "https://api.mypikpak.com/v1/auth/callback"
	}
	meta := map[string]string{"captcha_sign": p.captchaSign(ts), "client_version": pikPakClientVersion, "package_name": pikPakPackageName, "timestamp": ts, "user_id": p.userID}
	if action == "POST:/v1/auth/signin" {
		meta["username"] = p.username
	}
	body := map[string]any{"action": action, "captcha_token": p.captchaToken, "client_id": pikPakClientID, "device_id": p.deviceID, "meta": meta, "redirect_uri": redirectURI}
	b, _ := json.Marshal(body)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, pikPakUserHost+"/v1/shield/captcha/init", strings.NewReader(string(b)))
	p.setHeaders(req)
	var out struct {
		CaptchaToken     string `json:"captcha_token"`
		ExpiresIn        int64  `json:"expires_in"`
		URL              string `json:"url"`
		ErrorDescription string `json:"error_description"`
	}
	if err := p.doJSON(req, &out); err != nil {
		p.verificationURL = safePikPakVerificationURL(out.URL)
		if p.verificationURL != "" {
			return fmt.Errorf("PikPak requires human verification: %s", p.verificationURL)
		}
		return err
	}
	if out.CaptchaToken == "" {
		p.verificationURL = safePikPakVerificationURL(out.URL)
		if p.verificationURL != "" {
			return fmt.Errorf("PikPak requires human verification: %s", p.verificationURL)
		}
		return errors.New("PikPak did not issue an anonymous CAPTCHA token: " + out.ErrorDescription)
	}
	p.captchaToken = out.CaptchaToken
	return nil
}
func (p *pikPakClient) setHeaders(req *http.Request) {
	req.Header.Set("User-Agent", publicShareUserAgent)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Client-ID", pikPakClientID)
	req.Header.Set("X-Device-ID", p.deviceID)
	req.Header.Set("X-Captcha-Token", p.captchaToken)
	req.Header.Set("Referer", "https://mypikpak.com/")
	if p.accessToken != "" {
		req.Header.Set("Authorization", "Bearer "+p.accessToken)
	}
}
func (p *pikPakClient) doJSON(req *http.Request, out any) error {
	if err := pikPakRequestThrottle.wait(req.Context()); err != nil {
		return err
	}
	resp, err := p.http.Do(req)
	pikPakRequestThrottle.reportResult(err)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusForbidden {
			pikPakRequestThrottle.reportResult(fmt.Errorf("HTTP %d", resp.StatusCode))
		}
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		if out != nil && len(data) > 0 {
			_ = json.Unmarshal(data, out)
		}
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	err = json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(out)
	if errors.Is(err, io.EOF) {
		return nil
	}
	return err
}
func (p *pikPakClient) request(ctx context.Context, path string, q url.Values) (pikPakResponse, error) {
	action := "GET:" + path
	if p.captchaToken == "" {
		if err := p.refreshCaptcha(ctx, action); err != nil {
			return pikPakResponse{}, err
		}
	}
	do := func() (pikPakResponse, error) {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, pikPakAPIHost+path+"?"+q.Encode(), nil)
		p.setHeaders(req)
		var out pikPakResponse
		err := p.doJSON(req, &out)
		return out, err
	}
	out, err := do()
	if err == nil && out.ErrorCode == 9 {
		if err = p.refreshCaptcha(ctx, action); err == nil {
			out, err = do()
		}
	}
	if err != nil {
		return out, err
	}
	if out.ErrorCode != 0 {
		return out, fmt.Errorf("PikPak error %d: %s", out.ErrorCode, firstNonEmpty(out.ErrorDescription, out.Error))
	}
	return out, nil
}

func (p *pikPakClient) login(ctx context.Context, username, password string) error {
	p.username = username
	if err := p.refreshCaptcha(ctx, "POST:/v1/auth/signin"); err != nil {
		return fmt.Errorf("PikPak sign-in CAPTCHA token: %w", redactPikPakAuthError(err, username, password))
	}
	body, _ := json.Marshal(map[string]string{
		"client_id":     pikPakClientID,
		"client_secret": pikPakClientSecret,
		"captcha_token": p.captchaToken,
		"username":      username,
		"password":      password,
	})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, pikPakUserHost+"/v1/auth/signin?client_id="+url.QueryEscape(pikPakClientID), strings.NewReader(string(body)))
	p.setHeaders(req)
	var out struct {
		AccessToken      string `json:"access_token"`
		RefreshToken     string `json:"refresh_token"`
		ExpiresIn        int64  `json:"expires_in"`
		Sub              string `json:"sub"`
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
	}
	if err := p.doJSON(req, &out); err != nil {
		return fmt.Errorf("PikPak sign-in failed: %w", redactPikPakAuthError(err, username, password))
	}
	if out.AccessToken == "" {
		detail := errors.New(firstNonEmpty(out.ErrorDescription, out.Error, "PikPak returned no access token"))
		return fmt.Errorf("PikPak sign-in failed: %w", redactPikPakAuthError(detail, username, password))
	}
	p.accessToken = out.AccessToken
	p.refreshToken = out.RefreshToken
	p.userID = out.Sub
	p.tokenIssuedAt = time.Now().UTC()
	if out.ExpiresIn > 0 {
		p.tokenExpiresAt = p.tokenIssuedAt.Add(time.Duration(out.ExpiresIn) * time.Second)
	}
	p.captchaToken = ""
	return nil
}

func (p *pikPakClient) refreshLogin(ctx context.Context) error {
	if strings.TrimSpace(p.refreshToken) == "" {
		return errors.New("PikPak refresh token is unavailable")
	}
	body, _ := json.Marshal(map[string]string{
		// PikPak's browser/web client is public. Its token endpoint now rejects
		// refresh requests that include the embedded client secret as unsafe.
		// The refresh token, client ID, and stable device headers are sufficient.
		"client_id":  pikPakClientID,
		"grant_type": "refresh_token", "refresh_token": p.refreshToken,
	})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, pikPakUserHost+"/v1/auth/token?client_id="+url.QueryEscape(pikPakClientID), strings.NewReader(string(body)))
	p.setHeaders(req)
	var out struct {
		AccessToken      string `json:"access_token"`
		RefreshToken     string `json:"refresh_token"`
		Sub              string `json:"sub"`
		ExpiresIn        int64  `json:"expires_in"`
		Error            string `json:"error"`
		ErrorCode        int    `json:"error_code"`
		ErrorDescription string `json:"error_description"`
	}
	if err := p.doJSON(req, &out); err != nil {
		return fmt.Errorf("PikPak session refresh failed: %w", err)
	}
	if out.ErrorCode != 0 || out.AccessToken == "" {
		return fmt.Errorf("PikPak session refresh failed (%d): %s", out.ErrorCode, firstNonEmpty(out.ErrorDescription, out.Error, "no access token returned"))
	}
	p.accessToken = out.AccessToken
	if out.RefreshToken != "" {
		p.refreshToken = out.RefreshToken
	}
	if out.Sub != "" {
		p.userID = out.Sub
	}
	p.tokenIssuedAt = time.Now().UTC()
	p.tokenExpiresAt = time.Time{}
	if out.ExpiresIn > 0 {
		p.tokenExpiresAt = p.tokenIssuedAt.Add(time.Duration(out.ExpiresIn) * time.Second)
	}
	p.captchaToken = ""
	return nil
}

func safePikPakVerificationURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "https" {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	if host != "mypikpak.com" && !strings.HasSuffix(host, ".mypikpak.com") && host != "mypikpak.net" && !strings.HasSuffix(host, ".mypikpak.net") {
		return ""
	}
	return u.String()
}

func redactPikPakAuthError(err error, credentials ...string) error {
	if err == nil {
		return nil
	}
	message := err.Error()
	for _, credential := range credentials {
		if credential = strings.TrimSpace(credential); credential != "" {
			message = regexp.MustCompile(`(?i)`+regexp.QuoteMeta(credential)).ReplaceAllString(message, "[redacted]")
		}
	}
	return errors.New(message)
}

func (p *pikPakClient) authenticatedJSON(ctx context.Context, method, path string, q url.Values, body any, out any) error {
	if p.accessToken == "" {
		return errors.New("PikPak account is not authenticated")
	}
	if err := p.refreshCaptcha(ctx, method+":"+path); err != nil {
		return fmt.Errorf("PikPak authenticated CAPTCHA token: %w", err)
	}
	var encoded []byte
	if body != nil {
		encoded, _ = json.Marshal(body)
	}
	do := func() error {
		endpoint := pikPakAPIHost + path
		if len(q) > 0 {
			endpoint += "?" + q.Encode()
		}
		req, _ := http.NewRequestWithContext(ctx, method, endpoint, strings.NewReader(string(encoded)))
		p.setHeaders(req)
		return p.doJSON(req, out)
	}
	if err := do(); err != nil {
		return err
	}
	return nil
}

type pikPakRestoreResponse struct {
	RestoreStatus string `json:"restore_status"`
	RestoreTaskID string `json:"restore_task_id"`
	Params        struct {
		TraceFileIDs json.RawMessage `json:"trace_file_ids"`
		ErrorDetail  string          `json:"error_detail"`
	} `json:"params"`
	Phase   string `json:"phase"`
	Message string `json:"message"`
}

func pikPakRestoreCandidateIDs(raw json.RawMessage) []string {
	seen := map[string]bool{}
	var ids []string
	add := func(value string) {
		for _, id := range strings.FieldsFunc(value, func(r rune) bool { return r == ',' || unicode.IsSpace(r) }) {
			id = strings.Trim(strings.TrimSpace(id), `"`)
			if id != "" && !seen[id] {
				seen[id] = true
				ids = append(ids, id)
			}
		}
	}
	var collect func(any)
	collect = func(value any) {
		switch typed := value.(type) {
		case string:
			add(typed)
		case []any:
			for _, item := range typed {
				collect(item)
			}
		case map[string]any:
			for _, item := range typed {
				collect(item)
			}
		}
	}
	var value any
	if len(raw) > 0 && json.Unmarshal(raw, &value) == nil {
		collect(value)
	}
	return ids
}

func (p *pikPakClient) restoredFileFromTaskIDs(ctx context.Context, raw json.RawMessage, expectedName string, expectedSize int64) (pikPakFile, bool) {
	for _, id := range pikPakRestoreCandidateIDs(raw) {
		file, err := p.authenticatedFile(ctx, id)
		if err != nil {
			continue
		}
		if match, found := exactPikPakAccountFile([]pikPakFile{file}, expectedName, expectedSize, nil); found {
			return match, true
		}
	}
	return pikPakFile{}, false
}

func (p *pikPakClient) listPikPakRestoreAreaFiles(ctx context.Context) ([]pikPakFile, error) {
	// Restores are placed at the drive root or below PikPak's "Pack From
	// Shared" folder. Do not recursively inventory unrelated account folders:
	// large drives can otherwise occupy every HTTP worker for hours before the
	// restore request is even submitted.
	queue := []string{""}
	seen := map[string]bool{"": true}
	var all []pikPakFile
	for len(queue) > 0 {
		parentID := queue[0]
		queue = queue[1:]
		page := ""
		for {
			var response pikPakResponse
			q := url.Values{"parent_id": {parentID}, "thumbnail_size": {"SIZE_LARGE"}, "with_audit": {"true"}, "limit": {"100"}, "page_token": {page}, "filters": {`{"phase":{"eq":"PHASE_TYPE_COMPLETE"},"trashed":{"eq":false}}`}}
			if err := p.authenticatedJSON(ctx, http.MethodGet, "/drive/v1/files", q, nil, &response); err != nil {
				return nil, err
			}
			for _, file := range response.Files {
				if file.Kind == "drive#folder" {
					inRestoreArea := parentID != "" || strings.Contains(strings.ToLower(file.Name), "pack from shared")
					if inRestoreArea && file.ID != "" && !seen[file.ID] && len(seen) < 250 {
						seen[file.ID] = true
						queue = append(queue, file.ID)
					}
					continue
				}
				all = append(all, file)
			}
			page = response.NextPageToken
			if page == "" {
				break
			}
		}
	}
	return all, nil
}

func exactPikPakAccountFile(files []pikPakFile, expectedName string, expectedSize int64, exclude map[string]bool) (pikPakFile, bool) {
	for _, file := range files {
		size, _ := strconv.ParseInt(file.Size, 10, 64)
		if exclude[file.ID] || !strings.EqualFold(strings.TrimSpace(file.Name), strings.TrimSpace(expectedName)) || (expectedSize > 0 && size != expectedSize) {
			continue
		}
		return file, true
	}
	return pikPakFile{}, false
}

func retryablePikPakRestoreError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	for _, marker := range []string{
		"http 429", "http 500", "http 502", "http 503", "http 504",
		"timeout", "deadline exceeded", "connection reset", "connection refused",
		"unexpected eof", "temporary", "server closed idle connection",
	} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

func (p *pikPakClient) findRestoredFile(ctx context.Context, expectedName string, expectedSize int64, beforeIDs map[string]bool, allowExisting bool) (pikPakFile, bool, bool, error) {
	// Do not build a complete inventory for every restore poll. Large accounts
	// can contain hundreds of folders, and authenticated listing requires an API
	// round trip per folder. Search breadth-first, prioritize PikPak's restore
	// folder, and stop as soon as the exact selected file is found.
	queue := []string{""}
	seen := map[string]bool{"": true}
	var existing pikPakFile
	for len(queue) > 0 {
		parentID := queue[0]
		queue = queue[1:]
		page := ""
		for {
			var response pikPakResponse
			q := url.Values{"parent_id": {parentID}, "thumbnail_size": {"SIZE_LARGE"}, "with_audit": {"true"}, "limit": {"100"}, "page_token": {page}, "filters": {`{"phase":{"eq":"PHASE_TYPE_COMPLETE"},"trashed":{"eq":false}}`}}
			if err := p.authenticatedJSON(ctx, http.MethodGet, "/drive/v1/files", q, nil, &response); err != nil {
				return pikPakFile{}, false, false, err
			}
			folders := make([]string, 0)
			for _, file := range response.Files {
				if file.Kind == "drive#folder" {
					inRestoreArea := parentID != "" || strings.Contains(strings.ToLower(file.Name), "pack from shared")
					if !inRestoreArea || file.ID == "" || seen[file.ID] || len(seen) >= 250 {
						continue
					}
					seen[file.ID] = true
					folders = append(folders, file.ID)
					continue
				}
				if match, found := exactPikPakAccountFile([]pikPakFile{file}, expectedName, expectedSize, nil); found {
					if !beforeIDs[match.ID] {
						return match, true, true, nil
					}
					if allowExisting && existing.ID == "" {
						existing = match
					}
				}
			}
			queue = append(folders, queue...)
			page = response.NextPageToken
			if page == "" {
				break
			}
		}
		if existing.ID != "" {
			return existing, false, true, nil
		}
	}
	return pikPakFile{}, false, false, nil
}

func (p *pikPakClient) restoreSharedFile(ctx context.Context, shareID, fileID, expectedName string, expectedSize int64) (pikPakFile, bool, error) {
	before, err := p.listPikPakRestoreAreaFiles(ctx)
	if err != nil {
		return pikPakFile{}, false, fmt.Errorf("inventory PikPak restore area before restore: %w", err)
	}
	// A file already sitting in the restore area under this exact name and size is
	// almost certainly a restore from an earlier attempt at this same release
	// (an earlier download, a retry, a resume after restart, ...). Restoring
	// the share again would place a second copy alongside it - PikPak's
	// restore endpoint does not itself deduplicate - so reuse it instead of
	// submitting another restore. newlyRestored is false here on purpose: the
	// caller uses that flag to decide whether it's safe to delete the file
	// again once the transfer finishes, and this file predates this attempt.
	if existing, found := exactPikPakAccountFile(before, expectedName, expectedSize, nil); found {
		return existing, false, nil
	}
	beforeIDs := make(map[string]bool, len(before))
	for _, file := range before {
		beforeIDs[file.ID] = true
	}
	body := map[string]any{
		"kind":            "drive#file",
		"share_id":        shareID,
		"pass_code_token": "",
		"file_ids":        []string{fileID},
	}
	var restoreErr error
	var restoreResponse pikPakRestoreResponse
	for attempt := 1; attempt <= 3; attempt++ {
		restoreResponse = pikPakRestoreResponse{}
		restoreErr = p.authenticatedJSON(ctx, http.MethodPost, "/drive/v1/share/restore", nil, body, &restoreResponse)
		if restoreErr == nil {
			status := strings.ToUpper(restoreResponse.RestoreStatus)
			if status != "" && status != "RESTORE_UNKNOWN" && !strings.Contains(status, "ERROR") {
				restoreErr = nil
				break
			}
			restoreErr = fmt.Errorf("PikPak restore failed: %s", firstNonEmpty(restoreResponse.Params.ErrorDetail, restoreResponse.Message, restoreResponse.RestoreStatus))
		}

		// A timeout or gateway error can happen after PikPak accepted the
		// mutation. Reconcile the account before submitting it again so an
		// ambiguous response never creates duplicate restored files.
		if retryablePikPakRestoreError(restoreErr) {
			if file, newlyRestored, found, listErr := p.findRestoredFile(ctx, expectedName, expectedSize, beforeIDs, attempt == 3); listErr == nil && found {
				return file, newlyRestored, nil
			}
		} else {
			return pikPakFile{}, false, fmt.Errorf("restore selected PikPak share file: %w", restoreErr)
		}
		if attempt < 3 {
			timer := time.NewTimer(time.Duration(attempt) * pikPakRestoreRetryDelay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return pikPakFile{}, false, ctx.Err()
			case <-timer.C:
			}
		}
	}
	if restoreErr != nil {
		return pikPakFile{}, false, fmt.Errorf("restore selected PikPak share file after 3 attempts: %w", restoreErr)
	}
	if file, found := p.restoredFileFromTaskIDs(ctx, restoreResponse.Params.TraceFileIDs, expectedName, expectedSize); found {
		return file, !beforeIDs[file.ID], nil
	}
	restoreCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	attempt := 0
	var lastListErr error
	for {
		attempt++
		if restoreResponse.RestoreTaskID != "" {
			var task pikPakRestoreResponse
			path := "/drive/v1/tasks/" + url.PathEscape(restoreResponse.RestoreTaskID)
			if taskErr := p.authenticatedJSON(restoreCtx, http.MethodGet, path, nil, nil, &task); taskErr == nil {
				if file, found := p.restoredFileFromTaskIDs(restoreCtx, task.Params.TraceFileIDs, expectedName, expectedSize); found {
					return file, !beforeIDs[file.ID], nil
				}
				if strings.Contains(strings.ToUpper(task.Phase), "ERROR") || task.Params.ErrorDetail != "" {
					return pikPakFile{}, false, fmt.Errorf("PikPak restore task failed: %s", firstNonEmpty(task.Params.ErrorDetail, task.Message, task.Phase))
				}
			} else {
				lastListErr = fmt.Errorf("check PikPak restore task: %w", taskErr)
			}
		}
		if file, newlyRestored, found, listErr := p.findRestoredFile(restoreCtx, expectedName, expectedSize, beforeIDs, attempt >= 3); listErr == nil {
			lastListErr = nil
			if found {
				return file, newlyRestored, nil
			}
		} else {
			lastListErr = listErr
		}
		select {
		case <-restoreCtx.Done():
			if lastListErr != nil {
				return pikPakFile{}, false, fmt.Errorf("wait for restored PikPak file %q (%d bytes): %w (last drive inventory error: %v)", expectedName, expectedSize, restoreCtx.Err(), lastListErr)
			}
			return pikPakFile{}, false, fmt.Errorf("wait for restored PikPak file %q (%d bytes): %w", expectedName, expectedSize, restoreCtx.Err())
		case <-ticker.C:
		}
	}
}

func (p *pikPakClient) authenticatedFile(ctx context.Context, fileID string) (pikPakFile, error) {
	var file pikPakFile
	path := "/drive/v1/files/" + url.PathEscape(fileID)
	if err := p.authenticatedJSON(ctx, http.MethodGet, path, url.Values{"usage": {"FETCH"}}, nil, &file); err != nil {
		return file, err
	}
	if file.ID == "" {
		return file, errors.New("PikPak returned no restored file details")
	}
	return file, nil
}

func (p *pikPakClient) validateDriveAccess(ctx context.Context) error {
	var about map[string]any
	if err := p.authenticatedJSON(ctx, http.MethodGet, "/drive/v1/about", nil, nil, &about); err != nil {
		return fmt.Errorf("validate PikPak drive access: %w", err)
	}
	return nil
}

func (p *pikPakClient) deleteFile(ctx context.Context, fileID string) error {
	var out map[string]any
	if err := p.authenticatedJSON(ctx, http.MethodPost, "/drive/v1/files:batchDelete", nil, map[string]any{"ids": []string{fileID}}, &out); err != nil {
		return fmt.Errorf("delete restored PikPak file: %w", err)
	}
	return nil
}
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return "unknown error"
}

var publicShareURLPattern = regexp.MustCompile(`https://mypikpak\.com/s/([A-Za-z0-9_-]+)(?:/([A-Za-z0-9_-]+))?`)

func (p *pikPakClient) listShareFiles(ctx context.Context, shareID, parentID string) ([]pikPakFile, error) {
	return p.listShareFilesScoped(ctx, shareID, parentID, "", false)
}

func (p *pikPakClient) listShareFilesScoped(ctx context.Context, shareID, parentID, releaseID string, insideMatchingFolder bool) ([]pikPakFile, error) {
	var all []pikPakFile
	page := ""
	for {
		q := url.Values{"share_id": {shareID}, "parent_id": {parentID}, "thumbnail_size": {"SIZE_LARGE"}, "with_audit": {"true"}, "limit": {"100"}, "page_token": {page}, "filters": {`{"phase":{"eq":"PHASE_TYPE_COMPLETE"},"trashed":{"eq":false}}`}}
		detail, err := p.request(ctx, "/drive/v1/share/detail", q)
		if err != nil {
			return nil, err
		}
		if detail.ShareStatus != "" && detail.ShareStatus != "OK" {
			return nil, fmt.Errorf("PikPak share unavailable: %s %s", detail.ShareStatus, detail.ShareStatusText)
		}
		for _, file := range detail.Files {
			if file.Kind == "drive#folder" {
				folderMatches := insideMatchingFolder || releaseIDsEqual(file.Name, releaseID)
				children, childErr := p.listShareFilesScoped(ctx, shareID, file.ID, releaseID, folderMatches)
				if childErr != nil {
					return nil, childErr
				}
				all = append(all, children...)
				continue
			}
			file.FolderReleaseMatch = insideMatchingFolder
			all = append(all, file)
		}
		page = detail.NextPageToken
		if page == "" {
			return all, nil
		}
	}
}

func inspectPikPakShare(ctx context.Context, client *http.Client, keepshareURL, releaseID string, preferredPatterns []PreferredFilenamePattern, requestedFileID, expectedName string, expectedSize int64) (*pikPakClient, string, pikPakFile, []pikPakFile, error) {
	return inspectPikPakShareWithFolderFallback(ctx, client, keepshareURL, releaseID, preferredPatterns, requestedFileID, expectedName, expectedSize, false)
}

func inspectPikPakShareWithFolderFallback(ctx context.Context, client *http.Client, keepshareURL, releaseID string, preferredPatterns []PreferredFilenamePattern, requestedFileID, expectedName string, expectedSize int64, allowFolderFallback bool) (*pikPakClient, string, pikPakFile, []pikPakFile, error) {
	shareID, err := discoverPikPakShareID(ctx, client, keepshareURL)
	if err != nil {
		return nil, "", pikPakFile{}, nil, err
	}
	pp := newPikPakClient(client)
	scopeReleaseID := ""
	if allowFolderFallback {
		scopeReleaseID = releaseID
	}
	all, err := pp.listShareFilesScoped(ctx, shareID, "", scopeReleaseID, false)
	if err != nil {
		return nil, "", pikPakFile{}, nil, err
	}
	var selected pikPakFile
	found := false
	if requestedFileID != "" {
		selected, found = selectPikPakFileByID(all, requestedFileID, releaseID)
		if !found {
			return nil, "", pikPakFile{}, all, fmt.Errorf("selected PikPak file %s is unavailable or no longer matches %s", requestedFileID, releaseID)
		}
	} else if expectedName != "" || expectedSize > 0 {
		selected, found = selectPikPakFileByIdentity(all, expectedName, expectedSize, releaseID)
		if !found {
			return nil, "", pikPakFile{}, all, fmt.Errorf("previously selected PikPak file %q (%d bytes) is unavailable", expectedName, expectedSize)
		}
	} else {
		selected, found = selectPikPakFile(all, releaseID, preferredPatterns)
		if !found && allowFolderFallback {
			selected, found = selectPikPakFolderFallback(all, preferredPatterns)
		}
	}
	if !found {
		return nil, "", pikPakFile{}, all, fmt.Errorf("PikPak share contained no file matching %s", releaseID)
	}
	return pp, shareID, selected, all, nil
}

func selectPikPakFileByIdentity(files []pikPakFile, expectedName string, expectedSize int64, releaseID string) (pikPakFile, bool) {
	for _, file := range files {
		validFolderFallback := file.FolderReleaseMatch && pikPakVideoFile(file)
		if file.Kind == "drive#folder" || (!releaseIDMatchesText(file.Name, releaseID) && !validFolderFallback) {
			continue
		}
		size, _ := strconv.ParseInt(file.Size, 10, 64)
		if expectedName != "" && !strings.EqualFold(strings.TrimSpace(file.Name), strings.TrimSpace(expectedName)) {
			continue
		}
		if expectedSize > 0 && size != expectedSize {
			continue
		}
		return file, true
	}
	return pikPakFile{}, false
}

func selectPikPakFileByID(files []pikPakFile, fileID, releaseID string) (pikPakFile, bool) {
	for _, file := range files {
		validFolderFallback := file.FolderReleaseMatch && pikPakVideoFile(file)
		if file.ID == fileID && file.Kind != "drive#folder" && (releaseIDMatchesText(file.Name, releaseID) || validFolderFallback) {
			return file, true
		}
	}
	return pikPakFile{}, false
}

// discoverPikPakShareID follows Keepshare's own intermediate redirects but
// deliberately stops before requesting the public PikPak player. Keepshare
// currently redirects keepshare.org -> keepshare.cc -> mypikpak.com; stopping
// at the first hop loses the share ID, while loading the final ?act=play page
// can hang until Client.Timeout even though the share API remains healthy.
// Direct PikPak URLs and HTML/JS-based responses remain supported fallbacks.
func discoverPikPakShareID(ctx context.Context, client *http.Client, sourceURL string) (string, error) {
	if match := publicShareURLPattern.FindStringSubmatch(sourceURL); match != nil {
		return match[1], nil
	}
	noPlayer := *client
	noPlayer.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if publicShareURLPattern.MatchString(req.URL.String()) {
			return http.ErrUseLastResponse
		}
		if len(via) >= 10 {
			return errors.New("too many Keepshare redirects")
		}
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, sourceURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", publicShareUserAgent)
	resp, err := noPlayer.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if location := strings.TrimSpace(resp.Header.Get("Location")); location != "" {
		location = resolveURL(resp.Request.URL.String(), location)
		if match := publicShareURLPattern.FindStringSubmatch(location); match != nil {
			return match[1], nil
		}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return "", err
	}
	if match := publicShareURLPattern.FindStringSubmatch(string(body)); match != nil {
		return match[1], nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 400 {
		return "", fmt.Errorf("Keepshare returned HTTP %d without a PikPak share redirect", resp.StatusCode)
	}
	return "", errors.New("Keepshare did not resolve to a PikPak public share")
}

func selectPikPakFile(files []pikPakFile, releaseID string, preferredPatterns []PreferredFilenamePattern) (pikPakFile, bool) {
	var selected pikPakFile
	found := false
	for i := range files {
		f := files[i]
		if f.Kind == "drive#folder" || !releaseIDMatchesText(f.Name, releaseID) {
			continue
		}
		size, _ := strconv.ParseInt(f.Size, 10, 64)
		preferred, _, priority := matchesAcceptedHTTPPattern(f.Name, preferredPatterns)
		oldPreferred, _, oldPriority := matchesAcceptedHTTPPattern(selected.Name, preferredPatterns)
		oldSize, _ := strconv.ParseInt(selected.Size, 10, 64)
		if !found || (preferred && !oldPreferred) || (preferred && oldPreferred && priority < oldPriority) || (preferred == oldPreferred && priority == oldPriority && size > oldSize) {
			selected, found = f, true
		}
	}
	return selected, found
}

func selectPikPakFolderFallback(files []pikPakFile, preferredPatterns []PreferredFilenamePattern) (pikPakFile, bool) {
	var selected pikPakFile
	found := false
	for _, file := range files {
		if !file.FolderReleaseMatch || !pikPakVideoFile(file) {
			continue
		}
		size, _ := strconv.ParseInt(file.Size, 10, 64)
		preferred, _, priority := matchesAcceptedHTTPPattern(file.Name, preferredPatterns)
		oldPreferred, _, oldPriority := matchesAcceptedHTTPPattern(selected.Name, preferredPatterns)
		oldSize, _ := strconv.ParseInt(selected.Size, 10, 64)
		if !found || (preferred && !oldPreferred) || (preferred && oldPreferred && priority < oldPriority) || (preferred == oldPreferred && priority == oldPriority && (size > oldSize || (size == oldSize && strings.ToLower(file.Name) < strings.ToLower(selected.Name)))) {
			selected, found = file, true
		}
	}
	return selected, found
}

func pikPakVideoFile(file pikPakFile) bool {
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(file.MimeType)), "video/") {
		return true
	}
	switch strings.ToLower(path.Ext(strings.TrimSpace(file.Name))) {
	case ".mp4", ".mkv", ".avi", ".mov", ".m4v", ".webm", ".wmv", ".ts", ".m2ts":
		return true
	default:
		return false
	}
}

func pikPakSearchFiles(files []pikPakFile, selected pikPakFile) ([]string, []domain.SearchFile) {
	names := make([]string, 0, len(files))
	details := make([]domain.SearchFile, 0, len(files))
	for _, file := range files {
		size, _ := strconv.ParseInt(file.Size, 10, 64)
		names = append(names, file.Name)
		details = append(details, domain.SearchFile{Name: file.Name, SizeBytes: size, Matched: file.ID == selected.ID})
	}
	return names, details
}

func resolvePikPakShare(ctx context.Context, client *http.Client, keepshareURL, releaseID string, preferredPatterns []PreferredFilenamePattern, providerFileID, expectedName string, expectedSize int64) (resolvedHTTPFile, error) {
	return resolvePikPakShareWithFolderFallback(ctx, client, keepshareURL, releaseID, preferredPatterns, providerFileID, expectedName, expectedSize, false)
}

func resolvePikPakShareWithFolderFallback(ctx context.Context, client *http.Client, keepshareURL, releaseID string, preferredPatterns []PreferredFilenamePattern, providerFileID, expectedName string, expectedSize int64, allowFolderFallback bool) (resolvedHTTPFile, error) {
	pp, shareID, selected, _, err := inspectPikPakShareWithFolderFallback(ctx, client, keepshareURL, releaseID, preferredPatterns, providerFileID, expectedName, expectedSize, allowFolderFallback)
	if err != nil {
		return resolvedHTTPFile{}, err
	}
	selectedSize, _ := strconv.ParseInt(selected.Size, 10, 64)
	if expectedName != "" && !strings.EqualFold(strings.TrimSpace(selected.Name), strings.TrimSpace(expectedName)) {
		return resolvedHTTPFile{}, fmt.Errorf("selected PikPak file changed: expected %q, resolved %q", expectedName, selected.Name)
	}
	if expectedSize > 0 && selectedSize > 0 && expectedSize != selectedSize {
		return resolvedHTTPFile{}, fmt.Errorf("selected PikPak file size changed: expected %d bytes, resolved %d bytes", expectedSize, selectedSize)
	}
	info, err := pp.request(ctx, "/drive/v1/share/file_info", url.Values{"share_id": {shareID}, "file_id": {selected.ID}})
	if err != nil {
		return resolvedHTTPFile{}, err
	}
	file := info.FileInfo
	if file.ID != "" && file.ID != selected.ID {
		return resolvedHTTPFile{}, fmt.Errorf("PikPak returned the wrong file: requested %s, received %s", selected.ID, file.ID)
	}
	// The public player can expose web_content_link as its default transcode,
	// even when file_info also contains the full-size original. Pin the explicit
	// original representation first; this is the same direct CDN URL a browser
	// download helper sees after Play is clicked.
	direct := preferredPikPakDownloadURL(file)
	if direct == "" {
		return resolvedHTTPFile{}, errors.New("PikPak did not return a downloadable URL for the matching file")
	}
	checksumType, checksum := pikPakFileChecksum(file, selected)
	return resolvedHTTPFile{
		URL: direct, Name: selected.Name, Size: selectedSize,
		Headers:  map[string]string{"User-Agent": publicShareUserAgent, "Referer": "https://mypikpak.com/"},
		Checksum: checksum, ChecksumType: checksumType,
	}, nil
}

func resolveAuthenticatedPikPakShare(ctx context.Context, client *http.Client, keepshareURL, releaseID string, preferredPatterns []PreferredFilenamePattern, providerFileID, expectedName string, expectedSize int64, username, password string, cleanupRestored bool, authenticate func(context.Context, string, string) (*pikPakClient, error)) (resolvedHTTPFile, error) {
	return resolveAuthenticatedPikPakShareWithFolderFallback(ctx, client, keepshareURL, releaseID, preferredPatterns, providerFileID, expectedName, expectedSize, username, password, cleanupRestored, authenticate, false)
}

func resolveAuthenticatedPikPakShareWithFolderFallback(ctx context.Context, client *http.Client, keepshareURL, releaseID string, preferredPatterns []PreferredFilenamePattern, providerFileID, expectedName string, expectedSize int64, username, password string, cleanupRestored bool, authenticate func(context.Context, string, string) (*pikPakClient, error), allowFolderFallback bool) (resolvedHTTPFile, error) {
	_, shareID, selected, _, err := inspectPikPakShareWithFolderFallback(ctx, client, keepshareURL, releaseID, preferredPatterns, providerFileID, expectedName, expectedSize, allowFolderFallback)
	if err != nil {
		return resolvedHTTPFile{}, err
	}
	selectedSize, _ := strconv.ParseInt(selected.Size, 10, 64)
	if expectedName != "" && !strings.EqualFold(strings.TrimSpace(selected.Name), strings.TrimSpace(expectedName)) {
		return resolvedHTTPFile{}, fmt.Errorf("selected PikPak file changed: expected %q, resolved %q", expectedName, selected.Name)
	}
	if expectedSize > 0 && selectedSize > 0 && expectedSize != selectedSize {
		return resolvedHTTPFile{}, fmt.Errorf("selected PikPak file size changed: expected %d bytes, resolved %d bytes", expectedSize, selectedSize)
	}
	var authenticated *pikPakClient
	if authenticate != nil {
		authenticated, err = authenticate(ctx, username, password)
	} else {
		authenticated = newPikPakClient(client)
		err = authenticated.login(ctx, username, password)
	}
	if err != nil {
		return resolvedHTTPFile{}, err
	}
	restoredEntry, newlyRestored, err := authenticated.restoreSharedFile(ctx, shareID, selected.ID, selected.Name, selectedSize)
	if err != nil {
		return resolvedHTTPFile{}, err
	}
	restored, err := authenticated.authenticatedFile(ctx, restoredEntry.ID)
	if err != nil {
		return resolvedHTTPFile{}, fmt.Errorf("resolve restored PikPak file: %w", err)
	}
	restoredSize, _ := strconv.ParseInt(restored.Size, 10, 64)
	if restoredSize > 0 && selectedSize > 0 && restoredSize != selectedSize {
		return resolvedHTTPFile{}, fmt.Errorf("restored PikPak file size changed: selected %d bytes, restored %d bytes", selectedSize, restoredSize)
	}
	if restored.Name != "" && !strings.EqualFold(strings.TrimSpace(restored.Name), strings.TrimSpace(selected.Name)) {
		return resolvedHTTPFile{}, fmt.Errorf("restored PikPak file changed: selected %q, restored %q", selected.Name, restored.Name)
	}
	if !selected.FolderReleaseMatch && !releaseIDMatchesText(firstNonEmpty(restored.Name, selected.Name), releaseID) {
		return resolvedHTTPFile{}, fmt.Errorf("restored PikPak filename no longer matches release ID %s", releaseID)
	}
	direct := preferredPikPakDownloadURL(restored)
	if direct == "" {
		return resolvedHTTPFile{}, errors.New("PikPak account restored the matching file but returned no authenticated download URL")
	}
	resolved := resolvedHTTPFile{
		URL:              direct,
		Name:             selected.Name,
		Size:             selectedSize,
		Headers:          map[string]string{"User-Agent": publicShareUserAgent, "Referer": "https://mypikpak.com/"},
		Authenticated:    true,
		RestoredFileID:   restoredEntry.ID,
		RestoredParentID: restored.ParentID,
		NewlyRestored:    newlyRestored,
	}
	resolved.ChecksumType, resolved.Checksum = pikPakFileChecksum(restored, selected)
	if cleanupRestored && newlyRestored {
		resolved.Cleanup = func(cleanupCtx context.Context) error {
			return authenticated.deleteFile(cleanupCtx, restoredEntry.ID)
		}
	}
	return resolved, nil
}

func pikPakFileChecksum(files ...pikPakFile) (string, string) {
	for _, file := range files {
		// PikPak's `hash` looks like SHA-1 but identifies the underlying
		// resource (and commonly matches the signed URL's `g` value); it is not
		// a content checksum. Only the explicit md5_checksum field is safe to
		// compare with the completed file.
		value := strings.ToLower(strings.TrimSpace(file.MD5Checksum))
		if decoded, err := hex.DecodeString(value); err == nil && len(decoded) == 16 {
			return "md5", value
		}
	}
	return "", ""
}

func preferredPikPakDownloadURL(file pikPakFile) string {
	for _, media := range file.Medias {
		if media.IsOrigin && media.Link.URL != "" {
			return media.Link.URL
		}
	}
	if file.WebContentLink != "" {
		return file.WebContentLink
	}
	if file.Links.ApplicationOctetStream.URL != "" {
		return file.Links.ApplicationOctetStream.URL
	}
	for _, media := range file.Medias {
		if media.Link.URL != "" {
			return media.Link.URL
		}
	}
	return ""
}

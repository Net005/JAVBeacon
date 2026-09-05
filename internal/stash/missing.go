package stash

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/Net005/JAVBeacon/internal/domain"
)

// DefaultMissingQuery is the GraphQL query used by the "Missing Library
// Files" scan (TODO-2.0 Phase 2). It requests every field the feature's
// filter set needs beyond what DefaultQuery already fetches for the local-
// library sync above: per-file paths (to detect a scene with no surviving
// file on disk), O-counter/play count/last played/O-history (for filtering),
// studio and tags, and the scene's URL list (to find a JavLibrary product URL
// for retrieval). Operators with a differently-shaped StashApp GraphQL schema
// can override this via the stash_missing_graphql_query setting, mirroring
// how stash_graphql_query already overrides DefaultQuery.
const DefaultMissingQuery = `query JAVBeaconMissingScenes { findScenes(filter: { per_page: -1 }) { scenes { id title code date o_counter play_count last_played_at o_history studio { name } tags { name } urls files { path basename } } } }`

// recoverySiteTitle names the single, lazily-created, disabled Site that
// releases retrieved by this flow are attached to. Releases need a SiteID,
// but a scene recovered this way was never discovered through any of the
// user's configured scraper sites - a synthetic, clearly-labeled site
// avoids polluting the real site list while still satisfying the schema.
const recoverySiteTitle = "Missing Library Recovery (StashApp)"

const missingScanStatusSetting = "stash_missing_scan_status"

// MissingStatus is the pollable progress/result of a "Missing Library
// Files" scan, mirroring Status above. Processed/CurrentItem let the UI
// show live "checked N of Scenes" progress and the scene currently being
// checked while the scan is running, instead of only learning the final
// counts once the whole scan has finished (TODO-2.0 Task A: scan progress
// was previously invisible, stuck at 0 until completion).
type MissingStatus struct {
	Running     bool      `json:"running"`
	StartedAt   time.Time `json:"started_at,omitempty"`
	FinishedAt  time.Time `json:"finished_at,omitempty"`
	Scenes      int       `json:"scenes"`
	Processed   int       `json:"processed"`
	CurrentItem string    `json:"current_item,omitempty"`
	Missing     int       `json:"missing"`
	Matched     int       `json:"matched"`
	Error       string    `json:"error,omitempty"`
}

func (s *Service) MissingScanStatus() MissingStatus {
	s.missingMu.RLock()
	defer s.missingMu.RUnlock()
	return s.missingStatus
}

// restoreMissingScanStatus reloads the last completed scan summary after an
// application restart. Older installations do not have the summary setting
// yet, so fall back to the newest last_scan_at value already recorded on the
// missing-scene rows. Future runs persist the complete counts below.
func (s *Service) restoreMissingScanStatus(ctx context.Context) {
	settings, err := s.store.Settings(ctx)
	if err == nil {
		var status MissingStatus
		if raw := strings.TrimSpace(settings[missingScanStatusSetting]); raw != "" && json.Unmarshal([]byte(raw), &status) == nil && !status.FinishedAt.IsZero() {
			status.Running = false
			status.CurrentItem = ""
			s.missingStatus = status
			return
		}
	}
	rows, err := s.store.StashMissingScenes(ctx, domain.StashMissingFilter{Limit: 5000})
	if err != nil || len(rows) == 0 || rows[0].LastScanAt.IsZero() {
		return
	}
	status := MissingStatus{FinishedAt: rows[0].LastScanAt, Missing: len(rows)}
	for _, row := range rows {
		if row.ReleaseID != 0 {
			status.Matched++
		}
	}
	s.missingStatus = status
}

func (s *Service) persistMissingScanStatus(ctx context.Context, status MissingStatus) {
	encoded, err := json.Marshal(status)
	if err == nil {
		err = s.store.SaveSettings(ctx, map[string]string{missingScanStatusSetting: string(encoded)})
	}
	if err != nil {
		s.log.Warn("could not persist Missing Library Files scan status", "error", err)
	}
}

// ClearMissingScenes wipes every recorded Missing Library Files result for
// the manual "Clear results" action. It refuses to run while a scan is in
// progress, since that scan is actively writing rows the clear would
// otherwise race with.
func (s *Service) ClearMissingScenes(ctx context.Context) (int64, error) {
	if s.MissingScanStatus().Running {
		return 0, errors.New("cannot clear Missing Library Files results while a scan is running")
	}
	n, err := s.store.ClearStashMissingScenes(ctx)
	if err == nil {
		s.log.Info("Missing Library Files results cleared", "removed", n)
	}
	return n, err
}

// StartMissingScan kicks off a background scan of every scene StashApp
// reports (via stash_missing_graphql_query/DefaultMissingQuery), records any
// whose file(s) cannot be found on disk (after applying the
// stash_missing_path_remaps list, for setups where StashApp and
// JAVBeacon see the library under different mount paths) into stash_missing_scenes,
// and opportunistically links any that match an existing release by video
// ID. Poll MissingScanStatus for progress/results.
func (s *Service) StartMissingScan(ctx context.Context) error {
	s.missingMu.Lock()
	if s.missingStatus.Running {
		s.missingMu.Unlock()
		return errors.New("Missing Library Files scan already running")
	}
	s.missingStatus = MissingStatus{Running: true, StartedAt: time.Now().UTC()}
	s.missingMu.Unlock()
	s.log.Info("Missing Library Files scan started")
	go s.runMissingScan(context.WithoutCancel(ctx))
	return nil
}

func (s *Service) runMissingScan(ctx context.Context) {
	result := s.MissingScanStatus()
	scanStarted := time.Now().UTC()
	// flush publishes the current (in-progress) result so MissingScanStatus
	// polls see live counts instead of only the final snapshot - called
	// after every meaningful step, not just once at the end.
	flush := func() {
		s.missingMu.Lock()
		s.missingStatus = result
		s.missingMu.Unlock()
	}
	defer func() {
		result.Running = false
		result.FinishedAt = time.Now().UTC()
		result.CurrentItem = ""
		flush()
		s.persistMissingScanStatus(context.WithoutCancel(ctx), result)
	}()
	settings, err := s.store.Settings(ctx)
	if err != nil {
		result.Error = err.Error()
		return
	}
	baseURL := strings.TrimRight(strings.TrimSpace(settings["stash_base_url"]), "/")
	if baseURL == "" {
		result.Error = "StashApp Base URL is not configured"
		return
	}
	query := strings.TrimSpace(settings["stash_missing_graphql_query"])
	if query == "" {
		query = DefaultMissingQuery
	}
	apiKey := strings.TrimSpace(settings["stash_api_key"])
	result.CurrentItem = "Fetching scene list from StashApp…"
	flush()
	candidates, err := s.fetchMissingCandidates(ctx, baseURL+"/graphql", query, apiKey)
	if err != nil {
		result.Error = err.Error()
		s.log.Error("Missing Library Files scan failed", "url", baseURL, "error", err)
		return
	}
	if scopes := missingFolderScopes(settings["stash_missing_folder_scope"]); len(scopes) > 0 {
		candidates = filterCandidatesByFolderScope(candidates, scopes)
	}
	result.Scenes = len(candidates)
	result.CurrentItem = ""
	flush()

	byCanonical, err := s.releasesByCanonicalVideoID(ctx)
	if err != nil {
		result.Error = err.Error()
		return
	}

	remaps := parsePathRemaps(settings["stash_missing_path_remaps"])
	for _, c := range candidates {
		result.Processed++
		result.CurrentItem = missingCandidateLabel(c)
		if s.filesExist(c.Paths, remaps) {
			flush()
			continue
		}
		result.Missing++
		x := domain.StashMissingScene{
			StashSceneID: c.ID, Title: c.Title, Code: c.Code, Date: c.Date,
			OCounter: c.OCounter, PlayCount: c.PlayCount, LastPlayedAt: c.LastPlayedAt, LastOCountAt: c.LastOCountAt,
			Studio: c.Studio, Tags: c.Tags, URLs: c.URLs, JavLibraryURL: javLibraryURLFrom(c.URLs),
			Paths: c.Paths,
		}
		if len(c.Paths) > 0 {
			x.Path = c.Paths[0]
		}
		id, upsertErr := s.store.UpsertStashMissingScene(ctx, x)
		if upsertErr != nil {
			result.Error = upsertErr.Error()
			s.log.Error("Missing Library Files scan: failed to record a missing scene", "scene", missingCandidateLabel(c), "error", upsertErr)
			flush()
			continue
		}
		for _, token := range append([]string{c.Code}, idInText.FindAllString(c.Title, -1)...) {
			if key := canonical(token); key != "" {
				if release, ok := byCanonical[key]; ok {
					if linkErr := s.store.LinkStashMissingRelease(ctx, id, release.ID); linkErr == nil {
						result.Matched++
					}
					break
				}
			}
		}
		flush()
	}
	if _, pruneErr := s.store.PruneStashMissingScenes(ctx, scanStarted); pruneErr != nil {
		result.Error = pruneErr.Error()
	}
	s.log.Info("Missing Library Files scan completed", "scenes", result.Scenes, "missing", result.Missing, "matched", result.Matched)
}

// missingCandidateLabel picks the most useful human-readable label for a
// scene while scanning, for the "current item" progress display and log
// lines - the code (e.g. "ABC-123") when present, else the title, else a
// generic fallback so the UI never shows a blank current-item line.
func missingCandidateLabel(c missingCandidate) string {
	if c.Code != "" {
		return c.Code
	}
	if c.Title != "" {
		return c.Title
	}
	return "scene #" + c.ID
}

// missingFolderScopes parses the stash_missing_folder_scope setting into
// one or more trimmed, lowercased scope substrings, one per line (mirroring
// domain.ParseIgnoreList's newline-only convention elsewhere in this app).
// Multiple scopes let a setup with several studio subdirectories restrict a
// scan to all of them at once instead of just one.
func missingFolderScopes(raw string) []string {
	parsed := domain.ParseIgnoreList(raw)
	out := make([]string, 0, len(parsed))
	for _, p := range parsed {
		out = append(out, strings.ToLower(p))
	}
	return out
}

// filterCandidatesByFolderScope restricts a scan to scenes whose reported
// file path contains at least one of scopesLower (already lowercased) as a
// substring - a simple, StashApp-schema-agnostic way to scope the scan to
// one or more folders (e.g. specific studios' subdirectories) without
// needing filter support in the GraphQL query itself. scopesLower must be
// non-empty; callers skip calling this at all when the setting is blank, so
// an unscoped scan never pays for the extra pass.
func filterCandidatesByFolderScope(candidates []missingCandidate, scopesLower []string) []missingCandidate {
	out := candidates[:0]
	for _, c := range candidates {
		for _, p := range c.Paths {
			lower := strings.ToLower(p)
			matched := false
			for _, scope := range scopesLower {
				if strings.Contains(lower, scope) {
					matched = true
					break
				}
			}
			if matched {
				out = append(out, c)
				break
			}
		}
	}
	return out
}

// releasesByCanonicalVideoID loads every release, keyed by canonical(video
// ID), so a scan can match hundreds of missing scenes against the existing
// library with one pass instead of one query per scene.
func (s *Service) releasesByCanonicalVideoID(ctx context.Context) (map[string]domain.Release, error) {
	out := map[string]domain.Release{}
	for offset := 0; ; offset += 500 {
		rows, err := s.store.Releases(ctx, domain.ReleaseFilter{Limit: 500, Offset: offset})
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			if key := canonical(r.VideoID); key != "" {
				out[key] = r
			}
		}
		if len(rows) < 500 {
			break
		}
	}
	return out, nil
}

// missingCandidate is one scene as reported by fetchMissingCandidates,
// already flattened from the (possibly custom) GraphQL response shape.
type missingCandidate struct {
	ID, Title, Code, Date              string
	OCounter, PlayCount                int
	LastPlayedAt, LastOCountAt, Studio string
	Tags, URLs, Paths                  []string
}

func (s *Service) fetchMissingCandidates(ctx context.Context, url, query, apiKey string) ([]missingCandidate, error) {
	body, _ := json.Marshal(map[string]string{"query": query})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("ApiKey", apiKey)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("Stash returned HTTP %d", resp.StatusCode)
	}
	var payload struct {
		Data struct {
			FindScenes struct {
				Scenes []struct {
					ID           string   `json:"id"`
					Title        string   `json:"title"`
					Code         string   `json:"code"`
					Date         string   `json:"date"`
					OCounter     int      `json:"o_counter"`
					PlayCount    int      `json:"play_count"`
					LastPlayedAt string   `json:"last_played_at"`
					OHistory     []string `json:"o_history"`
					Studio       *struct {
						Name string `json:"name"`
					} `json:"studio"`
					Tags []struct {
						Name string `json:"name"`
					} `json:"tags"`
					URLs  []string `json:"urls"`
					Files []struct {
						Path     string `json:"path"`
						Basename string `json:"basename"`
					} `json:"files"`
				} `json:"scenes"`
			} `json:"findScenes"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}
	if len(payload.Errors) > 0 {
		// Older StashApp GraphQL schemas may not expose o_history. Preserve
		// the entire missing-file scan in that case and simply leave the
		// Last O Count Date blank, matching the main local-sync fallback.
		if strings.Contains(query, "o_history") {
			return s.fetchMissingCandidates(ctx, url, strings.ReplaceAll(query, "o_history", ""), apiKey)
		}
		return nil, errors.New(payload.Errors[0].Message)
	}
	out := make([]missingCandidate, 0, len(payload.Data.FindScenes.Scenes))
	for _, scene := range payload.Data.FindScenes.Scenes {
		c := missingCandidate{
			ID: scene.ID, Title: scene.Title, Code: scene.Code, Date: scene.Date,
			OCounter: scene.OCounter, PlayCount: scene.PlayCount, LastPlayedAt: scene.LastPlayedAt,
			URLs: scene.URLs,
		}
		for _, timestamp := range scene.OHistory {
			if timestamp > c.LastOCountAt {
				c.LastOCountAt = timestamp
			}
		}
		if scene.Studio != nil {
			c.Studio = scene.Studio.Name
		}
		for _, tag := range scene.Tags {
			c.Tags = append(c.Tags, tag.Name)
		}
		for _, file := range scene.Files {
			if file.Path != "" {
				c.Paths = append(c.Paths, file.Path)
			}
		}
		out = append(out, c)
	}
	return out, nil
}

// javLibraryURLFrom picks the first URL in a scene's StashApp URL list that
// looks like a JavLibrary product page, since that list may also contain
// unrelated links (a studio page, a torrent site, etc).
func javLibraryURLFrom(urls []string) string {
	for _, u := range urls {
		if strings.Contains(strings.ToLower(u), "javlibrary.com") {
			return u
		}
	}
	return ""
}

// PathRemap is one StashApp-mount-prefix-to-local-mount-prefix pair for the
// Missing Library Files path remap. Settings TODO-2.0's Settings overhaul
// replaced the original single stash_missing_path_from/stash_missing_path_to
// pair with a list of these (JSON-encoded in the stash_missing_path_remaps
// setting), since a StashApp instance can report more than one distinct
// mount prefix (e.g. separate volumes for different studios).
type PathRemap struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// parsePathRemaps decodes the stash_missing_path_remaps setting. Invalid or
// empty JSON yields no remaps (equivalent to every path being used as-is),
// rather than an error - a scan should still run against unremapped paths
// rather than fail outright over a malformed setting.
func parsePathRemaps(raw string) []PathRemap {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var remaps []PathRemap
	if err := json.Unmarshal([]byte(raw), &remaps); err != nil {
		return nil
	}
	return remaps
}

// remapPath rewrites a StashApp-reported file path for local disk access
// when the two applications see the library under different mount paths -
// e.g. StashApp reports "/data/jav/ABC-123.mp4" but JAVBeacon's own
// mount is "/mnt/jav/ABC-123.mp4". remaps is tried in order; the first pair
// whose From is a prefix of path wins. A path that does not start with any
// configured prefix (including when remaps is empty) is returned unchanged.
func remapPath(path string, remaps []PathRemap) string {
	for _, r := range remaps {
		if r.From == "" {
			continue
		}
		if rest, ok := strings.CutPrefix(path, r.From); ok {
			return r.To + rest
		}
	}
	return path
}

// missingScanStatTimeout bounds how long a single path check may block a
// running scan. A large library's paths often live on a network mount
// (NFS/SMB); if even one of them is on a stalled or unreachable share, a
// bare os.Stat can hang far longer than the whole rest of the scan would
// otherwise take, which looks indistinguishable from the progress display
// being frozen (the bug report this addresses: "still says for the entire
// run with no clear indication"). A var, not a const, so tests can shrink
// it instead of needing a real multi-second hang to exercise the timeout
// path.
var missingScanStatTimeout = 5 * time.Second

// statWithTimeout runs os.Stat without letting it block the caller for
// longer than timeout. os.Stat itself is not cancellable, so a stat that
// doesn't return in time is treated as "not found" for the caller's
// purposes while its goroutine is left to finish (or keep hanging) on its
// own - a small, bounded leak that only occurs against a genuinely
// unresponsive mount, which is preferable to stalling the entire scan on
// one path.
func statWithTimeout(path string, timeout time.Duration) (os.FileInfo, error) {
	type result struct {
		info os.FileInfo
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		info, err := os.Stat(path)
		ch <- result{info, err}
	}()
	select {
	case r := <-ch:
		return r.info, r.err
	case <-time.After(timeout):
		return nil, fmt.Errorf("stat timed out after %s", timeout)
	}
}

// filesExist reports whether at least one of a scene's reported files can
// still be found on disk (after the path remap). A scene with zero reported
// paths at all is treated as missing too - StashApp has nothing to point at
// on disk for it either way.
func (s *Service) filesExist(paths []string, remaps []PathRemap) bool {
	for _, p := range paths {
		if p == "" {
			continue
		}
		if _, err := statWithTimeout(remapPath(p, remaps), missingScanStatTimeout); err == nil {
			return true
		}
	}
	return false
}

// recoverySite finds or lazily creates the synthetic, disabled site that
// releases retrieved from JavLibrary during recovery are attached to.
func (s *Service) recoverySite(ctx context.Context) (domain.Site, error) {
	sites, err := s.store.Sites(ctx)
	if err != nil {
		return domain.Site{}, err
	}
	for _, site := range sites {
		if site.Title == recoverySiteTitle {
			return site, nil
		}
	}
	return s.store.SaveSite(ctx, domain.Site{Title: recoverySiteTitle, Type: "Site", Name: "JavLibrary", Enabled: false})
}

// RetrieveResult is one scene's outcome from a RetrieveMissing run.
type RetrieveResult struct {
	SceneID int64  `json:"scene_id"`
	VideoID string `json:"video_id,omitempty"`
	Status  string `json:"status"` // already_linked | retrieved | failed
	Error   string `json:"error,omitempty"`
}

// RetrieveStatus is the pollable progress/result of a retrieval run.
// CurrentItem names the scene currently being retrieved, and Results grows
// with each scene's outcome as the run progresses (rather than only being
// populated once the whole run finishes), so a poller can render live
// progress instead of a stalled 0/Total.
type RetrieveStatus struct {
	Running     bool             `json:"running"`
	StartedAt   time.Time        `json:"started_at,omitempty"`
	FinishedAt  time.Time        `json:"finished_at,omitempty"`
	Total       int              `json:"total"`
	CurrentItem string           `json:"current_item,omitempty"`
	Retrieved   int              `json:"retrieved"`
	Failed      int              `json:"failed"`
	Results     []RetrieveResult `json:"results,omitempty"`
	Error       string           `json:"error,omitempty"`
}

func (s *Service) RetrieveScanStatus() RetrieveStatus {
	s.retrieveMu.RLock()
	defer s.retrieveMu.RUnlock()
	return s.retrieveStatus
}

// StartRetrieve kicks off a background run that, for every given missing
// scene, checks the JAVBeacon database for an existing entry and - if
// none exists - scrapes the scene's JavLibrary URL (from StashApp's URL
// list) into a new release attached to the synthetic recovery site. This is
// TODO-2.0 Phase 2's "search and add the release from JavLibrary ... with
// priority 1" step: it runs eagerly and immediately for the selected
// scenes rather than being enqueued behind the scrape-job priority queue
// (whose numeric scale runs the opposite direction from "1" reading as
// urgent), which is the interpretation that actually matches the request's
// intent of "do this now, before anything else in this flow."
func (s *Service) StartRetrieve(ctx context.Context, ids []int64) error {
	s.retrieveMu.Lock()
	if s.retrieveStatus.Running {
		s.retrieveMu.Unlock()
		return errors.New("retrieval already running")
	}
	s.retrieveStatus = RetrieveStatus{Running: true, StartedAt: time.Now().UTC(), Total: len(ids)}
	s.retrieveMu.Unlock()
	go s.runRetrieve(context.WithoutCancel(ctx), ids)
	return nil
}

func (s *Service) runRetrieve(ctx context.Context, ids []int64) {
	result := s.RetrieveScanStatus()
	flush := func() {
		s.retrieveMu.Lock()
		s.retrieveStatus = result
		s.retrieveMu.Unlock()
	}
	defer func() {
		result.Running = false
		result.FinishedAt = time.Now().UTC()
		result.CurrentItem = ""
		flush()
	}()
	record := func(r RetrieveResult, failed bool) {
		if failed {
			result.Failed++
		}
		result.Results = append(result.Results, r)
		flush()
	}
	for _, id := range ids {
		result.CurrentItem = fmt.Sprintf("scene #%d", id)
		flush()
		scene, err := s.store.StashMissingScene(ctx, id)
		if err != nil {
			record(RetrieveResult{SceneID: id, Status: "failed", Error: err.Error()}, true)
			continue
		}
		result.CurrentItem = retrieveSceneLabel(scene)
		flush()
		if scene.ReleaseID != 0 {
			record(RetrieveResult{SceneID: id, VideoID: scene.ReleaseVideoID, Status: "already_linked"}, false)
			continue
		}
		if s.jav == nil {
			_ = s.store.SetStashMissingStatus(ctx, id, "retrieve_failed", "JavLibrary scraper is not available")
			record(RetrieveResult{SceneID: id, Status: "failed", Error: "JavLibrary scraper is not available"}, true)
			continue
		}
		if scene.JavLibraryURL == "" {
			_ = s.store.SetStashMissingStatus(ctx, id, "retrieve_failed", "scene has no JavLibrary URL in StashApp")
			record(RetrieveResult{SceneID: id, Status: "failed", Error: "scene has no JavLibrary URL in StashApp"}, true)
			continue
		}
		_ = s.store.SetStashMissingStatus(ctx, id, "retrieving", "")
		fallback := scene.Code
		if fallback == "" {
			fallback = scene.Title
		}
		release, err := s.jav.AddByURL(ctx, scene.JavLibraryURL, fallback)
		if err != nil {
			_ = s.store.SetStashMissingStatus(ctx, id, "retrieve_failed", err.Error())
			s.log.Error("Missing Library Files retrieval failed", "scene", result.CurrentItem, "error", err)
			record(RetrieveResult{SceneID: id, Status: "failed", Error: err.Error()}, true)
			continue
		}
		site, err := s.recoverySite(ctx)
		if err != nil {
			_ = s.store.SetStashMissingStatus(ctx, id, "retrieve_failed", err.Error())
			record(RetrieveResult{SceneID: id, Status: "failed", Error: err.Error()}, true)
			continue
		}
		release.SiteID = site.ID
		release.StashSceneID = scene.StashSceneID
		if _, err := s.store.UpsertRelease(ctx, release); err != nil {
			_ = s.store.SetStashMissingStatus(ctx, id, "retrieve_failed", err.Error())
			record(RetrieveResult{SceneID: id, Status: "failed", Error: err.Error()}, true)
			continue
		}
		releaseID, err := s.findReleaseID(ctx, release.VideoID)
		if err != nil || releaseID == 0 {
			msg := "could not look up the release just created"
			if err != nil {
				msg = err.Error()
			}
			_ = s.store.SetStashMissingStatus(ctx, id, "retrieve_failed", msg)
			record(RetrieveResult{SceneID: id, Status: "failed", Error: msg}, true)
			continue
		}
		if err := s.store.LinkStashMissingRelease(ctx, id, releaseID); err != nil {
			record(RetrieveResult{SceneID: id, Status: "failed", Error: err.Error()}, true)
			continue
		}
		result.Retrieved++
		s.log.Info("Missing Library Files scene retrieved", "scene", result.CurrentItem, "video_id", release.VideoID)
		record(RetrieveResult{SceneID: id, VideoID: release.VideoID, Status: "retrieved"}, false)
	}
}

// retrieveSceneLabel picks the most useful label for a scene's "current
// item" progress display during retrieval - the release code/title
// JAVBeacon already knows the scene by, falling back to its StashApp
// scene ID.
func retrieveSceneLabel(scene domain.StashMissingScene) string {
	if scene.Code != "" {
		return scene.Code
	}
	if scene.Title != "" {
		return scene.Title
	}
	return "scene #" + scene.StashSceneID
}

// findReleaseID looks up the ID of a just-upserted release by video ID,
// matching canonically the same way scan/sync matching does.
func (s *Service) findReleaseID(ctx context.Context, videoID string) (int64, error) {
	rows, err := s.store.Releases(ctx, domain.ReleaseFilter{Search: videoID, Limit: 20})
	if err != nil {
		return 0, err
	}
	key := canonical(videoID)
	for _, r := range rows {
		if canonical(r.VideoID) == key {
			return r.ID, nil
		}
	}
	return 0, nil
}

const (
	ApplyModeMonitorOnly     = "monitor_only"
	ApplyModeMonitorDownload = "monitor_download"
)

// ApplyResult is one scene's outcome from an ApplySelection run.
type ApplyResult struct {
	SceneID       int64   `json:"scene_id"`
	ReleaseID     int64   `json:"release_id,omitempty"`
	VideoID       string  `json:"video_id,omitempty"`
	Title         string  `json:"title,omitempty"`
	Stage         string  `json:"stage,omitempty"`
	Status        string  `json:"status"` // queued | working | monitored | found | not_found | failed
	Reason        string  `json:"reason,omitempty"`
	Error         string  `json:"error,omitempty"`
	Provider      string  `json:"provider,omitempty"`
	TorrentTitle  string  `json:"torrent_title,omitempty"`
	SourceURL     string  `json:"source_url,omitempty"`
	Size          string  `json:"size,omitempty"`
	DownloadState string  `json:"download_state,omitempty"`
	MatchReason   string  `json:"match_reason,omitempty"`
	Seeds         int     `json:"seeds"`
	Peers         int     `json:"peers"`
	Progress      float64 `json:"progress"`
	ETASeconds    int64   `json:"eta_seconds"`
	SeenComplete  int64   `json:"seen_complete"`
}

// ApplyStatus is the pollable progress/result of an ApplySelection run.
// Found is exactly the count the person asked to see reported back
// ("show results (found x releases on sukebei etc..)"). CurrentItem and
// live-growing Results give the UI something to show while a large
// selection's search-and-download apply run is still in progress, rather
// than a progress bar stuck at 0 until the whole batch finishes.
type ApplyStatus struct {
	Running           bool          `json:"running"`
	Mode              string        `json:"mode,omitempty"`
	AllowNonPreferred bool          `json:"allow_non_preferred_filenames,omitempty"`
	StartedAt         time.Time     `json:"started_at,omitempty"`
	FinishedAt        time.Time     `json:"finished_at,omitempty"`
	Total             int           `json:"total"`
	Processed         int           `json:"processed"`
	CurrentItem       string        `json:"current_item,omitempty"`
	Monitored         int           `json:"monitored"`
	Found             int           `json:"found"`
	NotFound          int           `json:"not_found"`
	Failed            int           `json:"failed"`
	Results           []ApplyResult `json:"results,omitempty"`
	Error             string        `json:"error,omitempty"`
}

func (s *Service) ApplyRunStatus() ApplyStatus {
	s.applyMu.RLock()
	defer s.applyMu.RUnlock()
	status := s.applyStatus
	status.Results = append([]ApplyResult(nil), s.applyStatus.Results...)
	return status
}

// StartApply kicks off a background run applying one of the two
// TODO-2.0 Phase 2 result actions to every given, already-linked missing
// scene: mode ApplyModeMonitorOnly just flips MonitorDownload on; mode
// ApplyModeMonitorDownload additionally searches and downloads immediately
// in the background (no per-release confirmation), via download.Service's
// SearchAndDownloadNow. allowNonPreferred is forwarded to SearchAndDownloadNow
// as-is (see its doc comment) - it enables the TODO-2.0 Task A fallback chain
// that accepts a seeded-but-unaccepted, or failing that merely most-recent,
// result instead of requiring a clean accepted-filename-pattern match.
// Both modes also always set IgnoreLocalForceDownload on the release (see
// its doc comment) - unlike allowNonPreferred this is not a toggle, since
// every release reachable from here already has a StashApp scene by
// definition of being a "missing file" entry.
func (s *Service) StartApply(ctx context.Context, ids []int64, mode string, allowNonPreferred bool) error {
	s.applyMu.Lock()
	if s.applyStatus.Running {
		s.applyMu.Unlock()
		return errors.New("apply already running")
	}
	s.applyStatus = ApplyStatus{Running: true, Mode: mode, AllowNonPreferred: allowNonPreferred, StartedAt: time.Now().UTC(), Total: len(ids)}
	s.applyMu.Unlock()
	go s.runApply(context.WithoutCancel(ctx), ids, mode, allowNonPreferred)
	return nil
}

func (s *Service) runApply(ctx context.Context, ids []int64, mode string, allowNonPreferred bool) {
	result := s.ApplyRunStatus()
	result.Results = make([]ApplyResult, len(ids))
	for index, id := range ids {
		result.Results[index] = ApplyResult{SceneID: id, Stage: "queued", Status: "queued"}
	}
	flush := func() {
		s.applyMu.Lock()
		s.applyStatus = result
		s.applyMu.Unlock()
	}
	defer func() {
		result.Running = false
		result.FinishedAt = time.Now().UTC()
		result.CurrentItem = ""
		flush()
	}()
	update := func(index int, r ApplyResult) {
		result.Results[index] = r
		flush()
	}
	record := func(index int, r ApplyResult, failed bool) {
		if failed {
			result.Failed++
		}
		result.Processed++
		result.Results[index] = r
		flush()
	}
	flush()
	for index, id := range ids {
		result.CurrentItem = fmt.Sprintf("scene #%d", id)
		task := ApplyResult{SceneID: id, Stage: "database_lookup", Status: "working"}
		update(index, task)
		scene, err := s.store.StashMissingScene(ctx, id)
		if err != nil {
			task.Status = "failed"
			task.Error = "JAVBeacon database lookup failed: " + err.Error()
			record(index, task, true)
			continue
		}
		task.Title = scene.Title
		task.VideoID = scene.Code
		if scene.ReleaseID == 0 {
			task.Status = "failed"
			switch {
			case scene.Status == "retrieve_failed" && scene.Message != "":
				task.Stage = "javlibrary_lookup"
				task.Error = "JavLibrary lookup failed: " + scene.Message
			case scene.JavLibraryURL == "":
				task.Error = "Release is not in the JAVBeacon database and this StashApp scene has no JavLibrary URL"
			default:
				task.Error = "Release is not in the JAVBeacon database; retrieve it from JavLibrary first"
			}
			record(index, task, true)
			continue
		}
		result.CurrentItem = retrieveSceneLabel(scene)
		task.ReleaseID = scene.ReleaseID
		task.VideoID = scene.ReleaseVideoID
		task.Stage = "release_lookup"
		update(index, task)
		release, err := s.store.Release(ctx, scene.ReleaseID)
		if err != nil {
			task.Status = "failed"
			task.Error = "Linked JAVBeacon release lookup failed: " + err.Error()
			record(index, task, true)
			continue
		}
		result.CurrentItem = release.VideoID
		task.ReleaseID = release.ID
		task.VideoID = release.VideoID
		task.Title = release.Title
		task.Stage = "monitoring"
		update(index, task)
		monitor := true
		// Persist allowNonPreferred onto the release itself (not just this
		// one apply run) whenever the toggle was on: the scheduled
		// download-search job's runSearch reads this same field so a
		// release recovered with relaxed matching keeps getting relaxed
		// matching on every future scheduled check too, instead of only
		// this one manual apply ever seeing it.
		var allowNonPreferredFlag *bool
		if allowNonPreferred {
			allowNonPreferredFlag = &allowNonPreferred
		}
		// ignoreLocal is always set (not conditional like allowNonPreferred)
		// whenever a release is marked monitored from here: a Missing
		// Library Files entry means the release's StashApp scene already
		// exists (release.Local/StashSceneID set) but its actual file on
		// disk is what's gone missing - the exact case
		// download.Service.duplicate's normal "already exists in StashApp"
		// skip gets wrong. Without this, both this apply's own immediate
		// SearchAndDownloadNow call below and every future scheduled
		// monitored-search check would silently skip these releases
		// forever, since they show as already linked in StashApp.
		ignoreLocal := true
		if err := s.store.PatchRelease(ctx, release.ID, nil, nil, nil, nil, nil, &monitor, nil, allowNonPreferredFlag, &ignoreLocal); err != nil {
			task.Status = "failed"
			task.Error = "Could not set release monitoring: " + err.Error()
			record(index, task, true)
			continue
		}
		if allowNonPreferred {
			release.AllowNonPreferredFilenames = true
		}
		release.IgnoreLocalForceDownload = true
		result.Monitored++
		if mode != ApplyModeMonitorDownload {
			task.Stage = "complete"
			task.Status = "monitored"
			task.Reason = "Release monitoring enabled"
			record(index, task, false)
			continue
		}
		if s.downloads == nil {
			task.Stage = "download"
			task.Status = "failed"
			task.Error = "Download service is not available"
			record(index, task, true)
			continue
		}
		release.MonitorDownload = true
		result.CurrentItem = "Searching for " + release.VideoID + "…"
		task.Stage = "searching"
		update(index, task)
		outcome, err := s.downloads.SearchAndDownloadDetailed(ctx, release, "Missing Library Recovery", allowNonPreferred)
		task.Provider = outcome.Result.Provider
		task.TorrentTitle = outcome.Result.Title
		task.SourceURL = outcome.Result.SourceURL
		if task.SourceURL == "" {
			task.SourceURL = outcome.Result.Link
		}
		task.Size = outcome.Result.Size
		task.Seeds = outcome.Result.Seeds
		task.Peers = outcome.Result.Peers
		task.DownloadState = outcome.Download.Status
		task.MatchReason = outcome.Download.MatchReason
		task.Progress = outcome.Download.Progress
		task.ETASeconds = outcome.Download.ETASeconds
		task.SeenComplete = outcome.Download.SeenComplete
		if err != nil {
			s.log.Error("Missing Library Files apply: search+download failed", "release", release.VideoID, "error", err)
			task.Stage = "download"
			task.Status = "failed"
			task.Error = outcome.Reason
			if task.Error == "" {
				task.Error = err.Error()
			}
			record(index, task, true)
			continue
		}
		if outcome.Found {
			result.Found++
			if outcome.Download.Status == "failed" {
				task.Stage = "download"
				task.Status = "failed"
				task.Error = outcome.Reason
				record(index, task, true)
				continue
			}
			s.log.Info("Missing Library Files apply: found and downloading", "release", release.VideoID)
			task.Stage = "complete"
			task.Status = "found"
			task.Reason = outcome.Reason
			record(index, task, false)
		} else {
			result.NotFound++
			task.Stage = "searching"
			task.Status = "not_found"
			task.Reason = outcome.Reason
			record(index, task, false)
		}
	}
}

package download

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Net005/JAVBeacon/internal/domain"
)

// releaseUpgradeWindowDays is how many days before and after a release's
// release_date the Release Upgrade Schedule still considers it worth
// re-checking. Outside that window a release is left alone even if its
// current file does not match the #1 preferred-filename pattern.
const releaseUpgradeWindowDays = 30

// defaultReleaseUpgradeTime is used whenever release_upgrade_time is unset
// or fails to parse as HH:MM - an off-peak default so the schedule, once
// enabled, has a sane time to run at without the user having to pick one
// immediately.
const defaultReleaseUpgradeTime = "03:00"

// releaseUpgradeSubtitleSuffixes are the sibling files deleted alongside a
// release's old video file once a better version is downloaded in its
// place. They are named after the OLD file - which usually ends up with a
// different filename than whatever replaces it - so once the video is
// gone these would otherwise linger forever as orphaned, stale subtitles
// for a file that no longer exists.
var releaseUpgradeSubtitleSuffixes = []string{
	".en.srt",
	".en.srt.json",
	".ja.srt",
	".ja.srt.json",
	".subtitles.json",
}

// releaseUpgradeCandidate is one release the Release Upgrade Schedule has
// determined is eligible to re-check: its release_date falls inside the
// configured window, it has at least one completed HTTP download, and that
// download's filename does not match the #1 (highest-priority) configured
// preferred-filename pattern.
type releaseUpgradeCandidate struct {
	release         domain.Release
	currentDownload domain.Download
}

// dueReleaseUpgradeRun reports whether, given the configured daily HH:MM
// time-of-day, the Release Upgrade Schedule should fire at now - once per
// calendar day, at or after the configured time, and never twice on the
// same date. lastRunDate is the "2006-01-02" date of the schedule's last
// run (empty if it has not run yet in this process's lifetime). An unset
// or unparseable timeOfDay falls back to defaultReleaseUpgradeTime.
func dueReleaseUpgradeRun(now time.Time, timeOfDay, lastRunDate string) bool {
	scheduled, err := time.Parse("15:04", strings.TrimSpace(timeOfDay))
	if err != nil {
		scheduled, _ = time.Parse("15:04", defaultReleaseUpgradeTime)
	}
	todayScheduled := time.Date(now.Year(), now.Month(), now.Day(), scheduled.Hour(), scheduled.Minute(), 0, 0, now.Location())
	if now.Before(todayScheduled) {
		return false
	}
	return now.Format("2006-01-02") != lastRunDate
}

// httpFilenameHasTopPriorityMatch reports whether name matches the single
// highest-priority (lowest Priority number) configured preferred-filename
// pattern - i.e. whether it already is the best a release's file can be,
// so the Release Upgrade Schedule has nothing left to improve on it.
func httpFilenameHasTopPriorityMatch(name string, patterns []PreferredFilenamePattern) bool {
	if len(patterns) == 0 {
		return false
	}
	matched, _, priority := matchesAcceptedHTTPPattern(name, patterns)
	return matched && priority == patterns[0].Priority
}

// bestTopPriorityHTTPCandidate returns the single best accepted, non-
// blacklisted HTTP search result that matches the #1 (highest-priority)
// preferred-filename pattern, if any exists - ties broken by the larger
// file, the same precedence sortJavDBDownloadCandidates already uses.
// Anything not matching the very top pattern is not "better" for this
// schedule's purposes, even if it is an otherwise acceptable, lower-
// priority match: the whole point of the Release Upgrade Schedule is
// reaching the #1 pattern, not settling for any accepted match.
func bestTopPriorityHTTPCandidate(results []domain.SearchResult, patterns []PreferredFilenamePattern) (domain.SearchResult, bool) {
	if len(patterns) == 0 {
		return domain.SearchResult{}, false
	}
	top := patterns[0].Priority
	var best domain.SearchResult
	found := false
	for _, result := range results {
		if !result.Accepted || result.BlacklistedFilenameMatch || !result.PreferredFilenameMatch || result.PreferredFilenamePriority != top {
			continue
		}
		if !found || result.SizeBytes > best.SizeBytes {
			best = result
			found = true
		}
	}
	return best, found
}

// latestCompletedHTTPDownload returns the most recently updated completed
// HTTP-transport download row for releaseID, if any - the row whose Name
// is checked against the configured preferred-filename patterns to decide
// whether a release still needs upgrading.
func latestCompletedHTTPDownload(rows []domain.Download, releaseID int64) (domain.Download, bool) {
	var best domain.Download
	found := false
	for _, row := range rows {
		if row.ReleaseID != releaseID || !strings.EqualFold(row.Transport, "http") || row.Status != "completed" {
			continue
		}
		if !found || row.UpdatedAt.After(best.UpdatedAt) {
			best = row
			found = true
		}
	}
	return best, found
}

// eligibleReleaseUpgrades finds every release within the configured
// release-date window that has a known HTTP download history whose
// currently-saved filename does not match the #1 preferred-filename
// pattern - the release set the Release Upgrade Schedule needs to
// re-check. It also returns the configured patterns themselves (already
// priority-sorted) so the caller does not need to re-fetch settings.
func (s *Service) eligibleReleaseUpgrades(ctx context.Context) ([]releaseUpgradeCandidate, []PreferredFilenamePattern, error) {
	settings, err := s.store.Settings(ctx)
	if err != nil {
		return nil, nil, err
	}
	patterns := ParsePreferredFilenamePatterns(settings["accepted_patterns"])
	if len(patterns) == 0 {
		return nil, patterns, nil
	}
	now := time.Now()
	filter := domain.ReleaseFilter{
		MinReleaseDate: now.AddDate(0, 0, -releaseUpgradeWindowDays).Format("2006-01-02"),
		MaxReleaseDate: now.AddDate(0, 0, releaseUpgradeWindowDays).Format("2006-01-02"),
		Limit:          5000,
	}
	releases, err := s.store.Releases(ctx, filter)
	if err != nil {
		return nil, patterns, err
	}
	downloads, err := s.store.Downloads(ctx, "")
	if err != nil {
		return nil, patterns, err
	}
	candidates := make([]releaseUpgradeCandidate, 0, len(releases))
	for _, release := range releases {
		current, ok := latestCompletedHTTPDownload(downloads, release.ID)
		if !ok {
			continue
		}
		if httpFilenameHasTopPriorityMatch(current.Name, patterns) {
			continue
		}
		candidates = append(candidates, releaseUpgradeCandidate{release: release, currentDownload: current})
	}
	return candidates, patterns, nil
}

// deleteReleaseUpgradeFiles best-effort deletes the old video at videoPath
// and its stale subtitle sibling files (releaseUpgradeSubtitleSuffixes),
// logging exactly what it found and removed, or failed to remove, for
// each one individually. A missing file is not an error - the point is
// cleaning up whatever is actually still there before the replacement is
// downloaded, and most releases will not have every subtitle variant.
func (s *Service) deleteReleaseUpgradeFiles(videoPath string, release domain.Release) {
	base := strings.TrimSuffix(videoPath, filepath.Ext(videoPath))
	paths := make([]string, 0, len(releaseUpgradeSubtitleSuffixes)+1)
	paths = append(paths, videoPath)
	for _, suffix := range releaseUpgradeSubtitleSuffixes {
		paths = append(paths, base+suffix)
	}
	for _, path := range paths {
		err := os.Remove(path)
		switch {
		case err == nil:
			if s.log != nil {
				s.log.Info("Release Upgrade Schedule removed stale file", "release_id", release.ID, "video_id", release.VideoID, "path", path)
			}
		case os.IsNotExist(err):
			// Nothing to clean up here - fine.
		default:
			if s.log != nil {
				s.log.Warn("Release Upgrade Schedule could not remove stale file", "release_id", release.ID, "video_id", release.VideoID, "path", path, "error", err)
			}
		}
	}
}

// ReleaseUpgradeStatus returns the current or most recently completed
// Release Upgrade Schedule run's live status.
func (s *Service) ReleaseUpgradeStatus() domain.ReleaseUpgradeJob {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.releaseUpgradeJob
}

func (s *Service) setReleaseUpgradeJob(job domain.ReleaseUpgradeJob) {
	s.mu.Lock()
	s.releaseUpgradeJob = job
	s.mu.Unlock()
}

// StartReleaseUpgradeSchedule starts one Release Upgrade Schedule run in
// the background, following the same "reject if already running, spawn a
// detached goroutine otherwise" convention as StartSearch/StartSearchOlder.
// trigger is recorded in the log only ("scheduled" or "manual") to
// distinguish a daily automatic fire from an operator-requested run.
func (s *Service) StartReleaseUpgradeSchedule(ctx context.Context, trigger string) error {
	s.mu.Lock()
	if s.releaseUpgradeJob.Running {
		s.mu.Unlock()
		return errors.New("release upgrade schedule already running")
	}
	s.releaseUpgradeJob = domain.ReleaseUpgradeJob{Running: true, StartedAt: time.Now().UTC()}
	s.mu.Unlock()
	go s.runReleaseUpgradeSchedule(context.WithoutCancel(ctx), trigger)
	return nil
}

// runReleaseUpgradeSchedule does the actual work for one Release Upgrade
// Schedule pass: build the eligible release set (eligibleReleaseUpgrades),
// then for each one re-run the normal HTTP search and, if the best
// accepted result now matches the #1 preferred-filename pattern, delete
// the old video and its stale subtitle siblings and queue the
// replacement. It always runs in its own goroutine, started by
// StartReleaseUpgradeSchedule.
func (s *Service) runReleaseUpgradeSchedule(ctx context.Context, trigger string) {
	job := domain.ReleaseUpgradeJob{Running: true, StartedAt: time.Now().UTC()}
	s.setReleaseUpgradeJob(job)
	var outcomes []domain.ReleaseUpgradeOutcome
	defer func() {
		job.Running = false
		job.CurrentItem = ""
		job.FinishedAt = time.Now().UTC()
		s.setReleaseUpgradeJob(job)
		if outcomes == nil {
			outcomes = []domain.ReleaseUpgradeOutcome{}
		}
		details, _ := json.Marshal(outcomes)
		run := domain.ReleaseUpgradeRun{StartedAt: job.StartedAt, FinishedAt: job.FinishedAt, Checked: job.Checked, Upgraded: job.Upgraded, Skipped: job.Skipped, Failed: job.Failed, Error: job.LastError, Details: string(details)}
		if _, saveErr := s.store.SaveReleaseUpgradeRun(context.WithoutCancel(ctx), run); saveErr != nil && s.log != nil {
			s.log.Error("could not persist Release Upgrade Schedule run", "error", saveErr)
		}
		if s.log != nil {
			s.log.Info("Release Upgrade Schedule completed", "trigger", trigger, "checked", job.Checked, "upgraded", job.Upgraded, "skipped", job.Skipped, "failed", job.Failed)
		}
	}()

	candidates, patterns, err := s.eligibleReleaseUpgrades(ctx)
	if err != nil {
		job.LastError = err.Error()
		s.setReleaseUpgradeJob(job)
		if s.log != nil {
			s.log.Error("Release Upgrade Schedule could not build eligible release list", "trigger", trigger, "error", err)
		}
		return
	}
	job.Total = len(candidates)
	s.setReleaseUpgradeJob(job)
	if s.log != nil {
		s.log.Info("Release Upgrade Schedule started", "trigger", trigger, "candidates", len(candidates), "patterns_configured", len(patterns))
	}
	if len(patterns) == 0 {
		if s.log != nil {
			s.log.Info("Release Upgrade Schedule skipped run: no preferred filename patterns are configured under Settings → Downloads")
		}
		return
	}

	settings, settingsErr := s.store.Settings(ctx)
	dir := ""
	if settingsErr == nil {
		dir = strings.TrimSpace(settings["http_download_directory"])
	}

	for _, candidate := range candidates {
		release := candidate.release
		job.Checked++
		job.CurrentItem = release.VideoID
		s.setReleaseUpgradeJob(job)

		outcome := domain.ReleaseUpgradeOutcome{ReleaseID: release.ID, VideoID: release.VideoID, OldFilename: candidate.currentDownload.Name}

		results, searchErr := s.searchHTTP(ctx, release, "Release Upgrade Schedule")
		if searchErr != nil {
			job.Failed++
			job.LastError = searchErr.Error()
			outcome.Outcome, outcome.Reason = "failed", searchErr.Error()
			outcomes = append(outcomes, outcome)
			if s.log != nil {
				s.log.Warn("Release Upgrade Schedule search failed", "release_id", release.ID, "video_id", release.VideoID, "error", searchErr)
			}
			continue
		}

		best, found := bestTopPriorityHTTPCandidate(results, patterns)
		if !found {
			job.Skipped++
			outcome.Outcome, outcome.Reason = "skipped", "no better version found"
			outcomes = append(outcomes, outcome)
			if s.log != nil {
				s.log.Info("Release Upgrade Schedule found no better version", "release_id", release.ID, "video_id", release.VideoID)
			}
			continue
		}

		if dir == "" {
			job.Failed++
			job.LastError = "HTTP download folder is not configured under Settings → Downloads → HTTP"
			outcome.Outcome, outcome.Reason = "failed", job.LastError
			outcomes = append(outcomes, outcome)
			if s.log != nil {
				s.log.Warn("Release Upgrade Schedule could not upgrade release: HTTP download folder is not configured", "release_id", release.ID, "video_id", release.VideoID)
			}
			continue
		}

		oldPath := httpDestinationPath(dir, strings.ToUpper(strings.TrimSpace(release.VideoID)))
		s.deleteReleaseUpgradeFiles(oldPath, release)

		best.ReplaceExisting = true
		best.IgnoreLocal = true
		downloaded, downloadErr := s.Download(ctx, release, best, "Release Upgrade Schedule", best.Link)
		if downloadErr != nil || (downloaded.Status != "queued" && downloaded.Status != "downloading") {
			job.Failed++
			outcome.Outcome = "failed"
			if downloadErr != nil {
				outcome.Reason = downloadErr.Error()
			} else {
				outcome.Reason = firstNonEmpty(downloaded.Error, downloaded.MatchReason, downloaded.Status)
			}
			job.LastError = outcome.Reason
			outcomes = append(outcomes, outcome)
			if s.log != nil {
				s.log.Warn("Release Upgrade Schedule replacement download failed to start", "release_id", release.ID, "video_id", release.VideoID, "error", outcome.Reason)
			}
			continue
		}

		job.Upgraded++
		outcome.Outcome = "upgraded"
		outcome.NewFilename = best.Title
		outcomes = append(outcomes, outcome)
		if s.log != nil {
			s.log.Info("Release Upgrade Schedule upgraded release", "release_id", release.ID, "video_id", release.VideoID, "old_filename", outcome.OldFilename, "new_filename", outcome.NewFilename, "download_id", downloaded.ID)
		}
	}
}

// ReleaseUpgradeSchedule runs the daily Release Upgrade Schedule loop for
// the lifetime of ctx, firing once per calendar day at the configured
// release_upgrade_time (server-local HH:MM) whenever
// release_upgrade_enabled is "true" - see dueReleaseUpgradeRun for the
// once-per-day firing rule this loop enforces.
func (s *Service) ReleaseUpgradeSchedule(ctx context.Context) {
	lastRunDate := ""
	for {
		settings, err := s.store.Settings(ctx)
		if err == nil && settings["release_upgrade_enabled"] == "true" && dueReleaseUpgradeRun(time.Now(), settings["release_upgrade_time"], lastRunDate) {
			lastRunDate = time.Now().Format("2006-01-02")
			if startErr := s.StartReleaseUpgradeSchedule(ctx, "scheduled"); startErr != nil && s.log != nil {
				s.log.Warn("Release Upgrade Schedule could not start on schedule", "error", startErr)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(scheduleMaxSleepChunk):
		}
	}
}

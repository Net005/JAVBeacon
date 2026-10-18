package download

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Net005/JAVBeacon/internal/domain"
)

const postStatusBlacklistedRelease = "blacklisted_release"

type releaseSafeguardError struct {
	reason   string
	size     int64
	duration float64
}

func (e *releaseSafeguardError) Error() string { return e.reason }

func (s *Service) payloadSafeguards(ctx context.Context, files []string, videoIDs ...string) error {
	if len(files) > 1 && len(videoIDs) > 0 && videoIDs[0] != "" {
		matched := []string{}
		for _, file := range files {
			if releaseIDMatchesText(filepath.Base(file), videoIDs[0]) {
				matched = append(matched, file)
			}
		}
		if len(matched) == 0 {
			return errors.New("could not identify the requested release video in this multi-video payload; processing blocked")
		}
		files = matched
	}
	settings := map[string]string{}
	if s.store != nil {
		var err error
		settings, err = s.store.Settings(ctx)
		if err != nil {
			return err
		}
	}
	// Judge the main video, not a bundled sample, cover or subtitle. Never
	// use an archive/container total to stand in for the actual video's size.
	var primary string
	var size int64
	for _, file := range files {
		info, err := os.Stat(file)
		if err != nil {
			return err
		}
		if primary == "" || info.Size() > size {
			primary = file
			size = info.Size()
		}
	}
	if primary != "" && size == 0 && safeguardSetting(settings, "download_min_filesize_mb", 200) > 0 {
		return &releaseSafeguardError{reason: "Video filesize is zero bytes and below the configured minimum", size: 0}
	}
	if reason := safeguardReason(settings, size, 0); reason != "" {
		return &releaseSafeguardError{reason: reason, size: size}
	}
	if safeguardSetting(settings, "download_min_duration_minutes", 10) == 0 {
		return nil
	}
	duration, err := probeVideoDuration(ctx, primary)
	if err != nil {
		return err
	}
	if reason := safeguardReason(settings, size, duration); reason != "" {
		return &releaseSafeguardError{reason: reason, size: size, duration: duration}
	}
	return nil
}

func probeVideoDuration(ctx context.Context, path string) (float64, error) {
	probeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(probeCtx, "ffprobe", "-v", "error", "-show_entries", "format=duration", "-of", "json", path)
	output, err := cmd.Output()
	if ctx.Err() != nil {
		return 0, ctx.Err()
	}
	if probeCtx.Err() != nil {
		return 0, probeCtx.Err()
	}
	if err != nil {
		return 0, errors.New("could not probe video duration")
	}
	var result ffprobeResult
	if json.Unmarshal(output, &result) != nil {
		return 0, errors.New("could not read video duration")
	}
	duration, err := strconv.ParseFloat(result.Format.Duration, 64)
	if err != nil || math.IsNaN(duration) || math.IsInf(duration, 0) || duration <= 0 {
		return 0, errors.New("video duration is unavailable; processing blocked")
	}
	return duration, nil
}

func (s *Service) storedBlacklistReason(ctx context.Context, d domain.Download) (string, error) {
	refs := []string{d.SourceReference, d.TransferReference}
	if d.TorrentHash != "" {
		refs = append(refs, "magnet:?xt=urn:btih:"+d.TorrentHash)
	}
	for _, ref := range refs {
		aliases, err := s.store.ReleaseSourceAliases(ctx, ref)
		if err != nil {
			return "", err
		}
		for _, alias := range aliases {
			e, blocked, err := s.store.BlacklistedRelease(ctx, alias)
			if err != nil {
				return "", err
			}
			if blocked {
				return "Blacklisted release: " + e.Reason, nil
			}
		}
	}
	return "", nil
}

func (s *Service) rejectSafeguardedDownload(ctx context.Context, d *domain.Download, reason string, size int64, duration float64) error {
	refs := []string{d.TransferReference, d.SourceReference}
	if d.TorrentHash != "" {
		refs = append(refs, "magnet:?xt=urn:btih:"+d.TorrentHash)
	}
	if len(refs) > 1 {
		if err := s.store.RememberReleaseSourceAliases(ctx, refs[0], refs[1:]); err != nil {
			return err
		}
	}
	for _, ref := range refs {
		if _, err := domain.BlacklistURLKey(ref); err != nil {
			continue
		}
		if err := s.store.SaveBlacklistedRelease(ctx, domain.BlacklistedRelease{URL: ref, ReleaseID: d.ReleaseID, VideoID: d.Query, Title: d.Name, Transport: normalizedDownloadTransport(d.Transport), Reason: reason, SizeBytes: size, DurationSeconds: duration}); err != nil {
			return err
		}
	}
	d.Status = "failed"
	d.PostStatus = postStatusBlacklistedRelease
	d.Error = reason
	d.BytesPerSecond = 0
	d.ETASeconds = 0
	saved, err := s.store.SaveDownload(ctx, *d)
	if err == nil {
		*d = saved
	}
	return err
}

func safeguardSetting(settings map[string]string, key string, fallback float64) float64 {
	value, err := strconv.ParseFloat(strings.TrimSpace(settings[key]), 64)
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
		return fallback
	}
	return value
}

func candidateSize(result domain.SearchResult, videoIDs ...string) int64 {
	if len(videoIDs) > 0 && videoIDs[0] != "" {
		var matched int64
		for _, file := range result.FileDetails {
			if videoExtensions[strings.ToLower(filepath.Ext(file.Name))] && releaseIDMatchesText(file.Name, videoIDs[0]) && file.SizeBytes > matched {
				matched = file.SizeBytes
			}
		}
		if matched > 0 {
			return matched
		}
	}
	var size int64
	for _, file := range result.FileDetails {
		if (file.Matched || file.Name == result.MatchedFile) && file.SizeBytes > size {
			size = file.SizeBytes
		}
	}
	if size > 0 {
		return size
	}
	if result.SizeBytes > 0 {
		return result.SizeBytes
	}
	return parseHumanBytes(result.Size)
}

func safeguardReason(settings map[string]string, size int64, duration float64) string {
	minSize := safeguardSetting(settings, "download_min_filesize_mb", 200)
	minDuration := safeguardSetting(settings, "download_min_duration_minutes", 10)
	if size > 0 && float64(size) < minSize*1_000_000 {
		return fmt.Sprintf("Video filesize %d bytes is below the minimum %.6g MB", size, minSize)
	}
	if duration > 0 && duration < minDuration*60 {
		return fmt.Sprintf("Video duration %.6g seconds is below the minimum %.6g minutes", duration, minDuration)
	}
	return ""
}

func (s *Service) candidateBlockReason(ctx context.Context, r domain.Release, result domain.SearchResult) (string, error) {
	aliasesToRemember := append([]string{}, result.SourceAliases...)
	if normalizedDownloadTransport(result.Transport) == "torrent" && result.SourceURL != "" {
		aliasesToRemember = append(aliasesToRemember, result.SourceURL)
	}
	if err := s.store.RememberReleaseSourceAliases(ctx, result.Link, aliasesToRemember); err != nil {
		return "", err
	}
	aliases, err := s.store.ReleaseSourceAliases(ctx, result.Link)
	if err != nil {
		return "", err
	}
	for _, reference := range aliases {
		entry, blocked, err := s.store.BlacklistedRelease(ctx, reference)
		if err != nil {
			return "", err
		}
		if blocked {
			entry.URL = result.Link
			if err := s.store.SaveBlacklistedRelease(ctx, entry); err != nil {
				return "", err
			}
			return "Blacklisted release: " + entry.Reason, nil
		}
	}
	for _, reference := range []string{result.Link, result.SourceURL} {
		entry, blocked, err := s.store.BlacklistedRelease(ctx, reference)
		if err != nil {
			return "", err
		}
		if blocked {
			return "Blacklisted release: " + entry.Reason, nil
		}
	}
	settings, err := s.store.Settings(ctx)
	if err != nil {
		return "", err
	}
	size := candidateSize(result, r.VideoID)
	reason := safeguardReason(settings, size, result.DurationSeconds)
	if result.BlacklistedFilenameMatch {
		reason = firstNonEmpty(result.Reason, "Filename matched a blacklist pattern")
	}
	if reason == "" {
		return "", nil
	}
	if _, err = domain.BlacklistURLKey(result.Link); err != nil {
		return "", fmt.Errorf("cannot persist rejected download source: %w", err)
	}
	err = s.store.SaveBlacklistedRelease(ctx, domain.BlacklistedRelease{URL: result.Link, ReleaseID: r.ID, VideoID: r.VideoID, Title: result.Title, Transport: normalizedDownloadTransport(result.Transport), Reason: reason, SizeBytes: size, DurationSeconds: result.DurationSeconds})
	if err != nil {
		return "", err
	}
	return reason, nil
}

func (s *Service) guardCandidate(ctx context.Context, r domain.Release, result domain.SearchResult, sourceType string) (domain.Download, bool, error) {
	reason, err := s.candidateBlockReason(ctx, r, result)
	if err != nil {
		return domain.Download{}, true, err
	}
	if reason == "" {
		return domain.Download{}, false, nil
	}
	d, err := s.store.SaveDownload(ctx, domain.Download{ReleaseID: r.ID, Provider: result.Provider, SourceType: sourceType, SourceReference: result.Link, TransferReference: result.Link, SourcePageURL: result.SourceURL, Query: r.VideoID, Name: result.Title, Transport: normalizedDownloadTransport(result.Transport), Status: "failed", PostStatus: postStatusBlacklistedRelease, Error: reason, MatchReason: reason, BytesTotal: candidateSize(result, r.VideoID)})
	return d, true, err
}

func (s *Service) filterSafeguardedResults(ctx context.Context, r domain.Release, rows []domain.SearchResult) error {
	for i := range rows {
		reason, err := s.candidateBlockReason(ctx, r, rows[i])
		if err != nil {
			return err
		}
		if reason != "" {
			rows[i].Accepted = false
			rows[i].BlacklistedReleaseMatch = true
			rows[i].Reason = reason
		}
	}
	return nil
}

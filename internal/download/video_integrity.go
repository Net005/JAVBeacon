package download

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Net005/JAVBeacon/internal/domain"
)

const (
	postStatusVideoRetry  = "re_downloading_failed_video_check"
	postStatusVideoFailed = "failed_video_check"
)

var videoExtensions = map[string]bool{
	".avi": true, ".flv": true, ".m2ts": true, ".m4v": true,
	".mkv": true, ".mov": true, ".mp4": true, ".mpeg": true,
	".mpg": true, ".mts": true, ".ts": true, ".webm": true, ".wmv": true,
}

type ffprobeResult struct {
	Format struct {
		Duration string `json:"duration"`
	} `json:"format"`
	Streams []struct {
		CodecType   string `json:"codec_type"`
		PacketsRead string `json:"nb_read_packets"`
	} `json:"streams"`
}

// ffprobeVideo reads the complete packet table rather than checking only the
// container header. -xerror turns demuxing corruption into a non-zero exit.
func ffprobeVideo(ctx context.Context, path string) error {
	probeCtx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(probeCtx, "ffprobe", "-v", "error", "-xerror", "-count_packets", "-show_entries", "format=duration:stream=codec_type,nb_read_packets", "-of", "json", path)
	output, err := cmd.CombinedOutput()
	if errors.Is(probeCtx.Err(), context.DeadlineExceeded) {
		return errors.New("ffprobe timed out after 30 minutes")
	}
	if err != nil {
		reason := strings.TrimSpace(string(output))
		if reason == "" {
			reason = err.Error()
		}
		return fmt.Errorf("ffprobe rejected the video: %s", reason)
	}
	var result ffprobeResult
	if err := json.Unmarshal(output, &result); err != nil {
		return fmt.Errorf("read ffprobe result: %w", err)
	}
	videoPackets := int64(0)
	hasVideo := false
	packetCountReported := false
	for _, stream := range result.Streams {
		if stream.CodecType != "video" {
			continue
		}
		hasVideo = true
		if packets, err := strconv.ParseInt(stream.PacketsRead, 10, 64); err == nil {
			packetCountReported = true
			videoPackets += packets
		}
	}
	if !hasVideo {
		return errors.New("ffprobe found no video stream")
	}
	if packetCountReported && videoPackets == 0 {
		return errors.New("ffprobe found no readable video packets")
	}
	if !packetCountReported {
		duration, err := strconv.ParseFloat(result.Format.Duration, 64)
		if err != nil || duration <= 0 {
			return errors.New("ffprobe could not confirm readable video packets or a positive duration")
		}
	}
	return nil
}

func videoFiles(path string) ([]string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return []string{path}, nil
	}
	files := []string{}
	err = filepath.WalkDir(path, func(candidate string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.IsDir() && videoExtensions[strings.ToLower(filepath.Ext(entry.Name()))] {
			files = append(files, candidate)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, errors.New("download contains no recognized video files")
	}
	return files, nil
}

func (s *Service) verifyDownloadedVideo(ctx context.Context, path string) error {
	if s.videoProbe != nil {
		return s.videoProbe(ctx, path)
	}
	files, err := videoFiles(path)
	if err != nil {
		return fmt.Errorf("locate downloaded video: %w", err)
	}
	for _, file := range files {
		if err := ffprobeVideo(ctx, file); err != nil {
			return fmt.Errorf("%s: %w", filepath.Base(file), err)
		}
	}
	return nil
}

func markHTTPVideoCheckFailure(d domain.Download, probeErr error) (domain.Download, bool) {
	if d.PostStatus != postStatusVideoRetry {
		d.Status = "downloading"
		d.PostStatus = postStatusVideoRetry
		d.Error = "failed video check: " + probeErr.Error()
		d.Progress = 0
		d.BytesDownloaded = 0
		d.BytesPerSecond = 0
		d.ETASeconds = 0
		return d, true
	}
	d.Status = "failed"
	d.PostStatus = postStatusVideoFailed
	d.Error = "video check failed again after automatic re-download: " + probeErr.Error()
	d.ETASeconds = 0
	d.BytesPerSecond = 0
	return d, false
}

func (s *Service) failTorrentVideoCheck(ctx context.Context, d *domain.Download, reason string) {
	d.Status = "failed"
	d.PostStatus = postStatusVideoFailed
	d.Error = reason
	d.Progress = 1
	d.ETASeconds = 0
	d.BytesPerSecond = 0
	*d, _ = s.store.SaveDownload(ctx, *d)
	s.log.Error("qBittorrent video check failed", "download_id", d.ID, "release_id", d.ReleaseID, "video_id", d.Query, "torrent_hash", d.TorrentHash, "error", reason)
	_, _ = s.store.CreateNotification(ctx, d.ReleaseID, "download_failed", reason)
}

// handleTorrentVideoCheck returns true when normal completion processing must
// stop because the payload is being replaced or has terminally failed.
func (s *Service) handleTorrentVideoCheck(ctx context.Context, qb *QBClient, d *domain.Download, torrent Torrent) bool {
	path := s.mapPath(ctx, torrent.ContentPath)
	if err := s.verifyDownloadedVideo(ctx, path); err == nil {
		s.log.Info("qBittorrent video check passed", "download_id", d.ID, "release_id", d.ReleaseID, "video_id", d.Query, "torrent_hash", torrent.Hash, "path", path)
		d.MatchReason = appendDownloadPreference("ffprobe video check passed", d.MatchReason)
		return false
	} else if d.PostStatus == postStatusVideoRetry {
		s.failTorrentVideoCheck(ctx, d, "video check failed again after automatic re-download: "+err.Error())
		return true
	} else {
		firstFailure := "failed video check: " + err.Error()
		d.Status = "downloading"
		d.PostStatus = postStatusVideoRetry
		d.Error = firstFailure
		*d, _ = s.store.SaveDownload(ctx, *d)
		s.log.Warn("qBittorrent video check failed; automatically re-downloading once", "download_id", d.ID, "release_id", d.ReleaseID, "video_id", d.Query, "torrent_hash", torrent.Hash, "error", err)
		if strings.TrimSpace(d.TransferReference) == "" {
			s.failTorrentVideoCheck(ctx, d, firstFailure+"; automatic re-download unavailable because this older download has no retained direct torrent reference")
			return true
		}
		if removeErr := qb.DeleteFiles(ctx, torrent.Hash); removeErr != nil {
			s.failTorrentVideoCheck(ctx, d, firstFailure+"; remove corrupt torrent files before re-download: "+removeErr.Error())
			return true
		}
		settings, settingsErr := s.store.Settings(ctx)
		if settingsErr != nil {
			s.failTorrentVideoCheck(ctx, d, firstFailure+"; load settings for re-download: "+settingsErr.Error())
			return true
		}
		response, addErr := qb.Add(ctx, d.TransferReference, settings["qb_category"])
		d.QBResponse = response
		if addErr != nil {
			s.failTorrentVideoCheck(ctx, d, firstFailure+"; re-submit torrent: "+addErr.Error())
			return true
		}
		newHash, appeared := s.verifyAddedToQBittorrent(ctx, qb, d.TransferReference, d.Query)
		if !appeared {
			s.failTorrentVideoCheck(ctx, d, firstFailure+"; qBittorrent accepted the retry but the torrent never appeared")
			return true
		}
		d.TorrentHash = newHash
		d.Progress = 0
		d.Seeds = 0
		d.Peers = 0
		d.ETASeconds = 0
		d.SeedRatio = 0
		d.Status = "downloading"
		*d, _ = s.store.SaveDownload(ctx, *d)
		s.log.Info("corrupt qBittorrent video removed and automatic re-download started", "download_id", d.ID, "release_id", d.ReleaseID, "video_id", d.Query, "torrent_hash", newHash, "failed_video_check", firstFailure)
		_, _ = s.store.CreateNotification(ctx, d.ReleaseID, "download_started", "Video check failed; automatically re-downloading once")
		return true
	}
}

package stash

import (
	"context"
	"errors"
	"sort"

	"github.com/Net005/JAVBeacon/internal/domain"
)

const watchlistReportListLimit = 50

// WatchlistDifference names one release involved in a Watchlist mismatch.
type WatchlistDifference struct {
	ReleaseID    int64  `json:"release_id"`
	VideoID      string `json:"video_id"`
	Title        string `json:"title"`
	StashSceneID string `json:"stash_scene_id"`
}

// WatchlistReport compares JAVBeacon's Watchlist with the configured StashApp
// Watchlist tag. It only reads: no release, tag or collection is changed.
type WatchlistReport struct {
	Consistent          bool                  `json:"consistent"`
	JAVBeaconWatchlist  int                   `json:"javbeacon_watchlist"`
	StashTagged         int                   `json:"stash_tagged_scenes"`
	InAgreement         int                   `json:"in_agreement"`
	PendingScene        int                   `json:"pending_scene"`
	MissingInStashCount int                   `json:"missing_in_stash_count"`
	MissingInStash      []WatchlistDifference `json:"missing_in_stash"`
	MissingInJAVBeacon  []WatchlistDifference `json:"missing_in_javbeacon"`
	MissingInJAVBCount  int                   `json:"missing_in_javbeacon_count"`
	UnlinkedStashScenes int                   `json:"unlinked_stash_scenes"`
	Truncated           bool                  `json:"truncated"`
}

func (s *Service) WatchlistReport(ctx context.Context) (WatchlistReport, error) {
	var out WatchlistReport
	tagged, configured, err := s.SiloWatchlistScenes(ctx)
	if err != nil {
		return out, err
	}
	if !configured {
		return out, errors.New("StashApp Base URL and Watchlist tag ID are required")
	}
	out.StashTagged = len(tagged)
	out.MissingInStash, out.MissingInJAVBeacon = []WatchlistDifference{}, []WatchlistDifference{}
	seen := map[string]bool{}
	for offset := 0; ; offset += 500 {
		rows, err := s.store.Releases(ctx, domain.ReleaseFilter{Watchlist: true, Limit: 500, Offset: offset})
		if err != nil {
			return out, err
		}
		for _, r := range rows {
			out.JAVBeaconWatchlist++
			if !r.Local || r.StashSceneID == "" {
				out.PendingScene++
				continue
			}
			seen[r.StashSceneID] = true
			if _, ok := tagged[r.StashSceneID]; ok {
				out.InAgreement++
				continue
			}
			out.MissingInStashCount++
			if len(out.MissingInStash) < watchlistReportListLimit {
				out.MissingInStash = append(out.MissingInStash, WatchlistDifference{ReleaseID: r.ID, VideoID: r.VideoID, Title: r.Title, StashSceneID: r.StashSceneID})
			}
		}
		if len(rows) < 500 {
			break
		}
	}
	scenes := make([]string, 0, len(tagged))
	for id := range tagged {
		if !seen[id] {
			scenes = append(scenes, id)
		}
	}
	sort.Strings(scenes)
	for _, id := range scenes {
		rows, err := s.store.Releases(ctx, domain.ReleaseFilter{StashSceneID: id, Limit: 1})
		if err != nil {
			return out, err
		}
		if len(rows) == 0 {
			out.UnlinkedStashScenes++
			continue
		}
		out.MissingInJAVBCount++
		if len(out.MissingInJAVBeacon) < watchlistReportListLimit {
			r := rows[0]
			out.MissingInJAVBeacon = append(out.MissingInJAVBeacon, WatchlistDifference{ReleaseID: r.ID, VideoID: r.VideoID, Title: r.Title, StashSceneID: id})
		}
	}
	out.Truncated = out.MissingInStashCount > len(out.MissingInStash) || out.MissingInJAVBCount > len(out.MissingInJAVBeacon)
	out.Consistent = out.MissingInStashCount == 0 && out.MissingInJAVBCount == 0 && out.UnlinkedStashScenes == 0 && out.PendingScene == 0
	return out, nil
}

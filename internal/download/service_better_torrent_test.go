package download

import (
	"github.com/Net005/JAVBeacon/internal/domain"
	"testing"
)

func TestBetterTorrentCandidate(t *testing.T) {
	http := domain.SearchResult{Accepted: true, MatchedFile: "ABC-123 1080p.mp4", PreferredFilenameMatch: true, PreferredFilenamePriority: 2}
	for _, tc := range []struct {
		name    string
		torrent domain.SearchResult
		want    bool
	}{
		{"equal keeps HTTP", domain.SearchResult{Accepted: true, MatchedFile: "ABC-123 1080p.mp4", Seeds: 10}, false},
		{"4K meets threshold", domain.SearchResult{Accepted: true, MatchedFile: "ABC-123 4K.mp4", Seeds: 3}, true},
		{"4K too few seeds", domain.SearchResult{Accepted: true, MatchedFile: "ABC-123 4K.mp4", Seeds: 2}, false},
		{"earlier filename pattern", domain.SearchResult{Accepted: true, MatchedFile: "ABC-123.mp4", Seeds: 3, PreferredFilenameMatch: true, PreferredFilenamePriority: 1}, true},
		{"later pattern", domain.SearchResult{Accepted: true, MatchedFile: "ABC-123.mp4", Seeds: 3, PreferredFilenameMatch: true, PreferredFilenamePriority: 3}, false},
		{"blacklisted", domain.SearchResult{Accepted: true, BlacklistedFilenameMatch: true, MatchedFile: "ABC-123 4K.mp4", Seeds: 100}, false},
		{"unaccepted", domain.SearchResult{MatchedFile: "ABC-123 4K.mp4", Seeds: 100}, false},
		{"matched file overrides pack title", domain.SearchResult{Accepted: true, Title: "4K pack", MatchedFile: "ABC-123 720p.mp4", Seeds: 100}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, got := betterTorrentCandidate([]domain.SearchResult{tc.torrent}, http, 3)
			if got != tc.want {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
	low := domain.SearchResult{Accepted: true, MatchedFile: "ABC-123 4K.mp4", Seeds: 1, PreferredFilenameMatch: true, PreferredFilenamePriority: 1}
	eligible := domain.SearchResult{Accepted: true, MatchedFile: "ABC-123 2160p.mp4", Seeds: 4}
	if got, ok := betterTorrentCandidate([]domain.SearchResult{low, eligible}, http, 3); !ok || got.Seeds != 4 {
		t.Fatal("must consider eligible candidates beyond highest filename preference")
	}
	if normalizeDownloadMethod("http_better_torrent") != downloadHTTPBetterTorrent {
		t.Fatal("new mode not recognized")
	}
	if effectiveDownloadMethod(map[string]string{"default_download_method": "http_better_torrent"}, domain.Release{DownloadMethodOverride: "http"}) != downloadHTTPOnly {
		t.Fatal("release override lost")
	}
}

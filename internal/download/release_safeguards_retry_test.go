package download

import (
	"context"
	"testing"

	"github.com/Net005/JAVBeacon/internal/domain"
)

func TestSafeguardBlacklistBlocksRetryAndPipeline(t *testing.T) {
	s, st, r := safeguardTestService(t)
	ctx := context.Background()
	const link = "https://keepshare.org/s/synthetic"
	if err := st.SaveBlacklistedRelease(ctx, domain.BlacklistedRelease{URL: link, Reason: "Manual exclusion"}); err != nil {
		t.Fatal(err)
	}
	d, err := st.SaveDownload(ctx, domain.Download{ReleaseID: r.ID, Query: r.VideoID, Name: r.VideoID, Transport: "http", Status: "failed", SourceReference: link})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.RetryHTTPDownload(ctx, d.ID); err == nil {
		t.Error("blacklisted source was requeued by retry")
	}
	if err = s.runPipelineEvent(ctx, &d, Torrent{}, "download_completed"); err == nil {
		t.Error("blacklisted source allowed into pipeline")
	}
}

func TestSafeguardThresholdBoundariesAndUnknownMetadata(t *testing.T) {
	for _, tc := range []struct {
		name     string
		settings map[string]string
		size     int64
		duration float64
		blocked  bool
	}{
		{"both exact minimum", nil, 200_000_000, 600, false},
		{"size below", nil, 199_999_999, 600, true},
		{"duration below", nil, 200_000_000, 599.999, true},
		{"unknown deferred to payload", nil, 0, 0, false},
		{"custom duration", map[string]string{"download_min_duration_minutes": "12"}, 200_000_000, 600, true},
		{"custom size", map[string]string{"download_min_filesize_mb": "250"}, 200_000_000, 600, true},
		{"disabled", map[string]string{"download_min_filesize_mb": "0", "download_min_duration_minutes": "0"}, 1, 1, false},
		{"invalid default", map[string]string{"download_min_filesize_mb": "NaN", "download_min_duration_minutes": "-1"}, 199_999_999, 600, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := safeguardReason(tc.settings, tc.size, tc.duration) != ""; got != tc.blocked {
				t.Fatalf("blocked=%v, expected %v", got, tc.blocked)
			}
		})
	}
}

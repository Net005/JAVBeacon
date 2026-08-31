package monitor

import "testing"

func TestShouldAutoMonitorFutureRelease(t *testing.T) {
	tests := []struct {
		name                      string
		ready, newForSite         bool
		baselineDate, releaseDate string
		want                      bool
	}{
		{"newer release", true, true, "2026-08-30", "2026-08-31", true},
		{"same date", true, true, "2026-08-30", "2026-08-30", true},
		{"older release", true, true, "2026-08-30", "2026-08-29", false},
		{"first scrape has no baseline", false, true, "", "2026-08-31", false},
		{"known association is one-time", true, false, "2026-08-30", "2026-08-31", false},
		{"missing release date", true, true, "2026-08-30", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldAutoMonitorFutureRelease(tt.ready, tt.newForSite, tt.baselineDate, tt.releaseDate); got != tt.want {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}

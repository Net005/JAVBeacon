package download

import "testing"

// TestCleanupRetryThrottlesThenAllowsAnotherAttempt covers the fix for a
// download that gets permanently stuck once a single removal attempt fails
// (e.g. a transient qBittorrent error): cleanupDue should refuse an
// immediate second attempt, but must allow one again once the retry
// interval has elapsed, and clearCleanupRetry should reset that state
// entirely once a removal finally succeeds.
func TestCleanupRetryThrottlesThenAllowsAnotherAttempt(t *testing.T) {
	s := &Service{}
	const downloadID = int64(42)

	if !s.cleanupDue(downloadID) {
		t.Fatal("expected the first cleanup attempt to be allowed")
	}
	if s.cleanupDue(downloadID) {
		t.Fatal("expected an immediate second attempt to be throttled")
	}

	// Simulate the retry interval having already elapsed.
	s.cleanupRetryMu.Lock()
	s.cleanupRetryAt[downloadID] = s.cleanupRetryAt[downloadID].Add(-cleanupRetryInterval - 1)
	s.cleanupRetryMu.Unlock()
	if !s.cleanupDue(downloadID) {
		t.Fatal("expected a retry to be allowed once the backoff interval elapsed")
	}

	s.clearCleanupRetry(downloadID)
	if !s.cleanupDue(downloadID) {
		t.Fatal("expected cleanupDue to allow an immediate attempt right after clearCleanupRetry")
	}
}

func TestCompletedTorrentCleanupRules(t *testing.T) {
	tests := []struct {
		name    string
		rule    string
		ratio   float64
		minimum float64
		ready   bool
	}{
		{name: "keep never removes", rule: completedTorrentKeep, ratio: 9, minimum: 1, ready: false},
		{name: "completion removes immediately", rule: completedTorrentRemove, ratio: 0, minimum: 1, ready: true},
		{name: "ratio waits", rule: completedTorrentRemoveAtRatio, ratio: .99, minimum: 1, ready: false},
		{name: "ratio boundary removes", rule: completedTorrentRemoveAtRatio, ratio: 1, minimum: 1, ready: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := completedTorrentReady(test.rule, test.ratio, test.minimum); got != test.ready {
				t.Fatalf("completedTorrentReady(%q, %v, %v)=%v, want %v", test.rule, test.ratio, test.minimum, got, test.ready)
			}
		})
	}
}

func TestCompletedTorrentCleanupRuleDefaultsToLegacyRatioBehavior(t *testing.T) {
	for _, raw := range []string{"", "unknown"} {
		if got := completedTorrentRule(raw); got != completedTorrentRemoveAtRatio {
			t.Fatalf("completedTorrentRule(%q)=%q, want %q", raw, got, completedTorrentRemoveAtRatio)
		}
	}
}

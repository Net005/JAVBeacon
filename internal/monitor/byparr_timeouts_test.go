package monitor

import "testing"

func TestByparrTimeoutsFromSettingsParsesConfiguredValues(t *testing.T) {
	settings := map[string]string{"byparr_request_timeout_seconds": "45", "byparr_solve_timeout_seconds": "90"}
	requestTimeout, solveSeconds := byparrTimeoutsFromSettings(settings)
	if requestTimeout.Seconds() != 45 {
		t.Fatalf("requestTimeout=%v, want 45s", requestTimeout)
	}
	if solveSeconds != 90 {
		t.Fatalf("solveSeconds=%d, want 90", solveSeconds)
	}
}

// TestByparrTimeoutsFromSettingsZeroValueOnMissingOrInvalid guards the
// contract ConfigureTimeouts relies on: a missing/blank/unparsable setting
// must come back as the zero value here, not some substitute default,
// because ConfigureTimeouts is what applies the actual fallback default -
// duplicating that logic here would risk the two drifting apart.
func TestByparrTimeoutsFromSettingsZeroValueOnMissingOrInvalid(t *testing.T) {
	cases := []map[string]string{
		{},
		{"byparr_request_timeout_seconds": "", "byparr_solve_timeout_seconds": ""},
		{"byparr_request_timeout_seconds": "not-a-number", "byparr_solve_timeout_seconds": "also-not-a-number"},
	}
	for _, settings := range cases {
		requestTimeout, solveSeconds := byparrTimeoutsFromSettings(settings)
		if requestTimeout != 0 || solveSeconds != 0 {
			t.Fatalf("byparrTimeoutsFromSettings(%v) = (%v, %d), want (0, 0)", settings, requestTimeout, solveSeconds)
		}
	}
}

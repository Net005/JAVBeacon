package domain

import (
	"testing"
	"time"
)

func TestParseScheduleDuration(t *testing.T) {
	cases := []struct {
		in      string
		want    time.Duration
		wantErr bool
	}{
		{in: "1h", want: time.Hour},
		{in: "30m", want: 30 * time.Minute},
		{in: "24h", want: 24 * time.Hour},
		{in: "7d", want: 7 * 24 * time.Hour},
		{in: "1d", want: 24 * time.Hour},
		{in: "2w", want: 2 * 7 * 24 * time.Hour},
		{in: "1w3d", want: (7 + 3) * 24 * time.Hour},
		{in: "1w12h", want: 7*24*time.Hour + 12*time.Hour},
		{in: "0.5d", want: 12 * time.Hour},
		{in: " 7d ", want: 7 * 24 * time.Hour},
		{in: "7D", want: 7 * 24 * time.Hour},
		{in: "", wantErr: true},
		{in: "not a duration", wantErr: true},
		{in: "dog", wantErr: true},
	}
	for _, tc := range cases {
		got, err := ParseScheduleDuration(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("ParseScheduleDuration(%q) = %v, want error", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseScheduleDuration(%q) unexpected error: %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("ParseScheduleDuration(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

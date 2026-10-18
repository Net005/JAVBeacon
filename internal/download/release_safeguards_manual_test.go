package download

import (
	"context"
	"net/http"
	"testing"
)

func TestManualBlacklistResolvesKeepshareAndPikPak(t *testing.T) {
	s, st, _ := safeguardTestService(t)
	ctx := context.Background()
	s.client = &http.Client{Transport: pikPakRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		response := pikPakJSONResponse(http.StatusFound, "")
		response.Request = r
		response.Header.Set("Location", "https://mypikpak.com/s/SyntheticShare?act=play")
		return response, nil
	})}
	warning, err := s.BlacklistSource(ctx, "https://keepshare.org/s/synthetic", "Manual exclusion")
	if err != nil || warning != "" {
		t.Fatalf("blacklist: %q %v", warning, err)
	}
	for _, raw := range []string{"https://keepshare.org/s/synthetic", "https://mypikpak.com/s/SyntheticShare?act=play"} {
		if _, blocked, err := st.BlacklistedRelease(ctx, raw); err != nil || !blocked {
			t.Fatalf("alias unblocked: %s %v", raw, err)
		}
	}
	if err = st.DeleteBlacklistedRelease(ctx, "https://keepshare.org/s/synthetic"); err != nil {
		t.Fatal(err)
	}
	_, total, err := st.BlacklistedReleases(ctx, "", 25, 0)
	if err != nil || total != 0 {
		t.Fatalf("linked aliases not removed: %d %v", total, err)
	}
}

package scraper

import (
	"context"
	"errors"
	"testing"
	"time"

	"golang.org/x/net/html"
)

// withFastRetryBackoff shrinks the retry backoff for the duration of a test
// so exercising withScrapeRetry does not cost real wall-clock seconds.
func withFastRetryBackoff(t *testing.T) {
	t.Helper()
	origFirst, origSecond := RetryFirstBackoff, RetrySecondBackoff
	RetryFirstBackoff, RetrySecondBackoff = time.Millisecond, 2*time.Millisecond
	t.Cleanup(func() { RetryFirstBackoff, RetrySecondBackoff = origFirst, origSecond })
}

func TestShouldRetryScrapeClassification(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil error", nil, false},
		{"blocked is retried", &StatusError{Status: ScrapeBlocked, Message: "cloudflare"}, true},
		{"generic scrape error is retried", &StatusError{Status: ScrapeError, Message: "boom"}, true},
		{"invalid page is not retried", &StatusError{Status: ScrapeInvalid, Message: "wrong page"}, false},
		{"explicitly retryable invalid page is retried", &StatusError{Status: ScrapeInvalid, Message: "empty listing", Retryable: true}, true},
		{"plain transport error is retried", errors.New("connection reset"), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := shouldRetryScrape(ctx, c.err); got != c.want {
				t.Fatalf("shouldRetryScrape(%v) = %v, want %v", c.err, got, c.want)
			}
		})
	}
}

func TestShouldRetryScrapeRespectsCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if shouldRetryScrape(ctx, &StatusError{Status: ScrapeBlocked, Message: "cloudflare"}) {
		t.Fatal("expected no retry once the context is already done")
	}
}

func TestWithScrapeRetrySucceedsAfterTransientBlocks(t *testing.T) {
	withFastRetryBackoff(t)
	attempts := 0
	var retriesLogged []int
	doc, err := withScrapeRetry(context.Background(), func() (*html.Node, error) {
		attempts++
		if attempts < 3 {
			return nil, &StatusError{Status: ScrapeBlocked, Message: "cloudflare"}
		}
		return &html.Node{}, nil
	}, func(attempt int, wait time.Duration, err error) {
		retriesLogged = append(retriesLogged, attempt)
	})
	if err != nil {
		t.Fatalf("expected eventual success, got error: %v", err)
	}
	if doc == nil {
		t.Fatal("expected a document to be returned")
	}
	if attempts != 3 {
		t.Fatalf("expected 3 attempts (1 initial + 2 retries), got %d", attempts)
	}
	if len(retriesLogged) != 2 || retriesLogged[0] != 1 || retriesLogged[1] != 2 {
		t.Fatalf("expected retry callbacks for attempts [1 2], got %v", retriesLogged)
	}
}

func TestWithScrapeRetryGivesUpAfterAtLeastTwoRetries(t *testing.T) {
	withFastRetryBackoff(t)
	attempts := 0
	_, err := withScrapeRetry(context.Background(), func() (*html.Node, error) {
		attempts++
		return nil, &StatusError{Status: ScrapeError, Message: "boom"}
	}, nil)
	if err == nil {
		t.Fatal("expected the final error to be returned")
	}
	if attempts < 3 {
		t.Fatalf("expected at least 2 retries (3 total attempts), got %d attempts", attempts)
	}
}

func TestWithScrapeRetryDoesNotRetryInvalidPage(t *testing.T) {
	withFastRetryBackoff(t)
	attempts := 0
	_, err := withScrapeRetry(context.Background(), func() (*html.Node, error) {
		attempts++
		return nil, &StatusError{Status: ScrapeInvalid, Message: "wrong page"}
	}, func(int, time.Duration, error) { t.Fatal("should not retry a structurally invalid page") })
	if err == nil {
		t.Fatal("expected the invalid-page error to be returned")
	}
	if attempts != 1 {
		t.Fatalf("expected exactly 1 attempt for an invalid page, got %d", attempts)
	}
}

func TestWithScrapeRetryRetriesTransientInvalidListing(t *testing.T) {
	withFastRetryBackoff(t)
	attempts := 0
	doc, err := withScrapeRetry(context.Background(), func() (*html.Node, error) {
		attempts++
		if attempts < 3 {
			return nil, retryableStatusErrorf(ScrapeInvalid, "JavLibrary listing contained no entries")
		}
		return &html.Node{}, nil
	}, nil)
	if err != nil || doc == nil {
		t.Fatalf("expected retryable invalid listing to recover, doc=%v err=%v", doc, err)
	}
	if attempts != 3 {
		t.Fatalf("attempts=%d, want initial request plus two retries", attempts)
	}
}

package download

import (
	"context"
	"errors"
	"github.com/Net005/JAVBeacon/internal/domain"
	"net/http"
	"testing"
)

func TestJavDBKnownReleaseSourceMismatchSkipsInspection(t *testing.T) {
	p, close := javDBFixtureProvider(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/search" {
			w.Write([]byte(javDBSearchPage("SYNTH-101", "2024-01-01", "/v/exact")))
			return
		}
		w.Write([]byte(`<html><body>ID: SYNTH-101<section class="new-download-layout"><a href="https://keepshare.org/share">Get file</a></section></body></html>`))
	})
	defer close()
	inspected, saved := 0, 0
	blocked := ""
	p.sourceRejectionCheck = func(context.Context, string) (string, error) { return blocked, nil }
	p.rememberSourceRejection = func(_ context.Context, ref string, aliases []string, reason string) error {
		saved++
		blocked = reason
		return nil
	}
	p.inspectCandidate = func(context.Context, string, string) (pikPakFile, []pikPakFile, error) {
		inspected++
		return pikPakFile{}, nil, &noMatchingShareFileError{releaseID: "SYNTH-101", aliases: []string{"https://mypikpak.com/s/EXAMPLE"}}
	}
	for i := 0; i < 3; i++ {
		rows, err := p.Search(context.Background(), domain.Release{VideoID: "SYNTH-101", ReleaseDate: "2024-01-01"})
		if err != nil || len(rows) != 1 || rows[0].Accepted {
			t.Fatalf("rows=%+v err=%v", rows, err)
		}
	}
	if inspected != 1 || saved != 1 {
		t.Fatalf("inspection loop inspected=%d saved=%d", inspected, saved)
	}
}
func TestJavDBTransientInspectionIsNotRemembered(t *testing.T) {
	p, close := javDBFixtureProvider(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/search" {
			w.Write([]byte(javDBSearchPage("SYNTH-101", "2024-01-01", "/v/exact")))
			return
		}
		w.Write([]byte(`<html><body>ID: SYNTH-101<section class="new-download-layout"><a href="https://keepshare.org/share">Get file</a></section></body></html>`))
	})
	defer close()
	p.rememberSourceRejection = func(context.Context, string, []string, string) error {
		t.Error("transient failure persisted")
		return nil
	}
	inspected := 0
	p.inspectCandidate = func(context.Context, string, string) (pikPakFile, []pikPakFile, error) {
		inspected++
		return pikPakFile{}, nil, errors.New("HTTP 429")
	}
	for i := 0; i < 2; i++ {
		_, err := p.Search(context.Background(), domain.Release{VideoID: "SYNTH-101", ReleaseDate: "2024-01-01"})
		if err != nil {
			t.Fatal(err)
		}
	}
	if inspected != 2 {
		t.Fatalf("transient failure not retryable: %d", inspected)
	}
}

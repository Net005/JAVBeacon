package scraper

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Net005/JAVBeacon/internal/domain"
	"golang.org/x/net/html"
)

func TestJavLibraryListingAndDetail(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/list", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<html><title>Videos starring Test</title><div class="video"><a href="/javabc123.html" title="Listing title"><img src="/abc00123ps.jpg"></a><div class="id">ABC-123</div></div></html>`))
	})
	mux.HandleFunc("/javabc123.html", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<html><title>Detailed title - JAVLibrary</title><img id="video_jacket_img" src="/abc00123pl.jpg"><table><tr><td class="header">Release Date:</td><td class="text">2024-02-03</td></tr><tr><td class="header">Length:</td><td>90 min(s)</td></tr><tr><td class="header">Maker:</td><td><a>Attackers</a></td></tr><tr><td class="header">Label:</td><td><a>Otona No Drama</a></td></tr></table><div class="director"><a>Director Name</a></div><div id="video_genres"><span class="genre"><a>Drama</a></span><span class="genre"><a>Best, Omnibus</a></span></div><div id="video_cast"><span class="cast"><span class="star"><a>Actor One</a></span></span><span class="cast"><span class="star"><a>Actor Two</a></span><span class="alias">Alias Two</span></span><span class="cast"><span class="star"><a>Actor Three</a></span> (Stage Three)</span></div></html>`))
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	items, err := NewJavLibrary(2*time.Second, "", 0, nil).Scrape(context.Background(), server.URL+"/list", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("items=%d", len(items))
	}
	x := items[0]
	if x.VideoID != "ABC-123" || x.Title != "Detailed title" || x.ReleaseDate != "2024-02-03" || x.Actress != "Actor One, Actor Two, Alias Two, Actor Three, Stage Three" || len(x.Genres) != 2 || x.Genres[1] != "Best, Omnibus" || x.Director != "Director Name" || x.Studio != "Attackers" || x.Label != "Otona No Drama" || !x.Released {
		t.Fatalf("unexpected release: %+v", x)
	}
}

// TestJavLibraryRejectsCloudflareChallengeFromFlareSolverr guards against the
// exact bug Phase 2 fixed: a FlareSolverr solve can itself still return the
// Cloudflare interstitial (a failed or timed-out solve), and that response
// must be validated the same way a direct fetch is rather than mined as if it
// were the real listing page.
func TestJavLibraryRejectsCloudflareChallengeFromFlareSolverr(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/list", func(w http.ResponseWriter, r *http.Request) {
		// Direct fetch fails so the code falls back to FlareSolverr.
		w.WriteHeader(http.StatusForbidden)
	})
	mux.HandleFunc("/flaresolverr", func(w http.ResponseWriter, r *http.Request) {
		challenge := `<html><head><title>Just a moment...</title></head><body><div id="challenge-form"></div></body></html>`
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":   "ok",
			"solution": map[string]any{"response": challenge},
		})
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	items, err := NewJavLibrary(2*time.Second, server.URL+"/flaresolverr", 0, nil).Scrape(context.Background(), server.URL+"/list", 1)
	if err == nil {
		t.Fatalf("expected an error rejecting the Cloudflare challenge page, got items=%+v", items)
	}
	if len(items) != 0 {
		t.Fatalf("expected no releases scraped from a challenge page, got %+v", items)
	}
}

func TestJavLibraryRetriesTransientInvalidListingResponse(t *testing.T) {
	withFastRetryBackoff(t)
	attempts := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/list", func(w http.ResponseWriter, _ *http.Request) {
		attempts++
		if attempts < 3 {
			_, _ = w.Write([]byte(`<html><body><p>Temporary incomplete response</p></body></html>`))
			return
		}
		_, _ = w.Write([]byte(`<html><div class="video"><a href="/javabc123.html" title="Recovered listing"><img src="/abc00123ps.jpg"></a><div class="id">ABC-123</div></div></html>`))
	})
	mux.HandleFunc("/javabc123.html", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<html><title>Recovered detail - JAVLibrary</title><img id="video_jacket_img" src="/abc00123pl.jpg"></html>`))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	items, err := NewJavLibrary(2*time.Second, "", 0, nil).Scrape(context.Background(), server.URL+"/list", 1)
	if err != nil {
		t.Fatal(err)
	}
	if attempts != 3 {
		t.Fatalf("listing attempts=%d, want initial request plus two retries", attempts)
	}
	if len(items) != 1 || items[0].VideoID != "ABC-123" {
		t.Fatalf("items=%+v, want recovered ABC-123 listing", items)
	}
}

// TestScrapeFilteredRetriesTransientDetailFetchFailureOnce covers
// scrapeFiltered's own extra, item-level retry: j.detail already retries
// internally (document -> withScrapeRetry, one initial attempt plus
// scrapeRetryAttempts=2 more), so this fails the detail endpoint on all
// three of those internal attempts and only succeeds on the fourth call,
// which only scrapeFiltered's own separate retry (after RetrySecondBackoff)
// can reach - proving the two retry layers are distinct and the item-level
// one actually recovers a candidate that would otherwise have been
// reported via OnDetailFailure/job.Error.
func TestScrapeFilteredRetriesTransientDetailFetchFailureOnce(t *testing.T) {
	withFastRetryBackoff(t)
	var detailCalls int32
	mux := http.NewServeMux()
	mux.HandleFunc("/list", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<html><div class="video"><a href="/javabc123.html" title="Listing title"><img src="/abc00123ps.jpg"></a><div class="id">ABC-123</div></div></html>`))
	})
	mux.HandleFunc("/javabc123.html", func(w http.ResponseWriter, _ *http.Request) {
		if atomic.AddInt32(&detailCalls, 1) <= 3 {
			http.Error(w, "solver unavailable", http.StatusBadGateway)
			return
		}
		_, _ = w.Write([]byte(`<html><title>Recovered detail - JAVLibrary</title><img id="video_jacket_img" src="/abc00123pl.jpg"><table><tr><td class="header">Label:</td><td><a>Otona No Drama</a></td></tr></table></html>`))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	var failed []string
	concurrency := ScrapeConcurrency{OnDetailFailure: func(videoID string, _ error) { failed = append(failed, videoID) }}
	items, err := NewJavLibrary(2*time.Second, "", 0, nil).ScrapeFiltered(context.Background(), server.URL+"/list", 1, nil, concurrency)
	if err != nil {
		t.Fatal(err)
	}
	if got := atomic.LoadInt32(&detailCalls); got != 4 {
		t.Fatalf("detail endpoint called %d times, want exactly 4 (document()'s 3 internal attempts + scrapeFiltered's 1 extra item-level retry)", got)
	}
	if len(failed) != 0 {
		t.Fatalf("OnDetailFailure fired for %v, want the item-level retry to have recovered the release before giving up", failed)
	}
	if len(items) != 1 || items[0].Label != "Otona No Drama" {
		t.Fatalf("items=%+v, want the recovered detail page's Label merged in", items)
	}
}

// TestNormalizeJavLibraryURL covers the https-enforcement fix: any
// javlibrary.com/www.javlibrary.com URL, whatever scheme or host variant it
// arrived with, must be rewritten to https://www.javlibrary.com so scraping
// never hits the plain-http host (which reliably 403s direct requests and can
// trip FlareSolverr into a mid-navigation redirect race). A non-JavLibrary
// URL must pass through unchanged.
func TestNormalizeJavLibraryURL(t *testing.T) {
	cases := map[string]string{
		"http://www.javlibrary.com/en/vl_maker.php?m=aqkq": "https://www.javlibrary.com/en/vl_maker.php?m=aqkq",
		"http://javlibrary.com/en/vl_maker.php?m=aqkq":     "https://www.javlibrary.com/en/vl_maker.php?m=aqkq",
		"http://WWW.JAVLIBRARY.COM:80/en/javabc.html":      "https://www.javlibrary.com/en/javabc.html",
		"http://www.javlibrary.com./en/javabc.html":        "https://www.javlibrary.com/en/javabc.html",
		"https://javlibrary.com/en/javabc.html":            "https://www.javlibrary.com/en/javabc.html",
		"https://www.javlibrary.com/en/javabc.html":        "https://www.javlibrary.com/en/javabc.html",
		"http://example.com/list":                          "http://example.com/list",
		"not a url at all %%%":                             "not a url at all %%%",
	}
	for input, want := range cases {
		if got := normalizeJavLibraryURL(input); got != want {
			t.Errorf("normalizeJavLibraryURL(%q)=%q, want %q", input, got, want)
		}
	}
}

func TestJavLibrarySolverBoundaryAlwaysReceivesHTTPS(t *testing.T) {
	var target string
	solver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			URL string `json:"url"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		target = payload.URL
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "solution": map[string]any{"response": "<html></html>"}})
	}))
	defer solver.Close()

	j := NewJavLibrary(2*time.Second, solver.URL, 0, nil)
	if _, err := j.flare(context.Background(), "http://www.javlibrary.com/en/javme3rf2u.html", solver.URL); err != nil {
		t.Fatal(err)
	}
	if target != "https://www.javlibrary.com/en/javme3rf2u.html" {
		t.Fatalf("solver target=%q, want canonical HTTPS JavLibrary URL", target)
	}
}

// TestJavLibrarySkipsDirectFetchWhenFlareSolverrConfigured guards against the
// exact bug reported in production: with a FlareSolverr solver configured,
// every scrape was still attempting a direct fetch first (logging a 403 "via
// direct" on every single request) before falling back to FlareSolverr.
// Once a solver is configured it must be used exclusively - the direct
// endpoint below must never be hit.
func TestJavLibrarySkipsDirectFetchWhenFlareSolverrConfigured(t *testing.T) {
	directHit := false
	mux := http.NewServeMux()
	mux.HandleFunc("/list", func(w http.ResponseWriter, r *http.Request) {
		directHit = true
		w.WriteHeader(http.StatusForbidden)
	})
	mux.HandleFunc("/flaresolverr", func(w http.ResponseWriter, r *http.Request) {
		page := `<html><div class="video"><a href="/javabc123.html" title="Listing title"><img src="/abc00123ps.jpg"></a><div class="id">ABC-123</div></div></html>`
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "solution": map[string]any{"response": page}})
	})
	mux.HandleFunc("/javabc123.html", func(w http.ResponseWriter, r *http.Request) {
		directHit = true
		w.WriteHeader(http.StatusForbidden)
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	items, err := NewJavLibrary(2*time.Second, server.URL+"/flaresolverr", 0, nil).Scrape(context.Background(), server.URL+"/list", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].VideoID != "ABC-123" {
		t.Fatalf("items=%+v", items)
	}
	if directHit {
		t.Fatal("direct fetch endpoint was hit even though a FlareSolverr solver was configured")
	}
}

func TestJavLibraryAllPagesContinuesPastFilteredPageAndStopsAtOnlineEnd(t *testing.T) {
	maxPage := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/list", func(w http.ResponseWriter, r *http.Request) {
		page := 1
		if r.URL.Query().Get("page") == "2" {
			page = 2
		} else if r.URL.Query().Get("page") == "3" {
			page = 3
		}
		maxPage = max(maxPage, page)
		if page == 1 {
			_, _ = w.Write([]byte(`<div class="video"><a href="/javold.html" title="Old"><img src="/oldps.jpg"></a><div class="id">OLD-1</div></div>`))
			return
		}
		_, _ = w.Write([]byte(`<div class="video"><a href="/javnew.html" title="New"><img src="/newps.jpg"></a><div class="id">NEW-2</div></div>`))
	})
	mux.HandleFunc("/javnew.html", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<html><title>New release - JAVLibrary</title></html>`))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	items, err := NewJavLibrary(2*time.Second, "", 0, nil).ScrapeFilteredThroughEnd(context.Background(), server.URL+"/list", 0, func(videoID string) bool {
		return videoID == "NEW-2"
	}, ScrapeConcurrency{})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].VideoID != "NEW-2" {
		t.Fatalf("items=%+v, want only NEW-2", items)
	}
	if maxPage != 3 {
		t.Fatalf("last requested page=%d, want repeated online end at page 3", maxPage)
	}
}

func TestJavLibraryRequestedLimitAbove500StopsAtReportedOnlineEnd(t *testing.T) {
	maxPage := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/list", func(w http.ResponseWriter, r *http.Request) {
		page := 1
		if value := r.URL.Query().Get("page"); value != "" {
			page, _ = strconv.Atoi(value)
		}
		maxPage = max(maxPage, page)
		_, _ = fmt.Fprintf(w, `<html><a href="?page=3">Last</a><div class="video"><a href="/javpage%d.html" title="Page %d"><img src="/page%dps.jpg"></a><div class="id">PAGE-%d</div></div></html>`, page, page, page, page)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<html><title>Release detail - JAVLibrary</title></html>`))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	items, err := NewJavLibrary(2*time.Second, "", 0, nil).Scrape(context.Background(), server.URL+"/list", 700)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 {
		t.Fatalf("items=%d, want 3", len(items))
	}
	if maxPage != 3 {
		t.Fatalf("last requested page=%d, want reported online end at page 3", maxPage)
	}
}

// TestJavLibraryAddByURL covers TODO-2.0 Phase 2's "Missing Library Files"
// recovery flow: given only a raw JavLibrary product URL (as found in a
// StashApp scene's URL list) and no listing page, AddByURL must still scrape
// full detail metadata and derive a usable VideoID.
func TestJavLibraryAddByURL(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/javabc123.html", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<html><title>ABC-123 Detailed title - JAVLibrary</title><img id="video_jacket_img" src="/abc00123pl.jpg"><table><tr><td class="header">Release Date:</td><td class="text">2024-02-03</td></tr></table><div id="video_cast"><span class="cast"><span class="star"><a>Actor One</a></span></span></div></html>`))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	got, err := NewJavLibrary(2*time.Second, "", 0, nil).AddByURL(context.Background(), server.URL+"/javabc123.html", "")
	if err != nil {
		t.Fatal(err)
	}
	if got.VideoID != "ABC-123" || got.Title != "ABC-123 Detailed title" || got.Source != "JavLibrary" || got.ProductURL != server.URL+"/javabc123.html" || got.Actress != "Actor One" {
		t.Fatalf("unexpected release: %+v", got)
	}
}

// TestJavLibraryAddByURLFallsBackToProvidedVideoID covers a detail page
// whose <title> has no id-shaped token to parse (common on pages with a
// purely descriptive title) - AddByURL must fall back to the caller-
// supplied video ID (the StashApp scene's own code/title match) rather
// than failing outright.
func TestJavLibraryAddByURLFallsBackToProvidedVideoID(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/javxyz.html", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<html><title>A Purely Descriptive Title - JAVLibrary</title><img id="video_jacket_img" src="/xyzpl.jpg"></html>`))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	got, err := NewJavLibrary(2*time.Second, "", 0, nil).AddByURL(context.Background(), server.URL+"/javxyz.html", "xyz-999")
	if err != nil {
		t.Fatal(err)
	}
	if got.VideoID != "XYZ-999" {
		t.Fatalf("expected fallback video id XYZ-999, got %q", got.VideoID)
	}
}

func TestParseJavLibraryDetailIgnoresNowPrintingPlaceholder(t *testing.T) {
	doc, err := html.Parse(strings.NewReader(`<html><title>Pending - JAVLibrary</title><img id="video_jacket_img" src="/now_printing.jpg" alt="Now Printing"></html>`))
	if err != nil {
		t.Fatal(err)
	}
	if got := parseJavLibraryDetail(doc, "https://www.javlibrary.com/en/javtest.html").ImageURL; got != "" {
		t.Fatalf("placeholder image URL = %q, want it ignored", got)
	}
}

func TestParseJavLibraryDetailUsesFullSizePreviewLinks(t *testing.T) {
	doc, err := html.Parse(strings.NewReader(`<html><title>FJIN-17 - JAVLibrary</title><div class="previewthumbs"><a href="https://pics.dmm.co.jp/digital/video/fjin00017/fjin00017jp-1.jpg"><img src="https://pics.dmm.co.jp/digital/video/fjin00017/fjin00017-1.jpg"></a><a href="https://pics.dmm.co.jp/digital/video/fjin00017/fjin00017jp-2.jpg"><img src="https://pics.dmm.co.jp/digital/video/fjin00017/fjin00017-2.jpg"></a></div></html>`))
	if err != nil {
		t.Fatal(err)
	}
	got := parseJavLibraryDetail(doc, "https://www.javlibrary.com/en/javtest.html").Screenshots
	want := []string{"https://pics.dmm.co.jp/digital/video/fjin00017/fjin00017jp-1.jpg", "https://pics.dmm.co.jp/digital/video/fjin00017/fjin00017jp-2.jpg"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("screenshots=%v, want full-size links %v", got, want)
	}
}

func TestParseJavLibraryDetailUpgradesUnlinkedPreviewImages(t *testing.T) {
	doc, err := html.Parse(strings.NewReader(`<html><title>FJIN-17 - JAVLibrary</title><div class="previewthumbs"><img src="https://pics.dmm.co.jp/digital/video/fjin00017/fjin00017-1.jpg"><img src="https://pics.dmm.co.jp/digital/video/fjin00017/fjin00017-2.jpg"></div></html>`))
	if err != nil {
		t.Fatal(err)
	}
	got := parseJavLibraryDetail(doc, "https://www.javlibrary.com/en/javtest.html").Screenshots
	want := []string{"https://pics.dmm.co.jp/digital/video/fjin00017/fjin00017jp-1.jpg", "https://pics.dmm.co.jp/digital/video/fjin00017/fjin00017jp-2.jpg"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("screenshots=%v, want upgraded full-size URLs %v", got, want)
	}
}

func TestJavLibraryLiveMultiActressAndTags(t *testing.T) {
	if os.Getenv("JAVBEACON_LIVE_JAVLIBRARY_TEST") != "1" {
		t.Skip("set JAVBEACON_LIVE_JAVLIBRARY_TEST=1")
	}
	solver := os.Getenv("JAVBEACON_FLARESOLVERR_URL")
	if solver == "" {
		t.Skip("set JAVBEACON_FLARESOLVERR_URL")
	}
	got, err := NewJavLibrary(90*time.Second, solver, 0, nil).Refresh(context.Background(), domain.Release{ProductURL: "https://www.javlibrary.com/en/javli6ycyi.html"})
	if err != nil {
		t.Fatal(err)
	}
	assertMIZD181Metadata(t, got)
}

func TestJavLibraryCapturedMultiActressAndTags(t *testing.T) {
	path := os.Getenv("JAVBEACON_JAVLIBRARY_CAPTURE")
	if path == "" {
		t.Skip("set JAVBEACON_JAVLIBRARY_CAPTURE to a solver JSON response")
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		Solution struct {
			Response string `json:"response"`
		} `json:"solution"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		t.Fatal(err)
	}
	doc, err := html.Parse(bytes.NewBufferString(response.Solution.Response))
	if err != nil {
		t.Fatal(err)
	}
	assertMIZD181Metadata(t, parseJavLibraryDetail(doc, "https://www.javlibrary.com/en/javli6ycyi.html"))
}

func assertMIZD181Metadata(t *testing.T, got domain.Release) {
	t.Helper()
	actresses := strings.Split(got.Actress, ", ")
	if len(actresses) != 7 || !containsFold(actresses, "Neo Akari") || !containsFold(actresses, "Kojima Ami") {
		t.Fatalf("multi-actress scrape: %q", got.Actress)
	}
	if len(got.Genres) != 8 || !containsFold(got.Genres, "Restraint") || !containsFold(got.Genres, "Best, Omnibus") {
		t.Fatalf("multi-tag scrape: %v", got.Genres)
	}
}

func containsFold(values []string, want string) bool {
	for _, value := range values {
		if strings.EqualFold(value, want) {
			return true
		}
	}
	return false
}

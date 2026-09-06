package download

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Net005/JAVBeacon/internal/domain"
	"golang.org/x/net/html"
)

type pikPakRoundTripFunc func(*http.Request) (*http.Response, error)

func (f pikPakRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func pikPakJSONResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

func TestPreferredPikPakDownloadURLChoosesExplicitOriginal(t *testing.T) {
	file := pikPakFile{WebContentLink: "https://cdn.test/default-transcode"}
	file.Medias = make([]struct {
		Link struct {
			URL string `json:"url"`
		} `json:"link"`
		IsOrigin bool `json:"is_origin"`
	}, 2)
	file.Medias[0].Link.URL = "https://cdn.test/transcode"
	file.Medias[1].Link.URL = "https://cdn.test/original"
	file.Medias[1].IsOrigin = true
	if got := preferredPikPakDownloadURL(file); got != "https://cdn.test/original" {
		t.Fatalf("URL=%q, want explicit original", got)
	}
}

func TestAuthenticatedPikPakRestoreAndOriginalResolution(t *testing.T) {
	client := &http.Client{Transport: pikPakRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch {
		case req.URL.Host == "user.mypikpak.com" && req.URL.Path == "/v1/shield/captcha/init":
			body, _ := io.ReadAll(req.Body)
			if bytes.Contains(body, []byte(`"action":"POST:/v1/auth/signin"`)) && !bytes.Contains(body, []byte(`"username":"person@example.test"`)) {
				t.Fatalf("sign-in CAPTCHA metadata omitted username: %s", body)
			}
			return pikPakJSONResponse(http.StatusOK, `{"captcha_token":"captcha"}`), nil
		case req.URL.Host == "user.mypikpak.com" && req.URL.Path == "/v1/auth/signin":
			if req.Header.Get("X-Captcha-Token") != "captcha" {
				t.Fatalf("sign-in omitted CAPTCHA token")
			}
			body, _ := io.ReadAll(req.Body)
			if !bytes.Contains(body, []byte(`"captcha_token":"captcha"`)) {
				t.Fatalf("sign-in body omitted CAPTCHA token: %s", body)
			}
			return pikPakJSONResponse(http.StatusOK, `{"access_token":"access","refresh_token":"refresh","sub":"user-id"}`), nil
		case req.URL.Path == "/drive/v1/share/restore":
			if req.Header.Get("Authorization") != "Bearer access" {
				t.Fatalf("restore omitted account authorization")
			}
			body, _ := io.ReadAll(req.Body)
			if !bytes.Contains(body, []byte(`"file_ids":["shared-file"]`)) {
				t.Fatalf("restore body did not pin selected file: %s", body)
			}
			return pikPakJSONResponse(http.StatusOK, `{"restore_status":"RESTORE_COMPLETE","params":{"trace_file_ids":"restored-file"}}`), nil
		case req.URL.Path == "/drive/v1/files/restored-file":
			return pikPakJSONResponse(http.StatusOK, `{"id":"restored-file","name":"TEST-001.mp4","size":"4331682987","medias":[{"is_origin":true,"link":{"url":"https://cdn.test/original"}}]}`), nil
		case req.URL.Path == "/drive/v1/files:batchDelete":
			return pikPakJSONResponse(http.StatusOK, `{}`), nil
		default:
			t.Fatalf("unexpected PikPak request: %s %s", req.Method, req.URL)
			return nil, nil
		}
	})}
	account := newPikPakClient(client)
	if err := account.login(context.Background(), "person@example.test", "secret"); err != nil {
		t.Fatal(err)
	}
	restoredID, err := account.restoreSharedFile(context.Background(), "share", "shared-file")
	if err != nil {
		t.Fatal(err)
	}
	if restoredID != "restored-file" {
		t.Fatalf("restored ID=%q", restoredID)
	}
	file, err := account.authenticatedFile(context.Background(), restoredID)
	if err != nil {
		t.Fatal(err)
	}
	if got := preferredPikPakDownloadURL(file); got != "https://cdn.test/original" {
		t.Fatalf("authenticated original URL=%q", got)
	}
	if err := account.deleteFile(context.Background(), restoredID); err != nil {
		t.Fatal(err)
	}
}

func TestPikPakSignInErrorRedactsCredentials(t *testing.T) {
	client := &http.Client{Transport: pikPakRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path == "/v1/shield/captcha/init" {
			return pikPakJSONResponse(http.StatusOK, `{"captcha_token":"captcha"}`), nil
		}
		return pikPakJSONResponse(http.StatusBadRequest, `{"error":"captcha_invalid","error_description":"meta.username expect person@example.test and password VerySecret"}`), nil
	})}
	err := newPikPakClient(client).login(context.Background(), "person@example.test", "VerySecret")
	if err == nil || !strings.Contains(err.Error(), "[redacted]") {
		t.Fatalf("error=%v, want redacted provider failure", err)
	}
	if strings.Contains(strings.ToLower(err.Error()), "person@example.test") || strings.Contains(err.Error(), "VerySecret") {
		t.Fatalf("authentication error leaked credentials: %v", err)
	}
}

func TestPikPakHumanVerificationExposesOnlyOfficialURL(t *testing.T) {
	client := &http.Client{Transport: pikPakRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		return pikPakJSONResponse(http.StatusBadRequest, `{"error":"captcha_required","url":"https://user.mypikpak.com/verify/challenge"}`), nil
	})}
	account := newPikPakClient(client)
	err := account.login(context.Background(), "person@example.test", "secret")
	if err == nil || account.verificationURL != "https://user.mypikpak.com/verify/challenge" || !strings.Contains(err.Error(), "human verification") {
		t.Fatalf("error=%v verification_url=%q", err, account.verificationURL)
	}
	if got := safePikPakVerificationURL("https://mypikpak.com.evil.test/steal"); got != "" {
		t.Fatalf("accepted untrusted verification URL %q", got)
	}
}

func TestDiscoverPikPakShareIDFollowsKeepshareIntermediateAndStopsBeforePikPak(t *testing.T) {
	intermediate := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://mypikpak.com/s/VP-YhHbopMQjt_gRbkB6ORjZo2/AAAAAAfNVsQqoHTeqzJ8lh0Yo2_VP-?act=play", http.StatusFound)
	}))
	defer intermediate.Close()
	entry := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, intermediate.URL+"/forwarded-share", http.StatusMovedPermanently)
	}))
	defer entry.Close()
	shareID, err := discoverPikPakShareID(context.Background(), entry.Client(), entry.URL)
	if err != nil {
		t.Fatal(err)
	}
	if shareID != "VP-YhHbopMQjt_gRbkB6ORjZo2" {
		t.Fatalf("share ID=%q", shareID)
	}
}

func TestDiscoverPikPakShareIDParsesDirectPlayerURLWithoutRequest(t *testing.T) {
	shareID, err := discoverPikPakShareID(context.Background(), &http.Client{Timeout: time.Nanosecond}, "https://mypikpak.com/s/direct-share/direct-file?act=play")
	if err != nil {
		t.Fatal(err)
	}
	if shareID != "direct-share" {
		t.Fatalf("share ID=%q", shareID)
	}
}

func TestReleaseIDMatchesTextIsCaseInsensitiveAndRejectsHalfMatches(t *testing.T) {
	for _, value := range []string{"ADN-803", "adn803.mp4", "[source] AdN_803-U.mp4", "Pred 899", "pred_899", "PRED.899", "pReD-899"} {
		releaseID := "ADN-803"
		if strings.Contains(strings.ToUpper(value), "PRED") || strings.Contains(value, "Pred") {
			releaseID = "PRED-899"
		}
		if !releaseIDMatchesText(value, releaseID) {
			t.Fatalf("expected %q to match", value)
		}
	}
	for _, value := range []string{"ADN-8030.mp4", "XADN-803.mp4", "ADN-80.mp4", "PRED-8990", "PRED-899A", "XPRED-899"} {
		releaseID := "ADN-803"
		if strings.Contains(value, "PRED") {
			releaseID = "PRED-899"
		}
		if releaseIDMatchesText(value, releaseID) {
			t.Fatalf("expected half-match %q to be rejected", value)
		}
	}
}

func TestReleaseIDsEqualIgnoresCaseAndCommonSeparators(t *testing.T) {
	for _, candidate := range []string{"pred-899", "PrEd-899", "Pred 899", "pred_899", "PRED.899"} {
		if !releaseIDsEqual(candidate, "PRED-899") {
			t.Fatalf("expected %q to canonically match PRED-899", candidate)
		}
	}
	for _, candidate := range []string{"PRED-899A", "PRED-8990", "PRED-899-U"} {
		if releaseIDsEqual(candidate, "PRED-899") {
			t.Fatalf("expected %q to remain a distinct ID", candidate)
		}
	}
}

func javDBFixtureProvider(t *testing.T, handler http.HandlerFunc) (*javDBProvider, func()) {
	t.Helper()
	server := httptest.NewServer(handler)
	provider := &javDBProvider{client: server.Client(), baseURL: server.URL}
	provider.inspectCandidate = func(_ context.Context, _ string, releaseID string) (pikPakFile, []pikPakFile, error) {
		selected := pikPakFile{ID: "video", Name: "4k688.com@" + releaseID + ".mp4", Size: "4294967296"}
		return selected, []pikPakFile{selected}, nil
	}
	return provider, server.Close
}

func javDBSearchPage(id, date, detailPath string) string {
	return `<html><body><div class="item"><a class="box" href="` + detailPath + `"><strong>` + id + `</strong><div class="meta">` + date + `</div></a></div></body></html>`
}

func TestJavDBSearchMatchesPREDSpacingAndPRPMExactDate(t *testing.T) {
	for _, test := range []struct {
		name, requested, displayed string
	}{
		{name: "PRED spacing", requested: "PRED-899", displayed: "ID: Pred 899"},
		{name: "PRPM exact", requested: "PRPM-002", displayed: "ID: PRPM-002"},
		{name: "PRPM lowercase", requested: "PRPM-002", displayed: "ID: prpm-002"},
	} {
		t.Run(test.name, func(t *testing.T) {
			provider, closeServer := javDBFixtureProvider(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/search":
					_, _ = w.Write([]byte(javDBSearchPage(test.displayed, "2026-09-15", "/v/exact")))
				case "/v/exact":
					_, _ = w.Write([]byte(`<html><body><div>` + test.displayed + `</div><section class="new-download-layout"><a href="https://keepshare.org/share">Get file</a></section></body></html>`))
				default:
					http.NotFound(w, r)
				}
			})
			defer closeServer()
			rows, err := provider.Search(context.Background(), domain.Release{VideoID: test.requested, ReleaseDate: "2026-09-15"})
			if err != nil || len(rows) != 1 {
				t.Fatalf("rows=%+v err=%v", rows, err)
			}
			if rows[0].MatchedFile != "4k688.com@"+test.requested+".mp4" {
				t.Fatalf("candidate inspection did not run: %+v", rows[0])
			}
			if rows[0].ProviderFileID != "video" {
				t.Fatalf("selected provider file ID was not retained: %+v", rows[0])
			}
		})
	}
}

func TestJavDBSearchClassifiesPipelineFailures(t *testing.T) {
	tests := []struct {
		name, requested, storedDate string
		handler                     http.HandlerFunc
		want                        string
	}{
		{name: "search forbidden", requested: "PRPM-002", storedDate: "2026-09-15", want: "search request failed", handler: func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "blocked", http.StatusForbidden) }},
		{name: "search challenge", requested: "PRPM-002", storedDate: "2026-09-15", want: "challenge page", handler: func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`<title>Just a moment...</title><div>cf-chl-widget</div>`))
		}},
		{name: "no parsable cards", requested: "PRPM-002", storedDate: "2026-09-15", want: "no parsable release results", handler: func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`<html><body>ordinary empty result markup</body></html>`))
		}},
		{name: "no exact ID", requested: "PRPM-002", storedDate: "2026-09-15", want: "no exact release ID match", handler: func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(javDBSearchPage("PRPM-003", "2026-09-15", "/v/wrong")))
		}},
		{name: "date mismatch", requested: "PRPM-002", storedDate: "2026-09-15", want: "release date is incompatible", handler: func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(javDBSearchPage("PRPM-002", "2025-01-01", "/v/date")))
		}},
		{name: "detail forbidden", requested: "PRPM-002", storedDate: "2026-09-15", want: "detail page fetch failed", handler: func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/search" {
				_, _ = w.Write([]byte(javDBSearchPage("PRPM-002", "2026-09-15", "/v/blocked")))
				return
			}
			http.Error(w, "blocked", http.StatusForbidden)
		}},
		{name: "download parser missing", requested: "PRPM-002", storedDate: "2026-09-15", want: "download section could not be parsed", handler: func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/search" {
				_, _ = w.Write([]byte(javDBSearchPage("PRPM-002", "2026-09-15", "/v/plain")))
				return
			}
			_, _ = w.Write([]byte(`<html><body>ID: PRPM-002 ordinary detail content</body></html>`))
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			provider, closeServer := javDBFixtureProvider(t, test.handler)
			defer closeServer()
			_, err := provider.Search(context.Background(), domain.Release{VideoID: test.requested, ReleaseDate: test.storedDate})
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(test.want)) {
				t.Fatalf("error=%v, want stage %q", err, test.want)
			}
		})
	}
}

func TestJavDBExactReleaseWithoutShareIsVisibleAndLogged(t *testing.T) {
	provider, closeServer := javDBFixtureProvider(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/search" {
			_, _ = w.Write([]byte(javDBSearchPage("PRPM-002", "2026-09-15", "/v/no-links")))
			return
		}
		_, _ = w.Write([]byte(`<html><body>ID: PRPM-002 <section class="download-list"><button>Download unavailable</button></section></body></html>`))
	})
	defer closeServer()
	var output bytes.Buffer
	provider.log = slog.New(slog.NewJSONHandler(&output, nil))
	rows, err := provider.Search(context.Background(), domain.Release{VideoID: "PRPM-002", ReleaseDate: "2026-09-15"})
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}
	if rows[0].Accepted || rows[0].SourceURL == "" || !strings.Contains(rows[0].Reason, "no Keepshare/PikPak download link") {
		t.Fatalf("expected a linked, non-downloadable diagnostic result, got %+v", rows[0])
	}
	logged := output.String()
	for _, want := range []string{`"msg":"JavDB exact release has no downloadable HTTP share"`, `"requested_id":"PRPM-002"`, `"matched_id":"PRPM-002"`, `"keepshare_links":0`} {
		if !strings.Contains(logged, want) {
			t.Fatalf("log %q does not contain %q", logged, want)
		}
	}
}

func TestJavDBDateTolerance(t *testing.T) {
	stored := parseJavDBDate("2026-09-15")
	if delta := calendarDeltaDays(stored, parseJavDBDate("Released date: 2026-09-10")); delta != -5 {
		t.Fatalf("within-tolerance delta=%d", delta)
	}
	if delta := calendarDeltaDays(stored, parseJavDBDate("2027-01-01")); delta <= 60 {
		t.Fatalf("outside-tolerance delta=%d", delta)
	}
}

func TestJavDBCandidatesRequireExactIDAndCarryFileSize(t *testing.T) {
	doc, err := html.Parse(strings.NewReader(`<html><body>
		<div class="item"><span class="name">ADN-803-U.torrent</span><span class="meta">4.43GB, 2 files</span><a href="https://keepshare.org/u">Download</a><a href="https://keepshare.org/u-alternate">Alternate</a></div>
		<div class="item"><span class="name">ADN803.torrent</span><span class="meta">3.45GB</span><a href="https://keepshare.org/plain">Download</a></div>
		<div class="item"><span class="name">ADN-8030.torrent</span><span class="meta">9.99GB</span><a href="https://keepshare.org/wrong">Download</a></div>
	</body></html>`))
	if err != nil {
		t.Fatal(err)
	}
	rows := parseJavDBDownloadCandidates(doc, "https://javdb.com/v/example", "adn-803")
	if len(rows) != 3 {
		t.Fatalf("got %d candidates, want every distinct Keepshare link (3)", len(rows))
	}
	if rows[0].SizeBytes == 0 || rows[1].SizeBytes == 0 {
		t.Fatal("expected parsed byte sizes")
	}
}

func TestJavDBDownloadDiscoverySurvivesAlternateMarkupAndDeduplicatesLinks(t *testing.T) {
	doc, err := html.Parse(strings.NewReader(`<html><body>
		<section data-role="downloads"><article><b>PRPM 002</b><a title="PRPM.002 mirror" href="//keepshare.org/one">Web file</a></article></section>
		<div class="completely-new-wrapper"><a href="https://keepshare.org/one">Duplicate</a></div>
		<p><a download="prpm_002.mp4" href="https://keepshare.cc/two">Alternate host</a></p>
		<table class="help"><tr><td><a href="https://mypikpak.com">PikPak help</a></td></tr></table>
	</body></html>`))
	if err != nil {
		t.Fatal(err)
	}
	discovery := discoverJavDBDownloads(doc, "https://javdb.com/v/example", "PRPM-002")
	if !discovery.downloadSectionFound || len(discovery.rows) != 2 {
		t.Fatalf("alternate markup discovery=%+v", discovery)
	}
	if discovery.rows[0].Link != "https://keepshare.org/one" || discovery.rows[1].Link != "https://keepshare.cc/two" {
		t.Fatalf("unexpected deduplicated links: %+v", discovery.rows)
	}
}

func TestParseJavDBDetailIDSupportsLiveClipboardMarkupAndSpacing(t *testing.T) {
	for _, fixture := range []string{
		`<html><body><a data-clipboard-text="PRPM-002">copy</a></body></html>`,
		`<html><body><h2><strong>Pred 899</strong><strong>Title</strong></h2></body></html>`,
	} {
		doc, err := html.Parse(strings.NewReader(fixture))
		if err != nil {
			t.Fatal(err)
		}
		if id := parseJavDBDetailID(doc); !releaseIDsEqual(id, map[bool]string{true: "PRPM-002", false: "PRED-899"}[strings.Contains(fixture, "PRPM")]) {
			t.Fatalf("detail ID %q was not parsed canonically from %s", id, fixture)
		}
	}
}

func TestJavDBSearchFollowsSeparateDownloadAction(t *testing.T) {
	provider, closeServer := javDBFixtureProvider(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/search":
			_, _ = w.Write([]byte(javDBSearchPage("PRPM-002", "2026-09-15", "/v/prpm")))
		case "/v/prpm":
			_, _ = w.Write([]byte(`<html><body>ID: PRPM-002 <a class="download-action" href="/download/prpm">Download</a></body></html>`))
		case "/download/prpm":
			_, _ = w.Write([]byte(`<html><body><aside><a href="https://keepshare.org/prpm">PRPM 002 file</a></aside></body></html>`))
		default:
			http.NotFound(w, r)
		}
	})
	defer closeServer()
	rows, err := provider.Search(context.Background(), domain.Release{VideoID: "PRPM-002", ReleaseDate: "2026-09-15"})
	if err != nil || len(rows) != 1 || rows[0].Link != "https://keepshare.org/prpm" {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}
}

func TestJavDBSearchSurfacesKeepshareInspectionFailureOnCandidate(t *testing.T) {
	provider, closeServer := javDBFixtureProvider(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/search" {
			_, _ = w.Write([]byte(javDBSearchPage("PRPM-002", "2026-09-15", "/v/prpm")))
			return
		}
		_, _ = w.Write([]byte(`<html><body>ID: PRPM-002 <a href="https://keepshare.org/expired">PRPM-002</a></body></html>`))
	})
	defer closeServer()
	provider.inspectCandidate = func(context.Context, string, string) (pikPakFile, []pikPakFile, error) {
		return pikPakFile{}, nil, errors.New("share expired")
	}
	rows, err := provider.Search(context.Background(), domain.Release{VideoID: "PRPM-002", ReleaseDate: "2026-09-15"})
	if err != nil || len(rows) != 1 || !strings.Contains(rows[0].Reason, "share expired") {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}
}

func TestJavDBSortingPrefersConfiguredFilenamePatternsBeforeNormalHTTPOrder(t *testing.T) {
	rows := []domain.SearchResult{
		{Title: "ADN-803.mp4", SizeBytes: 9 << 30},
		{Title: "trusted@ ADN-803-U.mp4", SizeBytes: 5 << 30},
		{Title: "trusted@ ADN-803.mp4", SizeBytes: 3 << 30},
	}
	sortJavDBDownloadCandidates(rows, "ADN-803", legacyPreferredFilenamePatterns([]string{"trusted@"}))
	if rows[0].Title != "trusted@ ADN-803.mp4" || rows[1].Title != "trusted@ ADN-803-U.mp4" || rows[2].Title != "ADN-803.mp4" {
		t.Fatalf("unexpected preferred HTTP order: %+v", rows)
	}
	if !strings.Contains(rows[0].Reason, "trusted@") || !strings.Contains(rows[1].Reason, "trusted@") {
		t.Fatalf("preferred candidates should explain their matching pattern: %+v", rows)
	}
}

func TestPikPakFileSelectionUsesPreferredPatternsThenLargestFallback(t *testing.T) {
	files := []pikPakFile{
		{ID: "large", Name: "ADN-803.mp4", Size: "9000"},
		{ID: "preferred", Name: "trusted@ ADN-803.mp4", Size: "3000"},
		{ID: "other", Name: "ADN-803 sample.mp4", Size: "1000"},
	}
	selected, found := selectPikPakFile(files, "ADN-803", legacyPreferredFilenamePatterns([]string{"trusted@"}))
	if !found || selected.ID != "preferred" {
		t.Fatalf("preferred file was not selected: %+v, found=%v", selected, found)
	}
	selected, found = selectPikPakFile(files, "ADN-803", legacyPreferredFilenamePatterns([]string{"does-not-match"}))
	if !found || selected.ID != "large" {
		t.Fatalf("largest fallback file was not selected: %+v, found=%v", selected, found)
	}
}

func TestPikPakFileSelectionUsesPatternPriorityBeforeFileSize(t *testing.T) {
	files := []pikPakFile{
		{ID: "priority-ten", Name: "large@ADN-803.mp4", Size: "9000"},
		{ID: "priority-one", Name: "best@ADN-803.mp4", Size: "3000"},
	}
	selected, found := selectPikPakFile(files, "ADN-803", []PreferredFilenamePattern{
		{Pattern: "large@", Priority: 10},
		{Pattern: "best@", Priority: 1},
	})
	if !found || selected.ID != "priority-one" {
		t.Fatalf("priority-one file was not selected: %+v, found=%v", selected, found)
	}
}

func TestPikPakPinnedFileSelectionNeverSubstitutesAnotherMatchingFile(t *testing.T) {
	files := []pikPakFile{
		{ID: "smaller", Name: "ADN-803.mp4", Size: "1900000000"},
		{ID: "chosen", Name: "ADN-803.mp4", Size: "3400000000"},
	}
	selected, found := selectPikPakFileByID(files, "chosen", "ADN-803")
	if !found || selected.ID != "chosen" || selected.Size != "3400000000" {
		t.Fatalf("exact selected file was not retained: %+v, found=%v", selected, found)
	}
	if selected, found = selectPikPakFileByID(files, "missing", "ADN-803"); found {
		t.Fatalf("missing pinned file silently fell back to %+v", selected)
	}
}

func TestPikPakHistoricalSelectionUsesExactStoredNameAndSize(t *testing.T) {
	files := []pikPakFile{
		{ID: "wrong", Name: "ADN-803.mp4", Size: "1900000000"},
		{ID: "chosen", Name: "4k688.com@ADN-803.mp4", Size: "3689305356"},
	}
	selected, found := selectPikPakFileByIdentity(files, "4K688.COM@adn-803.mp4", 3689305356, "ADN-803")
	if !found || selected.ID != "chosen" {
		t.Fatalf("stored HTTP selection was not recovered exactly: %+v, found=%v", selected, found)
	}
	if selected, found = selectPikPakFileByIdentity(files, "4k688.com@ADN-803.mp4", 1900000000, "ADN-803"); found {
		t.Fatalf("mismatched stored name/size silently selected %+v", selected)
	}
}

func TestPikPakSearchFilesExposeSizesAndMatchedFile(t *testing.T) {
	files := []pikPakFile{
		{ID: "noise", Name: "sample.mp4", Size: "10485760"},
		{ID: "matched", Name: "hhd800.com@DLDSS-530.mp4", Size: "4294967296"},
	}
	names, details := pikPakSearchFiles(files, files[1])
	if len(names) != 2 || len(details) != 2 {
		t.Fatalf("unexpected search file metadata: names=%+v details=%+v", names, details)
	}
	if details[0].Matched || details[0].SizeBytes != 10485760 || !details[1].Matched || details[1].SizeBytes != 4294967296 {
		t.Fatalf("file sizes or matched marker were not preserved: %+v", details)
	}
}

func TestNextHTTPDestinationUsesStableCollisionSuffixes(t *testing.T) {
	dir := t.TempDir()
	if got := nextHTTPDestination(dir, "ADN-803"); got != filepath.Join(dir, "ADN-803.mp4") {
		t.Fatalf("first path = %q", got)
	}
	for _, name := range []string{"ADN-803.mp4", "ADN-803-0.mp4"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if got := nextHTTPDestination(dir, "ADN-803"); got != filepath.Join(dir, "ADN-803-1.mp4") {
		t.Fatalf("collision path = %q", got)
	}
}

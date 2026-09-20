package download

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Net005/JAVBeacon/internal/domain"
	"github.com/Net005/JAVBeacon/internal/scraper"
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

func TestJavDBHTMLUsesMultiInstanceSolverPoolOnlyAfter403(t *testing.T) {
	var directCalls, firstSolverCalls, secondSolverCalls int
	mux := http.NewServeMux()
	mux.HandleFunc("/javdb", func(w http.ResponseWriter, _ *http.Request) {
		directCalls++
		w.WriteHeader(http.StatusForbidden)
	})
	mux.HandleFunc("/solver-first", func(w http.ResponseWriter, _ *http.Request) {
		firstSolverCalls++
		http.Error(w, "busy", http.StatusBadGateway)
	})
	mux.HandleFunc("/solver-second", func(w http.ResponseWriter, _ *http.Request) {
		secondSolverCalls++
		_, _ = io.WriteString(w, `{"status":"ok","solution":{"response":"<html><div id=\"video-search\">USBA-090</div></html>"}}`)
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	pool := scraper.NewSolverPool()
	pool.Configure([]scraper.Instance{
		{URL: server.URL + "/solver-first", Priority: 1, Enabled: true},
		{URL: server.URL + "/solver-second", Priority: 2, Enabled: true},
	}, time.Hour)
	provider := &javDBProvider{client: server.Client(), solverPool: pool}
	doc, status, err := provider.getHTML(context.Background(), server.URL+"/javdb")
	if err != nil || status != http.StatusOK || !strings.Contains(nodeText(doc), "USBA-090") {
		t.Fatalf("doc=%v status=%d err=%v", doc, status, err)
	}
	if directCalls != 1 || firstSolverCalls != 1 || secondSolverCalls != 1 {
		t.Fatalf("calls direct=%d first=%d second=%d", directCalls, firstSolverCalls, secondSolverCalls)
	}
}

func TestJavDBHTMLDoesNotUseSolverForNon403Failure(t *testing.T) {
	solverCalls := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/javdb", func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "upstream", http.StatusBadGateway) })
	mux.HandleFunc("/solver", func(w http.ResponseWriter, _ *http.Request) {
		solverCalls++
		_, _ = io.WriteString(w, `{"status":"ok","solution":{"response":"<html></html>"}}`)
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	pool := scraper.NewSolverPool()
	pool.Configure([]scraper.Instance{{URL: server.URL + "/solver", Priority: 1, Enabled: true}}, 0)
	provider := &javDBProvider{client: server.Client(), solverPool: pool}
	_, status, err := provider.getHTML(context.Background(), server.URL+"/javdb")
	if err == nil || status != http.StatusBadGateway || solverCalls != 0 {
		t.Fatalf("status=%d solver_calls=%d err=%v", status, solverCalls, err)
	}
}

func TestPikPakFileChecksumIgnoresResourceHashAndUsesExplicitMD5(t *testing.T) {
	sha1Value := strings.Repeat("A", 40)
	md5Value := strings.Repeat("B", 32)
	checksumType, checksum := pikPakFileChecksum(pikPakFile{Hash: sha1Value, MD5Checksum: md5Value})
	if checksumType != "md5" || checksum != strings.ToLower(md5Value) {
		t.Fatalf("checksum=%s:%s", checksumType, checksum)
	}
	checksumType, checksum = pikPakFileChecksum(pikPakFile{Hash: sha1Value})
	if checksumType != "" || checksum != "" {
		t.Fatalf("resource hash was incorrectly accepted as a file checksum: %s:%s", checksumType, checksum)
	}
}

func TestAuthenticatedPikPakRestoreAndOriginalResolution(t *testing.T) {
	restorePosted := false
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
			if !bytes.Contains(body, []byte(`"kind":"drive#file"`)) {
				t.Fatalf("restore body omitted file kind: %s", body)
			}
			if bytes.Contains(body, []byte("trace_file_ids")) {
				t.Fatalf("restore body incorrectly supplied a trace/source ID: %s", body)
			}
			restorePosted = true
			return pikPakJSONResponse(http.StatusOK, `{"restore_status":"RESTORE_COMPLETE"}`), nil
		case req.URL.Path == "/drive/v1/files":
			if !restorePosted {
				return pikPakJSONResponse(http.StatusOK, `{"files":[]}`), nil
			}
			return pikPakJSONResponse(http.StatusOK, `{"files":[{"id":"restored-file","name":"TEST-001.mp4","kind":"drive#file","size":"4331682987"}]}`), nil
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
	restored, newlyRestored, err := account.restoreSharedFile(context.Background(), "share", "shared-file", "TEST-001.mp4", 4331682987)
	if err != nil {
		t.Fatal(err)
	}
	if restored.ID != "restored-file" || !newlyRestored {
		t.Fatalf("restored file=%+v newlyRestored=%t", restored, newlyRestored)
	}
	file, err := account.authenticatedFile(context.Background(), restored.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := preferredPikPakDownloadURL(file); got != "https://cdn.test/original" {
		t.Fatalf("authenticated original URL=%q", got)
	}
	if err := account.deleteFile(context.Background(), restored.ID); err != nil {
		t.Fatal(err)
	}
}

// TestPikPakRestoreReusesExistingAccountFile guards against a real duplicate-
// file bug: restoreSharedFile used to call PikPak's restore endpoint on every
// download attempt for a release - the initial download, a retry, a resume
// after a JAVBeacon restart, an automatic re-download after a failed video
// check - even when a file with the exact same name and size from an earlier
// attempt was already sitting in the account. PikPak's restore endpoint does
// not deduplicate, so this placed a second copy of the same file every time.
// restoreSharedFile must now find that existing file (via the account
// inventory it already fetches) and reuse it instead of restoring again.
func TestPikPakRestoreReusesExistingAccountFile(t *testing.T) {
	restoreCalled := false
	client := &http.Client{Transport: pikPakRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch {
		case req.URL.Host == "user.mypikpak.com" && req.URL.Path == "/v1/shield/captcha/init":
			return pikPakJSONResponse(http.StatusOK, `{"captcha_token":"captcha"}`), nil
		case req.URL.Host == "user.mypikpak.com" && req.URL.Path == "/v1/auth/signin":
			return pikPakJSONResponse(http.StatusOK, `{"access_token":"access","refresh_token":"refresh","sub":"user-id"}`), nil
		case req.URL.Path == "/drive/v1/share/restore":
			restoreCalled = true
			return pikPakJSONResponse(http.StatusOK, `{"restore_status":"RESTORE_COMPLETE"}`), nil
		case req.URL.Path == "/drive/v1/files":
			// Already restored by an earlier attempt at this same release.
			return pikPakJSONResponse(http.StatusOK, `{"files":[{"id":"already-restored","name":"TEST-001.mp4","kind":"drive#file","size":"4331682987"}]}`), nil
		default:
			t.Fatalf("unexpected PikPak request: %s %s", req.Method, req.URL)
			return nil, nil
		}
	})}
	account := newPikPakClient(client)
	if err := account.login(context.Background(), "person@example.test", "secret"); err != nil {
		t.Fatal(err)
	}
	restored, newlyRestored, err := account.restoreSharedFile(context.Background(), "share", "shared-file", "TEST-001.mp4", 4331682987)
	if err != nil {
		t.Fatal(err)
	}
	if restored.ID != "already-restored" {
		t.Fatalf("restored file=%+v, want the pre-existing account file to be reused", restored)
	}
	if newlyRestored {
		t.Fatal("newlyRestored=true for a file that already existed before this attempt - cleanup would incorrectly delete a file another download attempt still depends on")
	}
	if restoreCalled {
		t.Fatal("restoreSharedFile called PikPak's restore endpoint even though the exact file already existed in the account - this duplicates the file")
	}
}

func TestPikPakRestoreRetriesTransientGatewayFailure(t *testing.T) {
	originalDelay := pikPakRestoreRetryDelay
	pikPakRestoreRetryDelay = time.Millisecond
	t.Cleanup(func() { pikPakRestoreRetryDelay = originalDelay })

	restoreAttempts := 0
	client := &http.Client{Transport: pikPakRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/v1/shield/captcha/init":
			return pikPakJSONResponse(http.StatusOK, `{"captcha_token":"captcha"}`), nil
		case "/drive/v1/files":
			if restoreAttempts < 2 {
				return pikPakJSONResponse(http.StatusOK, `{"files":[]}`), nil
			}
			return pikPakJSONResponse(http.StatusOK, `{"files":[{"id":"restored-file","name":"TEST-002.mp4","kind":"drive#file","size":"4000"}]}`), nil
		case "/drive/v1/share/restore":
			restoreAttempts++
			if restoreAttempts == 1 {
				return pikPakJSONResponse(http.StatusBadGateway, `{"error":"temporary gateway failure"}`), nil
			}
			return pikPakJSONResponse(http.StatusOK, `{"restore_status":"RESTORE_COMPLETE"}`), nil
		default:
			t.Fatalf("unexpected PikPak request: %s %s", req.Method, req.URL)
			return nil, nil
		}
	})}
	account := newPikPakClient(client)
	account.accessToken = "access"
	file, newlyRestored, err := account.restoreSharedFile(context.Background(), "share", "shared-file", "TEST-002.mp4", 4000)
	if err != nil {
		t.Fatal(err)
	}
	if restoreAttempts != 2 {
		t.Fatalf("restore attempts=%d, want 2", restoreAttempts)
	}
	if file.ID != "restored-file" || !newlyRestored {
		t.Fatalf("restored file=%+v newlyRestored=%t", file, newlyRestored)
	}
}

func TestPikPakRestoreReconcilesAmbiguousFailure(t *testing.T) {
	restorePosted := false
	restoreAttempts := 0
	client := &http.Client{Transport: pikPakRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/v1/shield/captcha/init":
			return pikPakJSONResponse(http.StatusOK, `{"captcha_token":"captcha"}`), nil
		case "/drive/v1/files":
			if !restorePosted {
				return pikPakJSONResponse(http.StatusOK, `{"files":[]}`), nil
			}
			return pikPakJSONResponse(http.StatusOK, `{"files":[{"id":"restored-file","name":"TEST-003.mp4","kind":"drive#file","size":"5000"}]}`), nil
		case "/drive/v1/share/restore":
			restoreAttempts++
			restorePosted = true
			return pikPakJSONResponse(http.StatusBadGateway, `{"error":"response lost after acceptance"}`), nil
		default:
			t.Fatalf("unexpected PikPak request: %s %s", req.Method, req.URL)
			return nil, nil
		}
	})}
	account := newPikPakClient(client)
	account.accessToken = "access"
	file, newlyRestored, err := account.restoreSharedFile(context.Background(), "share", "shared-file", "TEST-003.mp4", 5000)
	if err != nil {
		t.Fatal(err)
	}
	if restoreAttempts != 1 {
		t.Fatalf("restore attempts=%d, want no duplicate submission", restoreAttempts)
	}
	if file.ID != "restored-file" || !newlyRestored {
		t.Fatalf("restored file=%+v newlyRestored=%t", file, newlyRestored)
	}
}

func TestPikPakRestoreUsesValidatedTaskDestinationID(t *testing.T) {
	listCalls := 0
	client := &http.Client{Transport: pikPakRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch {
		case req.URL.Path == "/v1/shield/captcha/init":
			return pikPakJSONResponse(http.StatusOK, `{"captcha_token":"captcha"}`), nil
		case req.URL.Path == "/drive/v1/files":
			listCalls++
			return pikPakJSONResponse(http.StatusOK, `{"files":[]}`), nil
		case req.URL.Path == "/drive/v1/share/restore":
			return pikPakJSONResponse(http.StatusOK, `{"restore_status":"RESTORE_START","restore_task_id":"task-1"}`), nil
		case req.URL.Path == "/drive/v1/tasks/task-1":
			return pikPakJSONResponse(http.StatusOK, `{"phase":"PHASE_TYPE_COMPLETE","params":{"trace_file_ids":["shared-source","account-destination"]}}`), nil
		case req.URL.Path == "/drive/v1/files/shared-source":
			return pikPakJSONResponse(http.StatusNotFound, `{"error":"file_not_found"}`), nil
		case req.URL.Path == "/drive/v1/files/account-destination":
			return pikPakJSONResponse(http.StatusOK, `{"id":"account-destination","parent_id":"shared-folder","name":"PRTD-006.mp4","kind":"drive#file","size":"3690441219"}`), nil
		default:
			t.Fatalf("unexpected PikPak request: %s %s", req.Method, req.URL)
			return nil, nil
		}
	})}
	account := newPikPakClient(client)
	account.accessToken = "access"
	file, newlyRestored, err := account.restoreSharedFile(context.Background(), "share", "shared-source", "PRTD-006.mp4", 3690441219)
	if err != nil || !newlyRestored || file.ID != "account-destination" || file.ParentID != "shared-folder" {
		t.Fatalf("file=%+v newly=%t err=%v", file, newlyRestored, err)
	}
	if listCalls != 1 {
		t.Fatalf("drive listing calls=%d, want only the pre-restore safety inventory", listCalls)
	}
}

func TestExactPikPakAccountFilePinsNameAndSize(t *testing.T) {
	files := []pikPakFile{
		{ID: "wrong-size", Name: "TEST-001.mp4", Size: "1900000000"},
		{ID: "exact", Name: "test-001.MP4", Size: "4331682987"},
	}
	file, found := exactPikPakAccountFile(files, "TEST-001.mp4", 4331682987, nil)
	if !found || file.ID != "exact" {
		t.Fatalf("file=%+v found=%t, want exact name/size match", file, found)
	}
	if _, found := exactPikPakAccountFile(files, "TEST-001.mp4", 4331682987, map[string]bool{"exact": true}); found {
		t.Fatal("matched a pre-existing excluded account file")
	}
}

func TestFindRestoredPikPakFilePrioritizesSharedFolderAndStopsEarly(t *testing.T) {
	visitedUnrelated := false
	client := &http.Client{Transport: pikPakRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path == "/v1/shield/captcha/init" {
			return pikPakJSONResponse(http.StatusOK, `{"captcha_token":"captcha"}`), nil
		}
		switch req.URL.Query().Get("parent_id") {
		case "":
			return pikPakJSONResponse(http.StatusOK, `{"files":[{"id":"unrelated","name":"Archive","kind":"drive#folder"},{"id":"shared","name":"Pack From Shared","kind":"drive#folder"}]}`), nil
		case "shared":
			return pikPakJSONResponse(http.StatusOK, `{"files":[{"id":"restored","name":"PRTD-006.mp4","kind":"drive#file","size":"3690441219"}]}`), nil
		case "unrelated":
			visitedUnrelated = true
			return pikPakJSONResponse(http.StatusInternalServerError, `{"error":"unrelated folder should not be visited"}`), nil
		default:
			t.Fatalf("unexpected parent_id %q", req.URL.Query().Get("parent_id"))
			return nil, nil
		}
	})}
	account := newPikPakClient(client)
	account.accessToken = "access"
	file, newlyRestored, found, err := account.findRestoredFile(context.Background(), "PRTD-006.mp4", 3690441219, map[string]bool{}, false)
	if err != nil || !found || !newlyRestored || file.ID != "restored" {
		t.Fatalf("file=%+v newly=%t found=%t err=%v", file, newlyRestored, found, err)
	}
	if visitedUnrelated {
		t.Fatal("restore lookup scanned an unrelated folder after finding the exact restored file")
	}
}

func TestPikPakRestoreSkipsUnrelatedAccountFoldersBeforeRestore(t *testing.T) {
	restorePosted := false
	visitedUnrelated := false
	client := &http.Client{Transport: pikPakRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path == "/v1/shield/captcha/init" {
			return pikPakJSONResponse(http.StatusOK, `{"captcha_token":"captcha"}`), nil
		}
		switch req.URL.Path {
		case "/drive/v1/share/restore":
			restorePosted = true
			return pikPakJSONResponse(http.StatusOK, `{"restore_status":"RESTORE_COMPLETE"}`), nil
		case "/drive/v1/files":
			switch req.URL.Query().Get("parent_id") {
			case "":
				return pikPakJSONResponse(http.StatusOK, `{"files":[{"id":"archive","name":"Archive","kind":"drive#folder"},{"id":"shared","name":"Pack From Shared","kind":"drive#folder"}]}`), nil
			case "shared":
				if !restorePosted {
					return pikPakJSONResponse(http.StatusOK, `{"files":[]}`), nil
				}
				return pikPakJSONResponse(http.StatusOK, `{"files":[{"id":"restored","name":"TEST-004.mp4","kind":"drive#file","size":"6000"}]}`), nil
			case "archive":
				visitedUnrelated = true
				return pikPakJSONResponse(http.StatusInternalServerError, `{"error":"unrelated folder should not be visited"}`), nil
			}
		}
		t.Fatalf("unexpected PikPak request: %s %s", req.Method, req.URL)
		return nil, nil
	})}
	account := newPikPakClient(client)
	account.accessToken = "access"
	file, newlyRestored, err := account.restoreSharedFile(context.Background(), "share", "shared-file", "TEST-004.mp4", 6000)
	if err != nil || !newlyRestored || file.ID != "restored" {
		t.Fatalf("file=%+v newly=%t err=%v", file, newlyRestored, err)
	}
	if visitedUnrelated {
		t.Fatal("restore scanned an unrelated account folder")
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

func TestValidateJavDBShareReferenceRejectsUnsupportedLinks(t *testing.T) {
	for _, ref := range []string{
		"",
		"   ",
		"magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567&dn=PRPM-002",
		"ftp://keepshare.org/s/abc",
		"https://example.com/not-a-share",
		"https://keepshare.org",
		"https://mypikpak.com/not-a-share-path",
	} {
		if err := validateJavDBShareReference(ref); err == nil {
			t.Fatalf("reference %q: expected an error, got nil", ref)
		}
	}
	for _, ref := range []string{
		"https://keepshare.org/abc123",
		"https://mypikpak.com/s/abc123/def456",
	} {
		if err := validateJavDBShareReference(ref); err != nil {
			t.Fatalf("reference %q: expected no error, got %v", ref, err)
		}
	}
}

// TestJavDBResolveSkipsUnsupportedSourceReferenceWithoutNetworkCalls covers
// the bug where a Download row with no real Keepshare/PikPak share link -
// for example a JavDB release that only ever published a magnet/torrent
// link, or an empty SourceReference left over from a "not available"
// placeholder - reached discoverPikPakShareID's raw http.Client request and
// surfaced as "PikPak resolution failed after 3 attempts: ... unsupported
// protocol scheme". Resolve must now reject it immediately (no PikPak
// requests, no 3-attempt retry loop) with a clear, actionable error, while
// leaving a real share link to resolve normally.
func TestJavDBResolveSkipsUnsupportedSourceReferenceWithoutNetworkCalls(t *testing.T) {
	for _, tc := range []struct {
		name string
		ref  string
	}{
		{"empty", ""},
		{"magnet", "magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567&dn=PRPM-002"},
		{"unrelated host", "https://example.com/whatever"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requests := 0
			client := &http.Client{Transport: pikPakRoundTripFunc(func(r *http.Request) (*http.Response, error) {
				requests++
				return nil, fmt.Errorf("unexpected request to %s", r.URL)
			})}
			provider := &javDBProvider{client: client}
			_, err := provider.Resolve(context.Background(), domain.Download{SourceReference: tc.ref, Query: "PRPM-002"})
			if err == nil {
				t.Fatal("expected an error")
			}
			if requests != 0 {
				t.Fatalf("expected no PikPak requests for an unsupported reference, got %d", requests)
			}
			if strings.Contains(err.Error(), "unsupported protocol scheme") {
				t.Fatalf("expected a clear diagnostic, not the raw transport error: %v", err)
			}
		})
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

// TestGetHTMLDirectWithRetryRecoversFromTransientNetworkFailure covers a
// live report: a JavDB search request failing outright with "context
// deadline exceeded (Client.Timeout exceeded while awaiting headers)" or
// "read tcp ...: connection reset by peer" - a transport-level failure
// (status stays 0; the request never got an HTTP response at all) that a
// retry a moment later usually clears. getHTMLDirectWithRetry now retries
// that case instead of failing the whole search immediately.
func TestGetHTMLDirectWithRetryRecoversFromTransientNetworkFailure(t *testing.T) {
	originalDelay := javDBTransientRetryDelay
	javDBTransientRetryDelay = time.Millisecond
	t.Cleanup(func() { javDBTransientRetryDelay = originalDelay })
	attempts := 0
	client := &http.Client{Transport: pikPakRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		attempts++
		if attempts < javDBTransientNetworkAttempts {
			return nil, errors.New("read tcp 1.2.3.4:1->5.6.7.8:443: read: connection reset by peer")
		}
		return pikPakJSONResponse(http.StatusOK, "<html><body>ok</body></html>"), nil
	})}
	provider := &javDBProvider{client: client}
	doc, status, err := provider.getHTMLDirectWithRetry(context.Background(), "https://javdb.com/search?q=X")
	if err != nil || status != http.StatusOK {
		t.Fatalf("expected eventual success, got status=%d err=%v", status, err)
	}
	if doc == nil {
		t.Fatal("expected a parsed document")
	}
	if attempts != javDBTransientNetworkAttempts {
		t.Fatalf("attempts = %d, want %d", attempts, javDBTransientNetworkAttempts)
	}
}

func TestGetHTMLDirectWithRetryGivesUpAfterMaxAttempts(t *testing.T) {
	originalDelay := javDBTransientRetryDelay
	javDBTransientRetryDelay = time.Millisecond
	t.Cleanup(func() { javDBTransientRetryDelay = originalDelay })
	attempts := 0
	client := &http.Client{Transport: pikPakRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		attempts++
		return nil, errors.New("context deadline exceeded (Client.Timeout exceeded while awaiting headers)")
	})}
	provider := &javDBProvider{client: client}
	_, status, err := provider.getHTMLDirectWithRetry(context.Background(), "https://javdb.com/search?q=X")
	if err == nil {
		t.Fatal("expected an error after exhausting retries")
	}
	if status != 0 {
		t.Fatalf("status = %d, want 0 (no response was ever received)", status)
	}
	if attempts != javDBTransientNetworkAttempts {
		t.Fatalf("attempts = %d, want %d", attempts, javDBTransientNetworkAttempts)
	}
}

// TestGetHTMLDirectWithRetryDoesNotRetryARealHTTPResponse confirms the retry
// is scoped to transport failures only: once JavDB actually answers with an
// HTTP response - even an error one - it is returned as-is, on the first
// attempt, exactly like before this change.
func TestGetHTMLDirectWithRetryDoesNotRetryARealHTTPResponse(t *testing.T) {
	attempts := 0
	client := &http.Client{Transport: pikPakRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		attempts++
		return pikPakJSONResponse(http.StatusNotFound, "not found"), nil
	})}
	provider := &javDBProvider{client: client}
	_, status, err := provider.getHTMLDirectWithRetry(context.Background(), "https://javdb.com/search?q=X")
	if err == nil {
		t.Fatal("expected an error for a 404 response")
	}
	if status != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", status)
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1 (a real HTTP response must not be retried here)", attempts)
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

func TestJavDBSearchReliesOnExactIDInsteadOfReleaseDate(t *testing.T) {
	provider, closeServer := javDBFixtureProvider(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/search":
			_, _ = w.Write([]byte(javDBSearchPage("PRPM-002", "2020-01-01", "/v/exact-old-date")))
		case "/v/exact-old-date":
			_, _ = w.Write([]byte(`<html><body><div>ID: PRPM-002</div><section class="new-download-layout"><a href="https://keepshare.org/share">Get file</a></section></body></html>`))
		default:
			http.NotFound(w, r)
		}
	})
	defer closeServer()
	rows, err := provider.Search(context.Background(), domain.Release{VideoID: "PRPM-002", ReleaseDate: "2026-09-15"})
	if err != nil || len(rows) != 1 || !rows[0].Accepted {
		t.Fatalf("far-apart dates must not reject an exact release ID: rows=%+v err=%v", rows, err)
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
	if rows[0].Accepted || rows[0].ProviderFileID != "" {
		t.Fatalf("failed inspection remained downloadable: %+v", rows[0])
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

func TestJavDBSortingKeepsBlacklistedHTTPCandidatesRejectedAndLast(t *testing.T) {
	rows := []domain.SearchResult{
		{Title: "trusted@ ADN-803-CAMRIP.mp4", BlacklistedFilenameMatch: true, Reason: "filename matched blacklist pattern camrip"},
		{Title: "ADN-803.mp4", Accepted: true},
	}
	sortJavDBDownloadCandidates(rows, "ADN-803", legacyPreferredFilenamePatterns([]string{"trusted@"}))
	if rows[0].Title != "ADN-803.mp4" || !rows[1].BlacklistedFilenameMatch || rows[1].Accepted {
		t.Fatalf("unexpected blacklist order/state: %+v", rows)
	}
	if !strings.Contains(rows[1].Reason, "blacklist") || rows[1].PreferredFilenameMatch {
		t.Fatalf("blacklist reason was overwritten: %+v", rows[1])
	}
}

// TestJavDBSearchInspectsCandidatesConcurrentlyAndReportsProgress covers the
// fix for a live report: a release with many published mirrors (16
// candidates in the wild) left the Search & Download dialog on a static
// "Searching…" for 1-2 minutes, because every candidate's PikPak inspection
// - each its own handful of real network round trips - ran strictly one at
// a time. This builds a fixture release with several candidates, confirms
// they are inspected with real overlap (bounded by
// httpCandidateInspectionConcurrency, not fully serial and not unbounded),
// and confirms HTTPSearchProgress reports the search as active with the
// right total while it runs and clears once Search returns.
func TestJavDBSearchInspectsCandidatesConcurrentlyAndReportsProgress(t *testing.T) {
	const candidateCount = 6
	var detailBody strings.Builder
	detailBody.WriteString("<html><body>ID: MULTI-001")
	for i := 0; i < candidateCount; i++ {
		fmt.Fprintf(&detailBody, `<div class="item"><span class="name">MULTI-001-%d.mp4</span><a href="https://keepshare.org/share-%d">Download</a></div>`, i, i)
	}
	detailBody.WriteString("</body></html>")

	provider, closeServer := javDBFixtureProvider(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/search" {
			_, _ = w.Write([]byte(javDBSearchPage("MULTI-001", "2026-09-15", "/v/multi")))
			return
		}
		_, _ = w.Write([]byte(detailBody.String()))
	})
	defer closeServer()

	release := domain.Release{ID: 4242, VideoID: "MULTI-001", ReleaseDate: "2026-09-15"}
	if _, _, active := HTTPSearchProgress(release.ID); active {
		t.Fatal("progress should not be active before the search starts")
	}

	var (
		mu            sync.Mutex
		inFlight      int
		maxConcurrent int
	)
	provider.inspectCandidate = func(ctx context.Context, link, releaseID string) (pikPakFile, []pikPakFile, error) {
		mu.Lock()
		inFlight++
		if inFlight > maxConcurrent {
			maxConcurrent = inFlight
		}
		mu.Unlock()

		// Hold this "inspection" open briefly so concurrent ones actually
		// overlap, and so there is a window to observe live progress.
		time.Sleep(30 * time.Millisecond)
		if completed, total, active := HTTPSearchProgress(release.ID); !active || total != candidateCount {
			t.Errorf("progress mid-search = completed=%d total=%d active=%v, want total=%d active=true", completed, total, active, candidateCount)
		}

		mu.Lock()
		inFlight--
		mu.Unlock()

		selected := pikPakFile{ID: "video", Name: "4k688.com@" + releaseID + ".mp4", Size: "4294967296"}
		return selected, []pikPakFile{selected}, nil
	}

	rows, err := provider.Search(context.Background(), release)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != candidateCount {
		t.Fatalf("rows = %d, want %d", len(rows), candidateCount)
	}
	if _, _, active := HTTPSearchProgress(release.ID); active {
		t.Fatal("progress should be cleared once the search returns")
	}
	if maxConcurrent < 2 {
		t.Fatalf("max concurrent inspections = %d, want at least 2 (candidates should overlap, not run fully serially)", maxConcurrent)
	}
	if maxConcurrent > httpCandidateInspectionConcurrency {
		t.Fatalf("max concurrent inspections = %d, exceeded the cap of %d", maxConcurrent, httpCandidateInspectionConcurrency)
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

func TestPikPakFolderFallbackUsesPatternPriorityThenLargestVideo(t *testing.T) {
	files := []pikPakFile{
		{ID: "priority-ten", Name: "large@movie.mp4", Size: "9000", FolderReleaseMatch: true},
		{ID: "priority-one-small", Name: "best@movie.mp4", Size: "3000", FolderReleaseMatch: true},
		{ID: "priority-one-large", Name: "best@movie.mkv", Size: "5000", FolderReleaseMatch: true},
		{ID: "outside-folder", Name: "best@outside.mp4", Size: "12000"},
		{ID: "not-video", Name: "best@archive.txt", Size: "15000", FolderReleaseMatch: true},
	}
	patterns := []PreferredFilenamePattern{{Pattern: "large@", Priority: 10}, {Pattern: "best@", Priority: 1}}
	selected, found := selectPikPakFolderFallback(files, patterns)
	if !found || selected.ID != "priority-one-large" {
		t.Fatalf("folder fallback did not honor priority then size: %+v, found=%v", selected, found)
	}
	selected, found = selectPikPakFolderFallback(files, nil)
	if !found || selected.ID != "priority-ten" {
		t.Fatalf("folder fallback did not choose the largest eligible video: %+v, found=%v", selected, found)
	}
}

func TestPikPakFolderScopeMarksOnlyExactReleaseIDFolderChildren(t *testing.T) {
	client := &http.Client{Transport: pikPakRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host == "user.mypikpak.com" {
			return pikPakJSONResponse(http.StatusOK, `{"captcha_token":"captcha"}`), nil
		}
		switch req.URL.Query().Get("parent_id") {
		case "":
			return pikPakJSONResponse(http.StatusOK, `{"share_status":"OK","files":[{"id":"matching","name":"jur_843","kind":"drive#folder"},{"id":"other","name":"OTHER-001","kind":"drive#folder"}]}`), nil
		case "matching":
			return pikPakJSONResponse(http.StatusOK, `{"share_status":"OK","files":[{"id":"inside","name":"generic.mp4","kind":"drive#file","size":"5000"}]}`), nil
		case "other":
			return pikPakJSONResponse(http.StatusOK, `{"share_status":"OK","files":[{"id":"outside","name":"larger.mp4","kind":"drive#file","size":"9000"}]}`), nil
		default:
			t.Fatalf("unexpected parent_id %q", req.URL.Query().Get("parent_id"))
			return nil, nil
		}
	})}
	pp := newPikPakClient(client)
	files, err := pp.listShareFilesScoped(context.Background(), "share", "", "JUR-843", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 || !files[0].FolderReleaseMatch || files[1].FolderReleaseMatch {
		t.Fatalf("unexpected release-folder scope: %+v", files)
	}
	strictFiles, err := pp.listShareFilesScoped(context.Background(), "share", "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if strictFiles[0].FolderReleaseMatch || strictFiles[1].FolderReleaseMatch {
		t.Fatalf("strict traversal marked fallback children: %+v", strictFiles)
	}
}

func TestPikPakFolderFallbackIsNotAcceptedByStrictSelectors(t *testing.T) {
	files := []pikPakFile{{ID: "generic", Name: "movie.mp4", Size: "5000", FolderReleaseMatch: true}}
	if selected, found := selectPikPakFile(files, "JUR-843", nil); found {
		t.Fatalf("strict release-ID selection unexpectedly accepted folder fallback: %+v", selected)
	}
	selected, found := selectPikPakFolderFallback(files, nil)
	if !found || selected.ID != "generic" {
		t.Fatalf("explicit folder fallback did not accept marked child: %+v, found=%v", selected, found)
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

func TestHTTPDestinationPathIsDeterministicWithNoCollisionSuffix(t *testing.T) {
	dir := t.TempDir()
	want := filepath.Join(dir, "ADN-803.mp4")
	if got := httpDestinationPath(dir, "ADN-803"); got != want {
		t.Fatalf("path = %q, want %q", got, want)
	}
	// Even once a file already exists at that path, httpDestinationPath
	// itself never appends a "-0", "-1", ... suffix - runHTTPDownload is
	// responsible for checking existence and skipping the download before
	// this path is used, not this helper.
	if err := os.WriteFile(want, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := httpDestinationPath(dir, "ADN-803"); got != want {
		t.Fatalf("path after collision = %q, want unchanged %q", got, want)
	}
}

package download

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Net005/JAVBeacon/internal/domain"
	"github.com/Net005/JAVBeacon/internal/store"
)

type recordingQB struct{ removed bool }

func (q *recordingQB) Torrents(context.Context) ([]Torrent, error) { return nil, nil }
func (q *recordingQB) Add(context.Context, string, string) (string, error) {
	return "", nil
}
func (q *recordingQB) Remove(context.Context, string) error {
	q.removed = true
	return nil
}

// waitForPipelineRun polls PipelineRun until it reaches a terminal state
// ("completed" or "failed") or the deadline passes. runEventPipelineAsync
// (see service.go) runs the download-completed/download-removed event
// pipeline on a background goroutine rather than blocking the caller that
// triggered it (pollTorrents/cleanupTorrent), so tests that used to be able
// to inspect the run immediately after that call returned now need to wait
// for it to actually finish first.
func waitForPipelineRun(t *testing.T, st store.Store, downloadID int64, trigger string) domain.PipelineRun {
	t.Helper()
	// 10s, not the usual 5s: a timed-out shell step can legitimately take
	// up to its configured timeout plus pipelineKillGrace (5s) to actually
	// finish - see TestRunPipelineEventTimesOutAndRecordsFailure, which
	// exercises exactly that.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		run, err := st.PipelineRun(context.Background(), downloadID, trigger)
		if err == nil && (run.State == "completed" || run.State == "failed") {
			return run
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("timed out waiting for event pipeline run to reach a terminal state")
	return domain.PipelineRun{}
}

// waitForDownloadPostStatus polls a download's own row (via downloadByID's
// "completed"-status lookup, same as runEventPipelineAsync uses to apply a
// finished pipeline's outcome) until PostStatus reaches one of want, or the
// deadline passes. Reaching a terminal PipelineRun state (waitForPipelineRun)
// only proves the pipeline itself finished - runEventPipelineAsync still has
// its own follow-up store round trip afterward to persist PostStatus, which
// this closes the small remaining race against.
func waitForDownloadPostStatus(t *testing.T, service *Service, downloadID int64, want ...string) domain.Download {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		d, ok := service.downloadByID(context.Background(), downloadID, "completed")
		if ok {
			for _, w := range want {
				if d.PostStatus == w {
					return d
				}
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for download %d PostStatus to reach one of %v", downloadID, want)
	return domain.Download{}
}

func TestRenderPipelineTemplateIncludesCompletionContext(t *testing.T) {
	download := domain.Download{ReleaseID: 42, Query: "ABC-123"}
	torrent := Torrent{Hash: "hash-1"}
	result := renderPipelineTemplate(`{{event}}|{{download_path}}|{{release_id}}|{{release_db_id}}|{{torrent_hash}}`, pipelineDownloadCompleted, `/library/A "quote"`, download, torrent)
	for _, expected := range []string{"download_completed", "/library/A", `\"quote\"`, "ABC-123", "42", "hash-1"} {
		if !strings.Contains(result, expected) {
			t.Fatalf("rendered template %q does not contain %q", result, expected)
		}
	}
}

func TestRenderPipelineValueTemplateCanHandPathToNextStep(t *testing.T) {
	download := domain.Download{ReleaseID: 42, Query: "ABC-123"}
	torrent := Torrent{Hash: "hash-1"}
	result := renderPipelineValueTemplate(`/stash/{{release_id}}/{{torrent_hash}}`, pipelineDownloadCompleted, `/downloads/source`, download, torrent)
	if result != "/stash/ABC-123/hash-1" {
		t.Fatalf("rendered output path = %q", result)
	}
}

func TestCleanupRunsSuccessfulRemovalEvent(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "post-removal.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	site, err := st.SaveSite(ctx, domain.Site{Title: "Pipeline", Type: "Site", Name: "JavLibrary", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "PIPE-2", Title: "Removal event", Source: "JavLibrary"})
	if err != nil {
		t.Fatal(err)
	}
	releases, err := st.Releases(ctx, domain.ReleaseFilter{Search: "PIPE-2", Limit: 1})
	if err != nil || len(releases) != 1 {
		t.Fatalf("releases=%+v err=%v", releases, err)
	}
	release := releases[0]
	download, err := st.SaveDownload(ctx, domain.Download{ReleaseID: release.ID, Query: release.VideoID, Status: "completed"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SavePipelineSteps(ctx, []domain.PipelineStep{{Trigger: pipelineDownloadRemoved, Type: "shell", Name: "After removal", Config: []byte(`{"command":"true"}`), Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	qb := &recordingQB{}
	service := New(st, time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	service.cleanupTorrent(ctx, qb, &download, Torrent{Hash: "hash-2", ContentPath: "/downloads/PIPE-2"}, completedTorrentRemove)
	// The torrent removal itself (qb.Remove) and the "completed_removed"
	// PostStatus are set synchronously by cleanupTorrent - only the
	// post-removal pipeline runs in the background (runEventPipelineAsync).
	if !qb.removed || download.PostStatus != "completed_removed" {
		t.Fatalf("removed=%v post_status=%q error=%q", qb.removed, download.PostStatus, download.Error)
	}
	run := waitForPipelineRun(t, st, download.ID, pipelineDownloadRemoved)
	if run.State != "completed" {
		t.Fatalf("post-removal run=%+v", run)
	}
}

// TestTestPipelineStepShellPassAndFail covers the TODO-2.0 "test option for
// each Ordered event pipeline step" feature: a shell step should run against
// synthetic sample values and report pass/fail with output, without needing
// a real download.
func TestTestPipelineStepShellPassAndFail(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "pipeline-test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	service := New(st, time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))

	passing := domain.PipelineStep{
		Name:    "Echo sample values",
		Type:    "shell",
		Trigger: pipelineDownloadCompleted,
		Config:  []byte(`{"command":"echo $JAVBEACON_RELEASE_ID"}`),
		Enabled: true,
	}
	output, err := service.TestPipelineStep(ctx, passing)
	if err != nil {
		t.Fatalf("expected passing test, got error: %v (output=%q)", err, output)
	}
	if !strings.Contains(output, "TEST-001") {
		t.Fatalf("expected output to contain sample release id, got %q", output)
	}

	failing := domain.PipelineStep{
		Name:    "Always fails",
		Type:    "shell",
		Trigger: pipelineDownloadCompleted,
		Config:  []byte(`{"command":"exit 1"}`),
		Enabled: true,
	}
	if _, err := service.TestPipelineStep(ctx, failing); err == nil {
		t.Fatal("expected failing shell step to return an error")
	}

	empty := domain.PipelineStep{Name: "No command", Type: "shell", Config: []byte(`{}`)}
	if _, err := service.TestPipelineStep(ctx, empty); err == nil {
		t.Fatal("expected a shell step with no command to return an error")
	}
}

// TestTestPipelineStepStashRequiresQuery covers the StashApp side of the
// same test-step feature: a step with no query configured should fail fast
// with an actionable error instead of making a network call.
func TestTestPipelineStepStashRequiresQuery(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "pipeline-test-stash.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	service := New(st, time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	empty := domain.PipelineStep{Name: "No query", Type: "stash_graphql", Config: []byte(`{}`)}
	if _, err := service.TestPipelineStep(ctx, empty); err == nil {
		t.Fatal("expected a StashApp step with no query to return an error")
	}
}

// TestPipelineTimeoutDefaultsAndParsesSetting covers the persisted,
// per-ordered-event-pipeline timeout: unset or invalid values fall back to
// the 30s default, and a valid one is honored.
func TestPipelineTimeoutDefaultsAndParsesSetting(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "pipeline-timeout.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	service := New(st, time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))

	if got := service.pipelineTimeout(ctx); got != defaultPipelineTimeout {
		t.Fatalf("with no setting saved, pipelineTimeout() = %v, want default %v", got, defaultPipelineTimeout)
	}
	for _, bad := range []string{"0", "-5", "not-a-number", ""} {
		if err := st.SaveSettings(ctx, map[string]string{"pipeline_timeout_seconds": bad}); err != nil {
			t.Fatal(err)
		}
		if got := service.pipelineTimeout(ctx); got != defaultPipelineTimeout {
			t.Fatalf("pipeline_timeout_seconds=%q: pipelineTimeout() = %v, want default %v", bad, got, defaultPipelineTimeout)
		}
	}
	if err := st.SaveSettings(ctx, map[string]string{"pipeline_timeout_seconds": "90"}); err != nil {
		t.Fatal(err)
	}
	if got, want := service.pipelineTimeout(ctx), 90*time.Second; got != want {
		t.Fatalf("pipeline_timeout_seconds=90: pipelineTimeout() = %v, want %v", got, want)
	}
}

// TestTestPipelineStepTimesOutHungShellCommand covers the actual bug this
// setting fixes: before it existed, a hung shell step (or an unresponsive
// StashApp instance) could block pipeline execution forever. With a short
// configured timeout, a step that sleeps far longer than it should fail
// promptly with a clear timeout error instead of hanging the test (or, in
// production, the poll loop) indefinitely.
func TestTestPipelineStepTimesOutHungShellCommand(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "pipeline-timeout-test-step.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.SaveSettings(ctx, map[string]string{"pipeline_timeout_seconds": "1"}); err != nil {
		t.Fatal(err)
	}
	service := New(st, time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))

	hung := domain.PipelineStep{
		Name:    "Hangs forever",
		Type:    "shell",
		Trigger: pipelineDownloadCompleted,
		Config:  []byte(`{"command":"sleep 30"}`),
		Enabled: true,
	}
	started := time.Now()
	_, err = service.TestPipelineStep(ctx, hung)
	elapsed := time.Since(started)
	if err == nil {
		t.Fatal("expected the hung step to fail once the timeout elapsed")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("expected a timeout error, got: %v", err)
	}
	if elapsed > 10*time.Second {
		t.Fatalf("expected the 1s configured timeout to cut this short, took %v", elapsed)
	}
}

// TestRunPipelineEventTimesOutAndRecordsFailure exercises the same timeout
// through the real download-completion path (runPipelineEvent via
// cleanupTorrent's post-removal pipeline), confirming a hung step is
// recorded as a failed run rather than left "running" forever or losing the
// failure because the timed-out context also blocked saving it.
func TestRunPipelineEventTimesOutAndRecordsFailure(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "pipeline-timeout-run.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.SaveSettings(ctx, map[string]string{"pipeline_timeout_seconds": "1"}); err != nil {
		t.Fatal(err)
	}
	site, err := st.SaveSite(ctx, domain.Site{Title: "Pipeline", Type: "Site", Name: "JavLibrary", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "PIPE-TIMEOUT", Title: "Hung step", Source: "JavLibrary"}); err != nil {
		t.Fatal(err)
	}
	releases, err := st.Releases(ctx, domain.ReleaseFilter{Search: "PIPE-TIMEOUT", Limit: 1})
	if err != nil || len(releases) != 1 {
		t.Fatalf("releases=%+v err=%v", releases, err)
	}
	release := releases[0]
	download, err := st.SaveDownload(ctx, domain.Download{ReleaseID: release.ID, Query: release.VideoID, Status: "completed"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SavePipelineSteps(ctx, []domain.PipelineStep{{Trigger: pipelineDownloadRemoved, Type: "shell", Name: "Hangs forever", Config: []byte(`{"command":"sleep 30"}`), Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	qb := &recordingQB{}
	service := New(st, time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	started := time.Now()
	service.cleanupTorrent(ctx, qb, &download, Torrent{Hash: "hash-timeout", ContentPath: "/downloads/PIPE-TIMEOUT"}, completedTorrentRemove)
	// cleanupTorrent itself returns immediately - the post-removal pipeline
	// (and its timeout) run on the shared worker in the background via
	// runEventPipelineAsync, so this call returning quickly is expected and
	// no longer what proves the timeout was enforced. waitForPipelineRun
	// below, which fails the test if the run never reaches a terminal
	// state, is what actually confirms the hung step didn't hang forever.
	if elapsed := time.Since(started); elapsed > 10*time.Second {
		t.Fatalf("cleanupTorrent should return immediately now that its post-removal pipeline runs in the background, took %v", elapsed)
	}
	run := waitForPipelineRun(t, st, download.ID, pipelineDownloadRemoved)
	if run.State != "failed" || !strings.Contains(run.Error, "timed out") {
		t.Fatalf("post-removal run=%+v, want a failed run recording the timeout", run)
	}
	waitForDownloadPostStatus(t, service, download.ID, "removed_pipeline_failed")
}

// TestPipelineSerializedNeverRunsConcurrently covers "do not launch pipeline
// events multiple times at once": every trigger funnels through
// runPipelineSerialized, so however many fire together, only one pipeline
// job is ever actually executing at a time.
func TestPipelineSerializedNeverRunsConcurrently(t *testing.T) {
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "pipeline-serialized-concurrency.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	service := New(st, time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))

	const jobs = 25
	var active, maxActive int32
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < jobs; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start // fire every goroutine's enqueue attempt at once
			_ = service.runPipelineSerialized(func() error {
				cur := atomic.AddInt32(&active, 1)
				defer atomic.AddInt32(&active, -1)
				for {
					prevMax := atomic.LoadInt32(&maxActive)
					if cur <= prevMax || atomic.CompareAndSwapInt32(&maxActive, prevMax, cur) {
						break
					}
				}
				time.Sleep(2 * time.Millisecond)
				return nil
			})
		}()
	}
	close(start)
	wg.Wait()
	if maxActive != 1 {
		t.Fatalf("observed %d pipeline jobs running concurrently, want at most 1", maxActive)
	}
}

// TestPipelineSerializedPreservesTriggerOrder covers the "in order of when
// triggered" half of the requirement: jobs enqueued one after another run
// in that same order, even though later ones are queued up while an
// earlier one is still executing.
func TestPipelineSerializedPreservesTriggerOrder(t *testing.T) {
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "pipeline-serialized-order.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	service := New(st, time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))

	const jobs = 10
	var mu sync.Mutex
	var order []int
	var wg sync.WaitGroup
	for i := 0; i < jobs; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = service.runPipelineSerialized(func() error {
				mu.Lock()
				order = append(order, i)
				mu.Unlock()
				time.Sleep(time.Millisecond)
				return nil
			})
		}()
		// A short, deterministic gap between enqueues so each job's send
		// onto the shared channel lands well before the next one starts -
		// mirroring how triggers actually arrive one at a time in
		// practice, and letting this test assert order without asserting
		// anything about true simultaneous triggers (which have no
		// meaningful "order" to preserve in the first place).
		time.Sleep(5 * time.Millisecond)
	}
	wg.Wait()
	mu.Lock()
	defer mu.Unlock()
	if len(order) != jobs {
		t.Fatalf("expected all %d jobs to run, got %d: %v", jobs, len(order), order)
	}
	for i, got := range order {
		if got != i {
			t.Fatalf("job execution order = %v, want jobs to run in the order they were triggered (0..%d)", order, jobs-1)
		}
	}
}

// TestPipelineEventContinuesPastFailedStepAndLogsFailure covers the bug
// report that a single failing pipeline step (e.g. a curl call) must not
// abort the rest of that trigger's steps: both steps must run, the failure
// must be logged per-step, and the overall run is reported as failed
// (summarizing the failure) without ever returning early.
func TestPipelineEventContinuesPastFailedStepAndLogsFailure(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "pipeline-continue.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	site, err := st.SaveSite(ctx, domain.Site{Title: "Pipeline", Type: "Site", Name: "JavLibrary", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "PIPE-CONT", Title: "Continue past failure", Source: "JavLibrary"}); err != nil {
		t.Fatal(err)
	}
	releases, err := st.Releases(ctx, domain.ReleaseFilter{Search: "PIPE-CONT", Limit: 1})
	if err != nil || len(releases) != 1 {
		t.Fatalf("releases=%+v err=%v", releases, err)
	}
	download, err := st.SaveDownload(ctx, domain.Download{ReleaseID: releases[0].ID, Query: releases[0].VideoID, Status: "completed"})
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "second-step-ran")
	if err := st.SavePipelineSteps(ctx, []domain.PipelineStep{
		{Trigger: pipelineDownloadCompleted, Type: "shell", Name: "Fails", Config: []byte(`{"command":"exit 1"}`), Enabled: true},
		{Trigger: pipelineDownloadCompleted, Type: "shell", Name: "Runs anyway", Config: []byte(`{"command":"touch ` + marker + `"}`), Enabled: true},
	}); err != nil {
		t.Fatal(err)
	}
	service := New(st, time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	e := service.runPipelineSerialized(func() error {
		return service.runPipelineEvent(ctx, &download, Torrent{Hash: "hash-cont"}, pipelineDownloadCompleted)
	})
	if e == nil {
		t.Fatal("expected an error summarizing the failed step")
	}
	if _, statErr := os.Stat(marker); statErr != nil {
		t.Fatalf("second step did not run after the first failed: %v", statErr)
	}
	run, err := st.PipelineRun(ctx, download.ID, pipelineDownloadCompleted)
	if err != nil || run.State != "failed" || !strings.Contains(run.Error, "Fails") {
		t.Fatalf("run=%+v err=%v, want a failed run naming the failed step", run, err)
	}
	logs, err := st.PipelineLogs(ctx, download.ID)
	if err != nil || len(logs) != 2 {
		t.Fatalf("logs=%+v err=%v, want a log entry for both steps", logs, err)
	}
	var sawFailed, sawCompleted bool
	for _, l := range logs {
		if l.State == "failed" {
			sawFailed = true
		}
		if l.State == "completed" {
			sawCompleted = true
		}
	}
	if !sawFailed || !sawCompleted {
		t.Fatalf("logs=%+v, want one failed and one completed step log", logs)
	}
}

// TestPipelineStepTimeoutOverridesSettingsDefault proves each step's own
// TimeoutSeconds is honored independently, rather than one timeout shared
// across the whole run: a step whose command outlives its own short
// override must fail even though the settings-wide default is generous,
// and a later step relying on that default must still get its own full
// budget rather than whatever was left over from the first step's deadline.
func TestPipelineStepTimeoutOverridesSettingsDefault(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "pipeline-step-timeout.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.SaveSettings(ctx, map[string]string{"pipeline_timeout_seconds": "30"}); err != nil {
		t.Fatal(err)
	}
	site, err := st.SaveSite(ctx, domain.Site{Title: "Pipeline", Type: "Site", Name: "JavLibrary", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "PIPE-TIMEOUT", Title: "Per-step timeout", Source: "JavLibrary"}); err != nil {
		t.Fatal(err)
	}
	releases, err := st.Releases(ctx, domain.ReleaseFilter{Search: "PIPE-TIMEOUT", Limit: 1})
	if err != nil || len(releases) != 1 {
		t.Fatalf("releases=%+v err=%v", releases, err)
	}
	download, err := st.SaveDownload(ctx, domain.Download{ReleaseID: releases[0].ID, Query: releases[0].VideoID, Status: "completed"})
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "second-step-ran")
	if err := st.SavePipelineSteps(ctx, []domain.PipelineStep{
		{Trigger: pipelineDownloadCompleted, Type: "shell", Name: "Slow step with a short override", Config: []byte(`{"command":"sleep 2"}`), Enabled: true, TimeoutSeconds: 1},
		{Trigger: pipelineDownloadCompleted, Type: "shell", Name: "Uses the settings default", Config: []byte(`{"command":"touch ` + marker + `"}`), Enabled: true},
	}); err != nil {
		t.Fatal(err)
	}
	service := New(st, time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	e := service.runPipelineSerialized(func() error {
		return service.runPipelineEvent(ctx, &download, Torrent{Hash: "hash-timeout"}, pipelineDownloadCompleted)
	})
	if e == nil {
		t.Fatal("expected an error summarizing the timed-out step")
	}
	if !strings.Contains(e.Error(), "Slow step with a short override") {
		t.Fatalf("error = %v, want it to name the timed-out step", e)
	}
	if _, statErr := os.Stat(marker); statErr != nil {
		t.Fatalf("second step did not run after the first timed out: %v", statErr)
	}
	logs, err := st.PipelineLogs(ctx, download.ID)
	if err != nil || len(logs) != 2 {
		t.Fatalf("logs=%+v err=%v, want a log entry for both steps", logs, err)
	}
	var timedOutLog domain.PipelineLog
	for _, l := range logs {
		if l.State == "failed" {
			timedOutLog = l
		}
	}
	if !strings.Contains(timedOutLog.Error, "timed out after 1s") {
		t.Fatalf("timed-out step log error = %q, want it to cite its own 1s override, not the 30s settings default", timedOutLog.Error)
	}
}

// fakeQBittorrentServer stands in for a real qBittorrent instance across the
// HTTP surface pollTorrents actually talks to (NewQB, not the narrower
// QBittorrent interface, since pollTorrents also calls QBClient.Files).
// torrents is mutated by the test to simulate qBittorrent's own state
// (e.g. a torrent disappearing because it was deleted outside this app).
type fakeQBittorrentServer struct {
	mu       sync.Mutex
	torrents []Torrent
	removed  []string
}

func newFakeQBittorrentServer(t *testing.T) (*httptest.Server, *fakeQBittorrentServer) {
	t.Helper()
	f := &fakeQBittorrentServer{}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/auth/login", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("Ok."))
	})
	mux.HandleFunc("/api/v2/torrents/info", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(f.torrents)
	})
	mux.HandleFunc("/api/v2/torrents/files", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]TorrentFile{})
	})
	mux.HandleFunc("/api/v2/torrents/delete", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		f.mu.Lock()
		defer f.mu.Unlock()
		hash := r.FormValue("hashes")
		f.removed = append(f.removed, hash)
		kept := f.torrents[:0]
		for _, tr := range f.torrents {
			if tr.Hash != hash {
				kept = append(kept, tr)
			}
		}
		f.torrents = kept
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server, f
}

// TestPollTorrentsRemovesByRatioDespiteFailedCompletionPipelineStep is the
// core regression test for the bug report: a "download_completed" pipeline
// with a failing step (a curl call erroring, standing in as any shell/
// StashApp step) must not block ratio-based qBittorrent removal once the
// configured seed ratio is met.
func TestPollTorrentsRemovesByRatioDespiteFailedCompletionPipelineStep(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "poll-ratio-despite-failure.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	server, fake := newFakeQBittorrentServer(t)
	fake.torrents = []Torrent{{Hash: "hash-ratio", Name: "4k688.com@RATIO-1", State: "stalledUP", Ratio: 2.0}}

	if err := st.SaveSettings(ctx, map[string]string{
		"qb_url": server.URL, "qb_completed_action": "remove_at_ratio", "minimum_seed_ratio": "1.0",
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.SavePipelineSteps(ctx, []domain.PipelineStep{
		{Trigger: pipelineDownloadCompleted, Type: "shell", Name: "Broken curl call", Config: []byte(`{"command":"exit 1"}`), Enabled: true},
		{Trigger: pipelineDownloadCompleted, Type: "shell", Name: "Unrelated step", Config: []byte(`{"command":"true"}`), Enabled: true},
	}); err != nil {
		t.Fatal(err)
	}
	site, err := st.SaveSite(ctx, domain.Site{Title: "Pipeline", Type: "Site", Name: "JavLibrary", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "RATIO-1", Title: "Ratio despite failure", Source: "JavLibrary"}); err != nil {
		t.Fatal(err)
	}
	releases, err := st.Releases(ctx, domain.ReleaseFilter{Search: "RATIO-1", Limit: 1})
	if err != nil || len(releases) != 1 {
		t.Fatalf("releases=%+v err=%v", releases, err)
	}
	download, err := st.SaveDownload(ctx, domain.Download{ReleaseID: releases[0].ID, Query: releases[0].VideoID, Status: "completed"})
	if err != nil {
		t.Fatal(err)
	}

	service := New(st, 2*time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	// First tick: the download_completed pipeline has never run for this
	// row, so pollTorrents kicks it off in the background
	// (runEventPipelineAsync) and returns immediately rather than blocking
	// on it - this is the whole point of the fix this test now also
	// covers, so ratio-based removal itself can't happen on this same
	// tick. Wait for that pipeline (with its failing step) to actually
	// finish, then poll again so pollDownload's rule-based cleanup
	// decision - the thing this test is really about - gets to run.
	service.pollTorrents(ctx)
	run := waitForPipelineRun(t, st, download.ID, pipelineDownloadCompleted)
	if run.State != "failed" {
		t.Fatalf("run=%+v, want the download_completed run itself recorded as failed for troubleshooting", run)
	}
	service.pollTorrents(ctx)

	fake.mu.Lock()
	removedCount := len(fake.removed)
	fake.mu.Unlock()
	if removedCount != 1 {
		t.Fatalf("expected qBittorrent Remove to be called once despite the failed pipeline step, got %d calls", removedCount)
	}
	waitForDownloadPostStatus(t, service, download.ID, "completed_removed")
}

// TestPollTorrentsDoesNotBlockOnSlowCompletionPipeline is the regression
// test for the actual bug report this fix addresses: qBittorrent showing a
// torrent as fully seeded/complete while Download Activity kept showing
// stale pre-completion progress, for far longer than the poll interval,
// because pollTorrents used to block the entire tick - and therefore every
// other download's status update - on a slow or hung completion pipeline
// step. It asserts two things a fully synchronous implementation could not
// satisfy at the same time: pollTorrents returns quickly even though the
// completion pipeline step sleeps well past that, and the freshly-polled
// qBittorrent fields (Progress/Status) are persisted immediately rather
// than waiting on the pipeline to finish first.
func TestPollTorrentsDoesNotBlockOnSlowCompletionPipeline(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "poll-does-not-block.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	server, fake := newFakeQBittorrentServer(t)
	fake.torrents = []Torrent{{Hash: "hash-slow", Name: "4k688.com@SLOW-1", State: "stalledUP", Progress: 1, Ratio: 0.1}}

	if err := st.SaveSettings(ctx, map[string]string{"qb_url": server.URL, "qb_completed_action": "keep"}); err != nil {
		t.Fatal(err)
	}
	if err := st.SavePipelineSteps(ctx, []domain.PipelineStep{
		{Trigger: pipelineDownloadCompleted, Type: "shell", Name: "Slow post-processing", Config: []byte(`{"command":"sleep 3"}`), Enabled: true},
	}); err != nil {
		t.Fatal(err)
	}
	site, err := st.SaveSite(ctx, domain.Site{Title: "Pipeline", Type: "Site", Name: "JavLibrary", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "SLOW-1", Title: "Slow completion pipeline", Source: "JavLibrary"}); err != nil {
		t.Fatal(err)
	}
	releases, err := st.Releases(ctx, domain.ReleaseFilter{Search: "SLOW-1", Limit: 1})
	if err != nil || len(releases) != 1 {
		t.Fatalf("releases=%+v err=%v", releases, err)
	}
	download, err := st.SaveDownload(ctx, domain.Download{ReleaseID: releases[0].ID, Query: releases[0].VideoID, Status: "downloading", Progress: 0.16})
	if err != nil {
		t.Fatal(err)
	}

	service := New(st, 2*time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	started := time.Now()
	service.pollTorrents(ctx)
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("pollTorrents took %v - a completion pipeline step must run in the background, not block the poll tick", elapsed)
	}
	rows, err := st.Downloads(ctx, "completed")
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows=%+v err=%v, want the freshly-completed row persisted immediately even though its pipeline is still running", rows, err)
	}
	if rows[0].Progress != 1 || rows[0].PostStatus != "processing" {
		t.Fatalf("progress=%v post_status=%q, want progress persisted immediately and post_status \"processing\" while the background pipeline runs", rows[0].Progress, rows[0].PostStatus)
	}
	run := waitForPipelineRun(t, st, download.ID, pipelineDownloadCompleted)
	if run.State != "completed" {
		t.Fatalf("run=%+v", run)
	}
	// The rule-based "keep/wait-for-ratio/remove" decision (this test uses
	// "keep") only runs once the pipeline run is terminal - like the
	// ratio-despite-failure test above, that's a second tick's work now
	// that the pipeline itself no longer runs inline on the first one.
	service.pollTorrents(ctx)
	waitForDownloadPostStatus(t, service, download.ID, "completed_retained")
}

// TestPollTorrentsRemovesFromActivityWhenTorrentDisappearsForUnknownReason
// covers "if release is no longer available in QBittorrent (manually
// removed for instance) do not re-schedule, remove it from Download
// Activity and register it as state (removed - unknown reason)": once a
// torrent this app previously confirmed (by hash) is no longer present in
// qBittorrent's own torrent list, and this app did not remove it itself,
// the download must move to the terminal statusRemoved/
// postStatusRemovedUnknown state - which drops it out of every Download
// Activity tab (none of which query status "removed") - rather than being
// left stuck forever under its last known status (or endlessly retried).
func TestPollTorrentsRemovesFromActivityWhenTorrentDisappearsForUnknownReason(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "poll-removed-unknown.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	server, fake := newFakeQBittorrentServer(t)
	fake.torrents = nil // the torrent is already gone by the time this poll runs

	if err := st.SaveSettings(ctx, map[string]string{"qb_url": server.URL}); err != nil {
		t.Fatal(err)
	}
	site, err := st.SaveSite(ctx, domain.Site{Title: "Pipeline", Type: "Site", Name: "JavLibrary", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "GONE-1", Title: "Vanished from qBittorrent", Source: "JavLibrary"}); err != nil {
		t.Fatal(err)
	}
	releases, err := st.Releases(ctx, domain.ReleaseFilter{Search: "GONE-1", Limit: 1})
	if err != nil || len(releases) != 1 {
		t.Fatalf("releases=%+v err=%v", releases, err)
	}
	// A hash previously confirmed present (as if an earlier poll matched it)
	// but waiting on seed ratio when someone deleted it directly in qBittorrent.
	download, err := st.SaveDownload(ctx, domain.Download{ReleaseID: releases[0].ID, Query: releases[0].VideoID, Status: "completed", TorrentHash: "hash-gone", PostStatus: "completed_waiting_ratio"})
	if err != nil {
		t.Fatal(err)
	}

	service := New(st, 2*time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	service.pollTorrents(ctx)

	// The row must no longer show up under any Download Activity tab status.
	if row, err := st.Downloads(ctx, "completed"); err != nil || len(row) != 0 {
		t.Fatalf("rows=%+v err=%v, want the row gone from the completed tab", row, err)
	}
	row, err := st.Downloads(ctx, "removed")
	if err != nil || len(row) != 1 {
		t.Fatalf("rows=%+v err=%v", row, err)
	}
	if row[0].Status != "removed" {
		t.Fatalf("status=%q, want removed", row[0].Status)
	}
	if row[0].PostStatus != "removed_unknown_reason" {
		t.Fatalf("post_status=%q, want removed_unknown_reason", row[0].PostStatus)
	}

	// A further poll must not "rediscover" or re-flag anything - it's a
	// terminal state, not a retryable failure.
	service.pollTorrents(ctx)
	row, err = st.Downloads(ctx, "removed")
	if err != nil || len(row) != 1 || row[0].Status != "removed" || row[0].PostStatus != "removed_unknown_reason" {
		t.Fatalf("rows=%+v err=%v, want the removed state to remain stable across polls", row, err)
	}
	_ = download
}

// TestPollTorrentsDoesNotFlagFreshDownloadAsRemoved guards against a false
// positive: a brand new download that has never yet been matched to a live
// torrent (TorrentHash still empty, e.g. qBittorrent hasn't resolved the
// magnet/metadata yet) must not be mistaken for a removal just because it
// doesn't match anything on its first poll.
func TestPollTorrentsDoesNotFlagFreshDownloadAsRemoved(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "poll-fresh-download.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	server, fake := newFakeQBittorrentServer(t)
	fake.torrents = nil // qBittorrent has nothing matching yet

	if err := st.SaveSettings(ctx, map[string]string{"qb_url": server.URL}); err != nil {
		t.Fatal(err)
	}
	site, err := st.SaveSite(ctx, domain.Site{Title: "Pipeline", Type: "Site", Name: "JavLibrary", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "FRESH-1", Title: "Not yet in qBittorrent", Source: "JavLibrary"}); err != nil {
		t.Fatal(err)
	}
	releases, err := st.Releases(ctx, domain.ReleaseFilter{Search: "FRESH-1", Limit: 1})
	if err != nil || len(releases) != 1 {
		t.Fatalf("releases=%+v err=%v", releases, err)
	}
	if _, err := st.SaveDownload(ctx, domain.Download{ReleaseID: releases[0].ID, Query: releases[0].VideoID, Status: "downloading"}); err != nil {
		t.Fatal(err)
	}

	service := New(st, 2*time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	service.pollTorrents(ctx)

	rows, err := st.Downloads(ctx, "downloading")
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}
	if rows[0].PostStatus == "removed_unknown_reason" {
		t.Fatalf("a never-matched fresh download must not be flagged removed: %+v", rows[0])
	}
}

// TestPollTorrentsIgnoresNameCollisionOnceHashIsKnown is the core regression
// test for "monitoring the wrong download": once a download's torrent hash
// is known, a *different* torrent that happens to share matching text in its
// name (e.g. a different release whose video ID is a substring/superset of
// this one's) must never be treated as a match - even though the old
// name-substring fallback used to do exactly that, silently re-pointing this
// download at the unrelated torrent's live progress and hash. With the real
// torrent (hash-real) gone and only the unrelated look-alike torrent left,
// the download must be recognized as truly gone, not "matched" to the
// look-alike.
func TestPollTorrentsIgnoresNameCollisionOnceHashIsKnown(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "poll-name-collision.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	server, fake := newFakeQBittorrentServer(t)
	// A different torrent, for a different release, whose name happens to
	// contain this download's query text as a substring - e.g. ABC-100 vs
	// ABC-1. It is NOT the torrent this download was matched to.
	fake.torrents = []Torrent{{Hash: "hash-lookalike", Name: "Unrelated.Release.ABC-100.1080p", State: "downloading", Progress: 0.42, Seeds: 5}}

	if err := st.SaveSettings(ctx, map[string]string{"qb_url": server.URL}); err != nil {
		t.Fatal(err)
	}
	site, err := st.SaveSite(ctx, domain.Site{Title: "Pipeline", Type: "Site", Name: "JavLibrary", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: "ABC-1", Title: "Real release", Source: "JavLibrary"}); err != nil {
		t.Fatal(err)
	}
	releases, err := st.Releases(ctx, domain.ReleaseFilter{Search: "ABC-1", Limit: 1})
	if err != nil || len(releases) != 1 {
		t.Fatalf("releases=%+v err=%v", releases, err)
	}
	// This download's own torrent (hash-real) has already been removed from
	// qBittorrent by the time this poll runs - only the look-alike remains.
	if _, err := st.SaveDownload(ctx, domain.Download{ReleaseID: releases[0].ID, Query: releases[0].VideoID, Status: "downloading", TorrentHash: "hash-real", Progress: 0.9, Seeds: 3}); err != nil {
		t.Fatal(err)
	}

	service := New(st, 2*time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	service.pollTorrents(ctx)

	// Must NOT still show as "downloading" with the look-alike's live
	// progress/hash grafted on - it must be recognized as removed instead.
	if row, err := st.Downloads(ctx, "downloading"); err != nil || len(row) != 0 {
		t.Fatalf("rows=%+v err=%v, want no row left under downloading (must not adopt the look-alike torrent)", row, err)
	}
	row, err := st.Downloads(ctx, "removed")
	if err != nil || len(row) != 1 {
		t.Fatalf("rows=%+v err=%v", row, err)
	}
	if row[0].TorrentHash != "hash-real" {
		t.Fatalf("torrent_hash=%q, want the original hash-real left untouched (not overwritten with the look-alike's hash)", row[0].TorrentHash)
	}
	if row[0].PostStatus != "removed_unknown_reason" {
		t.Fatalf("post_status=%q, want removed_unknown_reason", row[0].PostStatus)
	}
}

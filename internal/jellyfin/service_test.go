package jellyfin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Net005/JAVBeacon/internal/domain"
	"github.com/Net005/JAVBeacon/internal/screenshots"
	"github.com/Net005/JAVBeacon/internal/stash"
	"github.com/Net005/JAVBeacon/internal/store"
)

type fakeStash struct {
	saves          []struct{ resume, duration float64 }
	plays          int
	os             int
	failNextPlay   bool
	sceneMeta      stash.StashSceneMetadata
	sceneMetaErr   error
	sceneMetaCalls int
	sceneMetaDelay time.Duration
}

func (f *fakeStash) SaveJellyfinActivity(_ context.Context, _ string, resume, duration float64) error {
	f.saves = append(f.saves, struct{ resume, duration float64 }{resume, duration})
	return nil
}
func (f *fakeStash) AddJellyfinPlay(context.Context, string, time.Time) (int, error) {
	if f.failNextPlay {
		f.failNextPlay = false
		return f.plays, errors.New("temporary Stash failure")
	}
	f.plays++
	return f.plays, nil
}
func (f *fakeStash) AddJellyfinO(context.Context, string, time.Time) (int, error) {
	f.os++
	return f.os, nil
}
func (f *fakeStash) JellyfinActivity(context.Context, string) (stash.JellyfinActivity, error) {
	return stash.JellyfinActivity{OCount: f.os, PlayCount: f.plays}, nil
}
func (f *fakeStash) StashSceneMetadata(ctx context.Context, _ string) (stash.StashSceneMetadata, error) {
	f.sceneMetaCalls++
	if f.sceneMetaDelay > 0 {
		select {
		case <-time.After(f.sceneMetaDelay):
		case <-ctx.Done():
			return stash.StashSceneMetadata{}, ctx.Err()
		}
	}
	if f.sceneMetaErr != nil || f.sceneMeta.Performers != nil {
		return f.sceneMeta, f.sceneMetaErr
	}
	return stash.StashSceneMetadata{}, errors.New("not configured in this test")
}

func testService(t *testing.T) (*Service, *store.SQLite, *fakeStash, domain.Release) {
	t.Helper()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "jellyfin.db"))
	if err != nil {
		t.Fatal(err)
	}
	site, err := st.SaveSite(context.Background(), domain.Site{Title: "Test", Name: "Test", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.UpsertRelease(context.Background(), domain.Release{SiteID: site.ID, VideoID: "ABC-123", Title: "ABC-123 — A title", Source: "Test", Duration: "100 min", Story: "Old story field", Actresses: []string{"One"}, Genres: []string{"Tag"}})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := st.Releases(context.Background(), domain.ReleaseFilter{Search: "ABC-123", Limit: 1})
	if err != nil || len(rows) != 1 {
		t.Fatalf("release lookup: %v %+v", err, rows)
	}
	r := rows[0]
	if err = st.SetStashState(context.Background(), r.ID, true, "stash-1"); err != nil {
		t.Fatal(err)
	}
	if err = st.SetStashFilePath(context.Background(), r.ID, "/media/ABC-123.mp4"); err != nil {
		t.Fatal(err)
	}
	r, _ = st.Release(context.Background(), r.ID)
	bridge := &fakeStash{}
	return newService(st, bridge), st, bridge, r
}

func TestMetadataPopulatesPerformerImagesFromStashByName(t *testing.T) {
	svc, st, bridge, r := testService(t)
	defer st.Close()
	bridge.sceneMeta = stash.StashSceneMetadata{
		Performers: []stash.StashPerformer{
			{ID: "p1", Name: "One", ImagePath: "/performer/p1/image"},
			{ID: "p2", Name: "No Photo", ImagePath: ""},
		},
	}
	m, err := svc.Metadata(context.Background(), r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := m.PerformerImages["One"]; got != "/api/v1/integrations/performers/p1/image" {
		t.Fatalf("PerformerImages[One] = %q", got)
	}
	if _, ok := m.PerformerImages["No Photo"]; ok {
		t.Fatalf("performer with no image path should not appear: %+v", m.PerformerImages)
	}
	// Search must never pay for this - it's Metadata-only, same policy as
	// CollectionNames, to keep bulk search cheap.
	rows, err := svc.Search(context.Background(), "ABC-123", 10)
	if err != nil || len(rows) != 1 {
		t.Fatalf("search: %v %+v", err, rows)
	}
	if rows[0].PerformerImages != nil {
		t.Fatalf("Search must not populate PerformerImages: %+v", rows[0].PerformerImages)
	}
}

// TestMetadataFetchesStashSceneOnlyOnce guards against Metadata() regressing
// back to two separate Stash round trips (one for text/image gap-fill, one
// for performer images) - the exact shape of the bug that made a Silo
// "Apply Match" click fail with a gRPC deadline-exceeded error even though
// both JAVBeacon and Stash were reachable.
func TestMetadataFetchesStashSceneOnlyOnce(t *testing.T) {
	svc, st, bridge, r := testService(t)
	defer st.Close()
	bridge.sceneMeta = stash.StashSceneMetadata{
		Performers: []stash.StashPerformer{{ID: "p1", Name: "One", ImagePath: "/performer/p1/image"}},
	}
	if _, err := svc.Metadata(context.Background(), r.ID); err != nil {
		t.Fatal(err)
	}
	if bridge.sceneMetaCalls != 1 {
		t.Fatalf("StashSceneMetadata called %d times, want exactly 1", bridge.sceneMetaCalls)
	}
}

// TestMetadataBoundsSlowStashLookup guards against a slow or stuck Stash
// server hanging Metadata() past its own caller's budget - confirmed live: a
// Silo "Apply Match" click failed with "context deadline exceeded" from
// Metadata()'s Stash calls running unbounded on the incoming request's
// context. Metadata() must return (with degraded, Stash-less data) well
// within stashLookupTimeout even when the caller's own context has no
// deadline and Stash never responds in time.
func TestMetadataBoundsSlowStashLookup(t *testing.T) {
	svc, st, bridge, r := testService(t)
	defer st.Close()
	bridge.sceneMetaDelay = stashLookupTimeout + 5*time.Second

	start := time.Now()
	m, err := svc.Metadata(context.Background(), r.ID)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("Metadata should degrade gracefully rather than error: %v", err)
	}
	if elapsed > stashLookupTimeout+2*time.Second {
		t.Fatalf("Metadata took %s, want it bounded near stashLookupTimeout (%s)", elapsed, stashLookupTimeout)
	}
	if m.PerformerImages != nil {
		t.Fatalf("expected no performer images from a timed-out Stash lookup: %+v", m.PerformerImages)
	}
}

func TestCollectionNamesForReleaseReflectsFilterPresetMembership(t *testing.T) {
	svc, st, _, r := testService(t)
	defer st.Close()
	watchlist := true
	if err := st.PatchRelease(context.Background(), r.ID, nil, nil, nil, nil, &watchlist, nil, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveUser(context.Background(), "admin", "hash"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SaveFilterPreset(context.Background(), domain.FilterPreset{Name: "My Watchlist", State: json.RawMessage(`{"watchlist":true}`)}); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveSettings(context.Background(), map[string]string{"jellyfin_library_revision": "revision-1"}); err != nil {
		t.Fatal(err)
	}
	m, err := svc.Metadata(context.Background(), r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.CollectionNames) != 1 || m.CollectionNames[0] != "My Watchlist" {
		t.Fatalf("CollectionNames=%+v, want [My Watchlist]", m.CollectionNames)
	}
}

// TestCollectionMembershipIndexCachesUntilRevisionChanges guards against
// collectionNamesForRelease regressing back to a full per-preset library scan
// on every single Metadata() call - confirmed live as the real cause of
// Metadata() timing out under load (a Silo "Apply Match" click failing with a
// gRPC deadline error, and Jellyfin's own HttpClient.Timeout tripping during
// a full library scan): with several saved presets and a library of several
// thousand releases, that per-request cost alone was enough to blow both
// budgets. The index must now be rebuilt only when jellyfin_library_revision
// moves, and reused as-is otherwise.
func TestCollectionMembershipIndexCachesUntilRevisionChanges(t *testing.T) {
	svc, st, _, _ := testService(t)
	defer st.Close()
	if err := st.SaveUser(context.Background(), "admin", "hash"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SaveFilterPreset(context.Background(), domain.FilterPreset{Name: "Everything", State: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveSettings(context.Background(), map[string]string{"jellyfin_library_revision": "revision-1"}); err != nil {
		t.Fatal(err)
	}

	idx1, err := svc.collectionMembershipIndex(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	idx2, err := svc.collectionMembershipIndex(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprintf("%p", idx1) != fmt.Sprintf("%p", idx2) {
		t.Fatal("expected the same cached index instance while the revision is unchanged")
	}

	if err := st.SaveSettings(context.Background(), map[string]string{"jellyfin_library_revision": "revision-2"}); err != nil {
		t.Fatal(err)
	}
	idx3, err := svc.collectionMembershipIndex(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprintf("%p", idx1) == fmt.Sprintf("%p", idx3) {
		t.Fatal("expected a freshly rebuilt index instance after the revision changed")
	}
}

func TestMatchPrefersExactPathAndReturnsPersistentIDs(t *testing.T) {
	svc, st, _, r := testService(t)
	defer st.Close()
	result, err := svc.Match(context.Background(), "/MEDIA/abc-123.MP4", "")
	if err != nil || !result.Matched || result.MatchMethod != "path" {
		t.Fatalf("match=%+v err=%v", result, err)
	}
	if result.Release.ProviderIDs[ProviderIDRelease] == "" || result.Release.ProviderIDs[ProviderIDStash] != "stash-1" || result.Release.ReleaseID != r.ID {
		t.Fatalf("metadata=%+v", result.Release)
	}
	if result.Release.RuntimeSeconds != 6000 {
		t.Fatalf("runtime=%d", result.Release.RuntimeSeconds)
	}
	if result.Release.Title != "ABC-123" || result.Release.OriginalTitle != "ABC-123" || result.Release.Overview != "A title" {
		t.Fatalf("Jellyfin title mapping=%+v", result.Release)
	}
}

func TestMetadataReturnsOnlyCachedJAVBeaconScreenshots(t *testing.T) {
	svc, st, _, r := testService(t)
	defer st.Close()
	r.Screenshots = []string{"https://source.invalid/one.jpg", "https://source.invalid/two.jpg"}
	if _, err := st.UpsertRelease(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	cache, err := screenshots.New(t.TempDir(), time.Second, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(cache.Path(r.VideoID, 1)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cache.Path(r.VideoID, 1), []byte("cached image"), 0o644); err != nil {
		t.Fatal(err)
	}
	svc.shots = cache
	value, err := svc.Metadata(context.Background(), r.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("/screenshots/%d/1", r.ID)
	if len(value.BackdropURLs) != 1 || value.BackdropURLs[0] != want {
		t.Fatalf("backdrops=%v", value.BackdropURLs)
	}
}

func TestReleaseTitleTrimsOnlyACompleteLeadingReleaseID(t *testing.T) {
	for _, test := range []struct{ title, want string }{
		{"ABC-123 A title", "A title"},
		{"abc-123: A title", "A title"},
		{"ABC-123", ""},
		{"ABC-1234 different release", "ABC-1234 different release"},
		{"Already trimmed", "Already trimmed"},
	} {
		if got := releaseTitle("ABC-123", test.title); got != test.want {
			t.Errorf("releaseTitle(%q)=%q, want %q", test.title, got, test.want)
		}
	}
}

func TestMatchSupportsJellyfinToStashPathRemaps(t *testing.T) {
	svc, st, _, r := testService(t)
	defer st.Close()
	if err := st.SaveSettings(context.Background(), map[string]string{"jellyfin_path_remaps": `[{"from":"/jellyfin/jav","to":"/media"}]`}); err != nil {
		t.Fatal(err)
	}
	result, err := svc.Match(context.Background(), "/jellyfin/jav/ABC-123.mp4", "")
	if err != nil || !result.Matched || result.MatchMethod != "remapped_path" || result.Release.ReleaseID != r.ID {
		t.Fatalf("match=%+v err=%v", result, err)
	}
}

func TestLibrarySyncReturnsOnlyLocalWatchlistItems(t *testing.T) {
	svc, st, _, r := testService(t)
	defer st.Close()
	watchlist := true
	if err := st.PatchRelease(context.Background(), r.ID, nil, nil, nil, nil, &watchlist, nil, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveSettings(context.Background(), map[string]string{"jellyfin_library_revision": "revision-1"}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := svc.LibrarySync(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Revision != "revision-1" || len(snapshot.Watchlist) != 1 || snapshot.Watchlist[0].ReleaseID != r.ID || snapshot.Watchlist[0].StashSceneID != "stash-1" || snapshot.Watchlist[0].WatchlistedAt.IsZero() {
		t.Fatalf("snapshot=%+v", snapshot)
	}
}

func TestLibrarySyncReportsStashPlayedReleasesAsWatchedIndependentlyOfWatchlist(t *testing.T) {
	svc, st, _, r := testService(t)
	defer st.Close()
	// Deliberately not on the Watchlist - watched status must not depend on
	// it (StashApp playback and the Watchlist tag are unrelated concepts).
	lastPlayed := "2026-09-08T12:30:00Z"
	if err := st.SetStashPlaybackStats(context.Background(), r.ID, 0, 3, lastPlayed, ""); err != nil {
		t.Fatal(err)
	}
	snapshot, err := svc.LibrarySync(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Watchlist) != 0 {
		t.Fatalf("watchlist=%+v, want empty (release was never watchlisted)", snapshot.Watchlist)
	}
	if len(snapshot.Watched) != 1 || snapshot.Watched[0].ReleaseID != r.ID || snapshot.Watched[0].StashSceneID != "stash-1" {
		t.Fatalf("watched=%+v", snapshot.Watched)
	}
	wantWatchedAt, _ := time.Parse(time.RFC3339, lastPlayed)
	if !snapshot.Watched[0].WatchedAt.Equal(wantWatchedAt) {
		t.Fatalf("watched_at=%v, want %v", snapshot.Watched[0].WatchedAt, wantWatchedAt)
	}
}

func TestLibrarySyncOmitsUnplayedReleasesFromWatched(t *testing.T) {
	svc, st, _, _ := testService(t)
	defer st.Close()
	snapshot, err := svc.LibrarySync(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Watched) != 0 {
		t.Fatalf("watched=%+v, want empty (play_count is 0)", snapshot.Watched)
	}
}

func TestPlaybackUsesWallTimeCheckpointsAndCountsCompletionOnce(t *testing.T) {
	svc, st, bridge, r := testService(t)
	defer st.Close()
	start := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	base := PlaybackEvent{SessionID: "play-1", ReleaseID: r.ID, JellyfinItemID: "item", JellyfinUserID: "user", RuntimeSeconds: 1000, OccurredAt: start}
	base.Event = "start"
	if _, err := svc.Playback(context.Background(), base); err != nil {
		t.Fatal(err)
	}
	base.Event, base.PositionSeconds, base.OccurredAt = "progress", 35, start.Add(35*time.Second)
	result, err := svc.Playback(context.Background(), base)
	if err != nil {
		t.Fatal(err)
	}
	if !result.CheckpointWritten || result.Forwarded != 35 {
		t.Fatalf("checkpoint=%+v", result)
	}
	// A seek close to the end completes the play but adds only elapsed wall time.
	base.PositionSeconds, base.OccurredAt = 995, start.Add(40*time.Second)
	result, err = svc.Playback(context.Background(), base)
	if err != nil {
		t.Fatal(err)
	}
	if !result.PlayCounted || result.Accumulated != 40 || bridge.plays != 1 {
		t.Fatalf("completion=%+v plays=%d", result, bridge.plays)
	}
	base.Event, base.OccurredAt = "stop", start.Add(45*time.Second)
	if _, err = svc.Playback(context.Background(), base); err != nil {
		t.Fatal(err)
	}
	if bridge.plays != 1 {
		t.Fatalf("play counted %d times", bridge.plays)
	}
	if len(bridge.saves) != 3 || bridge.saves[1].duration != 35 || bridge.saves[2].duration != 10 {
		t.Fatalf("saves=%+v", bridge.saves)
	}
}

func TestPlaybackDoesNotRepeatDurationWhenPlayMutationRetries(t *testing.T) {
	svc, st, bridge, r := testService(t)
	defer st.Close()
	start := time.Date(2026, 9, 8, 14, 0, 0, 0, time.UTC)
	event := PlaybackEvent{Event: "start", SessionID: "retry-1", ReleaseID: r.ID, RuntimeSeconds: 1000, OccurredAt: start}
	if _, err := svc.Playback(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	bridge.failNextPlay = true
	event.Event, event.PositionSeconds, event.OccurredAt = "progress", 995, start.Add(40*time.Second)
	if _, err := svc.Playback(context.Background(), event); err == nil {
		t.Fatal("expected temporary play mutation failure")
	}
	event.Event, event.OccurredAt = "stop", start.Add(45*time.Second)
	result, err := svc.Playback(context.Background(), event)
	if err != nil {
		t.Fatal(err)
	}
	if !result.PlayCounted || bridge.plays != 1 {
		t.Fatalf("result=%+v plays=%d", result, bridge.plays)
	}
	if len(bridge.saves) != 3 || bridge.saves[1].duration != 40 || bridge.saves[2].duration != 5 {
		t.Fatalf("duration was replayed: %+v", bridge.saves)
	}
}

func TestPlaybackIgnoresOutOfOrderCallbacks(t *testing.T) {
	svc, st, bridge, r := testService(t)
	defer st.Close()
	start := time.Date(2026, 9, 8, 15, 0, 0, 0, time.UTC)
	event := PlaybackEvent{Event: "progress", SessionID: "reordered-1", ReleaseID: r.ID, RuntimeSeconds: 1000, PositionSeconds: 40, OccurredAt: start.Add(40 * time.Second)}
	if _, err := svc.Playback(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	event.Event, event.PositionSeconds, event.OccurredAt = "start", 0, start
	result, err := svc.Playback(context.Background(), event)
	if err != nil {
		t.Fatal(err)
	}
	if result.ResumeTime != 40 || result.CheckpointWritten || len(bridge.saves) != 0 {
		t.Fatalf("stale callback changed session: result=%+v saves=%+v", result, bridge.saves)
	}
	event.Event, event.PositionSeconds, event.OccurredAt = "progress", 75, start.Add(75*time.Second)
	result, err = svc.Playback(context.Background(), event)
	if err != nil {
		t.Fatal(err)
	}
	if result.Accumulated != 35 || result.Forwarded != 35 || len(bridge.saves) != 1 {
		t.Fatalf("progress after reorder=%+v saves=%+v", result, bridge.saves)
	}
}

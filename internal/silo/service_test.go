package silo

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Net005/JAVBeacon/internal/domain"
	"github.com/Net005/JAVBeacon/internal/stash"
	"github.com/Net005/JAVBeacon/internal/store"
)

type fakeStash struct {
	sceneMeta           stash.StashSceneMetadata
	siloScenes          []stash.SiloScene
	siloScene           stash.SiloScene
	watchlistScenes     map[string]time.Time
	watchlistConfigured bool
	sceneMetaErr        error
	sceneMetaCalls      int
	saves               []struct{ resume, duration float64 }
	plays               int
	failNextPlay        bool
}

func (f *fakeStash) StashSceneMetadata(_ context.Context, _ string) (stash.StashSceneMetadata, error) {
	f.sceneMetaCalls++
	if f.sceneMetaErr != nil || f.sceneMeta.Performers != nil {
		return f.sceneMeta, f.sceneMetaErr
	}
	return stash.StashSceneMetadata{}, errors.New("not configured in this test")
}

func (f *fakeStash) SearchSiloScenes(context.Context, string) ([]stash.SiloScene, error) {
	return f.siloScenes, nil
}
func (f *fakeStash) SiloSceneByID(context.Context, string) (stash.SiloScene, error) {
	if f.siloScene.ID != "" {
		return f.siloScene, nil
	}
	return stash.SiloScene{}, errors.New("not configured")
}
func (f *fakeStash) SiloWatchlistScenes(context.Context) (map[string]time.Time, bool, error) {
	return f.watchlistScenes, f.watchlistConfigured, nil
}

func (f *fakeStash) SaveActivity(_ context.Context, _ string, resume, duration float64) error {
	f.saves = append(f.saves, struct{ resume, duration float64 }{resume, duration})
	return nil
}

func (f *fakeStash) AddPlay(context.Context, string, time.Time) (int, error) {
	if f.failNextPlay {
		f.failNextPlay = false
		return f.plays, errors.New("temporary Stash failure")
	}
	f.plays++
	return f.plays, nil
}

func testService(t *testing.T) (*Service, *store.SQLite, *fakeStash, domain.Release) {
	t.Helper()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "silo.db"))
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

// TestMetadataPopulatesPerformerImagesFromStashByName guards this package's
// deliberate duplicate of internal/jellyfin.applyPerformerImages, including
// the reversed-word-order fallback (JAVBeacon's "Hamasaki Mao" vs. StashApp's
// own "Mao Hamasaki" for the same person) and its collision safety (two
// performers whose real names happen to be exact reverses of each other).
// Unlike internal/jellyfin.Metadata, this package's Metadata has no
// PerformerIDs field (Silo's GetPersonDetail never uses it), so only
// PerformerImages is asserted here.
func TestMetadataPopulatesPerformerImagesFromStashByName(t *testing.T) {
	svc, st, bridge, r := testService(t)
	defer st.Close()
	bridge.sceneMeta = stash.StashSceneMetadata{
		Performers: []stash.StashPerformer{
			{ID: "p1", Name: "One", ImagePath: "/performer/p1/image"},
			{ID: "p2", Name: "No Photo", ImagePath: ""},
			{ID: "p3", Name: "Mao Hamasaki", ImagePath: "/performer/p3/image", Birthdate: "1980-01-01"},
			{ID: "p4", Name: "Ai Yuki", ImagePath: "/performer/p4/image"},
			{ID: "p5", Name: "Yuki Ai", ImagePath: "/performer/p5/image"},
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
	if got := m.PerformerImages["Hamasaki Mao"]; got != "/api/v1/integrations/performers/p3/image" {
		t.Fatalf("PerformerImages[Hamasaki Mao] (reversed) = %q", got)
	}
	if got := m.PerformerImages["Mao Hamasaki"]; got != "/api/v1/integrations/performers/p3/image" {
		t.Fatalf("PerformerImages[Mao Hamasaki] = %q", got)
	}
	if got := m.PerformerDetails["Hamasaki Mao"]; got.StashID != "p3" || got.Birthdate != "1980-01-01" {
		t.Fatalf("PerformerDetails[Hamasaki Mao] = %+v", got)
	}
	// Coincidental-reversal collision: each of the two real, distinct
	// performers must keep only its own image, never the other's.
	if got := m.PerformerImages["Ai Yuki"]; got != "/api/v1/integrations/performers/p4/image" {
		t.Fatalf("PerformerImages[Ai Yuki] = %q", got)
	}
	if got := m.PerformerImages["Yuki Ai"]; got != "/api/v1/integrations/performers/p5/image" {
		t.Fatalf("PerformerImages[Yuki Ai] = %q", got)
	}
}

// TestSearchNeverCallsStash guards the exact fix that resolved Silo's
// "days to match a library" bug: Search must be pure DB lookups, with zero
// per-row Stash enrichment.
func TestSearchNeverCallsStash(t *testing.T) {
	svc, st, bridge, _ := testService(t)
	defer st.Close()
	bridge.sceneMeta = stash.StashSceneMetadata{Performers: []stash.StashPerformer{{ID: "p1", Name: "One", ImagePath: "/x"}}}
	before := bridge.sceneMetaCalls
	rows, err := svc.Search(context.Background(), "ABC-123", 10)
	if err != nil || len(rows) != 1 {
		t.Fatalf("search: %v %+v", err, rows)
	}
	if bridge.sceneMetaCalls != before {
		t.Fatalf("Search called Stash %d times, want 0", bridge.sceneMetaCalls-before)
	}
}

// TestMetadataReflectsWatchlistState guards the Silo "Watchlist" genre/tag:
// Metadata.Watchlist must mirror domain.Release.Watchlist and (unlike
// CollectionNames/PerformerImages) cost nothing extra, so it is populated
// everywhere, including Search.
func TestSearchOnlyReturnsLocalStashLinkedReleases(t *testing.T) {
	svc, st, _, _ := testService(t)
	defer st.Close()
	site, err := st.SaveSite(context.Background(), domain.Site{Title: "Other", Name: "Other", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpsertRelease(context.Background(), domain.Release{SiteID: site.ID, VideoID: "REMOTE-123", Title: "REMOTE-123"}); err != nil {
		t.Fatal(err)
	}
	rows, err := svc.Search(context.Background(), "REMOTE-123", 10)
	if err != nil || len(rows) != 0 {
		t.Fatalf("nonlocal release leaked into Silo search: %+v err=%v", rows, err)
	}
	rows, err = svc.Search(context.Background(), "ABC-123", 10)
	if err != nil || len(rows) != 1 || rows[0].StashSceneID != "stash-1" {
		t.Fatalf("local Stash release missing: %+v err=%v", rows, err)
	}
}

func TestSearchCodeMissDoesNotFallBackToBroadText(t *testing.T) {
	svc, st, _, _ := testService(t)
	defer st.Close()
	// This code occurs in the title but is not the release's own code.
	// Returning it would waste a broad scan and cannot be safely matched.
	rows, err := svc.Search(context.Background(), "123", 10)
	if err != nil || len(rows) != 1 {
		t.Fatalf("plain text search: %v %+v", err, rows)
	}
	rows, err = svc.Search(context.Background(), "XYZ-123", 10)
	if err != nil || len(rows) != 0 {
		t.Fatalf("code miss must stay exact: %v %+v", err, rows)
	}
}

func TestMetadataReflectsWatchlistState(t *testing.T) {
	svc, st, _, r := testService(t)
	defer st.Close()
	m, err := svc.Metadata(context.Background(), r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if m.Watchlist {
		t.Fatalf("Watchlist=%v, want false before being added", m.Watchlist)
	}
	watchlist := true
	if err := st.PatchRelease(context.Background(), r.ID, nil, nil, nil, nil, &watchlist, nil, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	m, err = svc.Metadata(context.Background(), r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !m.Watchlist {
		t.Fatal("Watchlist=false, want true after being added")
	}
	rows, err := svc.Search(context.Background(), "ABC-123", 10)
	if err != nil || len(rows) != 1 {
		t.Fatalf("search: %v %+v", err, rows)
	}
	if !rows[0].Watchlist {
		t.Fatal("Search must also reflect Watchlist state")
	}
}

// TestLibrarySyncFilterPresetsNeverNil guards the same JSON-null hazard fixed
// on the Jellyfin side: FilterPresets must serialize as [] with zero saved
// presets, not null, even though this package's own consumer (the Silo
// plugin's Go decoder) tolerates a JSON null fine - keeping the wire shape
// identical between the two integrations is the point.
type failPresetReleaseStore struct {
	store.Store
	fail bool
}

func (s *failPresetReleaseStore) Releases(ctx context.Context, filter domain.ReleaseFilter) ([]domain.Release, error) {
	if filter.StashLinked && s.fail {
		s.fail = false
		return nil, errors.New("transient preset lookup failure")
	}
	return s.Store.Releases(ctx, filter)
}

func TestLibrarySyncRetriesFailedPresetIndexInsteadOfCachingEmpty(t *testing.T) {
	svc, st, _, release := testService(t)
	defer st.Close()
	if err := st.SaveUser(context.Background(), "admin", "hash"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SaveFilterPreset(context.Background(), domain.FilterPreset{Name: "Local", State: json.RawMessage(`{"watchlist":false}`)}); err != nil {
		t.Fatal(err)
	}
	wrapped := &failPresetReleaseStore{Store: st, fail: true}
	svc.store = wrapped
	if _, err := svc.LibrarySync(context.Background()); err == nil {
		t.Fatal("partial preset index should return an error")
	}
	snapshot, err := svc.LibrarySync(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.FilterPresets) != 1 || len(snapshot.FilterPresets[0].ReleaseIDs) != 1 || snapshot.FilterPresets[0].ReleaseIDs[0] != release.ID {
		t.Fatalf("retry omitted saved-filter membership: %+v", snapshot.FilterPresets)
	}
	if snapshot.ReleaseCodes[release.ID] != release.VideoID {
		t.Fatalf("snapshot release code = %q, want %q", snapshot.ReleaseCodes[release.ID], release.VideoID)
	}
}

func TestSiloWatchlistUsesStashAndNewestUpdateFirst(t *testing.T) {
	svc, st, bridge, release := testService(t)
	defer st.Close()
	newest := time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC)
	older := newest.Add(-time.Hour)
	bridge.watchlistConfigured = true
	bridge.watchlistScenes = map[string]time.Time{"stash-1": older, "stash-only": newest}
	snapshot, err := svc.LibrarySync(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Watchlist) != 2 || snapshot.Watchlist[0].StashSceneID != "stash-only" || snapshot.Watchlist[0].ReleaseID != 0 || snapshot.Watchlist[1].ReleaseID != release.ID || !snapshot.Watchlist[1].WatchlistedAt.Equal(older) {
		t.Fatalf("Watchlist must follow Stash update order: %+v", snapshot.Watchlist)
	}
	oldRevision := snapshot.Revision
	delete(bridge.watchlistScenes, "stash-1")
	snapshot, err = svc.LibrarySync(context.Background())
	if err != nil || snapshot.Revision == oldRevision {
		t.Fatalf("Stash Watchlist change did not update revision: %+v err=%v", snapshot, err)
	}
}

func TestSiloMetadataUsesStashTagOverJAVWatchlist(t *testing.T) {
	svc, st, bridge, release := testService(t)
	defer st.Close()
	if err := st.SaveSettings(context.Background(), map[string]string{"stash_watchlist_tag_id": "watch-tag"}); err != nil {
		t.Fatal(err)
	}
	bridge.sceneMeta = stash.StashSceneMetadata{TagIDs: []string{"watch-tag"}, Performers: []stash.StashPerformer{}}
	m, err := svc.Metadata(context.Background(), release.ID)
	if err != nil || !m.Watchlist {
		t.Fatalf("Stash tag was not primary: %+v err=%v", m, err)
	}
	watchlist := true
	if err := st.PatchRelease(context.Background(), release.ID, nil, nil, nil, nil, &watchlist, nil, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	bridge.sceneMeta.TagIDs = nil
	m, err = svc.Metadata(context.Background(), release.ID)
	if err != nil || m.Watchlist {
		t.Fatalf("removed Stash tag must override stale JAV watchlist: %+v err=%v", m, err)
	}
	bridge.siloScene = stash.SiloScene{ID: "stash-only", Code: "X-1", TagIDs: []string{"watch-tag"}}
	m, err = svc.StashMetadata(context.Background(), "stash-only")
	if err != nil || !m.Watchlist {
		t.Fatalf("Stash-only Watchlist tag missing: %+v err=%v", m, err)
	}
}

func TestLibrarySyncFilterPresetsNeverNil(t *testing.T) {
	svc, st, _, _ := testService(t)
	defer st.Close()
	snapshot, err := svc.LibrarySync(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.FilterPresets == nil {
		t.Fatal("FilterPresets is nil, want an empty (non-nil) slice")
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"filter_presets":null`) {
		t.Fatalf("FilterPresets serialized as null: %s", raw)
	}
}

// TestMetadataSkipsUnsanitizableFilterPresetTag mirrors
// internal/jellyfin's identical guard: a saved filter set named entirely
// with control characters must be skipped for tag purposes, never crash or
// add a blank genre entry.
func TestMetadataSkipsUnsanitizableFilterPresetTag(t *testing.T) {
	svc, st, _, r := testService(t)
	defer st.Close()
	if err := st.SaveUser(context.Background(), "admin", "hash"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SaveFilterPreset(context.Background(), domain.FilterPreset{Name: "\x00\x01\x02", State: json.RawMessage(`{"watchlist":false}`)}); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveSettings(context.Background(), map[string]string{"jellyfin_library_revision": "revision-1"}); err != nil {
		t.Fatal(err)
	}
	m, err := svc.Metadata(context.Background(), r.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range m.CollectionNames {
		if name == "" {
			t.Fatalf("CollectionNames contains an empty entry: %+v", m.CollectionNames)
		}
	}
}

// The three tests below are direct ports of
// internal/jellyfin's TestPlaybackUsesWallTimeCheckpointsAndCountsCompletionOnce/
// TestPlaybackDoesNotRepeatDurationWhenPlayMutationRetries/
// TestPlaybackIgnoresOutOfOrderCallbacks, guarding that this package's own,
// independently persisted (domain.SiloPlaybackSession) Playback engine
// behaves identically to Jellyfin's rather than merely compiling.

func TestPlaybackUsesWallTimeCheckpointsAndCountsCompletionOnce(t *testing.T) {
	svc, st, bridge, r := testService(t)
	defer st.Close()
	start := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	base := PlaybackEvent{SessionID: "play-1", ReleaseID: r.ID, SiloItemID: "item", SiloUserID: "user", RuntimeSeconds: 1000, OccurredAt: start}
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

func TestBroadLocalJAVHitDoesNotHideExactStashFilename(t *testing.T) {
	svc, st, bridge, _ := testService(t)
	defer st.Close()
	// The local JAV release has this text in its title, but a different
	// code. The Stash scene has an exact filename match.
	if _, err := st.DB().ExecContext(context.Background(), `UPDATE releases SET title='alternate-filename title' WHERE video_id='ABC-123'`); err != nil {
		t.Fatal(err)
	}
	bridge.siloScenes = []stash.SiloScene{{ID: "stash-only", Code: "alternate-filename", Title: "Scene"}}
	rows, err := svc.SearchWithStashFallback(context.Background(), "alternate-filename", 10)
	if err != nil || len(rows) != 1 || rows[0].ProviderID != "stash:stash-only" {
		t.Fatalf("exact Stash filename lost to broad JAV hit: %+v err=%v", rows, err)
	}
}

func TestNonlocalJAVReleaseDoesNotBlockStashFilenameFallback(t *testing.T) {
	svc, st, bridge, _ := testService(t)
	defer st.Close()
	site, err := st.SaveSite(context.Background(), domain.Site{Title: "Other", Name: "Other", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpsertRelease(context.Background(), domain.Release{SiteID: site.ID, VideoID: "REMOTE-123", Title: "REMOTE-123"}); err != nil {
		t.Fatal(err)
	}
	bridge.siloScenes = []stash.SiloScene{{ID: "local-scene", Code: "REMOTE-123", Title: "Local scene"}}
	catalog, err := svc.Search(context.Background(), "REMOTE-123", 10)
	if err != nil || len(catalog) != 0 {
		t.Fatalf("nonlocal catalog result blocked fallback: %+v err=%v", catalog, err)
	}
	fallback, err := svc.SearchStashScenes(context.Background(), "REMOTE-123")
	if err != nil || len(fallback) != 1 || fallback[0].ProviderID != "stash:local-scene" {
		t.Fatalf("Stash filename fallback missing: %+v err=%v", fallback, err)
	}
}

func TestStashFilenamePrefersLinkedLocalRelease(t *testing.T) {
	svc, st, bridge, release := testService(t)
	defer st.Close()
	bridge.siloScenes = []stash.SiloScene{{ID: "stash-1", Code: "alternate-filename", Title: "Scene"}}
	rows, err := svc.SearchStashScenes(context.Background(), "alternate-filename")
	if err != nil || len(rows) != 1 || rows[0].ReleaseID != release.ID || rows[0].Code != "alternate-filename" {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}
}

func TestStashOnlyFilenameFallback(t *testing.T) {
	svc, st, bridge, _ := testService(t)
	defer st.Close()
	bridge.siloScenes = []stash.SiloScene{{ID: "11631", Code: "ad-359", Title: "Scene"}}
	rows, err := svc.SearchStashScenes(context.Background(), "AD-359")
	if err != nil || len(rows) != 1 || rows[0].ProviderID != "stash:11631" || rows[0].Code != "ad-359" {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}
	bridge.siloScenes = append(bridge.siloScenes, stash.SiloScene{ID: "2", Code: "ad-359"})
	rows, err = svc.SearchStashScenes(context.Background(), "AD-359")
	if err != nil || len(rows) != 1 || rows[0].ProviderID != "stash:11631" {
		t.Fatalf("richer Stash candidate not selected: rows=%+v err=%v", rows, err)
	}
}

func TestBestStashSceneFavorsPlaybackAndRetainsTrueTies(t *testing.T) {
	plain := stash.SiloScene{ID: "plain", Code: "ANIX-01", Title: "ANIX-01"}
	rich := stash.SiloScene{ID: "rich", Code: "ANIX-01", Title: "ANIX-01", Details: "Detailed story", Date: "2024-01-01", Studio: "Studio", Tags: []string{"Drama"}, ScreenshotURL: "/screenshot.jpg"}
	played := stash.SiloScene{ID: "played", Code: "ANIX-01", Title: "ANIX-01", Details: "Detailed story", PlayCount: 3, OCounter: 2, LastPlayedAt: "2026-09-27T10:00:00Z"}
	if best, ok := bestStashScene([]stash.SiloScene{plain, rich, played}); !ok || best.ID != "played" {
		t.Fatalf("best=%+v ok=%v, want played", best, ok)
	}
	if _, ok := bestStashScene([]stash.SiloScene{plain, {ID: "other", Code: "ANIX-01", Title: "ANIX-01"}}); ok {
		t.Fatal("equal candidates must remain ambiguous")
	}
	older := stash.SiloScene{ID: "older", Title: "ANIX-01", PlayCount: 1, LastPlayedAt: "2026-09-20T10:00:00Z"}
	newer := stash.SiloScene{ID: "newer", Title: "ANIX-01", PlayCount: 1, LastPlayedAt: "2026-09-27T10:00:00Z"}
	if best, ok := bestStashScene([]stash.SiloScene{older, newer}); !ok || best.ID != "newer" {
		t.Fatalf("most recently played tie-break: best=%+v ok=%v", best, ok)
	}
}

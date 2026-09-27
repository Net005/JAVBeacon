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
	sceneMeta      stash.StashSceneMetadata
	sceneMetaErr   error
	sceneMetaCalls int
	saves          []struct{ resume, duration float64 }
	plays          int
	failNextPlay   bool
}

func (f *fakeStash) StashSceneMetadata(_ context.Context, _ string) (stash.StashSceneMetadata, error) {
	f.sceneMetaCalls++
	if f.sceneMetaErr != nil || f.sceneMeta.Performers != nil {
		return f.sceneMeta, f.sceneMetaErr
	}
	return stash.StashSceneMetadata{}, errors.New("not configured in this test")
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
			{ID: "p3", Name: "Mao Hamasaki", ImagePath: "/performer/p3/image"},
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

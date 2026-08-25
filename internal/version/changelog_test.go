package version

import (
	"context"
	"reflect"
	"testing"
)

type memorySettings struct {
	values map[string]string
}

func (m *memorySettings) Settings(context.Context) (map[string]string, error) {
	out := make(map[string]string, len(m.values))
	for key, value := range m.values {
		out[key] = value
	}
	return out, nil
}

func (m *memorySettings) SaveSettings(_ context.Context, values map[string]string) error {
	if m.values == nil {
		m.values = map[string]string{}
	}
	for key, value := range values {
		m.values[key] = value
	}
	return nil
}

func useVersion(t *testing.T, value string) {
	t.Helper()
	previous := Value
	Value = value
	t.Cleanup(func() { Value = previous })
}

func TestNotesBetweenIncludesEveryNewerRelease(t *testing.T) {
	notes := NotesBetween("v1.0.5", "v1.0.7")
	var versions []string
	for _, note := range notes {
		versions = append(versions, note.Version)
	}
	if want := []string{"1.0.7", "1.0.6"}; !reflect.DeepEqual(versions, want) {
		t.Fatalf("versions = %v, want %v", versions, want)
	}
	if len(notes[0].Sections) == 0 || len(notes[0].Sections[0].Changes) == 0 {
		t.Fatal("release notes did not include parsed changelog sections")
	}
}

func TestFreshInstallEstablishesBaselineWithoutPendingNotes(t *testing.T) {
	useVersion(t, "v1.0.7")
	store := &memorySettings{}
	if err := InitializeTracking(context.Background(), store, true); err != nil {
		t.Fatal(err)
	}
	if store.values[installedVersionKey] != "1.0.7" {
		t.Fatalf("installed version = %q", store.values[installedVersionKey])
	}
	change, err := PendingChange(context.Background(), store)
	if err != nil {
		t.Fatal(err)
	}
	if change.Available {
		t.Fatalf("fresh install unexpectedly has pending notes: %+v", change)
	}
}

func TestUpgradeCreatesAndAcknowledgesOnePendingTransition(t *testing.T) {
	useVersion(t, "v1.0.7")
	store := &memorySettings{values: map[string]string{installedVersionKey: "1.0.5"}}
	if err := InitializeTracking(context.Background(), store, false); err != nil {
		t.Fatal(err)
	}
	change, err := PendingChange(context.Background(), store)
	if err != nil {
		t.Fatal(err)
	}
	if !change.Available || change.From != "v1.0.5" || change.To != "v1.0.7" || len(change.Releases) != 2 {
		t.Fatalf("pending change = %+v", change)
	}
	if acknowledged, err := AcknowledgeChange(context.Background(), store, "v1.0.4", "v1.0.7"); err != nil || acknowledged {
		t.Fatalf("stale acknowledgement = %v, %v", acknowledged, err)
	}
	if acknowledged, err := AcknowledgeChange(context.Background(), store, change.From, change.To); err != nil || !acknowledged {
		t.Fatalf("acknowledgement = %v, %v", acknowledged, err)
	}
	change, err = PendingChange(context.Background(), store)
	if err != nil || change.Available {
		t.Fatalf("acknowledged change still pending: %+v, %v", change, err)
	}
	if err := InitializeTracking(context.Background(), store, false); err != nil {
		t.Fatal(err)
	}
	change, _ = PendingChange(context.Background(), store)
	if change.Available {
		t.Fatalf("same version was shown twice: %+v", change)
	}
}

func TestExistingInstallWithoutTrackingUsesPreviousRelease(t *testing.T) {
	useVersion(t, "v1.0.7")
	store := &memorySettings{values: map[string]string{"page_limit": "5"}}
	if err := InitializeTracking(context.Background(), store, false); err != nil {
		t.Fatal(err)
	}
	change, err := PendingChange(context.Background(), store)
	if err != nil {
		t.Fatal(err)
	}
	if !change.Available || change.From != "v1.0.6" || change.To != "v1.0.7" {
		t.Fatalf("bootstrap transition = %+v", change)
	}
}

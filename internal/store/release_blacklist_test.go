package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/Net005/JAVBeacon/internal/domain"
)

func verifyReleaseBlacklistPersistence(t *testing.T, st Store) {
	t.Helper()
	ctx := context.Background()
	keep := "https://keepshare.org/s/synthetic-blacklist-test"
	pik := "https://mypikpak.com/s/SyntheticBlacklistTest"
	if err := st.DeleteBlacklistedRelease(ctx, keep); err != nil {
		t.Fatal(err)
	}
	if err := st.RememberReleaseSourceAliases(ctx, keep, []string{pik}); err != nil {
		t.Fatal(err)
	}
	first := domain.BlacklistedRelease{URL: keep, VideoID: "SYNTH-BLACKLIST", Title: "Synthetic", Reason: "Video too short", DurationSeconds: 599, SizeBytes: 200_000_000, Transport: "http"}
	if err := st.SaveBlacklistedRelease(ctx, first); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveBlacklistedRelease(ctx, domain.BlacklistedRelease{URL: keep, Reason: "Later observation"}); err != nil {
		t.Fatal(err)
	}
	entries, total, err := st.BlacklistedReleases(ctx, "SYNTH-BLACKLIST", 1, 0)
	if err != nil || total != 2 || len(entries) != 1 || entries[0].DurationSeconds != 599 || entries[0].Reason != "Video too short" {
		t.Fatalf("list/idempotence: %+v %d %v", entries, total, err)
	}
	for _, url := range []string{keep, pik + "/file?act=play"} {
		e, blocked, err := st.BlacklistedRelease(ctx, url)
		if err != nil || !blocked || e.Reason != "Video too short" {
			t.Fatalf("alias: %+v %v %v", e, blocked, err)
		}
	}
	if err = st.DeleteBlacklistedRelease(ctx, pik); err != nil {
		t.Fatal(err)
	}
	for _, url := range []string{keep, pik} {
		if _, blocked, err := st.BlacklistedRelease(ctx, url); err != nil || blocked {
			t.Fatalf("alias removal: %v %v", blocked, err)
		}
	}
}

func TestSQLiteReleaseBlacklistAliases(t *testing.T) {
	st, err := OpenSQLite(filepath.Join(t.TempDir(), "blacklist.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	verifyReleaseBlacklistPersistence(t, st)
}

func TestPostgresReleaseBlacklistAliases(t *testing.T) {
	cfg := testPostgresConfig(t)
	st, err := OpenPostgresStore(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	verifyReleaseBlacklistPersistence(t, st)
}

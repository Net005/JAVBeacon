package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestSourceRejectionsScopedPersistentAndExpiring(t *testing.T) {
	ctx := context.Background()
	p := filepath.Join(t.TempDir(), "reject.db")
	s, err := OpenSQLite(p)
	if err != nil {
		t.Fatal(err)
	}
	a := "https://keepshare.example/a"
	b := "https://mypikpak.com/s/EXAMPLE/FILE?act=play"
	if err = s.RememberReleaseSourceAliases(ctx, a, []string{b}); err != nil {
		t.Fatal(err)
	}
	if err = s.SaveSourceRejection(ctx, "SYNTH-101", a, "no matching file", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = OpenSQLite(p)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, ref := range []string{a, b, "https://mypikpak.com/s/EXAMPLE"} {
		reason, err := s.SourceRejection(ctx, "SYNTH-101", ref)
		if err != nil || reason != "no matching file" {
			t.Fatalf("%s %q %v", ref, reason, err)
		}
	}
	reason, err := s.SourceRejection(ctx, "SYNTH-102", a)
	if err != nil || reason != "" {
		t.Fatalf("cross-release rejection %q %v", reason, err)
	}
	if err = s.SaveSourceRejection(ctx, "SYNTH-101", a, "expired", time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	reason, err = s.SourceRejection(ctx, "SYNTH-101", a)
	if err != nil || reason != "" {
		t.Fatalf("expired rejection %q %v", reason, err)
	}
}

func TestPostgresSourceRejections(t *testing.T) {
	ctx := context.Background()
	st, err := OpenPostgresStore(ctx, testPostgresConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	a := "https://keepshare.example/pg-" + time.Now().Format("150405.000000000")
	if err = st.RememberReleaseSourceAliases(ctx, a, []string{a + "-alias"}); err != nil {
		t.Fatal(err)
	}
	if err = st.SaveSourceRejection(ctx, "SYNTH-101", a, "no matching file", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{a, a + "-alias"} {
		if reason, err := st.SourceRejection(ctx, "synth-101", ref); err != nil || reason != "no matching file" {
			t.Fatalf("%s: %q %v", ref, reason, err)
		}
	}
	if reason, err := st.SourceRejection(ctx, "SYNTH-102", a); err != nil || reason != "" {
		t.Fatalf("cross-release rejection %q %v", reason, err)
	}
	if err = st.SaveSourceRejection(ctx, "SYNTH-101", a, "expired", time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if reason, err := st.SourceRejection(ctx, "SYNTH-101", a); err != nil || reason != "" {
		t.Fatalf("expired rejection %q %v", reason, err)
	}
}

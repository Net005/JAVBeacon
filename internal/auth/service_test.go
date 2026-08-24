package auth

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/Net005/JAVBeacon/internal/store"
)

func TestLoginSessionAndLogout(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "auth.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	s := New(st)
	if err := s.EnsureUser(ctx, "owner", "correct-horse"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Login(ctx, "owner", "wrong", time.Hour); err == nil {
		t.Fatal("wrong password was accepted")
	}
	session, err := s.Login(ctx, "owner", "correct-horse", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !s.Valid(ctx, session.Token) {
		t.Fatal("created session is not valid")
	}
	if err := s.Logout(ctx, session.Token); err != nil {
		t.Fatal(err)
	}
	if s.Valid(ctx, session.Token) {
		t.Fatal("logged out session is still valid")
	}
}

func TestDefaultLifetimeIsThirtyDays(t *testing.T) {
	if DefaultLifetime != 30*24*time.Hour {
		t.Fatalf("default lifetime = %v", DefaultLifetime)
	}
}

func TestChangeCredentialsRequiresCurrentPassword(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "credentials.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	s := New(st)
	if err := s.EnsureUser(ctx, "owner", "correct-horse"); err != nil {
		t.Fatal(err)
	}
	if err := s.Change(ctx, "wrong", "new-owner", "new-password"); err == nil {
		t.Fatal("credential change accepted the wrong current password")
	}
	if err := s.Change(ctx, "correct-horse", "new-owner", "new-password"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Login(ctx, "new-owner", "new-password", time.Hour); err != nil {
		t.Fatalf("changed credentials do not work: %v", err)
	}
}

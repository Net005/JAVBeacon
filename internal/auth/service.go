package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"strings"
	"time"

	"github.com/Net005/JAVBeacon/internal/domain"
	"github.com/Net005/JAVBeacon/internal/store"
	"golang.org/x/crypto/bcrypt"
)

const DefaultLifetime = 30 * 24 * time.Hour

type Service struct{ store store.Store }

func New(st store.Store) *Service { return &Service{store: st} }

func (s *Service) EnsureUser(ctx context.Context, username, password string) error {
	if _, err := s.store.User(ctx); err == nil {
		return nil
	}
	return s.Reset(ctx, username, password)
}

func (s *Service) Reset(ctx context.Context, username, password string) error {
	username = strings.TrimSpace(username)
	if username == "" || len(password) < 8 {
		return errors.New("username and password of at least 8 characters are required")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	return s.store.SaveUser(ctx, username, string(hash))
}

func (s *Service) Change(ctx context.Context, currentPassword, username, newPassword string) error {
	u, err := s.store.User(ctx)
	if err != nil || bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(currentPassword)) != nil {
		return errors.New("current password is incorrect")
	}
	username = strings.TrimSpace(username)
	if username == "" {
		username = u.Username
	}
	if strings.TrimSpace(newPassword) == "" {
		return s.store.SaveUser(ctx, username, u.PasswordHash)
	}
	if len(newPassword) < 8 {
		return errors.New("new password must contain at least 8 characters")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	return s.store.SaveUser(ctx, username, string(hash))
}

func (s *Service) Login(ctx context.Context, username, password string, lifetime time.Duration) (domain.Session, error) {
	u, err := s.store.User(ctx)
	if err != nil || u.Username != username || bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(password)) != nil {
		return domain.Session{}, errors.New("invalid username or password")
	}
	if lifetime <= 0 {
		lifetime = DefaultLifetime
	}
	b := make([]byte, 32)
	if _, err = rand.Read(b); err != nil {
		return domain.Session{}, err
	}
	x := domain.Session{Token: base64.RawURLEncoding.EncodeToString(b), UserID: u.ID, ExpiresAt: time.Now().UTC().Add(lifetime)}
	if err = s.store.CreateSession(ctx, x); err != nil {
		return domain.Session{}, err
	}
	return x, nil
}

func (s *Service) Valid(ctx context.Context, token string) bool {
	if token == "" {
		return false
	}
	_, err := s.store.Session(ctx, token)
	return err == nil
}

func (s *Service) Logout(ctx context.Context, token string) error {
	return s.store.DeleteSession(ctx, token)
}

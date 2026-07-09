// Package auth manages SAK's single local login — one username +
// bcrypt-hashed password gating access to the API (and therefore every
// review workflow) — plus stateless signed session tokens (see session.go)
// so a browser doesn't need to resend credentials on every request.
//
// Single login, not a user table: SAK is a self-hosted, single-operator
// tool (see the design's trust model), so there is exactly one account, the
// same way Settings has exactly one AI provider — no per-user permissions
// to model.
package auth

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"

	"golang.org/x/crypto/bcrypt"

	"github.com/curtiswtaylorjr/sak/internal/settings"
)

const (
	usernameKey     = "auth_username"
	passwordHashKey = "auth_password_hash"
)

// ErrNotConfigured is returned by Verify when no login has been set up yet.
var ErrNotConfigured = errors.New("auth: no login configured yet")

// Store persists the single login's credentials in internal/settings' flat
// KV store — a username and a bcrypt hash are just two more scalar values,
// no schema of their own needed (the hash is already safe to store as
// plaintext-in-DB; that's the entire point of a one-way hash).
type Store struct {
	settings *settings.Store
}

func New(settingsStore *settings.Store) *Store {
	return &Store{settings: settingsStore}
}

// Configured reports whether a login has been created yet — the API layer
// uses this to refuse a second SetCredentials call (see internal/api's
// setup handler): once an instance has an owner, a later unauthenticated
// visitor must not be able to silently take it over by "setting up" a new
// login of their own.
func (s *Store) Configured(ctx context.Context) (bool, error) {
	_, err := s.settings.Get(ctx, usernameKey)
	if errors.Is(err, settings.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// SetCredentials creates or replaces the single login.
func (s *Store) SetCredentials(ctx context.Context, username, password string) error {
	if username == "" || password == "" {
		return errors.New("auth: username and password are both required")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("hashing password: %w", err)
	}
	if err := s.settings.Set(ctx, usernameKey, username); err != nil {
		return fmt.Errorf("saving username: %w", err)
	}
	if err := s.settings.Set(ctx, passwordHashKey, string(hash)); err != nil {
		return fmt.Errorf("saving password hash: %w", err)
	}
	return nil
}

// Verify reports whether username/password match the configured login.
// Username is compared in constant time so a failed login can't leak
// anything about the real username via response timing; the password check
// is bcrypt's own constant-time comparison. Returns ErrNotConfigured (not a
// false negative) when no login exists yet — a caller must be able to tell
// "wrong password" apart from "there's nothing to check against."
func (s *Store) Verify(ctx context.Context, username, password string) (bool, error) {
	wantUsername, err := s.settings.Get(ctx, usernameKey)
	if errors.Is(err, settings.ErrNotFound) {
		return false, ErrNotConfigured
	}
	if err != nil {
		return false, err
	}
	hash, err := s.settings.Get(ctx, passwordHashKey)
	if err != nil {
		return false, err
	}

	usernameMatch := subtle.ConstantTimeCompare([]byte(username), []byte(wantUsername)) == 1
	passwordMatch := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
	return usernameMatch && passwordMatch, nil
}

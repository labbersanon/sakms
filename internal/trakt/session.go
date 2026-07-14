package trakt

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// ErrNotLinked is returned by Session methods that need a linked account
// (tokens obtained via a completed device flow) when only credentials
// (client_id/secret) are configured so far.
var ErrNotLinked = errors.New("trakt: no account linked (complete the device-code flow first)")

// refreshSkew is how far ahead of the actual expiry Session treats an
// access token as "needs refreshing" — a request that started just before
// expiry shouldn't lose a race against Trakt's clock, and refreshing a few
// minutes early costs nothing (Trakt's refresh tokens are long-lived and
// each refresh simply grants a fresh ~90-day window).
const refreshSkew = 5 * time.Minute

// Session ties Store and Client together so that "is my access token
// stale" is answered in exactly one place. A bare Client.Watchlist call
// makes no expiry check of its own (see its doc comment) — Session.Watchlist
// is the entry point every caller (the Settings/Discover HTTP handlers,
// once task #5 wires them) should use instead, so a Discover watchlist row
// backed by a ~90-day-old token can never silently start failing.
type Session struct {
	store  *Store
	client *Client
}

// NewSession builds a Session. client's Config should already carry the
// stored ClientID/ClientSecret (callers typically build client from
// store.Get's Credentials right before constructing a Session).
func NewSession(store *Store, client *Client) *Session {
	return &Session{store: store, client: client}
}

// ensureFreshToken loads the stored connection and, if linked but the access
// token is at or within refreshSkew of expiring, refreshes it and persists
// the new tokens before returning. Returns ErrNotConfigured/ErrNotLinked
// unchanged so callers can distinguish "nothing set up" from "expired".
func (sess *Session) ensureFreshToken(ctx context.Context) (*Connection, error) {
	conn, err := sess.store.Get(ctx)
	if err != nil {
		return nil, err
	}
	if !conn.Tokens.Linked() {
		return nil, ErrNotLinked
	}
	if time.Now().Add(refreshSkew).Before(conn.ExpiresAt) {
		return conn, nil
	}

	tok, err := sess.client.RefreshToken(ctx, conn.RefreshToken)
	if err != nil {
		return nil, fmt.Errorf("refreshing trakt token: %w", err)
	}
	if err := sess.store.SaveTokens(ctx, tok.AccessToken, tok.RefreshToken, tok.ExpiresAt); err != nil {
		return nil, fmt.Errorf("persisting refreshed trakt token: %w", err)
	}
	conn.Tokens = Tokens{AccessToken: tok.AccessToken, RefreshToken: tok.RefreshToken, ExpiresAt: tok.ExpiresAt}
	return conn, nil
}

// Watchlist returns the linked account's watchlist, transparently
// refreshing (and persisting) the access token first if it's expired or
// about to be. Returns ErrNotConfigured if no Trakt app is configured yet,
// or ErrNotLinked if configured but no account has completed the device
// flow.
func (sess *Session) Watchlist(ctx context.Context) ([]WatchlistItem, error) {
	conn, err := sess.ensureFreshToken(ctx)
	if err != nil {
		return nil, err
	}
	return sess.client.Watchlist(ctx, conn.AccessToken)
}

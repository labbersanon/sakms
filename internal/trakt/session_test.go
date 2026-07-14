package trakt

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// newTestSession wires a Session against a real Store (in-memory-ish temp
// SQLite, real secrets.Store — see store_test.go's newTestStore) and a
// Client pointed at handler, mirroring how a real caller would build one:
// Client's Config carries the same client_id/secret Store has on record.
func newTestSession(t *testing.T, handler http.HandlerFunc) (*Session, *Store) {
	t.Helper()
	store := newTestStore(t)
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	client := New(Config{BaseURL: srv.URL, ClientID: "client-abc", ClientSecret: "secret-xyz"}, srv.Client())
	return NewSession(store, client), store
}

func TestSessionWatchlist_NotConfigured(t *testing.T) {
	sess, _ := newTestSession(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("expected no HTTP request when nothing is configured")
	})
	_, err := sess.Watchlist(context.Background())
	if !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("expected ErrNotConfigured, got %v", err)
	}
}

func TestSessionWatchlist_ConfiguredButNotLinked(t *testing.T) {
	sess, store := newTestSession(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("expected no HTTP request when no account is linked")
	})
	secret := "secret-xyz"
	if err := store.SaveCredentials(context.Background(), "client-abc", &secret); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	_, err := sess.Watchlist(context.Background())
	if !errors.Is(err, ErrNotLinked) {
		t.Fatalf("expected ErrNotLinked, got %v", err)
	}
}

func TestSessionWatchlist_FreshTokenSkipsRefresh(t *testing.T) {
	refreshCalled := false
	sess, store := newTestSession(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/oauth/token" {
			refreshCalled = true
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[]`))
	})
	ctx := context.Background()
	secret := "secret-xyz"
	if err := store.SaveCredentials(ctx, "client-abc", &secret); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := store.SaveTokens(ctx, "fresh-access", "fresh-refresh", time.Now().Add(24*time.Hour)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, err := sess.Watchlist(ctx); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if refreshCalled {
		t.Error("expected no refresh call for a token far from expiry")
	}
}

func TestSessionWatchlist_ExpiringTokenRefreshesAndPersistsBeforeFetch(t *testing.T) {
	var watchlistAuth string
	sess, store := newTestSession(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth/token":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"access_token":"new-access","refresh_token":"new-refresh","expires_in":7776000,"created_at":1700000000}`))
		case "/sync/watchlist":
			watchlistAuth = r.Header.Get("Authorization")
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`[]`))
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
	})
	ctx := context.Background()
	secret := "secret-xyz"
	if err := store.SaveCredentials(ctx, "client-abc", &secret); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Already expired -> must refresh before fetching.
	if err := store.SaveTokens(ctx, "stale-access", "stale-refresh", time.Now().Add(-time.Hour)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, err := sess.Watchlist(ctx); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if watchlistAuth != "Bearer new-access" {
		t.Errorf("expected watchlist call to use the refreshed access token, got %q", watchlistAuth)
	}

	conn, err := store.Get(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if conn.AccessToken != "new-access" || conn.RefreshToken != "new-refresh" {
		t.Errorf("expected refreshed tokens persisted, got %+v", conn.Tokens)
	}
}

func TestSessionWatchlist_WithinRefreshSkewRefreshesEarly(t *testing.T) {
	refreshCalled := false
	sess, store := newTestSession(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth/token":
			refreshCalled = true
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"access_token":"new-access","refresh_token":"new-refresh","expires_in":7776000,"created_at":1700000000}`))
		case "/sync/watchlist":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`[]`))
		}
	})
	ctx := context.Background()
	secret := "secret-xyz"
	if err := store.SaveCredentials(ctx, "client-abc", &secret); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Not yet expired, but within refreshSkew (5 min) of expiry.
	if err := store.SaveTokens(ctx, "soon-stale-access", "soon-stale-refresh", time.Now().Add(2*time.Minute)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, err := sess.Watchlist(ctx); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !refreshCalled {
		t.Error("expected a refresh call for a token within the refresh skew window")
	}
}

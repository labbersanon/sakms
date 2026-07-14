package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/curtiswtaylorjr/sakms/internal/db"
	"github.com/curtiswtaylorjr/sakms/internal/secrets"
	"github.com/curtiswtaylorjr/sakms/internal/trakt"
)

// newTraktTestStore builds a *trakt.Store against a real, freshly migrated
// SQLite file and a real secrets.Store — same convention internal/trakt's
// own tests use, and every other Store-backed test in this repo.
func newTraktTestStore(t *testing.T) *trakt.Store {
	t.Helper()
	dir := t.TempDir()
	sqlDB, err := db.Open(filepath.Join(dir, "sakms.db"))
	if err != nil {
		t.Fatalf("opening db: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	secretStore, err := secrets.New(make([]byte, 32))
	if err != nil {
		t.Fatalf("building secret store: %v", err)
	}
	return trakt.NewStore(sqlDB, secretStore)
}

// newTraktTestMux wires this file's handlers into their own small mux,
// independent of the real internal/api/handler.go, exactly as task #9
// requires. Route paths mirror the authoritative /api/trakt/* contract
// (minus the /api prefix, irrelevant for a standalone test mux).
// traktSrvURL is the fake upstream Trakt server every handler's baseURL
// parameter points at.
func newTraktTestMux(store *trakt.Store, flow *traktDeviceFlow, traktSrvURL string) *http.ServeMux {
	httpClient := testHTTPClient()
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /trakt/credentials", traktSaveCredentialsHandler(store))
	mux.HandleFunc("GET /trakt/status", traktStatusHandler(store))
	mux.HandleFunc("POST /trakt/disconnect", traktDisconnectHandler(store))
	mux.HandleFunc("POST /trakt/device/start", traktDeviceStartHandler(store, flow, httpClient, traktSrvURL))
	mux.HandleFunc("POST /trakt/device/poll", traktDevicePollHandler(store, flow, httpClient, traktSrvURL))
	mux.HandleFunc("GET /trakt/watchlist", traktWatchlistHandler(store, httpClient, traktSrvURL))
	return mux
}

func TestTestTrakt_ValidAndInvalidClientID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("trakt-api-key") == "good-id" {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`[]`))
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	result := testTrakt(context.Background(), testHTTPClient(), srv.URL, "good-id")
	if !result.OK || result.Error != "" {
		t.Fatalf("expected success for a valid client_id, got %+v", result)
	}

	result = testTrakt(context.Background(), testHTTPClient(), srv.URL, "bad-id")
	if result.OK {
		t.Fatal("expected failure for an invalid client_id")
	}
}

func TestTraktSaveCredentialsHandler_ThreeStateSecret(t *testing.T) {
	store := newTraktTestStore(t)
	mux := newTraktTestMux(store, newTraktDeviceFlow(), "")
	srv := httptest.NewServer(mux)
	defer srv.Close()

	put := func(body string) *http.Response {
		req, _ := http.NewRequest(http.MethodPut, srv.URL+"/trakt/credentials", bytes.NewBufferString(body))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("PUT failed: %v", err)
		}
		return resp
	}

	// Set client_id + client_secret.
	resp := put(`{"clientId":"client-abc","clientSecret":"secret-xyz"}`)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", resp.StatusCode)
	}
	conn, err := store.Get(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if conn.ClientID != "client-abc" || conn.ClientSecret != "secret-xyz" {
		t.Fatalf("unexpected connection: %+v", conn)
	}

	// Omit clientSecret entirely -> preserve.
	resp = put(`{"clientId":"client-def"}`)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", resp.StatusCode)
	}
	conn, err = store.Get(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if conn.ClientID != "client-def" || conn.ClientSecret != "secret-xyz" {
		t.Fatalf("expected secret preserved, got %+v", conn)
	}

	// Explicit empty string -> clear.
	resp = put(`{"clientId":"client-def","clientSecret":""}`)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", resp.StatusCode)
	}
	conn, err = store.Get(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if conn.ClientSecret != "" {
		t.Fatalf("expected secret cleared, got %+v", conn)
	}
}

func TestTraktSaveCredentialsHandler_RequiresClientID(t *testing.T) {
	store := newTraktTestStore(t)
	mux := newTraktTestMux(store, newTraktDeviceFlow(), "")
	srv := httptest.NewServer(mux)
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/trakt/credentials", bytes.NewBufferString(`{}`))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT failed: %v", err)
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}

func TestTraktStatusHandler(t *testing.T) {
	store := newTraktTestStore(t)
	mux := newTraktTestMux(store, newTraktDeviceFlow(), "")
	srv := httptest.NewServer(mux)
	defer srv.Close()

	get := func() traktStatus {
		resp, err := http.Get(srv.URL + "/trakt/status")
		if err != nil {
			t.Fatalf("GET failed: %v", err)
		}
		var status traktStatus
		if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
			t.Fatalf("decoding response: %v", err)
		}
		return status
	}

	// Nothing configured yet.
	status := get()
	if status.Configured || status.Linked || status.TokenExpiresAt != "" {
		t.Fatalf("expected zero-value status, got %+v", status)
	}

	secret := "secret-xyz"
	if err := store.SaveCredentials(context.Background(), "client-abc", &secret); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	status = get()
	if !status.Configured || status.Linked {
		t.Fatalf("unexpected status after saving credentials: %+v", status)
	}

	expiresAt := time.Now().Add(90 * 24 * time.Hour).Truncate(time.Second).UTC()
	if err := store.SaveTokens(context.Background(), "at", "rt", expiresAt); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	status = get()
	if !status.Linked || status.TokenExpiresAt != expiresAt.Format(time.RFC3339) {
		t.Fatalf("unexpected status after linking: %+v", status)
	}
}

func TestTraktDisconnectHandler_ClearsTokensKeepsCredentials(t *testing.T) {
	store := newTraktTestStore(t)
	mux := newTraktTestMux(store, newTraktDeviceFlow(), "")
	srv := httptest.NewServer(mux)
	defer srv.Close()

	secret := "secret-xyz"
	if err := store.SaveCredentials(context.Background(), "client-abc", &secret); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := store.SaveTokens(context.Background(), "at", "rt", time.Now().Add(24*time.Hour)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	resp, err := http.Post(srv.URL+"/trakt/disconnect", "application/json", nil)
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", resp.StatusCode)
	}

	conn, err := store.Get(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if conn.Tokens.Linked() {
		t.Error("expected tokens cleared after disconnect")
	}
	if conn.ClientID != "client-abc" || conn.ClientSecret != "secret-xyz" {
		t.Errorf("expected credentials preserved after disconnect, got %+v", conn.Credentials)
	}
}

func TestTraktDeviceStart_NotConfigured(t *testing.T) {
	store := newTraktTestStore(t)
	mux := newTraktTestMux(store, newTraktDeviceFlow(), "")
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/trakt/device/start", "application/json", nil)
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	if resp.StatusCode != http.StatusPreconditionFailed {
		t.Fatalf("expected 412 when not configured, got %d", resp.StatusCode)
	}
}

func TestTraktDevicePoll_WithoutStart(t *testing.T) {
	store := newTraktTestStore(t)
	secret := "secret-xyz"
	if err := store.SaveCredentials(context.Background(), "client-abc", &secret); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	mux := newTraktTestMux(store, newTraktDeviceFlow(), "")
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/trakt/device/poll", "application/json", nil)
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("expected 409 when no flow started, got %d", resp.StatusCode)
	}
}

func TestTraktDeviceFlow_FullHappyPath(t *testing.T) {
	pollCount := 0
	traktSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth/device/code":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"device_code":"dc1","user_code":"USER1234","verification_url":"https://trakt.tv/activate","expires_in":600,"interval":1}`))
		case "/oauth/device/token":
			pollCount++
			if pollCount < 2 {
				w.WriteHeader(http.StatusBadRequest) // pending
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"access_token":"at1","refresh_token":"rt1","expires_in":7776000,"created_at":1700000000}`))
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer traktSrv.Close()

	store := newTraktTestStore(t)
	secret := "secret-xyz"
	if err := store.SaveCredentials(context.Background(), "client-abc", &secret); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	mux := newTraktTestMux(store, newTraktDeviceFlow(), traktSrv.URL)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// Start.
	resp, err := http.Post(srv.URL+"/trakt/device/start", "application/json", nil)
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var start traktDeviceStartResponse
	if err := json.NewDecoder(resp.Body).Decode(&start); err != nil {
		t.Fatalf("decoding start response: %v", err)
	}
	if start.UserCode != "USER1234" || start.VerificationURL != "https://trakt.tv/activate" {
		t.Fatalf("unexpected start response: %+v", start)
	}

	// First poll -> pending.
	pollResp, err := http.Post(srv.URL+"/trakt/device/poll", "application/json", nil)
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	var poll traktDevicePollResponse
	json.NewDecoder(pollResp.Body).Decode(&poll)
	if poll.Status != string(traktDeviceStatusPending) {
		t.Fatalf("expected pending, got %+v", poll)
	}

	// Second poll -> linked, tokens saved.
	pollResp, err = http.Post(srv.URL+"/trakt/device/poll", "application/json", nil)
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	json.NewDecoder(pollResp.Body).Decode(&poll)
	if poll.Status != string(traktDeviceStatusLinked) {
		t.Fatalf("expected linked, got %+v", poll)
	}

	conn, err := store.Get(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if conn.AccessToken != "at1" || conn.RefreshToken != "rt1" {
		t.Fatalf("expected tokens persisted, got %+v", conn.Tokens)
	}

	// A third poll after linking has cleared the pending code -> 409.
	pollResp, err = http.Post(srv.URL+"/trakt/device/poll", "application/json", nil)
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	if pollResp.StatusCode != http.StatusConflict {
		t.Fatalf("expected 409 after the flow already completed, got %d", pollResp.StatusCode)
	}

	// GET /trakt/status must independently reflect the now-linked state.
	statusResp, err := http.Get(srv.URL + "/trakt/status")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	var status traktStatus
	json.NewDecoder(statusResp.Body).Decode(&status)
	if !status.Linked {
		t.Fatalf("expected /trakt/status to report linked, got %+v", status)
	}
}

func TestTraktDeviceFlow_Denied(t *testing.T) {
	traktSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth/device/code":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"device_code":"dc1","user_code":"USER1234","verification_url":"https://trakt.tv/activate","expires_in":600,"interval":1}`))
		case "/oauth/device/token":
			w.WriteHeader(418)
		}
	}))
	defer traktSrv.Close()

	store := newTraktTestStore(t)
	secret := "secret-xyz"
	if err := store.SaveCredentials(context.Background(), "client-abc", &secret); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	mux := newTraktTestMux(store, newTraktDeviceFlow(), traktSrv.URL)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	http.Post(srv.URL+"/trakt/device/start", "application/json", nil)

	pollResp, err := http.Post(srv.URL+"/trakt/device/poll", "application/json", nil)
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	var poll traktDevicePollResponse
	json.NewDecoder(pollResp.Body).Decode(&poll)
	if poll.Status != string(traktDeviceStatusDenied) {
		t.Fatalf("expected denied, got %+v", poll)
	}
}

func TestTraktWatchlistHandler_NotConfiguredOrLinkedReturnsEmpty(t *testing.T) {
	store := newTraktTestStore(t)
	mux := newTraktTestMux(store, newTraktDeviceFlow(), "")
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/trakt/watchlist")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	var items []traktWatchlistItem
	if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("expected empty list when not configured, got %+v", items)
	}

	secret := "secret-xyz"
	if err := store.SaveCredentials(context.Background(), "client-abc", &secret); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp, err = http.Get(srv.URL + "/trakt/watchlist")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	items = nil
	if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("expected empty list when configured but not linked, got %+v", items)
	}
}

func TestTraktWatchlistHandler_MapsToContractShape(t *testing.T) {
	traktSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/sync/watchlist" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[
			{"type":"movie","movie":{"title":"Some Movie","year":2023,"ids":{"tmdb":100}}},
			{"type":"show","show":{"title":"Some Show","year":2021,"ids":{"tmdb":200}}}
		]`))
	}))
	defer traktSrv.Close()

	store := newTraktTestStore(t)
	secret := "secret-xyz"
	if err := store.SaveCredentials(context.Background(), "client-abc", &secret); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := store.SaveTokens(context.Background(), "at", "rt", time.Now().Add(24*time.Hour)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	mux := newTraktTestMux(store, newTraktDeviceFlow(), traktSrv.URL)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/trakt/watchlist")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	var items []traktWatchlistItem
	if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %+v", items)
	}
	if items[0].TMDBID != 100 || items[0].Title != "Some Movie" || items[0].Type != "movie" || items[0].Year != 2023 {
		t.Errorf("unexpected movie item: %+v", items[0])
	}
	if items[1].TMDBID != 200 || items[1].Title != "Some Show" || items[1].Type != "show" || items[1].Year != 2021 {
		t.Errorf("unexpected show item: %+v", items[1])
	}
}

// TestTraktWatchlistItem_JSONFieldNames locks in the exact wire field names
// the authoritative contract specifies ({type, title, year, tmdbId}) — a
// regression here would silently break worker-5's already-tested frontend.
func TestTraktWatchlistItem_JSONFieldNames(t *testing.T) {
	b, err := json.Marshal(traktWatchlistItem{Type: "movie", Title: "T", Year: 2020, TMDBID: 42})
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	for _, field := range []string{"type", "title", "year", "tmdbId"} {
		if _, ok := raw[field]; !ok {
			t.Errorf("expected JSON field %q, got %+v", field, raw)
		}
	}
}

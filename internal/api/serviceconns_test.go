package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labbersanon/sakms/internal/apidto"
	"github.com/labbersanon/sakms/internal/serviceconn"
)

// newRegistryServer builds a NewMux server backed by a real migrated database
// with a real registry store — the same full-stack shape every other handler
// test uses. Returns the server and the registry store, so a test can assert
// against stored state as well as the HTTP response.
func newRegistryServer(t *testing.T) (*httptest.Server, *serviceconn.Store) {
	t.Helper()
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, scStore := testStoresWithRegistry(t)
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, scStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), rssFeedsStore, nil, nil, nil, nil, nil, nil, nil, nil))
	t.Cleanup(srv.Close)
	return srv, scStore
}

func doJSON(t *testing.T, method, url string, body any) (*http.Response, []byte) {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("encoding request body: %v", err)
		}
		reader = bytes.NewReader(encoded)
	} else {
		reader = bytes.NewReader(nil)
	}
	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatalf("building %s %s: %v", method, url, err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s failed: %v", method, url, err)
	}
	defer resp.Body.Close()
	out := make([]byte, 0)
	buf := make([]byte, 4096)
	for {
		n, readErr := resp.Body.Read(buf)
		out = append(out, buf[:n]...)
		if readErr != nil {
			break
		}
	}
	return resp, out
}

// TestServiceConnectionsCRUD_EndToEnd exercises the whole registry route family
// over real HTTP against a real migrated database: create a usenet subscription
// and a player, list them redacted, update, assign modes, delete.
func TestServiceConnectionsCRUD_EndToEnd(t *testing.T) {
	srv, scStore := newRegistryServer(t)

	// Create a usenet subscription.
	secret := "hunter2pass"
	resp, body := doJSON(t, http.MethodPost, srv.URL+"/api/service-connections", serviceConnectionRequest{
		Kind: "usenet", Provider: "nntp", Label: "Newshosting", Enabled: true,
		Host: "news.example.com", Port: 563, TLS: true, MaxConns: 20,
		Username: "wade", Secret: &secret,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201 from create, got %d: %s", resp.StatusCode, body)
	}
	var sub serviceconn.Summary
	if err := json.Unmarshal(body, &sub); err != nil {
		t.Fatalf("decoding create response: %v (%s)", err, body)
	}
	if sub.ID == 0 || sub.Host != "news.example.com" || sub.Port != 563 || !sub.TLS {
		t.Fatalf("unexpected created subscription: %+v", sub)
	}
	// The secret is never echoed back — only its presence and last 4 chars.
	if !sub.HasSecret || sub.SecretSuffix != "pass" {
		t.Fatalf("expected a redacted secret summary, got %+v", sub)
	}
	if bytes.Contains(body, []byte(secret)) {
		t.Fatalf("create response leaked the plaintext secret: %s", body)
	}

	// Create a player.
	playerKey := "jellyfin-key-9999"
	resp, body = doJSON(t, http.MethodPost, srv.URL+"/api/service-connections", serviceConnectionRequest{
		Kind: "player", Provider: "jellyfin", Label: "Living room", Enabled: true,
		URL: "http://jellyfin:8096", Secret: &playerKey, Modes: []string{"movies"},
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201 from create, got %d: %s", resp.StatusCode, body)
	}
	var player serviceconn.Summary
	json.Unmarshal(body, &player)
	if len(player.Modes) != 1 || player.Modes[0] != "movies" {
		t.Fatalf("expected the create-time mode assignment to stick, got %+v", player)
	}

	// List returns both, secret-free.
	resp, body = doJSON(t, http.MethodGet, srv.URL+"/api/service-connections", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 from list, got %d", resp.StatusCode)
	}
	var list []serviceconn.Summary
	if err := json.Unmarshal(body, &list); err != nil {
		t.Fatalf("decoding list: %v (%s)", err, body)
	}
	if len(list) != 2 {
		t.Fatalf("expected 2 registry rows, got %d: %s", len(list), body)
	}
	if bytes.Contains(body, []byte(secret)) || bytes.Contains(body, []byte(playerKey)) {
		t.Fatalf("list leaked a plaintext secret: %s", body)
	}

	// Update the subscription's editable fields.
	resp, body = doJSON(t, http.MethodPut, fmt.Sprintf("%s/api/service-connections/%d", srv.URL, sub.ID), serviceConnectionRequest{
		Kind: "usenet", Provider: "nntp", Label: "Newshosting (EU)", Enabled: false,
		Host: "eu.example.com", Port: 443, TLS: true, MaxConns: 8, Username: "wade",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 from update, got %d: %s", resp.StatusCode, body)
	}
	var updated serviceconn.Summary
	json.Unmarshal(body, &updated)
	if updated.Label != "Newshosting (EU)" || updated.Host != "eu.example.com" || updated.Port != 443 || updated.Enabled {
		t.Fatalf("unexpected updated subscription: %+v", updated)
	}

	// Reassign the player's modes.
	resp, body = doJSON(t, http.MethodPut, fmt.Sprintf("%s/api/service-connections/%d/modes", srv.URL, player.ID), apidto.ServiceConnectionModesRequest{
		Modes: []string{"movies", "series"},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 from modes, got %d: %s", resp.StatusCode, body)
	}
	var remoded serviceconn.Summary
	json.Unmarshal(body, &remoded)
	if len(remoded.Modes) != 2 {
		t.Fatalf("expected 2 assigned modes, got %+v", remoded)
	}

	// Delete the subscription.
	resp, body = doJSON(t, http.MethodDelete, fmt.Sprintf("%s/api/service-connections/%d", srv.URL, sub.ID), nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204 from delete, got %d: %s", resp.StatusCode, body)
	}
	if _, err := scStore.Get(context.Background(), sub.ID); err == nil {
		t.Fatal("expected the deleted subscription to be gone from the store")
	}

	// A second delete of the same id is a 404, not a silent 204.
	resp, _ = doJSON(t, http.MethodDelete, fmt.Sprintf("%s/api/service-connections/%d", srv.URL, sub.ID), nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 deleting an unknown id, got %d", resp.StatusCode)
	}
}

// TestServiceConnectionUpdate_ThreeStateSecret locks the *T secret rule across
// the HTTP boundary, which is where a naive mapping breaks it:
// serviceconn.Store.Update IGNORES Connection.Secret and takes the three-state
// value as its own parameter, so a handler that threads the secret through the
// struct compiles, passes a store-level test, and silently drops every secret
// change an operator makes.
func TestServiceConnectionUpdate_ThreeStateSecret(t *testing.T) {
	srv, scStore := newRegistryServer(t)

	original := "original-secret-abcd"
	_, body := doJSON(t, http.MethodPost, srv.URL+"/api/service-connections", serviceConnectionRequest{
		Kind: "player", Provider: "emby", Label: "Emby", Enabled: true,
		URL: "http://emby:8096", Secret: &original,
	})
	var created serviceconn.Summary
	json.Unmarshal(body, &created)

	base := serviceConnectionRequest{
		Kind: "player", Provider: "emby", Label: "Emby", Enabled: true, URL: "http://emby:8096",
	}
	url := fmt.Sprintf("%s/api/service-connections/%d", srv.URL, created.ID)
	storedSecret := func() string {
		t.Helper()
		c, err := scStore.Get(context.Background(), created.ID)
		if err != nil {
			t.Fatalf("loading row: %v", err)
		}
		return c.Secret
	}

	// (1) secret absent from the JSON entirely -> PRESERVE. This is what the
	// frontend sends when the operator edits only the URL and leaves the masked
	// key field untouched.
	resp, respBody := doJSON(t, http.MethodPut, url, base)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, respBody)
	}
	if got := storedSecret(); got != original {
		t.Fatalf("expected an omitted secret to preserve the stored one, got %q", got)
	}
	var preserved serviceconn.Summary
	json.Unmarshal(respBody, &preserved)
	if !preserved.HasSecret || preserved.SecretSuffix != "abcd" {
		t.Fatalf("expected the response to still report the preserved secret, got %+v", preserved)
	}

	// (2) secret present and non-empty -> REPLACE.
	replacement := "replacement-secret-wxyz"
	withNew := base
	withNew.Secret = &replacement
	if resp, respBody = doJSON(t, http.MethodPut, url, withNew); resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, respBody)
	}
	if got := storedSecret(); got != replacement {
		t.Fatalf("expected the secret to be replaced, got %q", got)
	}

	// (3) secret present as "" -> CLEAR.
	empty := ""
	withEmpty := base
	withEmpty.Secret = &empty
	if resp, respBody = doJSON(t, http.MethodPut, url, withEmpty); resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, respBody)
	}
	if got := storedSecret(); got != "" {
		t.Fatalf("expected an explicit empty secret to clear the stored one, got %q", got)
	}
	var cleared serviceconn.Summary
	json.Unmarshal(respBody, &cleared)
	if cleared.HasSecret {
		t.Fatalf("expected hasSecret=false after clearing, got %+v", cleared)
	}
}

// TestServiceConnections_ValidationErrors confirms the store's per-kind shape
// invariants surface as 400s, not 500s — the new routes validate through the
// store rather than through handler.go's URL-shaped fixedURLServices map.
func TestServiceConnections_ValidationErrors(t *testing.T) {
	srv, _ := newRegistryServer(t)

	for _, tc := range []struct {
		name string
		req  serviceConnectionRequest
	}{
		{"player without url", serviceConnectionRequest{Kind: "player", Provider: "plex", Enabled: true}},
		{"usenet without host", serviceConnectionRequest{Kind: "usenet", Provider: "nntp", Enabled: true, Port: 563}},
		{"provider does not match kind", serviceConnectionRequest{Kind: "usenet", Provider: "plex", Enabled: true, Host: "h", Port: 1}},
		{"unknown provider", serviceConnectionRequest{Kind: "player", Provider: "kodi", Enabled: true, URL: "http://kodi"}},
		{"unknown mode", serviceConnectionRequest{Kind: "player", Provider: "plex", Enabled: true, URL: "http://plex", Modes: []string{"anime"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp, body := doJSON(t, http.MethodPost, srv.URL+"/api/service-connections", tc.req)
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d: %s", resp.StatusCode, body)
			}
		})
	}

	resp, _ := doJSON(t, http.MethodGet, srv.URL+"/api/service-connections", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 from list, got %d", resp.StatusCode)
	}
	resp, body := doJSON(t, http.MethodPut, srv.URL+"/api/service-connections/not-a-number", serviceConnectionRequest{
		Kind: "player", Provider: "plex", URL: "http://plex",
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for a non-numeric id, got %d: %s", resp.StatusCode, body)
	}
}

// TestServiceConnectionTest_UnsavedPlayerValues drives POST
// /api/service-connections/test against a stand-in Emby server, proving the
// field-based test path reaches the real per-provider client.
func TestServiceConnectionTest_UnsavedPlayerValues(t *testing.T) {
	var gotToken string
	fakeEmby := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotToken = r.Header.Get("X-Emby-Token")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"Version":"4.8.0"}`))
	}))
	defer fakeEmby.Close()

	srv, _ := newRegistryServer(t)
	key := "emby-key"
	resp, body := doJSON(t, http.MethodPost, srv.URL+"/api/service-connections/test", serviceConnectionRequest{
		Kind: "player", Provider: "emby", URL: fakeEmby.URL, Secret: &key,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
	}
	var result ConnectionTestResult
	json.Unmarshal(body, &result)
	if !result.OK {
		t.Fatalf("expected ok=true, got %+v", result)
	}
	if gotToken != key {
		t.Errorf("expected the Emby client's X-Emby-Token header to carry the secret, got %q", gotToken)
	}
}

// TestServiceConnectionTest_StoredRow drives POST
// /api/service-connections/{id}/test — the id-keyed replacement for
// test-stored, which could never address one of N subscriptions.
func TestServiceConnectionTest_StoredRow(t *testing.T) {
	fakePlex := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"MediaContainer":{"machineIdentifier":"abc123","version":"1.40"}}`))
	}))
	defer fakePlex.Close()

	srv, _ := newRegistryServer(t)
	token := "plex-token"
	_, body := doJSON(t, http.MethodPost, srv.URL+"/api/service-connections", serviceConnectionRequest{
		Kind: "player", Provider: "plex", Label: "Plex", Enabled: true, URL: fakePlex.URL, Secret: &token,
	})
	var created serviceconn.Summary
	json.Unmarshal(body, &created)

	resp, body := doJSON(t, http.MethodPost, fmt.Sprintf("%s/api/service-connections/%d/test", srv.URL, created.ID), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
	}
	var result ConnectionTestResult
	json.Unmarshal(body, &result)
	if !result.OK {
		t.Fatalf("expected ok=true against a stand-in Plex server, got %+v", result)
	}

	// An unknown id is a 404, never a bare "connection test failed".
	resp, _ = doJSON(t, http.MethodPost, srv.URL+"/api/service-connections/99999/test", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 testing an unknown id, got %d", resp.StatusCode)
	}
}

// TestServiceConnectionTest_StoredFailureIsDetailFree locks the security
// contract the id-keyed rewrite inherits from connectionsTestStoredHandler: a
// failure against a STORED row never echoes the downstream error, because a Go
// http-client error embeds the target host:port (and some clients the key),
// which is stored config the client is not allowed to read back.
func TestServiceConnectionTest_StoredFailureIsDetailFree(t *testing.T) {
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadURL := dead.URL
	dead.Close() // nothing is listening now, so the test dial fails

	srv, _ := newRegistryServer(t)
	_, body := doJSON(t, http.MethodPost, srv.URL+"/api/service-connections", serviceConnectionRequest{
		Kind: "player", Provider: "jellyfin", Label: "Dead", Enabled: true, URL: deadURL,
	})
	var created serviceconn.Summary
	json.Unmarshal(body, &created)

	resp, body := doJSON(t, http.MethodPost, fmt.Sprintf("%s/api/service-connections/%d/test", srv.URL, created.ID), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 carrying a failed result, got %d: %s", resp.StatusCode, body)
	}
	var result ConnectionTestResult
	json.Unmarshal(body, &result)
	if result.OK {
		t.Fatal("expected the test against a closed server to fail")
	}
	if result.Error != "connection test failed" {
		t.Fatalf("expected a detail-free error, got %q", result.Error)
	}
	if bytes.Contains(body, []byte(deadURL)) {
		t.Fatalf("stored-row test leaked the configured URL: %s", body)
	}
}

// TestConnectionsRoutes_RejectMovedServices is the M2 lock. connections.Store
// accepts any service key generically, so without an explicit guard a stale
// frontend or a saved curl one-liner could still write an `nntp` or `jellyfin`
// row that nothing reads — a connection the operator believes is configured and
// that has zero effect. All three arbitrary-{service} handlers must reject
// both, naming the endpoint that replaced them.
func TestConnectionsRoutes_RejectMovedServices(t *testing.T) {
	srv, _ := newRegistryServer(t)

	key := "should-never-be-stored"
	for _, service := range []string{"nntp", "jellyfin"} {
		for _, tc := range []struct {
			method string
			path   string
			body   any
		}{
			{http.MethodPut, "/api/connections/" + service, upsertConnectionRequest{URL: "http://moved:1234", APIKey: &key}},
			{http.MethodDelete, "/api/connections/" + service, nil},
			{http.MethodPost, "/api/connections/" + service + "/test-stored", nil},
		} {
			t.Run(tc.method+" "+tc.path, func(t *testing.T) {
				resp, body := doJSON(t, tc.method, srv.URL+tc.path, tc.body)
				if resp.StatusCode != http.StatusBadRequest {
					t.Fatalf("expected 400, got %d: %s", resp.StatusCode, body)
				}
				if !bytes.Contains(body, []byte("/api/service-connections")) {
					t.Fatalf("expected the rejection to name the new endpoint, got %q", body)
				}
			})
		}
	}

	// A surviving singleton service is unaffected by the guard.
	resp, body := doJSON(t, http.MethodPut, srv.URL+"/api/connections/prowlarr", upsertConnectionRequest{URL: "http://prowlarr:9696", APIKey: &key})
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected the guard to leave prowlarr alone, got %d: %s", resp.StatusCode, body)
	}
}

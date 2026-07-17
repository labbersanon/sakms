package netscan

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func testHTTPClient() *http.Client {
	return &http.Client{Timeout: 5 * time.Second}
}

// The exact canned identity payloads the real services serve (see the package
// doc). Prowlarr's /initialize.json carries its own live apiKey, so the
// leak-guard test can prove the probe never surfaces it.
const (
	prowlarrInitializeJSON = `{"apiRoot":"/api/v1","apiKey":"SUPERSECRETPROWLARRKEY","instanceName":"Prowlarr","urlBase":""}`
	qbittorrentVersion     = `2.15.1`
	jellyfinPublicInfoJSON = `{"ServerName":"MyJellyfin","Version":"10.9.11","Id":"abc123","ProductName":"Jellyfin Server"}`
)

// probeServer stands in for one real service: it routes each service's
// identity endpoint to a canned response and 404s everything else.
func probeServer(t *testing.T, service string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case service == "prowlarr" && r.URL.Path == "/initialize.json":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(prowlarrInitializeJSON))
		case service == "qbittorrent" && r.URL.Path == "/api/v2/app/webapiVersion":
			w.Write([]byte(qbittorrentVersion))
		case service == "nzbget" && r.URL.Path == "/":
			// A real NZBGet root answers 401 while still emitting Server.
			w.Header().Set("Server", "nzbget-26.2")
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte("<html>auth required</html>"))
		case service == "jellyfin" && r.URL.Path == "/System/Info/Public":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(jellyfinPublicInfoJSON))
		case service == "stash" && r.Method == http.MethodPost && r.URL.Path == "/graphql":
			// Stash returns JSON even when the API key is required.
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"errors":[{"message":"API key required"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestProbeProwlarr_ConfirmsIdentity(t *testing.T) {
	srv := probeServer(t, "prowlarr")
	f, ok := probeProwlarr(context.Background(), testHTTPClient(), srv.URL)
	if !ok {
		t.Fatal("expected prowlarr to be confirmed")
	}
	if f.Service != "prowlarr" || f.URL != srv.URL {
		t.Errorf("unexpected finding: %+v", f)
	}
	if !strings.Contains(strings.ToLower(f.Label), "prowlarr") {
		t.Errorf("label %q should mention prowlarr", f.Label)
	}
}

// TestProbeProwlarr_RequiresMatchingInstanceName proves the probe doesn't just
// confirm "any Servarr-shaped app" — a server whose /initialize.json reports a
// different instanceName (here, a Whisparr-shaped payload) must not confirm as
// Prowlarr.
func TestProbeProwlarr_RequiresMatchingInstanceName(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/initialize.json" {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"apiKey":"OTHERKEY","instanceName":"Whisparr"}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	if _, ok := probeProwlarr(context.Background(), testHTTPClient(), srv.URL); ok {
		t.Error("probing a non-Prowlarr Servarr server should not confirm as Prowlarr")
	}
}

// TestProbeProwlarr_NeverLeaksAPIKey is the required guard: /initialize.json
// carries a live key, but the returned Finding must never contain it — not in
// any field, not once marshaled to the JSON that would go over the wire.
func TestProbeProwlarr_NeverLeaksAPIKey(t *testing.T) {
	srv := probeServer(t, "prowlarr")
	f, ok := probeProwlarr(context.Background(), testHTTPClient(), srv.URL)
	if !ok {
		t.Fatal("expected prowlarr to be confirmed")
	}
	encoded, err := json.Marshal(f)
	if err != nil {
		t.Fatalf("marshaling finding: %v", err)
	}
	if strings.Contains(string(encoded), "SUPERSECRETPROWLARRKEY") {
		t.Fatalf("Finding leaked the API key: %s", encoded)
	}
	if strings.Contains(strings.ToLower(string(encoded)), "apikey") {
		t.Fatalf("Finding's JSON contains an apiKey-shaped field: %s", encoded)
	}
}

func TestProbeQBittorrent_ConfirmsIdentity(t *testing.T) {
	srv := probeServer(t, "qbittorrent")
	f, ok := probeQBittorrent(context.Background(), testHTTPClient(), srv.URL)
	if !ok {
		t.Fatal("expected qbittorrent to be confirmed")
	}
	if f.Service != "qbittorrent" {
		t.Errorf("unexpected finding: %+v", f)
	}
}

// TestProbeQBittorrent_RejectsNonVersionBody guards against an arbitrary
// 200-returning server being mistaken for qBittorrent.
func TestProbeQBittorrent_RejectsNonVersionBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("<!DOCTYPE html><html>hello</html>"))
	}))
	defer srv.Close()
	if _, ok := probeQBittorrent(context.Background(), testHTTPClient(), srv.URL); ok {
		t.Fatal("a non-version body should not confirm qBittorrent")
	}
}

func TestProbeNZBGet_ConfirmsFromServerHeaderOn401(t *testing.T) {
	srv := probeServer(t, "nzbget")
	f, ok := probeNZBGet(context.Background(), testHTTPClient(), srv.URL)
	if !ok {
		t.Fatal("expected nzbget to be confirmed from its Server header even on 401")
	}
	if f.Service != "nzbget" {
		t.Errorf("unexpected finding: %+v", f)
	}
}

func TestProbeNZBGet_RejectsWithoutServerHeader(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	if _, ok := probeNZBGet(context.Background(), testHTTPClient(), srv.URL); ok {
		t.Fatal("a server without an nzbget Server header should not confirm")
	}
}

func TestProbeJellyfin_ConfirmsIdentity(t *testing.T) {
	srv := probeServer(t, "jellyfin")
	f, ok := probeJellyfin(context.Background(), testHTTPClient(), srv.URL)
	if !ok {
		t.Fatal("expected jellyfin to be confirmed")
	}
	if f.Service != "jellyfin" {
		t.Errorf("unexpected finding: %+v", f)
	}
}

// TestProbeStash_ConfirmsIdentityOnAuthError proves probeStash confirms even
// when Stash requires an API key — the auth error is still valid JSON, which
// is sufficient to identify the instance.
func TestProbeStash_ConfirmsIdentityOnAuthError(t *testing.T) {
	srv := probeServer(t, "stash")
	f, ok := probeStash(context.Background(), testHTTPClient(), srv.URL)
	if !ok {
		t.Fatal("expected stash to be confirmed even when auth is required")
	}
	if f.Service != "stash" || f.URL != srv.URL {
		t.Errorf("unexpected finding: %+v", f)
	}
	if !strings.Contains(strings.ToLower(f.Label), "stash") {
		t.Errorf("label %q should mention stash", f.Label)
	}
}

func TestProbeStash_RejectsNonJSONResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte("<html>not stash</html>"))
	}))
	defer srv.Close()
	if _, ok := probeStash(context.Background(), testHTTPClient(), srv.URL); ok {
		t.Fatal("a non-JSON response should not confirm Stash")
	}
}

// TestProbeService_WrongServiceDoesNotConfirm proves the probes are specific:
// pointing the qBittorrent probe at a Prowlarr server (or vice-versa) yields
// no confirmation.
func TestProbeService_WrongServiceDoesNotConfirm(t *testing.T) {
	prowlarr := probeServer(t, "prowlarr")
	if _, ok := probeQBittorrent(context.Background(), testHTTPClient(), prowlarr.URL); ok {
		t.Error("qBittorrent probe confirmed against a Prowlarr server")
	}
	if _, ok := probeJellyfin(context.Background(), testHTTPClient(), prowlarr.URL); ok {
		t.Error("Jellyfin probe confirmed against a Prowlarr server")
	}
}

func TestFetchProwlarrAPIKey_ReturnsKey(t *testing.T) {
	srv := probeServer(t, "prowlarr")
	key, err := FetchProwlarrAPIKey(context.Background(), testHTTPClient(), srv.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if key != "SUPERSECRETPROWLARRKEY" {
		t.Fatalf("expected the live key, got %q", key)
	}
}

func TestFetchProwlarrAPIKey_ErrorsWhenNoKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"instanceName":"Prowlarr"}`))
	}))
	defer srv.Close()
	if _, err := FetchProwlarrAPIKey(context.Background(), testHTTPClient(), srv.URL); err == nil {
		t.Fatal("expected an error when no apiKey is present")
	}
}

// TestFetchProwlarrAPIKey_RefusesPublicURL guards the SSRF gap ProbeHost
// already closes: without this check, an authenticated caller could point
// this function (and its API handler) at an arbitrary public URL and get
// back whatever JSON field is named apiKey. No real request should ever be
// attempted against the public host.
func TestFetchProwlarrAPIKey_RefusesPublicURL(t *testing.T) {
	if _, err := FetchProwlarrAPIKey(context.Background(), testHTTPClient(), "http://8.8.8.8:9696"); err == nil {
		t.Fatal("expected an error fetching a key from a public URL")
	}
}

// TestValidatePrivateHost covers the SSRF guardrail: private/loopback/
// link-local accepted, public rejected — all with literal IPs so no DNS is
// touched.
func TestValidatePrivateHost(t *testing.T) {
	cases := []struct {
		host    string
		allowed bool
	}{
		{"192.168.1.5", true},
		{"10.0.0.5", true},
		{"172.16.4.4", true},
		{"127.0.0.1", true},
		{"169.254.10.10", true},
		{"::1", true},
		{"fc00::1", true},
		{"8.8.8.8", false},
		{"1.1.1.1", false},
		{"93.184.216.34", false}, // example.com's public IP
	}
	for _, c := range cases {
		err := validatePrivateHost(context.Background(), c.host)
		if c.allowed && err != nil {
			t.Errorf("%s: expected allowed, got error: %v", c.host, err)
		}
		if !c.allowed && err == nil {
			t.Errorf("%s: expected rejected, got no error", c.host)
		}
	}
}

func TestProbeHost_RejectsPublicIPBeforeProbing(t *testing.T) {
	// 8.8.8.8 must be refused outright, with no outbound request attempted.
	findings, err := ProbeHost(context.Background(), testHTTPClient(), "8.8.8.8")
	if err == nil {
		t.Fatal("expected ProbeHost to refuse a public IP")
	}
	if findings != nil {
		t.Errorf("expected no findings on refusal, got %+v", findings)
	}
}

func TestProbeHost_EmptyHost(t *testing.T) {
	if _, err := ProbeHost(context.Background(), testHTTPClient(), "   "); err == nil {
		t.Fatal("expected an error for an empty host")
	}
}

// TestProbeHost_PrivateIPIsProbed proves ProbeHost gets past the guardrail for
// a private IP and actually attempts probes (they simply find nothing on a
// loopback address with no service on the four default ports).
func TestProbeHost_PrivateIPIsProbed(t *testing.T) {
	findings, err := ProbeHost(context.Background(), testHTTPClient(), "127.0.0.1")
	if err != nil {
		t.Fatalf("a private IP should not be refused: %v", err)
	}
	// Nothing is expected to be listening on the four default service ports
	// during the test, so an empty result is the normal outcome — the point
	// is that it did NOT error out at the guardrail.
	_ = findings
}

func TestProbeKnownHosts_UnresolvableHostsAreSilentNegatives(t *testing.T) {
	// In the test environment the conventional container hostnames don't
	// resolve, so this returns an empty slice without error or panic.
	findings := ProbeKnownHosts(context.Background(), testHTTPClient())
	if len(findings) != 0 {
		t.Errorf("expected no findings for unresolvable container hostnames, got %+v", findings)
	}
}

func TestNormalizeHost(t *testing.T) {
	cases := map[string]string{
		"192.168.1.5":              "192.168.1.5",
		"http://192.168.1.5":       "192.168.1.5",
		"http://192.168.1.5:8096":  "192.168.1.5",
		"https://jelly.lan/System": "jelly.lan",
		"jelly.lan:8096":           "jelly.lan",
		"  10.0.0.5  ":             "10.0.0.5",
	}
	for in, want := range cases {
		if got := normalizeHost(in); got != want {
			t.Errorf("normalizeHost(%q) = %q, want %q", in, got, want)
		}
	}
}

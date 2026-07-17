// Package netscan probes the local network for the download/indexer/player
// services SAK talks to (Prowlarr, qBittorrent, NZBGet, Jellyfin),
// so the setup wizard can offer to pre-fill a connection's URL instead of
// making the operator type it by hand.
//
// SECURITY POSTURE — every result is a HINT TO VERIFY, NEVER A TRUSTED FACT.
// Each service here is identified by an UNAUTHENTICATED endpoint (Prowlarr's
// /initialize.json, qBittorrent's
// webapiVersion, NZBGet's Server header, Jellyfin's /System/Info/Public). Any
// host reachable on the same network can serve a fake response and
// impersonate one of these — so a Finding only ever says "possible X
// instance," and nothing here auto-saves a connection or treats a URL as
// confirmed. That is why the returned Finding type carries no credential of
// any kind: even for Prowlarr, whose /initialize.json exposes a live
// API key in plaintext, this package deliberately decodes only the identity
// field and discards the key. Retrieving the key is a separate, explicit
// operator action (FetchProwlarrAPIKey), never bundled into a probe.
//
// There is deliberately NO subnet/CIDR sweep and no worker pool: only a short
// fixed list of conventional container hostnames (ProbeKnownHosts) and a
// single operator-supplied host (ProbeHost, which refuses anything that does
// not resolve to a private/RFC1918 address, so SAK can't be used as an
// SSRF/scanning pivot against arbitrary public hosts).
package netscan

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	neturl "net/url"
	"strings"
	"time"

	"github.com/curtiswtaylorjr/sakms/internal/httpx"
)

// probeTimeout bounds every individual probe. These services are LAN-local
// (usually sakms's own docker network), so a healthy one answers in
// milliseconds — a short cap keeps the wizard from hanging on a host that
// resolves but has a filtered port.
const probeTimeout = 2 * time.Second

// Finding is a hint to verify, never a trusted fact — an unauthenticated
// network identity response is spoofable by anything reachable on the same
// network. It never includes credentials (see the package doc for why, even
// for Prowlarr).
type Finding struct {
	Service string `json:"service"` // "prowlarr" | "qbittorrent" | "nzbget" | "jellyfin" | "stash"
	URL     string `json:"url"`
	Label   string `json:"label"` // e.g. "possible Prowlarr instance" — for UI display
}

// knownService is one of the four services SAK knows how to identify, paired
// with the conventional container hostname and default port it's reached at
// on sakms's own docker network.
type knownService struct {
	name string
	port int
}

// knownServices is the fixed probe set — no arbitrary ports, no arbitrary
// services. The hostname doubles as the conventional container name embedded
// DNS resolves inside the compose network.
var knownServices = []knownService{
	{name: "prowlarr", port: 9696},
	{name: "qbittorrent", port: 8080},
	{name: "nzbget", port: 6789},
	{name: "jellyfin", port: 8096},
	{name: "stash", port: 9999},
}

// ProbeKnownHosts tries each known service at its conventional container
// hostname + default port (e.g. http://prowlarr:9696). A hostname that
// doesn't resolve is an expected negative (that service just isn't on this
// network), not an error — so this returns only the Findings that confirmed,
// never an error.
func ProbeKnownHosts(ctx context.Context, httpClient *http.Client) []Finding {
	return probeAll(ctx, httpClient, func(svc knownService) string {
		return fmt.Sprintf("http://%s:%d", svc.name, svc.port)
	})
}

// ProbeHost probes exactly one operator-supplied host across the four known
// services' default ports. It REFUSES — before making any outbound request —
// any host that doesn't resolve to a private/RFC1918 (or loopback/link-local)
// address, which is the guardrail against SAK being used as an SSRF or
// port-scanning pivot against arbitrary public hosts. host is a bare
// hostname or IP (a scheme/port, if present, is stripped — the ports probed
// are always the four known defaults, never an operator-chosen one).
func ProbeHost(ctx context.Context, httpClient *http.Client, host string) ([]Finding, error) {
	host = normalizeHost(host)
	if host == "" {
		return nil, errors.New("host is required")
	}
	if err := validatePrivateHost(ctx, host); err != nil {
		return nil, err
	}
	return probeAll(ctx, httpClient, func(svc knownService) string {
		return fmt.Sprintf("http://%s:%d", host, svc.port)
	}), nil
}

// probeAll fans out one probe per known service concurrently — so total
// latency is bounded to roughly one probeTimeout rather than the sum of
// four — and collects only the Findings that confirmed. baseURLFor supplies
// the host half of each service's URL: ProbeKnownHosts varies it per-service
// (each service's own conventional hostname), ProbeHost holds it fixed at the
// one operator-supplied host; the fan-out/collect shape is otherwise
// identical, so it's factored out once here.
func probeAll(ctx context.Context, httpClient *http.Client, baseURLFor func(knownService) string) []Finding {
	type indexed struct {
		i  int
		f  Finding
		ok bool
	}
	ch := make(chan indexed, len(knownServices))
	for i, svc := range knownServices {
		go func(i int, svc knownService) {
			f, ok := probeService(ctx, httpClient, svc.name, baseURLFor(svc))
			ch <- indexed{i: i, f: f, ok: ok}
		}(i, svc)
	}

	results := make([]Finding, len(knownServices))
	got := make([]bool, len(knownServices))
	for range knownServices {
		r := <-ch
		results[r.i] = r.f
		got[r.i] = r.ok
	}

	var out []Finding
	for i := range results {
		if got[i] {
			out = append(out, results[i])
		}
	}
	return out
}

// probeService dispatches to the right per-service probe. A plain switch (not
// an interface) matches this codebase's parallel-sibling-functions convention
// for external clients — each service confirms identity differently (a JSON
// field, a bare version string, a response header), so there's no shared
// contract worth abstracting.
func probeService(ctx context.Context, httpClient *http.Client, name, baseURL string) (Finding, bool) {
	switch name {
	case "prowlarr":
		return probeProwlarr(ctx, httpClient, baseURL)
	case "qbittorrent":
		return probeQBittorrent(ctx, httpClient, baseURL)
	case "nzbget":
		return probeNZBGet(ctx, httpClient, baseURL)
	case "jellyfin":
		return probeJellyfin(ctx, httpClient, baseURL)
	case "stash":
		return probeStash(ctx, httpClient, baseURL)
	}
	return Finding{}, false
}

// probeProwlarr confirms a Prowlarr instance via its unauthenticated
// /initialize.json. CRITICAL: that endpoint also returns a live API key in
// plaintext, but this decodes ONLY instanceName — the key is never read into
// memory here, so the returned Finding is provably credential-free. Fetching
// the key is a separate, explicit action (FetchProwlarrAPIKey). The
// instanceName field is matched case-insensitively to confirm identity (not
// just "something Servarr-shaped is listening").
func probeProwlarr(ctx context.Context, httpClient *http.Client, baseURL string) (Finding, bool) {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/initialize.json", nil)
	if err != nil {
		return Finding{}, false
	}
	// Only instanceName is decoded — apiKey is deliberately absent from this
	// struct so it's never even held in memory (see the func doc).
	var payload struct {
		InstanceName string `json:"instanceName"`
	}
	if err := httpx.DoJSON(httpClient, req, httpx.MaxResponseBodySize, &payload); err != nil {
		return Finding{}, false
	}
	if !strings.EqualFold(payload.InstanceName, "Prowlarr") {
		return Finding{}, false
	}
	return Finding{Service: "prowlarr", URL: baseURL, Label: "possible Prowlarr instance"}, true
}

// probeQBittorrent confirms a qBittorrent instance via its unauthenticated
// webapiVersion endpoint, which returns a bare version string (e.g.
// "2.15.1") — NOT JSON, so this reads the raw body rather than going through
// httpx.DoJSON. The leading-digit/version-shape check guards against any
// arbitrary 200-returning server being mistaken for qBittorrent.
func probeQBittorrent(ctx context.Context, httpClient *http.Client, baseURL string) (Finding, bool) {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/api/v2/app/webapiVersion", nil)
	if err != nil {
		return Finding{}, false
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return Finding{}, false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Finding{}, false
	}
	// A version string is tiny; cap the read hard.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64))
	if err != nil {
		return Finding{}, false
	}
	v := strings.TrimSpace(string(body))
	if !looksLikeVersion(v) {
		return Finding{}, false
	}
	return Finding{Service: "qbittorrent", URL: baseURL, Label: "possible qBittorrent instance"}, true
}

// looksLikeVersion is a cheap sanity filter for qBittorrent's bare version
// string: non-empty, short, and starting with a digit. Not a strict semver
// parse — just enough to reject an arbitrary server that happens to answer
// 200 on that path.
func looksLikeVersion(v string) bool {
	if v == "" || len(v) > 32 {
		return false
	}
	return v[0] >= '0' && v[0] <= '9'
}

// probeNZBGet confirms an NZBGet instance via its root response's Server
// header, which is literally "nzbget-<version>" (e.g. "nzbget-26.2"). This
// deliberately does NOT require a 2xx status and does NOT parse the body as
// JSON: a real NZBGet root can answer 401 (or return HTML) while still
// emitting the Server header — the header IS the identity signal.
func probeNZBGet(ctx context.Context, httpClient *http.Client, baseURL string) (Finding, bool) {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/", nil)
	if err != nil {
		return Finding{}, false
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return Finding{}, false
	}
	// Drain a bounded amount so the connection can be reused, then close.
	defer func() {
		io.Copy(io.Discard, io.LimitReader(resp.Body, httpx.MaxResponseBodySize))
		resp.Body.Close()
	}()

	if !strings.HasPrefix(strings.ToLower(resp.Header.Get("Server")), "nzbget") {
		return Finding{}, false
	}
	return Finding{Service: "nzbget", URL: baseURL, Label: "possible NZBGet instance"}, true
}

// probeJellyfin confirms a Jellyfin instance via its unauthenticated
// /System/Info/Public endpoint (NOT the auth-gated /System/Info the
// internal/jellyfin client's Ping uses — see that client's doc comment for
// why Ping deliberately avoids the public one). A non-empty Version is the
// identity signal.
func probeJellyfin(ctx context.Context, httpClient *http.Client, baseURL string) (Finding, bool) {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/System/Info/Public", nil)
	if err != nil {
		return Finding{}, false
	}
	var payload struct {
		ID      string `json:"Id"`
		Version string `json:"Version"`
	}
	if err := httpx.DoJSON(httpClient, req, httpx.MaxResponseBodySize, &payload); err != nil {
		return Finding{}, false
	}
	if payload.Version == "" {
		return Finding{}, false
	}
	return Finding{Service: "jellyfin", URL: baseURL, Label: "possible Jellyfin instance"}, true
}

// probeStash confirms a Stash instance via its /graphql endpoint. Stash's
// GraphQL always responds with a JSON envelope — even when authentication is
// required the response is a valid {"errors":[...]} object, not HTML. The
// combination of a JSON Content-Type header and a body that opens with '{'
// is sufficient to distinguish Stash from arbitrary HTTP servers on port 9999.
// The API key is never available unauthenticated; this probe only confirms the
// URL — the operator must retrieve their API key from Stash → Settings →
// Security.
func probeStash(ctx context.Context, httpClient *http.Client, baseURL string) (Finding, bool) {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	body := strings.NewReader(`{"query":"{__typename}"}`)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/graphql", body)
	if err != nil {
		return Finding{}, false
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return Finding{}, false
	}
	defer func() {
		io.Copy(io.Discard, io.LimitReader(resp.Body, httpx.MaxResponseBodySize))
		resp.Body.Close()
	}()

	if !strings.Contains(resp.Header.Get("Content-Type"), "application/json") {
		return Finding{}, false
	}
	peek, err := io.ReadAll(io.LimitReader(resp.Body, 64))
	if err != nil {
		return Finding{}, false
	}
	if !strings.HasPrefix(strings.TrimSpace(string(peek)), "{") {
		return Finding{}, false
	}
	return Finding{Service: "stash", URL: baseURL, Label: "possible Stash instance"}, true
}

// FetchProwlarrAPIKey re-fetches a Prowlarr instance's /initialize.json fresh
// from url specifically to retrieve the live API key it exposes in plaintext.
// This is SEPARATE from the probes on purpose: it's the only path that reads
// the key, and it runs ONLY when the operator explicitly asks for it (a
// dedicated button/route) — never bundled into a discovery response, never
// fired automatically. The returned key is still a hint to verify: it's
// whatever the host at url served, which is only trustworthy if url really is
// the operator's own instance.
//
// url is validated the same way ProbeHost validates a host — must resolve to
// a private/LAN address — before any request is made. Unlike ProbeHost/
// ProbeKnownHosts (which only ever construct URLs themselves from a fixed
// hostname list), this function's url comes from the caller, so without this
// check an authenticated operator could point sakms's server at an arbitrary
// URL and get back whatever JSON field is named apiKey — a limited but real
// SSRF primitive worth closing even though the caller is already
// authenticated.
func FetchProwlarrAPIKey(ctx context.Context, httpClient *http.Client, rawURL string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	parsed, err := neturl.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("invalid URL: %w", err)
	}
	if err := validatePrivateHost(ctx, parsed.Hostname()); err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(rawURL, "/")+"/initialize.json", nil)
	if err != nil {
		return "", fmt.Errorf("building request: %w", err)
	}
	var payload struct {
		InstanceName string `json:"instanceName"`
		APIKey       string `json:"apiKey"`
	}
	if err := httpx.DoJSON(httpClient, req, httpx.MaxResponseBodySize, &payload); err != nil {
		return "", err
	}
	if payload.APIKey == "" {
		return "", fmt.Errorf("no API key found at that URL — is it really a Prowlarr instance?")
	}
	return payload.APIKey, nil
}

// normalizeHost strips a scheme and/or port from an operator-typed host,
// leaving the bare hostname or IP the private-address check and the probe
// URLs are built from.
func normalizeHost(host string) string {
	host = strings.TrimSpace(host)
	if host == "" {
		return ""
	}
	// Strip a scheme if the operator pasted a full URL.
	if i := strings.Index(host, "://"); i >= 0 {
		host = host[i+3:]
	}
	// Drop any path.
	if i := strings.IndexByte(host, '/'); i >= 0 {
		host = host[:i]
	}
	// Strip a trailing port if present (SplitHostPort handles the IPv6
	// bracket form; a bare host or bracketless IPv6 literal fails and is
	// kept as-is).
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return strings.Trim(host, "[]")
}

// validatePrivateHost resolves host and refuses unless EVERY resolved address
// is private/RFC1918, loopback, or link-local. A single public address is
// enough to reject — this is what stops ProbeHost from being pointed at an
// arbitrary internet host. Runs before any probe request is made.
func validatePrivateHost(ctx context.Context, host string) error {
	if ip := net.ParseIP(host); ip != nil {
		if !isPrivateIP(ip) {
			return fmt.Errorf("refusing to probe %q: only private/LAN addresses are allowed", host)
		}
		return nil
	}

	resolver := net.DefaultResolver
	ips, err := resolver.LookupIP(ctx, "ip", host)
	if err != nil {
		return fmt.Errorf("could not resolve %q: %w", host, err)
	}
	if len(ips) == 0 {
		return fmt.Errorf("could not resolve %q to any address", host)
	}
	for _, ip := range ips {
		if !isPrivateIP(ip) {
			return fmt.Errorf("refusing to probe %q: it resolves to a public address (%s) — only private/LAN addresses are allowed", host, ip)
		}
	}
	return nil
}

// isPrivateIP reports whether ip is safe for ProbeHost to reach: RFC1918 /
// ULA (IsPrivate), loopback, or link-local. Anything else (a routable public
// address) is refused.
func isPrivateIP(ip net.IP) bool {
	return ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast()
}

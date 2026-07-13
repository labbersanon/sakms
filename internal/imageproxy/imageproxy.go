// Package imageproxy server-side-fetches and caches poster/thumbnail art from
// a small, fixed allowlist of external image hosts (TMDB for Movies/Series,
// ThePornDB for Adult), so the browser never hot-links those hosts directly.
//
// SECURITY POSTURE — inverted from internal/netscan, same discipline. netscan
// probes must REJECT public hosts (SSRF-out guardrail); this proxy must reject
// EVERYTHING EXCEPT a fixed set of allowed external image hosts, for the same
// reason: an authenticated operator (or a crafted page) must not be able to
// point SAK's server at an arbitrary URL and have it fetch that on the
// server's behalf — otherwise this becomes an open SSRF/exfil proxy. So the
// allowlist is closed by default: an upstream URL is fetched only if its
// scheme is https AND its host is one of the allowlisted image hosts. TMDB is
// pinned to its exact single image host; TPDB is scoped to the registrable
// domains ThePornDB itself owns (any subdomain), because TPDB shuffles image
// CDN subdomains and pinning one would silently break Adult posters — allowing
// all subdomains of a domain TPDB wholly controls does not widen the SSRF
// surface to any host an attacker controls. Dot-anchoring on the suffix match
// stops "image.tmdb.org.evil.com" / "evilmetadataapi.net" style bypasses.
//
// TPDB IMAGE HOST (honesty-about-guesses convention, project CLAUDE.md):
// the repo now models the TPDB scene image field (tpdbrest.Scene.Image /
// api.adultScene.Image / apidto.AdultDiscoverItem.Image, the scene object's
// flat "image" field). TPDB serves that art from its own image CDN —
// cdn.theporndb.net today, cdn.metadataapi.net historically — both of which
// are subdomains of the two owned domains allowlisted below, so this list
// covers the real host with no change needed. This was determined from TPDB's
// documented v2 scene shape and the community/Jellyfin/Plex TPDB agents (which
// read art from those CDN subdomains), NOT confirmed against a live
// authenticated TPDB instance in-repo. If TPDB ever moves scene art onto a
// third-party CDN (imgix/fastly/bunny) it would need a new domainHost entry;
// until then, Adult posters resolve through the allowlist as-is.
package imageproxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// maxImageBytes caps how much of an upstream image body this proxy reads into
// memory before giving up — a defensive bound against an allowlisted-but-
// misbehaving host streaming an unbounded body. Poster/thumbnail art is well
// under this; TMDB's largest originals are a couple MB.
const maxImageBytes = 10 * 1024 * 1024

// ErrInvalidURL is returned for a missing/malformed/non-https upstream URL —
// caller maps this to a 400, not a 502 (the operator sent a bad request, no
// upstream was ever contacted).
var ErrInvalidURL = errors.New("invalid image URL")

// ErrHostNotAllowed is returned when a syntactically valid https URL points at
// a host outside the allowlist — the core SSRF guardrail. Caller maps to 400.
// A redirect whose target is off-allowlist also fails wrapping this sentinel
// (see newGuardedClient): the allowlist is enforced on every hop, not just the
// initial URL.
var ErrHostNotAllowed = errors.New("image host is not on the allowlist")

// maxRedirects caps redirect-following, mirroring net/http's own default of 10
// (which only applies when CheckRedirect is nil — supplying our own guard opts
// out of that default, so we re-impose the same bound).
const maxRedirects = 10

// hostRule matches one allowed host. exact matches a single host verbatim
// (TMDB's one image host); suffix matches a registrable domain and all its
// subdomains, dot-anchored so only genuine subdomains qualify (TPDB's owned
// domains, whose image-CDN subdomains vary).
type hostRule struct {
	value   string
	isExact bool
}

func exactHost(h string) hostRule  { return hostRule{value: h, isExact: true} }
func domainHost(d string) hostRule { return hostRule{value: d, isExact: false} }

func (r hostRule) matches(host string) bool {
	if r.isExact {
		return host == r.value
	}
	return host == r.value || strings.HasSuffix(host, "."+r.value)
}

// defaultHosts is the production allowlist: TMDB's exact image host, plus
// TPDB's two owned domain families (any subdomain). See the package doc for
// why TMDB is pinned exact and TPDB is domain-scoped, and for the unverified-
// TPDB-host caveat.
var defaultHosts = []hostRule{
	exactHost("image.tmdb.org"),
	domainHost("metadataapi.net"),
	domainHost("theporndb.net"),
}

// Validate parses raw and returns it only if it is an https URL whose host is
// on the production allowlist. It is a pure allowlist check with no I/O — the
// SSRF gate, tested directly. A rejected URL yields ErrInvalidURL (malformed/
// non-https) or ErrHostNotAllowed (valid https, wrong host).
func Validate(raw string) (*url.URL, error) {
	return validate(raw, defaultHosts)
}

func validate(raw string, hosts []hostRule) (*url.URL, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, fmt.Errorf("%w: empty url", ErrInvalidURL)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidURL, err)
	}
	// Require https specifically: http would let a plaintext MITM on the LAN
	// swap poster bytes, and non-http(s) schemes (file://, gopher://, data:)
	// are exactly the SSRF/exfil vectors this gate exists to close.
	if u.Scheme != "https" {
		return nil, fmt.Errorf("%w: scheme %q is not https", ErrInvalidURL, u.Scheme)
	}
	host := u.Hostname() // strips any :port; also lowercases nothing, so normalize below
	host = strings.ToLower(host)
	if host == "" {
		return nil, fmt.Errorf("%w: no host", ErrInvalidURL)
	}
	for _, rule := range hosts {
		if rule.matches(host) {
			return u, nil
		}
	}
	return nil, fmt.Errorf("%w: %q", ErrHostNotAllowed, host)
}

// Image is one fetched image: its raw bytes and upstream content type.
// FromCache reports whether this Fetch was served from the in-memory cache
// without a fresh upstream round-trip (used by tests and available to the
// handler for diagnostics).
type Image struct {
	Body        []byte
	ContentType string
	FromCache   bool
}

// Proxy validates, fetches, and caches images from the allowlist. Construct
// one per process (New) — its cache is a singleton whose lifetime is the
// process, so a poster requested during one grid render is not re-fetched from
// TMDB/TPDB on the next.
type Proxy struct {
	client *http.Client
	cache  *cache
	hosts  []hostRule
}

// New returns a Proxy that allows the production TMDB + TPDB image hosts, with
// a default cache size/TTL. client is the shared outbound HTTP client (its
// timeout bounds each image fetch); New does NOT use it directly — it derives a
// dedicated redirect-guarded client from it (see newGuardedClient), so the
// SSRF allowlist is enforced on redirects too without changing redirect
// behavior for the shared client's other callers.
func New(client *http.Client) *Proxy {
	return &Proxy{
		client: newGuardedClient(client, defaultHosts),
		cache:  newCache(defaultCacheCap, defaultCacheTTL),
		hosts:  defaultHosts,
	}
}

// newGuardedClient returns a dedicated *http.Client for image fetching: a
// shallow copy of base (preserving its Timeout/Transport) whose CheckRedirect
// re-runs the allowlist check against every redirect target. This is the fix
// for the SSRF hole in the naive design: net/http's default client re-validates
// nothing on a 3xx, so an allowlisted upstream (TMDB / a TPDB CDN) that ever
// redirected to an internal address would be followed server-side, defeating
// the allowlist. CRITICAL: base is copied, never mutated — the caller's client
// is the process-wide outbound client shared by TMDB/Prowlarr/other calls, and
// those must keep net/http's default redirect behavior. hosts is the same
// allowlist the Proxy validates initial URLs against, so a legitimate redirect
// between allowlisted hosts still follows.
func newGuardedClient(base *http.Client, hosts []hostRule) *http.Client {
	guarded := &http.Client{}
	if base != nil {
		clone := *base // shallow copy: keep Timeout, Transport, Jar; do not touch base
		guarded = &clone
	}
	guarded.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= maxRedirects {
			return fmt.Errorf("stopped after %d redirects", maxRedirects)
		}
		if _, err := validate(req.URL.String(), hosts); err != nil {
			return fmt.Errorf("refusing off-allowlist redirect to %q: %w", req.URL.Host, err)
		}
		return nil
	}
	return guarded
}

// newTestProxy builds a Proxy whose allowlist and cache are caller-supplied —
// used only by this package's tests to point Fetch at an httptest upstream
// (the production allowlist rejects 127.0.0.1). Kept unexported so no test-only
// allowlist bypass leaks into the production API surface.
func newTestProxy(client *http.Client, c *cache, hosts []hostRule) *Proxy {
	return &Proxy{client: newGuardedClient(client, hosts), cache: c, hosts: hosts}
}

// Fetch validates raw against the allowlist, returns a cached copy if one is
// live, otherwise fetches the upstream image verbatim (query string included —
// TPDB/imgix art often carries sizing params), caches it, and returns it. Only
// a 2xx response with an image/* content type is cached or returned; anything
// else is a fetch error (never cached, so a transient upstream failure is not
// pinned for the whole TTL).
func (p *Proxy) Fetch(ctx context.Context, raw string) (*Image, error) {
	u, err := validate(raw, p.hosts)
	if err != nil {
		return nil, err
	}
	// Cache key is the full validated URL string (scheme+host+path+query) so
	// two sizes of the same poster don't collide.
	key := u.String()
	if img, ok := p.cache.get(key); ok {
		return &Image{Body: img.Body, ContentType: img.ContentType, FromCache: true}, nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, key, nil)
	if err != nil {
		return nil, fmt.Errorf("building image request: %w", err)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		// On a refused redirect, net/http returns the last (3xx) response
		// alongside the error; close it so the guard doesn't leak a body.
		if resp != nil {
			resp.Body.Close()
		}
		return nil, fmt.Errorf("fetching %s: %w", u.Host, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%s returned status %d for image", u.Host, resp.StatusCode)
	}
	ct := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(ct)), "image/") {
		// Defense in depth: even an allowlisted host must not be used to
		// proxy non-image content back to the browser.
		return nil, fmt.Errorf("%s returned non-image content type %q", u.Host, ct)
	}

	// LimitReader to maxImageBytes+1 so an over-cap body is detected rather
	// than silently truncated into a corrupt image.
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxImageBytes+1))
	if err != nil {
		return nil, fmt.Errorf("reading image from %s: %w", u.Host, err)
	}
	if len(body) > maxImageBytes {
		return nil, fmt.Errorf("image from %s exceeds %d bytes", u.Host, maxImageBytes)
	}

	img := &Image{Body: body, ContentType: ct}
	p.cache.put(key, img)
	return img, nil
}

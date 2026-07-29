// Package imageproxy server-side-fetches and caches poster/thumbnail art for
// Movies/Series/Adult, so the browser never hot-links an external image host
// directly.
//
// SECURITY POSTURE — inverted from internal/netscan, same discipline. netscan
// probes must REJECT public hosts (SSRF-out guardrail); this proxy must reject
// requests that would let SAK's server be used as an open SSRF/exfil proxy —
// an authenticated operator (or a crafted page, or a compromised/malicious
// TPDB catalog entry) must not be able to point SAK's server at an internal
// address and have it fetch that on the server's behalf.
//
// THE ALLOWLIST WAS REMOVED 2026-07-15 (was: a small fixed domain list —
// TMDB's exact image host, plus TPDB's owned domains). Real production
// evidence showed that assumption was wrong: a TPDB scene's "image" field
// (tpdbrest.Scene.Image) does NOT point at a TPDB-owned CDN — it points
// directly at whichever third-party host originally hosted the scene's
// promotional art (e.g. a studio's own domain like "cruel-handjobs.com", or a
// shared CDN like "*.st-content.com"), a different, effectively unbounded
// host per studio. A fixed domain allowlist structurally cannot cover this;
// every Adult poster 400'd. The guardrail is now IP-range-based instead:
// any https URL is fetched, UNLESS it (or a redirect target) resolves to a
// private/loopback/link-local/unspecified/multicast address — see
// isBlockedIP and validateHostNotPrivate. This is the standard SSRF-safe
// pattern for proxying arbitrary externally-sourced URLs (the same posture
// internal/netscan already uses, inverted — see isBlockedIP's doc for the
// direct comparison to netscan.isPrivateIP).
//
// Scheme stays https-only (unchanged): a real TPDB example was observed as
// plain "http://", which this proxy still refuses — that specific poster
// degrades to the existing text-fallback card rather than relaxing to allow
// plaintext fetches (an operator-visible poster is not worth the MITM/
// downgrade exposure of accepting http).
//
// KNOWN RESIDUAL RISK (honesty-about-limitations convention, project
// CLAUDE.md): validateHostNotPrivate's resolve-then-check is not atomic with
// the actual outbound connection — a host under attacker control with a very
// low DNS TTL could resolve publicly at validation time and privately by the
// time the HTTP client actually dials (DNS rebinding). internal/netscan's
// existing validatePrivateHost carries the same residual gap; closing it
// fully would need a custom dial-time net.Dialer.Control hook, which neither
// package implements today. Flagging honestly rather than presenting this as
// airtight.
package imageproxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
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
// a host this proxy refuses to fetch — the core SSRF guardrail. Caller maps to
// 400. A redirect whose target is refused also fails wrapping this sentinel
// (see newGuardedClient): the guard is enforced on every hop, not just the
// initial URL. Despite the name (kept for API/doc-comment continuity — see
// images.go's error-mapping switch), this is no longer a fixed allowlist; see
// the package doc for the 2026-07-15 change to an IP-range guardrail.
var ErrHostNotAllowed = errors.New("image host resolves to a private/internal address")

// maxRedirects caps redirect-following, mirroring net/http's own default of 10
// (which only applies when CheckRedirect is nil — supplying our own guard opts
// out of that default, so we re-impose the same bound).
const maxRedirects = 10

// isBlockedIP reports whether ip must never be dialed by this proxy. Mirrors
// internal/netscan.isPrivateIP's exact RFC1918/ULA/loopback/link-local
// rationale (see that function's doc) — same address classes, opposite
// direction: netscan only ALLOWS these ranges (a LAN-probing tool must stay
// off the public internet), this proxy only REJECTS them (an image-fetching
// tool must stay off internal addresses). Two additions beyond netscan's set,
// both cheap and directly relevant to a server-side fetch specifically:
// IsLinkLocalMulticast (paired with IsLinkLocalUnicast for a complete
// 169.254.0.0/16 block — the cloud-metadata-endpoint attack vector) and
// IsUnspecified (0.0.0.0 / ::, which some platforms route to localhost).
func isBlockedIP(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast()
}

// validateHostNotPrivate resolves host (a bare hostname or IP literal, no
// port — already stripped by validate) and rejects it unless every resolved
// address is a routable public address. This is the SSRF guardrail itself;
// see the package doc for why it replaced a fixed domain allowlist, and for
// the residual DNS-rebinding caveat this shares with
// internal/netscan.validatePrivateHost (whose resolve-then-check shape this
// mirrors, inverted).
func validateHostNotPrivate(ctx context.Context, host string) error {
	if ip := net.ParseIP(host); ip != nil {
		if isBlockedIP(ip) {
			return fmt.Errorf("%w: %q", ErrHostNotAllowed, host)
		}
		return nil
	}
	ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
	if err != nil {
		return fmt.Errorf("%w: could not resolve %q: %v", ErrHostNotAllowed, host, err)
	}
	if len(ips) == 0 {
		return fmt.Errorf("%w: %q did not resolve to any address", ErrHostNotAllowed, host)
	}
	for _, ip := range ips {
		if isBlockedIP(ip) {
			return fmt.Errorf("%w: %q resolves to a private/internal address (%s)", ErrHostNotAllowed, host, ip)
		}
	}
	return nil
}

// Validate parses raw and returns it only if it is an https URL whose host
// does not resolve to a private/internal address (see validateHostNotPrivate).
// Unlike its pre-2026-07-15 version, this performs real DNS I/O for a bare
// hostname (net.ParseIP short-circuits it for an IP literal) — no longer a
// pure check. TestIsBlockedIP and TestValidateHostNotPrivate_LiteralIPs cover
// the resolve step with IP literals only (no DNS touched, mirroring
// internal/netscan's own test convention); TestValidate_SchemeAndSyntax
// covers this function's own local checks the same DNS-free way. A rejected
// URL yields ErrInvalidURL (malformed/non-https) or ErrHostNotAllowed (valid
// https, private/unresolvable host).
func Validate(ctx context.Context, raw string) (*url.URL, error) {
	return validate(ctx, raw, nil)
}

// allowPrivateHosts is a test-only escape hatch (nil in production, see New):
// hostnames in this set skip validateHostNotPrivate entirely, letting tests
// point Fetch at an httptest server (which listens on the private 127.0.0.1)
// without weakening the production guardrail. Keyed by lowercase hostname
// with no port, matching validate's own host normalization.
func validate(ctx context.Context, raw string, allowPrivateHosts map[string]bool) (*url.URL, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, fmt.Errorf("%w: empty url", ErrInvalidURL)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidURL, err)
	}
	// Require https specifically: http would let a plaintext MITM on the LAN
	// swap poster bytes, and non-http(s) schemes (file://, gopher://, data:)
	// are exactly the SSRF/exfil vectors this gate exists to close. A real
	// TPDB scene was observed serving its image over plain http — that
	// poster degrades to the text-fallback card rather than this proxy
	// relaxing to allow plaintext fetches (see package doc).
	if u.Scheme != "https" {
		return nil, fmt.Errorf("%w: scheme %q is not https", ErrInvalidURL, u.Scheme)
	}
	host := u.Hostname() // strips any :port; also lowercases nothing, so normalize below
	host = strings.ToLower(host)
	if host == "" {
		return nil, fmt.Errorf("%w: no host", ErrInvalidURL)
	}
	if allowPrivateHosts[host] {
		return u, nil
	}
	if err := validateHostNotPrivate(ctx, host); err != nil {
		return nil, err
	}
	return u, nil
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

// Proxy validates, fetches, and caches images. Construct one per process
// (New) — its cache is a singleton whose lifetime is the process, so a poster
// requested during one grid render is not re-fetched from the same upstream
// host on the next.
type Proxy struct {
	client            *http.Client
	cache             *cache
	durable           *DurableStore   // nil = today's exact behavior (opt-in, see NewWithDurable)
	allowPrivateHosts map[string]bool // nil in production; test-only, see validate's doc
}

// New returns a Proxy with the production SSRF guardrail (see package doc)
// and a default cache size/TTL. client is the shared outbound HTTP client
// (its timeout bounds each image fetch); New does NOT use it directly — it
// derives a dedicated redirect-guarded client from it (see newGuardedClient),
// so the guardrail is enforced on redirects too without changing redirect
// behavior for the shared client's other callers.
func New(client *http.Client) *Proxy {
	return &Proxy{
		client: newGuardedClient(client, nil),
		cache:  newCache(defaultCacheCap, defaultCacheTTL),
	}
}

// NewWithDurable is New plus an optional durable store (see DurableStore): a
// process-persistent, on-disk image cache the Adult precompute cycle warms
// (FetchAndPersist) and the request path reads (Fetch) across restarts. durable
// may be nil, in which case the returned Proxy behaves byte-for-byte like New's
// — the durable path is fully opt-in, so callers/tests that don't wire one are
// unaffected. The in-memory LRU and SSRF guardrail are identical either way
// (this reuses New so that wiring stays in one place).
func NewWithDurable(client *http.Client, durable *DurableStore) *Proxy {
	p := New(client)
	p.durable = durable
	return p
}

// newGuardedClient returns a dedicated *http.Client for image fetching: a
// shallow copy of base (preserving its Timeout/Transport) whose CheckRedirect
// re-runs the SSRF check against every redirect target. This is the fix for
// the SSRF hole in the naive design: net/http's default client re-validates
// nothing on a 3xx, so an upstream that ever redirected to an internal
// address would be followed server-side, defeating the guardrail. CRITICAL:
// base is copied, never mutated — the caller's client is the process-wide
// outbound client shared by TMDB/Prowlarr/other calls, and those must keep
// net/http's default redirect behavior. allowPrivateHosts is the same
// test-only escape hatch Fetch's validate call uses, so a legitimate
// httptest redirect chain still follows in tests.
func newGuardedClient(base *http.Client, allowPrivateHosts map[string]bool) *http.Client {
	guarded := &http.Client{}
	if base != nil {
		clone := *base // shallow copy: keep Timeout, Transport, Jar; do not touch base
		guarded = &clone
	}
	guarded.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= maxRedirects {
			return fmt.Errorf("stopped after %d redirects", maxRedirects)
		}
		if _, err := validate(req.Context(), req.URL.String(), allowPrivateHosts); err != nil {
			return fmt.Errorf("refusing redirect to %q: %w", req.URL.Host, err)
		}
		return nil
	}
	return guarded
}

// Fetch validates raw against the SSRF guardrail, returns a cached copy if
// one is live, otherwise fetches the upstream image verbatim (query string
// included — TPDB/imgix art often carries sizing params), caches it, and
// returns it. Only a 2xx response with an image/* content type is cached or
// returned; anything else is a fetch error (never cached, so a transient
// upstream failure is not pinned for the whole TTL).
func (p *Proxy) Fetch(ctx context.Context, raw string) (*Image, error) {
	u, err := validate(ctx, raw, p.allowPrivateHosts)
	if err != nil {
		return nil, err
	}
	key := p.keyFor(u)

	// Durable store (opt-in): a hit returns immediately with no network call.
	// A durable Get error is a DB fault only — a missing/unreadable backing file
	// self-heals to a clean miss inside Get — so it is treated as a miss here and
	// falls through to the LRU/upstream path. A transient durable hiccup must
	// never break image serving that the nil-durable path handles fine, and this
	// keeps "durable miss → exactly one upstream call" honest. Fetch NEVER writes
	// to the durable store (only FetchAndPersist does).
	if p.durable != nil {
		if body, ct, found, gErr := p.durable.Get(ctx, key); gErr == nil && found {
			return &Image{Body: body, ContentType: ct, FromCache: true}, nil
		}
	}

	if img, ok := p.cache.get(key); ok {
		return &Image{Body: img.Body, ContentType: img.ContentType, FromCache: true}, nil
	}

	img, err := p.fetchUpstream(ctx, u)
	if err != nil {
		return nil, err
	}
	p.cache.put(key, img)
	return img, nil
}

// keyFor derives the single cache key both Fetch (read side) and
// FetchAndPersist (write side) use for durable + in-memory lookups: the full
// validated URL string (scheme+host+path+query, so two sizes of the same poster
// don't collide). DurableStore.Get/Has/Put hash this internally (their own
// shaOf) and store the raw URL as source_url provenance — so this MUST return
// the URL string, never a precomputed sha, or the durable write-key and
// read-key would diverge and source_url would hold a hash instead of a URL.
// Routing both paths through this one helper is what guarantees they can never
// key differently.
func (p *Proxy) keyFor(u *url.URL) string {
	return u.String()
}

// fetchUpstream performs the live upstream fetch for an already-validated URL:
// HTTP GET → 2xx status + image/* content-type check → size-capped body read →
// *Image. It does NO caching (neither the in-memory LRU nor the durable store) —
// the caller decides what to do with the result, so Fetch and FetchAndPersist
// share this SSRF/status/content-type/size logic verbatim without either's
// caching policy leaking into the other.
func (p *Proxy) fetchUpstream(ctx context.Context, u *url.URL) (*Image, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
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

	return &Image{Body: body, ContentType: ct}, nil
}

// FetchAndPersist warms the durable store for a single upstream image URL — the
// write side of the precompute cycle (US-4), the mirror of Fetch's read side.
// It validates raw with the SAME method-internal validate Fetch uses (honoring
// p.allowPrivateHosts, NOT the package-level Validate whose defaults hardcode
// nil), so an httptest CDN on 127.0.0.1 passes the SSRF gate in integration
// tests exactly as it does for Fetch. Flow:
//
//	if durable == nil { return nil }              // opt-in: no-op, error-free
//	validate(raw)                                 // same SSRF guard as Fetch
//	key := keyFor(u)
//	if durable.Has(key) { return nil }            // incremental skip, no CDN fetch
//	img := fetchUpstream(u)                        // same guarded path as Fetch
//	durable.Put(key, img.ContentType, img.Body)
//
// The durable==nil short-circuit is FIRST so callers that never wired a durable
// store can never be broken by this method (not even by a malformed URL) —
// durable warming is opt-in infrastructure. Any validation/fetch/put error is
// returned so the caller (precompute) can log-and-skip just that one item
// without failing the page, entity type, or cycle. The Has-skip check lives here
// (not in precompute) so key computation only ever happens on the method side
// against the validated URL — precompute passes raw card.Image and never touches
// keys, keeping the write-key/read-key invariant impossible to violate.
func (p *Proxy) FetchAndPersist(ctx context.Context, raw string) error {
	if p.durable == nil {
		return nil
	}
	u, err := validate(ctx, raw, p.allowPrivateHosts)
	if err != nil {
		return err
	}
	key := p.keyFor(u)
	if has, hErr := p.durable.Has(ctx, key); hErr == nil && has {
		return nil
	}
	img, err := p.fetchUpstream(ctx, u)
	if err != nil {
		return err
	}
	return p.durable.Put(ctx, key, img.ContentType, img.Body)
}

// MaintainDurableResult reports one cycle-start durable-maintenance pass. The
// three counts are the durable store's Reconcile drift tallies (for the cycle
// log); TotalBytes is the walk-based on-disk usage after pruning; OverCap says
// usage is still at/over the cap after PruneToCap ran — the signal the precompute
// cycle uses to pause NEW image warming for the remainder of that cycle (metadata
// caching is unaffected).
type MaintainDurableResult struct {
	StrayTemps  int
	OrphanRows  int
	OrphanFiles int
	TotalBytes  int64
	OverCap     bool
}

// MaintainDurable is the accessor the Adult precompute cycle calls at the START
// OF EVERY runCycle (boot poll and every tick — §2.5) so the durable store's
// self-heal + disk-cap enforcement live inside package imageproxy while the
// DurableStore itself stays unexposed to callers. It is a no-op returning a zero
// result when no durable store is wired (durable == nil), so the metadata-only
// (nil-proxy) precompute path is unaffected.
//
// Sequence (adjacent walks, run once per cycle before any image warming):
//
//	Reconcile(ctx)         // delete stray temps, orphan rows, orphan files
//	PruneToCap(ctx, cap)   // evict oldest LRU entries back under the cap
//	TotalBytes(ctx)        // walk-based usage; OverCap = usage >= cap
//
// Reconcile ALWAYS runs (index/disk truth must hold every cycle); PruneToCap and
// the OverCap decision only engage when capBytes > 0 (a non-positive cap means
// "no cap" — never prune everything). DurableStore.Reconcile/TotalBytes each do
// their own bounded tree walk (their shipped US-2 shape); calling them back-to-back
// here keeps that cost to one maintenance step at cycle start over a tree the cap
// already bounds.
func (p *Proxy) MaintainDurable(ctx context.Context, capBytes int64) (MaintainDurableResult, error) {
	var res MaintainDurableResult
	if p.durable == nil {
		return res, nil
	}
	strayTemps, orphanRows, orphanFiles, err := p.durable.Reconcile(ctx)
	res.StrayTemps, res.OrphanRows, res.OrphanFiles = strayTemps, orphanRows, orphanFiles
	if err != nil {
		return res, err
	}
	if capBytes > 0 {
		if err := p.durable.PruneToCap(ctx, capBytes); err != nil {
			return res, err
		}
	}
	total, err := p.durable.TotalBytes(ctx)
	if err != nil {
		return res, err
	}
	res.TotalBytes = total
	res.OverCap = capBytes > 0 && total >= capBytes
	return res, nil
}

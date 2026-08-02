// Package tpdbrest is a minimal client for ThePornDB's REST API — used as a
// fallback where its GraphQL endpoint (see internal/stashbox) doesn't cover a
// lookup (e.g. hash-based search), and for title text search.
package tpdbrest

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/labbersanon/sakms/internal/httpx"
	"github.com/labbersanon/sakms/internal/throttle"
)

// tpdbProdHost is ThePornDB's real REST API hostname — the single source of
// truth both DefaultBaseURL and the shared-limiter host gate derive from, so a
// future domain change updates the base URL and the gate together and can't
// silently disable production throttling by letting the two drift apart.
const tpdbProdHost = "api.theporndb.net"

// DefaultBaseURL is ThePornDB's single canonical REST API base (no trailing
// slash — the client builds paths as baseURL + "/scenes"). TPDB is a fixed
// public service, so callers hardcode this instead of a user-supplied
// Connection.URL, mirroring the TPDBGraphQLURL precedent (internal/mode).
// A var (not const) so tests can override it to point at an httptest fake;
// derived from the tpdbProdHost const (never an independent literal) so the
// host gate below always matches the real production base.
var DefaultBaseURL = "https://" + tpdbProdHost

// tpdbProdMinInterval is the process-wide minimum spacing between real-TPDB
// REST calls, baked into sharedLimiter at package init. TPDB documents no
// public rate limit, so this is a conservative empirical value (~5 req/s);
// Retry-After (see doGet) is the real safety net. Tunable in this one place.
const tpdbProdMinInterval = 200 * time.Millisecond

// defaultRetryAfterFloor is the minimum sleep before retrying a 429 when the
// response carries no usable Retry-After header. Deliberately DISTINCT from
// tpdbProdMinInterval and ~1s: if the retry collapsed to the sub-second
// inter-call spacing it would re-fire almost immediately, re-429, and exhaust
// the cap in well under a second — giving no benefit in exactly the
// no-header case it exists for. Overridable per-Client via WithRetryAfterFloor
// so tests assert the floor's behavior without eating a real second per run.
const defaultRetryAfterFloor = 1 * time.Second

// maxRetries bounds how many times doGet re-issues a request after a 429 (so at
// most maxRetries+1 total requests). Each retry re-acquires a limiter slot, so
// retries are themselves rate-limited — a retry storm is structurally
// impossible, not merely mitigated.
const maxRetries = 2

// sharedLimiter is the process-wide TPDB rate limiter every Client built with no
// options uses. Constructed once at package init with the production interval
// baked in — never reassigned, no exported setter — so there is no init-order
// race and no mutable global to synchronize (throttle's own mutex is the only
// lock). doGet gates its use on the real TPDB host (shouldThrottle), so every
// production caller of DefaultBaseURL is spaced while httptest hosts stay exempt.
var sharedLimiter = throttle.New(tpdbProdMinInterval)

type Client struct {
	baseURL string
	apiKey  string
	http    *http.Client
	// limiter, when non-nil, is an explicit per-caller limiter installed via
	// WithMinInterval. It spaces calls UNCONDITIONALLY (no host gate), so a
	// caller that explicitly asks for throttling gets it everywhere it points,
	// including an httptest host. When nil (the default, zero-option case) doGet
	// falls back to the package sharedLimiter gated by shouldThrottle.
	limiter *throttle.Throttle
	// retryAfterFloor is the minimum 429 back-off when no Retry-After header is
	// present. Defaults to defaultRetryAfterFloor; overridable via
	// WithRetryAfterFloor.
	retryAfterFloor time.Duration
}

// Option customizes a Client at construction (functional-options pattern). New
// is variadic so every existing call site keeps compiling with zero changes.
type Option func(*Client)

// WithMinInterval installs a per-caller rate limiter on this Client, distinct
// from the shared default. Unlike the default shared limiter it is UNGATED —
// it spaces every call regardless of host — which is what lets a test observe
// spacing against a local httptest server. Pass 0 to opt out of throttling
// entirely (a no-op limiter that also bypasses the shared gated one).
func WithMinInterval(d time.Duration) Option {
	return func(c *Client) { c.limiter = throttle.New(d) }
}

// WithRetryAfterFloor overrides the 429 no-header back-off floor (default
// defaultRetryAfterFloor) — used by tests to assert floor behavior with a small
// value instead of a real ~1s sleep.
func WithRetryAfterFloor(d time.Duration) Option {
	return func(c *Client) { c.retryAfterFloor = d }
}

func New(baseURL, apiKey string, httpClient *http.Client, opts ...Option) *Client {
	c := &Client{
		baseURL:         baseURL,
		apiKey:          apiKey,
		http:            httpClient,
		retryAfterFloor: defaultRetryAfterFloor,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// shouldThrottle reports whether the default shared limiter should space a
// request to host — true only for the real TPDB service. Kept a standalone pure
// function so the gate decision is unit-testable in isolation, and compared
// against the tpdbProdHost const (never the mutable DefaultBaseURL var) so a
// test reassigning DefaultBaseURL to an httptest URL can't start throttling
// 127.0.0.1 and break hermeticity.
func shouldThrottle(host string) bool {
	return host == tpdbProdHost
}

// StatusError is returned when TPDB responds with a non-2xx status. Its message
// is text-equivalent to the previous httpx.DoJSON output
// ("<host> returned status <N>") so no caller asserting on that text breaks;
// StatusCode lets a caller/test detect a 429 specifically (the
// capped-retry-exhaustion case).
type StatusError struct {
	Host       string
	StatusCode int
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("%s returned status %d", e.Host, e.StatusCode)
}

// Scene mirrors a subset of ThePornDB's REST scene response shape.
type Scene struct {
	ID    string
	Title string
	Date  string
	Site  string // studio name
	Image string // scene thumbnail/poster URL (may be empty; see rawScene.Image)
	// Duration is the scene's runtime in seconds — see rawScene.Duration for
	// sourcing/confidence. May be 0 (absent/unknown); consumers computing an
	// implied bitrate (Size×8/runtime) MUST treat 0 as "unknown, skip the
	// check," never as a real zero-length runtime or a divide-by-zero input.
	Duration int
	// Rating is the scene's own numeric rating — see rawScene.Rating for
	// sourcing. Modeled as float64 to tolerate either an integer (the spec's
	// example value is 5) or a fractional score without a decode error; may be
	// 0 (absent/unrated). Used by Adult Discover's "Highest Rated" row, which
	// re-sorts one browse page by this field server-side (that ordering is NOT
	// a true global popularity ranking — see BrowseScenes' doc).
	Rating float64
	// Hashes are the scene's pHash values — TPDB's per-scene "hashes" array
	// filtered to type=="phash" (see rawScene.Hashes). Present on every scene
	// response (browse and search), populated for free by every caller through
	// the shared toScene() path. Backs the merged-recent dedup, which drops a
	// stash-box scene whose pHash TPDB already carries. May be empty (a scene
	// with no submitted fingerprints).
	Hashes []string
	// Slug is TPDB's URL-friendly scene identifier — unlike StashDB/FansDB
	// (stash-box software, whose scene detail pages are UUID-path:
	// stashdb.org/scenes/{uuid}), TPDB's own scene pages are slug-path:
	// theporndb.net/scenes/{slug} (e.g. "evilangel-ivy-ireland-dp-dvp-
	// threesome-1" — a real example URL, not a guess). The `_id` field
	// (rawScene.ID above) does NOT work in that path position. Sourcing: the
	// `slug` JSON field itself is modeled from goenvoy's TPDB REST client
	// (pkg.go.dev/github.com/lusoris/goenvoy/metadata/adult/tpdb), the same
	// corroborating source already used for Duration/Rating above (its other
	// field names match this package's rawScene byte-for-byte); the URL
	// PATH SHAPE it builds is directly confirmed by the real example URL.
	// May be empty for an older/edge-case scene; treat that as "no confirmed
	// external link," not a broken guessed URL.
	Slug string
	// Tags is TPDB's per-scene "tags" array (TagResource objects) — confirmed
	// present on SceneResource in TPDB's live OpenAPI schema (fetched from
	// https://api.theporndb.net/openapi.json) this session, 2026-07-15. Only the
	// flat id/uuid/name of each tag is modeled (see Tag); TagResource's recursive
	// `parents` array is deliberately not decoded. May be empty (a scene with no
	// tags on file).
	Tags []Tag
	// Type is TPDB's own discriminator on the shared Scene/Movie resource schema
	// — "Scene" or "Movie" per the live OpenAPI spec's SceneResource.type field,
	// example value "Scene"; not independently confirmed for the Movie case,
	// verify against a live /movies response the first time this is exercised for
	// real.
	Type string
	// Performers is TPDB's per-scene "performers" array reduced to just each
	// performer's display name — confirmed present on SceneResource in TPDB's
	// live OpenAPI schema (fetched from https://api.theporndb.net/openapi.json)
	// this session, 2026-07-15, typed as an array of PerformerResource. Only the
	// name is consumed (id/image/etc. aren't needed for a plain name list). May
	// be empty (a scene with no performers on file, or a solo/POV scene TPDB
	// hasn't tagged).
	Performers []string
	// Description is TPDB's per-scene synopsis — SceneResource.description,
	// confirmed present in TPDB's live OpenAPI schema (fetched from
	// https://api.theporndb.net/openapi.json) 2026-08-02, whose example value
	// is a full multi-sentence synopsis rather than a vestigial stub. The
	// field carries NO `nullable` annotation there (unlike
	// PerformerResource.bio and SiteResource.description, which do).
	//
	// Verification level, stated rather than overclaimed: the SCHEMA is
	// live-confirmed; the POPULATION RATE on real records is NOT verified (no
	// TPDB API key was available in the environment where this was added).
	// Treat empty as entirely normal and degrade gracefully — never render a
	// "missing data" error on its absence.
	Description string
}

// Tag mirrors one entry of TPDB's per-scene "tags" array (a TagResource) — only
// the flat id/uuid/name are modeled; TagResource's recursive `parents` array is
// deliberately not decoded (this client only needs a scene's own flat tag list).
type Tag struct {
	ID   int
	UUID string
	Name string
}

type rawSite struct {
	Name string `json:"name"`
}

// flexID unmarshals a TPDB "_id" field that's normally a quoted string but
// has been observed coming back as a bare JSON number for some scenes —
// encoding/json refuses that straight into a string field, so every _id in
// this package uses flexID instead of string and stringifies either shape.
// Every downstream consumer (internal/identify, internal/library's
// TEXT scene_id column) already treats the id as an opaque string, so this
// stays purely a decode-time tolerance — no type changes ripple outward.
type flexID string

func (f *flexID) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		*f = flexID(s)
		return nil
	}
	var n json.Number
	if err := json.Unmarshal(b, &n); err != nil {
		return fmt.Errorf("_id is neither a string nor a number: %w", err)
	}
	*f = flexID(n.String())
	return nil
}

// rawScene mirrors the fields this client consumes from a TPDB v2 scene object.
// Image is built by toScene() from a preference chain, NOT the flat "image"
// field alone — corrected 2026-07-15 after a live production bug: the flat
// "image"/"poster_image"/"back_image" fields are a raw passthrough of
// whatever URL the STUDIO itself submitted (real production samples:
// images.nubiles-porn.com, c.bellesa.co, ods.manyvids.com — dozens of
// distinct third-party hosts, not TPDB's own CDN), and those URLs are
// frequently broken: some are short-lived signed CDN links (confirmed live,
// HTTP 410 Gone on a URL whose own signature embedded an already-past
// expiry timestamp), others are blocked by the studio's own hotlink/bot
// protection when fetched server-side (confirmed live, HTTP 403 from a
// datacenter IP with no Referer, several User-Agents, and a matching
// Referer all tried). TPDB's OWN site does NOT use the flat "image" field
// for its own scene pages — confirmed by loading a real scene page
// (theporndb.net/scenes/{id}) and inspecting its actual <img> src, which
// resolved to cdn.theporndb.net/scene/.../background/.../large/... — TPDB's
// own re-hosted, reliable copy. toScene() now prefers Background.Large,
// falling back to Poster (also cdn.theporndb.net per the live OpenAPI
// schema's own documented example, thumb.theporndb.net), falling back to
// the raw Image field only as a last resort (better than nothing on the
// rare scene TPDB hasn't re-hosted art for). The previous doc comment here
// claimed "image" was itself TPDB-CDN-hosted; that was a documented-but-
// unconfirmed assumption (its own caveat said so) that turned out to be
// wrong for real production data — this correction replaces it with what
// was actually observed live, not another unverified guess.
//
// Duration ("duration", seconds) — investigated for the frontend-redesign
// plan's auto-grab bitrate scorer, which needs a title's runtime before
// grabbing (implied bitrate = Size×8/runtime). Not directly confirmed against
// a live TPDB instance (same constraint as Image above), but corroborated by
// two independent sources: (1) the stash-box GraphQL schema TPDB's own
// GraphQL endpoint implements (github.com/stashapp/stash-box) declares
// `duration: Int` on its Scene type; (2) github.com/lusoris/goenvoy's TPDB
// REST client (actively maintained, last verified 2026-06-14) models
// `Duration int `json:"duration"“ on Scene/Movie/Jav, with a passing test
// fixture (1800 for a scene) — and that library's other field names
// (_id/title/date/site.name/image) match this client's own rawScene
// byte-for-byte, indicating it targets the same API version. Confidence:
// documented-shape + strong corroboration, NOT live-confirmed.
//
// Rating ("rating") is the scene object's own numeric rating field, confirmed
// present in TPDB's live OpenAPI SceneResource schema (fetched/parsed from
// https://api.theporndb.net/openapi.json), whose example value is the bare
// integer 5. It is decoded into a float64 so either an integer or a fractional
// score round-trips without a type error, and defaults to 0 when absent
// (unrated). This is the field Adult Discover's "Highest Rated" row sorts on.
//
// Hashes is TPDB's per-scene "hashes" array (SceneHashBasicResponse objects,
// each carrying a hash string and a type). Confirmed present on SceneResource
// in TPDB's live OpenAPI schema (fetched/parsed from
// https://api.theporndb.net/openapi.json, same source as Rating above) — both
// the array's existence on the browse/search response shape AND its per-entry
// `hash`/`type` fields are directly confirmed there, not merely modeled from
// third-party documentation. toScene filters it to type=="phash" (the
// confirmed lowercase enum value SearchByHash already sends as hash_type) and
// collects just the pHash strings into Scene.Hashes — the merged-recent
// dedup's TPDB-side hash set.
type rawScene struct {
	ID     flexID   `json:"_id"`
	Title  string   `json:"title"`
	Slug   string   `json:"slug"`
	Date   string   `json:"date"`
	Site   *rawSite `json:"site"`
	Image  string   `json:"image"`
	Poster string   `json:"poster"`
	// Background is TPDB's own re-hosted image set (cdn.theporndb.net),
	// distinct from the studio-passthrough Image/Poster fields above — see
	// this struct's doc comment for the live evidence. Only Large is
	// consumed; Full/Medium/Small exist on the live schema but aren't needed
	// for a Discover-grid-sized thumbnail.
	Background struct {
		Large string `json:"large"`
	} `json:"background"`
	Duration    int                 `json:"duration"`
	Rating      float64             `json:"rating"`
	Hashes      []rawSceneHash      `json:"hashes"`
	Type        string              `json:"type"`
	Tags        []rawTag            `json:"tags"`
	Performers  []rawScenePerformer `json:"performers"`
	Description string              `json:"description"`
}

// rawScenePerformer mirrors one entry of a TPDB scene's "performers" array — a
// PerformerResource, confirmed present on SceneResource in TPDB's live OpenAPI
// schema (https://api.theporndb.net/openapi.json). Only name is consumed; this
// client's own Performer type (id/image) is for the standalone performer
// browse/search endpoints, not for a scene's embedded performer list.
type rawScenePerformer struct {
	Name string `json:"name"`
}

// rawTag mirrors one entry of a TPDB scene's "tags" array — a TagResource,
// confirmed present on SceneResource in TPDB's live OpenAPI schema
// (https://api.theporndb.net/openapi.json). Only id/uuid/name are consumed; the
// recursive `parents` array is deliberately not decoded.
type rawTag struct {
	ID   int    `json:"id"`
	UUID string `json:"uuid"`
	Name string `json:"name"`
}

// rawSceneHash mirrors one entry of a TPDB scene's "hashes" array — only the
// hash string and its type are consumed (type distinguishes phash from oshash).
type rawSceneHash struct {
	Hash string `json:"hash"`
	Type string `json:"type"`
}

func (s rawScene) toScene() Scene {
	site := ""
	if s.Site != nil {
		site = s.Site.Name
	}
	var phashes []string
	for _, h := range s.Hashes {
		if h.Type == "phash" {
			phashes = append(phashes, h.Hash)
		}
	}
	var tags []Tag
	for _, t := range s.Tags {
		tags = append(tags, Tag{ID: t.ID, UUID: t.UUID, Name: t.Name})
	}
	var performers []string
	for _, p := range s.Performers {
		if p.Name != "" {
			performers = append(performers, p.Name)
		}
	}
	// Prefer TPDB's own re-hosted, reliable images over the studio-passthrough
	// fields — see rawScene's doc comment for the live evidence behind this
	// order.
	image := firstNonEmpty(s.Background.Large, s.Poster, s.Image)
	return Scene{ID: string(s.ID), Title: s.Title, Slug: s.Slug, Date: s.Date, Site: site, Image: image, Duration: s.Duration, Rating: s.Rating, Hashes: phashes, Tags: tags, Type: s.Type, Performers: performers, Description: s.Description}
}

// firstNonEmpty returns the first non-empty string from vals, or "" if all are
// empty — the "pick the first present image URL in preference order" helper the
// performer/site image-field selection below shares (TPDB exposes several
// nullable image fields per entity and this client exposes exactly one).
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

type scenesResponse struct {
	Data []rawScene `json:"data"`
	// Meta.CurrentPage is TPDB's echoed page number — the same field
	// performersResponse carries, decoded here on the shared scenes envelope so
	// ScenesByPerformer can reuse BrowsePerformers' out-of-range clamp detection
	// (see ScenesByPerformer). Every other scenes caller decodes it harmlessly
	// and ignores it; only ScenesByPerformer acts on it.
	Meta struct {
		CurrentPage int `json:"current_page"`
	} `json:"meta"`
}

// doGet is the shared GET+decode mechanics every REST lookup (scenes,
// performers, sites) uses — path-scoped so each gets its own typed wrapper
// below rather than every caller reaching into a shared /scenes endpoint.
func (c *Client) doGet(ctx context.Context, path string, params url.Values, out any) error {
	u := c.baseURL + path + "?" + params.Encode()

	// Bound the whole retry loop (requests + back-off sleeps) by the client's
	// own outbound timeout, so a large Retry-After can't stall a scheduler or
	// interactive request far past that budget. Test clients pass Timeout==0
	// (no derived deadline) → pure-ctx behavior is preserved.
	if c.http.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.http.Timeout)
		defer cancel()
	}

	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return fmt.Errorf("building request: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+c.apiKey)

		// Rate-limit before sending. An explicit per-caller limiter spaces every
		// call regardless of host; the default shared limiter spaces only the
		// real TPDB host, exempting httptest hosts (hermetic tests). Each retry
		// re-enters this loop and re-acquires a slot, so retries are rate-limited.
		if c.limiter != nil {
			if err := c.limiter.Wait(ctx, req.URL.Host); err != nil {
				return err
			}
		} else if shouldThrottle(req.URL.Host) {
			if err := sharedLimiter.Wait(ctx, req.URL.Host); err != nil {
				return err
			}
		}

		resp, err := c.http.Do(req)
		if err != nil {
			return httpx.WrapTransportError(req.URL.Host, err)
		}

		if resp.StatusCode == http.StatusTooManyRequests && attempt < maxRetries {
			sleep := c.retryAfterBackoff(resp)
			resp.Body.Close()
			// Don't sleep past the remaining budget — return the 429 now rather
			// than block for a back-off that can't complete in time anyway.
			if dl, ok := ctx.Deadline(); ok && time.Now().Add(sleep).After(dl) {
				return &StatusError{Host: req.URL.Host, StatusCode: http.StatusTooManyRequests}
			}
			if err := sleepCtx(ctx, sleep); err != nil {
				return err
			}
			continue
		}

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			resp.Body.Close()
			return &StatusError{Host: req.URL.Host, StatusCode: resp.StatusCode}
		}

		// httpx has no decode-only helper (DoJSON/DoJSONAllowEmpty each do their
		// own client.Do), so replicate its capped-body decode inline.
		err = json.NewDecoder(io.LimitReader(resp.Body, httpx.MaxResponseBodySize)).Decode(out)
		resp.Body.Close()
		if err != nil {
			return fmt.Errorf("decoding response from %s: %w", req.URL.Host, err)
		}
		return nil
	}
}

// retryAfterBackoff is how long to wait before retrying a 429 —
// max(parsed Retry-After, this Client's retryAfterFloor). A missing or
// unparseable header yields 0 from parseRetryAfter, so the floor governs.
func (c *Client) retryAfterBackoff(resp *http.Response) time.Duration {
	if parsed := parseRetryAfter(resp.Header.Get("Retry-After")); parsed > c.retryAfterFloor {
		return parsed
	}
	return c.retryAfterFloor
}

// parseRetryAfter reads an HTTP Retry-After value in either supported form —
// delay-seconds (an integer) or an HTTP-date (RFC 7231) — returning the delay
// as a duration, or 0 for an absent, past, or unparseable value (never an
// error: an unparseable header just falls back to the floor).
func parseRetryAfter(v string) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil {
		if secs <= 0 {
			return 0
		}
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}

// sleepCtx blocks for d, honoring ctx cancellation (a cancelled/expired ctx
// returns its error instead of sleeping out the full duration).
func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *Client) get(ctx context.Context, params url.Values) ([]Scene, error) {
	return c.getScenes(ctx, "/scenes", params)
}

// getScenes is the shared scenes-decode path for any endpoint that returns
// TPDB's {"data":[...scenes...]} envelope — the top-level /scenes browse/search
// AND the dedicated per-entity drill-downs (/sites/{id}/scenes,
// /performers/{id}/scenes), all of which the live OpenAPI spec documents as
// returning that same SceneResource array shape. Kept path-scoped so each
// caller passes its own already-escaped path rather than reaching into a shared
// /scenes endpoint.
func (c *Client) getScenes(ctx context.Context, path string, params url.Values) ([]Scene, error) {
	out, _, err := c.getScenesPaged(ctx, path, params)
	return out, err
}

// getScenesPaged is getScenes plus TPDB's echoed meta.current_page (0 when
// absent) — the scenes analogue of getPerformers' extra return value. Only
// ScenesByPerformer needs the page (for its out-of-range clamp detection);
// every other scenes caller goes through getScenes and discards it.
func (c *Client) getScenesPaged(ctx context.Context, path string, params url.Values) ([]Scene, int, error) {
	var sr scenesResponse
	if err := c.doGet(ctx, path, params, &sr); err != nil {
		return nil, 0, err
	}
	out := make([]Scene, len(sr.Data))
	for i, rs := range sr.Data {
		out[i] = rs.toScene()
	}
	return out, sr.Meta.CurrentPage, nil
}

// Ping confirms the base URL/key work by making one real, minimal request
// against the same /scenes endpoint SearchByHash and SearchByTitle use —
// ThePornDB's REST API has no separate lightweight "verify key" endpoint, so
// a trivially-scoped real call (per_page=1, no search term) is the honest
// check: it 401s on a bad key exactly like a real search would, without
// asserting anything about the result content.
func (c *Client) Ping(ctx context.Context) error {
	_, err := c.get(ctx, url.Values{"per_page": {"1"}})
	return err
}

// defaultBrowsePerPage is BrowseScenes' page size when the caller passes a
// non-positive per-page count — a sane default for a Discover grid.
const defaultBrowsePerPage = 20

// BrowseScenes returns one page of ThePornDB's scene catalog with NO search
// term — the plain paginated browse backing Adult's Discover screen, reusing
// the exact bare-pagination call shape Ping already proved works (per_page/page,
// no q). page and perPage are clamped to sane minimums (page >= 1; perPage
// defaults to defaultBrowsePerPage when non-positive) so a bad client value can
// never produce a malformed query.
//
// orderBy selects GET /scenes' server-side sort. Pass "" for the historical
// plain-browse behavior (no ordering param sent at all). Pass a value from
// TPDB's SearchOrderEnum to sort server-side — Adult Discover's "Recently
// Released" row passes "recently_released". The query param is named exactly
// "orderBy" (confirmed casing from the live OpenAPI spec at
// https://api.theporndb.net/openapi.json — NOT order_by or sort).
//
// IMPORTANT — there is deliberately no "top rated"/"trending" orderBy here,
// because the live spec's SearchOrderEnum has no popularity/rating sort (only
// duration_*, former_*, most_relevant, recently_created/released/updated).
// Discover's "Highest Rated" row is therefore implemented by the caller as a
// plain BrowseScenes(orderBy: "") followed by a server-side re-sort of that ONE
// page by each scene's own Scene.Rating — a client-visible-but-page-local
// ordering, NOT a true global popularity ranking. Be honest about that limit;
// don't dress a same-page re-sort up as a real "top rated" feed.
func (c *Client) BrowseScenes(ctx context.Context, page, perPage int, orderBy string) ([]Scene, error) {
	if perPage <= 0 {
		perPage = defaultBrowsePerPage
	}
	if page <= 0 {
		page = 1
	}
	params := url.Values{
		"per_page": {strconv.Itoa(perPage)},
		"page":     {strconv.Itoa(page)},
	}
	if orderBy != "" {
		params.Set("orderBy", orderBy)
	}
	return c.get(ctx, params)
}

// SearchByHash looks up scenes by perceptual hash (TPDB's GraphQL fingerprint
// lookup is tried first by callers; this REST fallback covers what it misses).
func (c *Client) SearchByHash(ctx context.Context, phash string) ([]Scene, error) {
	params := url.Values{"hash": {phash}, "hash_type": {"phash"}}
	return c.get(ctx, params)
}

// SearchByTitle text-searches by title, optionally narrowed by site (studio).
// Similarity filtering of results is business logic that belongs in
// internal/identify, not here.
func (c *Client) SearchByTitle(ctx context.Context, title, site string) ([]Scene, error) {
	params := url.Values{"q": {title}, "per_page": {"5"}}
	if site != "" {
		params.Set("site", site)
	}
	return c.get(ctx, params)
}

// sceneResponse is the single-object envelope GET /scenes/{identifier}
// returns — confirmed in the live OpenAPI spec, a different shape from the
// array-wrapped scenesResponse every browse/search endpoint returns.
type sceneResponse struct {
	Data rawScene `json:"data"`
}

// GetSceneByID fetches one scene directly by its TPDB id — an exact,
// no-fuzzy-matching lookup, unlike SearchByTitle/BrowseScenes. Used by
// identify.BoxSearcher.resolveTPDBDuration as a confirming re-fetch when a
// search result's duration comes back suspiciously 0 (found live
// 2026-07-15 — see that function's doc comment).
func (c *Client) GetSceneByID(ctx context.Context, id string) (*Scene, error) {
	var sr sceneResponse
	if err := c.doGet(ctx, "/scenes/"+url.PathEscape(id), url.Values{}, &sr); err != nil {
		return nil, err
	}
	scene := sr.Data.toScene()
	return &scene, nil
}

// Performer mirrors a subset of ThePornDB's REST performer response shape.
// Image is the single chosen image URL — see rawPerformer for how it's picked
// from TPDB's several nullable image fields; may be empty (no art on file), so
// consumers must degrade gracefully.
type Performer struct {
	ID     string
	Name   string
	Image  string
	Gender string
	// Bio is TPDB's per-performer biography — PerformerResource.bio, confirmed
	// present in TPDB's live OpenAPI schema (fetched from
	// https://api.theporndb.net/openapi.json) 2026-08-02, where it IS marked
	// nullable. Neither StashDB nor FansDB has any bio-like field at all
	// (proven by complete field enumeration of their live GraphQL Performer
	// type, same session), so TPDB is the only source for this.
	//
	// Verification level, stated rather than overclaimed: the SCHEMA is
	// live-confirmed; the POPULATION RATE on real records is NOT verified (no
	// TPDB API key was available in the environment where this was added).
	// Treat empty as entirely normal.
	Bio string
}

// rawPerformer mirrors the fields this client consumes from a TPDB
// PerformerResource. Per the live OpenAPI schema
// (https://api.theporndb.net/openapi.json) a performer carries three nullable
// flat image fields — "image", "thumbnail", "face" — plus a separate
// "posters" array of MediaResource (TPDB's own re-hosted cdn.theporndb.net
// images, id/url/size/order), the performer analogue of a scene's
// Background.Large. Confirmed by a live sample against this deployment's own
// data (2026-07-26): the flat fields are empty for a large share of real
// performers even though TPDB has art for them — that art lives in the
// unread "posters" array, causing missing-poster cards on Adult Discover's
// Performers row. toPerformer prefers the first posters[] entry, falling
// back to the flat fields only when posters is empty (same shape as
// toScene's Background.Large-first preference below).
type rawPerformer struct {
	ID        flexID             `json:"_id"`
	Name      string             `json:"name"`
	Image     string             `json:"image"`
	Thumbnail string             `json:"thumbnail"`
	Face      string             `json:"face"`
	Posters   []rawPosterEntry   `json:"posters"`
	Extras    rawPerformerExtras `json:"extras"`
	// Bio is a TOP-LEVEL PerformerResource field, a sibling of "extras" —
	// not a member of it, unlike gender below. Confirmed against the live
	// OpenAPI schema's PerformerResource property list 2026-08-02.
	Bio string `json:"bio"`
}

// rawPosterEntry mirrors one entry of a TPDB MediaResource — only url is
// consumed; id/size/order aren't needed for picking the first poster.
type rawPosterEntry struct {
	URL string `json:"url"`
}

// rawPerformerExtras mirrors the only field this client consumes from a TPDB
// performer's "extras" object. gender is a free-form lowercase string
// ("female", "male", "transgender_female", ...) — value space is
// documentation-modeled, normalized defensively downstream (adultmerge.normalizeGender).
type rawPerformerExtras struct {
	Gender string `json:"gender"`
}

func (rp rawPerformer) toPerformer() Performer {
	posterURL := ""
	if len(rp.Posters) > 0 {
		posterURL = rp.Posters[0].URL
	}
	return Performer{ID: string(rp.ID), Name: rp.Name, Image: firstNonEmpty(posterURL, rp.Image, rp.Thumbnail, rp.Face), Gender: rp.Extras.Gender, Bio: rp.Bio}
}

type performersResponse struct {
	Data []rawPerformer `json:"data"`
	// Meta.CurrentPage is TPDB's echoed page number. It is load-bearing for
	// BrowsePerformers' clamp detection: TPDB answers an out-of-range
	// /performers page with HTTP 200 + the last real page's items repeated,
	// echoing meta.current_page as that clamped (last real) page rather than
	// the requested one — the signal used to synthesize the empty-200 the
	// pagination contract requires (see BrowsePerformers).
	Meta struct {
		CurrentPage int `json:"current_page"`
	} `json:"meta"`
}

// performerResponse is TPDB's single-entity envelope — GetPerformerByID's
// response shape, distinct from performersResponse's array envelope above.
type performerResponse struct {
	Data rawPerformer `json:"data"`
}

// GetPerformerByID fetches one performer via TPDB's dedicated
// GET /performers/{identifier} endpoint (confirmed in the live OpenAPI path
// list). Used by backfillMissingImages below to recover an image the list
// endpoint omitted — see that function's doc comment for why the list
// endpoint alone isn't always sufficient.
func (c *Client) GetPerformerByID(ctx context.Context, id string) (Performer, error) {
	var pr performerResponse
	if err := c.doGet(ctx, "/performers/"+url.PathEscape(id), url.Values{}, &pr); err != nil {
		return Performer{}, err
	}
	return pr.Data.toPerformer(), nil
}

// maxImageBackfillConcurrency bounds how many detail-fetch calls
// backfillMissingImages fires at once, so a browse/search page full of
// list-endpoint image gaps never bursts an unbounded number of concurrent
// upstream requests.
const maxImageBackfillConcurrency = 5

// backfillMissingImages fills in Image for any performer whose list-endpoint
// entry came back empty, by calling GetPerformerByID for just that performer.
// Live-verified against this deployment's real data (2026-07-26) in three
// steps: (1) toPerformer() alone, preferring "posters", left the same
// performers empty as before — the list endpoint's own posters/image/
// thumbnail/face are all empty for a real share of performers; (2) this
// backfill's working hypothesis was that the per-entity GET /performers/{id}
// detail endpoint hydrates what the list endpoint doesn't (a known
// Laravel-API-Resource pattern); (3) a post-deploy check found the same
// performers still empty even through the detail endpoint. Conclusion:
// genuine upstream no-art-on-file for those specific entries (several of
// which are tag-style junk names, not real performers) — not a remaining
// code bug. This backfill is still worth keeping because it fixes every
// performer where TPDB *does* have posters-only or detail-only data, which a
// live sample showed is a real, separate case from the no-art-anywhere one.
//
// Deliberately NOT called from getPerformers/SearchPerformers: SearchPerformers
// is used by internal/identify's entity-verification pipeline
// (entityverify.go), which only reads performer Name (never Image) and already
// rate-limits its TPDB calls via internal/throttle before calling
// SearchPerformers — routing an unthrottled detail-fetch burst through that
// path would defeat the exact protection internal/throttle exists for, for a
// caller that doesn't even use the data. Only BrowsePerformers (Adult
// Discover's Performers row, where the image is actually displayed) calls
// this.
//
// Best-effort and bounded: a failed or slow detail fetch for one performer
// never fails the whole browse (its Image just stays empty, same
// degrade-gracefully contract Performer.Image already documents), and only
// entries with an empty Image are fetched at all — a fully-populated page
// costs zero extra requests. Worst case (every entry needs backfill) is
// bounded by maxImageBackfillConcurrency and this client's outboundTimeout
// (cmd/sakms/main.go): ceil(perPage/maxImageBackfillConcurrency) sequential
// waves — at this package's defaultBrowsePerPage (20) and
// maxImageBackfillConcurrency (5), up to ~4 timeout-length waves added to one
// page load if TPDB is unresponsive for every gap. An accepted
// latency/completeness tradeoff for a browse row, not a hard bound on
// wall-clock time.
func (c *Client) backfillMissingImages(ctx context.Context, performers []Performer) {
	sem := make(chan struct{}, maxImageBackfillConcurrency)
	var wg sync.WaitGroup
	for i := range performers {
		if performers[i].Image != "" || performers[i].ID == "" {
			continue
		}
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			detail, err := c.GetPerformerByID(ctx, performers[i].ID)
			if err != nil {
				return
			}
			performers[i].Image = detail.Image
		}(i)
	}
	wg.Wait()
}

// getPerformers returns the decoded performer page plus TPDB's echoed
// meta.current_page (0 when absent). The current-page value is what
// BrowsePerformers uses to detect TPDB's out-of-range clamp; callers that
// don't paginate (SearchPerformers) discard it.
func (c *Client) getPerformers(ctx context.Context, params url.Values) ([]Performer, int, error) {
	var pr performersResponse
	if err := c.doGet(ctx, "/performers", params, &pr); err != nil {
		return nil, 0, err
	}
	out := make([]Performer, len(pr.Data))
	for i, rp := range pr.Data {
		out[i] = rp.toPerformer()
	}
	return out, pr.Meta.CurrentPage, nil
}

// SearchPerformers text-searches performers by name. Similarity filtering of
// results is business logic that belongs in internal/identify, not here —
// same convention as SearchByTitle. Deliberately does NOT run
// backfillMissingImages — see that function's doc comment for why (its one
// caller, internal/identify's entity-verification pipeline, never reads
// Image and already throttles its own TPDB calls).
func (c *Client) SearchPerformers(ctx context.Context, term string) ([]Performer, error) {
	out, _, err := c.getPerformers(ctx, url.Values{"q": {term}})
	return out, err
}

// BrowsePerformers returns one page of TPDB's performer catalog with NO search
// term — the plain paginated browse backing Adult Discover's Performers row.
// The live OpenAPI spec confirms GET /performers requires no "q" (it's absent
// from that endpoint's required params); page/per_page alone are a valid browse,
// exactly like BrowseScenes. page/perPage are clamped the same way (page >= 1;
// perPage defaults to defaultBrowsePerPage when non-positive). The spec's
// optional "letter" first-initial filter is deliberately not used here.
//
// Sort (live-verified 2026-07-26, this deployment's creds): the request sends
// orderBy=most_relevant, NOT a name sort. Originally shipped as orderBy=
// asc_name (name-aligned with StashDB for the merged row's page-local dedup),
// but live sampling after launch found TPDB's alphabetically-first entries
// (roughly pages 1-5 of 500) are dominated by non-performer junk — sex-toy
// product names, compilation-video titles, auto-generated hex-string ids —
// while pages 50+ are entirely real performer names. orderBy=most_relevant
// (confirmed via TPDB's live OpenAPI PerformerOrderEnum, then live-sampled:
// two full pages, zero junk, all real names) fixes that. The value is
// LOWERCASE most_relevant, NOT the spec's uppercase "MOST_RELEVANT" — same
// lowercase-snake-case-vs-uppercase-spec mismatch already confirmed for
// asc_name/desc_name and BrowseScenes' orderBy (e.g. "recently_created").
//
// KNOWN TRADEOFF, accepted by the operator (2026-07-26): most_relevant is an
// independent ranking TPDB computes internally; StashDB's paired
// queryPerformers now sorts by its own POPULARITY, a SEPARATE ranking system
// with no structural guarantee the two align page-for-page the way a name
// sort does by definition (same starting letter → same page on both sides).
// Page-local dedup may be less effective than under the original name-sort
// design — some real cross-source duplicates may show up as two unmerged
// cards more often than before. Not independently re-measured after this
// switch; if dedup quality visibly regresses, that's the tradeoff to
// revisit, not a bug in this comment's absent claim of alignment.
//
// Out-of-range clamp handling (live-verified 2026-07-26 under orderBy=
// asc_name; NOT independently re-verified under most_relevant, though the
// clamp is pagination-metadata behavior rather than sort-content-dependent,
// so it's expected — not confirmed — to hold under any orderBy value): TPDB
// does NOT honor the standard "empty-200 past the final page" contract for
// /performers. Instead it answers a page beyond the last one with HTTP 200 +
// the final page's items REPEATED, while echoing meta.current_page as the
// last real page (e.g. request page 501 of a 500-page catalog → 200, the
// same 20 items as page 500, meta.current_page == 500). Left as-is that
// would make the merged Performers row non-terminating (a full stale page
// keeps merged size >= perPage forever, so the frontend's `< perPage`
// exhaustion check never fires). So this method DETECTS the clamp —
// currentPage > 0 && currentPage < requested page — and returns an empty
// slice, synthesizing the empty-200 the pagination contract requires so the
// row exhausts correctly.
//
// This detection depends on TPDB's OBSERVED clamp-echo behavior: that it
// reports the last real page in meta.current_page instead of the requested
// one. If TPDB ever changed to echo the REQUESTED page while still serving
// stale repeated data, detection would silently stop firing. That failure
// mode is soft — a Performers row that scrolls forever past the catalog's end,
// NOT data corruption or a crash — and would surface via the deep-row
// exhaustion manual/e2e check, not silently. The comparison is deliberately
// page-value-based (never a hardcoded 500 or catalog size — those are today's
// data, not a contract) and so is direction/perPage-agnostic.
//
// backfillMissingImages (see its doc comment) runs only on a real,
// non-clamped page — Discover's Performers row is the one place a performer's
// Image is actually rendered — and is skipped entirely on a clamped page so
// the bounded detail-fetch burst never fires on discarded stale data.
func (c *Client) BrowsePerformers(ctx context.Context, page, perPage int) ([]Performer, error) {
	if perPage <= 0 {
		perPage = defaultBrowsePerPage
	}
	if page <= 0 {
		page = 1
	}
	out, currentPage, err := c.getPerformers(ctx, url.Values{
		"per_page": {strconv.Itoa(perPage)},
		"page":     {strconv.Itoa(page)},
		"orderBy":  {"most_relevant"},
	})
	if err != nil {
		return nil, err
	}
	// currentPage > 0 guards against a missing/zero meta (treated as
	// not-clamped — conservative, never a false drain).
	if currentPage > 0 && currentPage < page {
		log.Printf("tpdbrest: BrowsePerformers page %d clamped by TPDB to %d; returning empty (treating source as exhausted past its final page)", page, currentPage)
		return nil, nil
	}
	c.backfillMissingImages(ctx, out)
	return out, nil
}

// ScenesByPerformer returns one page of a single performer's scenes via TPDB's
// dedicated GET /performers/{identifier}/scenes endpoint (confirmed in the live
// OpenAPI path list; it accepts only identifier (path) + page + per_page (query)
// — no other filter params). performerID is the opaque id string this client
// already returns from Performer.ID (the flexID-decoded _id); it's URL-path
// escaped and used directly, never parsed as an int. page/perPage are clamped
// like the browse methods. This is preferred over filtering /scenes by a
// performer_id query param — the dedicated endpoint is what the spec provides
// for exactly this drill-down.
//
// Out-of-range clamp handling — applied BY ANALOGY, NOT independently
// live-verified for this endpoint. BrowsePerformers documents (and its clamp
// test guards) a live-verified TPDB behavior: GET /performers answers a page
// past the last one with HTTP 200 + the final page's items REPEATED, echoing
// meta.current_page as the last real page rather than the requested one, which
// left un-mitigated makes a paginated row non-terminating. This method is the
// same TPDB REST API family with the same per-entity-paginated shape
// (SceneResource array + meta.current_page), and the merged drill-down that
// calls it (internal/api/adultdiscover_merge.go's mergedScenes) routinely pages
// TPDB past its own end once the StashDB leg is deeper — the exact condition
// that would trip the clamp bug. The same guard is therefore applied here
// pre-emptively: currentPage > 0 && currentPage < requested page → return an
// empty slice (the synthetic empty-200 the pagination contract needs). This is
// an inference from the confirmed-buggy sibling /performers endpoint, NOT a fact
// established against /performers/{id}/scenes directly. If a future session gets
// the chance to live-verify this specific endpoint's out-of-range behavior,
// update this comment to state the confirmed fact (clamps, or is clean) instead
// of this by-analogy inference.
func (c *Client) ScenesByPerformer(ctx context.Context, performerID string, page, perPage int) ([]Scene, error) {
	if perPage <= 0 {
		perPage = defaultBrowsePerPage
	}
	if page <= 0 {
		page = 1
	}
	params := url.Values{
		"per_page": {strconv.Itoa(perPage)},
		"page":     {strconv.Itoa(page)},
	}
	out, currentPage, err := c.getScenesPaged(ctx, "/performers/"+url.PathEscape(performerID)+"/scenes", params)
	if err != nil {
		return nil, err
	}
	// currentPage > 0 guards against a missing/zero meta (treated as
	// not-clamped — conservative, never a false drain). Mirrors
	// BrowsePerformers' clamp detection exactly.
	if currentPage > 0 && currentPage < page {
		log.Printf("tpdbrest: ScenesByPerformer performer %s page %d clamped by TPDB to %d; returning empty (treating source as exhausted past its final page)", performerID, page, currentPage)
		return nil, nil
	}
	return out, nil
}

// Site mirrors a subset of ThePornDB's REST site (studio) response shape.
// Image is the single chosen image URL — see rawSiteEntry for how it's picked
// from TPDB's several nullable image fields; may be empty, so consumers must
// degrade gracefully.
type Site struct {
	ID    string
	Name  string
	Image string
	// Description is TPDB's per-site (studio) blurb —
	// SiteResource.description, confirmed present in TPDB's live OpenAPI
	// schema (fetched from https://api.theporndb.net/openapi.json) 2026-08-02,
	// where it IS marked nullable. Neither StashDB nor FansDB has any
	// description-like field on Studio at all (proven by complete field
	// enumeration of their live GraphQL Studio type, same session), so TPDB is
	// the only source for this.
	//
	// Verification level, stated rather than overclaimed: the SCHEMA is
	// live-confirmed; the POPULATION RATE on real records is NOT verified (no
	// TPDB API key was available in the environment where this was added).
	// Treat empty as entirely normal.
	//
	// WHICH ENDPOINTS actually populate it is likewise NOT verified:
	// /sites/{identifier} returns the full SiteResource per the spec, but
	// whether the /sites LIST response serializes description was not checked
	// live — contrast the Network field below, whose comment says "present on
	// /sites LIST responses (confirmed live...)" precisely because this client
	// has already been bitten by list-vs-detail divergence (see
	// backfillMissingImages, which exists because the PERFORMER list endpoint
	// omits data its detail endpoint has). toSite() populates Description from
	// whichever response happens to carry it; GetSiteByID is the detail path.
	Description string
}

// rawSiteEntry mirrors the fields this client consumes from a TPDB
// SiteResource. Per the live OpenAPI schema
// (https://api.theporndb.net/openapi.json) a site carries three nullable image
// fields — "logo", "favicon", "poster" — and toSite collapses them into the
// single Image field above by first-non-empty preference: logo, then poster,
// then favicon (favicon last as it's the least presentable at grid size).
//
// ID decodes "uuid", NOT "_id" — live-verified 2026-07-26 bug fix:
// SiteResource has NO "_id" field at all (confirmed against the live spec's
// SiteResource schema: only "uuid" (string) and "id" (a plain number) exist;
// "_id" is a PerformerResource-only field). rawSiteEntry previously decoded
// "_id" here, matching rawPerformer's convention, which silently left every
// Site.ID empty (the JSON key never matched) since before this session —
// this predates and is unrelated to today's TPDB+StashDB merge feature; it
// surfaced today because the new merged Studios row was the first caller to
// actually depend on Site.ID end-to-end for drill-down routing. "uuid" is
// confirmed live-valid for the /sites/{identifier} and
// /sites/{identifier}/scenes path parameter ("Identifier for this entity,
// can be slug, id or uuid").
type rawSiteEntry struct {
	ID      string `json:"uuid"`
	Name    string `json:"name"`
	Logo    string `json:"logo"`
	Favicon string `json:"favicon"`
	Poster  string `json:"poster"`
	// Network is present on /sites LIST responses (confirmed live 2026-07-26,
	// not just the /sites/{id} detail endpoint) — see junkNetworkIDs below,
	// the only consumer of this field.
	Network *struct {
		ID int `json:"id"`
	} `json:"network"`
	Description string `json:"description"`
}

func (rs rawSiteEntry) toSite() Site {
	return Site{ID: rs.ID, Name: rs.Name, Image: firstNonEmpty(rs.Logo, rs.Poster, rs.Favicon), Description: rs.Description}
}

// junkNetworkIDs are TPDB "network" ids for clip-selling platforms whose
// entire catalog of TPDB "sites" is one auto-generated storefront per
// individual creator (e.g. "Manyvids: Aryana", "I Want Clips: Athenabastet")
// rather than a real production studio — confirmed live 2026-07-26: sampling
// 100 pages spread across TPDB's full /sites catalog found ~63% of entries
// belong to one of these seven networks. Each entry's raw JSON carries a
// nested "network" object with a stable numeric id; filtering on that id is
// far more robust than matching the entry's own display-name text (which was
// tried and rejected — a name-prefix heuristic risks a real studio whose name
// happens to collide, and doesn't survive typo/capitalization drift the way a
// structural id does). NOT exhaustive: smaller clip-storefront platforms
// below the sampling threshold likely exist and aren't caught here — this
// list only covers the seven confirmed by live sampling, not a documented
// TPDB API enum (no such enum exists).
var junkNetworkIDs = map[int]bool{
	5502:   true, // ManyVids
	9347:   true, // I Want Clips
	47302:  true, // APClips
	5940:   true, // Clips4Sale
	39646:  true, // TPDB's own "FansDB" network — unrelated to this app's separate FansDB stash-box connection
	71743:  true, // HobbyPorn
	111095: true, // Yourvids
}

func isJunkNetwork(rs rawSiteEntry) bool {
	return rs.Network != nil && junkNetworkIDs[rs.Network.ID]
}

type sitesResponse struct {
	Data []rawSiteEntry `json:"data"`
}

// siteResponse is TPDB's single-entity envelope — GetSiteByID's response
// shape, distinct from sitesResponse's array envelope above and identical in
// shape to performerResponse/sceneResponse.
type siteResponse struct {
	Data rawSiteEntry `json:"data"`
}

// GetSiteByID fetches one site (TPDB's name for a studio) via its dedicated
// GET /sites/{identifier} endpoint. The single-entity envelope ({data:
// SiteResource}) was confirmed against the live OpenAPI spec 2026-08-02 — the
// same shape GET /performers/{identifier} returns, so this mirrors
// GetPerformerByID rather than inventing a new envelope pattern. The
// identifier may be a slug, id or uuid per that spec; Site.ID (which decodes
// "uuid", see rawSiteEntry) is a valid value for it.
//
// Deliberately NOT junk-network-filtered and deliberately NOT wired into
// getSites/BrowseSites as an image-backfill fan-out: an explicit by-id lookup
// is explicit operator intent (the same precedent SearchSites documents), and
// extending backfillMissingImages' concurrency pattern to the site endpoints
// would defeat internal/throttle exactly as that function's own doc warns.
func (c *Client) GetSiteByID(ctx context.Context, id string) (Site, error) {
	var sr siteResponse
	if err := c.doGet(ctx, "/sites/"+url.PathEscape(id), url.Values{}, &sr); err != nil {
		return Site{}, err
	}
	return sr.Data.toSite(), nil
}

func (c *Client) getSitesRaw(ctx context.Context, params url.Values) ([]rawSiteEntry, error) {
	var sr sitesResponse
	if err := c.doGet(ctx, "/sites", params, &sr); err != nil {
		return nil, err
	}
	return sr.Data, nil
}

func (c *Client) getSites(ctx context.Context, params url.Values) ([]Site, error) {
	raw, err := c.getSitesRaw(ctx, params)
	if err != nil {
		return nil, err
	}
	out := make([]Site, len(raw))
	for i, rs := range raw {
		out[i] = rs.toSite()
	}
	return out, nil
}

// SearchSites text-searches sites (studios) by name. Deliberately NOT
// junk-network-filtered, same scope precedent as BrowsePerformers only
// filtering Browse, not Search — a search is explicit user intent, including
// (if the operator really wants it) a specific clip-platform storefront.
func (c *Client) SearchSites(ctx context.Context, term string) ([]Site, error) {
	return c.getSites(ctx, url.Values{"q": {term}})
}

// maxSitePagesPerRequest bounds how many raw TPDB /sites pages a single
// BrowseSites call will walk (see that method's doc for why the walk
// exists). Sized generously so ordinary shallow browsing never hits it —
// junk clusters alphabetically (all "Manyvids: " entries fall together under
// M, etc.), so the cost of the walk grows with how deep into the catalog a
// row has scrolled, not with perPage alone. Hitting the cap means BrowseSites
// returns fewer than perPage items on a page that isn't the true catalog end
// — an accepted, logged degradation for a Discover browse row, not a
// correctness bug (see BrowseSites' doc).
const maxSitePagesPerRequest = 15

// BrowseSites returns one page of TPDB's site (studio) catalog with NO search
// term — the plain paginated browse backing Adult Discover's Studios row. The
// live OpenAPI spec confirms GET /sites requires no "q" (absent from its
// required params); page/per_page alone are a valid browse. page/perPage are
// clamped like the other browse methods (page >= 1; perPage defaults to
// defaultBrowsePerPage). The spec's optional "letter" filter is not used here.
//
// Name sort (live-verified 2026-07-26): sends orderBy=asc_name. As with
// /performers, the working value is LOWERCASE asc_name; the uppercase
// "ASC_NAME" 422s live. Extra wrinkle for /sites: the OpenAPI operation does
// not even list an orderBy param (only q/letter/page/per_page), yet the live
// API accepts orderBy=asc_name and sorts by it — a spec-vs-implementation
// mismatch confirmed live, stated as fact. Unlike BrowsePerformers, /sites'
// own orderBy=most_relevant was live-verified (2026-07-26) to return results
// BYTE-IDENTICAL to asc_name — TPDB's site relevance ranking doesn't
// functionally differ from name order for this catalog, so it's not a usable
// fix signal here and asc_name stays.
//
// Junk-network filtering (added 2026-07-26, see junkNetworkIDs): entries
// belonging to a known clip-platform storefront network are excluded. Because
// some raw TPDB pages are close to or exactly 100% junk by this definition,
// returning whatever's left after filtering a single fetched page could
// return fewer than perPage items (even zero) on a page that ISN'T the true
// end of the catalog — which would make the merged Studios row's "fewer than
// a full page = no more results" exhaustion check (see PaginatedStrip,
// frontend/src/screens/discover/shared.tsx) terminate early, well before the
// real catalog is exhausted. So this method walks: it re-fetches raw TPDB
// /sites pages starting from page 1 on every call (no cross-call state —
// simpler and safer than a resume cursor; a per-row server-side page cache
// was considered and rejected, since two browser tabs or a page reload could
// interleave requests out of sequence and silently skip or duplicate
// studios), filtering junk-network entries as it goes, until it has
// accumulated page*perPage legitimate items, or hits maxSitePagesPerRequest,
// or reaches TPDB's own genuine end of catalog.
//
// End-of-catalog detection (live-verified 2026-07-26): unlike /performers,
// TPDB /sites DOES honor the standard contract — a page past the final one
// returns a clean empty-200 (not the clamp-and-repeat that /performers
// exhibits). The walk's only true-exhaustion signal is a raw page returning
// zero items (len(sr.Data)==0) — NOT "zero items survived filtering," which
// can happen on an all-junk-but-nonempty page and must NOT stop the walk.
// Conflating those two would silently reintroduce the exact bug this method
// fixes, just one layer down.
//
// The bool return is hasMore: true if this walk collected a full requested
// page (page*perPage items), meaning more likely exists upstream; false if
// it stopped at TPDB's genuine end-of-catalog or the maxSitePagesPerRequest
// cap. It's computed HERE, on accum BEFORE any caller-side filtering (e.g.
// internal/api/adultdiscover_merge.go's filterZeroSceneSites) — that's the
// whole point: a caller-side filter can legitimately shrink the returned
// slice below perPage on a page that ISN'T the catalog's real end, and a
// caller inferring "no more results" from post-filter length alone would
// reintroduce this exact class of premature-termination bug one layer up.
// Callers that don't need this signal (e.g. the unmerged, currently-dead
// adultStudiosHandler) can discard it.
func (c *Client) BrowseSites(ctx context.Context, page, perPage int) ([]Site, bool, error) {
	if perPage <= 0 {
		perPage = defaultBrowsePerPage
	}
	if page <= 0 {
		page = 1
	}
	needed := page * perPage

	var accum []Site
	for rawPage := 1; len(accum) < needed && rawPage <= maxSitePagesPerRequest; rawPage++ {
		raw, err := c.getSitesRaw(ctx, url.Values{
			"per_page": {strconv.Itoa(perPage)},
			"page":     {strconv.Itoa(rawPage)},
			"orderBy":  {"asc_name"},
		})
		if err != nil {
			return nil, false, err
		}
		if len(raw) == 0 {
			break // true end of TPDB's /sites catalog
		}
		for _, rs := range raw {
			if isJunkNetwork(rs) {
				continue
			}
			accum = append(accum, rs.toSite())
		}
	}
	hasMore := len(accum) >= needed

	start := (page - 1) * perPage
	if start >= len(accum) {
		return []Site{}, false, nil
	}
	return accum[start:min(page*perPage, len(accum))], hasMore, nil
}

// ScenesBySite returns one page of a single site's scenes via TPDB's dedicated
// GET /sites/{identifier}/scenes endpoint (confirmed in the live OpenAPI path
// list; it accepts only identifier (path) + page + per_page (query) — no other
// filter params). siteID is the opaque id string this client already returns
// from Site.ID (the flexID-decoded _id); it's URL-path escaped and used
// directly, never parsed as an int. Preferred over filtering /scenes by a
// site_id query param — the dedicated endpoint is what the spec provides for
// exactly this drill-down.
//
// Deliberately NO out-of-range clamp guard — left unguarded BY ANALOGY to
// BrowseSites, whose doc records that GET /sites was live-verified to honor the
// standard empty-200-past-the-end contract (unlike /performers, which clamps and
// repeats). Since /sites is clean, its scenes sub-resource is assumed clean too.
// That is an inference from the parent /sites endpoint's confirmed-clean
// behavior, NOT a fact established against /sites/{id}/scenes directly — the same
// honesty caveat ScenesByPerformer carries in the opposite direction. If a future
// session live-verifies this endpoint (or observes it clamping meta.current_page
// like /performers does), update this comment with the confirmed fact and, if it
// clamps, mirror ScenesByPerformer's guard here.
func (c *Client) ScenesBySite(ctx context.Context, siteID string, page, perPage int) ([]Scene, error) {
	if perPage <= 0 {
		perPage = defaultBrowsePerPage
	}
	if page <= 0 {
		page = 1
	}
	params := url.Values{
		"per_page": {strconv.Itoa(perPage)},
		"page":     {strconv.Itoa(page)},
	}
	return c.getScenes(ctx, "/sites/"+url.PathEscape(siteID)+"/scenes", params)
}

// Movies share TPDB's Scene resource shape (confirmed via the live OpenAPI
// spec, https://api.theporndb.net/openapi.json) — these methods return []Scene
// with Type=="Movie", not a separate Movie type. Check .Type to distinguish
// when mixing scene and movie results. They reuse the same getScenes decode
// machinery the /scenes endpoints do, since the response envelope
// ({"data":[...SceneResource...]}) is identical.

// SearchMovies text-searches movies by title — the GET /movies analogue of
// SearchByTitle, same q + per_page=5 shape. Similarity filtering of results is
// business logic that belongs in internal/identify, not here.
func (c *Client) SearchMovies(ctx context.Context, title string) ([]Scene, error) {
	return c.getScenes(ctx, "/movies", url.Values{"q": {title}, "per_page": {"5"}})
}

// BrowseMovies returns one page of TPDB's movie catalog with NO search term —
// the GET /movies analogue of BrowseScenes' plain paginated browse. page/perPage
// are clamped the same way (page >= 1; perPage defaults to defaultBrowsePerPage
// when non-positive). Unlike BrowseScenes, no orderBy param is sent — movies
// need no server-side sort here.
func (c *Client) BrowseMovies(ctx context.Context, page, perPage int) ([]Scene, error) {
	if perPage <= 0 {
		perPage = defaultBrowsePerPage
	}
	if page <= 0 {
		page = 1
	}
	return c.getScenes(ctx, "/movies", url.Values{
		"per_page": {strconv.Itoa(perPage)},
		"page":     {strconv.Itoa(page)},
	})
}

// MoviesBySite returns one page of a single site's movies via TPDB's dedicated
// GET /sites/{identifier}/movies endpoint — the movies analogue of
// ScenesBySite. siteID is the opaque id string this client already returns from
// Site.ID (the flexID-decoded _id); it's URL-path escaped and used directly,
// never parsed as an int. page/perPage are clamped like the browse methods.
func (c *Client) MoviesBySite(ctx context.Context, siteID string, page, perPage int) ([]Scene, error) {
	if perPage <= 0 {
		perPage = defaultBrowsePerPage
	}
	if page <= 0 {
		page = 1
	}
	params := url.Values{
		"per_page": {strconv.Itoa(perPage)},
		"page":     {strconv.Itoa(page)},
	}
	return c.getScenes(ctx, "/sites/"+url.PathEscape(siteID)+"/movies", params)
}

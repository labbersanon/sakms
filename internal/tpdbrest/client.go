// Package tpdbrest is a minimal client for ThePornDB's REST API — used as a
// fallback where its GraphQL endpoint (see internal/stashbox) doesn't cover a
// lookup (e.g. hash-based search), and for title text search.
package tpdbrest

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"github.com/curtiswtaylorjr/sakms/internal/httpx"
)

type Client struct {
	baseURL string
	apiKey  string
	http    *http.Client
}

func New(baseURL, apiKey string, httpClient *http.Client) *Client {
	return &Client{baseURL: baseURL, apiKey: apiKey, http: httpClient}
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
// Image is TPDB's top-level "image" field — the primary scene still/poster URL,
// served from TPDB's own image CDN (cdn.theporndb.net, and legacy
// cdn.metadataapi.net — both subdomains of the domains internal/imageproxy
// already allowlists). The scene object also carries poster_image/poster and a
// posters[] array, but the flat "image" field is the one universally present
// and is what the Discover thumbnail uses. It can be empty for scenes with no
// art, so consumers must degrade gracefully. Anchored to TPDB's documented v2
// scene shape (Jellyfin/Plex TPDB agents, community Go clients); the field is
// modeled from that documentation, not confirmed against a live authenticated
// instance in-repo.
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
	ID       flexID         `json:"_id"`
	Title    string         `json:"title"`
	Slug     string         `json:"slug"`
	Date     string         `json:"date"`
	Site     *rawSite       `json:"site"`
	Image    string         `json:"image"`
	Duration int            `json:"duration"`
	Rating   float64        `json:"rating"`
	Hashes   []rawSceneHash `json:"hashes"`
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
	return Scene{ID: string(s.ID), Title: s.Title, Slug: s.Slug, Date: s.Date, Site: site, Image: s.Image, Duration: s.Duration, Rating: s.Rating, Hashes: phashes}
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
}

// doGet is the shared GET+decode mechanics every REST lookup (scenes,
// performers, sites) uses — path-scoped so each gets its own typed wrapper
// below rather than every caller reaching into a shared /scenes endpoint.
func (c *Client) doGet(ctx context.Context, path string, params url.Values, out any) error {
	u := c.baseURL + path + "?" + params.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	return httpx.DoJSON(c.http, req, httpx.MaxResponseBodySize, out)
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
	var sr scenesResponse
	if err := c.doGet(ctx, path, params, &sr); err != nil {
		return nil, err
	}
	out := make([]Scene, len(sr.Data))
	for i, rs := range sr.Data {
		out[i] = rs.toScene()
	}
	return out, nil
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

// Performer mirrors a subset of ThePornDB's REST performer response shape.
// Image is the single chosen image URL — see rawPerformer for how it's picked
// from TPDB's several nullable image fields; may be empty (no art on file), so
// consumers must degrade gracefully.
type Performer struct {
	ID    string
	Name  string
	Image string
}

// rawPerformer mirrors the fields this client consumes from a TPDB
// PerformerResource. Per the live OpenAPI schema
// (https://api.theporndb.net/openapi.json) a performer carries three nullable
// image fields — "image", "thumbnail", "face" (there is NO field literally
// named "avatar") — and toPerformer collapses them into the single Image field
// above by first-non-empty preference: image, then thumbnail, then face.
type rawPerformer struct {
	ID        flexID `json:"_id"`
	Name      string `json:"name"`
	Image     string `json:"image"`
	Thumbnail string `json:"thumbnail"`
	Face      string `json:"face"`
}

func (rp rawPerformer) toPerformer() Performer {
	return Performer{ID: string(rp.ID), Name: rp.Name, Image: firstNonEmpty(rp.Image, rp.Thumbnail, rp.Face)}
}

type performersResponse struct {
	Data []rawPerformer `json:"data"`
}

func (c *Client) getPerformers(ctx context.Context, params url.Values) ([]Performer, error) {
	var pr performersResponse
	if err := c.doGet(ctx, "/performers", params, &pr); err != nil {
		return nil, err
	}
	out := make([]Performer, len(pr.Data))
	for i, rp := range pr.Data {
		out[i] = rp.toPerformer()
	}
	return out, nil
}

// SearchPerformers text-searches performers by name. Similarity filtering of
// results is business logic that belongs in internal/identify, not here —
// same convention as SearchByTitle.
func (c *Client) SearchPerformers(ctx context.Context, term string) ([]Performer, error) {
	return c.getPerformers(ctx, url.Values{"q": {term}})
}

// BrowsePerformers returns one page of TPDB's performer catalog with NO search
// term — the plain paginated browse backing Adult Discover's Performers row.
// The live OpenAPI spec confirms GET /performers requires no "q" (it's absent
// from that endpoint's required params); page/per_page alone are a valid browse,
// exactly like BrowseScenes. page/perPage are clamped the same way (page >= 1;
// perPage defaults to defaultBrowsePerPage when non-positive). The spec's
// optional "letter" first-initial filter is deliberately not used here.
func (c *Client) BrowsePerformers(ctx context.Context, page, perPage int) ([]Performer, error) {
	if perPage <= 0 {
		perPage = defaultBrowsePerPage
	}
	if page <= 0 {
		page = 1
	}
	return c.getPerformers(ctx, url.Values{
		"per_page": {strconv.Itoa(perPage)},
		"page":     {strconv.Itoa(page)},
	})
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
	return c.getScenes(ctx, "/performers/"+url.PathEscape(performerID)+"/scenes", params)
}

// Site mirrors a subset of ThePornDB's REST site (studio) response shape.
// Image is the single chosen image URL — see rawSiteEntry for how it's picked
// from TPDB's several nullable image fields; may be empty, so consumers must
// degrade gracefully.
type Site struct {
	ID    string
	Name  string
	Image string
}

// rawSiteEntry mirrors the fields this client consumes from a TPDB
// SiteResource. Per the live OpenAPI schema
// (https://api.theporndb.net/openapi.json) a site carries three nullable image
// fields — "logo", "favicon", "poster" — and toSite collapses them into the
// single Image field above by first-non-empty preference: logo, then poster,
// then favicon (favicon last as it's the least presentable at grid size).
type rawSiteEntry struct {
	ID      flexID `json:"_id"`
	Name    string `json:"name"`
	Logo    string `json:"logo"`
	Favicon string `json:"favicon"`
	Poster  string `json:"poster"`
}

func (rs rawSiteEntry) toSite() Site {
	return Site{ID: string(rs.ID), Name: rs.Name, Image: firstNonEmpty(rs.Logo, rs.Poster, rs.Favicon)}
}

type sitesResponse struct {
	Data []rawSiteEntry `json:"data"`
}

func (c *Client) getSites(ctx context.Context, params url.Values) ([]Site, error) {
	var sr sitesResponse
	if err := c.doGet(ctx, "/sites", params, &sr); err != nil {
		return nil, err
	}
	out := make([]Site, len(sr.Data))
	for i, rs := range sr.Data {
		out[i] = rs.toSite()
	}
	return out, nil
}

// SearchSites text-searches sites (studios) by name.
func (c *Client) SearchSites(ctx context.Context, term string) ([]Site, error) {
	return c.getSites(ctx, url.Values{"q": {term}})
}

// BrowseSites returns one page of TPDB's site (studio) catalog with NO search
// term — the plain paginated browse backing Adult Discover's Studios row. The
// live OpenAPI spec confirms GET /sites requires no "q" (absent from its
// required params); page/per_page alone are a valid browse. page/perPage are
// clamped like the other browse methods (page >= 1; perPage defaults to
// defaultBrowsePerPage). The spec's optional "letter" filter is not used here.
func (c *Client) BrowseSites(ctx context.Context, page, perPage int) ([]Site, error) {
	if perPage <= 0 {
		perPage = defaultBrowsePerPage
	}
	if page <= 0 {
		page = 1
	}
	return c.getSites(ctx, url.Values{
		"per_page": {strconv.Itoa(perPage)},
		"page":     {strconv.Itoa(page)},
	})
}

// ScenesBySite returns one page of a single site's scenes via TPDB's dedicated
// GET /sites/{identifier}/scenes endpoint (confirmed in the live OpenAPI path
// list; it accepts only identifier (path) + page + per_page (query) — no other
// filter params). siteID is the opaque id string this client already returns
// from Site.ID (the flexID-decoded _id); it's URL-path escaped and used
// directly, never parsed as an int. Preferred over filtering /scenes by a
// site_id query param — the dedicated endpoint is what the spec provides for
// exactly this drill-down.
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

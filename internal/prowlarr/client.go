// Package prowlarr is a client for Prowlarr's indexer-search API — Prowlarr
// aggregates every indexer a user has configured behind one normalized
// endpoint, so this client only ever needs to know Prowlarr's own wire
// shape, never any individual tracker's Torznab/Newznab quirks (that
// normalization is Prowlarr's whole job).
//
// The response shape below (ReleaseResource: title/guid/indexer/protocol/
// size/seeders/downloadUrl/publishDate/categories) is modeled on Prowlarr's
// documented /api/v1/search endpoint, which mirrors the release-search
// resource shared across the Servarr-family apps (Radarr/Sonarr/Prowlarr are
// built on the same underlying codebase). This has NOT been run against a
// real Prowlarr instance yet — flagging that honestly, the same way this
// project already flags its unverified Whisparr Dedup assumption, rather
// than presenting it as confirmed.
package prowlarr

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/labbersanon/sakms/internal/httpx"
)

// Protocol distinguishes a torrent release (grabbed via a torrent client)
// from a usenet one (grabbed via a usenet client) — Prowlarr reports this
// per-result, since a single search can span indexers of both kinds.
type Protocol string

const (
	Torrent Protocol = "torrent"
	Usenet  Protocol = "usenet"
)

// Config parameterizes the client for one Prowlarr instance.
type Config struct {
	BaseURL string
	APIKey  string
}

type Client struct {
	cfg  Config
	http *http.Client
}

func New(cfg Config, httpClient *http.Client) *Client {
	return &Client{cfg: cfg, http: httpClient}
}

func (c *Client) do(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.cfg.BaseURL+path, nil)
	if err != nil {
		return fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("X-Api-Key", c.cfg.APIKey)
	return httpx.DoJSON(c.http, req, httpx.MaxResponseBodySize, out)
}

// Release is one search result — a release Prowlarr found on some indexer,
// not yet grabbed.
type Release struct {
	GUID        string   `json:"guid"`
	Title       string   `json:"title"`
	Indexer     string   `json:"indexer"`
	Protocol    Protocol `json:"protocol"`
	Size        int64    `json:"size"`
	Seeders     int      `json:"seeders"`
	DownloadURL string   `json:"downloadUrl"`
	PublishDate string   `json:"publishDate"`
	Categories  []int    `json:"categories"`
	// IndexerFlags is Prowlarr's per-result indexer metadata (e.g.
	// "freeleech", "internal") — used by release.ScoreCandidate as the one
	// "reputation" signal this project sources, no additional lookup. Like
	// the rest of this client's shape, this field is modeled on Prowlarr's
	// documented API and has NOT been confirmed against a real instance.
	IndexerFlags []string `json:"indexerFlags"`
}

// releaseResource is the raw shape Prowlarr's /api/v1/search returns —
// decoded into this first, then mapped into Release, so a shape mismatch
// against the real API (this hasn't been confirmed live — see package doc)
// is easy to isolate and fix without touching every caller of Release.
type releaseResource struct {
	GUID        string `json:"guid"`
	Title       string `json:"title"`
	Indexer     string `json:"indexer"`
	Protocol    string `json:"protocol"`
	Size        int64  `json:"size"`
	Seeders     int    `json:"seeders"`
	DownloadURL string `json:"downloadUrl"`
	PublishDate string `json:"publishDate"`
	Categories  []struct {
		ID int `json:"id"`
	} `json:"categories"`
	IndexerFlags []string `json:"indexerFlags"`
}

// Search queries every indexer Prowlarr has configured for query, restricted
// to categories (Newznab category codes — e.g. the 2000-range for Movies,
// the 5000-range for TV; Prowlarr normalizes indexer-specific category
// mappings onto this same numbering, so the caller never needs to know a
// given indexer's own category IDs).
func (c *Client) Search(ctx context.Context, query string, categories []int) ([]Release, error) {
	q := url.Values{}
	q.Set("query", query)
	q.Set("type", "search")
	addCategories(q, categories)
	return c.search(ctx, q)
}

// SearchByIDParams carries the structured ids for an id-based indexer query.
// Only non-zero/non-empty fields are sent — this is how Radarr/Sonarr query
// Prowlarr internally (a precise id-scoped search rather than a fuzzy title
// match), which is what makes availability probing exact.
//
// Query matters more than its name suggests: found via a real "nothing is
// being found to grab" investigation (164 raw releases came back for a
// Moana search, none of them Moana — The Mummy, Starship Troopers,
// Fractured...). An id-only request (type=movie&tmdbid=X&imdbid=Y, no query
// text) isn't reliably honored as a precise filter by every indexer —
// several fall back to Torznab's "empty query = list recent releases in
// this category" RSS-style behavior when there's no query string to anchor
// the search, silently ignoring the id params entirely. Radarr/Sonarr
// themselves send the title as query text ALONGSIDE the id params for
// exactly this reason (broader indexer compatibility, not redundancy) —
// SearchByID previously didn't, which was the actual bug. Always send the
// title here; there is no real caller that has ids but not a title.
type SearchByIDParams struct {
	Query      string // the title — see the field's own doc comment above
	TMDBID     int    // 0 if not applicable
	IMDBID     string // "" if not applicable ("tt" prefix is stripped — see SearchByID)
	TVDBID     int    // 0 if not applicable (Series only)
	Season     int    // 0 if not applicable
	Episode    int    // 0 if not applicable
	Categories []int
}

// SearchByID runs a structured, id-based Prowlarr search — the id-scoped
// equivalent of the free-text Search, sharing its response shape and its
// do+parse mechanics (only the query-string construction differs).
//
// UNVERIFIED-ASSUMPTION NOTE (matching this package's existing
// honesty-about-unverified-assumptions posture — see the package doc): the
// exact wire contract of Prowlarr's /api/v1/search for id-based queries has
// NOT been confirmed against a live instance. This models the standard
// Newznab/Torznab "separate structured params" convention:
//
//   - type=movie   for a movie search (Newznab's `t=movie` function),
//     carrying imdbid/tmdbid — used when TMDBID/IMDBID are present without a
//     season/episode.
//   - type=tvsearch for a TV search (Newznab's `t=tvsearch` function),
//     carrying tvdbid/season/ep (and imdbid, valid there too) — used when a
//     TVDBID or a Season/Episode is present.
//
// The `t=`-function values are `movie`/`tvsearch` (Newznab's caps XML
// advertises these as the `<movie-search>`/`<tv-search>` capability
// *elements*, but the invoked function values are the shorter `movie`/
// `tvsearch` — the same value-space as the existing free-text
// `type=search`). The `imdbid` param is Newznab-conventionally the numeric
// id with no leading "tt", so any "tt" prefix is stripped below. Episode is
// the `ep` param, not `episode`.
//
// If the real endpoint instead expects these ids embedded in the free-text
// `query` string, only this query-building differs — the shared do+parse
// path decodes into releaseResource, so a wire mismatch stays isolated to
// this one method, exactly as the package doc describes for Release.
func (c *Client) SearchByID(ctx context.Context, params SearchByIDParams) ([]Release, error) {
	q := url.Values{}

	isTV := params.TVDBID != 0 || params.Season != 0 || params.Episode != 0
	if isTV {
		q.Set("type", "tvsearch")
	} else {
		q.Set("type", "movie")
	}

	if params.Query != "" {
		q.Set("query", params.Query)
	}
	if params.TMDBID != 0 {
		q.Set("tmdbid", strconv.Itoa(params.TMDBID))
	}
	if params.IMDBID != "" {
		q.Set("imdbid", strings.TrimPrefix(params.IMDBID, "tt"))
	}
	if params.TVDBID != 0 {
		q.Set("tvdbid", strconv.Itoa(params.TVDBID))
	}
	if params.Season != 0 {
		q.Set("season", strconv.Itoa(params.Season))
	}
	if params.Episode != 0 {
		q.Set("ep", strconv.Itoa(params.Episode))
	}
	addCategories(q, params.Categories)

	return c.search(ctx, q)
}

// addCategories appends the shared Newznab-category param used by both
// search entry points (omitted entirely when none are given).
func addCategories(q url.Values, categories []int) {
	if len(categories) == 0 {
		return
	}
	cats := make([]string, len(categories))
	for i, cat := range categories {
		cats[i] = strconv.Itoa(cat)
	}
	q.Set("categories", strings.Join(cats, ","))
}

// search performs the /api/v1/search GET for an already-built query and maps
// the raw releaseResource list into Release — the shared HTTP+parse
// mechanics both Search and SearchByID differ only in how they reach.
func (c *Client) search(ctx context.Context, q url.Values) ([]Release, error) {
	var raw []releaseResource
	if err := c.do(ctx, "/api/v1/search?"+q.Encode(), &raw); err != nil {
		return nil, err
	}

	out := make([]Release, len(raw))
	for i, r := range raw {
		cats := make([]int, len(r.Categories))
		for j, cat := range r.Categories {
			cats[j] = cat.ID
		}
		out[i] = Release{
			GUID:         r.GUID,
			Title:        r.Title,
			Indexer:      r.Indexer,
			Protocol:     Protocol(strings.ToLower(r.Protocol)),
			Size:         r.Size,
			Seeders:      r.Seeders,
			DownloadURL:  r.DownloadURL,
			PublishDate:  r.PublishDate,
			Categories:   cats,
			IndexerFlags: r.IndexerFlags,
		}
	}
	return out, nil
}

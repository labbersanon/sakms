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
//
// Claude 2026-08-03: added SeasonSpecified.
// Reason: Season 0 (Specials) is a legitimate, deliberate scope — but
// Season==0 is also Go's zero value for "no season was ever picked" (a
// whole-show/whole-series probe). Without a separate boolean, SearchByID
// cannot tell those two apart, so a genuine Specials request would silently
// omit the {Season:0} token and search unscoped instead.
// Troubleshooting: mirrors the exact same Season-0-vs-unspecified ambiguity
// grabs.Grab.SeasonSpecified already exists to solve (see that field's doc).
// Review if: SearchByIDParams gains a richer season/episode scope type that
// makes the "was a season picked at all" question structurally unambiguous
// without a side boolean.
type SearchByIDParams struct {
	Query           string // the title — see the field's own doc comment above
	TMDBID          int    // 0 if not applicable
	IMDBID          string // "" if not applicable (a missing "tt" prefix is added for the brace form — see SearchByID)
	TVDBID          int    // 0 if not applicable (Series only)
	Season          int    // meaningful only when SeasonSpecified is true
	SeasonSpecified bool   // true when a season (possibly 0/Specials) was deliberately picked
	Episode         int    // 0 if not applicable
	Categories      []int
}

// SearchByID runs a structured, id-based Prowlarr search — the id-scoped
// equivalent of the free-text Search, sharing its response shape and its
// do+parse mechanics (only the query-string construction differs).
//
// Claude 2026-08-03: rewrote the query-building from separate top-level
// params (tmdbid=/imdbid=/tvdbid=/season=/ep=) to brace tokens embedded in
// the query= string.
// Reason: Prowlarr's /api/v1/search does NOT parse standalone
// tmdbid/imdbid/tvdbid/season/ep query params for a manual search request —
// its SearchRequest DTO has no such fields. The only place those ids are
// read from is QueryToParams, which regex-parses bracketed tokens
// (`{TvdbId:81189}`, `{Season:4}`, `{Episode:5}`, `{TmdbId:550}`,
// `{ImdbId:tt0137523}`) out of the free-text query string itself — this is
// documented on the Servarr wiki's Prowlarr indexer-search page and
// confirmed against Prowlarr's own source (QueryToParams). The previous
// top-level-params version was an UNVERIFIED ASSUMPTION (modeled on the
// generic Newznab/Torznab convention, which Prowlarr's OWN manual-search
// endpoint does not actually follow) and was the root cause of a real
// "picking Season 4 grabs S1E1" bug: every structured id/season/episode
// param was silently ignored, so SearchByID always ran an unscoped
// title-only search and returned whatever release the query-title
// similarity/scoring pass ranked first, regardless of which season was
// requested.
// Troubleshooting: if a season/episode-scoped grab starts returning
// wrong-season or wrong-episode releases again, check this brace-token
// construction first — a regression here reintroduces exactly the bug
// above. type=tvsearch vs type=movie is unaffected by this change (that
// selector is a real, working Prowlarr param) and Categories is unaffected
// (categories= is also a real, working param) — only the id/season/episode
// encoding moved.
// Review if: Prowlarr adds real top-level id params to its manual-search
// API in a future version (there is no such indication as of this date).
func (c *Client) SearchByID(ctx context.Context, params SearchByIDParams) ([]Release, error) {
	q := url.Values{}

	isTV := params.TVDBID != 0 || params.SeasonSpecified || params.Season != 0 || params.Episode != 0
	if isTV {
		q.Set("type", "tvsearch")
	} else {
		q.Set("type", "movie")
	}

	var tokens []string
	if params.TMDBID != 0 {
		tokens = append(tokens, fmt.Sprintf("{TmdbId:%d}", params.TMDBID))
	}
	if params.IMDBID != "" {
		// The brace form carries the "tt" prefix (unlike the old top-level
		// imdbid= param, which was the bare numeric id) — add it back if the
		// caller passed a bare number.
		imdb := params.IMDBID
		if !strings.HasPrefix(imdb, "tt") {
			imdb = "tt" + imdb
		}
		tokens = append(tokens, fmt.Sprintf("{ImdbId:%s}", imdb))
	}
	if params.TVDBID != 0 {
		tokens = append(tokens, fmt.Sprintf("{TvdbId:%d}", params.TVDBID))
	}
	if params.SeasonSpecified {
		// Season 0 (Specials) is deliberately included here — the whole
		// point of SeasonSpecified is to distinguish "Season 0 was picked"
		// from "no season was picked at all" (Season's own zero value).
		tokens = append(tokens, fmt.Sprintf("{Season:%d}", params.Season))
	}
	if params.Episode != 0 {
		tokens = append(tokens, fmt.Sprintf("{Episode:%d}", params.Episode))
	}

	// Braces first, then the free-text title (see this type's Query doc
	// comment for why the title must still travel alongside the ids).
	queryParts := tokens
	if params.Query != "" {
		queryParts = append(queryParts, params.Query)
	}
	if queryStr := strings.TrimSpace(strings.Join(queryParts, " ")); queryStr != "" {
		q.Set("query", queryStr)
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

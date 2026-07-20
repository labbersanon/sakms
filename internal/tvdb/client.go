// Package tvdb is a client for TheTVDB's v4 REST API, used as a search
// fallback when TMDB's own search returns no results or a below-threshold
// confidence match for Movies and Series Rename. TheTVDB is a fixed public
// service (not self-hostable), but SAK stores its API key as a normal "tvdb"
// connections.Store entry, matching how it already treats TMDB.
//
// Authentication uses TVDB v4's bearer-token flow (POST /v4/login with the
// API key in the request body); the returned token is cached in memory for
// up to 29 days, re-fetched lazily on the next call after expiry. Token
// caching is safe for concurrent use via mu.
//
// UNVERIFIED ASSUMPTION: the v4 /v4/search response shape (field names
// tvdb_id/name/year, string-encoded numeric ids and year) is modeled from
// TheTVDB's public API documentation — not confirmed against a live instance.
// If field names change between TVDB API revisions, decoded items will have
// zero TVDBID values and be silently filtered, degrading to "no TVDB result"
// (the same as 0 search results) rather than an error.
package tvdb

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/labbersanon/sakms/internal/httpx"
)

// DefaultBaseURL is TheTVDB's v4 API endpoint. A var (not const) so tests can
// override it to point at an httptest server, same as tmdb.DefaultBaseURL.
var DefaultBaseURL = "https://api4.thetvdb.com"

// tokenTTL is how long a TVDB bearer token remains valid. TVDB documents
// 30 days; we refresh one day early to avoid clock-drift races at the boundary.
const tokenTTL = 29 * 24 * time.Hour

// Config holds TVDB v4 credentials. APIKey is obtained from
// thetvdb.com/dashboard after registering a project. BaseURL is normally
// DefaultBaseURL; tests override it to point at a local httptest server.
type Config struct {
	BaseURL string
	APIKey  string
}

// Client is a TVDB v4 API client. Create with New; safe for concurrent use.
type Client struct {
	cfg     Config
	http    *http.Client
	mu      sync.Mutex
	token   string
	tokenAt time.Time
}

// New returns a new Client backed by httpClient.
func New(cfg Config, httpClient *http.Client) *Client {
	return &Client{cfg: cfg, http: httpClient}
}

// loginBody is the JSON payload for POST /v4/login.
type loginBody struct {
	APIKey string `json:"apikey"`
}

type loginResponse struct {
	Status string `json:"status"`
	Data   struct {
		Token string `json:"token"`
	} `json:"data"`
}

// ensureToken fetches a fresh bearer token if the cached one is absent or
// older than tokenTTL. Caller must NOT hold mu.
func (c *Client) ensureToken(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" && time.Since(c.tokenAt) < tokenTTL {
		return nil
	}
	body, err := json.Marshal(loginBody{APIKey: c.cfg.APIKey})
	if err != nil {
		return fmt.Errorf("tvdb: marshaling login body: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.cfg.BaseURL+"/v4/login", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("tvdb: building login request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	var lr loginResponse
	if err := httpx.DoJSON(c.http, req, httpx.MaxResponseBodySize, &lr); err != nil {
		return fmt.Errorf("tvdb: login: %w", err)
	}
	if lr.Data.Token == "" {
		return fmt.Errorf("tvdb: login returned empty token (status %q)", lr.Status)
	}
	c.token = lr.Data.Token
	c.tokenAt = time.Now()
	return nil
}

// Result is one item from a TVDB search response.
type Result struct {
	TVDBID int
	Name   string
	Year   int // 0 if unknown or unparseable
}

// searchResponse is the top-level envelope for GET /v4/search.
type searchResponse struct {
	Status string       `json:"status"`
	Data   []searchItem `json:"data"`
}

// searchItem is one entry in /v4/search's data array. Both tvdb_id and year
// are strings in the TVDB v4 API (UNVERIFIED ASSUMPTION — see package doc).
type searchItem struct {
	TVDBID string `json:"tvdb_id"` // numeric string, e.g. "81189"
	Name   string `json:"name"`
	Year   string `json:"year"` // "YYYY" or "" if absent
}

// doSearch calls GET /v4/search with the given query and type filter
// ("series" or "movie"), returning decoded results. Items with a zero or
// non-numeric TVDBID or an empty name are silently dropped.
func (c *Client) doSearch(ctx context.Context, query, mediaType string) ([]Result, error) {
	if err := c.ensureToken(ctx); err != nil {
		return nil, err
	}
	c.mu.Lock()
	token := c.token
	c.mu.Unlock()

	q := url.Values{}
	q.Set("query", query)
	q.Set("type", mediaType)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.cfg.BaseURL+"/v4/search?"+q.Encode(), nil)
	if err != nil {
		return nil, fmt.Errorf("tvdb: building search request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)

	var sr searchResponse
	if err := httpx.DoJSON(c.http, req, httpx.MaxResponseBodySize, &sr); err != nil {
		return nil, fmt.Errorf("tvdb: search %q (%s): %w", query, mediaType, err)
	}

	out := make([]Result, 0, len(sr.Data))
	for _, d := range sr.Data {
		if d.TVDBID == "" || d.Name == "" {
			continue
		}
		id, err := strconv.Atoi(d.TVDBID)
		if err != nil || id <= 0 {
			continue
		}
		year, _ := strconv.Atoi(d.Year)
		out = append(out, Result{TVDBID: id, Name: d.Name, Year: year})
	}
	return out, nil
}

// SearchSeries searches TheTVDB for TV series by title.
func (c *Client) SearchSeries(ctx context.Context, query string) ([]Result, error) {
	return c.doSearch(ctx, query, "series")
}

// SearchMovies searches TheTVDB for movies by title.
func (c *Client) SearchMovies(ctx context.Context, query string) ([]Result, error) {
	return c.doSearch(ctx, query, "movie")
}

// Ping verifies connectivity and API-key validity by forcing a fresh token
// fetch (bypassing the cache). Used by TestConnection in internal/api.
func (c *Client) Ping(ctx context.Context) error {
	c.mu.Lock()
	c.token = "" // force re-fetch
	c.mu.Unlock()
	return c.ensureToken(ctx)
}

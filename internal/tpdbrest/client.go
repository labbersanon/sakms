// Package tpdbrest is a minimal client for ThePornDB's REST API — used as a
// fallback where its GraphQL endpoint (see internal/stashbox) doesn't cover a
// lookup (e.g. hash-based search), and for title text search.
package tpdbrest

import (
	"context"
	"fmt"
	"net/http"
	"net/url"

	"github.com/curtiswtaylorjr/tidyarr/internal/httpx"
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
}

type rawSite struct {
	Name string `json:"name"`
}

type rawScene struct {
	ID    string   `json:"_id"`
	Title string   `json:"title"`
	Date  string   `json:"date"`
	Site  *rawSite `json:"site"`
}

func (s rawScene) toScene() Scene {
	site := ""
	if s.Site != nil {
		site = s.Site.Name
	}
	return Scene{ID: s.ID, Title: s.Title, Date: s.Date, Site: site}
}

type scenesResponse struct {
	Data []rawScene `json:"data"`
}

func (c *Client) get(ctx context.Context, params url.Values) ([]Scene, error) {
	u := c.baseURL + "/scenes?" + params.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	var sr scenesResponse
	if err := httpx.DoJSON(c.http, req, httpx.MaxResponseBodySize, &sr); err != nil {
		return nil, err
	}
	out := make([]Scene, len(sr.Data))
	for i, rs := range sr.Data {
		out[i] = rs.toScene()
	}
	return out, nil
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

// Package bravesearch is a minimal client for the Brave Search API — used as a
// web-search fallback when internal database lookups don't identify a file.
//
// This package is a plain network client with no policy about missing keys or
// graceful degradation; that decision belongs to the caller (internal/identify
// / the orchestration layer), matching how the Brave key is lazily loaded and
// treated as optional (unlike Stash/StashDB/TPDB, which are hard requirements).
package bravesearch

import (
	"context"
	"fmt"
	"net/http"

	"github.com/curtiswtaylorjr/sak/internal/httpx"
)

type Client struct {
	baseURL string
	apiKey  string
	http    *http.Client
}

func New(baseURL, apiKey string, httpClient *http.Client) *Client {
	return &Client{baseURL: baseURL, apiKey: apiKey, http: httpClient}
}

type Result struct {
	Title       string
	Description string
	URL         string
}

type webResults struct {
	Web struct {
		Results []struct {
			Title       string `json:"title"`
			Description string `json:"description"`
			URL         string `json:"url"`
		} `json:"results"`
	} `json:"web"`
}

// Ping confirms the API key works by performing one real, minimal search —
// Brave's API has no separate lightweight "verify key" endpoint, so this is
// the only honest check available. It costs one query against the account's
// quota, same as any other call this client makes.
func (c *Client) Ping(ctx context.Context) error {
	_, err := c.Search(ctx, "test", 1)
	return err
}

// Search performs a web search and returns up to count results.
func (c *Client) Search(ctx context.Context, query string, count int) ([]Result, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL, nil)
	if err != nil {
		return nil, fmt.Errorf("building request: %w", err)
	}
	q := req.URL.Query()
	q.Set("q", query)
	q.Set("count", fmt.Sprintf("%d", count))
	req.URL.RawQuery = q.Encode()
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Subscription-Token", c.apiKey)

	var wr webResults
	if err := httpx.DoJSON(c.http, req, httpx.MaxResponseBodySize, &wr); err != nil {
		return nil, err
	}
	out := make([]Result, len(wr.Web.Results))
	for i, r := range wr.Web.Results {
		out[i] = Result{Title: r.Title, Description: r.Description, URL: r.URL}
	}
	return out, nil
}

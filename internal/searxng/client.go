// Package searxng is a minimal client for a self-hosted SearXNG instance.
package searxng

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/labbersanon/sakms/internal/httpx"
	"github.com/labbersanon/sakms/internal/websearch"
)

type Client struct {
	baseURL string
	http    *http.Client
}

func New(baseURL string, httpClient *http.Client) *Client {
	return &Client{baseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"), http: httpClient}
}

type jsonResponse struct {
	Results []struct {
		Title   string `json:"title"`
		Content string `json:"content"`
		URL     string `json:"url"`
	} `json:"results"`
}

// Ping confirms JSON search works with one minimal query.
func (c *Client) Ping(ctx context.Context) error {
	_, err := c.Search(ctx, "test", 1)
	return err
}

// Search performs a SearXNG JSON search and returns up to count results.
func (c *Client) Search(ctx context.Context, query string, count int) ([]websearch.Result, error) {
	if c == nil || c.baseURL == "" {
		return nil, nil
	}
	if count <= 0 {
		count = 5
	}
	u, err := url.Parse(c.baseURL + "/search")
	if err != nil {
		return nil, fmt.Errorf("searxng: bad base URL: %w", err)
	}
	q := u.Query()
	q.Set("q", query)
	q.Set("format", "json")
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	var body jsonResponse
	if err := httpx.DoJSON(c.http, req, httpx.MaxResponseBodySize, &body); err != nil {
		return nil, err
	}
	n := len(body.Results)
	if n > count {
		n = count
	}
	out := make([]websearch.Result, 0, n)
	for i := 0; i < n; i++ {
		r := body.Results[i]
		out = append(out, websearch.Result{Title: r.Title, Description: r.Content, URL: r.URL})
	}
	return out, nil
}

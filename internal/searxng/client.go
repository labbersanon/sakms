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

type jsonResult struct {
	Title        string `json:"title"`
	Content      string `json:"content"`
	URL          string `json:"url"`
	ImgSrc       string `json:"img_src"`
	ThumbnailSrc string `json:"thumbnail_src"`
}

type jsonResponse struct {
	Results []jsonResult `json:"results"`
}

// Ping confirms JSON search works with one minimal query.
func (c *Client) Ping(ctx context.Context) error {
	_, err := c.Search(ctx, "test", 1)
	return err
}

// Search performs a SearXNG JSON search and returns up to count results.
func (c *Client) Search(ctx context.Context, query string, count int) ([]websearch.Result, error) {
	body, n, err := c.fetch(ctx, query, count, "")
	if err != nil || n == 0 {
		return nil, err
	}
	out := make([]websearch.Result, 0, n)
	for i := 0; i < n; i++ {
		r := body.Results[i]
		out = append(out, websearch.Result{Title: r.Title, Description: r.Content, URL: r.URL})
	}
	return out, nil
}

// SearchImages queries the images category and returns direct https image URLs.
// Page links and this instance's own image-proxy URLs are dropped.
// Claude 2026-09-23: image category for posters when TMDB and TVDB have no art.
// Reason: a general web hit is a page, not a poster file.
// Troubleshooting: empty image results → SearXNG has no image engines enabled.
// Review if: an image engine is pinned in the SearXNG settings.
func (c *Client) SearchImages(ctx context.Context, query string, count int) ([]websearch.Result, error) {
	body, n, err := c.fetch(ctx, query, count, "images")
	if err != nil || n == 0 {
		return nil, err
	}
	out := make([]websearch.Result, 0, n)
	for i := 0; i < n; i++ {
		r := body.Results[i]
		img := c.directImageURL(r)
		if img == "" {
			continue
		}
		out = append(out, websearch.Result{Title: r.Title, Description: r.Content, URL: img})
	}
	return out, nil
}

func (c *Client) fetch(ctx context.Context, query string, count int, category string) (jsonResponse, int, error) {
	var body jsonResponse
	if c == nil || c.baseURL == "" {
		return body, 0, nil
	}
	if count <= 0 {
		count = 5
	}
	u, err := url.Parse(c.baseURL + "/search")
	if err != nil {
		return body, 0, fmt.Errorf("searxng: bad base URL: %w", err)
	}
	q := u.Query()
	q.Set("q", query)
	q.Set("format", "json")
	if category != "" {
		q.Set("categories", category)
	}
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return body, 0, fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	if err := httpx.DoJSON(c.http, req, httpx.MaxResponseBodySize, &body); err != nil {
		return body, 0, err
	}
	n := len(body.Results)
	if n > count {
		n = count
	}
	return body, n, nil
}

func (c *Client) directImageURL(r jsonResult) string {
	for _, raw := range []string{r.ImgSrc, r.ThumbnailSrc} {
		u, err := url.Parse(strings.TrimSpace(raw))
		if err != nil || u.Scheme != "https" || u.Host == "" {
			continue
		}
		if c.sameHost(u.Host) {
			continue
		}
		return u.String()
	}
	return ""
}

func (c *Client) sameHost(host string) bool {
	base, err := url.Parse(c.baseURL)
	if err != nil || base.Host == "" {
		return false
	}
	return strings.EqualFold(base.Host, host)
}

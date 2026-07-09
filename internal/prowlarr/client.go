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

	"github.com/curtiswtaylorjr/sakms/internal/httpx"
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
	if len(categories) > 0 {
		cats := make([]string, len(categories))
		for i, cat := range categories {
			cats[i] = strconv.Itoa(cat)
		}
		q.Set("categories", strings.Join(cats, ","))
	}

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
			GUID:        r.GUID,
			Title:       r.Title,
			Indexer:     r.Indexer,
			Protocol:    Protocol(strings.ToLower(r.Protocol)),
			Size:        r.Size,
			Seeders:     r.Seeders,
			DownloadURL: r.DownloadURL,
			PublishDate: r.PublishDate,
			Categories:  cats,
		}
	}
	return out, nil
}

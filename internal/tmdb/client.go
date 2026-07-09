// Package tmdb is a client for The Movie Database's public v3 REST API —
// the catalog SAK's Discover view browses (trending/popular titles with
// poster art) before a user picks one to search+grab. TMDB is a single
// fixed public service (like TPDB or Brave), not self-hostable, but still
// modeled as a normal "url + api key" connections.Store entry, matching how
// this project already treats Brave/TPDB.
//
// A movie's TMDB id is exactly what Radarr's AddRequest.TMDBID expects, but
// Sonarr's AddRequest.TVDBID is a DIFFERENT id space entirely (TheTVDB, not
// TMDB) — TMDB covers TV shows too, but under its own id. ExternalIDs
// resolves a TMDB TV show id to its TVDB id, which the Discover flow calls
// once a user actually picks a TV show to search+grab (not for every item in
// a trending list — see the API handler for why that's deferred to
// click-time).
package tmdb

import (
	"context"
	"fmt"
	"net/http"
	"net/url"

	"github.com/curtiswtaylorjr/sakms/internal/httpx"
)

// MediaType distinguishes TMDB's movie and TV catalogs, which use
// different field names for the same concepts (title vs name, release_date
// vs first_air_date) — normalized away into one Item shape below.
type MediaType string

const (
	Movie MediaType = "movie"
	TV    MediaType = "tv"
)

// Config parameterizes the client. BaseURL is normally
// https://api.themoviedb.org/3, stored explicitly (not hardcoded) the same
// way this project already treats Brave's fixed-but-configurable endpoint.
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

// do executes a GET against path (which may already contain its own query
// string), adding TMDB's v3 api_key auth param.
func (c *Client) do(ctx context.Context, path string, query url.Values, out any) error {
	if query == nil {
		query = url.Values{}
	}
	query.Set("api_key", c.cfg.APIKey)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.cfg.BaseURL+path+"?"+query.Encode(), nil)
	if err != nil {
		return fmt.Errorf("building request: %w", err)
	}
	return httpx.DoJSON(c.http, req, httpx.MaxResponseBodySize, out)
}

// Item is one browsable title, normalized across TMDB's movie and TV shapes.
type Item struct {
	ID          int       `json:"id"`
	Title       string    `json:"title"`
	PosterPath  string    `json:"posterPath"`
	Overview    string    `json:"overview"`
	ReleaseDate string    `json:"releaseDate"`
	VoteAverage float64   `json:"voteAverage"`
	MediaType   MediaType `json:"mediaType"`
}

// rawResult covers both TMDB's movie and TV result shapes in one struct —
// Title/Name and ReleaseDate/FirstAirDate are mutually exclusive depending
// on which endpoint returned it, normalized into Item by normalize below.
type rawResult struct {
	ID           int     `json:"id"`
	Title        string  `json:"title"`
	Name         string  `json:"name"`
	PosterPath   string  `json:"poster_path"`
	Overview     string  `json:"overview"`
	ReleaseDate  string  `json:"release_date"`
	FirstAirDate string  `json:"first_air_date"`
	VoteAverage  float64 `json:"vote_average"`
	MediaType    string  `json:"media_type"`
}

type listResponse struct {
	Results []rawResult `json:"results"`
}

func normalize(r rawResult, fallbackType MediaType) Item {
	item := Item{
		ID: r.ID, PosterPath: r.PosterPath, Overview: r.Overview, VoteAverage: r.VoteAverage,
		MediaType: fallbackType,
	}
	if r.MediaType != "" {
		item.MediaType = MediaType(r.MediaType)
	}
	if item.MediaType == TV {
		item.Title = r.Name
		item.ReleaseDate = r.FirstAirDate
	} else {
		item.Title = r.Title
		item.ReleaseDate = r.ReleaseDate
	}
	return item
}

// Trending returns TMDB's trending titles for mt over timeWindow ("day" or
// "week").
func (c *Client) Trending(ctx context.Context, mt MediaType, timeWindow string) ([]Item, error) {
	var resp listResponse
	if err := c.do(ctx, fmt.Sprintf("/trending/%s/%s", mt, timeWindow), nil, &resp); err != nil {
		return nil, err
	}
	return normalizeAll(resp.Results, mt), nil
}

// Popular returns TMDB's currently popular titles for mt.
func (c *Client) Popular(ctx context.Context, mt MediaType) ([]Item, error) {
	var resp listResponse
	if err := c.do(ctx, fmt.Sprintf("/%s/popular", mt), nil, &resp); err != nil {
		return nil, err
	}
	return normalizeAll(resp.Results, mt), nil
}

func normalizeAll(raw []rawResult, mt MediaType) []Item {
	out := make([]Item, len(raw))
	for i, r := range raw {
		out[i] = normalize(r, mt)
	}
	return out
}

type externalIDsResponse struct {
	TVDBID int `json:"tvdb_id"`
}

// ExternalIDs resolves a TMDB TV show id to its TVDB id — 0 if TMDB doesn't
// have one on file for this show (rare, but possible for very new or
// obscure titles).
func (c *Client) ExternalIDs(ctx context.Context, tmdbTVID int) (tvdbID int, err error) {
	var resp externalIDsResponse
	if err := c.do(ctx, fmt.Sprintf("/tv/%d/external_ids", tmdbTVID), nil, &resp); err != nil {
		return 0, err
	}
	return resp.TVDBID, nil
}

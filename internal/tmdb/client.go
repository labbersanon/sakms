// Package tmdb is a client for The Movie Database's public v3 REST API —
// the catalog SAK's Discover view browses (trending/popular titles with
// poster art) before a user picks one to search+grab. TMDB is a single
// fixed public service (like TPDB or Brave), not self-hostable, but still
// modeled as a normal "url + api key" connections.Store entry, matching how
// this project already treats Brave/TPDB.
//
// A movie's TMDB id is a different id space entirely from TheTVDB's — TMDB
// covers TV shows too, but under its own id. ExternalIDs resolves a TMDB TV
// show id to its TVDB id (used by Discover once a user picks a TV show to
// search+grab, not for every item in a trending list). The reverse lookup
// (TVDB id → TMDB id, FindByTVDBID) existed only to serve the one-time Sonarr
// importer and was removed with it (2026-07-12) — SAK's library keys
// everything by TMDB id, and nothing else in the codebase ever needed to
// resolve the other direction.
//
// SearchMovies/Trending/Popular/ExternalIDs are exercised live by this
// project's Discover flow. SeasonDetails, MovieDetails, and TVDetails are
// NOT — their response shapes are modeled from TMDB's public API
// documentation only, per this project's
// honesty-about-unverified-assumptions convention.
package tmdb

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

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

// pageQuery builds the optional TMDB `page` query param (1-based). A page <= 1
// is left off entirely — TMDB defaults to page 1, and omitting it keeps the
// request URL identical to the pre-pagination shape for the common first-page
// call. Matches SearchMovies/SearchTV's url.Values-based query building.
func pageQuery(page int) url.Values {
	if page <= 1 {
		return nil
	}
	q := url.Values{}
	q.Set("page", strconv.Itoa(page))
	return q
}

// Trending returns TMDB's trending titles for mt over timeWindow ("day" or
// "week"), for the given 1-based page (page <= 1 fetches the first page).
func (c *Client) Trending(ctx context.Context, mt MediaType, timeWindow string, page int) ([]Item, error) {
	var resp listResponse
	if err := c.do(ctx, fmt.Sprintf("/trending/%s/%s", mt, timeWindow), pageQuery(page), &resp); err != nil {
		return nil, err
	}
	return normalizeAll(resp.Results, mt), nil
}

// Popular returns TMDB's currently popular titles for mt, for the given 1-based
// page (page <= 1 fetches the first page).
func (c *Client) Popular(ctx context.Context, mt MediaType, page int) ([]Item, error) {
	var resp listResponse
	if err := c.do(ctx, fmt.Sprintf("/%s/popular", mt), pageQuery(page), &resp); err != nil {
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

// SearchMovies searches TMDB's movie catalog by title — the title-lookup
// Rename/Dedup's Movies-library code path uses instead of Servarr's own
// Lookup, since eliminating Radarr for Movies means there's no *arr app's
// TVDB/TMDB search proxy sitting between SAK and TMDB anymore (see
// internal/library's package doc).
func (c *Client) SearchMovies(ctx context.Context, query string) ([]Item, error) {
	q := url.Values{}
	q.Set("query", query)
	var resp listResponse
	if err := c.do(ctx, "/search/movie", q, &resp); err != nil {
		return nil, err
	}
	return normalizeAll(resp.Results, Movie), nil
}

// SearchTV searches TMDB's TV catalog by title — the show-title lookup
// Rename's Series-library code path uses, direct sibling of SearchMovies.
func (c *Client) SearchTV(ctx context.Context, query string) ([]Item, error) {
	q := url.Values{}
	q.Set("query", query)
	var resp listResponse
	if err := c.do(ctx, "/search/tv", q, &resp); err != nil {
		return nil, err
	}
	return normalizeAll(resp.Results, TV), nil
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

// MovieDetails is the subset of TMDB's /movie/{id} response SAK needs to
// turn a picked TMDB id into a precise, id-based indexer query — chiefly
// IMDBID, which /movie/{id} carries natively at the top level (no separate
// external_ids round-trip, unlike TV — see TVDetails). Runtime/Genres are
// cheap extras from the same response.
type MovieDetails struct {
	ID         int
	Title      string
	PosterPath string // "" if TMDB has none on file
	IMDBID     string // "" if TMDB has none on file
	Runtime    int    // minutes; 0 if TMDB reports null
	Genres     []string
}

type movieDetailsResponse struct {
	ID         int    `json:"id"`
	Title      string `json:"title"`
	PosterPath string `json:"poster_path"`
	IMDBID     string `json:"imdb_id"`
	Runtime    int    `json:"runtime"`
	Genres     []struct {
		Name string `json:"name"`
	} `json:"genres"`
}

// MovieDetails fetches TMDB's /movie/{id} — the details-by-id lookup that
// resolves a browsed/picked TMDB movie id into the structured ids
// (especially imdb_id) an id-based Prowlarr search wants. Direct sibling of
// ExternalIDs in request-build/response-decode style; a null runtime or
// absent imdb_id decodes to the zero value without erroring.
func (c *Client) MovieDetails(ctx context.Context, tmdbID int) (MovieDetails, error) {
	var resp movieDetailsResponse
	if err := c.do(ctx, fmt.Sprintf("/movie/%d", tmdbID), nil, &resp); err != nil {
		return MovieDetails{}, err
	}
	details := MovieDetails{
		ID:         resp.ID,
		Title:      resp.Title,
		PosterPath: resp.PosterPath,
		IMDBID:     resp.IMDBID,
		Runtime:    resp.Runtime,
		Genres:     make([]string, len(resp.Genres)),
	}
	for i, g := range resp.Genres {
		details.Genres[i] = g.Name
	}
	return details, nil
}

// TVDetails is the subset of TMDB's /tv/{id} response SAK needs. Note the
// deliberate asymmetry with MovieDetails: TMDB's /tv/{id} has NO top-level
// imdb_id field — a TV show's IMDB id lives only under /tv/{id}/external_ids
// (the same endpoint ExternalIDs already hits for tvdb_id). Rather than fake
// parity with a bound-to-be-empty IMDBID field, this type omits it; a caller
// that needs a TV show's IMDB id must fetch external_ids separately.
type TVDetails struct {
	ID         int
	Title      string
	PosterPath string // "" if TMDB has none on file
	Genres     []string
}

type tvDetailsResponse struct {
	ID         int    `json:"id"`
	Name       string `json:"name"`
	PosterPath string `json:"poster_path"`
	Genres     []struct {
		Name string `json:"name"`
	} `json:"genres"`
}

// TVDetails fetches TMDB's /tv/{id} — the details-by-id sibling of
// MovieDetails for the TV catalog (title normalized from TMDB's `name`
// field, matching normalize's convention). See TVDetails' type doc for why
// no IMDBID is returned here.
func (c *Client) TVDetails(ctx context.Context, tmdbID int) (TVDetails, error) {
	var resp tvDetailsResponse
	if err := c.do(ctx, fmt.Sprintf("/tv/%d", tmdbID), nil, &resp); err != nil {
		return TVDetails{}, err
	}
	details := TVDetails{
		ID:         resp.ID,
		Title:      resp.Name,
		PosterPath: resp.PosterPath,
		Genres:     make([]string, len(resp.Genres)),
	}
	for i, g := range resp.Genres {
		details.Genres[i] = g.Name
	}
	return details, nil
}

// SeasonEpisode is one episode as TMDB's season-details endpoint reports
// it — enough to record a canonical episode row even before any file for
// it exists on disk (see internal/library's Episode). Runtime (minutes; 0
// if TMDB reports null) is the per-episode duration Discover's auto-grab
// bitrate scorer needs for a single-episode Series grab — the whole-season
// list in one call, so the picked episode's runtime is a lookup, not an
// extra per-episode round-trip.
type SeasonEpisode struct {
	EpisodeNumber int    `json:"episodeNumber"`
	Name          string `json:"name"`
	AirDate       string `json:"airDate"`
	Runtime       int    `json:"runtime"`
}

type seasonEpisodeRaw struct {
	EpisodeNumber int    `json:"episode_number"`
	Name          string `json:"name"`
	AirDate       string `json:"air_date"`
	Runtime       int    `json:"runtime"`
}

type seasonDetailsResponse struct {
	Episodes []seasonEpisodeRaw `json:"episodes"`
}

// SeasonDetails returns every episode TMDB knows about for one season of a
// TV show — hits /tv/{id}/season/{season_number}. Unlike SearchMovies/
// ExternalIDs (already exercised live by Discover), this shape is modeled
// from TMDB's public documentation, not yet confirmed against a live call
// — flagged per this project's honesty-about-unverified-assumptions
// convention.
func (c *Client) SeasonDetails(ctx context.Context, tmdbTVID, seasonNumber int) ([]SeasonEpisode, error) {
	var resp seasonDetailsResponse
	path := fmt.Sprintf("/tv/%d/season/%d", tmdbTVID, seasonNumber)
	if err := c.do(ctx, path, nil, &resp); err != nil {
		return nil, err
	}
	out := make([]SeasonEpisode, len(resp.Episodes))
	for i, e := range resp.Episodes {
		out[i] = SeasonEpisode{EpisodeNumber: e.EpisodeNumber, Name: e.Name, AirDate: e.AirDate, Runtime: e.Runtime}
	}
	return out, nil
}

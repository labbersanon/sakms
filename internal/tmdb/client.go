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
	"time"

	"github.com/labbersanon/sakms/internal/httpx"
)

// MediaType distinguishes TMDB's movie and TV catalogs, which use
// different field names for the same concepts (title vs name, release_date
// vs first_air_date) — normalized away into one Item shape below.
type MediaType string

const (
	Movie MediaType = "movie"
	TV    MediaType = "tv"
)

// DefaultBaseURL is TMDB's single canonical public v3 REST endpoint. TMDB is a
// fixed public service (not self-hostable), so there is nothing for an operator
// to point it at — callers hardcode this instead of reading a user-supplied
// Connection.URL, mirroring the existing TPDBGraphQLURL precedent
// (internal/mode/mode.go). A var (not const) so tests can override it to point
// at an httptest fake, exactly as TPDBGraphQLURL documents.
var DefaultBaseURL = "https://api.themoviedb.org/3"

// Config parameterizes the client. BaseURL is normally DefaultBaseURL — a fixed
// public endpoint every caller now passes as a hardcoded constant, the same way
// this project already treats TPDB's GraphQL give-back endpoint.
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

// CollectionRef is the subset of TMDB's belongs_to_collection object SAK
// records when a movie belongs to a franchise collection. ID == 0 means the
// movie has no collection entry on TMDB (field absent or null in the response).
type CollectionRef struct {
	ID   int    // TMDB collection id
	Name string
}

// MovieDetails is the subset of TMDB's /movie/{id} response SAK needs to
// turn a picked TMDB id into a precise, id-based indexer query — chiefly
// IMDBID, which /movie/{id} carries natively at the top level (no separate
// external_ids round-trip, unlike TV — see TVDetails). Runtime/Genres are
// cheap extras from the same response. Collection carries the
// belongs_to_collection franchise data (zero value when absent).
type MovieDetails struct {
	ID          int
	Title       string
	PosterPath  string // "" if TMDB has none on file
	IMDBID      string // "" if TMDB has none on file
	Runtime     int    // minutes; 0 if TMDB reports null
	Overview    string
	ReleaseDate string // "YYYY-MM-DD" or "" if absent
	Genres      []string
	Collection  CollectionRef // zero (ID==0) when movie has no franchise collection
}

type movieDetailsResponse struct {
	ID          int    `json:"id"`
	Title       string `json:"title"`
	PosterPath  string `json:"poster_path"`
	IMDBID      string `json:"imdb_id"`
	Runtime     int    `json:"runtime"`
	Overview    string `json:"overview"`
	ReleaseDate string `json:"release_date"`
	Genres      []struct {
		Name string `json:"name"`
	} `json:"genres"`
	BelongsToCollection struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	} `json:"belongs_to_collection"`
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
		ID:          resp.ID,
		Title:       resp.Title,
		PosterPath:  resp.PosterPath,
		IMDBID:      resp.IMDBID,
		Runtime:     resp.Runtime,
		Overview:    resp.Overview,
		ReleaseDate: resp.ReleaseDate,
		Genres:      make([]string, len(resp.Genres)),
	}
	for i, g := range resp.Genres {
		details.Genres[i] = g.Name
	}
	if resp.BelongsToCollection.ID != 0 {
		details.Collection = CollectionRef{
			ID:   resp.BelongsToCollection.ID,
			Name: resp.BelongsToCollection.Name,
		}
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

type movieCreditsResponse struct {
	Cast []struct {
		Name string `json:"name"`
	} `json:"cast"`
}

// MovieCredits returns the top 10 cast member names from TMDB's
// /movie/{id}/credits. Soft-fail: callers treat errors as enrichment
// gaps, not blocking failures.
func (c *Client) MovieCredits(ctx context.Context, tmdbID int) ([]string, error) {
	var resp movieCreditsResponse
	if err := c.do(ctx, fmt.Sprintf("/movie/%d/credits", tmdbID), nil, &resp); err != nil {
		return nil, err
	}
	names := make([]string, 0, 10)
	for i, m := range resp.Cast {
		if i >= 10 {
			break
		}
		names = append(names, m.Name)
	}
	return names, nil
}

type tvAggregateCreditsResponse struct {
	Cast []struct {
		Name string `json:"name"`
	} `json:"cast"`
}

// TVAggregateCredits returns the top 10 cast member names from TMDB's
// /tv/{id}/aggregate_credits. Soft-fail same as MovieCredits.
func (c *Client) TVAggregateCredits(ctx context.Context, tmdbID int) ([]string, error) {
	var resp tvAggregateCreditsResponse
	if err := c.do(ctx, fmt.Sprintf("/tv/%d/aggregate_credits", tmdbID), nil, &resp); err != nil {
		return nil, err
	}
	names := make([]string, 0, 10)
	for i, m := range resp.Cast {
		if i >= 10 {
			break
		}
		names = append(names, m.Name)
	}
	return names, nil
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

// UpcomingMovies returns TMDB's /movie/upcoming list — movies with a future
// release date, for the given 1-based page. Direct sibling of Popular in
// request-build/response-decode shape.
func (c *Client) UpcomingMovies(ctx context.Context, page int) ([]Item, error) {
	var resp listResponse
	if err := c.do(ctx, "/movie/upcoming", pageQuery(page), &resp); err != nil {
		return nil, err
	}
	return normalizeAll(resp.Results, Movie), nil
}

// UpcomingTV returns TMDB's /tv/on_the_air list — shows with an episode
// airing within the next 7 days, for the given 1-based page. TMDB has no
// direct TV equivalent of /movie/upcoming (unreleased, future release
// date); on_the_air is the closer analog for a TV "upcoming" row, as
// opposed to /tv/airing_today, which is scoped to shows airing that exact
// calendar day rather than a rolling window.
func (c *Client) UpcomingTV(ctx context.Context, page int) ([]Item, error) {
	var resp listResponse
	if err := c.do(ctx, "/tv/on_the_air", pageQuery(page), &resp); err != nil {
		return nil, err
	}
	return normalizeAll(resp.Results, TV), nil
}

// Genre is one TMDB genre, as listed by /genre/movie/list or /genre/tv/list
// — the fixed catalog a "browse by genre" row's picker offers, and the id
// DiscoverMoviesByGenre/DiscoverTVByGenre filter on.
type Genre struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

type genreListResponse struct {
	Genres []Genre `json:"genres"`
}

// MovieGenres returns TMDB's full movie genre list (/genre/movie/list) —
// rarely-changing reference data, not paginated by TMDB.
func (c *Client) MovieGenres(ctx context.Context) ([]Genre, error) {
	var resp genreListResponse
	if err := c.do(ctx, "/genre/movie/list", nil, &resp); err != nil {
		return nil, err
	}
	return resp.Genres, nil
}

// TVGenres is MovieGenres' direct sibling for /genre/tv/list — TMDB keeps
// separate genre catalogs per media type (e.g. movie has "Science Fiction",
// TV has "Sci-Fi & Fantasy"), so this is not just a filtered view of
// MovieGenres.
func (c *Client) TVGenres(ctx context.Context) ([]Genre, error) {
	var resp genreListResponse
	if err := c.do(ctx, "/genre/tv/list", nil, &resp); err != nil {
		return nil, err
	}
	return resp.Genres, nil
}

// discoverQuery builds pageQuery's params plus one extra TMDB /discover
// filter (with_genres, with_companies, or with_networks) set to filterValue
// — the shared query-building shape behind DiscoverMoviesByGenre/
// DiscoverTVByGenre/DiscoverMoviesByStudio/DiscoverTVByNetwork.
func discoverQuery(page int, filterKey string, filterValue int) url.Values {
	q := pageQuery(page)
	if q == nil {
		q = url.Values{}
	}
	q.Set(filterKey, strconv.Itoa(filterValue))
	return q
}

// DiscoverMoviesByGenre returns TMDB movies matching genreID (one of
// MovieGenres' ids), for the given 1-based page — the "browse by genre"
// Discover row's data source.
func (c *Client) DiscoverMoviesByGenre(ctx context.Context, genreID int, page int) ([]Item, error) {
	var resp listResponse
	if err := c.do(ctx, "/discover/movie", discoverQuery(page, "with_genres", genreID), &resp); err != nil {
		return nil, err
	}
	return normalizeAll(resp.Results, Movie), nil
}

// DiscoverTVByGenre is DiscoverMoviesByGenre's direct sibling for the TV
// catalog, filtering on one of TVGenres' ids.
func (c *Client) DiscoverTVByGenre(ctx context.Context, genreID int, page int) ([]Item, error) {
	var resp listResponse
	if err := c.do(ctx, "/discover/tv", discoverQuery(page, "with_genres", genreID), &resp); err != nil {
		return nil, err
	}
	return normalizeAll(resp.Results, TV), nil
}

// Studio is a well-known movie production company, keyed by TMDB's company
// id — DiscoverMoviesByStudio's with_companies filter operates on this id.
// JSON-tagged (unlike a bare internal Go type) because KnownStudios is
// served directly to the frontend as a slider-editor reference list.
type Studio struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

// KnownStudios is a starting seed list of major movie studios for a "browse
// by studio" Discover row, seerr-style — not exhaustive, extend as needed.
// IDs are TMDB's own public company ids, the same catalog SearchMovies/
// Trending already read from (visible on themoviedb.org's own company
// pages, e.g. themoviedb.org/company/420-marvel-studios).
var KnownStudios = []Studio{
	{ID: 420, Name: "Marvel Studios"},
	{ID: 2, Name: "Walt Disney Pictures"},
	{ID: 3, Name: "Pixar"},
	{ID: 9993, Name: "DC Entertainment"},
	{ID: 1, Name: "Lucasfilm Ltd."},
	{ID: 174, Name: "Warner Bros. Pictures"},
	{ID: 33, Name: "Universal Pictures"},
	{ID: 4, Name: "Paramount Pictures"},
	{ID: 34, Name: "Sony Pictures"},
	{ID: 521, Name: "DreamWorks Animation"},
	{ID: 923, Name: "Legendary Pictures"},
	{ID: 3172, Name: "Blumhouse Productions"},
}

// DiscoverMoviesByStudio returns TMDB movies produced by companyID (one of
// KnownStudios' ids, or any other TMDB company id an admin-configured
// slider names), for the given 1-based page.
func (c *Client) DiscoverMoviesByStudio(ctx context.Context, companyID int, page int) ([]Item, error) {
	var resp listResponse
	if err := c.do(ctx, "/discover/movie", discoverQuery(page, "with_companies", companyID), &resp); err != nil {
		return nil, err
	}
	return normalizeAll(resp.Results, Movie), nil
}

// Network is a well-known TV network or streaming service, keyed by TMDB's
// network id — DiscoverTVByNetwork's with_networks filter operates on this
// id. Direct sibling of Studio for the TV catalog; TMDB tracks companies
// and networks as separate id spaces, so a network id is never a company id
// or vice versa. JSON-tagged for the same reason as Studio.
type Network struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

// KnownNetworks is a starting seed list of major TV networks/streaming
// services for a "browse by network" Discover row — not exhaustive, extend
// as needed. Same public-id sourcing convention as KnownStudios.
var KnownNetworks = []Network{
	{ID: 213, Name: "Netflix"},
	{ID: 1024, Name: "Amazon"},
	{ID: 2552, Name: "Apple TV+"},
	{ID: 2739, Name: "Disney+"},
	{ID: 49, Name: "HBO"},
	{ID: 67, Name: "Showtime"},
	{ID: 318, Name: "Starz"},
	{ID: 88, Name: "FX"},
	{ID: 6, Name: "NBC"},
	{ID: 2, Name: "ABC"},
	{ID: 16, Name: "CBS"},
	{ID: 19, Name: "FOX"},
}

// DiscoverTVByNetwork returns TMDB shows on networkID (one of KnownNetworks'
// ids, or any other TMDB network id an admin-configured slider names), for
// the given 1-based page.
func (c *Client) DiscoverTVByNetwork(ctx context.Context, networkID int, page int) ([]Item, error) {
	var resp listResponse
	if err := c.do(ctx, "/discover/tv", discoverQuery(page, "with_networks", networkID), &resp); err != nil {
		return nil, err
	}
	return normalizeAll(resp.Results, TV), nil
}

// Keyword is one TMDB keyword, as returned by /search/keyword — unlike
// Genre/Studio/Network, TMDB has no fixed enumerable keyword list (there are
// hundreds of thousands), so a keyword-filtered slider's FilterValue is
// resolved from free-typed admin text via SearchKeywords rather than picked
// from a seed list.
type Keyword struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

type keywordListResponse struct {
	Results []Keyword `json:"results"`
}

// SearchKeywords looks up TMDB keywords by free-typed text (/search/keyword)
// — the admin slider editor's way of turning "heist" into the numeric
// keyword id DiscoverMoviesByKeyword/DiscoverTVByKeyword actually filter on.
func (c *Client) SearchKeywords(ctx context.Context, query string) ([]Keyword, error) {
	q := url.Values{}
	q.Set("query", query)
	var resp keywordListResponse
	if err := c.do(ctx, "/search/keyword", q, &resp); err != nil {
		return nil, err
	}
	return resp.Results, nil
}

// DiscoverMoviesByKeyword returns TMDB movies tagged with keywordID (one of
// SearchKeywords' ids), for the given 1-based page. Direct sibling of
// DiscoverMoviesByGenre/DiscoverMoviesByStudio, filtering on with_keywords
// instead.
func (c *Client) DiscoverMoviesByKeyword(ctx context.Context, keywordID int, page int) ([]Item, error) {
	var resp listResponse
	if err := c.do(ctx, "/discover/movie", discoverQuery(page, "with_keywords", keywordID), &resp); err != nil {
		return nil, err
	}
	return normalizeAll(resp.Results, Movie), nil
}

// DiscoverTVByKeyword is DiscoverMoviesByKeyword's direct sibling for the TV
// catalog.
func (c *Client) DiscoverTVByKeyword(ctx context.Context, keywordID int, page int) ([]Item, error) {
	var resp listResponse
	if err := c.do(ctx, "/discover/tv", discoverQuery(page, "with_keywords", keywordID), &resp); err != nil {
		return nil, err
	}
	return normalizeAll(resp.Results, TV), nil
}

type videoRaw struct {
	Key      string `json:"key"`
	Site     string `json:"site"`
	Type     string `json:"type"`
	Official bool   `json:"official"`
}

type videosResponse struct {
	Results []videoRaw `json:"results"`
}

// youtubeURL builds a watchable YouTube URL from a TMDB video's `key` field
// (YouTube's own video id) — the only site TrailerURL matches on, since it's
// the one TMDB itself links to for a browser-viewable trailer.
func youtubeURL(key string) string {
	return "https://www.youtube.com/watch?v=" + key
}

// TrailerURL returns a watchable YouTube trailer URL for mt/tmdbID (hits
// /movie|tv/{id}/videos), or "" if TMDB has none on file — not an error, the
// Discover detail popup simply omits the "Watch Trailer" link in that case.
// Preference order: an official YouTube "Trailer" first, then any YouTube
// "Trailer", then any YouTube video at all (e.g. a Teaser) as a last resort.
// UNVERIFIED ASSUMPTION (per this project's honesty-about-unverified-
// assumptions convention): this shape is modeled from TMDB's public API
// documentation only, not yet confirmed against a live call.
func (c *Client) TrailerURL(ctx context.Context, mt MediaType, tmdbID int) (string, error) {
	var resp videosResponse
	if err := c.do(ctx, fmt.Sprintf("/%s/%d/videos", mt, tmdbID), nil, &resp); err != nil {
		return "", err
	}
	var fallbackTrailer, fallbackAny string
	for _, v := range resp.Results {
		if v.Site != "YouTube" {
			continue
		}
		if fallbackAny == "" {
			fallbackAny = youtubeURL(v.Key)
		}
		if v.Type != "Trailer" {
			continue
		}
		if v.Official {
			return youtubeURL(v.Key), nil
		}
		if fallbackTrailer == "" {
			fallbackTrailer = youtubeURL(v.Key)
		}
	}
	if fallbackTrailer != "" {
		return fallbackTrailer, nil
	}
	return fallbackAny, nil
}

type releaseDateEntry struct {
	Type        int    `json:"type"`
	ReleaseDate string `json:"release_date"`
}

type releaseDatesCountry struct {
	ISO31661     string             `json:"iso_3166_1"`
	ReleaseDates []releaseDateEntry `json:"release_dates"`
}

type releaseDatesResponse struct {
	Results []releaseDatesCountry `json:"results"`
}

// TMDB's release_dates "type" enum (documented: 1 Premiere, 2 Theatrical
// limited, 3 Theatrical, 4 Digital, 5 Physical, 6 TV) — HasUSRelease only
// counts type 4/5 as "actually acquirable," not a theatrical-only release.
const (
	releaseTypeDigital  = 4
	releaseTypePhysical = 5
)

// HasUSRelease reports whether TMDB's /movie/{id}/release_dates lists a US
// digital or physical release dated today or earlier — i.e. whether this
// movie is actually acquirable yet, as opposed to theatrical-only or still
// upcoming. Movies only: TMDB's TV catalog has no equivalent release_dates
// concept. A movie with no US entry at all, or only earlier-stage entries
// (premiere/theatrical), returns false — the same title as "not yet
// released" for this check's purpose. UNVERIFIED ASSUMPTION (per this
// project's honesty-about-unverified-assumptions convention): modeled from
// TMDB's public API documentation only, not yet confirmed against a live
// call.
func (c *Client) HasUSRelease(ctx context.Context, tmdbID int) (bool, error) {
	var resp releaseDatesResponse
	if err := c.do(ctx, fmt.Sprintf("/movie/%d/release_dates", tmdbID), nil, &resp); err != nil {
		return false, err
	}
	now := time.Now()
	for _, country := range resp.Results {
		if country.ISO31661 != "US" {
			continue
		}
		for _, rd := range country.ReleaseDates {
			if rd.Type != releaseTypeDigital && rd.Type != releaseTypePhysical {
				continue
			}
			t, err := time.Parse(time.RFC3339, rd.ReleaseDate)
			if err != nil {
				continue
			}
			if !t.After(now) {
				return true, nil
			}
		}
	}
	return false, nil
}

// findResponse is the envelope for TMDB's /find/{external_id} endpoint, which
// cross-references an external id (TVDB, IMDB, etc.) to a TMDB id.
//
// UNVERIFIED ASSUMPTION: the field names (movie_results/tv_results, id within
// each) are modeled from TMDB's public API documentation — not confirmed
// against a live call. A mismatch returns 0 (not found) rather than an error.
type findResponse struct {
	MovieResults []struct {
		ID int `json:"id"`
	} `json:"movie_results"`
	TVResults []struct {
		ID int `json:"id"`
	} `json:"tv_results"`
}

// FindMovieByTVDBID looks up a TMDB movie id by a TheTVDB movie id via
// TMDB's /find endpoint with external_source=tvdb_id. Returns 0 if the
// cross-reference is absent from TMDB's database (common for movies, since
// TVDB added movies more recently than TV shows). The caller should fall back
// to a name-based search when 0 is returned.
func (c *Client) FindMovieByTVDBID(ctx context.Context, tvdbID int) (tmdbID int, err error) {
	q := url.Values{}
	q.Set("external_source", "tvdb_id")
	var resp findResponse
	if err := c.do(ctx, fmt.Sprintf("/find/%d", tvdbID), q, &resp); err != nil {
		return 0, err
	}
	if len(resp.MovieResults) > 0 {
		return resp.MovieResults[0].ID, nil
	}
	return 0, nil
}

// FindTVByTVDBID looks up a TMDB TV show id by a TheTVDB series id via
// TMDB's /find endpoint with external_source=tvdb_id. Returns 0 if the
// cross-reference is absent. TVDB is historically the canonical database for
// TV, so TMDB's cross-reference coverage here is much better than for movies.
func (c *Client) FindTVByTVDBID(ctx context.Context, tvdbID int) (tmdbID int, err error) {
	q := url.Values{}
	q.Set("external_source", "tvdb_id")
	var resp findResponse
	if err := c.do(ctx, fmt.Sprintf("/find/%d", tvdbID), q, &resp); err != nil {
		return 0, err
	}
	if len(resp.TVResults) > 0 {
		return resp.TVResults[0].ID, nil
	}
	return 0, nil
}

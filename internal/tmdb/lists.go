package tmdb

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// Claude 2026-09-28: TMDB account watchlist + v3 public list fetch for ingest.
// Reason: list ingest needs live titles, not Discover sliders; v3 lists are
//   movie-oriented and take api_key (+ session_id for account watchlist).
// Troubleshooting: empty watchlist → no session; 404 list → bad numeric id.
// Review if: v4 Bearer lists are added, or account lists need a write API.

const listIngestMaxPages = 10

type accountResponse struct {
	ID int `json:"id"`
}

type pagedListResponse struct {
	Page       int         `json:"page"`
	TotalPages int         `json:"total_pages"`
	Results    []rawResult `json:"results"`
}

type listDetailsResponse struct {
	Items []rawResult `json:"items"`
}

// Account returns the TMDB account id for sessionID (GET /account).
// 0 means the session did not resolve — not an error we invent.
func (c *Client) Account(ctx context.Context, sessionID string) (int, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return 0, nil
	}
	q := url.Values{}
	q.Set("session_id", sessionID)
	var resp accountResponse
	if err := c.do(ctx, "/account", q, &resp); err != nil {
		return 0, err
	}
	return resp.ID, nil
}

// WatchlistMovies pages GET /account/{id}/watchlist/movies.
func (c *Client) WatchlistMovies(ctx context.Context, accountID int, sessionID string) ([]Item, error) {
	return c.accountWatchlist(ctx, accountID, sessionID, "movies", Movie)
}

// WatchlistTV pages GET /account/{id}/watchlist/tv.
func (c *Client) WatchlistTV(ctx context.Context, accountID int, sessionID string) ([]Item, error) {
	return c.accountWatchlist(ctx, accountID, sessionID, "tv", TV)
}

func (c *Client) accountWatchlist(ctx context.Context, accountID int, sessionID, kind string, mt MediaType) ([]Item, error) {
	sessionID = strings.TrimSpace(sessionID)
	if accountID <= 0 || sessionID == "" {
		return nil, nil
	}
	var out []Item
	for page := 1; page <= listIngestMaxPages; page++ {
		q := url.Values{}
		q.Set("session_id", sessionID)
		if page > 1 {
			q.Set("page", strconv.Itoa(page))
		}
		var resp pagedListResponse
		path := fmt.Sprintf("/account/%d/watchlist/%s", accountID, kind)
		if err := c.do(ctx, path, q, &resp); err != nil {
			return nil, err
		}
		out = append(out, normalizeAll(resp.Results, mt)...)
		if resp.TotalPages <= page || len(resp.Results) == 0 {
			break
		}
	}
	return out, nil
}

// ListItems returns GET /list/{id} items. v3 lists are movie-oriented;
// media_type on an item still wins when TMDB sends tv.
func (c *Client) ListItems(ctx context.Context, listID string) ([]Item, error) {
	listID = strings.TrimSpace(listID)
	if listID == "" {
		return nil, nil
	}
	var resp listDetailsResponse
	if err := c.do(ctx, "/list/"+listID, nil, &resp); err != nil {
		return nil, err
	}
	return normalizeAll(resp.Items, Movie), nil
}

// FindByIMDBID resolves an IMDb tt… id to TMDB movie and/or TV ids via /find.
// Either side is 0 when TMDB has no cross-reference for that media type.
func (c *Client) FindByIMDBID(ctx context.Context, imdbID string) (movieID, tvID int, err error) {
	imdbID = strings.TrimSpace(imdbID)
	if imdbID == "" {
		return 0, 0, nil
	}
	q := url.Values{}
	q.Set("external_source", "imdb_id")
	var resp findResponse
	if err := c.do(ctx, fmt.Sprintf("/find/%s", imdbID), q, &resp); err != nil {
		return 0, 0, err
	}
	if len(resp.MovieResults) > 0 {
		movieID = resp.MovieResults[0].ID
	}
	if len(resp.TVResults) > 0 {
		tvID = resp.TVResults[0].ID
	}
	return movieID, tvID, nil
}

// FindTVByIMDBID looks up a TMDB TV id by IMDb id (tt…). Returns 0 when
// TMDB has no TV cross-reference.
func (c *Client) FindTVByIMDBID(ctx context.Context, imdbID string) (int, error) {
	_, tvID, err := c.FindByIMDBID(ctx, imdbID)
	return tvID, err
}

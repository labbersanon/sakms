// Package omdb is a client for the OMDb API — the lookup that turns an IMDb
// id into an IMDb score for the Discover/Library detail popup when Trakt did
// not already supply one. OMDb is a fixed public service (not self-hostable);
// SAK stores its API key as a singleton "omdb" connections.Store entry,
// matching TMDB.
//
// UNVERIFIED ASSUMPTION: the response shape (imdbRating/imdbVotes,
// Response/Error as strings) is modeled from omdbapi.com's published
// parameter docs, not confirmed against a live key in this repo. A missing
// title or "N/A" field degrades to an empty score rather than an error.
package omdb

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/labbersanon/sakms/internal/httpx"
)

// DefaultBaseURL is OMDb's public endpoint. A var so tests can point it at
// an httptest server, matching tmdb.DefaultBaseURL.
var DefaultBaseURL = "https://www.omdbapi.com"

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

// Title is the subset of one OMDb by-id lookup the detail popup needs.
// Scores are 0 when OMDb omitted them or reported "N/A".
type Title struct {
	IMDbRating float64
	IMDbVotes  int
	// Claude 2026-08-15: TomatoMeter / TomatoUserMeter no longer filled.
	// Reason: operator does not want Rotten Tomatoes on the detail row.
	// Review if: RT is added back as an explicit opt-in source.
	// TomatoMeter     int // critics Tomatometer, 0–100
	// TomatoUserMeter int // audience Popcornmeter, 0–100
}

type rawTitle struct {
	IMDbRating string `json:"imdbRating"`
	IMDbVotes  string `json:"imdbVotes"`
	// TomatoMeter     string `json:"tomatoMeter"`
	// TomatoUserMeter string `json:"tomatoUserMeter"`
	// Ratings         []struct {
	// 	Source string `json:"Source"`
	// 	Value  string `json:"Value"`
	// } `json:"Ratings"`
	Response string `json:"Response"`
	Error    string `json:"Error"`
}

// ByIMDBID fetches GET /?i=tt…. IMDb score only — tomatoes=true is not
// sent because the detail row does not show Rotten Tomatoes.
func (c *Client) ByIMDBID(ctx context.Context, imdbID string) (Title, error) {
	if c.cfg.APIKey == "" {
		return Title{}, fmt.Errorf("omdb: api key is required")
	}
	imdbID = strings.TrimSpace(imdbID)
	if imdbID == "" {
		return Title{}, fmt.Errorf("omdb: imdb id is required")
	}
	// Claude 2026-08-15: dropped tomatoes=true.
	// Reason: operator does not want RT; the extra tomatoMeter fields
	// were unused after the IMDb-only row.
	// raw, err := c.get(ctx, url.Values{"i": {imdbID}, "tomatoes": {"true"}})
	raw, err := c.get(ctx, url.Values{"i": {imdbID}})
	if err != nil {
		return Title{}, err
	}
	out := Title{
		IMDbRating: parseOMDbFloat(raw.IMDbRating),
		IMDbVotes:  int(parseOMDbFloat(raw.IMDbVotes)),
		// TomatoMeter:     int(parseOMDbFloat(raw.TomatoMeter)),
		// TomatoUserMeter: int(parseOMDbFloat(raw.TomatoUserMeter)),
	}
	// if out.TomatoMeter == 0 {
	// 	out.TomatoMeter = int(parseOMDbFloat(ratingValue(raw.Ratings, "Rotten Tomatoes")))
	// }
	return out, nil
}

// Ping confirms the API key is accepted. OMDb has no dedicated health
// endpoint: an empty-id call with a valid key returns Response=False and
// an "Incorrect IMDb ID" (or similar) error, while a bad key returns
// "Invalid API key!". That split is the check.
func (c *Client) Ping(ctx context.Context) error {
	if c.cfg.APIKey == "" {
		return fmt.Errorf("omdb: api key is required")
	}
	_, err := c.get(ctx, url.Values{})
	return err
}

func (c *Client) get(ctx context.Context, extra url.Values) (rawTitle, error) {
	base := c.cfg.BaseURL
	if base == "" {
		base = DefaultBaseURL
	}
	q := url.Values{"apikey": {c.cfg.APIKey}}
	for k, vs := range extra {
		q[k] = vs
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/?"+q.Encode(), nil)
	if err != nil {
		return rawTitle{}, fmt.Errorf("building request: %w", err)
	}
	var raw rawTitle
	if err := httpx.DoJSON(c.http, req, httpx.MaxResponseBodySize, &raw); err != nil {
		return rawTitle{}, err
	}
	if strings.EqualFold(raw.Response, "False") {
		msg := strings.TrimSpace(raw.Error)
		if msg == "" {
			msg = "request failed"
		}
		if strings.Contains(strings.ToLower(msg), "invalid api key") {
			return rawTitle{}, fmt.Errorf("omdb: %s", msg)
		}
		// A valid key with no/unknown id still comes back Response=False.
		// Ping treats that as success; ByIMDBID treats a real miss as empty.
		if extra.Get("i") == "" {
			return raw, nil
		}
		return rawTitle{}, fmt.Errorf("omdb: %s", msg)
	}
	return raw, nil
}

// Claude 2026-08-15: ratingValue was only used to fill RT from Ratings[].
// Reason: operator does not want Rotten Tomatoes.
// func ratingValue(ratings []struct {
// 	Source string `json:"Source"`
// 	Value  string `json:"Value"`
// }, source string) string {
// 	for _, r := range ratings {
// 		if strings.EqualFold(r.Source, source) {
// 			return r.Value
// 		}
// 	}
// 	return ""
// }

func parseOMDbFloat(s string) float64 {
	s = strings.TrimSpace(s)
	if s == "" || strings.EqualFold(s, "N/A") {
		return 0
	}
	s = strings.ReplaceAll(s, ",", "")
	s = strings.TrimSuffix(s, "%")
	if i := strings.Index(s, "/"); i >= 0 {
		s = s[:i]
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return f
}

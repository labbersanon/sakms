package trakt

import (
	"context"
	"fmt"
	"net/http"

	"github.com/curtiswtaylorjr/sakms/internal/httpx"
)

// Config parameterizes the client. BaseURL is normally https://api.trakt.tv,
// stored explicitly rather than hardcoded, same convention as tmdb.Config.
// ClientID/ClientSecret are the operator-registered Trakt application
// (Settings-entered, never hardcoded) — required on every request, even
// unauthenticated ones (ClientID as the trakt-api-key header) per Trakt's
// API convention of identifying the calling app on every call.
type Config struct {
	BaseURL      string
	ClientID     string
	ClientSecret string
}

type Client struct {
	cfg  Config
	http *http.Client
}

func New(cfg Config, httpClient *http.Client) *Client {
	return &Client{cfg: cfg, http: httpClient}
}

// traktAPIVersion is Trakt's required trakt-api-version header value —
// fixed at 2, the only version Trakt's API has ever had since introducing
// the header (per docs.trakt.tv), not expected to change.
const traktAPIVersion = "2"

// newAuthedRequest builds a GET request carrying Trakt's three required
// headers for a user-scoped call: Content-Type, trakt-api-version, and
// trakt-api-key (the app's client_id — Trakt requires this on every
// request, including ones already carrying a user's bearer token).
func (c *Client) newAuthedRequest(ctx context.Context, path, accessToken string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.cfg.BaseURL+path, nil)
	if err != nil {
		return nil, fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("trakt-api-version", traktAPIVersion)
	req.Header.Set("trakt-api-key", c.cfg.ClientID)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	return req, nil
}

// WatchlistItem is one normalized Trakt watchlist entry — enough shape to
// map into sakms's existing Discover card (title/year/TMDB id), regardless
// of whether the underlying Trakt entry was a movie or a show.
type WatchlistItem struct {
	Type   string // "movie" or "show"
	Title  string
	Year   int
	TMDBID int
}

// rawIDs covers Trakt's ids block on both movie and show entries — same
// field set either way (trakt/slug/imdb/tmdb; shows add tvdb, unused here).
type rawIDs struct {
	TMDB int `json:"tmdb"`
}

type rawMovieOrShow struct {
	Title string `json:"title"`
	Year  int    `json:"year"`
	IDs   rawIDs `json:"ids"`
}

// rawWatchlistEntry mirrors one element of GET /sync/watchlist. Trakt
// returns a mixed-type list (Type distinguishes each entry); Movie/Show are
// mutually exclusive depending on Type, mirroring tmdb.rawResult's
// Title/Name split for the same "one endpoint, two shapes" reason. Modeled
// from docs.trakt.tv + third-party client examples (this package's doc
// comment), not confirmed against a live account.
type rawWatchlistEntry struct {
	Type  string          `json:"type"`
	Movie *rawMovieOrShow `json:"movie"`
	Show  *rawMovieOrShow `json:"show"`
}

// Watchlist fetches the linked account's full watchlist via GET
// /sync/watchlist. Entries whose type isn't "movie"/"show" (Trakt also
// allows watchlisting seasons/episodes) or whose TMDB id is missing/zero
// (observed in the wild for occasional Trakt entries with no TMDB match,
// e.g. github.com/Radarr/Radarr#11519) are silently skipped — every
// consumer of this list (Discover's watchlist row) keys on TMDB id, so an
// entry without one has nothing useful to map into a card. No pagination is
// applied; sync/watchlist returns the full list in one call.
//
// Callers should generally prefer Session.Watchlist, which refreshes an
// expiring access token first — this method makes no expiry check of its
// own and will simply fail with whatever error Trakt returns for an
// expired token.
func (c *Client) Watchlist(ctx context.Context, accessToken string) ([]WatchlistItem, error) {
	req, err := c.newAuthedRequest(ctx, "/sync/watchlist", accessToken)
	if err != nil {
		return nil, err
	}
	var raw []rawWatchlistEntry
	if err := httpx.DoJSON(c.http, req, httpx.MaxResponseBodySize, &raw); err != nil {
		return nil, err
	}

	out := make([]WatchlistItem, 0, len(raw))
	for _, e := range raw {
		var mos *rawMovieOrShow
		switch e.Type {
		case "movie":
			mos = e.Movie
		case "show":
			mos = e.Show
		default:
			continue
		}
		if mos == nil || mos.IDs.TMDB == 0 {
			continue
		}
		out = append(out, WatchlistItem{Type: e.Type, Title: mos.Title, Year: mos.Year, TMDBID: mos.IDs.TMDB})
	}
	return out, nil
}

// Ping confirms client_id is genuinely valid by making one minimal
// unauthenticated call to a public, non-OAuth endpoint (/movies/trending) —
// deliberately NOT /sync/watchlist or any other user-scoped route, since
// those require a bearer token and 401 unconditionally regardless of
// whether client_id is valid, which would make them useless as a "does this
// client_id work" check. Trakt's public browse endpoints only require the
// trakt-api-key header: a valid client_id gets 200, an invalid/missing one
// gets 401 — the same public-vs-authenticated split TMDB/TPDB's own
// Ping-equivalents rely on. This only validates client_id; it says nothing
// about client_secret (never used until a token exchange) or whether an
// account has actually been linked — see the doc comment above for why full
// validation requires completing the device flow.
func (c *Client) Ping(ctx context.Context) error {
	if c.cfg.ClientID == "" {
		return fmt.Errorf("trakt: client_id is required")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.cfg.BaseURL+"/movies/trending?limit=1", nil)
	if err != nil {
		return fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("trakt-api-version", traktAPIVersion)
	req.Header.Set("trakt-api-key", c.cfg.ClientID)

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("request to %s failed: %w", req.URL.Host, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("trakt: client_id rejected (401 unauthorized)")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("trakt: unexpected status %d checking client_id", resp.StatusCode)
	}
	return nil
}

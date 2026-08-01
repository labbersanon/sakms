// Package plex is a minimal client for a Plex Media Server instance. SAK
// notifies Plex of changed paths after a file op so Plex's library index
// stays accurate, the same job internal/jellyfin does for Jellyfin — but
// Plex's protocol is genuinely different, not a Jellyfin reskin:
//
//   - Auth is the X-Plex-Token header (this client never uses the
//     ?X-Plex-Token= query-param form, even though Plex accepts both — a
//     token in a URL ends up in server access logs and proxy logs).
//   - Plex responds with XML by default; this client always sends
//     "Accept: application/json" to get JSON instead, so no XML parsing is
//     needed anywhere in this package.
//   - There is no single global "rescan" or path-targeted media-update
//     endpoint like Jellyfin's POST /Library/Media/Updated. Plex scopes
//     scans to a "library section" (one Plex library, e.g. "Movies"), so a
//     path-targeted refresh first requires knowing which section owns that
//     path (GET /library/sections), then hitting that section's partial-scan
//     endpoint with the path (GET /library/sections/{key}/refresh?path=...).
//
// HONESTY NOTE (house "unverified assumptions" convention, see
// internal/jellyfin's parallel note): Plex Media Server has no current
// official reference for its JSON response shapes — developer.plex.tv's
// public docs describe endpoints but not field-level JSON schemas. The
// request/response shapes below (the /identity and /library/sections JSON
// envelopes, the refresh-by-path query parameter) are modeled from
// community reverse-engineered documentation (plexopedia.com's Plex API
// pages, the Arcanemagus/plex-api GitHub wiki) and prior general knowledge
// of the Plex Media Server API, NOT confirmed against a live Plex instance.
// Plex is widely known to be under-documented officially; community docs
// are the norm here, not a fallback. RefreshPath in particular has NO
// documented response body shape at all (success may be an empty body, a
// small XML/JSON status blob, or something else) — see RefreshPath's own
// comment for how that uncertainty is handled without risking a false
// failure.
package plex

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/labbersanon/sakms/internal/httpx"
)

// Config points at a Plex Media Server instance.
type Config struct {
	URL   string // base, e.g. https://plex.example.com:32400 (a trailing slash is tolerated)
	Token string // X-Plex-Token
}

type Client struct {
	cfg  Config
	http *http.Client
}

func New(cfg Config, httpClient *http.Client) *Client {
	return &Client{cfg: cfg, http: httpClient}
}

// newRequest builds a request against {base}+path, joining cfg.URL with a
// trailing slash trimmed first, setting req.URL.RawQuery to query.Encode()
// (this REPLACES any query string path itself carries, it does not merge
// with it — no caller in this package passes a query-bearing path today, so
// this has never mattered in practice, but it would silently drop an
// existing query if one were ever added), and setting the headers every
// Plex endpoint here needs: the token via header (never the ?X-Plex-Token=
// query form — see package doc) and Accept: application/json so Plex
// returns JSON instead of its XML default.
func (c *Client) newRequest(ctx context.Context, method, path string, query url.Values) (*http.Request, error) {
	u := strings.TrimSuffix(c.cfg.URL, "/") + path
	req, err := http.NewRequestWithContext(ctx, method, u, nil)
	if err != nil {
		return nil, fmt.Errorf("building request: %w", err)
	}
	if len(query) > 0 {
		req.URL.RawQuery = query.Encode()
	}
	req.Header.Set("X-Plex-Token", c.cfg.Token)
	req.Header.Set("Accept", "application/json")
	return req, nil
}

// identityResponse is GET /identity's JSON envelope.
type identityResponse struct {
	MediaContainer struct {
		MachineIdentifier string `json:"machineIdentifier"`
		Version           string `json:"version"`
	} `json:"MediaContainer"`
}

// Ping validates that base URL points at a reachable Plex Media Server via
// GET {base}/identity. Unlike internal/jellyfin's Ping (which deliberately
// uses an auth-gated endpoint so a bad key surfaces as an error), Plex's
// /identity is documented as NOT requiring a token at all — it is Plex's
// "unauthenticated-friendly" server-discovery endpoint. That means Ping
// confirms the URL reaches a real Plex Media Server, but a wrong Token
// value will NOT make Ping fail; only Sections/RefreshPath (which hit
// token-gated endpoints) can detect a bad token. This asymmetry is
// intentional, not an oversight — Plex has no equivalent to Jellyfin's
// auth-gated /System/Info to check a token cheaply and side-effect-free.
func (c *Client) Ping(ctx context.Context) error {
	req, err := c.newRequest(ctx, http.MethodGet, "/identity", nil)
	if err != nil {
		return err
	}

	var out identityResponse
	if err := httpx.DoJSON(c.http, req, httpx.MaxResponseBodySize, &out); err != nil {
		return err
	}
	if out.MediaContainer.MachineIdentifier == "" {
		return fmt.Errorf("plex: /identity response missing machineIdentifier")
	}
	return nil
}

// Location is one filesystem path a library Section indexes. Plex library
// sections can be backed by more than one on-disk folder (e.g. a "Movies"
// section spanning two drives), hence a slice on Section rather than a
// single path. Plex's Location JSON also carries a numeric "id" attribute,
// deliberately not modeled here: nothing in this package (or its only
// planned caller, RefreshPath's section resolution) reads it, and the
// exact JSON type community docs give it (string vs. number) is
// inconsistent — not worth guessing wrong for a field with no consumer.
type Location struct {
	Path string
}

type locationJSON struct {
	Path string `json:"path"`
}

// Section is one Plex library section (what Plex's own UI calls a
// "library", e.g. "Movies" or "TV Shows"). Key is the path segment used to
// address it in section-scoped endpoints (GET/refresh under
// /library/sections/{Key}/...).
type Section struct {
	Key       string
	Title     string
	Type      string
	Locations []Location
}

type sectionJSON struct {
	Key      string         `json:"key"`
	Title    string         `json:"title"`
	Type     string         `json:"type"`
	Location []locationJSON `json:"Location"`
}

type sectionsResponse struct {
	MediaContainer struct {
		Directory []sectionJSON `json:"Directory"`
	} `json:"MediaContainer"`
}

// Sections lists every library section configured on the server via
// GET {base}/library/sections.
func (c *Client) Sections(ctx context.Context) ([]Section, error) {
	req, err := c.newRequest(ctx, http.MethodGet, "/library/sections", nil)
	if err != nil {
		return nil, err
	}

	var out sectionsResponse
	if err := httpx.DoJSON(c.http, req, httpx.MaxResponseBodySize, &out); err != nil {
		return nil, err
	}

	sections := make([]Section, len(out.MediaContainer.Directory))
	for i, d := range out.MediaContainer.Directory {
		locs := make([]Location, len(d.Location))
		for j, l := range d.Location {
			locs[j] = Location{Path: l.Path}
		}
		sections[i] = Section{Key: d.Key, Title: d.Title, Type: d.Type, Locations: locs}
	}
	return sections, nil
}

// RefreshPath tells Plex to run a partial (path-scoped) scan for path,
// after first resolving which library section owns it. Section resolution
// fetches the current section list (GET /library/sections) and picks the
// section whose Location.Path is the longest matching prefix of path — the
// same "longest matching root wins" rule SAK uses elsewhere for root-path
// resolution, and necessary here because a Location deeper in the
// filesystem (e.g. a section rooted at /media/movies/kids) must win over a
// shallower one (e.g. /media/movies) when both are prefixes of path.
//
// Returns an error, with no partial-scan request sent, if no configured
// section's Location contains path — SAK has nothing to poke in that case.
func (c *Client) RefreshPath(ctx context.Context, path string) error {
	sections, err := c.Sections(ctx)
	if err != nil {
		return fmt.Errorf("plex: resolving library section for path %q: %w", path, err)
	}

	var bestKey string
	var bestLen int
	for _, s := range sections {
		for _, loc := range s.Locations {
			root := loc.Path
			if root == "" {
				continue
			}
			if path != root && !strings.HasPrefix(path, strings.TrimSuffix(root, "/")+"/") {
				continue
			}
			if len(root) > bestLen {
				bestLen = len(root)
				bestKey = s.Key
			}
		}
	}
	if bestKey == "" {
		return fmt.Errorf("plex: no library section found containing path %q", path)
	}

	req, err := c.newRequest(ctx, http.MethodGet, "/library/sections/"+bestKey+"/refresh", url.Values{"path": {path}})
	if err != nil {
		return err
	}

	// Deliberately a status-only check, not httpx.DoJSON(AllowEmpty): unlike
	// /identity and /library/sections, no community documentation this
	// package's author found describes what body (if any) the refresh
	// endpoint returns on success. Requiring a JSON-decodable (or truly
	// empty) body here risks a false failure — reporting a scan that
	// genuinely fired as an error just because this client guessed wrong
	// about its response shape — so this only checks for a 2xx status and
	// discards the body unparsed. See package doc's HONESTY NOTE.
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("plex: refresh request to %s failed: %w", req.URL.Host, err)
	}
	defer resp.Body.Close()
	if _, err := io.Copy(io.Discard, io.LimitReader(resp.Body, httpx.MaxResponseBodySize)); err != nil {
		return fmt.Errorf("plex: reading refresh response from %s: %w", req.URL.Host, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("plex: refresh on %s returned status %d", req.URL.Host, resp.StatusCode)
	}
	return nil
}

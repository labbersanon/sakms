// Package qbittorrent is a client for qBittorrent's WebUI API — the torrent
// download client this project targets first (per the interview: qBittorrent
// for torrents, NZBGet for usenet). Unlike every Servarr-family client in
// this program (a static X-Api-Key header), qBittorrent authenticates with
// a username+password login that returns a session cookie (SID), so this
// client logs in fresh on every call rather than caching a session — the
// same "cheap, no cached state" design every other client/Session in this
// program already follows (see mode.Build's doc comment), and it avoids
// needing a mutex-guarded session field on Client for what's expected to be
// an infrequent, human-triggered action (one grab at a time), not a hot path.
//
// The /api/v2/torrents/info response fields this client reads (state,
// progress, content_path) are modeled on qBittorrent's documented WebUI API,
// not yet confirmed against a real instance — flagged honestly, the same way
// this project already flags its other unverified external-API assumptions
// (Whisparr's Dedup response shape, Prowlarr's search response, NZBGet's
// append/listgroups/history shapes).
package qbittorrent

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/curtiswtaylorjr/sakms/internal/httpx"
)

// Config parameterizes the client for one qBittorrent instance.
type Config struct {
	BaseURL  string
	Username string
	Password string
}

type Client struct {
	cfg  Config
	http *http.Client
}

func New(cfg Config, httpClient *http.Client) *Client {
	return &Client{cfg: cfg, http: httpClient}
}

// login authenticates and returns the SID session cookie value. qBittorrent
// returns HTTP 200 for both success ("Ok.") and bad credentials ("Fails.")
// on this endpoint — the status code alone doesn't distinguish them, so the
// body text has to be checked too.
func (c *Client) login(ctx context.Context) (sid string, err error) {
	form := url.Values{"username": {c.cfg.Username}, "password": {c.cfg.Password}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.BaseURL+"/api/v2/auth/login", strings.NewReader(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("building login request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("login request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, httpx.MaxResponseBodySize))
	if err != nil {
		return "", fmt.Errorf("reading login response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("login returned status %d", resp.StatusCode)
	}
	if strings.TrimSpace(string(body)) != "Ok." {
		return "", fmt.Errorf("login rejected: invalid username or password")
	}

	for _, ck := range resp.Cookies() {
		if ck.Name == "SID" {
			return ck.Value, nil
		}
	}
	return "", fmt.Errorf("login succeeded but no SID cookie was returned")
}

// Ping confirms the URL and credentials actually work — logging in is
// itself the cheapest real call qBittorrent's API offers; there's no
// separate lightweight health-check endpoint.
func (c *Client) Ping(ctx context.Context) error {
	_, err := c.login(ctx)
	return err
}

// Add submits a torrent (by URL or magnet link) to qBittorrent, optionally
// tagged with category (blank to leave uncategorized).
func (c *Client) Add(ctx context.Context, urlOrMagnet, category string) error {
	sid, err := c.login(ctx)
	if err != nil {
		return err
	}

	form := url.Values{"urls": {urlOrMagnet}}
	if category != "" {
		form.Set("category", category)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.BaseURL+"/api/v2/torrents/add", strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("building add request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Cookie", "SID="+sid)

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("add request failed: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, httpx.MaxResponseBodySize))
	if err != nil {
		return fmt.Errorf("reading add response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("add returned status %d", resp.StatusCode)
	}
	if strings.TrimSpace(string(body)) != "Ok." {
		return fmt.Errorf("add rejected: %s", strings.TrimSpace(string(body)))
	}
	return nil
}

// Status is one torrent's current state, as reported by qBittorrent.
type Status struct {
	Hash     string  `json:"hash"`
	Name     string  `json:"name"`
	State    string  `json:"state"`
	Progress float64 `json:"progress"`
	// ContentPath is qBittorrent's own reported location of the downloaded
	// content on disk — where the import step (internal/api's check-import
	// handler) reads from to relocate a completed download into the
	// library. Populated from qBittorrent's documented content_path field;
	// not yet confirmed against a real instance (see package doc).
	ContentPath string `json:"content_path"`
}

// Status returns the current state of the torrent identified by hash.
// Returns an error if qBittorrent reports no torrent with that hash (e.g. it
// hasn't finished registering the add yet, or was removed).
func (c *Client) Status(ctx context.Context, hash string) (*Status, error) {
	sid, err := c.login(ctx)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.cfg.BaseURL+"/api/v2/torrents/info?hashes="+url.QueryEscape(hash), nil)
	if err != nil {
		return nil, fmt.Errorf("building status request: %w", err)
	}
	req.Header.Set("Cookie", "SID="+sid)

	var out []Status
	if err := httpx.DoJSON(c.http, req, httpx.MaxResponseBodySize, &out); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("qbittorrent has no torrent with hash %q", hash)
	}
	return &out[0], nil
}

var magnetHashPattern = regexp.MustCompile(`(?i)xt=urn:btih:([a-fA-F0-9]+)`)

// HashFromMagnet extracts a torrent's info-hash from a magnet URI, so a grab
// can later be polled via Status even though Add itself doesn't return one.
// Returns ok=false for anything that isn't a magnet URI (e.g. a plain
// .torrent download URL) — deriving the hash from a .torrent file's content
// would need a bencode parser, which this package doesn't have; a grab from
// a non-magnet URL simply can't be status-polled yet, a known gap rather
// than a silently wrong answer.
func HashFromMagnet(magnetURI string) (hash string, ok bool) {
	if !strings.HasPrefix(strings.ToLower(magnetURI), "magnet:") {
		return "", false
	}
	m := magnetHashPattern.FindStringSubmatch(magnetURI)
	if m == nil {
		return "", false
	}
	return strings.ToLower(m[1]), true
}

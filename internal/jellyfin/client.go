// Package jellyfin is a minimal client for Jellyfin's targeted media-refresh
// endpoint. SAK notifies Jellyfin of exact changed paths after a file op so
// Jellyfin's library index stays accurate (Movies/Series only — see
// mode.Build). HONESTY NOTE: the request shape is modeled from Jellyfin
// master source (LibraryController.PostUpdatedMedia); the server currently
// IGNORES UpdateType (reads only Path) — confirmed by reading the
// controller, NOT against a live instance. Jellyfin is a downstream player
// with zero organizational authority (see CLAUDE.md Mission); this
// connection exists ONLY to receive rescan pokes.
package jellyfin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/labbersanon/sakms/internal/httpx"
)

// Config points at a Jellyfin instance.
type Config struct {
	URL    string // base, e.g. https://jf.zaena.us (a trailing slash is tolerated)
	APIKey string
}

type Client struct {
	cfg  Config
	http *http.Client
}

func New(cfg Config, httpClient *http.Client) *Client {
	return &Client{cfg: cfg, http: httpClient}
}

// MediaUpdate is one changed path in a NotifyMediaUpdated batch. JSON tags
// are explicit (PascalCase) to match Jellyfin's C# DTO, even though Go's
// default marshaling of these exported field names already agrees.
type MediaUpdate struct {
	Path       string `json:"Path"`
	UpdateType string `json:"UpdateType"` // "Created"|"Modified"|"Deleted"
}

type mediaUpdateRequest struct {
	Updates []MediaUpdate `json:"Updates"`
}

// newRequest builds a request against {base}+path, joining cfg.URL with a
// trailing slash trimmed first (handles Settings' UI not being strict about
// whether the user typed one), and sets the auth header every Jellyfin
// endpoint here requires.
func (c *Client) newRequest(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	url := strings.TrimSuffix(c.cfg.URL, "/") + path
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, fmt.Errorf("building request: %w", err)
	}
	// The always-parsed "MediaBrowser" scheme header — never the legacy
	// X-Emby-Token header or ?api_key= query param.
	req.Header.Set("Authorization", `MediaBrowser Token="`+c.cfg.APIKey+`"`)
	return req, nil
}

// NotifyMediaUpdated POSTs {base}/Library/Media/Updated. Fire-and-forget: the
// server returns 204 and refreshes in the background; no job to wait on.
func (c *Client) NotifyMediaUpdated(ctx context.Context, updates []MediaUpdate) error {
	body, err := json.Marshal(mediaUpdateRequest{Updates: updates})
	if err != nil {
		return fmt.Errorf("marshaling request: %w", err)
	}

	req, err := c.newRequest(ctx, http.MethodPost, "/Library/Media/Updated", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	var discard json.RawMessage
	return httpx.DoJSONAllowEmpty(c.http, req, httpx.MaxResponseBodySize, &discard)
}

// Ping validates URL+key via GET {base}/System/Info (auth-gated by
// FirstTimeSetupOrIgnoreParentalControl — any valid key works; NOT
// elevation. GET {base}/System/Info/Public has no auth attribute at all, so
// it would not validate the key — deliberately not used here).
func (c *Client) Ping(ctx context.Context) error {
	req, err := c.newRequest(ctx, http.MethodGet, "/System/Info", nil)
	if err != nil {
		return err
	}

	var discard json.RawMessage
	return httpx.DoJSON(c.http, req, httpx.MaxResponseBodySize, &discard)
}

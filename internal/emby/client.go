// Package emby is a minimal client for Emby's targeted media-refresh
// endpoint. SAK notifies Emby of exact changed paths after a file op so
// Emby's library index stays accurate, the same information-flow direction
// as the Jellyfin client (see internal/jellyfin/client.go) and Radarr/
// Sonarr/Whisparr's own rescan commands before it. Emby is a downstream
// player with zero organizational authority (see CLAUDE.md Mission); this
// connection exists ONLY to receive rescan pokes.
//
// HONESTY NOTE: the request/response shapes below are modeled from Emby's
// published OpenAPI spec (https://swagger.emby.media/openapi.json, fetched
// and inspected directly — operationId postLibraryMediaUpdated and
// getSystemInfo), which documents the request/response schema precisely.
// This has NOT been confirmed against a live Emby instance's actual runtime
// behaviour (e.g. whether the server truly refreshes on receipt, or
// silently ignores UpdateType the way Jellyfin's controller does) — only
// the wire contract is verified from the spec, not live behaviour.
//
// Auth deliberately differs from Jellyfin: Jellyfin uses the
// `Authorization: MediaBrowser Token="..."` scheme and explicitly rejects
// the legacy X-Emby-Token header (see internal/jellyfin/client.go). Emby
// uses X-Emby-Token instead — documented in the OpenAPI spec's apikeyauth
// scheme description and on the official wiki
// (https://github.com/MediaBrowser/Emby/wiki/Api-Key-Authentication), though
// the spec's *structured* security scheme models the equivalent ?api_key=
// query parameter, not the header; the header is documented in prose, not
// machine-checked here. Do not copy Jellyfin's Authorization header onto an
// Emby request — the two servers' history diverged on exactly this point
// after Jellyfin's 2018 fork.
//
// Path prefix: the OpenAPI spec's one example `servers[].url` entry
// (https://home.ourflix.de:32865/emby) suggested paths might need an
// "/emby" prefix. Cross-checked against pyemby (mezz64/pyemby), the client
// library behind Home Assistant's official Emby integration: its
// construct_url() returns a bare "https://host:port" with no "/emby"
// segment, and every endpoint call appends its path directly to that base
// (e.g. "{base}/Users/{id}/Items/Latest") — confirming the example server's
// "/emby" segment was that operator's own reverse-proxy path, not a
// canonical API requirement. Config.URL is therefore the operator-entered
// API base exactly as Jellyfin's is: normally just the host, but if an
// instance really is served under a path (reverse-proxied at "/emby" or
// otherwise), the operator includes that path in Config.URL and the
// existing TrimSuffix+join in newRequest carries it through unchanged — see
// TestNotifyMediaUpdated_URLPathPrefix.
package emby

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

// Config points at an Emby Server instance.
type Config struct {
	URL    string // base, e.g. https://emby.zaena.us (a trailing slash is tolerated)
	APIKey string
}

type Client struct {
	cfg  Config
	http *http.Client
}

func New(cfg Config, httpClient *http.Client) *Client {
	return &Client{cfg: cfg, http: httpClient}
}

// MediaUpdate is one changed path in a NotifyMediaUpdated batch. Defined
// locally rather than importing internal/jellyfin.MediaUpdate — two sibling
// player-client packages importing each other for a shared DTO is exactly
// the coupling the house HTTP client pattern exists to avoid. The mode
// package's player adapter is what translates between the two identical
// shapes. JSON tags are explicit (PascalCase) to match Emby's
// Library.MediaUpdateInfo schema.
type MediaUpdate struct {
	Path       string `json:"Path"`
	UpdateType string `json:"UpdateType"` // "Created"|"Modified"|"Deleted"
}

type mediaUpdateRequest struct {
	Updates []MediaUpdate `json:"Updates"`
}

// newRequest builds a request against {base}+path, joining cfg.URL with a
// trailing slash trimmed first (handles Settings' UI not being strict about
// whether the user typed one), and sets the auth header every Emby endpoint
// here requires.
func (c *Client) newRequest(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	url := strings.TrimSuffix(c.cfg.URL, "/") + path
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, fmt.Errorf("building request: %w", err)
	}
	// Emby's own API-key header — NOT Jellyfin's "MediaBrowser Token=..."
	// Authorization scheme. See the package doc for why these diverge.
	req.Header.Set("X-Emby-Token", c.cfg.APIKey)
	return req, nil
}

// NotifyMediaUpdated POSTs {base}/Library/Media/Updated ("Reports that new
// movies have been added by an external source" per Emby's OpenAPI spec).
// Fire-and-forget: per the spec the server returns 200 with an empty body
// and refreshes in the background; there is no job to wait on.
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

// Ping validates URL+key via GET {base}/System/Info. Per the OpenAPI spec
// this route requires authentication as a user (apikeyauth is an accepted
// scheme) and returns a SystemInfo object on success, so any valid key
// succeeds and an invalid one 401s.
func (c *Client) Ping(ctx context.Context) error {
	req, err := c.newRequest(ctx, http.MethodGet, "/System/Info", nil)
	if err != nil {
		return err
	}

	var discard json.RawMessage
	return httpx.DoJSON(c.http, req, httpx.MaxResponseBodySize, &discard)
}

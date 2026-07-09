// Package nzbget is a client for NZBGet's JSON-RPC API — the usenet
// download client this project targets first (per the interview:
// qBittorrent for torrents, NZBGet for usenet).
//
// NZBGet's own "append" RPC method takes the NZB file's raw content
// (base64-encoded), not a download URL — unlike qBittorrent's torrent/add,
// which accepts a URL directly. So Append fetches the .nzb file itself
// first (a plain HTTP GET against the URL Prowlarr gave us), then
// base64-encodes it into the RPC call. This method signature and the
// listgroups/history response shapes below are modeled on NZBGet's
// documented JSON-RPC API, NOT confirmed against a real NZBGet instance —
// flagging that honestly, the same way this project already flags its
// unverified Whisparr Dedup and Prowlarr search-response assumptions,
// rather than presenting it as confirmed.
package nzbget

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/curtiswtaylorjr/sakms/internal/httpx"
)

// Config parameterizes the client for one NZBGet instance.
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

type rpcRequest struct {
	Method string `json:"method"`
	Params []any  `json:"params"`
	ID     int    `json:"id"`
}

type rpcResponse struct {
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// call executes one JSON-RPC method against NZBGet, over HTTP Basic auth.
func (c *Client) call(ctx context.Context, method string, params []any, out any) error {
	reqBody, err := json.Marshal(rpcRequest{Method: method, Params: params, ID: 1})
	if err != nil {
		return fmt.Errorf("marshaling rpc request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.BaseURL+"/jsonrpc", bytes.NewReader(reqBody))
	if err != nil {
		return fmt.Errorf("building rpc request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth(c.cfg.Username, c.cfg.Password)

	var resp rpcResponse
	if err := httpx.DoJSON(c.http, req, httpx.MaxResponseBodySize, &resp); err != nil {
		return err
	}
	if resp.Error != nil {
		return fmt.Errorf("nzbget rpc error: %s", resp.Error.Message)
	}
	if out != nil {
		if err := json.Unmarshal(resp.Result, out); err != nil {
			return fmt.Errorf("decoding rpc result: %w", err)
		}
	}
	return nil
}

// fetchNZB downloads the raw .nzb file content from downloadURL.
func (c *Client) fetchNZB(ctx context.Context, downloadURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return nil, fmt.Errorf("building nzb download request: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("downloading nzb: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("nzb download returned status %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, httpx.MaxResponseBodySizeLarge))
}

// Ping confirms the URL and credentials actually work, via NZBGet's
// lightweight "version" RPC method.
func (c *Client) Ping(ctx context.Context) error {
	var version string
	return c.call(ctx, "version", nil, &version)
}

// Append downloads the .nzb at downloadURL and queues it in NZBGet under
// category (blank for NZBGet's default). Returns NZBGet's assigned NZBID.
func (c *Client) Append(ctx context.Context, downloadURL, filename, category string) (id int64, err error) {
	content, err := c.fetchNZB(ctx, downloadURL)
	if err != nil {
		return 0, err
	}
	encoded := base64.StdEncoding.EncodeToString(content)

	// append(NZBFilename, NZBContent, Category, Priority, AddToTop,
	// AddPaused, DupeKey, DupeScore, DupeMode, PPParameters)
	params := []any{filename, encoded, category, 0, false, false, "", 0, "SCORE", []any{}}

	var result json.RawMessage
	if err := c.call(ctx, "append", params, &result); err != nil {
		return 0, err
	}

	// NZBGet's append has returned either a plain NZBID (newer versions) or
	// a bare boolean success/failure (older versions) across its history —
	// handle both without hard-failing on whichever shape a given install
	// actually returns.
	var asInt int64
	if err := json.Unmarshal(result, &asInt); err == nil {
		if asInt <= 0 {
			return 0, fmt.Errorf("nzbget rejected the nzb")
		}
		return asInt, nil
	}
	var asBool bool
	if err := json.Unmarshal(result, &asBool); err == nil {
		if !asBool {
			return 0, fmt.Errorf("nzbget rejected the nzb")
		}
		return 0, nil
	}
	return 0, fmt.Errorf("unrecognized append result: %s", result)
}

// Status is one queued/completed download's current state, normalized to
// the same shape internal/qbittorrent.Status uses so callers (the
// check-import handler) don't need to branch on which download client a
// grab used.
type Status struct {
	State    string
	Progress float64
	// DestDir is NZBGet's own reported destination directory for this
	// download — where the import step (internal/api's check-import
	// handler) reads from to relocate a completed download into the
	// library. Populated from NZBGet's documented DestDir field; not yet
	// confirmed against a real instance (see package doc).
	DestDir string
}

type groupEntry struct {
	NZBID           int64  `json:"NZBID"`
	NZBName         string `json:"NZBName"`
	Status          string `json:"Status"`
	FileSizeMB      int64  `json:"FileSizeMB"`
	RemainingSizeMB int64  `json:"RemainingSizeMB"`
	DestDir         string `json:"DestDir"`
}

type historyEntry struct {
	NZBID   int64  `json:"NZBID"`
	Name    string `json:"Name"`
	Status  string `json:"Status"`
	DestDir string `json:"DestDir"`
}

// Status reports id's current state — checking the active queue first
// (listgroups), then completed history (history) if it isn't still active.
func (c *Client) Status(ctx context.Context, id int64) (*Status, error) {
	var groups []groupEntry
	if err := c.call(ctx, "listgroups", nil, &groups); err != nil {
		return nil, err
	}
	for _, g := range groups {
		if g.NZBID != id {
			continue
		}
		progress := 0.0
		if g.FileSizeMB > 0 {
			progress = float64(g.FileSizeMB-g.RemainingSizeMB) / float64(g.FileSizeMB)
		}
		return &Status{State: g.Status, Progress: progress, DestDir: g.DestDir}, nil
	}

	var history []historyEntry
	if err := c.call(ctx, "history", nil, &history); err != nil {
		return nil, err
	}
	for _, h := range history {
		if h.NZBID != id {
			continue
		}
		return &Status{State: h.Status, Progress: 1, DestDir: h.DestDir}, nil
	}

	return nil, fmt.Errorf("nzbget has no queued or historical item with id %d", id)
}

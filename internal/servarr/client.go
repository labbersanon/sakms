// Package servarr is a shared client for Sonarr's and Radarr's REST APIs —
// nearly identical shapes (both are "servarr"-family apps), parameterized by
// an App flag for the handful of places their resource/command names differ
// (series vs movie, episodefile vs moviefile, RescanSeries vs RescanMovie).
// One implementation, parameterized config, instead of two near-duplicate
// clients.
package servarr

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"

	"github.com/curtiswtaylorjr/tidyarr/internal/httpx"
)

// App distinguishes Sonarr from Radarr for the handful of endpoint/command
// names that differ between them.
type App int

const (
	Sonarr App = iota
	Radarr
)

// Config parameterizes the client per app instance.
type Config struct {
	BaseURL string
	APIKey  string
	App     App
}

type Client struct {
	cfg  Config
	http *http.Client
}

func New(cfg Config, httpClient *http.Client) *Client {
	return &Client{cfg: cfg, http: httpClient}
}

// itemResource is "series" for Sonarr, "movie" for Radarr — used to build
// /api/v3/{itemResource}... paths.
func (c *Client) itemResource() string {
	if c.cfg.App == Sonarr {
		return "series"
	}
	return "movie"
}

// fileResource is "episodefile" for Sonarr, "moviefile" for Radarr.
func (c *Client) fileResource() string {
	if c.cfg.App == Sonarr {
		return "episodefile"
	}
	return "moviefile"
}

// rescanCommand is the command name Sonarr/Radarr uses to rescan a single
// tracked item's folder for new/changed files.
func (c *Client) rescanCommand() string {
	if c.cfg.App == Sonarr {
		return "RescanSeries"
	}
	return "RescanMovie"
}

// downloadedScanCommand is the command name for a broader "scan everything
// under my root folders for files I don't know about yet" pass.
func (c *Client) downloadedScanCommand() string {
	if c.cfg.App == Sonarr {
		return "DownloadedEpisodesScan"
	}
	return "DownloadedMoviesScan"
}

func (c *Client) do(ctx context.Context, method, path string, body any, out any) error {
	var reqBody []byte
	if body != nil {
		var err error
		reqBody, err = json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshaling request: %w", err)
		}
	}

	req, err := http.NewRequestWithContext(ctx, method, c.cfg.BaseURL+path, bytes.NewReader(reqBody))
	if err != nil {
		return fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("X-Api-Key", c.cfg.APIKey)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	if out == nil {
		// Some endpoints (e.g. DELETE) return no body — DoJSONAllowEmpty
		// treats that as success rather than a decode failure.
		var discard json.RawMessage
		return httpx.DoJSONAllowEmpty(c.http, req, httpx.MaxResponseBodySize, &discard)
	}
	return httpx.DoJSON(c.http, req, httpx.MaxResponseBodySize, out)
}

// RootFolder is one of the app's configured library root folders.
type RootFolder struct {
	ID              int              `json:"id"`
	Path            string           `json:"path"`
	Accessible      bool             `json:"accessible"`
	FreeSpace       int64            `json:"freeSpace"`
	UnmappedFolders []UnmappedFolder `json:"unmappedFolders"`
}

type UnmappedFolder struct {
	Name         string `json:"name"`
	Path         string `json:"path"`
	RelativePath string `json:"relativePath"`
}

// RootFolders returns every configured root folder, each with its current
// unmappedFolders list — the live discovery mechanism for orphaned content.
// IDs and paths are install-specific; always fetch fresh, never hardcode.
func (c *Client) RootFolders(ctx context.Context) ([]RootFolder, error) {
	var out []RootFolder
	if err := c.do(ctx, http.MethodGet, "/api/v3/rootfolder", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// LookupResult is a candidate TVDB/TMDB match from the app's own read-only
// search endpoint — NOT the Manual/Interactive Import flow, just a text
// search proxy.
type LookupResult struct {
	Title    string   `json:"title"`
	Year     int      `json:"year"`
	TVDBID   int      `json:"tvdbId"`
	TMDBID   int      `json:"tmdbId"`
	Genres   []string `json:"genres"`
	Overview string   `json:"overview"`
	// Certification is populated for Radarr (TMDB provides it, e.g. "PG");
	// Sonarr's series/lookup does NOT return this field at all (TVDB has no
	// equivalent in this response) — always "" there. Note this is specific
	// to the *lookup* (pre-add search) response — once a series is tracked,
	// Sonarr's own /series/{id} DOES report a certification (e.g. "TV-MA") —
	// see TrackedItem.
	Certification string `json:"certification"`
}

// Lookup searches by term (title, or "tvdb:<id>"/"tmdb:<id>" for a direct
// ID lookup) via the app's own read-only TVDB/TMDB search proxy.
func (c *Client) Lookup(ctx context.Context, term string) ([]LookupResult, error) {
	path := fmt.Sprintf("/api/v3/%s/lookup?term=%s", c.itemResource(), url.QueryEscape(term))
	var out []LookupResult
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// TriggerCommand fires a named command (e.g. RescanSeries, RescanMovie,
// DownloadedEpisodesScan, DownloadedMoviesScan) with the given extra body
// fields merged in (e.g. {"seriesId": 5} or {"movieId": 5}).
func (c *Client) TriggerCommand(ctx context.Context, name string, extra map[string]any) error {
	body := map[string]any{"name": name}
	for k, v := range extra {
		body[k] = v
	}
	return c.do(ctx, http.MethodPost, "/api/v3/command", body, nil)
}

// RescanTracked triggers a rescan of one already-tracked item's folder.
func (c *Client) RescanTracked(ctx context.Context, itemID int) error {
	key := "movieId"
	if c.cfg.App == Sonarr {
		key = "seriesId"
	}
	return c.TriggerCommand(ctx, c.rescanCommand(), map[string]any{key: itemID})
}

// ScanForDownloaded triggers a broad scan for new/unmapped files under all
// configured root folders — used after registering a newly-added
// series/movie so the app picks up the file(s) already sitting on disk.
func (c *Client) ScanForDownloaded(ctx context.Context) error {
	return c.TriggerCommand(ctx, c.downloadedScanCommand(), nil)
}

// DeleteTrackedFile removes a tracked episodefile/moviefile record AND its
// underlying file in one call — the correct way to remove a duplicate
// Sonarr/Radarr already knows about (never bare fsops.Remove on a tracked
// file, which would leave a dangling DB pointer).
func (c *Client) DeleteTrackedFile(ctx context.Context, fileID int) error {
	path := fmt.Sprintf("/api/v3/%s/%d", c.fileResource(), fileID)
	return c.do(ctx, http.MethodDelete, path, nil, nil)
}

// QualityProfile is one of the app's configured quality profiles — needed to
// register a new series/movie (Add requires a valid qualityProfileId).
type QualityProfile struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

func (c *Client) QualityProfiles(ctx context.Context) ([]QualityProfile, error) {
	var out []QualityProfile
	if err := c.do(ctx, http.MethodGet, "/api/v3/qualityprofile", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// AddRequest is the subset of fields needed to register a new series/movie
// against a specific root folder — the identification (TVDBID/TMDBID) and
// kids-classification (which RootFolderPath) decisions are made independently
// by this tool; this call just hands the resolved answer to Sonarr/Radarr so
// their own naming/import engine takes it from here.
type AddRequest struct {
	Title            string
	TVDBID           int // Sonarr only
	TMDBID           int // Radarr only
	QualityProfileID int
	RootFolderPath   string
	Monitored        bool
}

// Add registers a new series/movie. Sonarr/Radarr's own scan (call
// ScanForDownloaded afterward) then imports whatever file already exists
// under the given root folder, applying their own naming/organization.
func (c *Client) Add(ctx context.Context, req AddRequest) (id int, err error) {
	body := map[string]any{
		"title":            req.Title,
		"qualityProfileId": req.QualityProfileID,
		"rootFolderPath":   req.RootFolderPath,
		"monitored":        req.Monitored,
	}
	if c.cfg.App == Sonarr {
		body["tvdbId"] = req.TVDBID
		body["seasonFolder"] = true
		body["addOptions"] = map[string]any{"monitor": "all", "searchForMissingEpisodes": false, "searchForCutoffUnmetEpisodes": false}
	} else {
		body["tmdbId"] = req.TMDBID
		body["addOptions"] = map[string]any{"searchForMovie": false}
	}

	var resp struct {
		ID int `json:"id"`
	}
	if err := c.do(ctx, http.MethodPost, "/api/v3/"+c.itemResource(), body, &resp); err != nil {
		return 0, err
	}
	return resp.ID, nil
}

// TrackedItem is the subset of an already-tracked series/movie this tool
// cares about — fetched fresh whenever needed rather than cached across a
// run, since a cached path can go stale mid-run if this tool triggers a move
// in between.
//
// Unlike LookupResult, Certification IS populated here for both apps once a
// series/movie is tracked (a tracked Sonarr series reports e.g. "TV-MA",
// even though the pre-add series/lookup search never does).
type TrackedItem struct {
	ID               int      `json:"id"`
	Title            string   `json:"title"`
	Path             string   `json:"path"`
	RootFolderPath   string   `json:"rootFolderPath"`
	Monitored        bool     `json:"monitored"`
	TVDBID           int      `json:"tvdbId"`
	TMDBID           int      `json:"tmdbId"`
	Genres           []string `json:"genres"`
	Certification    string   `json:"certification"`
	QualityProfileID int      `json:"qualityProfileId"`
	Overview         string   `json:"overview"`
}

// AllTracked returns every series/movie the app currently tracks.
func (c *Client) AllTracked(ctx context.Context) ([]TrackedItem, error) {
	var out []TrackedItem
	if err := c.do(ctx, http.MethodGet, "/api/v3/"+c.itemResource(), nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// GetTracked fetches a single tracked item by ID — used to learn its new
// Path after an operation (like UpdateRootFolder) that changes it, since
// AllTracked's response shouldn't be trusted as still-current mid-run.
func (c *Client) GetTracked(ctx context.Context, itemID int) (*TrackedItem, error) {
	var out TrackedItem
	path := fmt.Sprintf("/api/v3/%s/%d", c.itemResource(), itemID)
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// AppType reports which app this client is configured for — callers need
// this to know whether to compare by TVDBID or TMDBID (e.g. when checking
// whether an identified item is already tracked under a different name).
func (c *Client) AppType() App {
	return c.cfg.App
}

// UpdateRootFolder moves a tracked item to a different root folder by
// fetching its full current resource, patching just rootFolderPath, and
// PUTting it back with moveFiles=true — Sonarr/Radarr perform the physical
// move + rename themselves as part of this call (atomic on their side, no
// dangling-pointer risk).
//
// The resource is round-tripped as a raw map rather than a typed struct
// deliberately: Sonarr/Radarr's series/movie objects have dozens of fields
// this tool has no reason to know about, and a typed struct would silently
// drop any field it didn't declare when PUTting back.
func (c *Client) UpdateRootFolder(ctx context.Context, itemID int, newRootFolderPath string) error {
	getPath := fmt.Sprintf("/api/v3/%s/%d", c.itemResource(), itemID)
	var resource map[string]any
	if err := c.do(ctx, http.MethodGet, getPath, nil, &resource); err != nil {
		return fmt.Errorf("fetching current resource: %w", err)
	}
	resource["rootFolderPath"] = newRootFolderPath

	putPath := fmt.Sprintf("/api/v3/%s/%d?moveFiles=true", c.itemResource(), itemID)
	return c.do(ctx, http.MethodPut, putPath, resource, nil)
}

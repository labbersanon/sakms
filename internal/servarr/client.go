// Package servarr is a shared client for Sonarr's, Radarr's, and Whisparr's
// REST APIs — nearly identical shapes (all three are "servarr"-family apps),
// parameterized by an App flag for the handful of places their
// resource/command names differ (series vs movie, episodefile vs moviefile,
// RescanSeries vs RescanMovie). One implementation, parameterized config,
// instead of near-duplicate clients.
//
// Whisparr support (App = Whisparr) targets V3 specifically, which is a
// Radarr fork — confirmed by reading V3's actual source
// (github.com/Whisparr/Whisparr-Eros, "Eros" being its internal codename;
// not github.com/Whisparr/Whisparr, whose default branch is V2 and whose
// own "master" is an empty placeholder). V2 was forked from Sonarr instead
// and has a different (series/episode-shaped) entity model entirely — not
// supported here. Every Whisparr-specific detail below is cited to the
// exact source file it was confirmed against, not inferred from the fork
// lineage alone.
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

// App distinguishes Sonarr, Radarr, and Whisparr for the handful of
// endpoint/command names that differ between them.
type App int

const (
	Sonarr App = iota
	Radarr
	// Whisparr means Whisparr V3 specifically (a Radarr fork) — see the
	// package doc comment. Not V2 (a Sonarr fork), which this client does
	// not support.
	Whisparr
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

// itemResource is "series" for Sonarr, "movie" for Radarr and Whisparr V3 —
// used to build /api/v3/{itemResource}... paths.
//
// Whisparr's "movie" is confirmed directly: Whisparr-Eros's
// src/Whisparr.Api.V3/Movies/MovieController.cs and MovieResource.cs both
// exist (there is no separate "Scene" resource — a scene is a Movie with
// ItemType=Scene, see Add), and its frontend hits /movie routes throughout.
func (c *Client) itemResource() string {
	switch c.cfg.App {
	case Sonarr:
		return "series"
	default: // Radarr, Whisparr
		return "movie"
	}
}

// fileResource is "episodefile" for Sonarr, "moviefile" for Radarr and
// Whisparr V3.
//
// Whisparr's "moviefile" is confirmed directly:
// frontend/src/MovieFile/useMovieFile.ts declares `const PATH =
// '/moviefile'` in Whisparr-Eros's own source.
func (c *Client) fileResource() string {
	switch c.cfg.App {
	case Sonarr:
		return "episodefile"
	default: // Radarr, Whisparr
		return "moviefile"
	}
}

// rescanCommand is the command name used to rescan a single tracked item's
// folder for new/changed files.
//
// Whisparr's "RescanMovie" is confirmed directly:
// src/NzbDrone.Core/MediaFiles/Commands/RescanMovieCommand.cs exists in
// Whisparr-Eros with a MovieId field, and the base Command type derives its
// wire "name" as GetType().Name.Replace("Command", "")
// (Messaging/Commands/Command.cs) — so the literal command name Whisparr's
// API expects is exactly "RescanMovie", same as Radarr.
func (c *Client) rescanCommand() string {
	switch c.cfg.App {
	case Sonarr:
		return "RescanSeries"
	default: // Radarr, Whisparr
		return "RescanMovie"
	}
}

// downloadedScanCommand is the command name for a broader "scan everything
// under my root folders for files I don't know about yet" pass.
//
// Whisparr's "DownloadedMoviesScan" is confirmed the same way as
// rescanCommand: DownloadedMoviesScanCommand.cs exists in Whisparr-Eros.
func (c *Client) downloadedScanCommand() string {
	switch c.cfg.App {
	case Sonarr:
		return "DownloadedEpisodesScan"
	default: // Radarr, Whisparr
		return "DownloadedMoviesScan"
	}
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

// DeleteTracked permanently removes a tracked series/movie/scene AND its
// underlying file(s) in one atomic call (deleteFiles=true) — the mechanism
// behind Purge. Deliberately not a two-step unmonitor-then-delete-file: that
// would leave a dangling unmonitored, fileless entry behind if the second
// call never happened, which is a worse failure mode than this one call
// simply not happening at all.
func (c *Client) DeleteTracked(ctx context.Context, itemID int) error {
	path := fmt.Sprintf("/api/v3/%s/%d?deleteFiles=true", c.itemResource(), itemID)
	return c.do(ctx, http.MethodDelete, path, nil, nil)
}

// Tag is one of the app's native organizational tags — the same Tags
// resource Sonarr, Radarr, and Whisparr V3 all expose identically (a stable,
// long-documented part of the Servarr API, not something that varies by
// app the way movie/series-shaped fields do).
type Tag struct {
	ID    int    `json:"id"`
	Label string `json:"label"`
}

// Tags returns every tag currently defined in the app. A TrackedItem only
// carries tag IDs (see TrackedItem.TagIDs) — resolve them against this list
// to get human-readable labels.
func (c *Client) Tags(ctx context.Context) ([]Tag, error) {
	var out []Tag
	if err := c.do(ctx, http.MethodGet, "/api/v3/tag", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// CreateTag registers a brand-new tag with the app and returns it (with its
// real assigned ID) — the "push new tags upstream immediately" half of the
// design's import-not-duplicate principle. Callers should check Tags first
// and only call this for a label that genuinely doesn't exist yet, so the
// app's tag list is never silently duplicated with near-identical labels.
func (c *Client) CreateTag(ctx context.Context, label string) (Tag, error) {
	var out Tag
	if err := c.do(ctx, http.MethodPost, "/api/v3/tag", map[string]any{"label": label}, &out); err != nil {
		return Tag{}, err
	}
	return out, nil
}

// UpdateItemTags replaces a tracked item's full set of tag IDs. The resource
// is round-tripped as a raw map, same reasoning as UpdateRootFolder: a typed
// struct would silently drop every field this tool doesn't declare.
func (c *Client) UpdateItemTags(ctx context.Context, itemID int, tagIDs []int) error {
	getPath := fmt.Sprintf("/api/v3/%s/%d", c.itemResource(), itemID)
	var resource map[string]any
	if err := c.do(ctx, http.MethodGet, getPath, nil, &resource); err != nil {
		return fmt.Errorf("fetching current resource: %w", err)
	}
	ids := make([]any, len(tagIDs))
	for i, id := range tagIDs {
		ids[i] = id
	}
	resource["tags"] = ids

	putPath := fmt.Sprintf("/api/v3/%s/%d", c.itemResource(), itemID)
	return c.do(ctx, http.MethodPut, putPath, resource, nil)
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

// DefaultQualityProfileID picks the profile most commonly already used by
// tracked items in rootPath, so a new addition fits how this particular
// library is already organized instead of guessing at a hardcoded profile.
// Falls back to the first available profile if rootPath has no tracked items
// yet to learn a convention from. Shared by every workflow that registers
// new items (Rename, Dedup) rather than duplicated in each.
func DefaultQualityProfileID(tracked []TrackedItem, rootPath string, profiles []QualityProfile) int {
	counts := make(map[int]int)
	for _, t := range tracked {
		if t.RootFolderPath == rootPath {
			counts[t.QualityProfileID]++
		}
	}

	// Map iteration order is randomized — break ties by lowest ID so the
	// result is deterministic across runs instead of depending on Go's map
	// ordering.
	bestID, bestCount := 0, 0
	for id, count := range counts {
		if count > bestCount || (count == bestCount && id < bestID) {
			bestID, bestCount = id, count
		}
	}
	if bestCount > 0 {
		return bestID
	}
	if len(profiles) > 0 {
		return profiles[0].ID
	}
	return 0
}

// AddRequest is the subset of fields needed to register a new series/movie/
// scene against a specific root folder — the identification and
// kids-classification (which RootFolderPath) decisions are made
// independently by this tool; this call just hands the resolved answer to
// Sonarr/Radarr/Whisparr so their own naming/import engine takes it from
// here.
type AddRequest struct {
	Title            string
	TVDBID           int // Sonarr only
	TMDBID           int // Radarr only
	QualityProfileID int
	RootFolderPath   string
	Monitored        bool

	// ForeignID and ItemType are Whisparr only. Confirmed directly from
	// Whisparr-Eros's src/Whisparr.Api.V3/Movies/MovieResource.cs (the
	// ForeignId/StashId/TpdbId/TmdbId fields) and
	// MetadataSource/SkyHook/SkyHookProxy.cs's MapForeignId: a scene
	// identified via StashDB/FansDB sets ForeignID to the raw stash-box
	// scene UUID (identify.MatchResult.SceneID, unprefixed); a TPDB-only
	// match instead uses "tpdbId:<id>". TMDB doesn't catalog adult scenes,
	// so TMDBID/tmdbId is never the right field here despite existing on
	// the schema (inherited from the Radarr fork).
	//
	// ItemType is the literal string "scene" or "movie" (matching
	// identify.MatchResult.Type and Whisparr's own ItemType enum, which the
	// server serializes as a lowercase camelCase string). It must be sent
	// explicitly — the enum's zero value is "movie", so an empty ItemType
	// would silently misclassify a scene as a movie rather than erroring.
	ForeignID string
	ItemType  string
}

// Add registers a new series/movie/scene. The app's own scan (call
// ScanForDownloaded afterward) then imports whatever file already exists
// under the given root folder, applying their own naming/organization.
func (c *Client) Add(ctx context.Context, req AddRequest) (id int, err error) {
	body := map[string]any{
		"title":            req.Title,
		"qualityProfileId": req.QualityProfileID,
		"rootFolderPath":   req.RootFolderPath,
		"monitored":        req.Monitored,
	}
	switch c.cfg.App {
	case Sonarr:
		body["tvdbId"] = req.TVDBID
		body["seasonFolder"] = true
		body["addOptions"] = map[string]any{"monitor": "all", "searchForMissingEpisodes": false, "searchForCutoffUnmetEpisodes": false}
	case Whisparr:
		body["foreignId"] = req.ForeignID
		body["itemType"] = req.ItemType
		body["addOptions"] = map[string]any{"searchForMovie": false}
	default: // Radarr
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
	// TagIDs are this item's native organizational tag IDs — resolve against
	// Tags to get labels. Named TagIDs, not Tags, to stay unambiguous next to
	// the servarr.Tag type.
	TagIDs []int `json:"tags"`
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

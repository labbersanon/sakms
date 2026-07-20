// Package stashapi wraps Stash's own (local instance) GraphQL API — distinct
// from the stash-box protocol (internal/stashbox) used for StashDB/FansDB/TPDB.
//
// This client uses a plain http.Client with no special TLS configuration.
// Stash's Traefik-fronted wildcard cert is a real Let's Encrypt certificate,
// so disabling certificate validation would be an unnecessary MITM exposure,
// not a requirement.
package stashapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/labbersanon/sakms/internal/httpx"
)

type Config struct {
	URL    string
	APIKey string
}

type Client struct {
	cfg  Config
	http *http.Client
}

func New(cfg Config, httpClient *http.Client) *Client {
	u := strings.TrimRight(cfg.URL, "/")
	if !strings.HasSuffix(u, "/graphql") {
		u += "/graphql"
	}
	cfg.URL = u
	return &Client{cfg: cfg, http: httpClient}
}

type gqlError struct {
	Message string `json:"message"`
}

type gqlEnvelope struct {
	Data   json.RawMessage `json:"data"`
	Errors []gqlError      `json:"errors"`
}

type GraphQLError struct {
	Messages []string
}

func (e *GraphQLError) Error() string {
	return fmt.Sprintf("stash graphql errors: %s", strings.Join(e.Messages, "; "))
}

func (c *Client) do(ctx context.Context, query string, variables map[string]any, out any) error {
	return c.doWithLimit(ctx, query, variables, out, httpx.MaxResponseBodySize)
}

// doWithLimit is do() with an overridable response-size cap, for the rare
// query that is deliberately unbounded by the API itself (see
// FindScenesByTagIDs's use of Stash's `per_page: -1` convention).
func (c *Client) doWithLimit(ctx context.Context, query string, variables map[string]any, out any, maxBodyBytes int64) error {
	body, err := json.Marshal(map[string]any{"query": query, "variables": variables})
	if err != nil {
		return fmt.Errorf("marshaling request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.URL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("ApiKey", c.cfg.APIKey)

	var env gqlEnvelope
	if err := httpx.DoJSON(c.http, req, maxBodyBytes, &env); err != nil {
		return err
	}
	if len(env.Errors) > 0 {
		msgs := make([]string, len(env.Errors))
		for i, e := range env.Errors {
			msgs[i] = e.Message
		}
		return &GraphQLError{Messages: msgs}
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(env.Data, out)
}

// StashID mirrors a scene's stash_ids entries (used to distinguish scene vs
// movie content by inspecting which stash-box endpoints a scene is linked to).
type StashID struct {
	Endpoint string `json:"endpoint"`
	StashID  string `json:"stash_id"`
}

// StashFile is the full per-file info this program needs, keyed by file path
// in the index built by LoadAllScenes (a scene with multiple files produces
// one StashFile per file, all sharing the same SceneID/Title/Studio/Date).
type StashFile struct {
	SceneID    string
	Title      string
	Studio     string
	Date       string
	Height     int
	Width      int
	Duration   float64
	PHash      string
	OSHash     string
	StashIDs   []StashID
	VideoCodec string
	BitRate    int64
}

type rawFingerprint struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}

type rawStudio struct {
	Name         string `json:"name"`
	ParentStudio *struct {
		Name string `json:"name"`
	} `json:"parent_studio"`
}

type rawSceneFile struct {
	Path         string           `json:"path"`
	Width        int              `json:"width"`
	Height       int              `json:"height"`
	Duration     float64          `json:"duration"`
	VideoCodec   string           `json:"video_codec"`
	BitRate      int64            `json:"bit_rate"`
	Fingerprints []rawFingerprint `json:"fingerprints"`
}

type rawFullScene struct {
	ID       string         `json:"id"`
	Title    string         `json:"title"`
	Date     string         `json:"date"`
	Studio   *rawStudio     `json:"studio"`
	StashIDs []StashID      `json:"stash_ids"`
	Files    []rawSceneFile `json:"files"`
}

func flattenStudio(s *rawStudio) string {
	if s == nil {
		return ""
	}
	if s.Name != "" {
		return s.Name
	}
	if s.ParentStudio != nil {
		return s.ParentStudio.Name
	}
	return ""
}

func fingerprintMap(fps []rawFingerprint) map[string]string {
	m := make(map[string]string, len(fps))
	for _, fp := range fps {
		m[fp.Type] = fp.Value
	}
	return m
}

const findScenesPageQuery = `
query AllScenes($page: Int!) {
  findScenes(filter: {per_page: 200, page: $page}) {
    count
    scenes {
      id
      title
      date
      studio { name parent_studio { name } }
      stash_ids { endpoint stash_id }
      files {
        path
        width
        height
        duration
        video_codec
        bit_rate
        fingerprints { type value }
      }
    }
  }
}`

// LoadAllScenes loads Stash's ENTIRE scene library into an in-memory index
// keyed by file path.
//
// Pagination counts by scenes SEEN, not the resulting index size — a scene
// with multiple files inflates the index past the scene count, and an
// index-size-based pagination check would break out before the last page(s)
// were fetched.
func (c *Client) LoadAllScenes(ctx context.Context) (map[string]*StashFile, error) {
	index := make(map[string]*StashFile)
	page := 1
	total := -1
	scenesSeen := 0

	for {
		var data struct {
			FindScenes struct {
				Count  int            `json:"count"`
				Scenes []rawFullScene `json:"scenes"`
			} `json:"findScenes"`
		}
		if err := c.do(ctx, findScenesPageQuery, map[string]any{"page": page}, &data); err != nil {
			return nil, fmt.Errorf("loading scenes page %d: %w", page, err)
		}
		if total == -1 {
			total = data.FindScenes.Count
		}

		for _, scene := range data.FindScenes.Scenes {
			studioName := flattenStudio(scene.Studio)
			for _, f := range scene.Files {
				fps := fingerprintMap(f.Fingerprints)
				index[f.Path] = &StashFile{
					SceneID:    scene.ID,
					Title:      strings.TrimSpace(scene.Title),
					Studio:     studioName,
					Date:       scene.Date,
					Height:     f.Height,
					Width:      f.Width,
					Duration:   f.Duration,
					PHash:      fps["phash"],
					OSHash:     fps["oshash"],
					StashIDs:   scene.StashIDs,
					VideoCodec: f.VideoCodec,
					BitRate:    f.BitRate,
				}
			}
		}

		scenesSeen += len(data.FindScenes.Scenes)
		if scenesSeen >= total || len(data.FindScenes.Scenes) == 0 {
			break
		}
		page++
	}

	return index, nil
}

const findSceneInfoByPathQuery = `
query FindByPath($path: String!) {
  findScenes(scene_filter: {path: {value: $path, modifier: EQUALS}}) {
    scenes {
      id
      title
      date
      studio { name parent_studio { name } }
      stash_ids { endpoint stash_id }
      files {
        path width height duration video_codec bit_rate
        fingerprints { type value }
      }
    }
  }
}`

// FindSceneInfoByPath re-checks a single file's info without reloading the
// whole library — used after a targeted rescan to see if a phash appeared.
// Returns nil (no error) if Stash has no scene at that exact path.
func (c *Client) FindSceneInfoByPath(ctx context.Context, path string) (*StashFile, error) {
	var data struct {
		FindScenes struct {
			Scenes []rawFullScene `json:"scenes"`
		} `json:"findScenes"`
	}
	if err := c.do(ctx, findSceneInfoByPathQuery, map[string]any{"path": path}, &data); err != nil {
		return nil, err
	}
	for _, scene := range data.FindScenes.Scenes {
		for _, f := range scene.Files {
			if f.Path != path {
				continue
			}
			fps := fingerprintMap(f.Fingerprints)
			return &StashFile{
				SceneID:    scene.ID,
				Title:      strings.TrimSpace(scene.Title),
				Studio:     flattenStudio(scene.Studio),
				Date:       scene.Date,
				Height:     f.Height,
				Width:      f.Width,
				Duration:   f.Duration,
				PHash:      fps["phash"],
				OSHash:     fps["oshash"],
				StashIDs:   scene.StashIDs,
				VideoCodec: f.VideoCodec,
				BitRate:    f.BitRate,
			}, nil
		}
	}
	return nil, nil
}

// FindSceneInfoByPaths is the batched form of FindSceneInfoByPath — it
// re-checks many files' info in a bounded number of HTTP round trips (via
// GraphQL query aliasing, chunked to keep any single request modest) instead
// of one request per path. A path with no matching scene is simply absent
// from the returned map (same "not found" semantics as FindSceneInfoByPath
// returning (nil, nil)).
func (c *Client) FindSceneInfoByPaths(ctx context.Context, paths []string) (map[string]*StashFile, error) {
	out := make(map[string]*StashFile, len(paths))
	const chunkSize = 25
	for start := 0; start < len(paths); start += chunkSize {
		end := start + chunkSize
		if end > len(paths) {
			end = len(paths)
		}
		if err := c.findSceneInfoByPathsChunk(ctx, paths[start:end], out); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (c *Client) findSceneInfoByPathsChunk(ctx context.Context, paths []string, out map[string]*StashFile) error {
	if len(paths) == 0 {
		return nil
	}

	var query strings.Builder
	query.WriteString("query BatchFindByPath(")
	for i := range paths {
		if i > 0 {
			query.WriteString(", ")
		}
		fmt.Fprintf(&query, "$p%d: String!", i)
	}
	query.WriteString(") {\n")
	variables := make(map[string]any, len(paths))
	for i := range paths {
		fmt.Fprintf(&query, "  s%d: findScenes(scene_filter: {path: {value: $p%d, modifier: EQUALS}}) {\n", i, i)
		query.WriteString("    scenes { id title date studio { name parent_studio { name } } stash_ids { endpoint stash_id } files { path width height duration video_codec bit_rate fingerprints { type value } } }\n")
		query.WriteString("  }\n")
		variables[fmt.Sprintf("p%d", i)] = paths[i]
	}
	query.WriteString("}")

	var data map[string]struct {
		Scenes []rawFullScene `json:"scenes"`
	}
	if err := c.doWithLimit(ctx, query.String(), variables, &data, httpx.MaxResponseBodySizeLarge); err != nil {
		return err
	}

	for i, path := range paths {
		entry, ok := data[fmt.Sprintf("s%d", i)]
		if !ok {
			continue
		}
		for _, scene := range entry.Scenes {
			for _, f := range scene.Files {
				if f.Path != path {
					continue
				}
				fps := fingerprintMap(f.Fingerprints)
				out[path] = &StashFile{
					SceneID:    scene.ID,
					Title:      strings.TrimSpace(scene.Title),
					Studio:     flattenStudio(scene.Studio),
					Date:       scene.Date,
					Height:     f.Height,
					Width:      f.Width,
					Duration:   f.Duration,
					PHash:      fps["phash"],
					OSHash:     fps["oshash"],
					StashIDs:   scene.StashIDs,
					VideoCodec: f.VideoCodec,
					BitRate:    f.BitRate,
				}
			}
		}
	}
	return nil
}

const scanMutation = `
mutation ScanPaths($input: ScanMetadataInput!) {
  metadataScan(input: $input)
}`

// ScanPaths triggers a targeted Stash scan (with phash generation) and
// returns the job ID.
func (c *Client) ScanPaths(ctx context.Context, paths []string, rescan bool) (string, error) {
	return c.scanPaths(ctx, paths, rescan, true)
}

// RescanPaths triggers a phash-free add/update sweep (rescan:false) — used by
// the player-notify path (internal/mode.Session.NotifyPlayers), where the
// goal is "notice a file changed" for Stash's own index, not compute a
// phash: SAK computes its own StashDB-compatible phash now
// (internal/videophash), so asking Stash to also generate one here would be
// redundant work on every rename/move.
func (c *Client) RescanPaths(ctx context.Context, paths []string) (string, error) {
	return c.scanPaths(ctx, paths, false, false)
}

// scanPaths is the shared core behind ScanPaths/RescanPaths — a sibling pair
// rather than a bool param on ScanPaths, to keep ScanPaths's existing public
// contract (and its existing test) untouched, and to avoid a two-adjacent-
// bool "boolean trap" at call sites.
func (c *Client) scanPaths(ctx context.Context, paths []string, rescan, generatePhashes bool) (string, error) {
	var data struct {
		MetadataScan string `json:"metadataScan"`
	}
	input := map[string]any{
		"paths": paths, "rescan": rescan, "scanGeneratePhashes": generatePhashes,
	}
	if err := c.do(ctx, scanMutation, map[string]any{"input": input}, &data); err != nil {
		return "", err
	}
	return data.MetadataScan, nil
}

const findJobQuery = `
query FindJob($id: ID!) {
  findJob(input: {id: $id}) { status }
}`

// JobStatus polls a job's current status. found=false means the job has
// already been cleared from Stash's queue (treated as finished by callers).
func (c *Client) JobStatus(ctx context.Context, jobID string) (status string, found bool, err error) {
	var data struct {
		FindJob *struct {
			Status string `json:"status"`
		} `json:"findJob"`
	}
	if err := c.do(ctx, findJobQuery, map[string]any{"id": jobID}, &data); err != nil {
		return "", false, err
	}
	if data.FindJob == nil {
		return "", false, nil
	}
	return data.FindJob.Status, true, nil
}

// WaitJob polls until the job reaches FINISHED/FAILED/CANCELLED or ctx is
// done. Returns true only if it reached FINISHED (or was cleared from the
// queue, treated as finished). The caller controls the timeout via ctx.
func (c *Client) WaitJob(ctx context.Context, jobID string, pollInterval time.Duration) (bool, error) {
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		status, found, err := c.JobStatus(ctx, jobID)
		if err == nil {
			if !found {
				return true, nil
			}
			switch status {
			case "FINISHED":
				return true, nil
			case "FAILED", "CANCELLED":
				return false, nil
			}
		}
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-ticker.C:
		}
	}
}

const cleanMutation = `
mutation Clean($input: CleanMetadataInput!) { metadataClean(input: $input) }`

// CleanMetadata removes Stash DB entries whose files no longer exist on disk,
// scoped to paths. dryRun=true previews (candidates land in Stash's own log,
// not returned via this API); dryRun=false actually removes them.
func (c *Client) CleanMetadata(ctx context.Context, paths []string, dryRun bool) (string, error) {
	var data struct {
		MetadataClean string `json:"metadataClean"`
	}
	input := map[string]any{"paths": paths, "dryRun": dryRun}
	if err := c.do(ctx, cleanMutation, map[string]any{"input": input}, &data); err != nil {
		return "", err
	}
	return data.MetadataClean, nil
}

const destroyScenesMutation = `
mutation DestroyScenes($input: ScenesDestroyInput!) { scenesDestroy(input: $input) }`

// DestroyScenes PERMANENTLY deletes scenes (DB record + file on disk when
// deleteFile is true). Irreversible.
func (c *Client) DestroyScenes(ctx context.Context, ids []string, deleteFile, deleteGenerated bool) error {
	input := map[string]any{
		"ids": ids, "delete_file": deleteFile, "delete_generated": deleteGenerated,
	}
	return c.do(ctx, destroyScenesMutation, map[string]any{"input": input}, nil)
}

type Tag struct {
	ID   string
	Name string
}

const allTagsQuery = `{ allTags { id name } }`

func (c *Client) AllTags(ctx context.Context) ([]Tag, error) {
	var data struct {
		AllTags []Tag `json:"allTags"`
	}
	if err := c.do(ctx, allTagsQuery, nil, &data); err != nil {
		return nil, err
	}
	return data.AllTags, nil
}

// PurgeCandidate is a scene matched by tag for the destructive purge feature.
type PurgeCandidate struct {
	ID    string
	Title string
	Tags  []string
	Path  string // first file's path, or "" if the scene has no files
}

const scenesByTagsQuery = `
query ScenesByTags($ids: [ID!]!) {
  findScenes(scene_filter: { tags: { value: $ids, modifier: INCLUDES } },
             filter: { per_page: -1 }) {
    scenes { id title tags { name } files { path } }
  }
}`

func (c *Client) FindScenesByTagIDs(ctx context.Context, tagIDs []string) ([]PurgeCandidate, error) {
	var data struct {
		FindScenes struct {
			Scenes []struct {
				ID    string `json:"id"`
				Title string `json:"title"`
				Tags  []struct {
					Name string `json:"name"`
				} `json:"tags"`
				Files []struct {
					Path string `json:"path"`
				} `json:"files"`
			} `json:"scenes"`
		} `json:"findScenes"`
	}
	// scenesByTagsQuery uses Stash's own `per_page: -1` (unbounded) convention,
	// unlike LoadAllScenes' paginated 200/request — MaxResponseBodySize was
	// sized for the paginated path, so this uses the larger cap instead.
	if err := c.doWithLimit(ctx, scenesByTagsQuery, map[string]any{"ids": tagIDs}, &data, httpx.MaxResponseBodySizeLarge); err != nil {
		return nil, err
	}
	out := make([]PurgeCandidate, len(data.FindScenes.Scenes))
	for i, s := range data.FindScenes.Scenes {
		tags := make([]string, len(s.Tags))
		for j, t := range s.Tags {
			tags[j] = t.Name
		}
		path := ""
		if len(s.Files) > 0 {
			path = s.Files[0].Path
		}
		out[i] = PurgeCandidate{ID: s.ID, Title: s.Title, Tags: tags, Path: path}
	}
	return out, nil
}

// StashBoxConfig is one entry from Stash's own configured stash-boxes
// (Settings → Metadata Providers), used to discover FansDB's live
// endpoint/key without hardcoding a second plaintext secret.
type StashBoxConfig struct {
	Endpoint string `json:"endpoint"`
	APIKey   string `json:"api_key"`
}

const stashBoxConfigQuery = `
{ configuration { general { stashBoxes { endpoint api_key } } } }`

func (c *Client) StashBoxConfigs(ctx context.Context) ([]StashBoxConfig, error) {
	var data struct {
		Configuration struct {
			General struct {
				StashBoxes []StashBoxConfig `json:"stashBoxes"`
			} `json:"general"`
		} `json:"configuration"`
	}
	if err := c.do(ctx, stashBoxConfigQuery, nil, &data); err != nil {
		return nil, err
	}
	return data.Configuration.General.StashBoxes, nil
}

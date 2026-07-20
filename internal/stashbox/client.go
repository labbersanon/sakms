// Package stashbox implements the "stash-box" GraphQL protocol shared by
// StashDB, FansDB, and ThePornDB's GraphQL endpoint. One client
// implementation serves all three, parameterized by endpoint/auth-style/a
// protocol quirk flag — see Config.HasVoteField.
package stashbox

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/labbersanon/sakms/internal/httpx"
)

// StashDBURL and FansDBURL are the two community stash-box instances' single
// canonical GraphQL endpoints. Like TMDB/TPDB, callers hardcode these instead
// of reading a user-supplied Connection.URL (mirroring the TPDBGraphQLURL
// precedent in internal/mode/mode.go). Vars (not consts) so tests can override
// them to point at an httptest fake.
//
// StashDBURL is well-known and stable. FansDBURL ("https://fansdb.cc/graphql")
// is corroborated by this repo's own test fixtures (internal/stashapi's
// client_test.go) and is FansDB's known public endpoint; if the live server1
// deployment is ever pointed at a different FansDB-compatible instance, verify
// this value against its configured "fansdb" connection URL.
var (
	StashDBURL = "https://stashdb.org/graphql"
	FansDBURL  = "https://fansdb.cc/graphql"
)

// URLForBox returns the hardcoded endpoint for a stash-box connection service
// name ("stashdb" or "fansdb"); ok is false for any other name. Callers that
// build a per-name stash-box client (identification, Adult Discover, entity
// sync) use this so the outbound endpoint is always the fixed constant, never
// the user-supplied Connection.URL.
func URLForBox(service string) (url string, ok bool) {
	switch service {
	case "stashdb":
		return StashDBURL, true
	case "fansdb":
		return FansDBURL, true
	default:
		return "", false
	}
}

// Config parameterizes the client per stash-box instance.
type Config struct {
	Endpoint string
	APIKey   string
	// IsBearer: TPDB-GraphQL uses "Authorization: Bearer <key>"; StashDB and
	// FansDB use an "ApiKey: <key>" header.
	IsBearer bool
	// HasVoteField: StashDB/FansDB's FingerprintSubmission input type has a
	// "vote" field; TPDB's does not, and sending it to TPDB fails GraphQL
	// validation. Set true for StashDB/FansDB, false for TPDB.
	HasVoteField bool
}

type Client struct {
	cfg  Config
	http *http.Client
}

func New(cfg Config, httpClient *http.Client) *Client {
	return &Client{cfg: cfg, http: httpClient}
}

type Scene struct {
	ID          string
	Title       string
	ReleaseDate string
	StudioName  string
	// ImageURL, Tags, and Duration are populated by both the browse path
	// (QueryScenes, ImageURL/Duration only) and the title/id identification
	// paths (SearchScene and FindScene, which request images/tags/duration
	// for display + matching). The fingerprint path
	// (FindScenesByFingerprints) keeps a minimal hash-matching selection that
	// omits them, so scenes it returns leave these zero-valued — that's
	// expected, not a bug. (Tags is not requested by QueryScenes: the
	// Discover browse rows it backs don't need tag names, only the
	// identification/matching pipeline does.) Duration was added to
	// SearchScene/FindScene's selections after a live bug: a caller
	// (internal/adultnewest) building a grab request from a match with no
	// runtime silently failed to auto-qualify anything against Adult's
	// bitrate-quality-floor scorer, which never re-fetches a real runtime
	// the way Movies/Series do.
	ImageURL string
	Tags     []string
	Duration int
	// PHashes are the scene's fingerprint hashes whose algorithm is PHASH
	// (MD5/OSHASH fingerprints are filtered out) — used by the merged-recent
	// dedup to spot a stash-box scene TPDB already carries. Populated ONLY by
	// the browse path (QueryScenes); the identification paths don't request
	// it (fingerprints aren't needed for display/matching there).
	PHashes []string
}

// rawScene mirrors the GraphQL response shape (studio.name, falling back to
// studio.parent.name if the studio itself has no name — a real Stash-box
// convention for sub-studios).
type rawScene struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	ReleaseDate string `json:"release_date"`
	Studio      *struct {
		Name   string `json:"name"`
		Parent *struct {
			Name string `json:"name"`
		} `json:"parent"`
	} `json:"studio"`
	Images []struct {
		URL string `json:"url"`
	} `json:"images"`
	Tags []struct {
		Name string `json:"name"`
	} `json:"tags"`
	Duration int `json:"duration"`
}

func (s rawScene) toScene() Scene {
	studioName := ""
	if s.Studio != nil {
		studioName = s.Studio.Name
		if studioName == "" && s.Studio.Parent != nil {
			studioName = s.Studio.Parent.Name
		}
	}
	imageURL := ""
	if len(s.Images) > 0 {
		imageURL = s.Images[0].URL
	}
	var tags []string
	for _, t := range s.Tags {
		tags = append(tags, t.Name)
	}
	return Scene{
		ID:          s.ID,
		Title:       s.Title,
		ReleaseDate: s.ReleaseDate,
		StudioName:  studioName,
		ImageURL:    imageURL,
		Tags:        tags,
		Duration:    s.Duration,
	}
}

type gqlError struct {
	Message string `json:"message"`
}

type gqlEnvelope struct {
	Data   json.RawMessage `json:"data"`
	Errors []gqlError      `json:"errors"`
}

// GraphQLError carries the raw error messages returned by a stash-box, so
// callers can string-match known failure modes (e.g. "not authorized" for
// draft submission) the same way the identify package needs to.
type GraphQLError struct {
	Messages []string
}

func (e *GraphQLError) Error() string {
	return fmt.Sprintf("graphql errors: %s", strings.Join(e.Messages, "; "))
}

func (c *Client) do(ctx context.Context, query string, variables map[string]any, out any) error {
	body, err := json.Marshal(map[string]any{"query": query, "variables": variables})
	if err != nil {
		return fmt.Errorf("marshaling request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.Endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.cfg.IsBearer {
		req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	} else {
		req.Header.Set("ApiKey", c.cfg.APIKey)
	}

	var env gqlEnvelope
	if err := httpx.DoJSON(c.http, req, httpx.MaxResponseBodySize, &env); err != nil {
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

const fpQuery = `query FindByFingerprints($fps: [[FingerprintQueryInput!]!]!) {
  findScenesBySceneFingerprints(fingerprints: $fps) {
    id title release_date studio { name parent { name } }
  }
}`

// FindScenesByFingerprints batch-looks-up phashes. Returns a slice aligned
// with phashes: out[i] is nil if phashes[i] had no match.
func (c *Client) FindScenesByFingerprints(ctx context.Context, phashes []string) ([]*Scene, error) {
	fpInput := make([][]map[string]string, len(phashes))
	for i, ph := range phashes {
		fpInput[i] = []map[string]string{{"hash": ph, "algorithm": "PHASH"}}
	}
	var data struct {
		FindScenesBySceneFingerprints [][]rawScene `json:"findScenesBySceneFingerprints"`
	}
	if err := c.do(ctx, fpQuery, map[string]any{"fps": fpInput}, &data); err != nil {
		return nil, err
	}
	out := make([]*Scene, len(phashes))
	for i, matches := range data.FindScenesBySceneFingerprints {
		if i >= len(out) {
			break
		}
		if len(matches) > 0 {
			sc := matches[0].toScene()
			out[i] = &sc
		}
	}
	return out, nil
}

// SceneSort selects QueryScenes' server-side ordering — a typed subset of
// stash-box's SceneSortEnum (only the two Adult Discover browses need: DATE for
// the "recently released" feed, TRENDING for the "trending" feed). Direction is
// always DESC, hardcoded in QueryScenes, so no direction knob is exposed.
type SceneSort string

const (
	SceneSortDate     SceneSort = "DATE"
	SceneSortTrending SceneSort = "TRENDING"
)

// defaultBrowsePerPage is the QueryScenes/QueryStudios/QueryPerformers page
// size when the caller passes a non-positive per-page count — matching
// tpdbrest.defaultBrowsePerPage so Adult Discover's stash-box rows and its TPDB
// rows page identically.
const defaultBrowsePerPage = 20

// rawBrowseScene mirrors the richer GraphQL selection the browse query
// (QueryScenes) requests — the same studio.name/parent.name fallback as
// rawScene, plus images/duration/fingerprints. It's separate from rawScene so
// the identification query paths keep their minimal selection unchanged.
type rawBrowseScene struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	ReleaseDate string `json:"release_date"`
	Studio      *struct {
		Name   string `json:"name"`
		Parent *struct {
			Name string `json:"name"`
		} `json:"parent"`
	} `json:"studio"`
	Images []struct {
		URL string `json:"url"`
	} `json:"images"`
	Duration     int `json:"duration"`
	Fingerprints []struct {
		Hash      string `json:"hash"`
		Algorithm string `json:"algorithm"`
	} `json:"fingerprints"`
}

func (s rawBrowseScene) toScene() Scene {
	studioName := ""
	if s.Studio != nil {
		studioName = s.Studio.Name
		if studioName == "" && s.Studio.Parent != nil {
			studioName = s.Studio.Parent.Name
		}
	}
	imageURL := ""
	if len(s.Images) > 0 {
		imageURL = s.Images[0].URL
	}
	var phashes []string
	for _, fp := range s.Fingerprints {
		if fp.Algorithm == "PHASH" {
			phashes = append(phashes, fp.Hash)
		}
	}
	return Scene{
		ID:          s.ID,
		Title:       s.Title,
		ReleaseDate: s.ReleaseDate,
		StudioName:  studioName,
		ImageURL:    imageURL,
		Duration:    s.Duration,
		PHashes:     phashes,
	}
}

const queryScenesQuery = `query QueryScenes($input: SceneQueryInput!) {
  queryScenes(input: $input) {
    scenes { id title release_date studio { name parent { name } } images { url } duration fingerprints { hash algorithm } }
  }
}`

// QueryScenes browses one page of a stash-box's scene catalog, sorted server-side
// by sort (direction always DESC). page/perPage are clamped to sane minimums
// (page >= 1; perPage defaults to defaultBrowsePerPage when non-positive), the
// same lenient contract tpdbrest's browse methods use, so a bad client value
// never produces a malformed query. Backs Adult Discover's optional StashDB/
// FansDB scene rows.
func (c *Client) QueryScenes(ctx context.Context, sort SceneSort, page, perPage int) ([]Scene, error) {
	if perPage <= 0 {
		perPage = defaultBrowsePerPage
	}
	if page <= 0 {
		page = 1
	}
	input := map[string]any{
		"page":      page,
		"per_page":  perPage,
		"sort":      string(sort),
		"direction": "DESC",
	}
	var data struct {
		QueryScenes struct {
			Scenes []rawBrowseScene `json:"scenes"`
		} `json:"queryScenes"`
	}
	if err := c.do(ctx, queryScenesQuery, map[string]any{"input": input}, &data); err != nil {
		return nil, err
	}
	out := make([]Scene, len(data.QueryScenes.Scenes))
	for i, rs := range data.QueryScenes.Scenes {
		out[i] = rs.toScene()
	}
	return out, nil
}

const searchSceneQuery = `query SearchScene($term: String!) {
  searchScene(term: $term) { id title release_date studio { name parent { name } } tags { name } images { url } duration }
}`

// SearchScene returns raw title-search candidates. Similarity/studio-overlap
// filtering is business logic that belongs in internal/identify, not here.
func (c *Client) SearchScene(ctx context.Context, term string) ([]Scene, error) {
	var data struct {
		SearchScene []rawScene `json:"searchScene"`
	}
	if err := c.do(ctx, searchSceneQuery, map[string]any{"term": term}, &data); err != nil {
		return nil, err
	}
	out := make([]Scene, len(data.SearchScene))
	for i, rs := range data.SearchScene {
		out[i] = rs.toScene()
	}
	return out, nil
}

const findSceneQuery = `query FindScene($id: ID!) {
  findScene(id: $id) { id title release_date studio { name parent { name } } tags { name } images { url } duration }
}`

// FindScene looks up a scene directly by its stash-box scene ID.
func (c *Client) FindScene(ctx context.Context, id string) (*Scene, error) {
	var data struct {
		FindScene *rawScene `json:"findScene"`
	}
	if err := c.do(ctx, findSceneQuery, map[string]any{"id": id}, &data); err != nil {
		return nil, err
	}
	if data.FindScene == nil {
		return nil, nil
	}
	sc := data.FindScene.toScene()
	return &sc, nil
}

const submitFingerprintMutation = `mutation SubmitFingerprint($input: FingerprintSubmission!) {
  submitFingerprint(input: $input)
}`

// SubmitFingerprint submits a pHash for an existing scene. The "vote" field is
// included only when Config.HasVoteField is set (StashDB/FansDB, not TPDB).
func (c *Client) SubmitFingerprint(ctx context.Context, sceneID, hash string, durationSeconds int) error {
	input := map[string]any{
		"scene_id": sceneID,
		"fingerprint": map[string]any{
			"hash": hash, "algorithm": "PHASH", "duration": durationSeconds,
		},
	}
	if c.cfg.HasVoteField {
		input["vote"] = "VALID"
	}
	return c.do(ctx, submitFingerprintMutation, map[string]any{"input": input}, nil)
}

const submitSceneDraftMutation = `mutation SubmitSceneDraft($input: SceneDraftInput!) {
  submitSceneDraft(input: $input) { id }
}`

// SubmitSceneDraft submits a new scene draft for community review when no
// existing scene matches at all.
func (c *Client) SubmitSceneDraft(ctx context.Context, title, studio, date string) (string, error) {
	input := map[string]any{
		"title":        title,
		"studio":       map[string]string{"name": studio},
		"date":         date,
		"urls":         []string{},
		"performers":   []string{},
		"tags":         []string{},
		"fingerprints": []string{},
	}
	var data struct {
		SubmitSceneDraft struct {
			ID string `json:"id"`
		} `json:"submitSceneDraft"`
	}
	if err := c.do(ctx, submitSceneDraftMutation, map[string]any{"input": input}, &data); err != nil {
		return "", err
	}
	return data.SubmitSceneDraft.ID, nil
}

type Performer struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// ImageURL is populated by both the browse path (QueryPerformers) and the
	// search path (SearchPerformer) — both request images and collapse
	// images[0].url via rawBrowsePerformer.
	ImageURL string `json:"-"`
}

// rawBrowsePerformer decodes QueryPerformers' images-carrying selection,
// collapsing images[0].url into Performer.ImageURL (empty when the performer
// has no art).
type rawBrowsePerformer struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Images []struct {
		URL string `json:"url"`
	} `json:"images"`
}

func (p rawBrowsePerformer) toPerformer() Performer {
	imageURL := ""
	if len(p.Images) > 0 {
		imageURL = p.Images[0].URL
	}
	return Performer{ID: p.ID, Name: p.Name, ImageURL: imageURL}
}

const queryPerformersQuery = `query QueryPerformers($input: PerformerQueryInput!) {
  queryPerformers(input: $input) {
    performers { id name images { url } }
  }
}`

// QueryPerformers browses one page of a stash-box's performer catalog. page/
// perPage are clamped like QueryScenes. Backs Adult Discover's optional
// StashDB/FansDB Performers rows.
func (c *Client) QueryPerformers(ctx context.Context, page, perPage int) ([]Performer, error) {
	if perPage <= 0 {
		perPage = defaultBrowsePerPage
	}
	if page <= 0 {
		page = 1
	}
	input := map[string]any{"page": page, "per_page": perPage}
	var data struct {
		QueryPerformers struct {
			Performers []rawBrowsePerformer `json:"performers"`
		} `json:"queryPerformers"`
	}
	if err := c.do(ctx, queryPerformersQuery, map[string]any{"input": input}, &data); err != nil {
		return nil, err
	}
	out := make([]Performer, len(data.QueryPerformers.Performers))
	for i, rp := range data.QueryPerformers.Performers {
		out[i] = rp.toPerformer()
	}
	return out, nil
}

const searchPerformerQuery = `query SearchPerformer($term: String!, $limit: Int) {
  searchPerformer(term: $term, limit: $limit) { id name images { url } }
}`

// SearchPerformer text-searches performers by name/alias. Similarity
// filtering of results is business logic that belongs in internal/identify,
// not here — same convention as SearchScene.
func (c *Client) SearchPerformer(ctx context.Context, term string, limit int) ([]Performer, error) {
	var data struct {
		SearchPerformer []rawBrowsePerformer `json:"searchPerformer"`
	}
	if err := c.do(ctx, searchPerformerQuery, map[string]any{"term": term, "limit": limit}, &data); err != nil {
		return nil, err
	}
	out := make([]Performer, len(data.SearchPerformer))
	for i, rp := range data.SearchPerformer {
		out[i] = rp.toPerformer()
	}
	return out, nil
}

type Studio struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// ImageURL is populated by both the browse path (QueryStudios) and the
	// lookup path (FindStudio) — both request images and collapse images[0].url
	// via rawBrowseStudio.
	ImageURL string `json:"-"`
}

// rawBrowseStudio decodes QueryStudios' images-carrying selection, collapsing
// images[0].url into Studio.ImageURL (empty when the studio has no art).
type rawBrowseStudio struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Images []struct {
		URL string `json:"url"`
	} `json:"images"`
}

func (s rawBrowseStudio) toStudio() Studio {
	imageURL := ""
	if len(s.Images) > 0 {
		imageURL = s.Images[0].URL
	}
	return Studio{ID: s.ID, Name: s.Name, ImageURL: imageURL}
}

const queryStudiosQuery = `query QueryStudios($input: StudioQueryInput!) {
  queryStudios(input: $input) {
    studios { id name images { url } }
  }
}`

// QueryStudios browses one page of a stash-box's studio catalog, letting the
// server apply StudioQueryInput's default NAME sort (no direction override).
// page/perPage are clamped like QueryScenes. Backs Adult Discover's optional
// StashDB/FansDB Studios rows.
func (c *Client) QueryStudios(ctx context.Context, page, perPage int) ([]Studio, error) {
	if perPage <= 0 {
		perPage = defaultBrowsePerPage
	}
	if page <= 0 {
		page = 1
	}
	input := map[string]any{"page": page, "per_page": perPage, "sort": "NAME"}
	var data struct {
		QueryStudios struct {
			Studios []rawBrowseStudio `json:"studios"`
		} `json:"queryStudios"`
	}
	if err := c.do(ctx, queryStudiosQuery, map[string]any{"input": input}, &data); err != nil {
		return nil, err
	}
	out := make([]Studio, len(data.QueryStudios.Studios))
	for i, rs := range data.QueryStudios.Studios {
		out[i] = rs.toStudio()
	}
	return out, nil
}

const findStudioQuery = `query FindStudio($name: String) {
  findStudio(name: $name) { id name images { url } }
}`

// FindStudio looks up a studio by name. This is stash-box's own "find by
// name" query (not a fuzzy search like SearchPerformer) — callers should
// pass an already-cleaned name (see identify.normalizeForSearch) and treat a
// nil result as "no exact match" rather than assuming fuzzy tolerance.
func (c *Client) FindStudio(ctx context.Context, name string) (*Studio, error) {
	var data struct {
		FindStudio *rawBrowseStudio `json:"findStudio"`
	}
	if err := c.do(ctx, findStudioQuery, map[string]any{"name": name}, &data); err != nil {
		return nil, err
	}
	if data.FindStudio == nil {
		return nil, nil
	}
	st := data.FindStudio.toStudio()
	return &st, nil
}

const meQuery = `{ me { id name } }`

// Me is the authenticated user stash-box's own "me" query returns — real per
// the stash-box GraphQL schema (StashDB, FansDB, and TPDB's GraphQL endpoint
// all implement it), and the protocol's own argument-free way to confirm an
// endpoint/key actually work without a search or fingerprint lookup.
type Me struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Me queries the currently authenticated user, for connection testing.
func (c *Client) Me(ctx context.Context) (*Me, error) {
	var data struct {
		Me *Me `json:"me"`
	}
	if err := c.do(ctx, meQuery, nil, &data); err != nil {
		return nil, err
	}
	return data.Me, nil
}

// IsNotAuthorized reports whether err represents a stash-box "not authorized"
// rejection (e.g. an account lacking draft-submission privilege).
func IsNotAuthorized(err error) bool {
	var gqlErr *GraphQLError
	if !errors.As(err, &gqlErr) {
		return false
	}
	for _, m := range gqlErr.Messages {
		lower := strings.ToLower(m)
		if strings.Contains(lower, "not authorized") || strings.Contains(lower, "unauthorized") {
			return true
		}
	}
	return false
}

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

	"github.com/curtiswtaylorjr/sak/internal/httpx"
)

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
}

func (s rawScene) toScene() Scene {
	studioName := ""
	if s.Studio != nil {
		studioName = s.Studio.Name
		if studioName == "" && s.Studio.Parent != nil {
			studioName = s.Studio.Parent.Name
		}
	}
	return Scene{ID: s.ID, Title: s.Title, ReleaseDate: s.ReleaseDate, StudioName: studioName}
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

const searchSceneQuery = `query SearchScene($term: String!) {
  searchScene(term: $term) { id title release_date studio { name parent { name } } }
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
  findScene(id: $id) { id title release_date studio { name parent { name } } }
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

// Package api implements SAK's HTTP API.
package api

import (
	"context"
	"fmt"
	"net/http"

	"github.com/curtiswtaylorjr/sak/internal/bravesearch"
	"github.com/curtiswtaylorjr/sak/internal/ollama"
	"github.com/curtiswtaylorjr/sak/internal/servarr"
	"github.com/curtiswtaylorjr/sak/internal/stashapi"
	"github.com/curtiswtaylorjr/sak/internal/stashbox"
	"github.com/curtiswtaylorjr/sak/internal/tpdbrest"
)

// ConnectionTestRequest is enough to construct a client and make one real,
// read-only call against it — the same thing Settings' "Test connection"
// button does. Nothing here is persisted.
type ConnectionTestRequest struct {
	Service string `json:"service"` // "radarr" | "sonarr" | "whisparr" | "ollama" | "stash" | "stashdb" | "fansdb" | "tpdb" | "brave"
	URL     string `json:"url"`
	APIKey  string `json:"apiKey,omitempty"`
}

// ConnectionTestResult reports whether the test call succeeded. A false OK
// with a populated Error is the normal, expected shape for "wrong URL" or
// "wrong key" — not a server-side failure.
type ConnectionTestResult struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// TestConnection dispatches req to the client for its Service and makes one
// lightweight call to confirm the URL/key actually work. Every branch uses a
// real, already-existing endpoint — StashDB/FansDB's protocol-level "me"
// query, TPDB's own /scenes endpoint with minimal params, and Brave's actual
// search endpoint (it has no separate lightweight check, so this is a real,
// billable query, same as any other Brave call) — never a guessed one.
func TestConnection(ctx context.Context, httpClient *http.Client, req ConnectionTestRequest) ConnectionTestResult {
	switch req.Service {
	case "radarr":
		return testServarr(ctx, httpClient, servarr.Radarr, req)
	case "sonarr":
		return testServarr(ctx, httpClient, servarr.Sonarr, req)
	case "whisparr":
		return testServarr(ctx, httpClient, servarr.Whisparr, req)
	case "ollama":
		return testOllama(ctx, httpClient, req)
	case "stash":
		return testStash(ctx, httpClient, req)
	case "stashdb", "fansdb":
		return testStashBox(ctx, httpClient, req)
	case "tpdb":
		return testTPDB(ctx, httpClient, req)
	case "brave":
		return testBrave(ctx, httpClient, req)
	default:
		return ConnectionTestResult{Error: fmt.Sprintf("unsupported service %q", req.Service)}
	}
}

func testServarr(ctx context.Context, httpClient *http.Client, app servarr.App, req ConnectionTestRequest) ConnectionTestResult {
	c := servarr.New(servarr.Config{BaseURL: req.URL, APIKey: req.APIKey, App: app}, httpClient)
	if _, err := c.RootFolders(ctx); err != nil {
		return ConnectionTestResult{Error: err.Error()}
	}
	return ConnectionTestResult{OK: true}
}

func testOllama(ctx context.Context, httpClient *http.Client, req ConnectionTestRequest) ConnectionTestResult {
	c := ollama.New(req.URL, "", httpClient)
	if err := c.Ping(ctx); err != nil {
		return ConnectionTestResult{Error: err.Error()}
	}
	return ConnectionTestResult{OK: true}
}

// testStash expects req.URL to already point at Stash's GraphQL endpoint
// (e.g. "http://host:9999/graphql"), matching stashapi.Config.URL.
func testStash(ctx context.Context, httpClient *http.Client, req ConnectionTestRequest) ConnectionTestResult {
	c := stashapi.New(stashapi.Config{URL: req.URL, APIKey: req.APIKey}, httpClient)
	if _, err := c.AllTags(ctx); err != nil {
		return ConnectionTestResult{Error: err.Error()}
	}
	return ConnectionTestResult{OK: true}
}

// testStashBox covers both StashDB and FansDB — same stash-box protocol,
// ApiKey-header auth (as opposed to TPDB's GraphQL endpoint, which is
// Bearer-authed and reached separately via testTPDB). req.URL must already
// point at the GraphQL endpoint (e.g. "https://stashdb.org/graphql").
func testStashBox(ctx context.Context, httpClient *http.Client, req ConnectionTestRequest) ConnectionTestResult {
	c := stashbox.New(stashbox.Config{Endpoint: req.URL, APIKey: req.APIKey, IsBearer: false, HasVoteField: true}, httpClient)
	if _, err := c.Me(ctx); err != nil {
		return ConnectionTestResult{Error: err.Error()}
	}
	return ConnectionTestResult{OK: true}
}

// testTPDB uses TPDB's REST endpoint (req.URL is the REST base, e.g.
// "https://api.theporndb.net") rather than its GraphQL endpoint — REST is
// what identify's actual text-search fallback uses day to day, so that's
// the connection worth confirming here.
func testTPDB(ctx context.Context, httpClient *http.Client, req ConnectionTestRequest) ConnectionTestResult {
	c := tpdbrest.New(req.URL, req.APIKey, httpClient)
	if err := c.Ping(ctx); err != nil {
		return ConnectionTestResult{Error: err.Error()}
	}
	return ConnectionTestResult{OK: true}
}

// testBrave costs one real query against the account's search quota — Brave
// has no free lightweight way to verify a key, and pretending otherwise
// would be misleading (see the Settings design's cost-visibility note for
// Brave specifically).
func testBrave(ctx context.Context, httpClient *http.Client, req ConnectionTestRequest) ConnectionTestResult {
	c := bravesearch.New(req.URL, req.APIKey, httpClient)
	if err := c.Ping(ctx); err != nil {
		return ConnectionTestResult{Error: err.Error()}
	}
	return ConnectionTestResult{OK: true}
}

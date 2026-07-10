// Package api implements SAK's HTTP API.
package api

import (
	"context"
	"fmt"
	"net/http"

	"github.com/curtiswtaylorjr/sakms/internal/bravesearch"
	"github.com/curtiswtaylorjr/sakms/internal/jellyfin"
	"github.com/curtiswtaylorjr/sakms/internal/nzbget"
	"github.com/curtiswtaylorjr/sakms/internal/ollama"
	"github.com/curtiswtaylorjr/sakms/internal/prowlarr"
	"github.com/curtiswtaylorjr/sakms/internal/qbittorrent"
	"github.com/curtiswtaylorjr/sakms/internal/servarr"
	"github.com/curtiswtaylorjr/sakms/internal/stashapi"
	"github.com/curtiswtaylorjr/sakms/internal/stashbox"
	"github.com/curtiswtaylorjr/sakms/internal/tmdb"
	"github.com/curtiswtaylorjr/sakms/internal/tpdbrest"
)

// ConnectionTestRequest is enough to construct a client and make one real,
// read-only call against it — the same thing Settings' "Test connection"
// button does. Nothing here is persisted.
type ConnectionTestRequest struct {
	Service  string `json:"service"` // "sonarr" | "whisparr" | "ollama" | "stash" | "jellyfin" | "stashdb" | "fansdb" | "tpdb" | "brave" | "prowlarr" | "qbittorrent" | "nzbget" | "tmdb"
	URL      string `json:"url"`
	Username string `json:"username,omitempty"` // only qbittorrent/nzbget use this
	APIKey   string `json:"apiKey,omitempty"`
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
// There is deliberately no "radarr" case: Movies owns its own library now
// instead of proxying Radarr (see internal/library's package doc), so
// there's nothing left in SAK that would ever test a Radarr connection.
func TestConnection(ctx context.Context, httpClient *http.Client, req ConnectionTestRequest) ConnectionTestResult {
	switch req.Service {
	case "sonarr":
		return testServarr(ctx, httpClient, servarr.Sonarr, req)
	case "whisparr":
		return testServarr(ctx, httpClient, servarr.Whisparr, req)
	case "ollama":
		return testOllama(ctx, httpClient, req)
	case "stash":
		return testStash(ctx, httpClient, req)
	case "jellyfin":
		return testJellyfin(ctx, httpClient, req)
	case "stashdb", "fansdb":
		return testStashBox(ctx, httpClient, req)
	case "tpdb":
		return testTPDB(ctx, httpClient, req)
	case "brave":
		return testBrave(ctx, httpClient, req)
	case "prowlarr":
		return testProwlarr(ctx, httpClient, req)
	case "qbittorrent":
		return testQBittorrent(ctx, httpClient, req)
	case "nzbget":
		return testNZBGet(ctx, httpClient, req)
	case "tmdb":
		return testTMDB(ctx, httpClient, req)
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

// testJellyfin expects req.URL to point at Jellyfin's base URL (e.g.
// "https://jf.zaena.us") — req.APIKey is a Jellyfin API key, matching
// jellyfin.Config.
func testJellyfin(ctx context.Context, httpClient *http.Client, req ConnectionTestRequest) ConnectionTestResult {
	c := jellyfin.New(jellyfin.Config{URL: req.URL, APIKey: req.APIKey}, httpClient)
	if err := c.Ping(ctx); err != nil {
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

func testProwlarr(ctx context.Context, httpClient *http.Client, req ConnectionTestRequest) ConnectionTestResult {
	c := prowlarr.New(prowlarr.Config{BaseURL: req.URL, APIKey: req.APIKey}, httpClient)
	if _, err := c.Search(ctx, "", nil); err != nil {
		return ConnectionTestResult{Error: err.Error()}
	}
	return ConnectionTestResult{OK: true}
}

func testQBittorrent(ctx context.Context, httpClient *http.Client, req ConnectionTestRequest) ConnectionTestResult {
	c := qbittorrent.New(qbittorrent.Config{BaseURL: req.URL, Username: req.Username, Password: req.APIKey}, httpClient)
	if err := c.Ping(ctx); err != nil {
		return ConnectionTestResult{Error: err.Error()}
	}
	return ConnectionTestResult{OK: true}
}

func testNZBGet(ctx context.Context, httpClient *http.Client, req ConnectionTestRequest) ConnectionTestResult {
	c := nzbget.New(nzbget.Config{BaseURL: req.URL, Username: req.Username, Password: req.APIKey}, httpClient)
	if err := c.Ping(ctx); err != nil {
		return ConnectionTestResult{Error: err.Error()}
	}
	return ConnectionTestResult{OK: true}
}

func testTMDB(ctx context.Context, httpClient *http.Client, req ConnectionTestRequest) ConnectionTestResult {
	c := tmdb.New(tmdb.Config{BaseURL: req.URL, APIKey: req.APIKey}, httpClient)
	if _, err := c.Popular(ctx, tmdb.Movie); err != nil {
		return ConnectionTestResult{Error: err.Error()}
	}
	return ConnectionTestResult{OK: true}
}

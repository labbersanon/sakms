// Package api implements SAK's HTTP API.
package api

import (
	"context"
	"fmt"
	"net/http"

	"github.com/labbersanon/sakms/internal/bravesearch"
	"github.com/labbersanon/sakms/internal/emby"
	"github.com/labbersanon/sakms/internal/jellyfin"
	"github.com/labbersanon/sakms/internal/nzbget"
	"github.com/labbersanon/sakms/internal/ollama"
	"github.com/labbersanon/sakms/internal/plex"
	"github.com/labbersanon/sakms/internal/prowlarr"
	"github.com/labbersanon/sakms/internal/qbittorrent"
	"github.com/labbersanon/sakms/internal/stashapi"
	"github.com/labbersanon/sakms/internal/stashbox"
	"github.com/labbersanon/sakms/internal/tmdb"
	"github.com/labbersanon/sakms/internal/tpdbrest"
	"github.com/labbersanon/sakms/internal/trakt"
	"github.com/labbersanon/sakms/internal/tvdb"
	"github.com/labbersanon/sakms/internal/usenet"
)

// ConnectionTestRequest is enough to construct a client and make one real,
// read-only call against it — the same thing Settings' "Test connection"
// button does. Nothing here is persisted.
type ConnectionTestRequest struct {
	// Service is a singleton connections service name OR, on the registry test
	// routes, a serviceconn.Provider — the two vocabularies overlap on
	// "jellyfin" deliberately (one dispatch, two callers).
	Service  string `json:"service"` // "ollama" | "stash" | "jellyfin" | "emby" | "plex" | "stashdb" | "fansdb" | "tpdb" | "brave" | "prowlarr" | "nntp" | "tmdb" | "tvdb" | "trakt"
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
// There is deliberately no "radarr" or "sonarr" case: Movies/Series own their
// own libraries now instead of proxying Radarr/Sonarr (see internal/library's
// package doc), so there's nothing left in SAK that would ever test a
// Radarr/Sonarr connection.
func TestConnection(ctx context.Context, httpClient *http.Client, req ConnectionTestRequest) ConnectionTestResult {
	switch req.Service {
	case "ollama":
		return testOllama(ctx, httpClient, req)
	case "stash":
		return testStash(ctx, httpClient, req)
	case "jellyfin":
		return testJellyfin(ctx, httpClient, req)
	// Emby and Plex are registry providers (internal/serviceconn), not
	// singleton `connections` services — they are reached through
	// POST /api/service-connections/test and .../{id}/test, which funnel a
	// player row's provider/url/secret into this same switch rather than
	// carrying a second copy of the per-provider dispatch. Jellyfin's case
	// above serves both callers for the same reason.
	case "emby":
		return testEmby(ctx, httpClient, req)
	case "plex":
		return testPlex(ctx, httpClient, req)
	case "stashdb", "fansdb":
		// Fixed public stash-box endpoints — the URL is the hardcoded per-name
		// constant, never req.URL (the UI collects no URL for these).
		endpoint, _ := stashbox.URLForBox(req.Service)
		return testStashBox(ctx, httpClient, req, endpoint)
	case "tpdb":
		return testTPDB(ctx, httpClient, req)
	case "brave":
		return testBrave(ctx, httpClient, req)
	case "prowlarr":
		return testProwlarr(ctx, httpClient, req)
	// No "qbittorrent"/"nzbget" cases: the unified aria2c downloader replaced
	// both as SAK's download engine (Unified downloader, 2026-07-18), so there
	// is no external download-client connection to test anymore — same
	// precedent as the removed radarr/sonarr/whisparr cases. internal/qbittorrent
	// and internal/nzbget are kept as generic capability (their testQBittorrent/
	// testNZBGet helpers stay for potential reuse) but no live connection type
	// reaches them.
	case "tmdb":
		return testTMDB(ctx, httpClient, req)
	case "tvdb":
		return testTVDB(ctx, httpClient, req)
	case "nntp":
		return testNNTP(ctx, req)
	case "trakt":
		// Trakt has no dedicated client_id field on ConnectionTestRequest, so
		// by convention (see trakt.go's testTrakt doc comment) the generic
		// APIKey field carries client_id here — client_secret isn't needed,
		// Ping only validates client_id against a public endpoint.
		return testTrakt(ctx, httpClient, trakt.DefaultBaseURL, req.APIKey)
	default:
		return ConnectionTestResult{Error: fmt.Sprintf("unsupported service %q", req.Service)}
	}
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

// testEmby expects req.URL to point at Emby's base URL and req.APIKey to be an
// Emby API key. Deliberately NOT routed through testJellyfin despite the shared
// fork ancestry: the two servers authenticate differently (Emby's X-Emby-Token
// vs. Jellyfin's MediaBrowser Token scheme, which Jellyfin's own controller
// rejects the legacy header for), so testing one with the other's client would
// report a working instance as unreachable.
func testEmby(ctx context.Context, httpClient *http.Client, req ConnectionTestRequest) ConnectionTestResult {
	c := emby.New(emby.Config{URL: req.URL, APIKey: req.APIKey}, httpClient)
	if err := c.Ping(ctx); err != nil {
		return ConnectionTestResult{Error: err.Error()}
	}
	return ConnectionTestResult{OK: true}
}

// testPlex expects req.URL to point at the Plex Media Server base URL. The
// generic APIKey field carries the X-Plex-Token — Plex's credential is a token,
// not an API key, but ConnectionTestRequest has one secret field and every other
// provider reaches it the same way (see testTrakt's identical note for
// client_id).
//
// Deliberately calls Sections, not Ping: Ping hits Plex's unauthenticated
// /identity endpoint (see plex.Client.Ping's doc), so it would report success
// for a reachable server even with a wrong/missing token — exactly the kind
// of false-positive Test button this project's Test-connection contract
// exists to prevent (every other case in this file, e.g. testStash's
// AllTags, testProwlarr's Search, calls a real token-gated endpoint for the
// same reason). Sections is a read-only, side-effect-free listing call that
// is token-gated, so a bad token now genuinely fails the test.
func testPlex(ctx context.Context, httpClient *http.Client, req ConnectionTestRequest) ConnectionTestResult {
	c := plex.New(plex.Config{URL: req.URL, Token: req.APIKey}, httpClient)
	if _, err := c.Sections(ctx); err != nil {
		return ConnectionTestResult{Error: err.Error()}
	}
	return ConnectionTestResult{OK: true}
}

// testStashBox covers both StashDB and FansDB — same stash-box protocol,
// ApiKey-header auth (as opposed to TPDB's GraphQL endpoint, which is
// Bearer-authed and reached separately via testTPDB). endpoint is the fixed
// per-name public GraphQL URL (stashbox.StashDBURL / FansDBURL), not a
// user-supplied value — StashDB/FansDB collect no URL in Settings.
func testStashBox(ctx context.Context, httpClient *http.Client, req ConnectionTestRequest, endpoint string) ConnectionTestResult {
	c := stashbox.New(stashbox.Config{Endpoint: endpoint, APIKey: req.APIKey, IsBearer: false, HasVoteField: true}, httpClient)
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
	// Fixed public REST base — hardcoded, never req.URL (no URL collected).
	c := tpdbrest.New(tpdbrest.DefaultBaseURL, req.APIKey, httpClient)
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
	// Fixed public search endpoint — hardcoded, never req.URL (no URL collected).
	c := bravesearch.New(bravesearch.DefaultBaseURL, req.APIKey, httpClient)
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
	// Fixed public base URL — hardcoded, never req.URL (no URL collected).
	c := tmdb.New(tmdb.Config{BaseURL: tmdb.DefaultBaseURL, APIKey: req.APIKey}, httpClient)
	if _, err := c.Popular(ctx, tmdb.Movie, 1); err != nil {
		return ConnectionTestResult{Error: err.Error()}
	}
	return ConnectionTestResult{OK: true}
}

func testTVDB(ctx context.Context, httpClient *http.Client, req ConnectionTestRequest) ConnectionTestResult {
	// Fixed public base URL — hardcoded, never req.URL (no URL collected).
	c := tvdb.New(tvdb.Config{BaseURL: tvdb.DefaultBaseURL, APIKey: req.APIKey}, httpClient)
	if err := c.Ping(ctx); err != nil {
		return ConnectionTestResult{Error: err.Error()}
	}
	return ConnectionTestResult{OK: true}
}

// testNNTP dials the NNTP server, authenticates, and disconnects. ctx is unused
// (nntp.Dial does not accept a context) but is kept for signature symmetry.
func testNNTP(_ context.Context, req ConnectionTestRequest) ConnectionTestResult {
	cfg, err := usenet.ParseURL(req.URL)
	if err != nil {
		return ConnectionTestResult{Error: err.Error()}
	}
	cfg.Username = req.Username
	cfg.Password = req.APIKey
	if err := usenet.TestConnect(cfg); err != nil {
		return ConnectionTestResult{Error: err.Error()}
	}
	return ConnectionTestResult{OK: true}
}

// testNNTPServer is testNNTP for an already-host/port/tls-shaped subscription —
// what the registry stores. It cannot go through TestConnection: that switch is
// keyed on a URL-shaped ConnectionTestRequest, and a registry usenet row has no
// URL to parse (migration 0053 copies the legacy one, then BackfillUsenetURL
// normalizes it into host/port/tls and blanks it).
func testNNTPServer(cfg usenet.ServerConfig) ConnectionTestResult {
	if err := usenet.TestConnect(cfg); err != nil {
		return ConnectionTestResult{Error: err.Error()}
	}
	return ConnectionTestResult{OK: true}
}

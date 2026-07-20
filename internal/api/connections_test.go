package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func testHTTPClient() *http.Client {
	return &http.Client{Timeout: 5 * time.Second}
}

// TestTestConnection_RadarrSonarr_Unsupported confirms "radarr"/"sonarr" are
// no longer recognized services — a clear, actionable error rather than
// silently succeeding against a client nothing else in SAK ever builds.
func TestTestConnection_RadarrSonarr_Unsupported(t *testing.T) {
	for _, service := range []string{"radarr", "sonarr"} {
		result := TestConnection(context.Background(), testHTTPClient(), ConnectionTestRequest{
			Service: service, URL: "http://example.invalid", APIKey: "test-key",
		})
		if result.OK {
			t.Fatalf("expected %s to be unsupported, got ok=true", service)
		}
	}
}

func TestTestConnection_Ollama_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tags" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.Write([]byte(`{"models":[{"name":"qwen2.5:14b"}]}`))
	}))
	defer srv.Close()

	result := TestConnection(context.Background(), testHTTPClient(), ConnectionTestRequest{
		Service: "ollama", URL: srv.URL,
	})
	if !result.OK || result.Error != "" {
		t.Fatalf("expected success, got %+v", result)
	}
}

func TestTestConnection_Ollama_Unreachable(t *testing.T) {
	// A closed server: connection refused, not a status-code failure.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close()

	result := TestConnection(context.Background(), testHTTPClient(), ConnectionTestRequest{
		Service: "ollama", URL: srv.URL,
	})
	if result.OK {
		t.Fatal("expected failure against a closed server")
	}
}

func TestTestConnection_Stash_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("ApiKey") != "stash-key" {
			t.Error("missing ApiKey header")
		}
		w.Write([]byte(`{"data":{"allTags":[{"id":"1","name":"low-quality-flag"}]}}`))
	}))
	defer srv.Close()

	result := TestConnection(context.Background(), testHTTPClient(), ConnectionTestRequest{
		Service: "stash", URL: srv.URL, APIKey: "stash-key",
	})
	if !result.OK || result.Error != "" {
		t.Fatalf("expected success, got %+v", result)
	}
}

func TestTestConnection_Stash_GraphQLError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"errors":[{"message":"not authorized"}]}`))
	}))
	defer srv.Close()

	result := TestConnection(context.Background(), testHTTPClient(), ConnectionTestRequest{
		Service: "stash", URL: srv.URL, APIKey: "bad-key",
	})
	if result.OK {
		t.Fatal("expected failure on a GraphQL error response")
	}
}

func TestTestConnection_Jellyfin_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/System/Info" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != `MediaBrowser Token="jf-key"` {
			t.Error("missing/wrong Authorization header")
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"Version":"10.9.0"}`))
	}))
	defer srv.Close()

	result := TestConnection(context.Background(), testHTTPClient(), ConnectionTestRequest{
		Service: "jellyfin", URL: srv.URL, APIKey: "jf-key",
	})
	if !result.OK || result.Error != "" {
		t.Fatalf("expected success, got %+v", result)
	}
}

func TestTestConnection_Jellyfin_WrongKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	result := TestConnection(context.Background(), testHTTPClient(), ConnectionTestRequest{
		Service: "jellyfin", URL: srv.URL, APIKey: "wrong-key",
	})
	if result.OK {
		t.Fatal("expected failure on 401")
	}
	if result.Error == "" {
		t.Error("expected a populated error message")
	}
}

func TestTestConnection_StashDB_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("ApiKey") != "stashdb-key" {
			t.Error("expected ApiKey header for stashdb")
		}
		w.Write([]byte(`{"data":{"me":{"id":"1","name":"curtis"}}}`))
	}))
	defer srv.Close()
	// testStashBox now targets the hardcoded stashbox.StashDBURL, not req.URL —
	// point it at the fake so the "me" query lands here.
	overrideFixedURL(t, "stashdb", srv.URL)

	result := TestConnection(context.Background(), testHTTPClient(), ConnectionTestRequest{
		Service: "stashdb", URL: srv.URL, APIKey: "stashdb-key",
	})
	if !result.OK || result.Error != "" {
		t.Fatalf("expected success, got %+v", result)
	}
}

func TestTestConnection_FansDB_NotAuthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"errors":[{"message":"not authorized"}]}`))
	}))
	defer srv.Close()
	overrideFixedURL(t, "fansdb", srv.URL)

	result := TestConnection(context.Background(), testHTTPClient(), ConnectionTestRequest{
		Service: "fansdb", URL: srv.URL, APIKey: "bad-key",
	})
	if result.OK {
		t.Fatal("expected failure on a not-authorized response")
	}
}

func TestTestConnection_TPDB_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tpdb-key" {
			t.Error("expected Bearer auth for tpdb")
		}
		w.Write([]byte(`{"data":[]}`))
	}))
	defer srv.Close()
	overrideFixedURL(t, "tpdb", srv.URL)

	result := TestConnection(context.Background(), testHTTPClient(), ConnectionTestRequest{
		Service: "tpdb", URL: srv.URL, APIKey: "tpdb-key",
	})
	if !result.OK || result.Error != "" {
		t.Fatalf("expected success, got %+v", result)
	}
}

func TestTestConnection_Brave_BadKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	// testBrave now targets the hardcoded bravesearch.DefaultBaseURL, not
	// req.URL — point it at the fake so the search call lands here.
	overrideFixedURL(t, "brave", srv.URL)

	result := TestConnection(context.Background(), testHTTPClient(), ConnectionTestRequest{
		Service: "brave", URL: srv.URL, APIKey: "bad-key",
	})
	if result.OK {
		t.Fatal("expected failure on 401")
	}
}

// TestTestConnection_Brave_IgnoresReqURL proves testBrave targets
// bravesearch.DefaultBaseURL regardless of req.URL — req.URL is set to a
// bogus, unreachable host, yet the test still succeeds by reaching the fake
// the package var was redirected to. Regression guard for Brave's Test
// button continuing to work once its URL field is hidden in the UI.
func TestTestConnection_Brave_IgnoresReqURL(t *testing.T) {
	var hit bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = true
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"web":{"results":[]}}`))
	}))
	defer srv.Close()
	overrideFixedURL(t, "brave", srv.URL)

	result := TestConnection(context.Background(), testHTTPClient(), ConnectionTestRequest{
		Service: "brave", URL: "http://wrong.invalid/nope", APIKey: "good-key",
	})
	if !result.OK || result.Error != "" {
		t.Fatalf("expected success (fixed URL used, bogus req.URL ignored), got %+v", result)
	}
	if !hit {
		t.Error("expected the fixed-URL fake to be hit, not req.URL")
	}
}

func TestTestConnection_UnsupportedService(t *testing.T) {
	result := TestConnection(context.Background(), testHTTPClient(), ConnectionTestRequest{
		Service: "plex", URL: "http://example.com",
	})
	if result.OK {
		t.Fatal("expected failure for an unsupported service")
	}
	if result.Error == "" {
		t.Error("expected a populated error message naming the bad service")
	}
}

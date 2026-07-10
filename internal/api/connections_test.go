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

// TestTestConnection_Sonarr_Success proves the generic *arr-family
// connection test path — Movies has no "radarr" case anymore (see
// TestConnection's doc comment for why).
func TestTestConnection_Sonarr_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v3/rootfolder" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Header.Get("X-Api-Key") != "test-key" {
			t.Error("missing X-Api-Key header")
		}
		w.Write([]byte(`[{"id":1,"path":"/media/Series","accessible":true,"freeSpace":123}]`))
	}))
	defer srv.Close()

	result := TestConnection(context.Background(), testHTTPClient(), ConnectionTestRequest{
		Service: "sonarr", URL: srv.URL, APIKey: "test-key",
	})
	if !result.OK || result.Error != "" {
		t.Fatalf("expected success, got %+v", result)
	}
}

// TestTestConnection_Radarr_Unsupported confirms "radarr" is no longer a
// recognized service — a clear, actionable error rather than silently
// succeeding against a client nothing else in SAK ever builds.
func TestTestConnection_Radarr_Unsupported(t *testing.T) {
	result := TestConnection(context.Background(), testHTTPClient(), ConnectionTestRequest{
		Service: "radarr", URL: "http://example.invalid", APIKey: "test-key",
	})
	if result.OK {
		t.Fatal("expected radarr to be unsupported, got ok=true")
	}
}

func TestTestConnection_Sonarr_WrongKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	result := TestConnection(context.Background(), testHTTPClient(), ConnectionTestRequest{
		Service: "sonarr", URL: srv.URL, APIKey: "wrong-key",
	})
	if result.OK {
		t.Fatal("expected failure on 401")
	}
	if result.Error == "" {
		t.Error("expected a populated error message")
	}
}

func TestTestConnection_Whisparr_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v3/rootfolder" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.Write([]byte(`[{"id":1,"path":"/media/Adult","accessible":true,"freeSpace":123}]`))
	}))
	defer srv.Close()

	result := TestConnection(context.Background(), testHTTPClient(), ConnectionTestRequest{
		Service: "whisparr", URL: srv.URL, APIKey: "test-key",
	})
	if !result.OK || result.Error != "" {
		t.Fatalf("expected success, got %+v", result)
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

	result := TestConnection(context.Background(), testHTTPClient(), ConnectionTestRequest{
		Service: "brave", URL: srv.URL, APIKey: "bad-key",
	})
	if result.OK {
		t.Fatal("expected failure on 401")
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

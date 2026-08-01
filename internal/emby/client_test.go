package emby

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func testHTTPClient() *http.Client {
	return &http.Client{Timeout: 5 * time.Second}
}

func TestNotifyMediaUpdated_Success(t *testing.T) {
	var gotMethod, gotPath, gotToken, gotAuth, gotContentType string
	var gotBody mediaUpdateRequest

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotToken = r.Header.Get("X-Emby-Token")
		gotAuth = r.Header.Get("Authorization")
		gotContentType = r.Header.Get("Content-Type")
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("decoding request body: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := New(Config{URL: srv.URL, APIKey: "KEY"}, testHTTPClient())
	updates := []MediaUpdate{
		{Path: "/media/old.mkv", UpdateType: "Deleted"},
		{Path: "/media/new.mkv", UpdateType: "Created"},
	}
	if err := c.NotifyMediaUpdated(context.Background(), updates); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotPath != "/Library/Media/Updated" {
		t.Errorf("path = %q, want /Library/Media/Updated", gotPath)
	}
	if gotToken != "KEY" {
		t.Errorf("X-Emby-Token header = %q, want KEY", gotToken)
	}
	if gotAuth != "" {
		t.Errorf("Authorization header = %q, want empty (Emby uses X-Emby-Token, not Jellyfin's MediaBrowser scheme)", gotAuth)
	}
	if gotContentType != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", gotContentType)
	}
	want := mediaUpdateRequest{Updates: updates}
	if len(gotBody.Updates) != len(want.Updates) {
		t.Fatalf("Updates length = %d, want %d", len(gotBody.Updates), len(want.Updates))
	}
	for i := range want.Updates {
		if gotBody.Updates[i] != want.Updates[i] {
			t.Errorf("Updates[%d] = %+v, want %+v", i, gotBody.Updates[i], want.Updates[i])
		}
	}
}

func TestNotifyMediaUpdated_TrailingSlashURL(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := New(Config{URL: srv.URL + "/", APIKey: "KEY"}, testHTTPClient())
	if err := c.NotifyMediaUpdated(context.Background(), []MediaUpdate{{Path: "/media/a.mkv", UpdateType: "Created"}}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotPath != "/Library/Media/Updated" {
		t.Errorf("path = %q, want /Library/Media/Updated (no doubled slash)", gotPath)
	}
}

// TestNotifyMediaUpdated_URLPathPrefix covers an operator whose Emby
// instance is served under a path prefix (e.g. reverse-proxied at "/emby",
// mirroring the OpenAPI spec's example server) — see the path-prefix
// HONESTY NOTE in client.go. Config.URL carries the whole base including
// the prefix, and the existing TrimSuffix+join in newRequest must pass it
// through unchanged rather than assuming a bare host:port.
func TestNotifyMediaUpdated_URLPathPrefix(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := New(Config{URL: srv.URL + "/emby", APIKey: "KEY"}, testHTTPClient())
	if err := c.NotifyMediaUpdated(context.Background(), []MediaUpdate{{Path: "/media/a.mkv", UpdateType: "Created"}}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotPath != "/emby/Library/Media/Updated" {
		t.Errorf("path = %q, want /emby/Library/Media/Updated (operator-supplied prefix preserved)", gotPath)
	}
}

func TestNotifyMediaUpdated_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := New(Config{URL: srv.URL, APIKey: "KEY"}, testHTTPClient())
	if err := c.NotifyMediaUpdated(context.Background(), []MediaUpdate{{Path: "/media/a.mkv", UpdateType: "Created"}}); err == nil {
		t.Fatal("expected an error on a 500 response")
	}
}

func TestNotifyMediaUpdated_Unauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	c := New(Config{URL: srv.URL, APIKey: "bad-key"}, testHTTPClient())
	if err := c.NotifyMediaUpdated(context.Background(), []MediaUpdate{{Path: "/media/a.mkv", UpdateType: "Created"}}); err == nil {
		t.Fatal("expected an error on a 401 response")
	}
}

func TestPing_Success(t *testing.T) {
	var gotMethod, gotPath, gotToken string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotToken = r.Header.Get("X-Emby-Token")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"Version":"4.8.0","ServerName":"emby"}`))
	}))
	defer srv.Close()

	c := New(Config{URL: srv.URL, APIKey: "KEY"}, testHTTPClient())
	if err := c.Ping(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotMethod != http.MethodGet {
		t.Errorf("method = %q, want GET", gotMethod)
	}
	if gotPath != "/System/Info" {
		t.Errorf("path = %q, want /System/Info", gotPath)
	}
	if gotToken != "KEY" {
		t.Errorf("X-Emby-Token header = %q, want KEY", gotToken)
	}
}

func TestPing_Unauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	c := New(Config{URL: srv.URL, APIKey: "bad-key"}, testHTTPClient())
	if err := c.Ping(context.Background()); err == nil {
		t.Fatal("expected an error on a 401 response")
	}
}

func TestPing_TrailingSlashURL(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"Version":"4.8.0"}`))
	}))
	defer srv.Close()

	c := New(Config{URL: srv.URL + "/", APIKey: "KEY"}, testHTTPClient())
	if err := c.Ping(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotPath != "/System/Info" {
		t.Errorf("path = %q, want /System/Info (no doubled slash)", gotPath)
	}
}

package jellyfin

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
	var gotMethod, gotPath, gotAuth, gotContentType string
	var gotBody mediaUpdateRequest

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotContentType = r.Header.Get("Content-Type")
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("decoding request body: %v", err)
		}
		w.WriteHeader(http.StatusNoContent)
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
	if gotAuth != `MediaBrowser Token="KEY"` {
		t.Errorf("Authorization header = %q, want MediaBrowser Token=\"KEY\"", gotAuth)
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
		w.WriteHeader(http.StatusNoContent)
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

func TestPing_Success(t *testing.T) {
	var gotMethod, gotPath, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"Version":"10.9.0","ServerName":"jf"}`))
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
	if gotAuth != `MediaBrowser Token="KEY"` {
		t.Errorf("Authorization header = %q, want MediaBrowser Token=\"KEY\"", gotAuth)
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
		w.Write([]byte(`{"Version":"10.9.0"}`))
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

package plex

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func testHTTPClient() *http.Client {
	return &http.Client{Timeout: 5 * time.Second}
}

func TestPing_Success(t *testing.T) {
	var gotMethod, gotPath, gotToken, gotAccept string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotToken = r.Header.Get("X-Plex-Token")
		gotAccept = r.Header.Get("Accept")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"MediaContainer":{"size":0,"claimed":1,"machineIdentifier":"abc123","version":"1.29.1"}}`))
	}))
	defer srv.Close()

	c := New(Config{URL: srv.URL, Token: "TOKEN"}, testHTTPClient())
	if err := c.Ping(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotMethod != http.MethodGet {
		t.Errorf("method = %q, want GET", gotMethod)
	}
	if gotPath != "/identity" {
		t.Errorf("path = %q, want /identity", gotPath)
	}
	if gotToken != "TOKEN" {
		t.Errorf("X-Plex-Token header = %q, want TOKEN", gotToken)
	}
	if gotAccept != "application/json" {
		t.Errorf("Accept header = %q, want application/json", gotAccept)
	}
}

func TestPing_TrailingSlashURL(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"MediaContainer":{"machineIdentifier":"abc123","version":"1.29.1"}}`))
	}))
	defer srv.Close()

	c := New(Config{URL: srv.URL + "/", Token: "TOKEN"}, testHTTPClient())
	if err := c.Ping(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotPath != "/identity" {
		t.Errorf("path = %q, want /identity (no doubled slash)", gotPath)
	}
}

func TestPing_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := New(Config{URL: srv.URL, Token: "TOKEN"}, testHTTPClient())
	if err := c.Ping(context.Background()); err == nil {
		t.Fatal("expected an error on a 500 response")
	}
}

func TestPing_MissingMachineIdentifier(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"MediaContainer":{"version":"1.29.1"}}`))
	}))
	defer srv.Close()

	c := New(Config{URL: srv.URL, Token: "TOKEN"}, testHTTPClient())
	if err := c.Ping(context.Background()); err == nil {
		t.Fatal("expected an error when machineIdentifier is missing")
	}
}

func TestSections_Success(t *testing.T) {
	var gotMethod, gotPath, gotToken string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotToken = r.Header.Get("X-Plex-Token")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"MediaContainer":{"size":2,"Directory":[
			{"key":"1","title":"Movies","type":"movie","Location":[{"id":12,"path":"/media/movies"}]},
			{"key":"2","title":"TV Shows","type":"show","Location":[{"id":"13","path":"/media/tv"}]}
		]}}`))
	}))
	defer srv.Close()

	c := New(Config{URL: srv.URL, Token: "TOKEN"}, testHTTPClient())
	sections, err := c.Sections(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotMethod != http.MethodGet {
		t.Errorf("method = %q, want GET", gotMethod)
	}
	if gotPath != "/library/sections" {
		t.Errorf("path = %q, want /library/sections", gotPath)
	}
	if gotToken != "TOKEN" {
		t.Errorf("X-Plex-Token header = %q, want TOKEN", gotToken)
	}
	if len(sections) != 2 {
		t.Fatalf("len(sections) = %d, want 2", len(sections))
	}
	if sections[0].Key != "1" || sections[0].Title != "Movies" || sections[0].Type != "movie" {
		t.Errorf("sections[0] = %+v, unexpected", sections[0])
	}
	if len(sections[0].Locations) != 1 || sections[0].Locations[0].Path != "/media/movies" {
		t.Errorf("sections[0].Locations = %+v, unexpected", sections[0].Locations)
	}
	if len(sections[1].Locations) != 1 || sections[1].Locations[0].Path != "/media/tv" {
		t.Errorf("sections[1].Locations = %+v, unexpected", sections[1].Locations)
	}
}

func TestSections_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	c := New(Config{URL: srv.URL, Token: "bad-token"}, testHTTPClient())
	if _, err := c.Sections(context.Background()); err == nil {
		t.Fatal("expected an error on a 401 response")
	}
}

// sectionsHandler serves a fixed two-section library list: a "Movies"
// section rooted at /media/movies, and a nested "Kids Movies" section
// rooted at /media/movies/kids — the overlapping-prefix case RefreshPath's
// longest-match resolution exists for.
func sectionsHandler(refreshed *[]string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/library/sections":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"MediaContainer":{"Directory":[
				{"key":"1","title":"Movies","type":"movie","Location":[{"id":1,"path":"/media/movies"}]},
				{"key":"2","title":"Kids Movies","type":"movie","Location":[{"id":2,"path":"/media/movies/kids"}]}
			]}}`))
		case strings.HasPrefix(r.URL.Path, "/library/sections/") && strings.HasSuffix(r.URL.Path, "/refresh"):
			*refreshed = append(*refreshed, r.URL.Path+"?"+r.URL.Query().Get("path"))
			// Deliberately a non-empty, non-JSON body (Plex's XML default,
			// unaffected by the Accept header on this particular endpoint in
			// this fixture) — RefreshPath must not require a parseable body
			// to treat this as success; see RefreshPath's own comment.
			w.Header().Set("Content-Type", "text/xml")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`<MediaContainer size="0"></MediaContainer>`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}
}

func TestRefreshPath_ResolvesLongestMatchingSection(t *testing.T) {
	var refreshed []string
	srv := httptest.NewServer(sectionsHandler(&refreshed))
	defer srv.Close()

	c := New(Config{URL: srv.URL, Token: "TOKEN"}, testHTTPClient())
	if err := c.RefreshPath(context.Background(), "/media/movies/kids/Foo (2020)/foo.mkv"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(refreshed) != 1 {
		t.Fatalf("refreshed = %v, want exactly 1 request", refreshed)
	}
	if !strings.HasPrefix(refreshed[0], "/library/sections/2/refresh?") {
		t.Errorf("refreshed[0] = %q, want the longer-matching Kids Movies section (key 2), not Movies (key 1)", refreshed[0])
	}
}

func TestRefreshPath_ResolvesShallowerSection(t *testing.T) {
	var refreshed []string
	srv := httptest.NewServer(sectionsHandler(&refreshed))
	defer srv.Close()

	c := New(Config{URL: srv.URL, Token: "TOKEN"}, testHTTPClient())
	if err := c.RefreshPath(context.Background(), "/media/movies/Foo (2020)/foo.mkv"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(refreshed) != 1 || !strings.HasPrefix(refreshed[0], "/library/sections/1/refresh?") {
		t.Errorf("refreshed = %v, want exactly 1 request to section key 1", refreshed)
	}
}

func TestRefreshPath_NoMatchingSection(t *testing.T) {
	var refreshed []string
	srv := httptest.NewServer(sectionsHandler(&refreshed))
	defer srv.Close()

	c := New(Config{URL: srv.URL, Token: "TOKEN"}, testHTTPClient())
	err := c.RefreshPath(context.Background(), "/media/tv/Some Show/episode.mkv")
	if err == nil {
		t.Fatal("expected an error when no section contains the path")
	}
	if len(refreshed) != 0 {
		t.Errorf("refreshed = %v, want no refresh request sent", refreshed)
	}
}

func TestRefreshPath_DoesNotMatchSiblingWithSharedPrefix(t *testing.T) {
	// /media/movies-archive must NOT match the /media/movies section just
	// because it shares a string prefix — RefreshPath requires a path
	// separator boundary, not a bare strings.HasPrefix.
	var refreshed []string
	srv := httptest.NewServer(sectionsHandler(&refreshed))
	defer srv.Close()

	c := New(Config{URL: srv.URL, Token: "TOKEN"}, testHTTPClient())
	err := c.RefreshPath(context.Background(), "/media/movies-archive/foo.mkv")
	if err == nil {
		t.Fatal("expected an error: /media/movies-archive is not under /media/movies")
	}
}

func TestRefreshPath_RefreshRequestErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/library/sections":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"MediaContainer":{"Directory":[{"key":"1","title":"Movies","type":"movie","Location":[{"path":"/media/movies"}]}]}}`))
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer srv.Close()

	c := New(Config{URL: srv.URL, Token: "TOKEN"}, testHTTPClient())
	if err := c.RefreshPath(context.Background(), "/media/movies/foo.mkv"); err == nil {
		t.Fatal("expected an error when the refresh request itself returns a 500")
	}
}

func TestRefreshPath_SectionsFetchError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := New(Config{URL: srv.URL, Token: "TOKEN"}, testHTTPClient())
	if err := c.RefreshPath(context.Background(), "/media/movies/foo.mkv"); err == nil {
		t.Fatal("expected an error when /library/sections itself fails")
	}
}

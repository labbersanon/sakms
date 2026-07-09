package prowlarr

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return New(Config{BaseURL: srv.URL, APIKey: "test-key"}, srv.Client())
}

// searchFixture is a plausible (but not live-confirmed — see package doc)
// /api/v1/search response spanning both protocols.
const searchFixture = `[
  {
    "guid": "prowlarr-guid-1",
    "title": "Some.Movie.2023.1080p.WEB-DL.x264-GROUP",
    "indexer": "SomeTorrentIndexer",
    "protocol": "torrent",
    "size": 4294967296,
    "seeders": 42,
    "downloadUrl": "https://indexer.example/download/1.torrent",
    "publishDate": "2023-05-01T00:00:00Z",
    "categories": [{"id": 2000}, {"id": 2040}]
  },
  {
    "guid": "prowlarr-guid-2",
    "title": "Some.Movie.2023.2160p.WEB-DL.x265-GROUP",
    "indexer": "SomeUsenetIndexer",
    "protocol": "usenet",
    "size": 8589934592,
    "seeders": 0,
    "downloadUrl": "https://indexer.example/download/2.nzb",
    "publishDate": "2023-05-02T00:00:00Z",
    "categories": [{"id": 2000}]
  }
]`

func TestSearch_ParsesFixtureAcrossBothProtocols(t *testing.T) {
	var gotPath string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.String()
		if r.Header.Get("X-Api-Key") != "test-key" {
			t.Error("missing X-Api-Key header")
		}
		w.Write([]byte(searchFixture))
	})

	releases, err := c.Search(context.Background(), "Some Movie 2023", []int{2000})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(releases) != 2 {
		t.Fatalf("expected 2 releases, got %d", len(releases))
	}
	if releases[0].Protocol != Torrent || releases[0].Seeders != 42 {
		t.Errorf("unexpected first release: %+v", releases[0])
	}
	if releases[1].Protocol != Usenet {
		t.Errorf("unexpected second release: %+v", releases[1])
	}
	if !strings.Contains(gotPath, "query=Some+Movie+2023") {
		t.Errorf("expected query param in request path, got %q", gotPath)
	}
	if !strings.Contains(gotPath, "categories=2000") {
		t.Errorf("expected categories param in request path, got %q", gotPath)
	}
}

func TestSearch_NoCategoriesOmitsParam(t *testing.T) {
	var gotPath string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.String()
		w.Write([]byte(`[]`))
	})

	if _, err := c.Search(context.Background(), "anything", nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(gotPath, "categories") {
		t.Errorf("expected no categories param when none given, got %q", gotPath)
	}
}

func TestSearch_PropagatesErrorStatus(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})

	if _, err := c.Search(context.Background(), "anything", nil); err == nil {
		t.Fatal("expected an error for a 401 response")
	}
}

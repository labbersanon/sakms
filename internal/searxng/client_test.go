package searxng

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClient_SearchParsesJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/search" || r.URL.Query().Get("format") != "json" {
			t.Fatalf("path=%s format=%s", r.URL.Path, r.URL.Query().Get("format"))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"results": []map[string]any{{
				"title":   "Jo Jo Dancer, Your Life Is Calling - Wikipedia",
				"content": "1986 American film starring Richard Pryor.",
				"url":     "https://en.wikipedia.org/wiki/Jo_Jo_Dancer,_Your_Life_Is_Calling",
			}},
		})
	}))
	t.Cleanup(srv.Close)

	c := New(srv.URL, srv.Client())
	got, err := c.Search(context.Background(), "Jo Jo Dancer", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !contains(got[0].Title, "Jo Jo Dancer") {
		t.Fatalf("got %+v", got)
	}
}

func TestClient_SearchImagesUsesImgSrc(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("categories") != "images" {
			t.Fatalf("categories=%s", r.URL.Query().Get("categories"))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"results": []map[string]any{
				{
					"title":   "proxy",
					"url":     "https://example.test/page",
					"img_src": srvImage(r, "/image_proxy?x=1"),
				},
				{
					"title":         "poster",
					"url":           "https://example.test/article",
					"img_src":       "http://insecure.example/p.jpg",
					"thumbnail_src": "https://upload.wikimedia.org/wikipedia/poster.jpg",
				},
			},
		})
	}))
	t.Cleanup(srv.Close)

	c := New(srv.URL, srv.Client())
	got, err := c.SearchImages(context.Background(), "Funny Faces poster", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].URL != "https://upload.wikimedia.org/wikipedia/poster.jpg" {
		t.Fatalf("got %+v", got)
	}
}

func srvImage(r *http.Request, path string) string {
	return "https://" + r.Host + path
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 ||
		(func() bool {
			for i := 0; i+len(sub) <= len(s); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
			return false
		})())
}

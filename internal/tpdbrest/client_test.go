package tpdbrest

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestSearchByHash_ParsesResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("hash") != "abc123" || q.Get("hash_type") != "phash" {
			t.Errorf("unexpected query params: %v", q)
		}
		if auth := r.Header.Get("Authorization"); auth != "Bearer testkey" {
			t.Errorf("expected Bearer auth, got %q", auth)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"_id":"1","title":"A Scene","date":"2024-01-01","site":{"name":"Some Site"}}]}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "testkey", &http.Client{Timeout: 5 * time.Second})
	out, err := c.SearchByHash(context.Background(), "abc123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 1 || out[0].Title != "A Scene" || out[0].Site != "Some Site" {
		t.Fatalf("got %+v", out)
	}
}

// TestSearchByHash_ToleratesNumericSceneID regression-covers a real production
// error: TPDB returns a bare JSON number for _id on some scenes, not always a
// quoted string, which the plain-string rawScene.ID field used to reject
// outright ("json: cannot unmarshal number into Go struct field
// rawScene.data._id of type string"). flexID must decode either shape.
func TestSearchByHash_ToleratesNumericSceneID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"_id":42,"title":"Numeric ID Scene","date":"2024-03-03","site":{"name":"Some Site"}}]}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "testkey", &http.Client{Timeout: 5 * time.Second})
	out, err := c.SearchByHash(context.Background(), "abc123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 1 || out[0].ID != "42" || out[0].Title != "Numeric ID Scene" {
		t.Fatalf("got %+v", out)
	}
}

func TestBrowseScenes_PaginatesWithoutSearchTerm(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("per_page") != "10" || q.Get("page") != "3" {
			t.Errorf("expected per_page=10 page=3, got %v", q)
		}
		if _, hasQ := q["q"]; hasQ {
			t.Errorf("expected no search term on a browse, got %v", q)
		}
		if _, hasOrder := q["orderBy"]; hasOrder {
			t.Errorf("expected no orderBy param when orderBy is empty, got %v", q)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"_id":"s9","title":"Browsed Scene","date":"2024-02-02","site":{"name":"BrowseSite"}}]}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "testkey", &http.Client{Timeout: 5 * time.Second})
	out, err := c.BrowseScenes(context.Background(), 3, 10, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 1 || out[0].Title != "Browsed Scene" || out[0].Site != "BrowseSite" {
		t.Fatalf("got %+v", out)
	}
}

func TestBrowseScenes_ClampsBadPagination(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("per_page") != "20" || q.Get("page") != "1" {
			t.Errorf("expected defaulted per_page=20 page=1, got %v", q)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "testkey", &http.Client{Timeout: 5 * time.Second})
	if _, err := c.BrowseScenes(context.Background(), 0, -5, ""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestBrowseScenes_SendsOrderByWhenSet proves the new orderBy param is sent
// verbatim (exact "orderBy" casing) when non-empty — the "recently_released"
// ordering Adult Discover's Recently Released row relies on.
func TestBrowseScenes_SendsOrderByWhenSet(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("orderBy"); got != "recently_released" {
			t.Errorf("expected orderBy=recently_released, got %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "testkey", &http.Client{Timeout: 5 * time.Second})
	if _, err := c.BrowseScenes(context.Background(), 1, 20, "recently_released"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestGet_ParsesRating proves the scene's numeric "rating" field decodes into
// Scene.Rating — the field Adult Discover's Highest Rated row sorts on.
func TestGet_ParsesRating(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"_id":"1","title":"Rated Scene","date":"2024-01-01","site":{"name":"Some Site"},"rating":5}]}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "testkey", &http.Client{Timeout: 5 * time.Second})
	out, err := c.SearchByHash(context.Background(), "x")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 1 || out[0].Rating != 5 {
		t.Fatalf("expected Rating=5, got %+v", out)
	}
}

func TestBrowsePerformers_PaginatesWithoutSearchTerm(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/performers" {
			t.Errorf("expected path /performers, got %q", r.URL.Path)
		}
		q := r.URL.Query()
		if q.Get("per_page") != "10" || q.Get("page") != "2" {
			t.Errorf("expected per_page=10 page=2, got %v", q)
		}
		if _, hasQ := q["q"]; hasQ {
			t.Errorf("expected no search term on a browse, got %v", q)
		}
		w.Header().Set("Content-Type", "application/json")
		// image empty → falls back to thumbnail per the first-non-empty rule.
		_, _ = w.Write([]byte(`{"data":[{"_id":"p1","name":"Riley Reid","image":"","thumbnail":"http://cdn/thumb.jpg","face":"http://cdn/face.jpg"}]}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "testkey", &http.Client{Timeout: 5 * time.Second})
	out, err := c.BrowsePerformers(context.Background(), 2, 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 1 || out[0].Name != "Riley Reid" || out[0].ID != "p1" {
		t.Fatalf("got %+v", out)
	}
	if out[0].Image != "http://cdn/thumb.jpg" {
		t.Errorf("expected Image to fall back to thumbnail, got %q", out[0].Image)
	}
}

// TestBrowsePerformers_ToleratesNumericID guards flexID's reuse on
// rawPerformer.ID: a regression that narrowed it back to a plain string would
// pass every other performer test (all use quoted-string ids) while still
// crashing on the exact bug class TPDB already shipped once for scenes.
func TestBrowsePerformers_ToleratesNumericID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"_id":42,"name":"Numeric ID Performer"}]}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "testkey", &http.Client{Timeout: 5 * time.Second})
	out, err := c.BrowsePerformers(context.Background(), 1, 20)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 1 || out[0].ID != "42" {
		t.Fatalf("got %+v", out)
	}
}

func TestBrowseSites_PaginatesWithoutSearchTerm(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/sites" {
			t.Errorf("expected path /sites, got %q", r.URL.Path)
		}
		q := r.URL.Query()
		if q.Get("per_page") != "20" || q.Get("page") != "1" {
			t.Errorf("expected defaulted per_page=20 page=1, got %v", q)
		}
		if _, hasQ := q["q"]; hasQ {
			t.Errorf("expected no search term on a browse, got %v", q)
		}
		w.Header().Set("Content-Type", "application/json")
		// logo present → chosen first over poster/favicon.
		_, _ = w.Write([]byte(`{"data":[{"_id":"s1","name":"Tushy","logo":"http://cdn/logo.png","favicon":"http://cdn/fav.ico","poster":"http://cdn/poster.jpg"}]}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "testkey", &http.Client{Timeout: 5 * time.Second})
	out, err := c.BrowseSites(context.Background(), 0, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 1 || out[0].Name != "Tushy" || out[0].ID != "s1" {
		t.Fatalf("got %+v", out)
	}
	if out[0].Image != "http://cdn/logo.png" {
		t.Errorf("expected Image to prefer logo, got %q", out[0].Image)
	}
}

// TestBrowseSites_ToleratesNumericID is TestBrowsePerformers_ToleratesNumericID's
// sibling for rawSiteEntry.ID — same regression-guard rationale.
func TestBrowseSites_ToleratesNumericID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"_id":7,"name":"Numeric ID Site"}]}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "testkey", &http.Client{Timeout: 5 * time.Second})
	out, err := c.BrowseSites(context.Background(), 1, 20)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 1 || out[0].ID != "7" {
		t.Fatalf("got %+v", out)
	}
}

func TestScenesBySite_UsesDedicatedEndpoint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/sites/s%201/scenes" && r.URL.Path != "/sites/s 1/scenes" {
			t.Errorf("expected path /sites/{id}/scenes with escaped id, got %q", r.URL.Path)
		}
		q := r.URL.Query()
		if q.Get("per_page") != "5" || q.Get("page") != "2" {
			t.Errorf("expected per_page=5 page=2, got %v", q)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"_id":"sc1","title":"Site Scene","date":"2024-01-01","site":{"name":"Tushy"}}]}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "testkey", &http.Client{Timeout: 5 * time.Second})
	// "s 1" (with a space) proves the id is path-escaped, not parsed as an int.
	out, err := c.ScenesBySite(context.Background(), "s 1", 2, 5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 1 || out[0].Title != "Site Scene" || out[0].Site != "Tushy" {
		t.Fatalf("got %+v", out)
	}
}

func TestScenesByPerformer_UsesDedicatedEndpoint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/performers/p1/scenes" {
			t.Errorf("expected path /performers/p1/scenes, got %q", r.URL.Path)
		}
		q := r.URL.Query()
		if q.Get("per_page") != "20" || q.Get("page") != "1" {
			t.Errorf("expected defaulted per_page=20 page=1, got %v", q)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"_id":"sc2","title":"Performer Scene","date":"2024-01-01","site":{"name":"Vixen"}}]}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "testkey", &http.Client{Timeout: 5 * time.Second})
	out, err := c.ScenesByPerformer(context.Background(), "p1", 0, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 1 || out[0].Title != "Performer Scene" {
		t.Fatalf("got %+v", out)
	}
}

func TestSearchByTitle_OmitsSiteWhenEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("q") != "Some Title" {
			t.Errorf("expected q=Some Title, got %q", q.Get("q"))
		}
		if _, has := q["site"]; has {
			t.Errorf("expected no 'site' param when studio is empty, got %v", q)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "testkey", &http.Client{Timeout: 5 * time.Second})
	if _, err := c.SearchByTitle(context.Background(), "Some Title", ""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSearchByTitle_IncludesSiteWhenSet(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("site") != "Tushy" {
			t.Errorf("expected site=Tushy, got %q", q.Get("site"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "testkey", &http.Client{Timeout: 5 * time.Second})
	if _, err := c.SearchByTitle(context.Background(), "Some Title", "Tushy"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestPing_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("per_page") != "1" {
			t.Errorf("expected per_page=1, got %q", q.Get("per_page"))
		}
		if _, hasQ := q["q"]; hasQ {
			t.Errorf("expected no search term on a ping, got %v", q)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "testkey", &http.Client{Timeout: 5 * time.Second})
	if err := c.Ping(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestPing_UnauthorizedKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	c := New(srv.URL, "badkey", &http.Client{Timeout: 5 * time.Second})
	if err := c.Ping(context.Background()); err == nil {
		t.Fatal("expected an error for a bad key")
	}
}

func TestGet_NonOKStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	c := New(srv.URL, "badkey", &http.Client{Timeout: 5 * time.Second})
	_, err := c.SearchByHash(context.Background(), "x")
	if err == nil {
		t.Fatal("expected an error on non-200 status")
	}
}

func TestGet_ParsesDuration(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"_id":"1","title":"Timed Scene","date":"2024-01-01","site":{"name":"Some Site"},"duration":1800}]}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "testkey", &http.Client{Timeout: 5 * time.Second})
	out, err := c.SearchByHash(context.Background(), "x")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 1 || out[0].Duration != 1800 {
		t.Fatalf("expected Duration=1800 seconds, got %+v", out)
	}
}

func TestGet_MissingDurationIsZero(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"_id":"1","title":"No Duration Scene","date":"2024-01-01","site":{"name":"Some Site"}}]}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "testkey", &http.Client{Timeout: 5 * time.Second})
	out, err := c.SearchByHash(context.Background(), "x")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// A scene with no "duration" in the response must decode to 0, the
	// documented "unknown, skip the bitrate check" sentinel — never an error.
	if len(out) != 1 || out[0].Duration != 0 {
		t.Fatalf("expected Duration=0 when absent from response, got %+v", out)
	}
}

func TestGet_EmptySiteFallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"_id":"1","title":"No Site Scene","date":"2024-01-01","site":null}]}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "k", &http.Client{Timeout: 5 * time.Second})
	out, err := c.SearchByHash(context.Background(), "x")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out[0].Site != "" {
		t.Fatalf("expected empty site for null site, got %q", out[0].Site)
	}
}

func TestSearchPerformers_ParsesResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/performers" {
			t.Errorf("expected path /performers, got %q", r.URL.Path)
		}
		if r.URL.Query().Get("q") != "riley reid" {
			t.Errorf("expected q=riley reid, got %q", r.URL.Query().Get("q"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"_id":"p1","name":"Riley Reid"}]}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "testkey", &http.Client{Timeout: 5 * time.Second})
	out, err := c.SearchPerformers(context.Background(), "riley reid")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 1 || out[0].Name != "Riley Reid" || out[0].ID != "p1" {
		t.Fatalf("got %+v", out)
	}
}

func TestSearchSites_ParsesResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/sites" {
			t.Errorf("expected path /sites, got %q", r.URL.Path)
		}
		if r.URL.Query().Get("q") != "tushy" {
			t.Errorf("expected q=tushy, got %q", r.URL.Query().Get("q"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"_id":"s1","name":"Tushy"}]}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "testkey", &http.Client{Timeout: 5 * time.Second})
	out, err := c.SearchSites(context.Background(), "tushy")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 1 || out[0].Name != "Tushy" || out[0].ID != "s1" {
		t.Fatalf("got %+v", out)
	}
}

package newznab

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestSearchCatalog_CatOnlyNoHTMLNoNZB(t *testing.T) {
	var gotQ, gotT, gotCat, gotOff, gotLim string
	var sawHTML, fetchedNZB bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "login") || strings.Contains(r.URL.Path, "details") {
			sawHTML = true
			http.NotFound(w, r)
			return
		}
		if r.URL.Path == "/getnzb" {
			fetchedNZB = true
			http.NotFound(w, r)
			return
		}
		gotT = r.URL.Query().Get("t")
		gotQ = r.URL.Query().Get("q")
		gotCat = r.URL.Query().Get("cat")
		gotOff = r.URL.Query().Get("offset")
		gotLim = r.URL.Query().Get("limit")
		io.WriteString(w, `<?xml version="1.0"?>
<rss version="2.0" xmlns:newznab="http://www.newznab.com/DTD/2007/feeds/attributes/">
<channel><item>
<title>Some.Movie.2021.1080p.WEB-DL.x264-GRP</title>
<guid>cat-1</guid>
<newznab:attr name="size" value="999"/>
<newznab:attr name="imdb" value="tt0111161"/>
<newznab:attr name="category" value="2000"/>
</item></channel></rss>`)
	}))
	defer srv.Close()

	c := &Client{HTTP: srv.Client(), Interval: time.Nanosecond}
	hits, err := c.SearchCatalog(context.Background(), Indexer{
		Name: "newznab", Endpoint: srv.URL + "/api", APIKey: "k",
	}, 2000, 50, 50)
	if err != nil {
		t.Fatal(err)
	}
	if gotT != "search" || gotCat != "2000" || gotOff != "50" || gotLim != "50" {
		t.Fatalf("t=%q cat=%q off=%q lim=%q", gotT, gotCat, gotOff, gotLim)
	}
	if gotQ != "" {
		t.Fatalf("q=%q want empty", gotQ)
	}
	if sawHTML || fetchedNZB {
		t.Fatalf("html=%v nzb=%v", sawHTML, fetchedNZB)
	}
	if len(hits) != 1 || hits[0].GUID != "cat-1" || hits[0].ImdbID != "tt0111161" {
		t.Fatalf("hits=%+v", hits)
	}
	if OverlapKey(hits[0]) != "imdb:tt0111161" {
		t.Fatalf("key=%q", OverlapKey(hits[0]))
	}
}

func TestSearchCatalog_MissingParamRetriesStar(t *testing.T) {
	var qs []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		qs = append(qs, r.URL.Query().Get("q"))
		if r.URL.Query().Get("q") == "" {
			io.WriteString(w, `<?xml version="1.0"?><error code="200" description="Missing parameter"/>`)
			return
		}
		io.WriteString(w, `<?xml version="1.0"?>
<rss version="2.0" xmlns:newznab="http://www.newznab.com/DTD/2007/feeds/attributes/">
<channel><item>
<title>Some.Show.S01E01.1080p</title>
<guid>star-1</guid>
<newznab:attr name="size" value="1"/>
<newznab:attr name="category" value="5000"/>
</item></channel></rss>`)
	}))
	defer srv.Close()

	c := &Client{HTTP: srv.Client(), Interval: time.Nanosecond}
	hits, err := c.SearchCatalog(context.Background(), Indexer{
		Name: "newznab", Endpoint: srv.URL + "/api", APIKey: "k",
	}, 5000, 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(qs) != 2 || qs[0] != "" || qs[1] != "*" {
		t.Fatalf("qs=%v want [\"\", \"*\"]", qs)
	}
	if len(hits) != 1 || hits[0].GUID != "star-1" {
		t.Fatalf("hits=%+v", hits)
	}
}

func TestSearchCatalog_429DoesNotRetryStar(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	c := &Client{HTTP: srv.Client(), Interval: time.Nanosecond}
	_, err := c.SearchCatalog(context.Background(), Indexer{
		Name: "newznab", Endpoint: srv.URL + "/api", APIKey: "k",
	}, 2000, 0, 50)
	if err == nil || !strings.Contains(err.Error(), "429") {
		t.Fatalf("err=%v", err)
	}
	if n.Load() != 1 {
		t.Fatalf("calls=%d want 1 (no q=* retry on 429)", n.Load())
	}
}

func TestSearchCatalog_Code300IsEmptyPage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `<?xml version="1.0"?><error code="300" description="No items found"/>`)
	}))
	defer srv.Close()
	c := &Client{HTTP: srv.Client(), Interval: time.Nanosecond}
	hits, err := c.SearchCatalog(context.Background(), Indexer{
		Name: "newznab", Endpoint: srv.URL + "/api", APIKey: "k",
	}, 2000, 0, 50)
	if err != nil || len(hits) != 0 {
		t.Fatalf("hits=%d err=%v", len(hits), err)
	}
}

func TestSearchCatalog_429HonorsRetryAfter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "5")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	c := &Client{HTTP: srv.Client(), Interval: time.Nanosecond}
	_, err := c.SearchCatalog(context.Background(), Indexer{
		Name: "newznab", Endpoint: srv.URL + "/api", APIKey: "k",
	}, 2000, 0, 50)
	if RetryAfter(err) != 5*time.Second {
		t.Fatalf("retry-after=%s err=%v", RetryAfter(err), err)
	}
}

func TestOverlapKey_PrefersIMDBThenTitleYear(t *testing.T) {
	if got := OverlapKey(CatalogHit{ImdbID: "111161"}); got != "imdb:tt111161" {
		t.Fatalf("imdb=%q", got)
	}
	if got := OverlapKey(CatalogHit{Title: "Some Movie 2021 1080p GRP"}); got != "title:some.movie.2021.1080p.grp:2021" {
		t.Fatalf("title=%q", got)
	}
}

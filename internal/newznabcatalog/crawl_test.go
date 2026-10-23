package newznabcatalog

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/labbersanon/sakms/internal/connections"
	"github.com/labbersanon/sakms/internal/dbtest"
	"github.com/labbersanon/sakms/internal/newznab"
	"github.com/labbersanon/sakms/internal/secrets"
)

func TestCrawlHost_PagesUntilEmptyAndDoesNotHitHTML(t *testing.T) {
	var pages atomic.Int32
	var sawHTML atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api" {
			sawHTML.Store(true)
			http.NotFound(w, r)
			return
		}
		n := int(pages.Add(1))
		io.WriteString(w, fmt.Sprintf(`<?xml version="1.0"?>
<rss version="2.0" xmlns:newznab="http://www.newznab.com/DTD/2007/feeds/attributes/">
<channel><item>
<title>Movie.%d.2021.1080p</title>
<guid>g-%d</guid>
<newznab:attr name="imdb" value="tt%d"/>
<newznab:attr name="category" value="2000"/>
</item></channel></rss>`, n, n, n))
	}))
	defer srv.Close()

	sqlDB := dbtest.New(t)
	st := NewStore(sqlDB)
	client := &newznab.Client{HTTP: srv.Client(), Interval: time.Nanosecond}
	idx := newznab.Indexer{Name: "newznab", Endpoint: srv.URL + "/api", APIKey: "k"}
	// Only crawl movies: mark series/adult complete first.
	ctx := context.Background()
	for _, kind := range []string{KindSeries, KindAdult} {
		if err := st.SaveCursor(ctx, Cursor{Indexer: "newznab", Kind: kind, Complete: true, UpdatedAt: 1}); err != nil {
			t.Fatal(err)
		}
	}
	crawlHost(ctx, st, client, idx)
	if sawHTML.Load() {
		t.Fatal("html")
	}
	cur, err := st.Cursor(ctx, "newznab", KindMovies)
	if err != nil || !cur.Complete || cur.PageOffset != 1 {
		t.Fatalf("cursor=%+v err=%v", cur, err)
	}
	rep, err := st.Report(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var nzbs int
	for _, c := range rep.Counts {
		if c.Indexer == "newznab" && c.Kind == KindMovies {
			nzbs = c.NZBs
		}
	}
	if nzbs != 1 {
		t.Fatalf("nzbs=%d report=%+v", nzbs, rep.Counts)
	}
}

func TestIndexersFromStore_SkipsEmpty(t *testing.T) {
	sqlDB := dbtest.New(t)
	sec, err := secrets.New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	conns := connections.New(sqlDB, sec)
	ctx := context.Background()
	if err := conns.Upsert(ctx, "newznab", "http://example.test/api", "key"); err != nil {
		t.Fatal(err)
	}
	got := indexersFromStore(ctx, conns)
	if len(got) != 1 || got[0].Name != "newznab" {
		t.Fatalf("got=%+v", got)
	}
}

func TestInterval_IsFiveSecondsPerHost(t *testing.T) {
	if Interval != 5*time.Second {
		t.Fatalf("Interval=%s want 5s", Interval)
	}
}

func TestIsRateLimit(t *testing.T) {
	if !isRateLimit(fmt.Errorf("host returned status 429")) {
		t.Fatal("429")
	}
	if isRateLimit(fmt.Errorf("search xml")) {
		t.Fatal("xml")
	}
}

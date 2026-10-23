package api

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/labbersanon/sakms/internal/newznab"
	"github.com/labbersanon/sakms/internal/prowlarr"
)

func grabSearchRSS(title, guid, nzb string) string {
	return `<?xml version="1.0"?>
<rss version="2.0" xmlns:newznab="http://www.newznab.com/DTD/2007/feeds/attributes/">
<channel><item>
<title>` + title + `</title>
<guid>` + guid + `</guid>
<enclosure url="` + nzb + `" length="999" type="application/x-nzb"/>
<newznab:attr name="size" value="999"/>
</item></channel></rss>`
}

func attachGrabSearch(t *testing.T, interval time.Duration, hosts ...newznab.Indexer) {
	t.Helper()
	connStore, _, _, _, _, _, _, _, _, _ := testStores(t)
	for _, h := range hosts {
		if err := connStore.Upsert(context.Background(), h.Name, h.Endpoint, h.APIKey); err != nil {
			t.Fatal(err)
		}
	}
	AttachNewznabGrabSearch(connStore, &http.Client{Timeout: 5 * time.Second})
	t.Cleanup(func() { AttachNewznabGrabSearch(nil, nil) })
	if c := newznabGrabClient; c != nil {
		c.Interval = interval
	}
}

func TestInsertNewznabPhase(t *testing.T) {
	phases := insertNewznabPhase(nil)
	if len(phases) != 1 || !phases[0].IsNewznab() {
		t.Fatalf("empty: %+v", phases)
	}
	phases = insertNewznabPhase([]prowlarr.Scope{prowlarr.ScopeAll})
	if len(phases) != 2 || !phases[0].IsNewznab() || phases[1] != prowlarr.ScopeAll {
		t.Fatalf("before all: %+v", phases)
	}
	if len(insertNewznabPhase(phases)) != 2 {
		t.Fatal("idempotent")
	}
	rest := insertNewznabPhase([]prowlarr.Scope{prowlarr.ScopeUsenet, prowlarr.ScopeTorrent})
	if len(rest) != 3 || !rest[0].IsNewznab() || rest[1] != prowlarr.ScopeUsenet || rest[2] != prowlarr.ScopeTorrent {
		t.Fatalf("prowlarr last: %+v", rest)
	}
}

func TestMergeGrabReleases_ProwlarrLast(t *testing.T) {
	out := mergeGrabReleases(
		[]prowlarr.Release{{Title: "nzb", Indexer: "newznab"}},
		[]prowlarr.Release{{Title: "tor", Indexer: "prowlarr"}},
	)
	if len(out) != 2 || out[0].Indexer != "newznab" || out[1].Indexer != "prowlarr" {
		t.Fatalf("order=%+v", out)
	}
}

func TestNewznabGrabSearch_ConfiguredHostNoHTML(t *testing.T) {
	var sawHTML, fetchedNZB bool
	var nzbURL string
	idx := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
		if r.URL.Query().Get("t") != "search" || r.URL.Query().Get("q") != "Grab Movie" {
			t.Errorf("t=%s q=%s", r.URL.Query().Get("t"), r.URL.Query().Get("q"))
		}
		io.WriteString(w, grabSearchRSS("Grab.Movie.2021.1080p.WEB-DL.x264-GRP", "nzbg-1", nzbURL))
	}))
	defer idx.Close()
	nzbURL = idx.URL + "/getnzb"

	attachGrabSearch(t, time.Nanosecond, newznab.Indexer{
		Name: "newznab", Endpoint: idx.URL + "/api", APIKey: "idx-key",
	})

	rels, err := newznabGrabSearch(context.Background(), "Grab Movie", []int{2000})
	if err != nil {
		t.Fatal(err)
	}
	if len(rels) != 1 || rels[0].Protocol != prowlarr.Usenet || rels[0].DownloadURL == "" {
		t.Fatalf("rels=%+v", rels)
	}
	if sawHTML || fetchedNZB {
		t.Fatalf("html=%v nzb=%v", sawHTML, fetchedNZB)
	}
}

func TestNewznabGrabSearch_SecondHostOnly(t *testing.T) {
	var nzbURL string
	idx := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("t") != "search" {
			t.Errorf("t=%s", r.URL.Query().Get("t"))
		}
		io.WriteString(w, grabSearchRSS("Grab.Movie.2021.1080p.WEB-DL.x264-GRP", "nzbg-2", nzbURL))
	}))
	defer idx.Close()
	nzbURL = idx.URL + "/getnzb"

	attachGrabSearch(t, time.Nanosecond, newznab.Indexer{
		Name: "newznab2", Endpoint: idx.URL + "/api", APIKey: "idx2-key",
	})

	rels, err := newznabGrabSearch(context.Background(), "Grab Movie", []int{2000})
	if err != nil {
		t.Fatal(err)
	}
	if len(rels) != 1 || rels[0].Indexer != "newznab2" {
		t.Fatalf("rels=%+v", rels)
	}
}

func TestNewznabGrabSearch_MergesBothHosts(t *testing.T) {
	a := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, grabSearchRSS("Grab.Movie.2021.1080p.WEB-DL.x264-AAA", "a", "http://a/nzb"))
	}))
	defer a.Close()
	b := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, grabSearchRSS("Grab.Movie.2021.1080p.WEB-DL.x264-BBB", "b", "http://b/nzb"))
	}))
	defer b.Close()

	attachGrabSearch(t, time.Nanosecond,
		newznab.Indexer{Name: "newznab", Endpoint: a.URL + "/api", APIKey: "a-key"},
		newznab.Indexer{Name: "newznab2", Endpoint: b.URL + "/api", APIKey: "b-key"},
	)

	rels, err := newznabGrabSearch(context.Background(), "Grab Movie", []int{2000})
	if err != nil {
		t.Fatal(err)
	}
	if len(rels) != 2 {
		t.Fatalf("rels=%+v", rels)
	}
	if rels[0].Indexer != "newznab" || rels[1].Indexer != "newznab2" {
		t.Fatalf("order=%+v", rels)
	}
}

func TestNewznabGrabSearch_QueriesBothHostsInParallel(t *testing.T) {
	var entered atomic.Int32
	gate := make(chan struct{})
	mk := func(title, guid, nzb string) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			entered.Add(1)
			<-gate
			io.WriteString(w, grabSearchRSS(title, guid, nzb))
		})
	}
	a := httptest.NewServer(mk("Grab.Movie.2021.1080p.WEB-DL.x264-AAA", "a", "http://a/nzb"))
	defer a.Close()
	b := httptest.NewServer(mk("Grab.Movie.2021.1080p.WEB-DL.x264-BBB", "b", "http://b/nzb"))
	defer b.Close()

	attachGrabSearch(t, 3*time.Second,
		newznab.Indexer{Name: "newznab", Endpoint: a.URL + "/api", APIKey: "a-key"},
		newznab.Indexer{Name: "newznab2", Endpoint: b.URL + "/api", APIKey: "b-key"},
	)

	type result struct {
		rels []prowlarr.Release
		err  error
	}
	got := make(chan result, 1)
	go func() {
		rels, err := newznabGrabSearch(context.Background(), "Grab Movie", []int{2000})
		got <- result{rels, err}
	}()
	deadline := time.Now().Add(400 * time.Millisecond)
	for entered.Load() < 2 {
		if time.Now().After(deadline) {
			t.Fatalf("entered=%d — hosts queried sequentially", entered.Load())
		}
		time.Sleep(5 * time.Millisecond)
	}
	close(gate)
	out := <-got
	if out.err != nil {
		t.Fatal(out.err)
	}
	if len(out.rels) != 2 {
		t.Fatalf("rels=%+v", out.rels)
	}
	if out.rels[0].Indexer != "newznab" || out.rels[1].Indexer != "newznab2" {
		t.Fatalf("order=%+v", out.rels)
	}
}

func TestNewznabGrabSearch_OneHostErrorKeepsTheOther(t *testing.T) {
	a := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusInternalServerError)
	}))
	defer a.Close()
	b := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, grabSearchRSS("Grab.Movie.2021.1080p.WEB-DL.x264-BBB", "b", "http://b/nzb"))
	}))
	defer b.Close()

	attachGrabSearch(t, time.Nanosecond,
		newznab.Indexer{Name: "newznab", Endpoint: a.URL + "/api", APIKey: "a-key"},
		newznab.Indexer{Name: "newznab2", Endpoint: b.URL + "/api", APIKey: "b-key"},
	)

	rels, err := newznabGrabSearch(context.Background(), "Grab Movie", []int{2000})
	if err != nil {
		t.Fatal(err)
	}
	if len(rels) != 1 || rels[0].Indexer != "newznab2" {
		t.Fatalf("rels=%+v", rels)
	}
}

func TestAdaptNewznabReleases_SkipsEmptyURL(t *testing.T) {
	out := adaptNewznabReleases([]newznab.Release{
		{Title: "No.URL.2021", DownloadURL: ""},
		{Title: "Ok.2021.1080p", DownloadURL: "http://x/nzb", Size: 10, Indexer: "NZBGeek"},
	})
	if len(out) != 1 || out[0].Indexer != "NZBGeek" || out[0].Protocol != prowlarr.Usenet {
		t.Fatalf("adapted=%+v", out)
	}
}

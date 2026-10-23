package api

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/labbersanon/sakms/internal/newznab"
	"github.com/labbersanon/sakms/internal/prowlarr"
)

func TestInsertNewznabPhase(t *testing.T) {
	phases := insertNewznabPhase(nil)
	if len(phases) != 1 || !phases[0].IsNewznab() {
		t.Fatalf("empty: %+v", phases)
	}
	phases = insertNewznabPhase([]prowlarr.Scope{prowlarr.ScopeNative, prowlarr.ScopeAll})
	if len(phases) != 3 || !phases[0].IsNative() || !phases[1].IsNewznab() || phases[2] != prowlarr.ScopeAll {
		t.Fatalf("after native: %+v", phases)
	}
	if len(insertNewznabPhase(phases)) != 3 {
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
		io.WriteString(w, `<?xml version="1.0"?>
<rss version="2.0" xmlns:newznab="http://www.newznab.com/DTD/2007/feeds/attributes/">
<channel><item>
<title>Grab.Movie.2021.1080p.WEB-DL.x264-GRP</title>
<guid>nzbg-1</guid>
<enclosure url="`+nzbURL+`" length="999" type="application/x-nzb"/>
<newznab:attr name="size" value="999"/>
</item></channel></rss>`)
	}))
	defer idx.Close()
	nzbURL = idx.URL + "/getnzb"

	connStore, _, _, _, _, _, _, _, _, _ := testStores(t)
	if err := connStore.Upsert(context.Background(), "newznab", idx.URL+"/api", "idx-key"); err != nil {
		t.Fatal(err)
	}
	AttachNewznabGrabSearch(connStore, &http.Client{Timeout: 5 * time.Second})
	t.Cleanup(func() { AttachNewznabGrabSearch(nil, nil) })
	if c := newznabGrabClient; c != nil {
		c.Interval = time.Nanosecond
	}

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
		io.WriteString(w, `<?xml version="1.0"?>
<rss version="2.0" xmlns:newznab="http://www.newznab.com/DTD/2007/feeds/attributes/">
<channel><item>
<title>Grab.Movie.2021.1080p.WEB-DL.x264-GRP</title>
<guid>nzbg-2</guid>
<enclosure url="`+nzbURL+`" length="999" type="application/x-nzb"/>
<newznab:attr name="size" value="999"/>
</item></channel></rss>`)
	}))
	defer idx.Close()
	nzbURL = idx.URL + "/getnzb"

	connStore, _, _, _, _, _, _, _, _, _ := testStores(t)
	if err := connStore.Upsert(context.Background(), "newznab2", idx.URL+"/api", "idx2-key"); err != nil {
		t.Fatal(err)
	}
	AttachNewznabGrabSearch(connStore, &http.Client{Timeout: 5 * time.Second})
	t.Cleanup(func() { AttachNewznabGrabSearch(nil, nil) })
	if c := newznabGrabClient; c != nil {
		c.Interval = time.Nanosecond
	}

	rels, err := newznabGrabSearch(context.Background(), "Grab Movie", []int{2000})
	if err != nil {
		t.Fatal(err)
	}
	if len(rels) != 1 || rels[0].Indexer != "newznab2" {
		t.Fatalf("rels=%+v", rels)
	}
}

func TestNewznabGrabSearch_MergesBothHosts(t *testing.T) {
	rss := func(title, guid, nzb string) string {
		return `<?xml version="1.0"?>
<rss version="2.0" xmlns:newznab="http://www.newznab.com/DTD/2007/feeds/attributes/">
<channel><item>
<title>` + title + `</title>
<guid>` + guid + `</guid>
<enclosure url="` + nzb + `" length="999" type="application/x-nzb"/>
<newznab:attr name="size" value="999"/>
</item></channel></rss>`
	}
	a := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, rss("Grab.Movie.2021.1080p.WEB-DL.x264-AAA", "a", "http://a/nzb"))
	}))
	defer a.Close()
	b := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, rss("Grab.Movie.2021.1080p.WEB-DL.x264-BBB", "b", "http://b/nzb"))
	}))
	defer b.Close()

	connStore, _, _, _, _, _, _, _, _, _ := testStores(t)
	if err := connStore.Upsert(context.Background(), "newznab", a.URL+"/api", "a-key"); err != nil {
		t.Fatal(err)
	}
	if err := connStore.Upsert(context.Background(), "newznab2", b.URL+"/api", "b-key"); err != nil {
		t.Fatal(err)
	}
	AttachNewznabGrabSearch(connStore, &http.Client{Timeout: 5 * time.Second})
	t.Cleanup(func() { AttachNewznabGrabSearch(nil, nil) })
	if c := newznabGrabClient; c != nil {
		c.Interval = time.Nanosecond
	}

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

func TestAdaptNewznabReleases_SkipsEmptyURL(t *testing.T) {
	out := adaptNewznabReleases([]newznab.Release{
		{Title: "No.URL.2021", DownloadURL: ""},
		{Title: "Ok.2021.1080p", DownloadURL: "http://x/nzb", Size: 10, Indexer: "NZBGeek"},
	})
	if len(out) != 1 || out[0].Indexer != "NZBGeek" || out[0].Protocol != prowlarr.Usenet {
		t.Fatalf("adapted=%+v", out)
	}
}

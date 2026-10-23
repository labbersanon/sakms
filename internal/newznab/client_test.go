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

func TestLookup_SearchGetNZBPasswordNoHTML(t *testing.T) {
	const stem = "o6938TxyzABCDEF"
	var paths []string
	mux := http.NewServeMux()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path+"?"+r.URL.Query().Get("t"))
		if strings.Contains(r.URL.Path, "login") || strings.Contains(r.URL.Path, "details") {
			t.Errorf("HTML scrape path %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		mux.ServeHTTP(w, r)
	}))
	defer srv.Close()

	nzbURL := srv.URL + "/getnzb"
	mux.HandleFunc("/api", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("apikey") != "idx-key" {
			t.Error("missing apikey")
		}
		switch r.URL.Query().Get("t") {
		case "search":
			if r.URL.Query().Get("q") != stem {
				t.Errorf("q=%s", r.URL.Query().Get("q"))
			}
			w.Header().Set("Content-Type", "application/xml")
			io.WriteString(w, `<?xml version="1.0"?>
<rss version="2.0" xmlns:newznab="http://www.newznab.com/DTD/2007/feeds/attributes/">
  <channel>
    <item>
      <title>Some.Show.S01E01.1080p.WEB-DL.x264-GRP {{hunter2}}</title>
      <guid>abc123</guid>
      <link>`+nzbURL+`</link>
      <enclosure url="`+nzbURL+`" length="10" type="application/x-nzb"/>
      <newznab:attr name="guid" value="abc123"/>
      <newznab:attr name="password" value="hunter2"/>
    </item>
  </channel>
</rss>`)
		default:
			http.NotFound(w, r)
		}
	})
	mux.HandleFunc("/getnzb", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-DNZB-Name", "Some.Show.S01E01.1080p.WEB-DL.x264-GRP")
		w.Header().Set("X-DNZB-Password", "hunter2")
		io.WriteString(w, `<?xml version="1.0"?><nzb>
<head><meta type="title">Some.Show.S01E01.1080p.WEB-DL.x264-GRP</meta>
<meta type="password">hunter2</meta></head>
<file poster="p" date="1" subject="`+stem+`.7z.001 yEnc (1/1)">
<groups><group>alt.binaries.test</group></groups>
<segments><segment bytes="1" number="1">mid@t</segment></segments>
</file></nzb>`)
	})

	c := &Client{HTTP: srv.Client(), Interval: time.Nanosecond}
	hit, ok := c.Lookup(context.Background(), []Indexer{{
		Name: "NZBGeek", Endpoint: srv.URL + "/api", APIKey: "idx-key",
	}}, Query{Filename: stem})
	if !ok {
		t.Fatalf("miss paths=%v", paths)
	}
	if hit.Title == "" || len(hit.Passwords) == 0 || hit.Passwords[0] != "hunter2" {
		t.Fatalf("hit=%+v", hit)
	}
	for _, p := range paths {
		if strings.Contains(p, "login") || strings.Contains(p, "details") {
			t.Fatalf("scraped %s", p)
		}
	}
}

func TestPickItems_RequiresStemOrSingleHit(t *testing.T) {
	items := []rssItem{
		{Title: "Other.Show.2021.1080p-GRP"},
		{Title: "Another.Show.2022.1080p-GRP"},
	}
	needles := []string{"o6938Txyz"}
	if got := pickItems(items, needles, true); got != nil {
		t.Fatalf("unmatched multi-hit must not download: %+v", got)
	}
	one := []rssItem{{Title: "Renamed.Show.2021.1080p-GRP"}}
	if got := pickItems(one, needles, true); len(got) != 1 {
		t.Fatalf("single hit: %+v", got)
	}
	if got := pickItems(items, needles, false); got != nil {
		t.Fatalf("poster browse unmatched must not download: %+v", got)
	}
	matched := []rssItem{
		{Title: "Renamed.Show.2021.1080p-GRP", Description: "o6938Txyz.7z"},
		{Title: "Unrelated.2020.1080p-GRP"},
	}
	got := pickItems(matched, needles, true)
	if len(got) != 1 || !strings.Contains(got[0].Description, "o6938Txyz") {
		t.Fatalf("matched: %+v", got)
	}
}

func TestPickItems_PosterAttr(t *testing.T) {
	const poster = "AbCdEfGhIjKlM@ngPost.com"
	const want = "Wanted.Show.S01E01.1080p.WEB-DL.x264-GRP"
	latest := rssItem{Title: "Latest.Show.2026.1080p-GRP"}
	cases := []struct {
		name string
		item rssItem
	}{
		{"2007", rssItem{Title: want, Attrs: []rssAttr{{Name: "poster", Value: poster}}}},
		{"2010", rssItem{Title: want, Attrs2010: []rssAttr{{Name: "poster", Value: poster}}}},
	}
	for _, tc := range cases {
		got := pickItems([]rssItem{latest, tc.item}, []string{"hashstem", poster}, false)
		if len(got) != 1 || got[0].Title != want {
			t.Fatalf("%s: %+v", tc.name, got)
		}
	}
}

func TestLookup_EmptyQuery(t *testing.T) {
	c := &Client{Interval: time.Nanosecond}
	if _, ok := c.Lookup(context.Background(), []Indexer{{Name: "x", Endpoint: "http://127.0.0.1", APIKey: "k"}}, Query{}); ok {
		t.Fatal("empty query")
	}
}

func TestLookup_PosterAfterStemMissNoHTML(t *testing.T) {
	const stem = "o6938TxyzABCDEF"
	const poster = "AbCdEfGhIjKlM@ngPost.com"
	var qs, posters []string
	var sawHTML bool
	mux := http.NewServeMux()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "login") || strings.Contains(r.URL.Path, "geekseek") || strings.Contains(r.URL.Path, "details") {
			sawHTML = true
			http.NotFound(w, r)
			return
		}
		mux.ServeHTTP(w, r)
	}))
	defer srv.Close()
	nzbURL := srv.URL + "/getnzb"
	mux.HandleFunc("/api", func(w http.ResponseWriter, r *http.Request) {
		qs = append(qs, r.URL.Query().Get("q"))
		posters = append(posters, r.URL.Query().Get("poster"))
		switch {
		case r.URL.Query().Get("poster") == poster:
			io.WriteString(w, `<?xml version="1.0"?>
<rss version="2.0" xmlns:newznab="http://www.newznab.com/DTD/2007/feeds/attributes/">
<channel><item>
<title>Adapter.Show.S01E01.1080p.WEB-DL.x264-GRP</title>
<link>`+nzbURL+`</link>
<enclosure url="`+nzbURL+`" length="10" type="application/x-nzb"/>
<newznab:attr name="poster" value="`+poster+`"/>
<newznab:attr name="password" value="hunter2"/>
</item></channel></rss>`)
		case r.URL.Query().Get("t") == "search":
			io.WriteString(w, `<?xml version="1.0"?><rss version="2.0"><channel></channel></rss>`)
		default:
			http.NotFound(w, r)
		}
	})
	mux.HandleFunc("/getnzb", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-DNZB-Password", "hunter2")
		io.WriteString(w, `<?xml version="1.0"?><nzb><head>
<meta type="password">hunter2</meta></head>
<file poster="p" date="1" subject="`+stem+`.7z.001">
<groups><group>a</group></groups>
<segments><segment bytes="1" number="1">m@t</segment></segments>
</file></nzb>`)
	})
	c := &Client{HTTP: srv.Client(), Interval: time.Nanosecond}
	hit, ok := c.Lookup(context.Background(), []Indexer{{
		Name: "NZBGeek", Endpoint: srv.URL + "/api", APIKey: "idx-key",
	}}, Query{Filename: stem, Poster: poster})
	if !ok {
		t.Fatalf("miss q=%v poster=%v", qs, posters)
	}
	if hit.Title == "" || len(hit.Passwords) == 0 {
		t.Fatalf("hit=%+v", hit)
	}
	if sawHTML {
		t.Fatal("HTML scrape")
	}
	var sawPoster bool
	for _, p := range posters {
		if p == poster {
			sawPoster = true
		}
	}
	if !sawPoster {
		t.Fatalf("poster= not sent: %v", posters)
	}
}

func TestNormalizeEndpoint(t *testing.T) {
	cases := []struct{ in, want string }{
		{"https://api.nzbgeek.info", "https://api.nzbgeek.info/api"},
		{"https://api.nzbgeek.info/", "https://api.nzbgeek.info/api"},
		{"https://api.nzbgeek.info/api", "https://api.nzbgeek.info/api"},
		{"https://api.nzbgeek.info/api?apikey=secret", "https://api.nzbgeek.info/api"},
		{"http://prowlarr:9696/5/", "http://prowlarr:9696/5"},
		{"", ""},
	}
	for _, tc := range cases {
		if got := NormalizeEndpoint(tc.in); got != tc.want {
			t.Errorf("NormalizeEndpoint(%q)=%q want %q", tc.in, got, tc.want)
		}
	}
}

func TestCaps_SuccessAndErrorXML(t *testing.T) {
	var sawHTML bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "login") || strings.Contains(r.URL.Path, "details") {
			sawHTML = true
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("t") != "caps" {
			t.Errorf("t=%s", r.URL.Query().Get("t"))
		}
		if r.URL.Query().Get("apikey") != "idx-key" {
			t.Error("missing apikey")
		}
		io.WriteString(w, `<?xml version="1.0"?><caps><server version="0.1" title="Test"/></caps>`)
	}))
	defer srv.Close()
	c := &Client{HTTP: srv.Client(), Interval: time.Nanosecond}
	if err := c.Caps(context.Background(), Indexer{Endpoint: srv.URL, APIKey: "idx-key"}); err != nil {
		t.Fatal(err)
	}
	if sawHTML {
		t.Fatal("HTML scrape")
	}

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `<?xml version="1.0"?><error code="100" description="Incorrect API key"/>`)
	}))
	defer bad.Close()
	err := c.Caps(context.Background(), Indexer{Endpoint: bad.URL, APIKey: "bad"})
	if err == nil || !strings.Contains(err.Error(), "Incorrect API key") {
		t.Fatalf("error xml: %v", err)
	}
	if strings.Contains(err.Error(), "bad") {
		t.Fatalf("leaked key: %v", err)
	}
}

func TestLookup_BrowseGroupSizeMatchesNZBStem(t *testing.T) {
	const stem = "o6938TxyzABCDEF"
	const poster = "AbCdEfGhIjKlM@ngPost.com"
	const group = "alt.binaries.teevee"
	const payload int64 = 1_000_000
	posted := time.Now().Unix() - 3600
	var gets, browseN, sawHTML int
	var sawGroup, sawMaxage bool
	mux := http.NewServeMux()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "login") || strings.Contains(r.URL.Path, "geekseek") || strings.Contains(r.URL.Path, "details") {
			sawHTML++
			http.NotFound(w, r)
			return
		}
		mux.ServeHTTP(w, r)
	}))
	defer srv.Close()
	nzbURL := srv.URL + "/getnzb"
	when := time.Now().UTC().Format(time.RFC1123Z)
	mux.HandleFunc("/api", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("t") == "caps" {
			io.WriteString(w, `<?xml version="1.0"?><caps><groups>
<group name="alt.binaries.teevee"/>
</groups></caps>`)
			return
		}
		if r.URL.Query().Get("t") != "search" {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("group") == group && r.URL.Query().Get("maxage") != "" && r.URL.Query().Get("q") == "" {
			sawGroup, sawMaxage = true, true
			browseN++
			io.WriteString(w, `<?xml version="1.0"?>
<rss version="2.0" xmlns:newznab="http://www.newznab.com/DTD/2010/feeds/attributes/">
<channel>
<item>
<title>Wrong.Size.Show.2026.1080p-GRP</title>
<link>`+nzbURL+`?id=wrong</link>
<enclosure url="`+nzbURL+`?id=wrong" length="10" type="application/x-nzb"/>
<newznab:attr name="size" value="5000000"/>
<newznab:attr name="usenetdate" value="`+when+`"/>
<newznab:attr name="password" value="0"/>
</item>
<item>
<title>Wanted.Show.S01E01.1080p.WEB-DL.x264-GRP</title>
<link>`+nzbURL+`?id=want</link>
<enclosure url="`+nzbURL+`?id=want" length="10" type="application/x-nzb"/>
<newznab:attr name="size" value="1000000"/>
<newznab:attr name="usenetdate" value="`+when+`"/>
<newznab:attr name="password" value="0"/>
</item>
</channel></rss>`)
			return
		}
		io.WriteString(w, `<?xml version="1.0"?><rss version="2.0"><channel></channel></rss>`)
	})
	mux.HandleFunc("/getnzb", func(w http.ResponseWriter, r *http.Request) {
		gets++
		if r.URL.Query().Get("id") != "want" {
			t.Errorf("fetched non-candidate NZB id=%s", r.URL.Query().Get("id"))
		}
		w.Header().Set("X-DNZB-Password", "hunter2")
		w.Header().Set("X-DNZB-Name", "Wanted.Show.S01E01.1080p.WEB-DL.x264-GRP")
		io.WriteString(w, `<?xml version="1.0"?><nzb><head>
<meta type="password">hunter2</meta></head>
<file poster="p" date="1" subject="`+stem+`.7z.001 yEnc (1/1)">
<groups><group>`+group+`</group></groups>
<segments><segment bytes="1" number="1">m@t</segment></segments>
</file></nzb>`)
	})
	c := &Client{HTTP: srv.Client(), Interval: time.Nanosecond}
	hit, ok := c.Lookup(context.Background(), []Indexer{{
		Name: "NZBGeek", Endpoint: srv.URL + "/api", APIKey: "idx-key",
	}}, Query{Filename: stem, Poster: poster, Group: group, PostedAt: posted, SizeBytes: payload})
	if !ok {
		t.Fatalf("browse miss gets=%d browseN=%d group=%t maxage=%t", gets, browseN, sawGroup, sawMaxage)
	}
	if hit.Title == "" || len(hit.Passwords) == 0 || hit.Passwords[0] != "hunter2" {
		t.Fatalf("hit=%+v", hit)
	}
	if gets != 1 {
		t.Fatalf("gets=%d want 1 (size filter)", gets)
	}
	if sawHTML > 0 {
		t.Fatal("HTML scrape")
	}
}

func TestLookup_SkipBrowseWhenGroupNotInCaps(t *testing.T) {
	var browse, gets int
	mux := http.NewServeMux()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "login") || strings.Contains(r.URL.Path, "geekseek") {
			t.Errorf("HTML scrape path %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		mux.ServeHTTP(w, r)
	}))
	defer srv.Close()
	mux.HandleFunc("/api", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("t") {
		case "caps":
			io.WriteString(w, `<?xml version="1.0"?><caps><groups>
<group name="alt.binaries.usenet"/>
</groups></caps>`)
		case "search":
			if r.URL.Query().Get("group") != "" && r.URL.Query().Get("q") == "" && r.URL.Query().Get("poster") == "" {
				browse++
			}
			io.WriteString(w, `<?xml version="1.0"?><rss version="2.0"><channel></channel></rss>`)
		case "get":
			gets++
			http.NotFound(w, r)
		default:
			http.NotFound(w, r)
		}
	})
	c := &Client{HTTP: srv.Client(), Interval: time.Nanosecond}
	_, ok := c.Lookup(context.Background(), []Indexer{{
		Name: "NZBGeek", Endpoint: srv.URL + "/api", APIKey: "idx-key",
	}}, Query{
		Filename:  "o6938TxyzABCDEF",
		Poster:    "AbCdEfGhIjKlM@ngPost.com",
		Group:     "alt.binaries.x264",
		PostedAt:  time.Now().Unix() - 3600,
		SizeBytes: 15_000_000_000,
	})
	if ok {
		t.Fatal("unlisted group must miss")
	}
	if browse != 0 {
		t.Fatalf("browse=%d want 0 (caps omits alt.binaries.x264)", browse)
	}
	if gets != 0 {
		t.Fatalf("gets=%d want 0", gets)
	}
}

func TestGroupInCaps(t *testing.T) {
	listed := capsInfo{Groups: []string{"alt.binaries.usenet"}}
	if groupInCaps(listed, "alt.binaries.x264") {
		t.Fatal("x264 not listed")
	}
	if !groupInCaps(listed, "alt.binaries.usenet") {
		t.Fatal("usenet listed")
	}
	if !groupInCaps(capsInfo{}, "alt.binaries.x264") {
		t.Fatal("empty caps groups still allow browse")
	}
}

func TestSearchGrab_ReturnsEnclosureWithoutFetchingNZB(t *testing.T) {
	var gotQ, gotCat, gotT string
	var fetchedNZB bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "login") || strings.Contains(r.URL.Path, "details") {
			t.Errorf("HTML scrape path %s", r.URL.Path)
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
		w.Header().Set("Content-Type", "application/xml")
		io.WriteString(w, `<?xml version="1.0"?>
<rss version="2.0" xmlns:newznab="http://www.newznab.com/DTD/2007/feeds/attributes/">
  <channel>
    <item>
      <title>Grab.Movie.2021.1080p.WEB-DL.x264-GRP</title>
      <guid>nzbg-1</guid>
      <pubDate>Wed, 21 Oct 2026 00:00:00 +0000</pubDate>
      <enclosure url="http://example.test/getnzb?id=nzbg-1" length="12345" type="application/x-nzb"/>
      <newznab:attr name="size" value="12345"/>
      <newznab:attr name="category" value="2000"/>
    </item>
  </channel>
</rss>`)
	}))
	defer srv.Close()

	c := &Client{HTTP: srv.Client(), Interval: time.Nanosecond}
	rels, err := c.SearchGrab(context.Background(), Indexer{
		Name: "NZBGeek", Endpoint: srv.URL + "/api", APIKey: "idx-key",
	}, "Grab Movie", []int{2000})
	if err != nil {
		t.Fatal(err)
	}
	if gotT != "search" || gotQ != "Grab Movie" || gotCat != "2000" {
		t.Fatalf("wire t=%q q=%q cat=%q", gotT, gotQ, gotCat)
	}
	if fetchedNZB {
		t.Fatal("grab search must not fetch the NZB")
	}
	if len(rels) != 1 || rels[0].Title == "" || rels[0].DownloadURL == "" || rels[0].Size != 12345 {
		t.Fatalf("rels=%+v", rels)
	}
}

func TestGet_TwoHostsAreNotSerializedByThrottle(t *testing.T) {
	const rss = `<?xml version="1.0"?><rss version="2.0"><channel></channel></rss>`
	var aN, bN atomic.Int32
	gate := make(chan struct{})
	mk := func(n *atomic.Int32) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			n.Add(1)
			<-gate
			io.WriteString(w, rss)
		})
	}
	a := httptest.NewServer(mk(&aN))
	defer a.Close()
	b := httptest.NewServer(mk(&bN))
	defer b.Close()
	c := &Client{HTTP: http.DefaultClient, Interval: 3 * time.Second}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	errc := make(chan error, 2)
	go func() {
		_, err := c.SearchGrab(ctx, Indexer{Name: "a", Endpoint: a.URL + "/api", APIKey: "k"}, "Grab Movie", nil)
		errc <- err
	}()
	go func() {
		_, err := c.SearchGrab(ctx, Indexer{Name: "b", Endpoint: b.URL + "/api", APIKey: "k"}, "Grab Movie", nil)
		errc <- err
	}()
	deadline := time.Now().Add(400 * time.Millisecond)
	for aN.Load() < 1 || bN.Load() < 1 {
		if time.Now().After(deadline) {
			t.Fatalf("a=%d b=%d — throttle still process-wide", aN.Load(), bN.Load())
		}
		time.Sleep(5 * time.Millisecond)
	}
	close(gate)
	for i := 0; i < 2; i++ {
		if err := <-errc; err != nil {
			t.Fatal(err)
		}
	}
}

func TestUnpackPasswordAttr_FlagsIgnored(t *testing.T) {
	if unpackPasswordAttr("0") != "" || unpackPasswordAttr("-1") != "" || unpackPasswordAttr("1") != "" {
		t.Fatal("flags")
	}
	if unpackPasswordAttr("hunter2") != "hunter2" {
		t.Fatal("real")
	}
}

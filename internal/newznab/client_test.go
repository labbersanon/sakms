package newznab

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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
	}}, stem)
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
	if got := pickItems(items, "o6938Txyz"); got != nil {
		t.Fatalf("unmatched multi-hit must not download: %+v", got)
	}
	one := []rssItem{{Title: "Renamed.Show.2021.1080p-GRP"}}
	if got := pickItems(one, "o6938Txyz"); len(got) != 1 {
		t.Fatalf("single hit: %+v", got)
	}
	matched := []rssItem{
		{Title: "Renamed.Show.2021.1080p-GRP", Description: "o6938Txyz.7z"},
		{Title: "Unrelated.2020.1080p-GRP"},
	}
	got := pickItems(matched, "o6938Txyz")
	if len(got) != 1 || !strings.Contains(got[0].Description, "o6938Txyz") {
		t.Fatalf("matched: %+v", got)
	}
}

func TestLookup_EmptyQuery(t *testing.T) {
	c := &Client{Interval: time.Nanosecond}
	if _, ok := c.Lookup(context.Background(), []Indexer{{Name: "x", Endpoint: "http://127.0.0.1", APIKey: "k"}}, ""); ok {
		t.Fatal("empty filename")
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

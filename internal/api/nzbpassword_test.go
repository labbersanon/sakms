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
	"github.com/labbersanon/sakms/internal/usenetsearch"
)

func TestNZBPasswordLookup_ProwlarrNewznabNoHTML(t *testing.T) {
	const stem = "o6938TxyzABCDEF"
	var sawHTML bool
	var nzbURL string
	idx := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "login") || strings.Contains(r.URL.Path, "details") {
			sawHTML = true
			http.NotFound(w, r)
			return
		}
		switch {
		case r.URL.Query().Get("t") == "search":
			if r.URL.Query().Get("q") != stem {
				t.Errorf("q=%s", r.URL.Query().Get("q"))
			}
			io.WriteString(w, `<?xml version="1.0"?>
<rss version="2.0" xmlns:newznab="http://www.newznab.com/DTD/2007/feeds/attributes/">
<channel><item>
<title>Adapter.Show.S01E01.1080p.WEB-DL.x264-GRP</title>
<link>`+nzbURL+`</link>
<enclosure url="`+nzbURL+`" length="10" type="application/x-nzb"/>
<newznab:attr name="password" value="from-attr"/>
</item></channel></rss>`)
		case r.URL.Path == "/getnzb":
			w.Header().Set("X-DNZB-Password", "from-header")
			w.Header().Set("X-DNZB-Name", "Adapter.Show.S01E01.1080p.WEB-DL.x264-GRP")
			io.WriteString(w, `<?xml version="1.0"?><nzb><head>
<meta type="password">from-header</meta></head>
<file poster="p" date="1" subject="`+stem+`.7z.001">
<groups><group>a</group></groups>
<segments><segment bytes="1" number="1">m@t</segment></segments>
</file></nzb>`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer idx.Close()
	nzbURL = idx.URL + "/getnzb"

	prow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/indexer" {
			t.Errorf("prowlarr path %s", r.URL.Path)
		}
		io.WriteString(w, `[{"id":1,"name":"NZBGeek","enable":true,"protocol":"usenet","fields":[
			{"name":"baseUrl","value":"`+idx.URL+`"},
			{"name":"apiPath","value":"/api"},
			{"name":"apiKey","value":"idx-key"}
		]}]`)
	}))
	defer prow.Close()

	connStore, _, settingsStore, _, _, _, _, _, _, _ := testStores(t)
	if err := connStore.Upsert(context.Background(), "prowlarr", prow.URL, "prowl-key"); err != nil {
		t.Fatal(err)
	}
	httpClient := &http.Client{Timeout: 5 * time.Second}
	lookup := &nzbPasswordLookup{
		conns:    connStore,
		settings: settingsStore,
		http:     httpClient,
		client:   &newznab.Client{HTTP: httpClient, Interval: time.Nanosecond},
	}
	hit, ok := lookup.Lookup(context.Background(), stem)
	if !ok {
		t.Fatal("lookup miss")
	}
	if hit.Title == "" || len(hit.Passwords) == 0 {
		t.Fatalf("hit=%+v", hit)
	}
	if sawHTML {
		t.Fatal("HTML scrape")
	}
}

func TestNZBPasswordLookup_KillSwitch(t *testing.T) {
	connStore, _, settingsStore, _, _, _, _, _, _, _ := testStores(t)
	if err := settingsStore.Set(context.Background(), usenetsearch.KeyNZBPasswordEnabled, "false"); err != nil {
		t.Fatal(err)
	}
	lookup := &nzbPasswordLookup{conns: connStore, settings: settingsStore, client: &newznab.Client{}}
	if _, ok := lookup.Lookup(context.Background(), "o6938TxyzABCDEF"); ok {
		t.Fatal("kill switch must miss")
	}
}

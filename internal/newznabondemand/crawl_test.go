package newznabondemand

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/labbersanon/sakms/internal/dbtest"
	"github.com/labbersanon/sakms/internal/newznab"
	"github.com/labbersanon/sakms/internal/newznabcatalog"
	"github.com/labbersanon/sakms/internal/usenet"
)

func TestFetchRow_CachesNZBAndMarksStreamable(t *testing.T) {
	const nzbXML = `<?xml version="1.0"?>
<nzb xmlns="http://www.newzbin.com/DTD/2003/nzb">
  <file subject="&quot;Movie.part001.rar&quot; yEnc (1/1)">
    <groups><group>alt.binaries.movies</group></groups>
    <segments>
      <segment bytes="1" number="1">a@x</segment>
      <segment bytes="1" number="2">b@x</segment>
    </segments>
  </file>
  <file subject="&quot;Movie.part002.rar&quot; yEnc (1/1)">
    <groups><group>alt.binaries.movies</group></groups>
    <segments>
      <segment bytes="1" number="1">c@x</segment>
      <segment bytes="1" number="2">d@x</segment>
    </segments>
  </file>
</nzb>`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("t") != "get" {
			http.NotFound(w, r)
			return
		}
		io.WriteString(w, nzbXML)
	}))
	defer srv.Close()

	sqlDB := dbtest.New(t)
	ctx := context.Background()
	cat := newznabcatalog.NewStore(sqlDB)
	if _, err := cat.InsertHits(ctx, newznabcatalog.KindMovies, []newznab.CatalogHit{
		{Indexer: "newznab", GUID: "g1", Title: "Movie.2021"},
	}); err != nil {
		t.Fatal(err)
	}
	st := NewStore(sqlDB)
	ref, ok, err := st.NextFromCatalog(ctx)
	if err != nil || !ok {
		t.Fatalf("next ok=%v err=%v", ok, err)
	}
	dir := t.TempDir()
	client := &newznab.Client{HTTP: srv.Client(), Interval: time.Nanosecond}
	idx := newznab.Indexer{Name: "newznab", Endpoint: srv.URL + "/api", APIKey: "k"}
	if err := fetchRow(ctx, st, client, idx, dir, ref); err != nil {
		t.Fatal(err)
	}
	row, ok, err := st.NextFetched(ctx)
	if err != nil || !ok || row.Status != StatusFetched || !row.Streamable {
		t.Fatalf("row=%+v ok=%v err=%v", row, ok, err)
	}
	if strings.Contains(strings.ToLower(row.NZBRelPath+row.RejectReason), "apikey") {
		t.Fatalf("leaked key in %+v", row)
	}
	data, err := os.ReadFile(filepath.Join(dir, row.NZBRelPath))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToLower(string(data)), "apikey") {
		t.Fatal("cached nzb has apikey")
	}
	nzb, err := usenet.ParseNZB(data)
	if err != nil || len(nzb.Files) != 2 {
		t.Fatalf("cached nzb=%v err=%v", nzb, err)
	}
}

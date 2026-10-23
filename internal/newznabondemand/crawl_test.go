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

func TestStatRow_SkipsOverBudgetWithoutSTAT(t *testing.T) {
	sqlDB := dbtest.New(t)
	ctx := context.Background()
	st := NewStore(sqlDB)
	row := Row{
		Indexer: "newznab2", GUID: "mutiny", Kind: newznabcatalog.KindMovies,
		Title: "Mutiny.2026", Status: StatusFetched, Streamable: true,
		PayloadSegments: 32810,
	}
	if err := st.Save(ctx, row); err != nil {
		t.Fatal(err)
	}
	inconc, err := statRow(ctx, st, nil, t.TempDir(), row, nil)
	if err != nil || inconc {
		t.Fatalf("inconc=%v err=%v", inconc, err)
	}
	if _, ok, err := st.NextFetched(ctx); err != nil || ok {
		t.Fatalf("over-budget row must leave the STAT queue ok=%v err=%v", ok, err)
	}
}

func TestStep_SkipOverBudgetUnblocksCatalog(t *testing.T) {
	sqlDB := dbtest.New(t)
	ctx := context.Background()
	cat := newznabcatalog.NewStore(sqlDB)
	if _, err := cat.InsertHits(ctx, newznabcatalog.KindMovies, []newznab.CatalogHit{
		{Indexer: "newznab2", GUID: "mutiny", Title: "Mutiny.2026"},
		{Indexer: "newznab", GUID: "g-next", Title: "Next.Movie.2021"},
	}); err != nil {
		t.Fatal(err)
	}
	st := NewStore(sqlDB)
	if err := st.Save(ctx, Row{
		Indexer: "newznab2", GUID: "mutiny", Kind: newznabcatalog.KindMovies,
		Title: "Mutiny.2026", Status: StatusFetched, Streamable: true,
		PayloadSegments: 32810,
	}); err != nil {
		t.Fatal(err)
	}
	worked, err := step(ctx, st, nil, nil, nil, t.TempDir(), nil)
	if err != nil || !worked {
		t.Fatalf("worked=%v err=%v", worked, err)
	}
	if _, ok, err := st.NextFetched(ctx); err != nil || ok {
		t.Fatalf("still queued ok=%v err=%v", ok, err)
	}
	ref, ok, err := st.NextFromCatalog(ctx)
	if err != nil || !ok || ref.GUID != "g-next" {
		t.Fatalf("catalog still blocked ref=%+v ok=%v err=%v", ref, ok, err)
	}
}

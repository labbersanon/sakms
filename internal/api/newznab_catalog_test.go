package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labbersanon/sakms/internal/dbtest"
	"github.com/labbersanon/sakms/internal/newznab"
	"github.com/labbersanon/sakms/internal/newznabcatalog"
	"github.com/labbersanon/sakms/internal/newznabondemand"
)

func catalogMux(t *testing.T) *httptest.Server {
	t.Helper()
	connStore, propStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	return httptest.NewServer(NewMux(testHTTPClient(), connStore, nil, propStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), rssFeedsStore, nil, nil, nil, nil, nil, nil, nil, nil, nil))
}

func TestNewznabCatalogHandler_EmptyWhenUnattached(t *testing.T) {
	AttachNewznabCatalog(nil)
	srv := catalogMux(t)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/admin/newznab-catalog")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	var got newznabCatalogReport
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.IntervalSeconds != 5 || got.PageSize != 100 {
		t.Fatalf("pace=%+v", got)
	}
	if got.Cursors == nil || got.Counts == nil || got.Overlap == nil || got.Ondemand == nil {
		t.Fatalf("nil slices: %+v", got)
	}
	if len(got.Cursors) != 0 || len(got.Counts) != 0 || len(got.Overlap) != 0 || len(got.Ondemand) != 0 {
		t.Fatalf("want empty, got %+v", got)
	}
}

func TestNewznabCatalogHandler_ReportsOverlapWithoutURLs(t *testing.T) {
	sqlDB := dbtest.New(t)
	st := newznabcatalog.NewStore(sqlDB)
	AttachNewznabCatalog(st)
	t.Cleanup(func() { AttachNewznabCatalog(nil) })

	ctx := context.Background()
	if _, err := st.InsertHits(ctx, newznabcatalog.KindMovies, []newznab.CatalogHit{
		{Indexer: "newznab", GUID: "a", Title: "Same.Movie.2021", ImdbID: "tt1"},
		{Indexer: "newznab2", GUID: "b", Title: "Same.Movie.2021", ImdbID: "tt1"},
	}); err != nil {
		t.Fatal(err)
	}

	srv := catalogMux(t)
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/api/admin/newznab-catalog")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	var got newznabCatalogReport
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	body := strings.ToLower(string(raw))
	for _, banned := range []string{"enclosure", "apikey", "downloadurl", "\"url\""} {
		if strings.Contains(body, banned) {
			t.Fatalf("payload contains %q: %s", banned, body)
		}
	}
	if len(got.Overlap) != 1 || got.Overlap[0].Kind != "movies" || got.Overlap[0].Both != 1 {
		t.Fatalf("overlap=%+v", got.Overlap)
	}
}

func TestNewznabCatalogHandler_ReportsOndemandCounts(t *testing.T) {
	sqlDB := dbtest.New(t)
	cat := newznabcatalog.NewStore(sqlDB)
	od := newznabondemand.NewStore(sqlDB)
	AttachNewznabCatalog(cat)
	AttachNewznabOndemand(od)
	t.Cleanup(func() {
		AttachNewznabCatalog(nil)
		AttachNewznabOndemand(nil)
	})
	ctx := context.Background()
	if err := od.Save(ctx, newznabondemand.Row{
		Indexer: "newznab2", GUID: "v1", Kind: newznabcatalog.KindMovies,
		Title: "Shelf.Movie.2021", Status: newznabondemand.StatusVerified, Streamable: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := od.Save(ctx, newznabondemand.Row{
		Indexer: "newznab2", GUID: "f1", Kind: newznabcatalog.KindMovies,
		Title: "Pending.Movie.2021", Status: newznabondemand.StatusFetched, Streamable: true,
	}); err != nil {
		t.Fatal(err)
	}

	srv := catalogMux(t)
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/api/admin/newznab-catalog")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	var got newznabCatalogReport
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if len(got.Ondemand) != 3 {
		t.Fatalf("ondemand=%+v", got.Ondemand)
	}
	if got.Ondemand[0].Kind != "movies" || got.Ondemand[0].Verified != 1 || got.Ondemand[0].Fetched != 1 {
		t.Fatalf("movies=%+v", got.Ondemand[0])
	}
}

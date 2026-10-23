package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/labbersanon/sakms/internal/dbtest"
	"github.com/labbersanon/sakms/internal/newznab"
	"github.com/labbersanon/sakms/internal/newznabcatalog"
	"github.com/labbersanon/sakms/internal/newznabondemand"
)

func TestOndemandHandler_EmptyWhenUnattached(t *testing.T) {
	AttachNewznabOndemand(nil)
	srv := catalogMux(t)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/modes/movies/ondemand")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	var got ondemandList
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Items == nil || len(got.Items) != 0 {
		t.Fatalf("want empty items, got %+v", got)
	}
}

func TestOndemandHandler_ListsVerifiedWithoutURLs(t *testing.T) {
	sqlDB := dbtest.New(t)
	ctx := context.Background()
	cat := newznabcatalog.NewStore(sqlDB)
	if _, err := cat.InsertHits(ctx, newznabcatalog.KindMovies, []newznab.CatalogHit{
		{Indexer: "newznab", GUID: "https://nzbgeek.info/geekseek.php?guid=hash1", Title: "Shelf.Movie.2021.1080p-GRP", ImdbID: "tt1", TmdbID: "9", Year: 2021},
	}); err != nil {
		t.Fatal(err)
	}
	st := newznabondemand.NewStore(sqlDB)
	if err := st.Save(ctx, newznabondemand.Row{
		Indexer: "newznab", GUID: "https://nzbgeek.info/geekseek.php?guid=hash1",
		Kind: newznabcatalog.KindMovies, Title: "Shelf.Movie.2021.1080p-GRP",
		Status: newznabondemand.StatusVerified, Streamable: true,
	}); err != nil {
		t.Fatal(err)
	}
	AttachNewznabOndemand(st)
	t.Cleanup(func() { AttachNewznabOndemand(nil) })

	srv := catalogMux(t)
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/api/modes/movies/ondemand")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	var got ondemandList
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	body := strings.ToLower(string(raw))
	for _, banned := range []string{"enclosure", "apikey", "downloadurl", "http://", "https://", "geekseek", "nzb.life"} {
		if strings.Contains(body, banned) {
			t.Fatalf("payload contains %q: %s", banned, body)
		}
	}
	if len(got.Items) != 1 || got.Items[0].Title != "Shelf Movie 2021" || got.Items[0].TmdbID != 9 {
		t.Fatalf("items=%+v", got.Items)
	}
	if len(got.Items[0].Qualities) != 1 || got.Items[0].Qualities[0].Label != "1080p" || got.Items[0].Qualities[0].NzbID != "hash1" {
		t.Fatalf("qualities=%+v", got.Items[0].Qualities)
	}
}

func TestOndemandHandler_RejectsUnknownMode(t *testing.T) {
	AttachNewznabOndemand(nil)
	srv := catalogMux(t)
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/api/modes/bogus/ondemand")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status=%d", resp.StatusCode)
	}
}

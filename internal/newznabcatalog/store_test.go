package newznabcatalog

import (
	"context"
	"testing"

	"github.com/labbersanon/sakms/internal/dbtest"
	"github.com/labbersanon/sakms/internal/newznab"
)

func TestStore_InsertAndOverlap(t *testing.T) {
	sqlDB := dbtest.New(t)
	st := NewStore(sqlDB)
	ctx := context.Background()

	n, err := st.InsertHits(ctx, KindMovies, []newznab.CatalogHit{
		{Indexer: "newznab", GUID: "a", Title: "Same.Movie.2021.1080p-GRP", ImdbID: "tt1"},
		{Indexer: "newznab2", GUID: "b", Title: "Same.Movie.2021.1080p-GRP", ImdbID: "tt1"},
		{Indexer: "newznab", GUID: "c", Title: "Same.Movie.2021.720p-GRP", ImdbID: "tt1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("inserted=%d", n)
	}
	again, err := st.InsertHits(ctx, KindMovies, []newznab.CatalogHit{
		{Indexer: "newznab", GUID: "a", Title: "Same.Movie.2021", ImdbID: "tt1"},
	})
	if err != nil || again != 0 {
		t.Fatalf("dup insert=%d err=%v", again, err)
	}

	rep, err := st.Report(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Overlap) != 1 || rep.Overlap[0].Kind != KindMovies {
		t.Fatalf("overlap=%+v", rep.Overlap)
	}
	o := rep.Overlap[0]
	if o.Both != 1 || o.OnlyHost1 != 1 || o.OnlyHost2 != 0 || o.Host1Nzbs != 2 || o.Host2Nzbs != 1 {
		t.Fatalf("overlap=%+v", o)
	}
}

func TestStore_RecomputeOverlapKeysRewritesIMDBKeys(t *testing.T) {
	sqlDB := dbtest.New(t)
	st := NewStore(sqlDB)
	ctx := context.Background()
	_, err := sqlDB.ExecContext(ctx, `
		INSERT INTO newznab_catalog (indexer, guid, title, kind, overlap_key)
		VALUES ('newznab', 'a', 'Same.Movie.2021.1080p-GRP', 'movies', 'imdb:tt1')`)
	if err != nil {
		t.Fatal(err)
	}
	n, err := st.RecomputeOverlapKeys(ctx)
	if err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	var key string
	if err := sqlDB.QueryRowContext(ctx, `SELECT overlap_key FROM newznab_catalog WHERE guid='a'`).Scan(&key); err != nil {
		t.Fatal(err)
	}
	if key != "nzb:same.movie.2021.1080p.grp" {
		t.Fatalf("key=%q", key)
	}
}

func TestStore_CursorRoundTrip(t *testing.T) {
	sqlDB := dbtest.New(t)
	st := NewStore(sqlDB)
	ctx := context.Background()
	in := Cursor{Indexer: "newznab", Kind: KindMovies, PageOffset: 100, LastPageNew: 50, UpdatedAt: 9}
	if err := st.SaveCursor(ctx, in); err != nil {
		t.Fatal(err)
	}
	got, err := st.Cursor(ctx, "newznab", KindMovies)
	if err != nil {
		t.Fatal(err)
	}
	if got.PageOffset != 100 || got.Complete || got.LastPageNew != 50 {
		t.Fatalf("got=%+v", got)
	}
}

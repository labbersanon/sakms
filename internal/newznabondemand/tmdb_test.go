package newznabondemand

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/labbersanon/sakms/internal/dbtest"
	"github.com/labbersanon/sakms/internal/newznab"
	"github.com/labbersanon/sakms/internal/newznabcatalog"
)

type stubFinder struct {
	movie map[string]int
	tv    map[string]int
	tvdb  map[int]int
	err   error
	calls []string
}

func (s *stubFinder) MovieByIMDB(_ context.Context, imdbID string) (int, error) {
	s.calls = append(s.calls, "movie:"+imdbID)
	if s.err != nil {
		return 0, s.err
	}
	return s.movie[imdbID], nil
}

func (s *stubFinder) TVByIMDB(_ context.Context, imdbID string) (int, error) {
	s.calls = append(s.calls, "tv:"+imdbID)
	if s.err != nil {
		return 0, s.err
	}
	return s.tv[imdbID], nil
}

func (s *stubFinder) TVByTVDB(_ context.Context, tvdbID int) (int, error) {
	s.calls = append(s.calls, "tvdb")
	if s.err != nil {
		return 0, s.err
	}
	return s.tvdb[tvdbID], nil
}

func TestIMDBCandidates(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"tt0111161", []string{"tt0111161"}},
		{"tt00087921", []string{"tt00087921", "tt0087921"}},
		{"tt0000000", nil},
		{"", nil},
		{"tt32338669", []string{"tt32338669"}},
	}
	for _, c := range cases {
		got := imdbCandidates(c.in)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("imdbCandidates(%q)=%v want %v", c.in, got, c.want)
		}
	}
}

func TestResolveCatalogTMDB_HitFillsSameIMDB(t *testing.T) {
	sqlDB := dbtest.New(t)
	ctx := context.Background()
	cat := newznabcatalog.NewStore(sqlDB)
	if _, err := cat.InsertHits(ctx, newznabcatalog.KindMovies, []newznab.CatalogHit{
		{Indexer: "newznab", GUID: "g1", Title: "Movie.1080p", ImdbID: "tt0111161"},
		{Indexer: "newznab", GUID: "g2", Title: "Movie.2160p", ImdbID: "tt0111161"},
	}); err != nil {
		t.Fatal(err)
	}
	st := NewStore(sqlDB)
	finder := &stubFinder{movie: map[string]int{"tt0111161": 278}}
	if err := resolveCatalogTMDB(ctx, st, finder, Row{Indexer: "newznab", GUID: "g1", Kind: newznabcatalog.KindMovies}); err != nil {
		t.Fatal(err)
	}
	_, tmdb1, _, err := st.CatalogIDs(ctx, "newznab", "g1")
	if err != nil || tmdb1 != "278" {
		t.Fatalf("g1 tmdb=%q err=%v", tmdb1, err)
	}
	_, tmdb2, _, err := st.CatalogIDs(ctx, "newznab", "g2")
	if err != nil || tmdb2 != "278" {
		t.Fatalf("g2 tmdb=%q err=%v", tmdb2, err)
	}
}

func TestResolveCatalogTMDB_MissStampsZero(t *testing.T) {
	sqlDB := dbtest.New(t)
	ctx := context.Background()
	cat := newznabcatalog.NewStore(sqlDB)
	if _, err := cat.InsertHits(ctx, newznabcatalog.KindMovies, []newznab.CatalogHit{
		{Indexer: "newznab", GUID: "g1", Title: "Unknown.1080p", ImdbID: "tt9999999"},
	}); err != nil {
		t.Fatal(err)
	}
	st := NewStore(sqlDB)
	if err := resolveCatalogTMDB(ctx, st, &stubFinder{movie: map[string]int{}}, Row{Indexer: "newznab", GUID: "g1", Kind: newznabcatalog.KindMovies}); err != nil {
		t.Fatal(err)
	}
	_, tmdbID, _, err := st.CatalogIDs(ctx, "newznab", "g1")
	if err != nil || tmdbID != "0" {
		t.Fatalf("tmdb=%q err=%v", tmdbID, err)
	}
}

func TestResolveCatalogTMDB_UnsetDoesNotStamp(t *testing.T) {
	sqlDB := dbtest.New(t)
	ctx := context.Background()
	cat := newznabcatalog.NewStore(sqlDB)
	if _, err := cat.InsertHits(ctx, newznabcatalog.KindMovies, []newznab.CatalogHit{
		{Indexer: "newznab", GUID: "g1", Title: "Movie.1080p", ImdbID: "tt0111161"},
	}); err != nil {
		t.Fatal(err)
	}
	st := NewStore(sqlDB)
	err := resolveCatalogTMDB(ctx, st, nil, Row{Indexer: "newznab", GUID: "g1", Kind: newznabcatalog.KindMovies})
	if !errors.Is(err, ErrTMDBUnset) {
		t.Fatalf("err=%v", err)
	}
	_, tmdbID, _, err := st.CatalogIDs(ctx, "newznab", "g1")
	if err != nil || tmdbID != "" {
		t.Fatalf("tmdb=%q err=%v want empty", tmdbID, err)
	}
}

func TestResolveCatalogTMDB_SkipsWhenAlreadySet(t *testing.T) {
	sqlDB := dbtest.New(t)
	ctx := context.Background()
	cat := newznabcatalog.NewStore(sqlDB)
	if _, err := cat.InsertHits(ctx, newznabcatalog.KindMovies, []newznab.CatalogHit{
		{Indexer: "newznab", GUID: "g1", Title: "Movie.1080p", ImdbID: "tt0111161", TmdbID: "278"},
	}); err != nil {
		t.Fatal(err)
	}
	st := NewStore(sqlDB)
	finder := &stubFinder{movie: map[string]int{"tt0111161": 1}}
	if err := resolveCatalogTMDB(ctx, st, finder, Row{Indexer: "newznab", GUID: "g1", Kind: newznabcatalog.KindMovies}); err != nil {
		t.Fatal(err)
	}
	if len(finder.calls) != 0 {
		t.Fatalf("calls=%v", finder.calls)
	}
}

func TestResolveCatalogTMDB_PadsOverlongIMDB(t *testing.T) {
	sqlDB := dbtest.New(t)
	ctx := context.Background()
	cat := newznabcatalog.NewStore(sqlDB)
	if _, err := cat.InsertHits(ctx, newznabcatalog.KindMovies, []newznab.CatalogHit{
		{Indexer: "newznab", GUID: "g1", Title: "Places.1080p", ImdbID: "tt00087921"},
	}); err != nil {
		t.Fatal(err)
	}
	st := NewStore(sqlDB)
	finder := &stubFinder{movie: map[string]int{"tt0087921": 9702}}
	if err := resolveCatalogTMDB(ctx, st, finder, Row{Indexer: "newznab", GUID: "g1", Kind: newznabcatalog.KindMovies}); err != nil {
		t.Fatal(err)
	}
	_, tmdbID, _, err := st.CatalogIDs(ctx, "newznab", "g1")
	if err != nil || tmdbID != "9702" {
		t.Fatalf("tmdb=%q err=%v calls=%v", tmdbID, err, finder.calls)
	}
}

func TestStep_BackfillsVerifiedTMDB(t *testing.T) {
	sqlDB := dbtest.New(t)
	ctx := context.Background()
	cat := newznabcatalog.NewStore(sqlDB)
	if _, err := cat.InsertHits(ctx, newznabcatalog.KindMovies, []newznab.CatalogHit{
		{Indexer: "newznab", GUID: "g1", Title: "Movie.1080p", ImdbID: "tt0111161"},
	}); err != nil {
		t.Fatal(err)
	}
	st := NewStore(sqlDB)
	if err := st.Save(ctx, Row{
		Indexer: "newznab", GUID: "g1", Kind: newznabcatalog.KindMovies,
		Title: "Movie.1080p", Status: StatusVerified, Streamable: true,
	}); err != nil {
		t.Fatal(err)
	}
	finder := &stubFinder{movie: map[string]int{"tt0111161": 278}}
	worked, err := step(ctx, st, nil, nil, nil, t.TempDir(), finder)
	if err != nil || !worked {
		t.Fatalf("worked=%v err=%v", worked, err)
	}
	_, tmdbID, _, err := st.CatalogIDs(ctx, "newznab", "g1")
	if err != nil || tmdbID != "278" {
		t.Fatalf("tmdb=%q err=%v", tmdbID, err)
	}
}

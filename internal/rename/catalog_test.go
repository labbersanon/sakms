package rename

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/proposals"
)

func TestCatalogMovieAtPath_NFO(t *testing.T) {
	dir := t.TempDir()
	video := filepath.Join(dir, "Inside Out (2015).mkv")
	if err := os.WriteFile(video, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	nfoPath := filepath.Join(dir, "movie.nfo")
	if err := os.WriteFile(nfoPath, []byte(`<movie><tmdbid>150540</tmdbid><title>Inside Out</title><year>2015</year></movie>`), 0o644); err != nil {
		t.Fatal(err)
	}
	libStore := newTestLibraryStore(t)
	ok, err := catalogMovieAtPath(context.Background(), libStore, video, dir)
	if err != nil {
		t.Fatalf("catalog: %v", err)
	}
	if !ok {
		t.Fatal("expected catalog")
	}
	got, err := libStore.GetByTMDBID(context.Background(), mode.Movies, 150540)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Title != "Inside Out" || got.FilePath != video {
		t.Fatalf("item = %+v", got)
	}
}

func TestCatalogMovieAtPath_DoesNotOverwriteExistingPrimary(t *testing.T) {
	dir := t.TempDir()
	video := filepath.Join(dir, "copy.mkv")
	if err := os.WriteFile(video, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "movie.nfo"), []byte(`<movie><tmdbid>11</tmdbid><title>Star Wars</title></movie>`), 0o644); err != nil {
		t.Fatal(err)
	}
	libStore := newTestLibraryStore(t)
	primary := filepath.Join(dir, "primary.mkv")
	if _, err := libStore.Upsert(context.Background(), library.Item{
		Mode: mode.Movies, TMDBID: 11, Title: "Star Wars", FilePath: primary,
	}); err != nil {
		t.Fatal(err)
	}
	ok, err := catalogMovieAtPath(context.Background(), libStore, video, dir)
	if err != nil || !ok {
		t.Fatalf("catalog ok=%v err=%v", ok, err)
	}
	got, err := libStore.GetByTMDBID(context.Background(), mode.Movies, 11)
	if err != nil {
		t.Fatal(err)
	}
	if got.FilePath != primary {
		t.Fatalf("primary overwritten: %q", got.FilePath)
	}
}

func TestCatalogEpisodeAtPath_TVShowNFO(t *testing.T) {
	root := t.TempDir()
	season := filepath.Join(root, "Animaniacs (1993)", "Season 01")
	if err := os.MkdirAll(season, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "Animaniacs (1993)", "tvshow.nfo"), []byte(`<tvshow><tmdbid>1566</tmdbid><title>Animaniacs</title><year>1993</year></tvshow>`), 0o644); err != nil {
		t.Fatal(err)
	}
	video := filepath.Join(season, "Animaniacs S01E01 Test.mkv")
	if err := os.WriteFile(video, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	libStore := newTestLibraryStore(t)
	ok, err := catalogEpisodeAtPath(context.Background(), nil, libStore, video, root, []string{root})
	if err != nil {
		t.Fatalf("catalog: %v", err)
	}
	if !ok {
		t.Fatal("expected catalog")
	}
	series, err := libStore.GetSeriesByTMDBID(context.Background(), 1566)
	if err != nil {
		t.Fatalf("series: %v", err)
	}
	if series.Title != "Animaniacs" {
		t.Fatalf("title = %q", series.Title)
	}
	ep, err := libStore.GetEpisode(context.Background(), series.ID, 1, 1)
	if err != nil {
		t.Fatalf("episode: %v", err)
	}
	if ep.FilePath != video {
		t.Fatalf("path = %q", ep.FilePath)
	}
}

func TestCatalogEpisodeAtPath_YearSeasonAndKidsTag(t *testing.T) {
	root := t.TempDir()
	season := filepath.Join(root, "Looney Tunes", "1958")
	if err := os.MkdirAll(season, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "Looney Tunes", "tvshow.nfo"), []byte(
		`<tvshow><tmdbid>333432</tmdbid><title>Looney Tunes</title><year>1929</year></tvshow>`,
	), 0o644); err != nil {
		t.Fatal(err)
	}
	video := filepath.Join(season, "Looney.Tunes.S1958E14.Fistic.Mystic.mkv")
	if err := os.WriteFile(video, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	libStore := newTestLibraryStore(t)
	sess := &mode.Session{Mode: mode.Series, KidsRootPath: root}
	ok, err := catalogEpisodeAtPath(context.Background(), sess, libStore, video, root, []string{root})
	if err != nil || !ok {
		t.Fatalf("catalog ok=%v err=%v", ok, err)
	}
	series, err := libStore.GetSeriesByTMDBID(context.Background(), 333432)
	if err != nil {
		t.Fatal(err)
	}
	ep, err := libStore.GetEpisode(context.Background(), series.ID, 1958, 14)
	if err != nil {
		t.Fatalf("episode: %v", err)
	}
	if ep.FilePath != video {
		t.Fatalf("path = %q", ep.FilePath)
	}
	tags, err := libStore.SeriesTags(context.Background(), series.ID)
	if err != nil {
		t.Fatal(err)
	}
	foundKids := false
	for _, tag := range tags {
		if tag == "kids" {
			foundKids = true
		}
	}
	if !foundKids {
		t.Fatalf("tags = %v, want kids", tags)
	}
}

func TestCatalogEpisodeAtPath_WrongNFOIgnored(t *testing.T) {
	root := t.TempDir()
	season := filepath.Join(root, "Looney Toons", "1958")
	if err := os.MkdirAll(season, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "Looney Toons", "tvshow.nfo"), []byte(
		`<tvshow><uniqueid type="tvdb">465409</uniqueid><title>The Tooney and Russo Show</title></tvshow>`,
	), 0o644); err != nil {
		t.Fatal(err)
	}
	video := filepath.Join(season, "Looney.Tunes.S1958E14.mkv")
	if err := os.WriteFile(video, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	ok, err := catalogEpisodeAtPath(context.Background(), nil, newTestLibraryStore(t), video, root, []string{root})
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("wrong-nfo file must not catalog without a trusted show id")
	}
}

func TestCatalogEpisodeAtPath_NestedDiscYearFolder(t *testing.T) {
	root := t.TempDir()
	disc := filepath.Join(root, "Looney Tunes", "1958", "Disc 1")
	if err := os.MkdirAll(disc, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "Looney Tunes", "tvshow.nfo"), []byte(
		`<tvshow><tmdbid>333432</tmdbid><title>Looney Tunes</title></tvshow>`,
	), 0o644); err != nil {
		t.Fatal(err)
	}
	video := filepath.Join(disc, "E14 Fistic Mystic.mkv")
	if err := os.WriteFile(video, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	libStore := newTestLibraryStore(t)
	ok, err := catalogEpisodeAtPath(context.Background(), nil, libStore, video, root, []string{root})
	if err != nil || !ok {
		t.Fatalf("catalog ok=%v err=%v", ok, err)
	}
	series, err := libStore.GetSeriesByTMDBID(context.Background(), 333432)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := libStore.GetEpisode(context.Background(), series.ID, 1958, 14); err != nil {
		t.Fatalf("episode: %v", err)
	}
}

func TestCatalogEpisodeAtPath_KeepsAnthologyNegativeTMDBID(t *testing.T) {
	root := t.TempDir()
	season := filepath.Join(root, "Laurel & Hardy (1919)", "Season 06")
	if err := os.MkdirAll(season, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "Laurel & Hardy (1919)", "tvshow.nfo"), []byte(
		`<tvshow><tmdbid>-1498833576</tmdbid><tvdbid>73910</tvdbid><title>Laurel &amp; Hardy</title><year>1919</year></tvshow>`,
	), 0o644); err != nil {
		t.Fatal(err)
	}
	video := filepath.Join(season, "Laurel & Hardy S06E08 Another Fine Mess.mp4")
	if err := os.WriteFile(video, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// FindTVByTVDBID hits /find/ — unexpected path fatals. A call would remap
	// TVDB 73910 to the 1966 cartoon.
	sess := &mode.Session{Mode: mode.Series, TMDB: fakeTMDBSeriesServer(t, map[string]string{}, nil)}
	libStore := newTestLibraryStore(t)
	ok, err := catalogEpisodeAtPath(context.Background(), sess, libStore, video, root, []string{root})
	if err != nil || !ok {
		t.Fatalf("catalog ok=%v err=%v", ok, err)
	}
	series, err := libStore.GetSeriesByTMDBID(context.Background(), -1498833576)
	if err != nil {
		t.Fatal(err)
	}
	if series.TVDBID != 73910 || series.Title != "Laurel & Hardy" {
		t.Fatalf("series = %+v", series)
	}
	if _, err := libStore.GetEpisode(context.Background(), series.ID, 6, 8); err != nil {
		t.Fatalf("episode: %v", err)
	}
}

func TestCatalogPendingSeries_WritesLibrary(t *testing.T) {
	root := t.TempDir()
	video := filepath.Join(root, "Curious George", "01-Rescue.mkv")
	if err := os.MkdirAll(filepath.Dir(video), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(video, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	libStore := newTestLibraryStore(t)
	catalogPendingSeries(context.Background(), nil, libStore, []string{root}, []proposals.Proposal{{
		Status: proposals.Pending, SourcePath: video, TMDBID: 656, Title: "Curious George",
		Year: 2006, SeasonNumber: 1, EpisodeNumber: 1, RootFolderPath: root,
	}})
	series, err := libStore.GetSeriesByTMDBID(context.Background(), 656)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := libStore.GetEpisode(context.Background(), series.ID, 1, 1); err != nil {
		t.Fatal(err)
	}
}

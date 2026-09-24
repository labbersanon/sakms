package rename

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/naming"
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

func TestCatalogEpisodeAtPath_NestsMovieShortUnderAnthology(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()
	libStore := newTestLibraryStore(t)
	parent, err := libStore.UpsertSeries(ctx, library.Series{
		TMDBID: -1498833576, TVDBID: 73910, Title: "Laurel & Hardy", Year: 1919, RootFolderPath: root,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := libStore.UpsertEpisode(ctx, library.Episode{
		SeriesID: parent.ID, SeasonNumber: 7, EpisodeNumber: 8, Title: "One Good Turn",
		FilePath: filepath.Join(root, "Laurel & Hardy (1919)", "Season 07", "S07E08.mp4"),
	}); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "One Good Turn (1931) [tmdbid-48903]", "Season 00")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	video := filepath.Join(dir, "One Good Turn S00E00.mkv")
	if err := os.WriteFile(video, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	stray, err := libStore.UpsertSeries(ctx, library.Series{
		TMDBID: 48903, Title: "One Good Turn", Year: 1931, RootFolderPath: root,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := libStore.UpsertEpisode(ctx, library.Episode{
		SeriesID: stray.ID, SeasonNumber: 0, EpisodeNumber: 0, Title: "One Good Turn", FilePath: video,
	}); err != nil {
		t.Fatal(err)
	}
	ok, err := catalogEpisodeAtPath(ctx, nil, libStore, video, root, []string{root})
	if err != nil || !ok {
		t.Fatalf("catalog ok=%v err=%v", ok, err)
	}
	if _, err := libStore.GetSeriesByTMDBID(ctx, 48903); err == nil {
		t.Fatal("did not expect a standalone series for the movie TMDB id")
	}
	ep, err := libStore.GetEpisode(ctx, parent.ID, 7, 8)
	if err != nil {
		t.Fatal(err)
	}
	files, err := libStore.ListEpisodeFiles(ctx, ep.ID)
	if err != nil {
		t.Fatal(err)
	}
	found := ep.FilePath == video
	for _, f := range files {
		if f.FilePath == video {
			found = true
		}
	}
	if !found {
		t.Fatalf("short was not attached to S07E08; primary=%q files=%+v", ep.FilePath, files)
	}
}

func TestDummyMovieEpisodeParse(t *testing.T) {
	if !dummyMovieEpisodeParse(0, []int{0}) {
		t.Fatal("S00E00 should be dummy")
	}
	if dummyMovieEpisodeParse(0, []int{11}) {
		t.Fatal("S00E11 is a real special")
	}
	if dummyMovieEpisodeParse(7, []int{8}) {
		t.Fatal("S07E08 is real")
	}
}

func TestTitleFromShowFolder(t *testing.T) {
	got := titleFromShowFolder("One Good Turn (1931) [tmdbid-48903]")
	if got != "One Good Turn" {
		t.Fatalf("title = %q", got)
	}
	if yearFromShowFolder("Night Owls (1930) [tmdbid-48889]") != 1930 {
		t.Fatal("year")
	}
}

func seedLaurelHardyParent(t *testing.T, libStore *library.Store, root string) library.Series {
	t.Helper()
	ctx := context.Background()
	parent, err := libStore.UpsertSeries(ctx, library.Series{
		TMDBID: -1498833576, TVDBID: 73910, Title: "Laurel & Hardy", Year: 1919, RootFolderPath: root,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := libStore.UpsertEpisode(ctx, library.Episode{
		SeriesID: parent.ID, SeasonNumber: 7, EpisodeNumber: 8, Title: "One Good Turn",
		FilePath: filepath.Join(root, "Laurel & Hardy (1919)", "Season 07", "S07E08.mp4"),
	}); err != nil {
		t.Fatal(err)
	}
	return parent
}

func TestCatalogEpisodeAtPath_NestsFromTVDBCatalog(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()
	libStore := newTestLibraryStore(t)
	parent := seedLaurelHardyParent(t, libStore, root)
	dir := filepath.Join(root, "Night Owls (1930) [tmdbid-48889]", "Season 00")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	video := filepath.Join(dir, "Night Owls S00E00.mkv")
	if err := os.WriteFile(video, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	stray, err := libStore.UpsertSeries(ctx, library.Series{
		TMDBID: 48889, Title: "Night Owls", Year: 2023, RootFolderPath: root,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := libStore.UpsertEpisode(ctx, library.Episode{
		SeriesID: stray.ID, SeasonNumber: 0, EpisodeNumber: 0, Title: "Night Owls", FilePath: video,
	}); err != nil {
		t.Fatal(err)
	}
	sess := &mode.Session{Mode: mode.Series, TVDB: fakeTVDBEpisodesServer(t, []fakeTVDBEpisode{
		{ID: 20, SeriesID: 73910, Name: "Night Owls", Number: 4, SeasonNumber: 5, Aired: "1930-01-04"},
		{ID: 8, SeriesID: 73910, Name: "One Good Turn", Number: 8, SeasonNumber: 7, Aired: "1931-10-31"},
	})}
	ok, err := catalogEpisodeAtPath(ctx, sess, libStore, video, root, []string{root})
	if err != nil || !ok {
		t.Fatalf("catalog ok=%v err=%v", ok, err)
	}
	if _, err := libStore.GetSeriesByTMDBID(ctx, 48889); err == nil {
		t.Fatal("expected stray movie-id series to be removed")
	}
	ep, err := libStore.GetEpisode(ctx, parent.ID, 5, 4)
	if err != nil {
		t.Fatal(err)
	}
	if ep.Title != "Night Owls" || ep.FilePath != video {
		t.Fatalf("episode = %+v", ep)
	}
}

func TestCatalogEpisodeAtPath_YearMismatchDoesNotNest(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()
	libStore := newTestLibraryStore(t)
	seedLaurelHardyParent(t, libStore, root)
	dir := filepath.Join(root, "Night Owls (2023) [tmdbid-48889]", "Season 00")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	video := filepath.Join(dir, "Night Owls S00E00.mkv")
	if err := os.WriteFile(video, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	sess := &mode.Session{Mode: mode.Series, TVDB: fakeTVDBEpisodesServer(t, []fakeTVDBEpisode{
		{ID: 20, SeriesID: 73910, Name: "Night Owls", Number: 4, SeasonNumber: 5, Aired: "1930-01-04"},
	})}
	ok, err := catalogEpisodeAtPath(ctx, sess, libStore, video, root, []string{root})
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("2023 Night Owls folder must not nest onto the 1930 short")
	}
}

func TestCatalogEpisodeAtPath_KeepsEstablishedMovieSeriesWhenOtherFilesRemain(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()
	libStore := newTestLibraryStore(t)
	parent := seedLaurelHardyParent(t, libStore, root)
	dir := filepath.Join(root, "Night Owls (1930) [tmdbid-48889]", "Season 00")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	video := filepath.Join(dir, "Night Owls S00E00.mkv")
	if err := os.WriteFile(video, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	s01 := filepath.Join(root, "Night Owls (2023) [tmdbid-48889]", "Season 01", "Night Owls S01E12.mkv")
	stray, err := libStore.UpsertSeries(ctx, library.Series{
		TMDBID: 48889, Title: "Night Owls", Year: 2023, RootFolderPath: root,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := libStore.UpsertEpisode(ctx, library.Episode{
		SeriesID: stray.ID, SeasonNumber: 0, EpisodeNumber: 0, Title: "Night Owls", FilePath: video,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := libStore.UpsertEpisode(ctx, library.Episode{
		SeriesID: stray.ID, SeasonNumber: 1, EpisodeNumber: 12, Title: "Episode 12", FilePath: s01,
	}); err != nil {
		t.Fatal(err)
	}
	sess := &mode.Session{Mode: mode.Series, TVDB: fakeTVDBEpisodesServer(t, []fakeTVDBEpisode{
		{ID: 20, SeriesID: 73910, Name: "Night Owls", Number: 4, SeasonNumber: 5, Aired: "1930-01-04"},
	})}
	ok, err := catalogEpisodeAtPath(ctx, sess, libStore, video, root, []string{root})
	if err != nil || !ok {
		t.Fatalf("catalog ok=%v err=%v", ok, err)
	}
	kept, err := libStore.GetSeriesByTMDBID(ctx, 48889)
	if err != nil {
		t.Fatal("2023 Night Owls card must remain")
	}
	if _, err := libStore.GetEpisode(ctx, kept.ID, 1, 12); err != nil {
		t.Fatal(err)
	}
	if _, err := libStore.GetEpisode(ctx, parent.ID, 5, 4); err != nil {
		t.Fatal(err)
	}
}

func TestCatalogEpisodeAtPath_TributeDoesNotNest(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()
	libStore := newTestLibraryStore(t)
	seedLaurelHardyParent(t, libStore, root)
	dir := filepath.Join(root, "Tribute to the Boys (1992) [tmdbid-48739]", "Season 00")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	video := filepath.Join(dir, "Tribute to the Boys S00E00.mkv")
	if err := os.WriteFile(video, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	sess := &mode.Session{Mode: mode.Series, TVDB: fakeTVDBEpisodesServer(t, []fakeTVDBEpisode{
		{ID: 20, SeriesID: 73910, Name: "Night Owls", Number: 4, SeasonNumber: 5, Aired: "1930-01-04"},
	})}
	ok, err := catalogEpisodeAtPath(ctx, sess, libStore, video, root, []string{root})
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("Tribute must stay a separate show")
	}
}

func TestScanLibrarySeries_NestsAlreadyTrackedDummyShort(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()
	libStore := newTestLibraryStore(t)
	parent := seedLaurelHardyParent(t, libStore, root)
	dir := filepath.Join(root, "Leave 'Em Laughing (1928) [tmdbid-48823]", "Season 00")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	video := filepath.Join(dir, "Leave 'Em Laughing S00E00.mkv")
	if err := os.WriteFile(video, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	stray, err := libStore.UpsertSeries(ctx, library.Series{
		TMDBID: 48823, Title: "Leave 'Em Laughing", Year: 1928, RootFolderPath: root,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := libStore.UpsertEpisode(ctx, library.Episode{
		SeriesID: stray.ID, SeasonNumber: 0, EpisodeNumber: 0, Title: "Leave 'Em Laughing", FilePath: video,
	}); err != nil {
		t.Fatal(err)
	}
	sess := &mode.Session{
		Mode: mode.Series,
		TMDB: fatalTMDBSeriesServer(t),
		TVDB: fakeTVDBEpisodesServer(t, []fakeTVDBEpisode{
			{ID: 11, SeriesID: 73910, Name: "Leave 'Em Laughing", Number: 2, SeasonNumber: 2, Aired: "1928-01-28"},
		}),
	}
	if _, err := ScanLibrarySeries(ctx, sess, libStore, root, naming.Jellyfin, DefaultMatchConfig(), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := libStore.GetSeriesByTMDBID(ctx, 48823); err == nil {
		t.Fatal("expected stray Leave 'Em Laughing series to be removed")
	}
	ep, err := libStore.GetEpisode(ctx, parent.ID, 2, 2)
	if err != nil {
		t.Fatal(err)
	}
	if ep.Title != "Leave 'Em Laughing" || ep.FilePath != video {
		t.Fatalf("episode = %+v", ep)
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

package api

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/labbersanon/sakms/internal/autograb"
	"github.com/labbersanon/sakms/internal/grabs"
	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mediainfo"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/quality"
	"github.com/labbersanon/sakms/internal/tvdb"
	"github.com/labbersanon/sakms/internal/usenet"
)

type importQualityProber struct {
	height   int
	codec    string
	duration float64
}

func (p importQualityProber) Probe(context.Context, string) (*mediainfo.Probe, error) {
	return &mediainfo.Probe{Height: p.height, CodecName: p.codec, Duration: p.duration}, nil
}

func TestRefuseImportBelowFloor_EliteSized1080p(t *testing.T) {
	ctx := context.Background()
	_, _, settingsStore, _, libStore, _, _, _, _, _ := testStores(t)
	if err := settingsStore.Set(ctx, qualityTiersKey(mode.Series), `["medium","high","lossless"]`); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "American.Horror.Story.S13E06.1080p.x265-ELiTE.mkv")
	// 1 MiB is enough: live ELiTE was ~294 MiB / 24 min and already below the
	// Medium/1080 floor; a smaller file is the same reject with less disk.
	if err := os.WriteFile(path, make([]byte, 1<<20), 0o644); err != nil {
		t.Fatal(err)
	}
	g := &grabs.Grab{Mode: mode.Series, Title: "American Horror Story", TMDBID: 1413, SeasonNumber: 13, EpisodeNumber: 6, Protocol: "usenet"}
	err := refuseImportBelowFloor(ctx, libStore, settingsStore, nil, g, path, importQualityProber{
		height: 1080, codec: "hevc", duration: 24 * 60,
	})
	if err == nil {
		t.Fatal("expected below-floor import reject")
	}
	if !errors.Is(err, autograb.ErrBelowQualityFloor) {
		t.Fatalf("err = %v, want ErrBelowQualityFloor", err)
	}
	if !errors.Is(err, usenet.ErrContentUnusable) {
		t.Fatalf("err = %v, want wrap of ErrContentUnusable so alternate-NZB park fires", err)
	}
	if !contentUnusableFailure(err) {
		t.Fatal("below-floor must route to alternate-release park")
	}
	if got := contentFailureReason(err); got != contentBelowFloorReason {
		t.Fatalf("reason = %q, want %q", got, contentBelowFloorReason)
	}
}

func TestRefuseImportBelowFloor_FailOpenWithoutRuntime(t *testing.T) {
	ctx := context.Background()
	_, _, settingsStore, _, libStore, _, _, _, _, _ := testStores(t)
	path := filepath.Join(t.TempDir(), "Show.S01E01.mkv")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	g := &grabs.Grab{Mode: mode.Series, Title: "Show", Protocol: "usenet"}
	if err := refuseImportBelowFloor(ctx, libStore, settingsStore, nil, g, path, nil); err != nil {
		t.Fatalf("nil sess/prober must fail open, got %v", err)
	}
}

func TestRefuseImportBelowFloor_DVDRipPassesLow480NotLow1080(t *testing.T) {
	ctx := context.Background()
	_, _, settingsStore, _, libStore, _, _, _, _, _ := testStores(t)
	if err := settingsStore.Set(ctx, qualityTiersKey(mode.Series), `["low","medium","high","lossless"]`); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "Looney.Tunes.S1956E28.The.Honey.Mousers.DVDRip.x264.mkv")
	if err := os.WriteFile(path, make([]byte, 67<<20), 0o644); err != nil {
		t.Fatal(err)
	}
	g := &grabs.Grab{Mode: mode.Series, Title: "Looney Tunes", TMDBID: 333432, SeasonNumber: 28, EpisodeNumber: 28, Protocol: "usenet"}
	err := refuseImportBelowFloor(ctx, libStore, settingsStore, nil, g, path, importQualityProber{
		height: 480, codec: "h264", duration: 15 * 60,
	})
	if err != nil {
		t.Fatalf("Low@480 must accept Honey-Mousers-sized DVDRip, got %v", err)
	}

	tiny1080 := filepath.Join(dir, "Show.S01E01.1080p.x264.mkv")
	if err := os.WriteFile(tiny1080, make([]byte, 67<<20), 0o644); err != nil {
		t.Fatal(err)
	}
	g1080 := &grabs.Grab{Mode: mode.Series, Title: "Show", TMDBID: 1, SeasonNumber: 1, EpisodeNumber: 1, Protocol: "usenet"}
	err = refuseImportBelowFloor(ctx, libStore, settingsStore, nil, g1080, tiny1080, importQualityProber{
		height: 1080, codec: "h264", duration: 15 * 60,
	})
	if err == nil || !errors.Is(err, autograb.ErrBelowQualityFloor) {
		t.Fatalf("same bytes at 1080p must still fail Low@1080, got %v", err)
	}
}

func TestRefuseImportBelowFloor_Animation480PassesMedium(t *testing.T) {
	clearAnimationGenreCache(t)
	ctx := context.Background()
	_, _, settingsStore, _, libStore, _, _, _, _, _ := testStores(t)
	if err := settingsStore.Set(ctx, qualityTiersKey(mode.Series), `["medium","high","lossless"]`); err != nil {
		t.Fatal(err)
	}
	if _, err := libStore.UpsertSeries(ctx, library.Series{
		TMDBID: 333432, Title: "Looney Tunes", Genres: []string{"Animation"},
	}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "Looney.Tunes.S1956E28.The.Honey.Mousers.DVDRip.x264.mkv")
	if err := os.WriteFile(path, make([]byte, 67<<20), 0o644); err != nil {
		t.Fatal(err)
	}
	g := &grabs.Grab{Mode: mode.Series, Title: "Looney Tunes", TMDBID: 333432, SeasonNumber: 28, EpisodeNumber: 28, Protocol: "usenet"}
	if err := refuseImportBelowFloor(ctx, libStore, settingsStore, nil, g, path, importQualityProber{
		height: 480, codec: "h264", duration: 15 * 60,
	}); err != nil {
		t.Fatalf("Medium@480 Animation must accept Honey-Mousers-sized DVDRip, got %v", err)
	}
}

func TestRefuseImportBelowFloor_Animation480TVDBAnthology(t *testing.T) {
	clearAnimationGenreCache(t)
	ctx := context.Background()
	_, _, settingsStore, _, libStore, _, _, _, _, _ := testStores(t)
	if err := settingsStore.Set(ctx, qualityTiersKey(mode.Series), `["medium","high","lossless"]`); err != nil {
		t.Fatal(err)
	}
	synth := -42
	if _, err := libStore.UpsertSeries(ctx, library.Series{
		TMDBID: synth, TVDBID: 7266, Title: "Looney Tunes",
	}); err != nil {
		t.Fatal(err)
	}
	tvdbSrv := fakeTVDBGenreServer(t, 7266, []map[string]any{{"name": "Animation"}})
	sess := &mode.Session{Mode: mode.Series, TVDB: tvdb.New(tvdb.Config{BaseURL: tvdbSrv.URL, APIKey: "k"}, tvdbSrv.Client())}
	path := filepath.Join(t.TempDir(), "Looney.Tunes.S1956E28.The.Honey.Mousers.DVDRip.x264.mkv")
	if err := os.WriteFile(path, make([]byte, 67<<20), 0o644); err != nil {
		t.Fatal(err)
	}
	g := &grabs.Grab{Mode: mode.Series, Title: "Looney Tunes", TMDBID: synth, TVDBID: 7266, SeasonNumber: 1956, EpisodeNumber: 28, Protocol: "usenet"}
	if err := refuseImportBelowFloor(ctx, libStore, settingsStore, sess, g, path, importQualityProber{
		height: 480, codec: "h264", duration: 15 * 60,
	}); err != nil {
		t.Fatalf("anthology TMDB + TVDB Animation must accept Honey-Mousers-sized DVDRip, got %v", err)
	}
}

func TestRefuseImportBelowFloor_LiveAction480FailsMedium(t *testing.T) {
	clearAnimationGenreCache(t)
	ctx := context.Background()
	_, _, settingsStore, _, libStore, _, _, _, _, _ := testStores(t)
	if err := settingsStore.Set(ctx, qualityTiersKey(mode.Series), `["medium","high","lossless"]`); err != nil {
		t.Fatal(err)
	}
	if _, err := libStore.UpsertSeries(ctx, library.Series{
		TMDBID: 1413, Title: "American Horror Story", Genres: []string{"Drama"},
	}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "Show.S01E01.DVDRip.x264.mkv")
	if err := os.WriteFile(path, make([]byte, 67<<20), 0o644); err != nil {
		t.Fatal(err)
	}
	g := &grabs.Grab{Mode: mode.Series, Title: "Show", TMDBID: 1413, SeasonNumber: 1, EpisodeNumber: 1, Protocol: "usenet"}
	err := refuseImportBelowFloor(ctx, libStore, settingsStore, nil, g, path, importQualityProber{
		height: 480, codec: "h264", duration: 15 * 60,
	})
	if err == nil || !errors.Is(err, autograb.ErrBelowQualityFloor) {
		t.Fatalf("live-action 480p Medium must still reject Honey-Mousers-sized file, got %v", err)
	}
}

func TestRefuseImportBelowFloor_SearchPickKeptAndStamped(t *testing.T) {
	ctx := context.Background()
	_, _, settingsStore, _, libStore, _, _, _, _, _ := testStores(t)
	if err := settingsStore.Set(ctx, qualityTiersKey(mode.Series), `["medium","high","lossless"]`); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "American.Horror.Story.S13E09.2160p.x265.mkv")
	if err := os.WriteFile(path, make([]byte, 1<<20), 0o644); err != nil {
		t.Fatal(err)
	}
	probe := importQualityProber{height: 2160, codec: "hevc", duration: 33 * 60}
	picked := &grabs.Grab{Mode: mode.Series, Title: "American Horror Story", TMDBID: 1413, SeasonNumber: 13, EpisodeNumber: 9, Protocol: "usenet", Origin: grabOriginSearchPick}
	if err := refuseImportBelowFloor(ctx, libStore, settingsStore, nil, picked, path, probe); err != nil {
		t.Fatalf("search-pick must be kept, got %v", err)
	}
	if got := searchPickLibraryTier(ctx, libStore, settingsStore, nil, picked, path, probe, "medium"); got != string(quality.Low) {
		t.Fatalf("stamped tier = %q, want %q", got, quality.Low)
	}
	auto := &grabs.Grab{Mode: mode.Series, Title: "American Horror Story", TMDBID: 1413, SeasonNumber: 13, EpisodeNumber: 9, Protocol: "usenet"}
	if err := refuseImportBelowFloor(ctx, libStore, settingsStore, nil, auto, path, probe); err == nil || !errors.Is(err, autograb.ErrBelowQualityFloor) {
		t.Fatalf("auto-grab must still reject, got %v", err)
	}
	if got := searchPickLibraryTier(ctx, libStore, settingsStore, nil, auto, path, probe, "medium"); got != "medium" {
		t.Fatalf("auto-grab stamp = %q, want medium", got)
	}
	// A downloaded torrent has no seeder count. Bitrate still decides the stamp.
	big := filepath.Join(t.TempDir(), "Movie.2020.2160p.REMUX.mkv")
	if err := os.WriteFile(big, make([]byte, 8<<20), 0o644); err != nil {
		t.Fatal(err)
	}
	torrent := &grabs.Grab{Mode: mode.Movies, Title: "Movie", TMDBID: 42, Protocol: "torrent", Origin: grabOriginSearchPick}
	torrentProbe := importQualityProber{height: 2160, codec: "hevc", duration: 1}
	if got := searchPickLibraryTier(ctx, libStore, settingsStore, nil, torrent, big, torrentProbe, "medium"); got != "medium" {
		t.Fatalf("torrent search-pick stamp = %q, want medium", got)
	}
}

func TestRefuseImportBelowFloor_AdultSkipped(t *testing.T) {
	ctx := context.Background()
	_, _, settingsStore, _, libStore, _, _, _, _, _ := testStores(t)
	path := filepath.Join(t.TempDir(), "scene.mp4")
	if err := os.WriteFile(path, make([]byte, 1<<20), 0o644); err != nil {
		t.Fatal(err)
	}
	g := &grabs.Grab{Mode: mode.Adult, Title: "Scene", Protocol: "usenet"}
	if err := refuseImportBelowFloor(ctx, libStore, settingsStore, nil, g, path, importQualityProber{
		height: 1080, codec: "hevc", duration: 24 * 60,
	}); err != nil {
		t.Fatalf("Adult must skip the movies/series floor, got %v", err)
	}
}

func TestRefuseImportBelowFloor_QualityStopgapKeptAndStamped(t *testing.T) {
	ctx := context.Background()
	_, _, settingsStore, _, libStore, _, _, _, _, _ := testStores(t)
	if err := settingsStore.Set(ctx, qualityTiersKey(mode.Series), `["medium","high","lossless"]`); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "Show.S01E01.1080p.x264.mkv")
	// 20 MiB / 120s ≈ 1.33 Mbps implied, padded 1.07: above the 0.8 mislabel
	// line, below Medium/1080 (5).
	if err := os.WriteFile(path, make([]byte, 20<<20), 0o644); err != nil {
		t.Fatal(err)
	}
	probe := importQualityProber{height: 1080, codec: "h264", duration: 120}
	stopgap := &grabs.Grab{Mode: mode.Series, Title: "Show", TMDBID: 1413, SeasonNumber: 1, EpisodeNumber: 1, Protocol: "usenet", Origin: grabOriginQualityStopgap}
	if err := refuseImportBelowFloor(ctx, libStore, settingsStore, nil, stopgap, path, probe); err != nil {
		t.Fatalf("quality-stopgap must be kept, got %v", err)
	}
	if got := searchPickLibraryTier(ctx, libStore, settingsStore, nil, stopgap, path, probe, "medium"); got != string(quality.Low) {
		t.Fatalf("stamped tier = %q, want %q", got, quality.Low)
	}
	auto := &grabs.Grab{Mode: mode.Series, Title: "Show", TMDBID: 1413, SeasonNumber: 1, EpisodeNumber: 1, Protocol: "usenet"}
	if err := refuseImportBelowFloor(ctx, libStore, settingsStore, nil, auto, path, probe); err == nil || !errors.Is(err, autograb.ErrBelowQualityFloor) {
		t.Fatalf("unmarked auto-grab must still reject, got %v", err)
	}
}

func TestRefuseImportBelowFloor_StopgapStillEnforcesMinResolution(t *testing.T) {
	ctx := context.Background()
	_, _, settingsStore, _, libStore, _, _, _, _, _ := testStores(t)
	if err := settingsStore.Set(ctx, minResolutionKey(mode.Movies), "1080"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "Movie.2020.720p.x264.mkv")
	if err := os.WriteFile(path, make([]byte, 8<<20), 0o644); err != nil {
		t.Fatal(err)
	}
	g := &grabs.Grab{Mode: mode.Movies, Title: "Movie", TMDBID: 42, Protocol: "usenet", Origin: grabOriginQualityStopgap}
	err := refuseImportBelowFloor(ctx, libStore, settingsStore, nil, g, path, importQualityProber{
		height: 720, codec: "h264", duration: 100 * 60,
	})
	if err == nil || !errors.Is(err, autograb.ErrBelowQualityFloor) {
		t.Fatalf("stopgap must still reject below min resolution, got %v", err)
	}
}

func TestEnableUpgradeWatchAfterStopgap(t *testing.T) {
	ctx := context.Background()
	_, _, _, _, libStore, _, _, _, _, _ := testStores(t)
	item, err := libStore.Upsert(ctx, library.Item{Mode: mode.Movies, TMDBID: 42, Title: "Movie", FilePath: "/movies/Movie.mkv"})
	if err != nil {
		t.Fatal(err)
	}
	enableUpgradeWatchAfterStopgap(ctx, libStore, &grabs.Grab{Mode: mode.Movies, Origin: grabOriginQualityStopgap}, item.ID, 0)
	got, err := libStore.GetByTMDBID(ctx, mode.Movies, 42)
	if err != nil {
		t.Fatal(err)
	}
	if !got.UpgradeWatch {
		t.Fatal("quality-stopgap import must turn upgrade-watch on")
	}
	plain, err := libStore.Upsert(ctx, library.Item{Mode: mode.Movies, TMDBID: 7, Title: "Other", FilePath: "/movies/Other.mkv"})
	if err != nil {
		t.Fatal(err)
	}
	enableUpgradeWatchAfterStopgap(ctx, libStore, &grabs.Grab{Mode: mode.Movies, Origin: ""}, plain.ID, 0)
	other, err := libStore.GetByTMDBID(ctx, mode.Movies, 7)
	if err != nil {
		t.Fatal(err)
	}
	if other.UpgradeWatch {
		t.Fatal("ordinary import must not turn upgrade-watch on")
	}
}

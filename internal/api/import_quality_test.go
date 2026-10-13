package api

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/labbersanon/sakms/internal/autograb"
	"github.com/labbersanon/sakms/internal/grabs"
	"github.com/labbersanon/sakms/internal/mediainfo"
	"github.com/labbersanon/sakms/internal/mode"
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

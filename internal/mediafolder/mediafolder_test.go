package mediafolder_test

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/labbersanon/sakms/internal/mediafolder"
	"github.com/labbersanon/sakms/internal/nfo"
)

func TestEnsureSeries_WritesNFO(t *testing.T) {
	dir := t.TempDir()
	ep := filepath.Join(dir, "Season 01", "Ancient Aliens - S01E01.mkv")
	if err := os.MkdirAll(filepath.Dir(ep), 0o755); err != nil {
		t.Fatal(err)
	}
	root := mediafolder.SeriesDirFromEpisode(ep)
	if root != dir {
		t.Fatalf("SeriesDirFromEpisode = %q, want %q", root, dir)
	}
	if mediafolder.HasLocalPoster(dir) {
		t.Fatal("expected no local poster")
	}

	art := mediafolder.Art{
		Title:  "Ancient Aliens",
		Year:   2009,
		Plot:   "Documentary series.",
		TMDBID: 32608,
		TVDBID: 101501,
		IMDBID: "tt1647504",
	}
	if err := mediafolder.EnsureSeries(context.Background(), nil, dir, art, false); err != nil {
		t.Fatal(err)
	}
	sn := nfo.ReadSeriesFile(filepath.Join(dir, mediafolder.SeriesNFOFile))
	if sn.TMDBID != 32608 || sn.TVDBID != 101501 || sn.Title != "Ancient Aliens" {
		t.Fatalf("nfo = %+v", sn)
	}
	if sn.IMDBID != "tt1647504" || !strings.Contains(sn.Plot, "Documentary") {
		t.Fatalf("nfo plot/imdb = %+v", sn)
	}
}

func TestEnsureMovie_SkipsExistingPosterUnlessForce(t *testing.T) {
	dir := t.TempDir()
	poster := filepath.Join(dir, mediafolder.PosterFile)
	if err := os.WriteFile(poster, []byte("existing"), 0o644); err != nil {
		t.Fatal(err)
	}
	art := mediafolder.Art{Title: "Film", Year: 2020, TMDBID: 1}
	if err := mediafolder.EnsureMovie(context.Background(), nil, dir, art, false); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(poster)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "existing" {
		t.Fatalf("poster rewritten without force: %q", b)
	}
	got, err := nfo.Read(filepath.Join(dir, mediafolder.MovieNFOFile))
	if err != nil {
		t.Fatal(err)
	}
	if got.TMDBID != 1 || got.Title != "Film" {
		t.Fatalf("movie nfo = %+v", got)
	}
}

func TestMovieDirFromFile(t *testing.T) {
	p := "/media/Movies/Foo (2020)/Foo (2020).mkv"
	if got := mediafolder.MovieDirFromFile(p); got != "/media/Movies/Foo (2020)" {
		t.Fatalf("got %q", got)
	}
}

func TestLocalPosterPath(t *testing.T) {
	dir := t.TempDir()
	if p := mediafolder.LocalPosterPath(dir); p != "" {
		t.Fatalf("want empty, got %q", p)
	}
	alt := filepath.Join(dir, "poster.jpg")
	if err := os.WriteFile(alt, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := mediafolder.LocalPosterPath(dir); got != alt {
		t.Fatalf("got %q want %q", got, alt)
	}
	if !mediafolder.HasLocalPoster(dir) {
		t.Fatal("HasLocalPoster false")
	}
}

func TestEnsureMovie_RejectsNonHTTPSPoster(t *testing.T) {
	dir := t.TempDir()
	art := mediafolder.Art{
		Title:  "X",
		TMDBID: 1,
		Poster: "http://example.com/p.jpg",
	}
	err := mediafolder.EnsureMovie(context.Background(), http.DefaultClient, dir, art, true)
	if err == nil || !strings.Contains(err.Error(), "non-https") {
		t.Fatalf("want non-https error, got %v", err)
	}
}

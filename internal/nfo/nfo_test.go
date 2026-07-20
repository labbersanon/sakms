package nfo_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/labbersanon/sakms/internal/nfo"
)

func writeNFO(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRead_FlatTmdbidField(t *testing.T) {
	d := t.TempDir()
	writeNFO(t, d, "movie.nfo", `<?xml version="1.0"?>
<movie>
  <title>The Dark Knight</title>
  <year>2008</year>
  <tmdbid>155</tmdbid>
</movie>`)
	m, err := nfo.Read(filepath.Join(d, "movie.nfo"))
	if err != nil {
		t.Fatal(err)
	}
	if m.TMDBID != 155 {
		t.Errorf("TMDBID: got %d, want 155", m.TMDBID)
	}
	if m.Title != "The Dark Knight" {
		t.Errorf("Title: got %q, want %q", m.Title, "The Dark Knight")
	}
	if m.Year != 2008 {
		t.Errorf("Year: got %d, want 2008", m.Year)
	}
}

func TestRead_UniqueidTypeTmdb(t *testing.T) {
	d := t.TempDir()
	writeNFO(t, d, "movie.nfo", `<?xml version="1.0"?>
<movie>
  <title>The Matrix</title>
  <year>1999</year>
  <uniqueid type="imdb">tt0133093</uniqueid>
  <uniqueid type="tmdb" default="true">603</uniqueid>
</movie>`)
	m, err := nfo.Read(filepath.Join(d, "movie.nfo"))
	if err != nil {
		t.Fatal(err)
	}
	if m.TMDBID != 603 {
		t.Errorf("TMDBID: got %d, want 603", m.TMDBID)
	}
}

func TestRead_FlatFieldTakesPrecedenceOverUniqueid(t *testing.T) {
	d := t.TempDir()
	writeNFO(t, d, "movie.nfo", `<?xml version="1.0"?>
<movie>
  <tmdbid>999</tmdbid>
  <uniqueid type="tmdb">111</uniqueid>
</movie>`)
	m, err := nfo.Read(filepath.Join(d, "movie.nfo"))
	if err != nil {
		t.Fatal(err)
	}
	if m.TMDBID != 999 {
		t.Errorf("flat <tmdbid> should win; got %d, want 999", m.TMDBID)
	}
}

func TestRead_MissingFile(t *testing.T) {
	_, err := nfo.Read("/nonexistent/path/movie.nfo")
	if err == nil {
		t.Error("expected error for missing file, got nil")
	}
}

func TestRead_MalformedXML(t *testing.T) {
	d := t.TempDir()
	writeNFO(t, d, "movie.nfo", `not xml at all <<<`)
	_, err := nfo.Read(filepath.Join(d, "movie.nfo"))
	if err == nil {
		t.Error("expected error for malformed XML, got nil")
	}
}

func TestReadSidecar_BasenameNFO(t *testing.T) {
	d := t.TempDir()
	writeNFO(t, d, "The.Matrix.1999.mkv.nfo", `<movie><tmdbid>603</tmdbid></movie>`)
	// simulate: video is "The.Matrix.1999.mkv", sidecar is same name + .nfo
	video := filepath.Join(d, "The.Matrix.1999.mkv")
	// base sidecar path strips .mkv and adds .nfo → The.Matrix.1999.nfo
	// but our file has double ext, so test the correct SidecarPaths result:
	nfoPath := nfo.SidecarPaths(video)[0]
	writeNFO(t, d, filepath.Base(nfoPath), `<movie><tmdbid>603</tmdbid></movie>`)
	m := nfo.ReadSidecar(video)
	if m.TMDBID != 603 {
		t.Errorf("got TMDBID %d, want 603", m.TMDBID)
	}
}

func TestReadSidecar_FolderNFO(t *testing.T) {
	d := t.TempDir()
	writeNFO(t, d, "movie.nfo", `<movie><tmdbid>155</tmdbid></movie>`)
	// no same-name sidecar — only movie.nfo exists
	video := filepath.Join(d, "The.Dark.Knight.2008.mkv")
	m := nfo.ReadSidecar(video)
	if m.TMDBID != 155 {
		t.Errorf("got TMDBID %d, want 155 (from movie.nfo fallback)", m.TMDBID)
	}
}

func TestReadSidecar_NoNFO(t *testing.T) {
	d := t.TempDir()
	video := filepath.Join(d, "Nomad.2021.mkv")
	m := nfo.ReadSidecar(video)
	if m.TMDBID != 0 {
		t.Errorf("expected zero MovieNFO, got TMDBID %d", m.TMDBID)
	}
}

func TestSidecarPaths_DistinctPaths(t *testing.T) {
	paths := nfo.SidecarPaths("/media/movies/Inception (2010)/Inception (2010) [tmdbid-27205].mkv")
	if len(paths) != 2 {
		t.Fatalf("expected 2 candidate paths, got %d", len(paths))
	}
	if filepath.Ext(paths[0]) != ".nfo" {
		t.Errorf("first path should be .nfo, got %q", paths[0])
	}
	if filepath.Base(paths[1]) != "movie.nfo" {
		t.Errorf("second path should be movie.nfo, got %q", filepath.Base(paths[1]))
	}
}

func TestSidecarPaths_DirectoryEntry(t *testing.T) {
	// ScanRootFolder returns the folder as the atomic entry for folder-based
	// movies. SidecarPaths must look inside the folder in that case.
	d := t.TempDir()
	movieDir := filepath.Join(d, "The Matrix (1999)")
	if err := os.Mkdir(movieDir, 0o755); err != nil {
		t.Fatal(err)
	}
	paths := nfo.SidecarPaths(movieDir)
	if len(paths) != 2 {
		t.Fatalf("expected 2 candidate paths, got %d", len(paths))
	}
	if paths[0] != filepath.Join(movieDir, "The Matrix (1999).nfo") {
		t.Errorf("first path: got %q, want dirname+.nfo", paths[0])
	}
	if paths[1] != filepath.Join(movieDir, "movie.nfo") {
		t.Errorf("second path: got %q, want movie.nfo", paths[1])
	}
}

func TestReadSidecar_DirectoryEntry(t *testing.T) {
	d := t.TempDir()
	movieDir := filepath.Join(d, "The Matrix (1999)")
	if err := os.Mkdir(movieDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(movieDir, "movie.nfo"),
		[]byte(`<movie><tmdbid>603</tmdbid></movie>`), 0o644); err != nil {
		t.Fatal(err)
	}
	m := nfo.ReadSidecar(movieDir)
	if m.TMDBID != 603 {
		t.Errorf("got TMDBID %d, want 603", m.TMDBID)
	}
}

// — Series tests ——————————————————————————————————————————————————————————————

func TestReadSeries_FlatTmdbidField(t *testing.T) {
	d := t.TempDir()
	writeNFO(t, d, "tvshow.nfo", `<?xml version="1.0"?>
<tvshow>
  <title>Breaking Bad</title>
  <year>2008</year>
  <tmdbid>1396</tmdbid>
</tvshow>`)
	s, err := nfo.ReadSeries(filepath.Join(d, "tvshow.nfo"))
	if err != nil {
		t.Fatal(err)
	}
	if s.TMDBID != 1396 {
		t.Errorf("TMDBID: got %d, want 1396", s.TMDBID)
	}
	if s.Title != "Breaking Bad" {
		t.Errorf("Title: got %q, want %q", s.Title, "Breaking Bad")
	}
	if s.Year != 2008 {
		t.Errorf("Year: got %d, want 2008", s.Year)
	}
}

func TestReadSeries_UniqueidTypeTmdb(t *testing.T) {
	d := t.TempDir()
	writeNFO(t, d, "tvshow.nfo", `<?xml version="1.0"?>
<tvshow>
  <title>Succession</title>
  <uniqueid type="imdb">tt4574334</uniqueid>
  <uniqueid type="tmdb" default="true">63333</uniqueid>
</tvshow>`)
	s, err := nfo.ReadSeries(filepath.Join(d, "tvshow.nfo"))
	if err != nil {
		t.Fatal(err)
	}
	if s.TMDBID != 63333 {
		t.Errorf("TMDBID: got %d, want 63333", s.TMDBID)
	}
}

func TestReadSeriesSidecar_TVShowNfoAtSeriesRoot(t *testing.T) {
	// Typical layout: series root / Season 01 / episode.mkv
	// tvshow.nfo lives at the series root, one level above the season folder.
	seriesRoot := t.TempDir()
	seasonDir := filepath.Join(seriesRoot, "Season 01")
	if err := os.Mkdir(seasonDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeNFO(t, seriesRoot, "tvshow.nfo", `<tvshow><tmdbid>1396</tmdbid></tvshow>`)
	video := filepath.Join(seasonDir, "Breaking.Bad.S01E01.mkv")
	s := nfo.ReadSeriesSidecar(video)
	if s.TMDBID != 1396 {
		t.Errorf("got TMDBID %d, want 1396 (from tvshow.nfo at series root)", s.TMDBID)
	}
}

func TestReadSeriesSidecar_TVShowNfoAtEpisodeDir(t *testing.T) {
	// tvshow.nfo lives in the same directory as the episode (flat layout).
	d := t.TempDir()
	writeNFO(t, d, "tvshow.nfo", `<tvshow><tmdbid>1399</tmdbid></tvshow>`)
	video := filepath.Join(d, "Game.of.Thrones.S01E01.mkv")
	s := nfo.ReadSeriesSidecar(video)
	if s.TMDBID != 1399 {
		t.Errorf("got TMDBID %d, want 1399 (from tvshow.nfo alongside episode)", s.TMDBID)
	}
}

func TestReadSeriesSidecar_EpisodeSidecarFallback(t *testing.T) {
	// No tvshow.nfo anywhere — fall back to episode-specific .nfo.
	d := t.TempDir()
	writeNFO(t, d, "Show.S02E03.nfo", `<tvshow><tmdbid>99999</tmdbid></tvshow>`)
	video := filepath.Join(d, "Show.S02E03.mkv")
	s := nfo.ReadSeriesSidecar(video)
	if s.TMDBID != 99999 {
		t.Errorf("got TMDBID %d, want 99999 (from episode sidecar)", s.TMDBID)
	}
}

func TestReadSeriesSidecar_NoNFO(t *testing.T) {
	d := t.TempDir()
	video := filepath.Join(d, "Show.S01E01.mkv")
	s := nfo.ReadSeriesSidecar(video)
	if s.TMDBID != 0 {
		t.Errorf("expected zero SeriesNFO, got TMDBID %d", s.TMDBID)
	}
}

func TestSeriesSidecarPaths_ThreeDistinctPaths(t *testing.T) {
	video := "/media/tv/Breaking Bad (2008)/Season 01/Breaking.Bad.S01E01.mkv"
	paths := nfo.SeriesSidecarPaths(video)
	if len(paths) != 3 {
		t.Fatalf("expected 3 candidate paths, got %d: %v", len(paths), paths)
	}
	if filepath.Base(paths[0]) != "tvshow.nfo" {
		t.Errorf("first path should be tvshow.nfo from parent dir, got %q", paths[0])
	}
	if filepath.Base(paths[1]) != "tvshow.nfo" {
		t.Errorf("second path should be tvshow.nfo from episode dir, got %q", paths[1])
	}
	if filepath.Ext(paths[2]) != ".nfo" {
		t.Errorf("third path should be episode sidecar (.nfo), got %q", paths[2])
	}
}

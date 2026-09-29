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

func TestScanImportMovies_DestIsLibraryRootNotSource(t *testing.T) {
	source := t.TempDir()
	dest := t.TempDir()
	seedMovieRelease(t, source, "A.Beautiful.Mind.2001.1080p.BluRay.x264-GROUP")

	sess := &mode.Session{Mode: mode.Movies, TMDB: fakeTMDBSearch(t, map[string]string{
		"A Beautiful Mind 2001": `{"results":[{"id":453,"title":"A Beautiful Mind","overview":"...","release_date":"2001-12-21"}]}`,
	})}
	libStore := newTestLibraryStore(t)

	got, err := ScanImportMovies(context.Background(), sess, libStore, source, dest, DefaultMatchConfig(), nil)
	if err != nil {
		t.Fatalf("ScanImportMovies: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 proposal, got %d: %+v", len(got), got)
	}
	p := got[0]
	if p.Status != proposals.Pending || p.Title != "A Beautiful Mind" || p.TMDBID != 453 {
		t.Fatalf("unexpected proposal: %+v", p)
	}
	if p.RootFolderPath != dest {
		t.Fatalf("RootFolderPath = %q, want library dest %q", p.RootFolderPath, dest)
	}
	wantDest := ImportDestPath(p, naming.Jellyfin)
	if wantDest == "" || filepath.Dir(filepath.Dir(wantDest)) != dest {
		t.Fatalf("ImportDestPath = %q, want under %q", wantDest, dest)
	}
}

func TestScanImportMovies_ApplyMovesFileIntoLibrary(t *testing.T) {
	source := t.TempDir()
	dest := t.TempDir()
	seedMovieRelease(t, source, "A.Beautiful.Mind.2001.1080p.BluRay.x264-GROUP")
	srcFile := filepath.Join(source, "A.Beautiful.Mind.2001.1080p.BluRay.x264-GROUP", "movie.mkv")

	sess := &mode.Session{Mode: mode.Movies, TMDB: fakeTMDBSearch(t, map[string]string{
		"A Beautiful Mind 2001": `{"results":[{"id":453,"title":"A Beautiful Mind","overview":"...","release_date":"2001-12-21"}]}`,
	})}
	libStore := newTestLibraryStore(t)

	got, err := ScanImportMovies(context.Background(), sess, libStore, source, dest, DefaultMatchConfig(), nil)
	if err != nil {
		t.Fatalf("ScanImportMovies: %v", err)
	}
	if len(got) != 1 || got[0].Status != proposals.Pending {
		t.Fatalf("scan = %+v", got)
	}

	id, changes, err := ApplyLibrary(context.Background(), libStore, got[0], naming.Jellyfin, "high", nil)
	if err != nil {
		t.Fatalf("ApplyLibrary: %v", err)
	}
	if id == 0 {
		t.Fatal("expected a library item id")
	}
	if _, err := os.Stat(srcFile); !os.IsNotExist(err) {
		t.Fatalf("source should be gone after move, stat err=%v", err)
	}
	item, err := libStore.GetByTMDBID(context.Background(), mode.Movies, 453)
	if err != nil {
		t.Fatalf("GetByTMDBID: %v", err)
	}
	if item.RootFolderPath != dest {
		t.Fatalf("library root = %q, want %q", item.RootFolderPath, dest)
	}
	if _, err := os.Stat(item.FilePath); err != nil {
		t.Fatalf("dest missing: %v", err)
	}
	if len(changes) == 0 {
		t.Fatal("expected Deleted+Created path changes for the move")
	}
}

func TestImportDestPath_EmptyWhenUnmatched(t *testing.T) {
	p := proposals.Proposal{Mode: mode.Movies, Status: proposals.Unmatched, Title: "X", RootFolderPath: "/media/movies"}
	if got := ImportDestPath(p, naming.Jellyfin); got != "" {
		t.Fatalf("unmatched dest = %q, want empty", got)
	}
}

func TestScanImportMovies_SkipsAlreadyTrackedPath(t *testing.T) {
	source := t.TempDir()
	dest := t.TempDir()
	seedMovieRelease(t, source, "A.Beautiful.Mind.2001.1080p.BluRay.x264-GROUP")
	srcFile := filepath.Join(source, "A.Beautiful.Mind.2001.1080p.BluRay.x264-GROUP", "movie.mkv")

	sess := &mode.Session{Mode: mode.Movies, TMDB: fakeTMDBSearch(t, map[string]string{
		"A Beautiful Mind 2001": `{"results":[{"id":453,"title":"A Beautiful Mind","release_date":"2001-12-21"}]}`,
	})}
	libStore := newTestLibraryStore(t)
	if _, err := libStore.Upsert(context.Background(), library.Item{
		Mode: mode.Movies, TMDBID: 453, Title: "A Beautiful Mind", FilePath: srcFile, RootFolderPath: dest,
	}); err != nil {
		t.Fatal(err)
	}

	got, err := ScanImportMovies(context.Background(), sess, libStore, source, dest, DefaultMatchConfig(), nil)
	if err != nil {
		t.Fatalf("ScanImportMovies: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected skip of tracked path, got %+v", got)
	}
}

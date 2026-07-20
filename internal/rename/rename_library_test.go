package rename

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/labbersanon/sakms/internal/db"
	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/naming"
	"github.com/labbersanon/sakms/internal/ollama"
	"github.com/labbersanon/sakms/internal/proposals"
	"github.com/labbersanon/sakms/internal/searchterm"
	"github.com/labbersanon/sakms/internal/tmdb"
)

func newTestLibraryStore(t *testing.T) *library.Store {
	t.Helper()
	sqlDB, err := db.Open(filepath.Join(t.TempDir(), "sakms.db"))
	if err != nil {
		t.Fatalf("opening db: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	return library.New(sqlDB)
}

// fakeTMDBSearch returns a *tmdb.Client whose /search/movie endpoint returns
// one result per search term found in results (raw movie-shaped JSON,
// keyed by the exact query string it expects).
func fakeTMDBSearch(t *testing.T, results map[string]string) *tmdb.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		term := r.URL.Query().Get("query")
		// enrichment calls (credits, details) carry no "query" param — return 404
		// so the soft-fail paths in rename.go leave genres/cast as empty slices.
		if term == "" {
			http.NotFound(w, r)
			return
		}
		body, ok := results[term]
		if !ok {
			t.Fatalf("unexpected search term %q", term)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return tmdb.New(tmdb.Config{BaseURL: srv.URL, APIKey: "test-key"}, srv.Client())
}

func TestScanLibrary_ProducesPendingProposalForNewItem(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "A.Beautiful.Mind.2001.1080p.BluRay.x264-GROUP"), 0o755); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sess := &mode.Session{Mode: mode.Movies, TMDB: fakeTMDBSearch(t, map[string]string{
		"A Beautiful Mind 2001": `{"results":[{"id":453,"title":"A Beautiful Mind","overview":"...","release_date":"2001-12-21"}]}`,
	})}
	libStore := newTestLibraryStore(t)

	got, err := ScanLibrary(context.Background(), sess, libStore, root, naming.Jellyfin, DefaultConfidenceThreshold)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 proposal, got %d: %+v", len(got), got)
	}
	p := got[0]
	if p.Status != proposals.Pending || p.Title != "A Beautiful Mind" || p.TMDBID != 453 {
		t.Errorf("unexpected proposal: %+v", p)
	}
	if p.RootFolderPath != root {
		t.Errorf("expected the proposal to stay in the general root, got %q", p.RootFolderPath)
	}
}

func TestScanLibrary_RequiresTMDBConfigured(t *testing.T) {
	sess := &mode.Session{Mode: mode.Movies}
	if _, err := ScanLibrary(context.Background(), sess, newTestLibraryStore(t), t.TempDir(), naming.Jellyfin, DefaultConfidenceThreshold); err == nil {
		t.Fatal("expected an error when TMDB isn't configured")
	}
}

func TestScanLibrary_RequiresRootFolderPath(t *testing.T) {
	sess := &mode.Session{Mode: mode.Movies, TMDB: fakeTMDBSearch(t, nil)}
	if _, err := ScanLibrary(context.Background(), sess, newTestLibraryStore(t), "", naming.Jellyfin, DefaultConfidenceThreshold); err == nil {
		t.Fatal("expected an error when no root folder path is configured")
	}
}

func TestScanLibrary_MarksUnmatchedForAlreadyInLibrary(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "A.Beautiful.Mind.2001.1080p.BluRay.x264-GROUP"), 0o755); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sess := &mode.Session{Mode: mode.Movies, TMDB: fakeTMDBSearch(t, map[string]string{
		"A Beautiful Mind 2001": `{"results":[{"id":453,"title":"A Beautiful Mind"}]}`,
	})}
	libStore := newTestLibraryStore(t)
	if _, err := libStore.Upsert(context.Background(), library.Item{
		Mode: mode.Movies, TMDBID: 453, Title: "A Beautiful Mind", FilePath: "/elsewhere/movie.mkv", RootFolderPath: "/elsewhere",
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := ScanLibrary(context.Background(), sess, libStore, root, naming.Jellyfin, DefaultConfidenceThreshold)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0].Status != proposals.Unmatched {
		t.Fatalf("expected the duplicate to surface as unmatched rather than re-adding it, got %+v", got)
	}
}

func TestScanLibrary_MarksUnmatchedWhenNoTMDBMatch(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "xyz123"), 0o755); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sess := &mode.Session{Mode: mode.Movies, TMDB: fakeTMDBSearch(t, map[string]string{
		"xyz123": `{"results":[]}`,
	})}

	got, err := ScanLibrary(context.Background(), sess, newTestLibraryStore(t), root, naming.Jellyfin, DefaultConfidenceThreshold)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0].Status != proposals.Unmatched {
		t.Fatalf("expected 1 unmatched proposal, got %+v", got)
	}
}

// TestScanLibrary_MarksUnmatchedWhenTMDBResultIsWeakMatch proves the
// confidence gate itself, not just its unit-level scoring function: a
// garbled/opaque search term that still returns SOME TMDB result (unlike
// the zero-results case above) is routed to Unmatched instead of silently
// accepting a confidently-wrong top hit.
func TestScanLibrary_MarksUnmatchedWhenTMDBResultIsWeakMatch(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "FathersLLDVD"), 0o755); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sess := &mode.Session{Mode: mode.Movies, TMDB: fakeTMDBSearch(t, map[string]string{
		"FathersLLDVD": `{"results":[{"id":999,"title":"Father's Day","overview":"...","release_date":"1997-05-09"}]}`,
	})}

	got, err := ScanLibrary(context.Background(), sess, newTestLibraryStore(t), root, naming.Jellyfin, DefaultConfidenceThreshold)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0].Status != proposals.Unmatched {
		t.Fatalf("expected the weak match to route to unmatched, got %+v", got)
	}
	if got[0].TMDBID != 0 {
		t.Errorf("expected no TMDB id to be assigned on a rejected weak match, got %d", got[0].TMDBID)
	}
}

// TestScanLibrary_ThresholdZeroAcceptsAnyTMDBResult proves the threshold
// parameter is actually load-bearing (not a hardcoded gate): a threshold of
// 0 must let even a zero-similarity result through, same as today's
// pre-feature unconditional-items[0] behavior.
func TestScanLibrary_ThresholdZeroAcceptsAnyTMDBResult(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "FathersLLDVD"), 0o755); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sess := &mode.Session{Mode: mode.Movies, TMDB: fakeTMDBSearch(t, map[string]string{
		"FathersLLDVD": `{"results":[{"id":999,"title":"Father's Day","overview":"...","release_date":"1997-05-09"}]}`,
	})}

	got, err := ScanLibrary(context.Background(), sess, newTestLibraryStore(t), root, naming.Jellyfin, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0].Status != proposals.Pending || got[0].TMDBID != 999 {
		t.Fatalf("expected a threshold of 0 to accept the weak match, got %+v", got)
	}
}

// TestScanLibrary_DiscoversNewFileAlongsideAlreadyTrackedItem proves
// ScanRootFolder's recursion: once a movie folder has one already-tracked
// file inside it, the folder is no longer atomic — a second, new file
// dropped in beside it surfaces individually rather than being masked by
// the whole folder having previously been marked known.
func TestScanLibrary_DiscoversNewFileAlongsideAlreadyTrackedItem(t *testing.T) {
	root := t.TempDir()
	movieDir := filepath.Join(root, "A.Beautiful.Mind.2001.1080p.BluRay.x264-GROUP")
	if err := os.MkdirAll(movieDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	tracked := filepath.Join(movieDir, "movie.mkv")
	if err := os.WriteFile(tracked, []byte("x"), 0o644); err != nil {
		t.Fatalf("writing movie.mkv: %v", err)
	}
	newFile := filepath.Join(movieDir, "extended-cut.mkv")
	if err := os.WriteFile(newFile, []byte("x"), 0o644); err != nil {
		t.Fatalf("writing extended-cut.mkv: %v", err)
	}

	term := searchterm.FromName("extended-cut.mkv")
	sess := &mode.Session{Mode: mode.Movies, TMDB: fakeTMDBSearch(t, map[string]string{
		term: `{"results":[]}`,
	})}
	libStore := newTestLibraryStore(t)
	if _, err := libStore.Upsert(context.Background(), library.Item{
		Mode: mode.Movies, TMDBID: 453, Title: "A Beautiful Mind", FilePath: tracked, RootFolderPath: root,
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := ScanLibrary(context.Background(), sess, libStore, root, naming.Jellyfin, DefaultConfidenceThreshold)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0].SourcePath != newFile {
		t.Fatalf("expected only the new file (not the already-tracked movie.mkv) to surface, got %+v", got)
	}
}

// TestScanLibrary_SkipsAlreadyConformantEntry proves the schema-conformance
// filter: an on-disk entry that already matches the active naming preset —
// even one never recorded in libStore, e.g. a library someone already
// organized by hand — is never proposed, while a non-conformant sibling in
// the same root still is.
func TestScanLibrary_SkipsAlreadyConformantEntry(t *testing.T) {
	root := t.TempDir()
	conformantDir := filepath.Join(root, "Some Movie (2020) [tmdbid-42]")
	if err := os.MkdirAll(conformantDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(conformantDir, "Some Movie (2020) [tmdbid-42].mkv"), []byte("x"), 0o644); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	nonConformantDir := filepath.Join(root, "A.Beautiful.Mind.2001.1080p.BluRay.x264-GROUP")
	if err := os.MkdirAll(nonConformantDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(nonConformantDir, "movie.mkv"), []byte("x"), 0o644); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sess := &mode.Session{Mode: mode.Movies, TMDB: fakeTMDBSearch(t, map[string]string{
		"A Beautiful Mind 2001": `{"results":[{"id":453,"title":"A Beautiful Mind"}]}`,
	})}

	got, err := ScanLibrary(context.Background(), sess, newTestLibraryStore(t), root, naming.Jellyfin, DefaultConfidenceThreshold)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0].SourcePath != nonConformantDir {
		t.Fatalf("expected only the non-conformant entry proposed, got %+v", got)
	}
}

func TestScanLibrary_SkipsSidecarFiles(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "poster.jpg"), []byte("x"), 0o644); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sess := &mode.Session{Mode: mode.Movies, TMDB: fakeTMDBSearch(t, nil)}
	got, err := ScanLibrary(context.Background(), sess, newTestLibraryStore(t), root, naming.Jellyfin, DefaultConfidenceThreshold)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected sidecar file to be skipped entirely, got %+v", got)
	}
}

func TestScanLibrary_RoutesKidsClassifiedContentToKidsRoot(t *testing.T) {
	generalRoot := t.TempDir()
	kidsRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(generalRoot, "Kids.Movie.2020"), 0o755); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sess := &mode.Session{Mode: mode.Movies, KidsRootPath: kidsRoot, TMDB: fakeTMDBSearch(t, map[string]string{
		"Kids Movie 2020": `{"results":[{"id":111,"title":"Kids Movie","overview":"A fun kids movie."}]}`,
	})}
	aiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"message": map[string]any{"content": `{"kids":true}`}})
	}))
	defer aiSrv.Close()
	sess.MainstreamAI = ollama.New(aiSrv.URL, "test-model", aiSrv.Client())

	got, err := ScanLibrary(context.Background(), sess, newTestLibraryStore(t), generalRoot, naming.Jellyfin, DefaultConfidenceThreshold)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0].Status != proposals.Pending || got[0].RootFolderPath != kidsRoot {
		t.Fatalf("expected the proposal to be routed to the Kids root, got %+v", got)
	}
}

func TestScanLibrary_NoRerouteWithoutMainstreamAI(t *testing.T) {
	generalRoot := t.TempDir()
	kidsRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(generalRoot, "Kids.Movie.2020"), 0o755); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sess := &mode.Session{Mode: mode.Movies, KidsRootPath: kidsRoot, TMDB: fakeTMDBSearch(t, map[string]string{
		"Kids Movie 2020": `{"results":[{"id":111,"title":"Kids Movie","overview":"A fun kids movie."}]}`,
	})}

	got, err := ScanLibrary(context.Background(), sess, newTestLibraryStore(t), generalRoot, naming.Jellyfin, DefaultConfidenceThreshold)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0].RootFolderPath != generalRoot {
		t.Fatalf("expected no reroute without a configured MainstreamAI, got %+v", got)
	}
}

func TestApplyLibrary_RelocatesFileAndRecordsInLibrary(t *testing.T) {
	base := t.TempDir()
	sourceRoot := filepath.Join(base, "incoming")
	destRoot := filepath.Join(base, "Movies")
	if err := os.MkdirAll(sourceRoot, 0o755); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := os.MkdirAll(destRoot, 0o755); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sourcePath := filepath.Join(sourceRoot, "Movie.mkv")
	if err := os.WriteFile(sourcePath, []byte("fake video data"), 0o644); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	libStore := newTestLibraryStore(t)
	p := proposals.Proposal{
		ID: 1, Status: proposals.Pending, Title: "Some Movie", TMDBID: 453, Year: 2020,
		SourcePath: sourcePath, RootFolderPath: destRoot,
	}
	id, changes, err := ApplyLibrary(context.Background(), libStore, p, naming.Jellyfin)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id == 0 {
		t.Error("expected a nonzero library item id")
	}

	wantDest := filepath.Join(destRoot, "Some Movie (2020) [tmdbid-453]", "Some Movie (2020) [tmdbid-453].mkv")
	if _, err := os.Stat(sourcePath); !os.IsNotExist(err) {
		t.Errorf("expected the source file to be gone, stat returned: %v", err)
	}
	if data, err := os.ReadFile(wantDest); err != nil || string(data) != "fake video data" {
		t.Errorf("expected the file to have moved to %q intact, err=%v data=%q", wantDest, err, data)
	}

	item, err := libStore.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if item.TMDBID != 453 || item.Title != "Some Movie" || item.Year != 2020 || item.FilePath != wantDest {
		t.Errorf("unexpected library item: %+v", item)
	}

	// Row 1 (player-rescan-notify plan): the Deleted side is the resolved
	// VIDEO FILE (sourcePath here, since it's the file directly, not a
	// wrapping directory), never p.SourcePath re-derived some other way;
	// the Created side is the actual returned destPath, verbatim.
	want := []mode.PathChange{{Path: sourcePath, Kind: mode.Deleted}, {Path: wantDest, Kind: mode.Created}}
	if len(changes) != 2 || changes[0] != want[0] || changes[1] != want[1] {
		t.Errorf("expected changes %+v, got %+v", want, changes)
	}
}

func TestApplyLibrary_RejectsNonPendingProposal(t *testing.T) {
	libStore := newTestLibraryStore(t)
	for _, status := range []proposals.Status{proposals.Applied, proposals.Dismissed, proposals.Unmatched} {
		if _, _, err := ApplyLibrary(context.Background(), libStore, proposals.Proposal{Status: status}, naming.Jellyfin); err == nil {
			t.Errorf("expected ApplyLibrary to refuse a %q proposal", status)
		}
	}
}

// TestApplyLibrary_NoMoveWhenAlreadyCorrectlyPlaced proves RelocateMovie's
// self-collision guard: if a file already sits exactly at the
// preset-computed destination (e.g. Apply is run again, or Scan's schema
// filter let something through that was already conformant), ApplyLibrary
// doesn't needlessly move it — comparing the computed destination against
// the source path up front, rather than always calling os.Rename, avoids
// place.UniquePath mistaking the file for colliding with itself.
func TestApplyLibrary_NoMoveWhenAlreadyCorrectlyPlaced(t *testing.T) {
	base := t.TempDir()
	folder := filepath.Join(base, "Movie [tmdbid-1]")
	sourcePath := filepath.Join(folder, "Movie [tmdbid-1].mkv")
	if err := os.MkdirAll(folder, 0o755); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := os.WriteFile(sourcePath, []byte("data"), 0o644); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	libStore := newTestLibraryStore(t)
	p := proposals.Proposal{
		ID: 1, Status: proposals.Pending, Title: "Movie", TMDBID: 1,
		SourcePath: sourcePath, RootFolderPath: base,
	}
	id, changes, err := ApplyLibrary(context.Background(), libStore, p, naming.Jellyfin)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := os.Stat(sourcePath); err != nil {
		t.Errorf("expected the file to stay in place (already correctly named), got: %v", err)
	}
	item, err := libStore.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if item.FilePath != sourcePath {
		t.Errorf("expected the recorded file path to be the unchanged source path, got %q", item.FilePath)
	}
	// No physical move happened, so no bogus Deleted+Created pair for the
	// same unchanged path should be reported.
	if len(changes) != 0 {
		t.Errorf("expected zero PathChanges when the file didn't move, got %+v", changes)
	}
}

// fakeTMDBMovieDetails returns a *tmdb.Client whose /movie/{id} endpoint
// returns movieJSON for the given id, and rejects /search/movie (the NFO
// fast-path must not fall through to the search).
func fakeTMDBMovieDetails(t *testing.T, id int, movieJSON string) *tmdb.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/search/movie" {
			t.Fatalf("NFO fast-path should not call /search/movie")
		}
		// enrichment calls (credits, etc.) — soft-fail is fine, return 404
		if r.URL.Path != "/movie/"+strconv.Itoa(id) {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(movieJSON))
	}))
	t.Cleanup(srv.Close)
	return tmdb.New(tmdb.Config{BaseURL: srv.URL, APIKey: "test-key"}, srv.Client())
}

func TestScanLibrary_NFOSidecarSkipsFuzzySearch(t *testing.T) {
	root := t.TempDir()
	// ScanRootFolder returns the directory as the atomic entry — the .nfo
	// must live inside the directory, not alongside it.
	dir := filepath.Join(root, "The.Matrix.1999.BluRay")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	videoPath := filepath.Join(dir, "The.Matrix.1999.BluRay.mkv")
	if err := os.WriteFile(videoPath, []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}
	// Same-name .nfo sidecar inside the folder — matches SidecarPaths' first
	// candidate when entryPath is a directory.
	nfoPath := filepath.Join(dir, "The.Matrix.1999.BluRay.nfo")
	if err := os.WriteFile(nfoPath, []byte(`<movie><tmdbid>603</tmdbid></movie>`), 0o644); err != nil {
		t.Fatal(err)
	}

	sess := &mode.Session{
		Mode: mode.Movies,
		TMDB: fakeTMDBMovieDetails(t, 603,
			`{"id":603,"title":"The Matrix","release_date":"1999-03-31","overview":"A hacker discovers reality."}`),
	}

	got, err := ScanLibrary(context.Background(), sess, newTestLibraryStore(t), root, naming.Jellyfin, DefaultConfidenceThreshold)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 proposal, got %d", len(got))
	}
	p := got[0]
	if p.Status != proposals.Pending {
		t.Errorf("expected Pending, got %v: %s", p.Status, p.Reason)
	}
	if p.TMDBID != 603 {
		t.Errorf("expected TMDBID 603, got %d", p.TMDBID)
	}
	if p.Title != "The Matrix" {
		t.Errorf("expected title %q, got %q", "The Matrix", p.Title)
	}
}

func TestScanLibrary_NFODuplicateMarkedUnmatched(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "The.Matrix.1999.BluRay")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	videoPath := filepath.Join(dir, "The.Matrix.1999.BluRay.mkv")
	if err := os.WriteFile(videoPath, []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}
	// .nfo inside the directory — same placement as the skip-fuzzy-search test.
	nfoPath := filepath.Join(dir, "The.Matrix.1999.BluRay.nfo")
	if err := os.WriteFile(nfoPath, []byte(`<movie><tmdbid>603</tmdbid></movie>`), 0o644); err != nil {
		t.Fatal(err)
	}

	libStore := newTestLibraryStore(t)
	if _, err := libStore.Upsert(context.Background(), library.Item{
		Mode: mode.Movies, TMDBID: 603, Title: "The Matrix", FilePath: "/other/matrix.mkv",
	}); err != nil {
		t.Fatalf("seeding library: %v", err)
	}

	// The TMDB server is never reached — the duplicate check fires before
	// the MovieDetails call. An unreachable base URL proves this.
	sess := &mode.Session{
		Mode: mode.Movies,
		TMDB: tmdb.New(tmdb.Config{BaseURL: "http://127.0.0.1:0", APIKey: "test"}, nil),
	}

	got, err := ScanLibrary(context.Background(), sess, libStore, root, naming.Jellyfin, DefaultConfidenceThreshold)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 proposal, got %d", len(got))
	}
	if got[0].Status != proposals.Unmatched {
		t.Errorf("expected Unmatched for duplicate TMDB id from .nfo, got %v", got[0].Status)
	}
}

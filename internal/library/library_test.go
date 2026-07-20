package library

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/labbersanon/sakms/internal/db"
	"github.com/labbersanon/sakms/internal/mode"
)

// newTestStore builds a Store against a real, freshly migrated SQLite file —
// exercising the actual SQL, not a mock, matching every other store test in
// this repo.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	sqlDB, err := db.Open(filepath.Join(t.TempDir(), "sakms.db"))
	if err != nil {
		t.Fatalf("opening db: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	return New(sqlDB)
}

func TestUpsert_CreatesThenUpdatesInPlace(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	created, err := s.Upsert(ctx, Item{
		Mode: mode.Movies, TMDBID: 100, Title: "Some Movie", Year: 2020,
		FilePath: "/movies/Some Movie/movie.mkv", RootFolderPath: "/movies",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if created.ID == 0 || created.CreatedAt == "" || created.UpdatedAt == "" {
		t.Fatalf("expected id/timestamps populated, got %+v", created)
	}

	updated, err := s.Upsert(ctx, Item{
		Mode: mode.Movies, TMDBID: 100, Title: "Some Movie (Updated)", Year: 2020,
		FilePath: "/movies/Some Movie (2020)/movie.mkv", RootFolderPath: "/movies",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if updated.ID != created.ID {
		t.Errorf("expected the same row to be updated (id %d), got id %d", created.ID, updated.ID)
	}
	if updated.Title != "Some Movie (Updated)" {
		t.Errorf("expected title to be updated, got %q", updated.Title)
	}

	all, err := s.List(ctx, mode.Movies)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("expected upsert to replace, not duplicate — got %d rows", len(all))
	}
}

func TestUpsert_RoundTripsPHashIdentity(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	created, err := s.Upsert(ctx, Item{
		Mode: mode.Movies, TMDBID: 700, Title: "Cached Movie", RootFolderPath: "/movies",
		FilePath: "/movies/Cached Movie/movie.mkv",
		PHash:    "phash64/5f:deadbeef", PHashFileSize: 12345, PHashFileMTime: "2026-07-10T00:00:00Z",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := s.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.PHash != "phash64/5f:deadbeef" || got.PHashFileSize != 12345 || got.PHashFileMTime != "2026-07-10T00:00:00Z" {
		t.Errorf("expected phash identity to round-trip, got %+v", got)
	}
}

func TestUpdatePHash_UpdatesInPlaceAndNoOpOnMissing(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	item, err := s.Upsert(ctx, Item{
		Mode: mode.Movies, TMDBID: 701, Title: "Movie", RootFolderPath: "/movies",
		FilePath: "/movies/Movie/movie.mkv",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if item.PHash != "" {
		t.Fatalf("expected an uncached item to start with an empty phash, got %q", item.PHash)
	}

	if err := s.UpdatePHash(ctx, item.ID, "phash64/5f:cafe", 999, "2026-07-10T12:00:00Z"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got, err := s.Get(ctx, item.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.PHash != "phash64/5f:cafe" || got.PHashFileSize != 999 || got.PHashFileMTime != "2026-07-10T12:00:00Z" {
		t.Errorf("expected UpdatePHash to persist the new hash + identity, got %+v", got)
	}
	// The targeted write must leave the rest of the row intact.
	if got.Title != "Movie" || got.FilePath != "/movies/Movie/movie.mkv" {
		t.Errorf("expected UpdatePHash to leave other columns untouched, got %+v", got)
	}

	if err := s.UpdatePHash(ctx, 999999, "x", 1, "y"); err != nil {
		t.Errorf("expected updating a nonexistent id to be a no-op, got %v", err)
	}
}

func TestGetByTMDBID_ScopedByMode(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if _, err := s.Upsert(ctx, Item{Mode: mode.Movies, TMDBID: 200, Title: "Movie", RootFolderPath: "/movies"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, err := s.GetByTMDBID(ctx, mode.Movies, 200); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := s.GetByTMDBID(ctx, mode.Series, 200); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound for a different mode with the same tmdb id, got %v", err)
	}
}

func TestGet_NotFound(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.Get(context.Background(), 999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestList_EmptyIsNotNil(t *testing.T) {
	s := newTestStore(t)
	got, err := s.List(context.Background(), mode.Movies)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got == nil {
		t.Error("expected an empty slice, not nil, so it serializes as [] not null")
	}
}

func TestDelete_CascadesTags(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	item, err := s.Upsert(ctx, Item{Mode: mode.Movies, TMDBID: 300, Title: "Movie", RootFolderPath: "/movies"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := s.AddTag(ctx, item.ID, "kids"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if err := s.Delete(ctx, item.ID); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	tags, err := s.Tags(ctx, item.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tags) != 0 {
		t.Errorf("expected tags to cascade-delete with the item, got %v", tags)
	}
}

func TestDelete_NotFoundIsNotAnError(t *testing.T) {
	s := newTestStore(t)
	if err := s.Delete(context.Background(), 999); err != nil {
		t.Fatalf("expected deleting a nonexistent id to be a no-op, got %v", err)
	}
}

func TestTags_AddIsIdempotentAndRemoveWorks(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	item, err := s.Upsert(ctx, Item{Mode: mode.Movies, TMDBID: 400, Title: "Movie", RootFolderPath: "/movies"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if err := s.AddTag(ctx, item.ID, "favorite"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := s.AddTag(ctx, item.ID, "favorite"); err != nil {
		t.Fatalf("adding the same tag twice should be a no-op, got error: %v", err)
	}

	tags, err := s.Tags(ctx, item.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tags) != 1 || tags[0] != "favorite" {
		t.Fatalf("expected exactly one tag, got %v", tags)
	}

	if err := s.RemoveTag(ctx, item.ID, "favorite"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := s.RemoveTag(ctx, item.ID, "not-there"); err != nil {
		t.Fatalf("removing a tag that isn't assigned should be a no-op, got error: %v", err)
	}

	tags, err = s.Tags(ctx, item.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tags) != 0 {
		t.Errorf("expected no tags after removal, got %v", tags)
	}
}

func TestTagVocabulary_DistinctAcrossItemsScopedByMode(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	movieA, err := s.Upsert(ctx, Item{Mode: mode.Movies, TMDBID: 500, Title: "A", RootFolderPath: "/movies"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	movieB, err := s.Upsert(ctx, Item{Mode: mode.Movies, TMDBID: 501, Title: "B", RootFolderPath: "/movies"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	seriesA, err := s.Upsert(ctx, Item{Mode: mode.Series, TMDBID: 502, Title: "C", RootFolderPath: "/tv"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if err := s.AddTag(ctx, movieA.ID, "kids"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := s.AddTag(ctx, movieB.ID, "kids"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := s.AddTag(ctx, movieB.ID, "favorite"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := s.AddTag(ctx, seriesA.ID, "documentary"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	vocab, err := s.TagVocabulary(ctx, mode.Movies)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(vocab) != 2 || vocab[0] != "favorite" || vocab[1] != "kids" {
		t.Fatalf("expected [favorite kids], got %v", vocab)
	}
}

func TestScanRootFolder_SkipsKnownAndSidecarFiles(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"New Movie (2024)", "Already Tracked Movie"} {
		if err := os.Mkdir(filepath.Join(dir, name), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", name, err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "poster.jpg"), []byte("x"), 0o644); err != nil {
		t.Fatalf("writing poster.jpg: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "subs.srt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("writing subs.srt: %v", err)
	}

	known := map[string]bool{filepath.Join(dir, "Already Tracked Movie"): true}
	entries, err := ScanRootFolder(dir, known)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(entries) != 1 || entries[0].Name != "New Movie (2024)" {
		t.Fatalf("expected only the one genuinely unmapped entry, got %+v", entries)
	}
}

func TestScanRootFolder_MissingRootGivesActionableMountMessage(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "does-not-exist")

	_, err := ScanRootFolder(missing, nil)
	if err == nil {
		t.Fatal("expected an error for a missing root folder, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "unreadable") || !strings.Contains(msg, "mounted") {
		t.Errorf("expected an actionable mount-disconnect message, got: %s", msg)
	}
	if !strings.Contains(msg, missing) {
		t.Errorf("expected the message to name the unreachable path %s, got: %s", missing, msg)
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("expected the wrapped error to still satisfy errors.Is(err, fs.ErrNotExist), got: %v", err)
	}
}

func TestClassifyScanErr_MountDisconnectErrnosGetActionableMessage(t *testing.T) {
	root := "/mnt/example"
	mountErrnos := []syscall.Errno{syscall.ENOTCONN, syscall.ESTALE, syscall.EIO, syscall.EHOSTUNREACH}
	for _, errno := range mountErrnos {
		t.Run(errno.Error(), func(t *testing.T) {
			wrapped := &fs.PathError{Op: "lstat", Path: root, Err: errno}
			got := classifyScanErr(root, wrapped)
			msg := got.Error()
			if !strings.Contains(msg, "unreadable") || !strings.Contains(msg, "mounted") {
				t.Errorf("expected an actionable mount-disconnect message for %v, got: %s", errno, msg)
			}
			if !errors.Is(got, errno) {
				t.Errorf("expected errors.Is(got, %v) to hold through the wrap, got: %v", errno, got)
			}
		})
	}
}

func TestClassifyScanErr_OtherErrorsStayGeneric(t *testing.T) {
	root := "/mnt/example"
	wrapped := &fs.PathError{Op: "lstat", Path: root, Err: syscall.EACCES}
	got := classifyScanErr(root, wrapped)
	msg := got.Error()
	if strings.Contains(msg, "mounted") {
		t.Errorf("did not expect the mount-specific message for a permission error, got: %s", msg)
	}
	if !errors.Is(got, syscall.EACCES) {
		t.Errorf("expected errors.Is(got, syscall.EACCES) to hold through the wrap, got: %v", got)
	}
}

func TestScanRootFolder_RecursesIntoOrganizationalDirectories(t *testing.T) {
	dir := t.TempDir()
	season1 := filepath.Join(dir, "Show Title", "Season 01")
	if err := os.MkdirAll(season1, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(season1, "ep2.mkv"), []byte("x"), 0o644); err != nil {
		t.Fatalf("writing ep2.mkv: %v", err)
	}
	tracked := filepath.Join(season1, "ep1.mkv")
	if err := os.WriteFile(tracked, []byte("x"), 0o644); err != nil {
		t.Fatalf("writing ep1.mkv: %v", err)
	}

	// "Show Title" has no direct video files of its own — nothing marks it
	// known, so ScanRootFolder must open it up rather than reporting the
	// whole show folder (which would hide ep2.mkv, added after ep1 was
	// already tracked, forever).
	known := map[string]bool{tracked: true}
	entries, err := ScanRootFolder(dir, known)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 1 || entries[0].Name != "ep2.mkv" {
		t.Fatalf("expected only the new episode file surfaced individually, got %+v", entries)
	}
}

func TestScanRootFolder_ReportsNewFileAlongsideKnownFile(t *testing.T) {
	dir := t.TempDir()
	movieDir := filepath.Join(dir, "Title (2024)")
	if err := os.MkdirAll(movieDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	tracked := filepath.Join(movieDir, "movie.mkv")
	if err := os.WriteFile(tracked, []byte("x"), 0o644); err != nil {
		t.Fatalf("writing movie.mkv: %v", err)
	}
	if err := os.WriteFile(filepath.Join(movieDir, "extended-cut.mkv"), []byte("x"), 0o644); err != nil {
		t.Fatalf("writing extended-cut.mkv: %v", err)
	}

	known := map[string]bool{tracked: true}
	entries, err := ScanRootFolder(dir, known)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 1 || entries[0].Name != "extended-cut.mkv" {
		t.Fatalf("expected the new file dropped alongside a tracked one to surface individually, got %+v", entries)
	}
}

func TestScanRootFolder_StillReportsFreshLeafDirectoryWhole(t *testing.T) {
	dir := t.TempDir()
	pack := filepath.Join(dir, "Show.S01.Group")
	if err := os.MkdirAll(pack, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	for _, name := range []string{"Show.S01E01.mkv", "Show.S01E02.mkv"} {
		if err := os.WriteFile(filepath.Join(pack, name), []byte("x"), 0o644); err != nil {
			t.Fatalf("writing %s: %v", name, err)
		}
	}

	entries, err := ScanRootFolder(dir, map[string]bool{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 1 || entries[0].Name != "Show.S01.Group" || entries[0].Path != pack {
		t.Fatalf("expected the fresh season-pack directory reported whole (not descended into), got %+v", entries)
	}
}

func TestScanRootFolder_SkipsKnownSubdirectoryEntirely(t *testing.T) {
	dir := t.TempDir()
	movieDir := filepath.Join(dir, "Title (2024)")
	if err := os.MkdirAll(movieDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(movieDir, "movie.mkv"), []byte("x"), 0o644); err != nil {
		t.Fatalf("writing movie.mkv: %v", err)
	}

	known := map[string]bool{movieDir: true}
	entries, err := ScanRootFolder(dir, known)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected a known subdirectory to be skipped entirely without descending into it, got %+v", entries)
	}
}

func TestUpsertCollection_IdempotentUpdatesName(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	id1, err := s.UpsertCollection(ctx, 12345, "Avengers Collection")
	if err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	if id1 == 0 {
		t.Fatal("expected non-zero id")
	}

	// Second call with same tmdbCollectionID but updated name — must update, not duplicate.
	id2, err := s.UpsertCollection(ctx, 12345, "Avengers Collection (updated)")
	if err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	if id2 != id1 {
		t.Errorf("expected idempotent upsert to return same id %d, got %d", id1, id2)
	}
}

func TestSetItemCollection_SetsCollectionOnItem(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	item, err := s.Upsert(ctx, Item{
		Mode: mode.Movies, TMDBID: 42, Title: "Iron Man", Year: 2008,
		FilePath: "/movies/Iron Man/movie.mkv", RootFolderPath: "/movies",
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}

	collID, err := s.UpsertCollection(ctx, 131292, "Iron Man Collection")
	if err != nil {
		t.Fatalf("upsert collection: %v", err)
	}

	if err := s.SetItemCollection(ctx, item.ID, collID); err != nil {
		t.Fatalf("SetItemCollection: %v", err)
	}

	all, err := s.List(ctx, mode.Movies)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("expected 1 item, got %d", len(all))
	}
	if all[0].TMDBCollectionID != 131292 {
		t.Errorf("TMDBCollectionID: want 131292, got %d", all[0].TMDBCollectionID)
	}
	if all[0].CollectionName != "Iron Man Collection" {
		t.Errorf("CollectionName: want %q, got %q", "Iron Man Collection", all[0].CollectionName)
	}
}

func TestListCollections_CountsMoviesPerCollection(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	collID, _ := s.UpsertCollection(ctx, 500, "MCU")

	for i, title := range []string{"Iron Man", "Thor", "Captain America"} {
		item, _ := s.Upsert(ctx, Item{
			Mode: mode.Movies, TMDBID: i + 1, Title: title, Year: 2010 + i,
			FilePath: "/movies/" + title + "/movie.mkv", RootFolderPath: "/movies",
		})
		s.SetItemCollection(ctx, item.ID, collID) //nolint:errcheck
	}

	cols, err := s.ListCollections(ctx)
	if err != nil {
		t.Fatalf("ListCollections: %v", err)
	}
	if len(cols) != 1 {
		t.Fatalf("expected 1 collection, got %d", len(cols))
	}
	if cols[0].TMDBCollectionID != 500 {
		t.Errorf("TMDBCollectionID: want 500, got %d", cols[0].TMDBCollectionID)
	}
	if cols[0].Name != "MCU" {
		t.Errorf("Name: want %q, got %q", "MCU", cols[0].Name)
	}
	if cols[0].Count != 3 {
		t.Errorf("Count: want 3, got %d", cols[0].Count)
	}
}

func TestListCollections_EmptyWhenNoMoviesInCollection(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// A collection exists but no movies are linked — must not appear in the list.
	s.UpsertCollection(ctx, 999, "Orphan Collection") //nolint:errcheck

	cols, err := s.ListCollections(ctx)
	if err != nil {
		t.Fatalf("ListCollections: %v", err)
	}
	if len(cols) != 0 {
		t.Errorf("expected empty list for unlinked collection, got %+v", cols)
	}
}

func TestScanRootFolder_ExcludesBonusContentDirectories(t *testing.T) {
	dir := t.TempDir()
	movieDir := filepath.Join(dir, "Title (2024)")
	sampleDir := filepath.Join(movieDir, "Sample")
	if err := os.MkdirAll(sampleDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(movieDir, "movie.mkv"), []byte("x"), 0o644); err != nil {
		t.Fatalf("writing movie.mkv: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sampleDir, "sample.mkv"), []byte("x"), 0o644); err != nil {
		t.Fatalf("writing sample.mkv: %v", err)
	}

	entries, err := ScanRootFolder(dir, map[string]bool{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 1 || entries[0].Name != "Title (2024)" {
		t.Fatalf("expected the movie folder reported whole, unaffected by its Sample/ subdirectory, got %+v", entries)
	}
}

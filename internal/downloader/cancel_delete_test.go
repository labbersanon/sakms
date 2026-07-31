package downloader

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte("data"), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// TestCancel_DeletesSingleFile_KeepsBystander proves cancelling a single-file
// torrent that sits directly in the shared staging dir deletes ONLY its own
// file — an unrelated file in the same staging dir must survive (the shared-dir
// safety requirement the spec calls out).
func TestCancel_DeletesSingleFile_KeepsBystander(t *testing.T) {
	staging := t.TempDir()
	m := NewForTesting(staging)

	mine := filepath.Join(staging, "movie.mkv")
	bystander := filepath.Join(staging, "someone-elses.mkv")
	writeFile(t, mine)
	writeFile(t, bystander)

	m.SeedState(Download{GID: "abc", Status: "complete", Dir: staging, Files: []string{mine}})

	if err := m.Cancel("abc"); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if _, err := os.Stat(mine); !os.IsNotExist(err) {
		t.Errorf("the torrent's own file should be deleted, stat err = %v", err)
	}
	if _, err := os.Stat(bystander); err != nil {
		t.Errorf("an unrelated file in the shared staging dir must survive, stat err = %v", err)
	}
	// And it's gone from the queue.
	if d, _ := m.FindByGID("abc"); d != nil {
		t.Errorf("cancelled download should no longer be listed, got %+v", d)
	}
}

// TestCancel_DeletesMultiFileFolder proves a multi-file torrent's own subfolder
// is fully emptied (every declared file removed) and the now-empty per-torrent
// folder is pruned, while the staging root itself remains.
func TestCancel_DeletesMultiFileFolder(t *testing.T) {
	staging := t.TempDir()
	m := NewForTesting(staging)

	sub := filepath.Join(staging, "Show.S01")
	a := filepath.Join(sub, "e01.mkv")
	b := filepath.Join(sub, "e02.mkv")
	writeFile(t, a)
	writeFile(t, b)

	m.SeedState(Download{GID: "xyz", Status: "complete", Dir: sub, Files: []string{a, b}})

	if err := m.Cancel("xyz"); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if _, err := os.Stat(a); !os.IsNotExist(err) {
		t.Errorf("file a should be deleted, stat err = %v", err)
	}
	if _, err := os.Stat(b); !os.IsNotExist(err) {
		t.Errorf("file b should be deleted, stat err = %v", err)
	}
	if _, err := os.Stat(sub); !os.IsNotExist(err) {
		t.Errorf("the emptied per-torrent folder should be pruned, stat err = %v", err)
	}
	if _, err := os.Stat(staging); err != nil {
		t.Errorf("the staging root must survive, stat err = %v", err)
	}
}

// TestCancel_UnknownGID_Errors confirms cancelling a missing GID is a plain
// not-found error (unchanged behavior — the file-deletion addition doesn't
// affect the error path).
func TestCancel_UnknownGID_Errors(t *testing.T) {
	m := NewForTesting(t.TempDir())
	if err := m.Cancel("nope"); err == nil {
		t.Fatal("expected an error cancelling an unknown GID")
	}
}

// TestCancel_MaliciousTorrentPath_RefusesDeletionOutsideStaging is the security
// regression for HIGH #2: a malicious torrent can declare a file path with ".."
// components (anacrolix's File.Path() returns the raw bencoded path unsanitized),
// which after filepath.Join+Clean escapes the staging dir. Cancel's file-deletion
// must refuse any path that isn't strictly under staging, so a victim file beside
// staging (e.g. secret.key) survives.
func TestCancel_MaliciousTorrentPath_RefusesDeletionOutsideStaging(t *testing.T) {
	root := t.TempDir()
	staging := filepath.Join(root, "staging")
	if err := os.MkdirAll(staging, 0o755); err != nil {
		t.Fatalf("mkdir staging: %v", err)
	}
	victim := filepath.Join(root, "secret.key")
	if err := os.WriteFile(victim, []byte("k"), 0o644); err != nil {
		t.Fatalf("write victim: %v", err)
	}

	m := NewForTesting(staging)
	// A declared path that escapes staging via "..": Clean resolves it to victim.
	escaped := filepath.Join(staging, "..", "secret.key")
	m.SeedState(Download{GID: "evil", Status: "complete", Dir: staging, Files: []string{escaped}})

	if err := m.Cancel("evil"); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if _, err := os.Stat(victim); err != nil {
		t.Errorf("containment guard must refuse to delete a torrent path escaping staging; victim gone: %v", err)
	}
}

// TestCancel_AfterImport_LeavesLibraryFileUntouched is the data-loss guard: a
// completed download that has already been IMPORTED (rename.Relocate os.Rename'd
// the file OUT of staging into the library, WITHOUT updating the engine's tracked
// paths) can still be cancel-able (import never removes the GID from the engine),
// so cancelling it must be a harmless no-op — the engine only ever holds STAGING
// paths, and import moved the file away from them, so the relocated library copy
// (at a different root the engine never tracked) is never touched.
func TestCancel_AfterImport_LeavesLibraryFileUntouched(t *testing.T) {
	staging := t.TempDir()
	libraryRoot := t.TempDir()
	m := NewForTesting(staging)

	stagingFile := filepath.Join(staging, "Movie.2020.1080p.mkv")
	writeFile(t, stagingFile)
	m.SeedState(Download{GID: "g", Status: "complete", Dir: staging, Files: []string{stagingFile}})

	// Simulate import: rename.Relocate MOVES the file out of staging into the
	// library. The engine's e.files still points at the old (now-gone) staging path.
	libFile := filepath.Join(libraryRoot, "Movie (2020).mkv")
	if err := os.Rename(stagingFile, libFile); err != nil {
		t.Fatalf("simulate import move: %v", err)
	}

	// Post-import cancel: must succeed and touch nothing real.
	if err := m.Cancel("g"); err != nil {
		t.Fatalf("post-import cancel should be a harmless no-op, got %v", err)
	}
	if _, err := os.Stat(libFile); err != nil {
		t.Errorf("the already-imported library file MUST survive a post-import cancel, stat err = %v", err)
	}
}

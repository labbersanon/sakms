package usenet

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// newTestManager builds a Manager without touching the NNTP pool or the poll
// loop — enough to exercise Cancel's file-deletion. downloads/subscribers are
// initialized so Cancel and List work.
func newTestManager(stagingDir string) *Manager {
	return &Manager{
		stagingDir:  stagingDir,
		downloads:   map[string]*dlState{},
		subscribers: map[int]chan []Download{},
	}
}

// seedDownload inserts a download with its own per-download staging subdir
// (mirroring AddNZB) plus an already-written file, and returns the subdir.
func seedDownload(t *testing.T, m *Manager, gid, name string) string {
	t.Helper()
	sub := filepath.Join(m.stagingDir, sanitizeName(name))
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", sub, err)
	}
	if err := os.WriteFile(filepath.Join(sub, "file.bin"), []byte("data"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	_, cancel := context.WithCancel(context.Background())
	m.downloads[gid] = &dlState{gid: gid, name: name, stagingDir: sub, status: "complete", cancel: cancel}
	return sub
}

// TestCancel_DeletesDownloadDir proves cancelling a usenet download removes its
// own per-download staging subfolder (and the file inside it), while a sibling
// download's directory in the same root staging dir is untouched.
func TestCancel_DeletesDownloadDir(t *testing.T) {
	root := t.TempDir()
	m := newTestManager(root)

	mine := seedDownload(t, m, "nzb-1", "My Release")
	sibling := seedDownload(t, m, "nzb-2", "Other Release")

	if err := m.Cancel("nzb-1"); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if _, err := os.Stat(mine); !os.IsNotExist(err) {
		t.Errorf("cancelled download's dir should be deleted, stat err = %v", err)
	}
	if _, err := os.Stat(sibling); err != nil {
		t.Errorf("a sibling download's dir must survive, stat err = %v", err)
	}
	if d, _ := m.FindByGID("nzb-1"); d != nil {
		t.Errorf("cancelled download should no longer be listed, got %+v", d)
	}
	// The root staging dir itself must always remain.
	if _, err := os.Stat(root); err != nil {
		t.Errorf("root staging dir must survive, stat err = %v", err)
	}
}

// TestDeleteDownloadDir_RefusesRoot guards the safety rule: the per-download
// deletion must never wipe the root staging dir even if a download's stagingDir
// were (wrongly) set to it.
func TestDeleteDownloadDir_RefusesRoot(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "keep.bin"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	m := newTestManager(root)
	m.deleteDownloadDir(root)
	m.deleteDownloadDir("")
	if _, err := os.Stat(filepath.Join(root, "keep.bin")); err != nil {
		t.Errorf("root staging contents must never be removed, stat err = %v", err)
	}
}

// TestCancel_UnknownGID_Errors confirms the not-found error path is unchanged.
func TestCancel_UnknownGID_Errors(t *testing.T) {
	m := newTestManager(t.TempDir())
	if err := m.Cancel("nzb-404"); err == nil {
		t.Fatal("expected an error cancelling an unknown GID")
	}
}

// TestCancel_MaliciousParentDir_RefusesDeletionOutsideStaging is the security
// regression for HIGH #1: a per-download dir that resolves to the PARENT of the
// staging root (the shape the pre-fix code produced from a ".." release name)
// must NOT be removed — the containment guard in deleteDownloadDir refuses it, so
// a sibling file living beside the staging dir (e.g. the SQLite DB / secret.key)
// survives. (The primary fix — gid-keyed dirs in AddNZB — means a ".." name can
// no longer even reach this dir; this proves the defense-in-depth guard closes it
// independently.)
func TestCancel_MaliciousParentDir_RefusesDeletionOutsideStaging(t *testing.T) {
	root := t.TempDir()
	staging := filepath.Join(root, "staging")
	if err := os.MkdirAll(staging, 0o755); err != nil {
		t.Fatalf("mkdir staging: %v", err)
	}
	// A victim file in the PARENT of staging — stands in for the DB / secret.key.
	victim := filepath.Join(root, "sakms.db")
	if err := os.WriteFile(victim, []byte("db"), 0o644); err != nil {
		t.Fatalf("write victim: %v", err)
	}

	m := newTestManager(staging)
	malicious := filepath.Clean(filepath.Join(staging, "..")) // == root, the parent
	_, cancel := context.WithCancel(context.Background())
	m.downloads["nzb-1"] = &dlState{gid: "nzb-1", stagingDir: malicious, status: "complete", cancel: cancel}

	if err := m.Cancel("nzb-1"); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if _, err := os.Stat(victim); err != nil {
		t.Errorf("containment guard must refuse to delete the staging parent; victim gone: %v", err)
	}
	if _, err := os.Stat(staging); err != nil {
		t.Errorf("staging dir itself must survive: %v", err)
	}
}

// TestCancel_AfterImport_LeavesLibraryFileUntouched is the data-loss guard: a
// completed usenet download whose staging subdir has already been IMPORTED
// (rename.Relocate os.Rename'd it OUT to the library, WITHOUT updating the
// engine's tracked stagingDir) can still be cancel-able, so cancelling it must be
// a harmless no-op — RemoveAll of the now-gone staging subdir does nothing, and
// the relocated library copy (at a different root) is never referenced.
func TestCancel_AfterImport_LeavesLibraryFileUntouched(t *testing.T) {
	root := t.TempDir()
	libraryRoot := t.TempDir()
	m := newTestManager(root)

	sub := seedDownload(t, m, "nzb-1", "My Release") // root/My Release/file.bin

	// Simulate import: move the whole per-download subdir out to the library.
	libDir := filepath.Join(libraryRoot, "My Release")
	if err := os.Rename(sub, libDir); err != nil {
		t.Fatalf("simulate import move: %v", err)
	}

	if err := m.Cancel("nzb-1"); err != nil {
		t.Fatalf("post-import cancel should be a harmless no-op, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(libDir, "file.bin")); err != nil {
		t.Errorf("the already-imported library file MUST survive a post-import cancel, stat err = %v", err)
	}
}

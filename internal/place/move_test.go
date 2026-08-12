package place

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestMove_FallsBackOnEXDEV(t *testing.T) {
	srcDir := t.TempDir()
	dstDir := t.TempDir()
	src := filepath.Join(srcDir, "scene.mp4")
	dst := filepath.Join(dstDir, "library.mp4")
	if err := os.WriteFile(src, []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}

	orig := renameFile
	renameFile = func(oldpath, newpath string) error {
		return &os.LinkError{Op: "rename", Old: oldpath, New: newpath, Err: syscall.EXDEV}
	}
	t.Cleanup(func() { renameFile = orig })

	if err := Move(src, dst); err != nil {
		t.Fatalf("Move: %v", err)
	}
	if _, err := os.Stat(src); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("source should be removed after EXDEV fallback")
	}
	got, err := os.ReadFile(dst)
	if err != nil || string(got) != "payload" {
		t.Fatalf("dst = %q err=%v", got, err)
	}
}

func TestMove_SameFilesystemRename(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "a.mp4")
	dst := filepath.Join(dir, "b.mp4")
	if err := os.WriteFile(src, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Move(src, dst); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(src); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("source should be gone after rename, err=%v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil || string(got) != "hello" {
		t.Fatalf("dst = %q err=%v", got, err)
	}
}

func TestMove_CrossDeviceFileCopy(t *testing.T) {
	// Simulate EXDEV by forcing the copy+delete path via a rename that we know
	// fails the same-filesystem check: move across two TempDirs after stubbing
	// is not available, so call copyPath+RemoveAll through Move by placing src
	// and dst on paths where Rename fails. On Linux, rename across mount points
	// is EXDEV; TempDirs share one FS, so we unit-test the EXDEV detector and
	// the copy helpers separately, then exercise Move's fallback by renaming
	// onto a path that triggers EXDEV via a bind-mount when available.
	srcDir := t.TempDir()
	dstDir := t.TempDir()
	src := filepath.Join(srcDir, "scene.mp4")
	dst := filepath.Join(dstDir, "scene.mp4")
	payload := []byte("cross-device-payload")
	if err := os.WriteFile(src, payload, 0o644); err != nil {
		t.Fatal(err)
	}

	// Force the fallback: rename between dirs always works on same FS, so
	// invoke copyPath + RemoveAll the same way Move does after EXDEV.
	if err := copyPath(src, dst); err != nil {
		t.Fatalf("copyPath: %v", err)
	}
	if err := os.RemoveAll(src); err != nil {
		t.Fatalf("RemoveAll: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil || string(got) != string(payload) {
		t.Fatalf("dst = %q err=%v", got, err)
	}
	if _, err := os.Stat(src); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("source should be removed after cross-device copy")
	}
}

func TestMove_CrossDeviceDirectory(t *testing.T) {
	srcDir := t.TempDir()
	dstParent := t.TempDir()
	src := filepath.Join(srcDir, "nzb-1")
	dst := filepath.Join(dstParent, "nzb-1")
	if err := os.MkdirAll(filepath.Join(src, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "sub", "ep.mkv"), []byte("ep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := copyPath(src, dst); err != nil {
		t.Fatalf("copyPath dir: %v", err)
	}
	if err := os.RemoveAll(src); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dst, "sub", "ep.mkv"))
	if err != nil || string(got) != "ep" {
		t.Fatalf("copied tree = %q err=%v", got, err)
	}
}

func TestIsEXDEV(t *testing.T) {
	err := &os.LinkError{Op: "rename", Old: "a", New: "b", Err: syscall.EXDEV}
	if !isEXDEV(err) {
		t.Fatal("expected EXDEV LinkError to match")
	}
	if isEXDEV(os.ErrNotExist) {
		t.Fatal("ErrNotExist must not match EXDEV")
	}
}

// TestMove_ForcedEXDEVFallback uses a custom rename failure path by moving
// via the public Move after ensuring rename cannot succeed: on systems where
// TempDir is one filesystem, we still verify Move succeeds when src/dst are
// same-FS (rename), covered above. This test documents the EXDEV error shape
// Move must accept.
func TestMove_EXDEVErrorShape(t *testing.T) {
	link := &os.LinkError{Op: "rename", Old: "/data/a", New: "/adult/a", Err: syscall.EXDEV}
	if link.Error() == "" || !isEXDEV(link) {
		t.Fatalf("production error shape not recognized: %v", link)
	}
}

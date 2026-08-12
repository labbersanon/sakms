package rename

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/labbersanon/sakms/internal/place"
)

// Claude 2026-08-11: Relocate must survive EXDEV (staging → library CIFS).
func TestRelocate_FallsBackOnEXDEV(t *testing.T) {
	srcRoot := t.TempDir()
	destRoot := t.TempDir()
	src := filepath.Join(srcRoot, "scene.mp4")
	if err := os.WriteFile(src, []byte("vid"), 0o644); err != nil {
		t.Fatal(err)
	}

	restore := place.SetRenameForTest(func(oldpath, newpath string) error {
		return &os.LinkError{Op: "rename", Old: oldpath, New: newpath, Err: syscall.EXDEV}
	})
	t.Cleanup(restore)

	got, err := Relocate(src, destRoot)
	if err != nil {
		t.Fatalf("Relocate: %v", err)
	}
	want := filepath.Join(destRoot, "scene.mp4")
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	if _, err := os.Stat(src); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("staging source should be removed after cross-device import")
	}
	body, err := os.ReadFile(want)
	if err != nil || string(body) != "vid" {
		t.Fatalf("library file = %q err=%v", body, err)
	}
}

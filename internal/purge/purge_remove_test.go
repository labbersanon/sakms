package purge

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/labbersanon/sakms/internal/mode"
)

func TestIsSafeLibrarySubdir(t *testing.T) {
	root := "/media/Movies"
	cases := []struct {
		dir  string
		want bool
	}{
		{filepath.Join(root, "Title (2026)"), true},
		{root, false},
		{"", false},
		{"/media", false},
		{"/other/Title", false},
	}
	for _, c := range cases {
		if got := isSafeLibrarySubdir(c.dir, root); got != c.want {
			t.Errorf("isSafeLibrarySubdir(%q, %q) = %v, want %v", c.dir, root, got, c.want)
		}
	}
	if isSafeLibrarySubdir(root, "") {
		t.Error("empty root must never be safe")
	}
}

func TestRemoveTrackedMedia_WipesMovieFolderNotLibraryRoot(t *testing.T) {
	root := t.TempDir()
	movieDir := filepath.Join(root, "The Last House (2026)")
	neighborDir := filepath.Join(root, "Other Movie (2025)")
	if err := os.MkdirAll(movieDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(neighborDir, 0o755); err != nil {
		t.Fatal(err)
	}
	primary := filepath.Join(movieDir, "title.mkv")
	alt := filepath.Join(movieDir, "title - 1080p.mp4")
	sidecar := filepath.Join(movieDir, "title.nfo")
	neighbor := filepath.Join(neighborDir, "other.mkv")
	for _, p := range []string{primary, alt, sidecar, neighbor} {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	changes, err := removeTrackedMedia([]string{primary, alt, primary, ""}, root)
	if err != nil {
		t.Fatalf("removeTrackedMedia: %v", err)
	}
	if len(changes) != 2 {
		t.Fatalf("expected 2 unique file PathChanges, got %+v", changes)
	}
	for _, c := range changes {
		if c.Kind != mode.Deleted {
			t.Errorf("Kind = %v, want Deleted", c.Kind)
		}
	}
	if _, err := os.Stat(movieDir); !os.IsNotExist(err) {
		t.Errorf("movie folder should be gone, stat: %v", err)
	}
	if _, err := os.Stat(neighbor); err != nil {
		t.Errorf("neighbor movie must survive: %v", err)
	}
	if _, err := os.Stat(root); err != nil {
		t.Errorf("library root must survive: %v", err)
	}
}

func TestRemoveTrackedMedia_FileInRootDoesNotWipeRoot(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "loose.mkv")
	keep := filepath.Join(root, "keep.mkv")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keep, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	changes, err := removeTrackedMedia([]string{file}, root)
	if err != nil {
		t.Fatalf("removeTrackedMedia: %v", err)
	}
	if len(changes) != 1 || changes[0].Path != file {
		t.Fatalf("changes = %+v", changes)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Errorf("expected %s deleted, stat: %v", file, err)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Errorf("sibling in root must survive: %v", err)
	}
	if _, err := os.Stat(root); err != nil {
		t.Errorf("root must survive: %v", err)
	}
}

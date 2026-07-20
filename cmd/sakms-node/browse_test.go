package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBrowseDirectory_DirsOnlyAlphaSorted(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"zeta", "alpha", "mu"} {
		if err := os.Mkdir(filepath.Join(dir, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// A plain file must never appear in the results — this endpoint's whole
	// use case is picking a directory, mirroring the server's own browse.go.
	if err := os.WriteFile(filepath.Join(dir, "not-a-dir.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	entries, err := browseDirectory(dir)
	if err != nil {
		t.Fatalf("browseDirectory: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("got %d entries, want 3 (dirs only, file excluded): %+v", len(entries), entries)
	}
	wantOrder := []string{"alpha", "mu", "zeta"}
	for i, want := range wantOrder {
		if entries[i].Name != want {
			t.Errorf("entries[%d].Name = %q, want %q (must be alpha-sorted)", i, entries[i].Name, want)
		}
		wantPath := filepath.Join(dir, want)
		if entries[i].Path != wantPath {
			t.Errorf("entries[%d].Path = %q, want %q", i, entries[i].Path, wantPath)
		}
	}
}

func TestBrowseDirectory_EmptyDir(t *testing.T) {
	dir := t.TempDir()
	entries, err := browseDirectory(dir)
	if err != nil {
		t.Fatalf("browseDirectory: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("got %d entries for an empty dir, want 0", len(entries))
	}
}

func TestBrowseDirectory_NonexistentPathReturnsError(t *testing.T) {
	_, err := browseDirectory(filepath.Join(t.TempDir(), "does-not-exist"))
	if err == nil {
		t.Fatal("expected an error for a nonexistent path, got nil")
	}
}

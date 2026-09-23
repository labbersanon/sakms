package api

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLocalPosterFile_FindsFolderJPG(t *testing.T) {
	dir := t.TempDir()
	video := filepath.Join(dir, "film.mp4")
	if err := os.WriteFile(video, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	poster := filepath.Join(dir, "folder.jpg")
	if err := os.WriteFile(poster, []byte("jpg"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := localPosterFile(video)
	if err != nil || got != poster {
		t.Fatalf("got %q err=%v", got, err)
	}
}

func TestLocalPosterFile_IgnoresOtherNames(t *testing.T) {
	dir := t.TempDir()
	video := filepath.Join(dir, "film.mp4")
	if err := os.WriteFile(video, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.jpg"), []byte("jpg"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := localPosterFile(video); err == nil {
		t.Fatal("expected no poster for an unrelated image")
	}
}

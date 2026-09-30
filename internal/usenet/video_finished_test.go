package usenet

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func writePopulatedFile(t *testing.T, path string, n int) {
	t.Helper()
	buf := make([]byte, n)
	for i := range buf {
		buf[i] = byte('A' + (i % 26))
	}
	if err := os.WriteFile(path, buf, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestVideoLooksFinished_Populated(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.mkv")
	writePopulatedFile(t, path, 64)
	if err := VideoLooksFinished(path, 0); err != nil {
		t.Fatal(err)
	}
}

func TestVideoLooksFinished_AllZeroIsHollow(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "z.mkv")
	if err := os.WriteFile(path, make([]byte, 1<<20), 0o644); err != nil {
		t.Fatal(err)
	}
	err := VideoLooksFinished(path, 0)
	if !errors.Is(err, ErrNoVideoUnpacked) {
		t.Fatalf("err = %v, want ErrNoVideoUnpacked", err)
	}
}

func TestVideoLooksFinished_ShortOfExpected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.mkv")
	writePopulatedFile(t, path, 100)
	err := VideoLooksFinished(path, 1000)
	if !errors.Is(err, ErrNoVideoUnpacked) {
		t.Fatalf("err = %v, want ErrNoVideoUnpacked", err)
	}
}

func TestVideoLooksFinished_SparseTruncate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sparse.mkv")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(2 << 20); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	err = VideoLooksFinished(path, 0)
	if !errors.Is(err, ErrNoVideoUnpacked) {
		t.Fatalf("err = %v, want ErrNoVideoUnpacked", err)
	}
}

func TestResumeExpectedFileSize(t *testing.T) {
	dir := t.TempDir()
	if got := ResumeExpectedFileSize(dir, "a.mkv"); got != 0 {
		t.Fatalf("missing sidecar = %d", got)
	}
	snap := ResumeSnapshot{
		Version: resumeSchemaVersion,
		Files: map[string]*ResumeFile{
			"a.mkv": {Size: 999},
		},
	}
	raw, err := json.Marshal(snap)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ResumeFileName), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := ResumeExpectedFileSize(dir, "a.mkv"); got != 999 {
		t.Fatalf("got %d", got)
	}
}

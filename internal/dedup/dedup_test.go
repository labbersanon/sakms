package dedup

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/labbersanon/sakms/internal/mediainfo"
)

// sceneUUIDA is a stash-box scene UUID reused across the Adult dedup tests —
// a fake stash-box's map key, so any stable UUID-shaped value works.
const sceneUUIDA = "a29768db-b3cd-4a71-a75e-4294373207bb"

// fakeProber maps a video file path to a canned mediainfo.Probe result, so
// tests never need a real ffprobe binary.
type fakeProber struct {
	byPath map[string]*mediainfo.Probe
}

func (f *fakeProber) Probe(ctx context.Context, path string) (*mediainfo.Probe, error) {
	p, ok := f.byPath[path]
	if !ok {
		return nil, os.ErrNotExist
	}
	return p, nil
}

// fakePHasher maps a video path to a canned scheme-tagged hash and records how
// many times each path was hashed, so a test can assert the cache avoided a
// re-hash. Mirrors fakeProber — ScanLibrary's phash refinement is faked without
// a real ffmpeg binary or video content. A path with no canned hash returns an
// error, mimicking an undecodable/short file (attachPHashes then drops it).
type fakePHasher struct {
	byPath map[string]string
	calls  map[string]int
}

func (f *fakePHasher) Hash(ctx context.Context, path string) (string, error) {
	if f.calls == nil {
		f.calls = map[string]int{}
	}
	f.calls[path]++
	h, ok := f.byPath[path]
	if !ok {
		return "", os.ErrNotExist
	}
	return h, nil
}

// matchingPHasher returns a fakePHasher that hashes every given path to the
// same value, so ScanLibrary's phash refinement keeps the whole group
// (identical hashes are within any threshold).
func matchingPHasher(paths ...string) *fakePHasher {
	sameHash := "phash64/5f:" + strings.Repeat("0", 80) // 40 zero bytes = 5 frames × 8
	byPath := map[string]string{}
	for _, p := range paths {
		byPath[p] = sameHash
	}
	return &fakePHasher{byPath: byPath}
}

// writeVideoFile creates dir (if needed) and a dummy video file inside it,
// returning the file's full path.
func writeVideoFile(t *testing.T, dir, name string, size int) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, make([]byte, size), 0o644); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
	return path
}

func TestFindVideoFile_PathIsAlreadyAFile(t *testing.T) {
	dir := t.TempDir()
	f := writeVideoFile(t, dir, "movie.mkv", 100)

	got, err := findVideoFile(f)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != f {
		t.Errorf("expected %q, got %q", f, got)
	}
}

func TestFindVideoFile_DirectoryPicksLargestVideoFile(t *testing.T) {
	dir := t.TempDir()
	writeVideoFile(t, dir, "sample.mkv", 10)
	big := writeVideoFile(t, dir, "movie.mkv", 1000)
	writeVideoFile(t, dir, "poster.jpg", 5000) // bigger, but not a video extension

	got, err := findVideoFile(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != big {
		t.Errorf("expected the largest video file %q, got %q", big, got)
	}
}

func TestFindVideoFile_NoVideoFilesErrors(t *testing.T) {
	dir := t.TempDir()
	writeVideoFile(t, dir, "readme.txt", 10)

	if _, err := findVideoFile(dir); err == nil {
		t.Error("expected an error when no video file exists in the directory")
	}
}

func TestFindVideoFile_MissingPathErrors(t *testing.T) {
	if _, err := findVideoFile(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Error("expected an error for a nonexistent path")
	}
}

// fakeStashboxByID serves StashDB's findScene-by-id GraphQL query, returning a
// scene for each UUID present in titles and null for anything else.
func fakeStashboxByID(t *testing.T, titles map[string]string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Variables struct {
				ID string `json:"id"`
			} `json:"variables"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		title, ok := titles[req.Variables.ID]
		if !ok {
			json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"findScene": nil}})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"findScene": map[string]any{
			"id": req.Variables.ID, "title": title, "release_date": "2021-01-01",
			"studio": map[string]any{"name": "Some Studio", "parent": nil},
		}}})
	}
}

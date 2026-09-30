package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// postPathTest POSTs a path to the root-folder test endpoint and returns the
// decoded result. The {mode} segment is irrelevant to the check (the handler
// validates whatever path is sent), so any mode works.
func postPathTest(t *testing.T, srv *httptest.Server, path string) pathTestResult {
	t.Helper()
	body, _ := json.Marshal(pathTestRequest{Path: path})
	resp, err := http.Post(srv.URL+"/api/modes/movies/library/root-folder/test", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 (a wrong path is a normal result, not a server error), got %d", resp.StatusCode)
	}
	var result pathTestResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	return result
}

func TestLibraryRootFolderTest_ExistingWritableDir(t *testing.T) {
	srv, _ := newStoredTestMux(t)
	dir := t.TempDir()
	withBrowsableRoot(t, dir)

	result := postPathTest(t, srv, dir)
	if !result.OK || result.Error != "" {
		t.Fatalf("expected ok=true for an existing writable dir, got %+v", result)
	}
	// The write test must clean up after itself — no probe file left behind.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading dir: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected the write-probe file to be removed, found %d entries", len(entries))
	}
}

func TestLibraryRootFolderTest_NonexistentPath(t *testing.T) {
	srv, _ := newStoredTestMux(t)
	root := t.TempDir()
	withBrowsableRoot(t, root)

	result := postPathTest(t, srv, filepath.Join(root, "does-not-exist"))
	if result.OK {
		t.Fatal("expected ok=false for a nonexistent path")
	}
	if result.Error == "" {
		t.Error("expected a populated error message")
	}
}

func TestLibraryRootFolderTest_FileNotDirectory(t *testing.T) {
	srv, _ := newStoredTestMux(t)
	dir := t.TempDir()
	withBrowsableRoot(t, dir)
	file := filepath.Join(dir, "a-file")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatalf("creating file: %v", err)
	}

	result := postPathTest(t, srv, file)
	if result.OK {
		t.Fatal("expected ok=false for a path that is a file, not a directory")
	}
	if result.Error == "" {
		t.Error("expected a populated error message")
	}
}

func TestLibraryRootFolderTest_EmptyPath(t *testing.T) {
	srv, _ := newStoredTestMux(t)

	result := postPathTest(t, srv, "")
	if result.OK {
		t.Fatal("expected ok=false for an empty path")
	}
}

func TestLibraryRootFolderTest_OutsideBrowsableRoots(t *testing.T) {
	srv, _ := newStoredTestMux(t)
	result := postPathTest(t, srv, "/etc")
	if result.OK {
		t.Fatal("expected ok=false for a path outside browsable roots")
	}
	if result.Error == "" {
		t.Error("expected a populated error message")
	}
}

package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/labbersanon/sakms/internal/apidto"
)

func TestResolveBrowsablePath(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{name: "bare root media", input: "/media", want: "/media"},
		{name: "bare root downloads", input: "/downloads", want: "/downloads"},
		{name: "bare root adult", input: "/adult", want: "/adult"},
		{name: "valid nested", input: "/media/Movies/Action", want: "/media/Movies/Action"},
		{name: "trailing slash cleaned", input: "/media/Movies/", want: "/media/Movies"},
		{name: "redundant segments cleaned", input: "/media/./Movies//Action", want: "/media/Movies/Action"},
		// The classic prefix bug: a sibling that shares a string prefix with a
		// root must NOT be accepted. A naive strings.HasPrefix(cleaned, root)
		// would wrongly allow this.
		{name: "sibling sharing prefix rejected", input: "/mediafoo", wantErr: true},
		{name: "sibling sharing prefix nested rejected", input: "/media-other/x", wantErr: true},
		// Traversal that escapes a root after cleaning must be rejected — the
		// CLEANED path is what's checked, not the raw string.
		{name: "traversal out of root", input: "/media/../etc", wantErr: true},
		{name: "deep traversal to etc", input: "/media/../../etc/passwd", wantErr: true},
		{name: "relative traversal", input: "../../etc", wantErr: true},
		// Entirely outside every root.
		{name: "outside all roots", input: "/etc/shadow", wantErr: true},
		{name: "root filesystem", input: "/", wantErr: true},
		{name: "empty string", input: "", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveBrowsablePath(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("resolveBrowsablePath(%q) = %q, want error", tt.input, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveBrowsablePath(%q) unexpected error: %v", tt.input, err)
			}
			if got != tt.want {
				t.Fatalf("resolveBrowsablePath(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestBrowseHandler_RootsWhenPathEmpty(t *testing.T) {
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/browse", nil)
	browseHandler()(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var resp apidto.BrowseResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Path != "" {
		t.Errorf("path = %q, want empty", resp.Path)
	}
	if len(resp.Entries) != len(browsableRoots) {
		t.Fatalf("got %d entries, want %d", len(resp.Entries), len(browsableRoots))
	}
	for i, root := range browsableRoots {
		if resp.Entries[i].Name != root || resp.Entries[i].Path != root {
			t.Errorf("entry %d = %+v, want name/path %q", i, resp.Entries[i], root)
		}
	}
}

func TestBrowseHandler_RejectsPathOutsideRoots(t *testing.T) {
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/browse?path=/etc", nil)
	browseHandler()(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

func TestBrowseHandler_RejectsTraversal(t *testing.T) {
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/browse?path=/media/../etc", nil)
	browseHandler()(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

// TestBrowseHandler_ListsDirsOnly exercises the happy path against a real
// temp tree by temporarily pointing browsableRoots at it — directories are
// returned sorted, files are omitted.
func TestBrowseHandler_ListsDirsOnly(t *testing.T) {
	tmp := t.TempDir()
	for _, d := range []string{"Zeta", "Alpha", "Mid"} {
		if err := os.Mkdir(filepath.Join(tmp, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(tmp, "afile.mkv"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	orig := browsableRoots
	browsableRoots = []string{tmp}
	t.Cleanup(func() { browsableRoots = orig })

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/browse?path="+tmp, nil)
	browseHandler()(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rr.Code, rr.Body.String())
	}
	var resp apidto.BrowseResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	wantNames := []string{"Alpha", "Mid", "Zeta"} // sorted, file excluded
	if len(resp.Entries) != len(wantNames) {
		t.Fatalf("got %d entries, want %d: %+v", len(resp.Entries), len(wantNames), resp.Entries)
	}
	for i, name := range wantNames {
		if resp.Entries[i].Name != name {
			t.Errorf("entry %d name = %q, want %q", i, resp.Entries[i].Name, name)
		}
		if resp.Entries[i].Path != filepath.Join(tmp, name) {
			t.Errorf("entry %d path = %q, want %q", i, resp.Entries[i].Path, filepath.Join(tmp, name))
		}
	}
}

// TestBrowseHandler_NonExistentValidPath confirms a valid-prefix path that
// doesn't exist returns 200 with no entries (so autocomplete degrades
// gracefully mid-keystroke) rather than a 500.
func TestBrowseHandler_NonExistentValidPath(t *testing.T) {
	tmp := t.TempDir()
	orig := browsableRoots
	browsableRoots = []string{tmp}
	t.Cleanup(func() { browsableRoots = orig })

	missing := filepath.Join(tmp, "does-not-exist-yet")
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/browse?path="+missing, nil)
	browseHandler()(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var resp apidto.BrowseResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Entries) != 0 {
		t.Errorf("got %d entries, want 0", len(resp.Entries))
	}
}

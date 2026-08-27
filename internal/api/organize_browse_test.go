package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/labbersanon/sakms/internal/apidto"
)

func withBrowsableRoot(t *testing.T, root string) {
	t.Helper()
	orig := browsableRoots
	browsableRoots = []string{root}
	t.Cleanup(func() { browsableRoots = orig })
}

func TestOrganizeBrowseList_RootsWhenPathEmpty(t *testing.T) {
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/organize/browse", nil)
	organizeBrowseListHandler(nil)(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var resp apidto.OrganizeBrowseResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Path != "" {
		t.Errorf("path = %q, want empty", resp.Path)
	}
	if len(resp.Entries) != len(browsableRoots) {
		t.Fatalf("got %d entries, want %d", len(resp.Entries), len(browsableRoots))
	}
	for i, root := range browsableRoots {
		if !resp.Entries[i].IsDir || resp.Entries[i].Path != root {
			t.Errorf("entry %d = %+v, want dir %q", i, resp.Entries[i], root)
		}
	}
}

func TestOrganizeBrowseList_FilesAndDirsSorted(t *testing.T) {
	tmp := t.TempDir()
	for _, d := range []string{"Zeta", "Alpha"} {
		if err := os.Mkdir(filepath.Join(tmp, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(tmp, "movie.mkv"), []byte("xx"), 0o644); err != nil {
		t.Fatal(err)
	}
	withBrowsableRoot(t, tmp)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/organize/browse?path="+tmp, nil)
	organizeBrowseListHandler(nil)(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", rr.Code, rr.Body.String())
	}
	var resp apidto.OrganizeBrowseResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Entries) != 3 {
		t.Fatalf("got %d entries: %+v", len(resp.Entries), resp.Entries)
	}
	if !resp.Entries[0].IsDir || resp.Entries[0].Name != "Alpha" {
		t.Errorf("first = %+v, want dir Alpha", resp.Entries[0])
	}
	if !resp.Entries[1].IsDir || resp.Entries[1].Name != "Zeta" {
		t.Errorf("second = %+v, want dir Zeta", resp.Entries[1])
	}
	if resp.Entries[2].IsDir || resp.Entries[2].Name != "movie.mkv" || resp.Entries[2].Size != 2 {
		t.Errorf("third = %+v, want file movie.mkv size 2", resp.Entries[2])
	}
	if resp.Parent != "" {
		t.Errorf("parent of root = %q, want empty", resp.Parent)
	}
}

func TestOrganizeBrowseList_RejectsTraversal(t *testing.T) {
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/organize/browse?path=/media/../etc", nil)
	organizeBrowseListHandler(nil)(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

func TestOrganizeBrowseRename_MovesFileAndRefusesOverwrite(t *testing.T) {
	tmp := t.TempDir()
	src := filepath.Join(tmp, "old.mkv")
	if err := os.WriteFile(src, []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "taken.mkv"), []byte("b"), 0o644); err != nil {
		t.Fatal(err)
	}
	withBrowsableRoot(t, tmp)

	body, _ := json.Marshal(apidto.OrganizeBrowseRenameRequest{Path: src, NewName: "new.mkv"})
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/organize/browse/rename", bytes.NewReader(body))
	organizeBrowseRenameHandler(nil)(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", rr.Code, rr.Body.String())
	}
	var resp apidto.OrganizeBrowseOpResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Results) != 1 || !resp.Results[0].OK {
		t.Fatalf("rename result = %+v", resp.Results)
	}
	dest := filepath.Join(tmp, "new.mkv")
	if _, err := os.Stat(dest); err != nil {
		t.Fatalf("renamed file missing: %v", err)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Fatal("old path still exists")
	}

	body, _ = json.Marshal(apidto.OrganizeBrowseRenameRequest{Path: dest, NewName: "taken.mkv"})
	rr = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/organize/browse/rename", bytes.NewReader(body))
	organizeBrowseRenameHandler(nil)(rr, req)
	var clash apidto.OrganizeBrowseOpResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &clash); err != nil {
		t.Fatal(err)
	}
	if clash.Results[0].OK || clash.Results[0].Error != "destination already exists" {
		t.Fatalf("overwrite = %+v", clash.Results[0])
	}
}

func TestOrganizeBrowseRename_RejectsRootAndSlashName(t *testing.T) {
	tmp := t.TempDir()
	withBrowsableRoot(t, tmp)

	body, _ := json.Marshal(apidto.OrganizeBrowseRenameRequest{Path: tmp, NewName: "nope"})
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/organize/browse/rename", bytes.NewReader(body))
	organizeBrowseRenameHandler(nil)(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("rename root status = %d, want 400", rr.Code)
	}

	src := filepath.Join(tmp, "a.mkv")
	if err := os.WriteFile(src, []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	body, _ = json.Marshal(apidto.OrganizeBrowseRenameRequest{Path: src, NewName: "b/c.mkv"})
	rr = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/organize/browse/rename", bytes.NewReader(body))
	organizeBrowseRenameHandler(nil)(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("slash name status = %d, want 400", rr.Code)
	}
}

func TestOrganizeBrowseMoveAndDelete(t *testing.T) {
	tmp := t.TempDir()
	srcDir := filepath.Join(tmp, "inbox")
	destDir := filepath.Join(tmp, "library")
	if err := os.Mkdir(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(destDir, 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(srcDir, "clip.mkv")
	if err := os.WriteFile(file, []byte("z"), 0o644); err != nil {
		t.Fatal(err)
	}
	withBrowsableRoot(t, tmp)

	body, _ := json.Marshal(apidto.OrganizeBrowseMoveRequest{
		Paths: []string{file}, DestDir: destDir,
	})
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/organize/browse/move", bytes.NewReader(body))
	organizeBrowseMoveHandler(nil)(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("move status = %d body %s", rr.Code, rr.Body.String())
	}
	moved := filepath.Join(destDir, "clip.mkv")
	if _, err := os.Stat(moved); err != nil {
		t.Fatalf("moved file missing: %v", err)
	}

	body, _ = json.Marshal(apidto.OrganizeBrowseDeleteRequest{Paths: []string{srcDir, moved}})
	rr = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/organize/browse/delete", bytes.NewReader(body))
	organizeBrowseDeleteHandler(nil)(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("delete status = %d body %s", rr.Code, rr.Body.String())
	}
	if _, err := os.Stat(moved); !os.IsNotExist(err) {
		t.Fatal("file still exists after delete")
	}
	if _, err := os.Stat(srcDir); !os.IsNotExist(err) {
		t.Fatal("dir still exists after delete")
	}

	body, _ = json.Marshal(apidto.OrganizeBrowseDeleteRequest{Paths: []string{tmp}})
	rr = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/organize/browse/delete", bytes.NewReader(body))
	organizeBrowseDeleteHandler(nil)(rr, req)
	var resp apidto.OrganizeBrowseOpResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Results[0].OK {
		t.Fatal("deleting the browsable root should fail")
	}
}

func TestOrganizeBrowseMove_RejectsIntoSelf(t *testing.T) {
	tmp := t.TempDir()
	dir := filepath.Join(tmp, "show")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(dir, "season")
	if err := os.Mkdir(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	withBrowsableRoot(t, tmp)

	body, _ := json.Marshal(apidto.OrganizeBrowseMoveRequest{
		Paths: []string{dir}, DestDir: nested,
	})
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/organize/browse/move", bytes.NewReader(body))
	organizeBrowseMoveHandler(nil)(rr, req)
	var resp apidto.OrganizeBrowseOpResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Results[0].OK {
		t.Fatalf("move into self should fail, got %+v", resp.Results[0])
	}
}

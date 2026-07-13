package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestLibraryRootFolder_Adult_PutThenGet_RoundTrip proves Adult now shares
// Movies/Series' free-typed library-root-folder route: a PUT stores the path
// and a subsequent GET reads it back. Adult previously 400'd on this route
// (no key existed); adultLibraryRootFolderKey now makes it work. The old
// generic /root-folders LISTING route (once a proxy to each mode's *arr app)
// has been removed entirely — see TestRootFolders_RouteRemoved in
// rootfolders_test.go.
func TestLibraryRootFolder_Adult_PutThenGet_RoundTrip(t *testing.T) {
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore := testStores(t)
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore))
	defer srv.Close()

	const rootPath = "/media/Adult"

	putBody, _ := json.Marshal(libraryRootFolderRequest{Path: rootPath})
	putReq, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/modes/adult/library/root-folder", bytes.NewReader(putBody))
	putResp, err := http.DefaultClient.Do(putReq)
	if err != nil {
		t.Fatalf("PUT failed: %v", err)
	}
	putResp.Body.Close()
	if putResp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204 from PUT, got %d", putResp.StatusCode)
	}

	getResp, err := http.Get(srv.URL + "/api/modes/adult/library/root-folder")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer getResp.Body.Close()
	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 from GET, got %d", getResp.StatusCode)
	}
	var got libraryRootFolderResponse
	if err := json.NewDecoder(getResp.Body).Decode(&got); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if got.Path != rootPath {
		t.Fatalf("expected round-tripped path %q, got %q", rootPath, got.Path)
	}
}

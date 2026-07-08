package stashapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func newTestClient(t *testing.T, handler http.HandlerFunc) (*Client, func()) {
	t.Helper()
	srv := httptest.NewServer(handler)
	return New(Config{URL: srv.URL, APIKey: "testkey"}, &http.Client{Timeout: 5 * time.Second}), srv.Close
}

// Page 1 reports a scene COUNT of 2, but scene #1 has TWO files (inflating
// the resulting file-path-keyed index to size 2 after page 1 alone). An
// index-size-based pagination check (`len(index) >= total`) would
// incorrectly stop after page 1, silently missing scene #2 on a real second
// page. Counting by scenes SEEN (not index size) must correctly fetch page 2.
func TestLoadAllScenes_PaginatesByScenesSeenNotIndexSize(t *testing.T) {
	page := 0
	c, closeSrv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		page++
		w.Header().Set("Content-Type", "application/json")
		switch page {
		case 1:
			// count=2 total scenes; this page returns scene #1 with 2 files.
			_, _ = w.Write([]byte(`{"data":{"findScenes":{"count":2,"scenes":[
				{"id":"1","title":"Multi-file Scene","date":"2024-01-01",
				 "studio":{"name":"Studio A","parent_studio":null},
				 "stash_ids":[],
				 "files":[
					{"path":"/a/file1.mp4","width":1920,"height":1080,"duration":100,"video_codec":"h264","bit_rate":1000,"fingerprints":[{"type":"phash","value":"ph1"}]},
					{"path":"/a/file1-alt.mp4","width":1280,"height":720,"duration":100,"video_codec":"h264","bit_rate":500,"fingerprints":[{"type":"phash","value":"ph1"}]}
				 ]}
			]}}}`))
		case 2:
			_, _ = w.Write([]byte(`{"data":{"findScenes":{"count":2,"scenes":[
				{"id":"2","title":"Second Scene","date":"2024-02-01",
				 "studio":{"name":"Studio B","parent_studio":null},
				 "stash_ids":[],
				 "files":[{"path":"/a/file2.mp4","width":1920,"height":1080,"duration":50,"video_codec":"av1","bit_rate":2000,"fingerprints":[]}]}
			]}}}}`))
		default:
			t.Fatalf("unexpected page request: %d", page)
		}
	})
	defer closeSrv()

	index, err := c.LoadAllScenes(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if page != 2 {
		t.Fatalf("expected exactly 2 page requests, got %d (pagination broke out early — the exact bug being guarded against)", page)
	}
	if len(index) != 3 {
		t.Fatalf("expected 3 file entries (2 from scene 1, 1 from scene 2), got %d: %+v", len(index), index)
	}
	if _, ok := index["/a/file2.mp4"]; !ok {
		t.Fatal("scene #2's file is missing from the index — page 2 was never fetched")
	}
}

func TestLoadAllScenes_StudioFallsBackToParentStudio(t *testing.T) {
	c, closeSrv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"findScenes":{"count":1,"scenes":[
			{"id":"1","title":"T","date":"2024-01-01",
			 "studio":{"name":"","parent_studio":{"name":"Parent Studio"}},
			 "stash_ids":[],
			 "files":[{"path":"/a/f.mp4","width":0,"height":0,"duration":0,"video_codec":"","bit_rate":0,"fingerprints":[]}]}
		]}}}`))
	})
	defer closeSrv()

	index, err := c.LoadAllScenes(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if index["/a/f.mp4"].Studio != "Parent Studio" {
		t.Fatalf("expected studio to fall back to parent_studio, got %q", index["/a/f.mp4"].Studio)
	}
}

func TestLoadAllScenes_EmptyLibrary(t *testing.T) {
	c, closeSrv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"findScenes":{"count":0,"scenes":[]}}}`))
	})
	defer closeSrv()

	index, err := c.LoadAllScenes(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(index) != 0 {
		t.Fatalf("expected empty index, got %d entries", len(index))
	}
}

func TestFindSceneInfoByPath_NotFound(t *testing.T) {
	c, closeSrv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"findScenes":{"scenes":[]}}}`))
	})
	defer closeSrv()

	info, err := c.FindSceneInfoByPath(context.Background(), "/nonexistent.mp4")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info != nil {
		t.Fatalf("expected nil for not-found path, got %+v", info)
	}
}

func TestScanPaths_SendsRescanFlagAndReturnsJobID(t *testing.T) {
	c, closeSrv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Variables struct {
				Input map[string]any `json:"input"`
			} `json:"variables"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Variables.Input["rescan"] != true {
			t.Errorf("expected rescan=true in request, got %v", req.Variables.Input["rescan"])
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"metadataScan":"42"}}`))
	})
	defer closeSrv()

	jobID, err := c.ScanPaths(context.Background(), []string{"/a/f.mp4"}, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if jobID != "42" {
		t.Fatalf("expected job id 42, got %q", jobID)
	}
}

func TestWaitJob_FinishedImmediately(t *testing.T) {
	c, closeSrv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"findJob":{"status":"FINISHED"}}}`))
	})
	defer closeSrv()

	ok, err := c.WaitJob(context.Background(), "1", 10*time.Millisecond)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("expected true for FINISHED job")
	}
}

func TestWaitJob_ClearedFromQueueTreatedAsFinished(t *testing.T) {
	c, closeSrv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"findJob":null}}`))
	})
	defer closeSrv()

	ok, err := c.WaitJob(context.Background(), "1", 10*time.Millisecond)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("expected true when job is no longer in the queue")
	}
}

func TestWaitJob_FailedReturnsFalse(t *testing.T) {
	c, closeSrv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"findJob":{"status":"FAILED"}}}`))
	})
	defer closeSrv()

	ok, err := c.WaitJob(context.Background(), "1", 10*time.Millisecond)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Fatal("expected false for FAILED job")
	}
}

func TestWaitJob_PollsUntilFinished(t *testing.T) {
	calls := 0
	c, closeSrv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		if calls < 3 {
			_, _ = w.Write([]byte(`{"data":{"findJob":{"status":"RUNNING"}}}`))
		} else {
			_, _ = w.Write([]byte(`{"data":{"findJob":{"status":"FINISHED"}}}`))
		}
	})
	defer closeSrv()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ok, err := c.WaitJob(ctx, "1", 5*time.Millisecond)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("expected eventual FINISHED to return true")
	}
	if calls < 3 {
		t.Fatalf("expected at least 3 polls, got %d", calls)
	}
}

func TestWaitJob_ContextDeadlineExceeded(t *testing.T) {
	c, closeSrv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"findJob":{"status":"RUNNING"}}}`))
	})
	defer closeSrv()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	ok, err := c.WaitJob(ctx, "1", 10*time.Millisecond)
	if err == nil {
		t.Fatal("expected a context deadline error")
	}
	if ok {
		t.Fatal("expected false on timeout")
	}
}

func TestDestroyScenes_SendsCorrectFlags(t *testing.T) {
	c, closeSrv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Variables struct {
				Input map[string]any `json:"input"`
			} `json:"variables"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Variables.Input["delete_file"] != true {
			t.Errorf("expected delete_file=true")
		}
		if req.Variables.Input["delete_generated"] != true {
			t.Errorf("expected delete_generated=true")
		}
		ids, _ := req.Variables.Input["ids"].([]any)
		if len(ids) != 2 {
			t.Errorf("expected 2 ids, got %v", req.Variables.Input["ids"])
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"scenesDestroy":true}}`))
	})
	defer closeSrv()

	err := c.DestroyScenes(context.Background(), []string{"1", "2"}, true, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestFindScenesByTagIDs_ParsesShape(t *testing.T) {
	c, closeSrv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"findScenes":{"scenes":[
			{"id":"1","title":"T","tags":[{"name":"BDSM"}],"files":[{"path":"/a/f.mp4"}]}
		]}}}`))
	})
	defer closeSrv()

	out, err := c.FindScenesByTagIDs(context.Background(), []string{"1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 1 || out[0].Path != "/a/f.mp4" || len(out[0].Tags) != 1 || out[0].Tags[0] != "BDSM" {
		t.Fatalf("got %+v", out)
	}
}

func TestFindScenesByTagIDs_NoFilesGivesEmptyPath(t *testing.T) {
	c, closeSrv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"findScenes":{"scenes":[
			{"id":"1","title":"T","tags":[],"files":[]}
		]}}}`))
	})
	defer closeSrv()

	out, err := c.FindScenesByTagIDs(context.Background(), []string{"1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out[0].Path != "" {
		t.Fatalf("expected empty path for a scene with no files, got %q", out[0].Path)
	}
}

func TestStashBoxConfigs_ParsesResponse(t *testing.T) {
	c, closeSrv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"configuration":{"general":{"stashBoxes":[
			{"endpoint":"https://stashdb.org/graphql","api_key":"k1"},
			{"endpoint":"https://fansdb.cc/graphql","api_key":"k2"}
		]}}}}`))
	})
	defer closeSrv()

	out, err := c.StashBoxConfigs(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 2 || out[1].Endpoint != "https://fansdb.cc/graphql" {
		t.Fatalf("got %+v", out)
	}
}

// Fail-fast behavior: LoadAllScenes against an unreachable Stash must return
// a clear error, not panic.
func TestLoadAllScenes_UnreachableStash_ReturnsError(t *testing.T) {
	// A closed server: connecting will fail immediately.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close() // close immediately so the port is unreachable

	c := New(Config{URL: url, APIKey: "k"}, &http.Client{Timeout: 2 * time.Second})
	_, err := c.LoadAllScenes(context.Background())
	if err == nil {
		t.Fatal("expected an error when Stash is unreachable, got nil (would have proceeded with a nil/empty index)")
	}
}

func TestGraphQLErrors_Surfaced(t *testing.T) {
	c, closeSrv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"errors":[{"message":"some stash error"}]}`))
	})
	defer closeSrv()

	_, err := c.AllTags(context.Background())
	if err == nil {
		t.Fatal("expected an error when the response contains GraphQL errors")
	}
}

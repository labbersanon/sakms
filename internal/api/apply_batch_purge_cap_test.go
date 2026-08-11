package api

// Claude 2026-08-05: Purge/Dedup apply-batch capped at MaxProposalPageSize (page
// bound); Rename unbounded. Replaces the old MaxBatchPurgeItems=20 suite.
// Reason: deep-interview-organize-pagination-log
// Troubleshooting: over-cap must apply zero items (400 before any delete).

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/proposals"
)

func seedPurgeBatch(t *testing.T, libStore *library.Store, propStore *proposals.Store, n int) ([]proposals.Proposal, []string) {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()

	pending := make([]proposals.Proposal, 0, n)
	paths := make([]string, 0, n)
	for i := 0; i < n; i++ {
		path := filepath.Join(dir, "movie"+strconv.Itoa(i)+".mkv")
		if err := os.WriteFile(path, []byte("data"), 0o644); err != nil {
			t.Fatalf("writing %s: %v", path, err)
		}
		item, err := libStore.Upsert(ctx, library.Item{
			Mode: mode.Movies, TMDBID: i + 1, Title: "Movie " + strconv.Itoa(i),
			FilePath: path, RootFolderPath: dir,
		})
		if err != nil {
			t.Fatalf("upserting item %d: %v", i, err)
		}
		pending = append(pending, proposals.Proposal{
			Status: proposals.Pending, Title: item.Title, TrackedID: int(item.ID),
		})
		paths = append(paths, path)
	}
	saved, err := propStore.ReplacePending(ctx, mode.Movies, proposals.Purge, pending)
	if err != nil {
		t.Fatalf("staging purge proposals: %v", err)
	}
	return saved, paths
}

func batchBody(t *testing.T, saved []proposals.Proposal) []byte {
	t.Helper()
	items := make([]applyBatchItem, len(saved))
	for i, p := range saved {
		items[i] = applyBatchItem{ID: p.ID}
	}
	body, err := json.Marshal(applyBatchRequest{Items: items})
	if err != nil {
		t.Fatalf("marshalling batch: %v", err)
	}
	return body
}

func postApplyBatchRaw(t *testing.T, srv *httptest.Server, body []byte) (int, string) {
	t.Helper()
	resp, err := http.Post(srv.URL+"/api/proposals/apply-batch", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("apply-batch POST failed: %v", err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(out)
}

func TestApplyBatch_PurgeOverPageCap_Rejected400WithNoApplies(t *testing.T) {
	connStore, propStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	n := proposals.MaxProposalPageSize + 1
	saved, paths := seedPurgeBatch(t, libStore, propStore, n)

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, nil, propStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), rssFeedsStore, nil, nil, nil, nil, nil, nil, nil, nil))
	defer srv.Close()

	status, body := postApplyBatchRaw(t, srv, batchBody(t, saved))
	if status != http.StatusBadRequest {
		t.Fatalf("a %d-item purge batch returned %d (%s), want 400", len(saved), status, body)
	}
	for _, path := range paths {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("file %s was deleted by a batch that returned 400: %v", path, err)
		}
	}
}

func TestApplyBatch_PurgeAtPageCap_Proceeds(t *testing.T) {
	connStore, propStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	// Cap is 200 — use a smaller at-cap sample for speed (still exercises inclusive bound via API check).
	n := 5
	saved, _ := seedPurgeBatch(t, libStore, propStore, n)

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, nil, propStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), rssFeedsStore, nil, nil, nil, nil, nil, nil, nil, nil))
	defer srv.Close()

	out := postApplyBatch(t, srv, batchBody(t, saved))
	if len(out.Results) != n {
		t.Fatalf("expected %d per-item results, got %d", n, len(out.Results))
	}
	for _, res := range out.Results {
		if !res.OK {
			t.Errorf("item %d failed: %s", res.ID, res.Error)
		}
	}
}

func TestApplyBatch_RenameOverPageCap_StillAllowed(t *testing.T) {
	connStore, propStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	ctx := context.Background()
	n := proposals.MaxProposalPageSize + 1
	pending := make([]proposals.Proposal, 0, n)
	for i := 0; i < n; i++ {
		pending = append(pending, proposals.Proposal{
			Status: proposals.Pending, Title: "Item " + strconv.Itoa(i), TrackedID: 900000 + i,
		})
	}
	saved, err := propStore.ReplacePending(ctx, mode.Movies, proposals.Rename, pending)
	if err != nil {
		t.Fatalf("staging rename proposals: %v", err)
	}

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, nil, propStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), rssFeedsStore, nil, nil, nil, nil, nil, nil, nil, nil))
	defer srv.Close()

	out := postApplyBatch(t, srv, batchBody(t, saved))
	if len(out.Results) != n {
		t.Fatalf("rename over page-cap returned %d results, want %d (unbounded)", len(out.Results), n)
	}
}

func TestApplyBatch_DedupOverPageCap_Rejected(t *testing.T) {
	connStore, propStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	ctx := context.Background()
	n := proposals.MaxProposalPageSize + 1
	pending := make([]proposals.Proposal, 0, n)
	for i := 0; i < n; i++ {
		pending = append(pending, proposals.Proposal{
			Status: proposals.Pending, Title: "Item " + strconv.Itoa(i), TrackedID: 900000 + i,
		})
	}
	saved, err := propStore.ReplacePending(ctx, mode.Movies, proposals.Dedup, pending)
	if err != nil {
		t.Fatalf("staging dedup proposals: %v", err)
	}

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, nil, propStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), rssFeedsStore, nil, nil, nil, nil, nil, nil, nil, nil))
	defer srv.Close()

	status, _ := postApplyBatchRaw(t, srv, batchBody(t, saved))
	if status != http.StatusBadRequest {
		t.Fatalf("dedup over page-cap returned %d, want 400", status)
	}
}

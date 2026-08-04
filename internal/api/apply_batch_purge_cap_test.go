package api

// Claude 2026-08-03: new file — the Purge-only apply-batch cap (B6, plan §13.2).
// Reason: separate from apply_batch_test.go so the cap's three cases read as
// one story (over-cap refused whole, at-cap allowed, other workflows
// unaffected) rather than being scattered through the partial-failure suite.
// Troubleshooting: the over-cap case asserts ZERO applies, not just the 400 —
// a cap enforced after the first Apply would still delete a file.

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

// seedPurgeBatch writes n real files, tracks each as a Movies library item, and
// stages one Pending Purge proposal per item.
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

// batchBody builds an apply-batch body naming every proposal in saved.
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

// TestApplyBatch_PurgeOverCap_Rejected400WithNoApplies is §13.2's core
// guarantee: 21 Purge items is refused before any delete runs.
func TestApplyBatch_PurgeOverCap_Rejected400WithNoApplies(t *testing.T) {
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	saved, paths := seedPurgeBatch(t, libStore, propStore, MaxBatchPurgeItems+1)

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, nil, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), rssFeedsStore, nil, nil, nil, nil, nil, nil, nil, nil))
	defer srv.Close()

	status, body := postApplyBatchRaw(t, srv, batchBody(t, saved))
	if status != http.StatusBadRequest {
		t.Fatalf("a %d-item purge batch returned %d (%s), want 400", len(saved), status, body)
	}

	// Zero applies: every file is still on disk and every proposal still
	// Pending. A cap checked mid-loop would already have deleted 20 files.
	for _, path := range paths {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("file %s was deleted by a batch that returned 400: %v", path, err)
		}
	}
	ctx := context.Background()
	for _, p := range saved {
		got, err := propStore.Get(ctx, p.ID)
		if err != nil {
			t.Fatalf("loading proposal %d: %v", p.ID, err)
		}
		if got.Status != proposals.Pending {
			t.Errorf("proposal %d is %q after a rejected batch, want still pending", p.ID, got.Status)
		}
	}
}

// Exactly at the cap proceeds — the boundary is inclusive, so an operator can
// still apply a full 20.
func TestApplyBatch_PurgeAtCap_Proceeds(t *testing.T) {
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	saved, _ := seedPurgeBatch(t, libStore, propStore, MaxBatchPurgeItems)

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, nil, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), rssFeedsStore, nil, nil, nil, nil, nil, nil, nil, nil))
	defer srv.Close()

	out := postApplyBatch(t, srv, batchBody(t, saved))
	if len(out.Results) != MaxBatchPurgeItems {
		t.Fatalf("expected %d per-item results, got %d", MaxBatchPurgeItems, len(out.Results))
	}
	for _, res := range out.Results {
		if !res.OK {
			t.Errorf("item %d failed in an at-cap batch: %s", res.ID, res.Error)
		}
	}
}

// Rename and Dedup keep the shared 200-item cap: a 21-item batch of either is
// still accepted, which is what makes the new cap Purge-ONLY rather than a
// global tightening.
func TestApplyBatch_NonPurgeOverPurgeCap_StillAllowed(t *testing.T) {
	for _, wf := range []proposals.Workflow{proposals.Rename, proposals.Dedup} {
		t.Run(string(wf), func(t *testing.T) {
			connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
			ctx := context.Background()

			// The proposals deliberately carry no resolvable target, so every
			// item fails at Apply and reports ok:false. That is the point: this
			// test asserts the request was ACCEPTED (200, one result per item),
			// not that a rename/dedup succeeded.
			pending := make([]proposals.Proposal, 0, MaxBatchPurgeItems+1)
			for i := 0; i < MaxBatchPurgeItems+1; i++ {
				pending = append(pending, proposals.Proposal{
					Status: proposals.Pending, Title: "Item " + strconv.Itoa(i), TrackedID: 900000 + i,
				})
			}
			saved, err := propStore.ReplacePending(ctx, mode.Movies, wf, pending)
			if err != nil {
				t.Fatalf("staging %s proposals: %v", wf, err)
			}

			srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, nil, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), rssFeedsStore, nil, nil, nil, nil, nil, nil, nil, nil))
			defer srv.Close()

			out := postApplyBatch(t, srv, batchBody(t, saved))
			if len(out.Results) != MaxBatchPurgeItems+1 {
				t.Fatalf("a %d-item %s batch returned %d results — the purge cap leaked onto %s",
					MaxBatchPurgeItems+1, wf, len(out.Results), wf)
			}
		})
	}
}

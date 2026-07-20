package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/proposals"
)

// markAppliedFailStore wraps a real *proposals.Store and forces MarkApplied to
// fail for one specific proposal ID, leaving every other method (Get,
// MarkFingerprintSubmitted, …) delegating to the real store. It exists to
// exercise the post-commit-failure branch that can't be induced with a real
// store: the physical move already happened (rename.ApplyLibrary relocated the
// file before applyByWorkflow reaches MarkApplied), so its changes must still
// reach the combined NotifyPlayers even though the item is reported OK:false.
type markAppliedFailStore struct {
	*proposals.Store
	failID int64
}

func (s markAppliedFailStore) MarkApplied(ctx context.Context, id int64, trackedID int) error {
	if id == s.failID {
		return fmt.Errorf("simulated MarkApplied failure for proposal %d", id)
	}
	return s.Store.MarkApplied(ctx, id, trackedID)
}

// postApplyBatch POSTs /api/proposals/apply-batch and decodes the response,
// asserting the always-200 contract (per-item ok/error lives in the body, not
// the status code).
func postApplyBatch(t *testing.T, srv *httptest.Server, body []byte) applyBatchResponse {
	t.Helper()
	resp, err := http.Post(srv.URL+"/api/proposals/apply-batch", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("apply-batch POST failed: %v", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 from apply-batch, got %d: %s", resp.StatusCode, respBody)
	}
	var out applyBatchResponse
	if err := json.Unmarshal(respBody, &out); err != nil {
		t.Fatalf("decoding apply-batch response: %v", err)
	}
	return out
}

// TestApplyBatch_PartialFailure_SkipsAndContinues is the partial-failure
// regression: a 3-item batch whose middle item fails at Apply must still apply
// items 1 and 3, report all three outcomes individually in request order, and
// never abort early. The middle item is a Purge proposal whose TrackedID
// points at a library row that doesn't exist, so purge.ApplyLibrary errors
// after the batch has already committed item 1 and before it reaches item 3.
func TestApplyBatch_PartialFailure_SkipsAndContinues(t *testing.T) {
	dir := t.TempDir()
	file1 := filepath.Join(dir, "one.mkv")
	file3 := filepath.Join(dir, "three.mkv")
	if err := os.WriteFile(file1, []byte("data"), 0o644); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := os.WriteFile(file3, []byte("data"), 0o644); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	ctx := context.Background()

	item1, err := libStore.Upsert(ctx, library.Item{Mode: mode.Movies, TMDBID: 1, Title: "One", FilePath: file1, RootFolderPath: dir})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	item3, err := libStore.Upsert(ctx, library.Item{Mode: mode.Movies, TMDBID: 3, Title: "Three", FilePath: file3, RootFolderPath: dir})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Item 2's TrackedID references no library row — a deterministic
	// apply-stage failure (libStore.Get inside purge.ApplyLibrary).
	const missingTrackedID = 999999
	saved, err := propStore.ReplacePending(ctx, mode.Movies, proposals.Purge, []proposals.Proposal{
		{Status: proposals.Pending, Title: "One", TrackedID: int(item1.ID)},
		{Status: proposals.Pending, Title: "Two", TrackedID: missingTrackedID},
		{Status: proposals.Pending, Title: "Three", TrackedID: int(item3.ID)},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, nil, nil, nil, nil, nil))
	defer srv.Close()

	body, _ := json.Marshal(applyBatchRequest{Items: []applyBatchItem{
		{ID: saved[0].ID}, {ID: saved[1].ID}, {ID: saved[2].ID},
	}})
	out := postApplyBatch(t, srv, body)

	if len(out.Results) != 3 {
		t.Fatalf("expected exactly 3 per-item results, got %d: %+v", len(out.Results), out.Results)
	}
	// Order must match the request order.
	if out.Results[0].ID != saved[0].ID || out.Results[1].ID != saved[1].ID || out.Results[2].ID != saved[2].ID {
		t.Fatalf("results out of request order: %+v", out.Results)
	}
	if !out.Results[0].OK {
		t.Errorf("expected item 1 to succeed, got %+v", out.Results[0])
	}
	if out.Results[1].OK || out.Results[1].Error == "" {
		t.Errorf("expected item 2 to fail with an error, got %+v", out.Results[1])
	}
	if out.Results[1].Proposal != nil {
		t.Errorf("expected no proposal on the failed item, got %+v", out.Results[1].Proposal)
	}
	if !out.Results[2].OK {
		t.Errorf("expected item 3 to succeed despite item 2's failure (no early abort), got %+v", out.Results[2])
	}

	// Successful items carry their refreshed, now-Applied proposal.
	if out.Results[0].Proposal == nil || out.Results[0].Proposal.Status != proposals.Applied {
		t.Errorf("expected item 1's proposal to be Applied, got %+v", out.Results[0].Proposal)
	}
	if out.Results[2].Proposal == nil || out.Results[2].Proposal.Status != proposals.Applied {
		t.Errorf("expected item 3's proposal to be Applied, got %+v", out.Results[2].Proposal)
	}

	// The two successful purges actually deleted their files; the failed
	// middle item left nothing half-done that affected the others.
	if _, err := os.Stat(file1); !os.IsNotExist(err) {
		t.Errorf("expected item 1's file to be deleted, stat returned: %v", err)
	}
	if _, err := os.Stat(file3); !os.IsNotExist(err) {
		t.Errorf("expected item 3's file to be deleted, stat returned: %v", err)
	}

	// The failed proposal is still Pending (not silently marked Applied).
	stillPending, err := propStore.Get(ctx, saved[1].ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stillPending.Status != proposals.Pending {
		t.Errorf("expected the failed item to remain Pending, got %q", stillPending.Status)
	}
}

// TestApplyBatch_CombinedNotify_OneCallBothItemsChanges proves the combined
// notify: a 2-item successful batch fires NotifyPlayers exactly once, and that
// single call carries BOTH items' file changes — not one notify per item. Each
// Movies Rename contributes {source Deleted, dest Created}, so the one Jellyfin
// batch must contain all four entries.
func TestApplyBatch_CombinedNotify_OneCallBothItemsChanges(t *testing.T) {
	base := t.TempDir()
	src1 := filepath.Join(base, "incoming", "First.mkv")
	src2 := filepath.Join(base, "incoming", "Second.mkv")
	destRoot := filepath.Join(base, "Movies")
	if err := os.MkdirAll(filepath.Join(base, "incoming"), 0o755); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := os.MkdirAll(destRoot, 0o755); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := os.WriteFile(src1, []byte("data"), 0o644); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := os.WriteFile(src2, []byte("data"), 0o644); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	ctx := context.Background()
	jf := newFakeJellyfin(0)
	if err := connStore.Upsert(ctx, "jellyfin", jf.Server(t).URL, "jf-key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	saved, err := propStore.ReplacePending(ctx, mode.Movies, proposals.Rename, []proposals.Proposal{
		{Status: proposals.Pending, SourceName: "First", SourcePath: src1, RootFolderPath: destRoot, Title: "First Movie", TMDBID: 101, Year: 2020},
		{Status: proposals.Pending, SourceName: "Second", SourcePath: src2, RootFolderPath: destRoot, Title: "Second Movie", TMDBID: 102, Year: 2021},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, nil, nil, nil, nil, nil))
	defer srv.Close()

	body, _ := json.Marshal(applyBatchRequest{Items: []applyBatchItem{
		{ID: saved[0].ID}, {ID: saved[1].ID},
	}})
	out := postApplyBatch(t, srv, body)

	if len(out.Results) != 2 || !out.Results[0].OK || !out.Results[1].OK {
		t.Fatalf("expected two successful results, got %+v", out.Results)
	}

	// Exactly one combined notify call for the whole batch.
	if jf.CallCount() != 1 {
		t.Fatalf("expected exactly one combined NotifyPlayers call for the batch, got %d: %+v", jf.CallCount(), jf.Batches())
	}

	// The resolved destinations both items moved to.
	item1, err := libStore.Get(ctx, int64(out.Results[0].Proposal.TrackedID))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	item2, err := libStore.Get(ctx, int64(out.Results[1].Proposal.TrackedID))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	batch := jf.Batches()[0]
	if len(batch) != 4 {
		t.Fatalf("expected the single batch to carry all 4 changes (2 per item), got %d: %+v", len(batch), batch)
	}
	want := map[jellyfinUpdate]bool{
		{Path: src1, UpdateType: "Deleted"}:           true,
		{Path: item1.FilePath, UpdateType: "Created"}: true,
		{Path: src2, UpdateType: "Deleted"}:           true,
		{Path: item2.FilePath, UpdateType: "Created"}: true,
	}
	for _, u := range batch {
		if !want[u] {
			t.Errorf("unexpected entry in combined batch: %+v", u)
		}
		delete(want, u)
	}
	if len(want) != 0 {
		t.Errorf("combined batch missing entries: %v", want)
	}
}

// TestApplyBatch_CommittedItemErrors_ChangesStillInCombinedNotify is the
// partial-success regression the plan's success-only accumulation would have
// missed: a 2-item Movies Rename batch where item 2's file physically moves
// (rename.ApplyLibrary relocates it and records the library item) but the
// subsequent MarkApplied DB write fails. Item 2 must be reported OK:false, yet
// its committed {source Deleted, dest Created} changes must STILL ride in the
// single combined NotifyPlayers call alongside item 1's — because the file
// really moved and the players have to know, exactly as the single-item apply
// path notifies unconditionally.
func TestApplyBatch_CommittedItemErrors_ChangesStillInCombinedNotify(t *testing.T) {
	base := t.TempDir()
	src1 := filepath.Join(base, "incoming", "First.mkv")
	src2 := filepath.Join(base, "incoming", "Second.mkv")
	destRoot := filepath.Join(base, "Movies")
	if err := os.MkdirAll(filepath.Join(base, "incoming"), 0o755); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := os.MkdirAll(destRoot, 0o755); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := os.WriteFile(src1, []byte("data"), 0o644); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := os.WriteFile(src2, []byte("data"), 0o644); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	connStore, propStore, _, settingsStore, _, libStore, _, _, _, _, _ := testStores(t)
	ctx := context.Background()
	jf := newFakeJellyfin(0)
	if err := connStore.Upsert(ctx, "jellyfin", jf.Server(t).URL, "jf-key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	saved, err := propStore.ReplacePending(ctx, mode.Movies, proposals.Rename, []proposals.Proposal{
		{Status: proposals.Pending, SourceName: "First", SourcePath: src1, RootFolderPath: destRoot, Title: "First Movie", TMDBID: 201, Year: 2020},
		{Status: proposals.Pending, SourceName: "Second", SourcePath: src2, RootFolderPath: destRoot, Title: "Second Movie", TMDBID: 202, Year: 2021},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The batch handler is mounted directly (not via NewMux) so its propStore
	// can be the fail-injecting wrapper — item 2's MarkApplied will fail after
	// its file has already moved.
	failStore := markAppliedFailStore{Store: propStore, failID: saved[1].ID}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/proposals/apply-batch", applyBatchHandler(testHTTPClient(), connStore, settingsStore, failStore, libStore, nil))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	body, _ := json.Marshal(applyBatchRequest{Items: []applyBatchItem{
		{ID: saved[0].ID}, {ID: saved[1].ID},
	}})
	out := postApplyBatch(t, srv, body)

	if len(out.Results) != 2 {
		t.Fatalf("expected 2 results, got %+v", out.Results)
	}
	if !out.Results[0].OK {
		t.Errorf("expected item 1 to fully succeed, got %+v", out.Results[0])
	}
	if out.Results[1].OK || out.Results[1].Error == "" {
		t.Errorf("expected item 2 to be reported OK:false with an error (MarkApplied failed), got %+v", out.Results[1])
	}
	if out.Results[1].Proposal != nil {
		t.Errorf("expected no proposal on the errored item, got %+v", out.Results[1].Proposal)
	}

	// Item 2's proposal never got marked Applied (its DB write failed), even
	// though its file moved and its library item was recorded.
	stillPending, err := propStore.Get(ctx, saved[1].ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stillPending.Status != proposals.Pending {
		t.Errorf("expected item 2 to remain Pending after the MarkApplied failure, got %q", stillPending.Status)
	}

	// Exactly one combined notify, carrying BOTH items' changes — item 2's
	// included despite its OK:false result.
	if jf.CallCount() != 1 {
		t.Fatalf("expected exactly one combined NotifyPlayers call, got %d: %+v", jf.CallCount(), jf.Batches())
	}

	item1, err := libStore.Get(ctx, int64(out.Results[0].Proposal.TrackedID))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Item 2's library item exists even though MarkApplied failed — Upsert ran
	// before MarkApplied — so look it up by its TMDB identity.
	item2, err := libStore.GetByTMDBID(ctx, mode.Movies, 202)
	if err != nil {
		t.Fatalf("expected item 2's library row to exist despite the MarkApplied failure: %v", err)
	}

	batch := jf.Batches()[0]
	if len(batch) != 4 {
		t.Fatalf("expected all 4 changes (both items) in the one batch, got %d: %+v", len(batch), batch)
	}
	want := map[jellyfinUpdate]bool{
		{Path: src1, UpdateType: "Deleted"}:           true,
		{Path: item1.FilePath, UpdateType: "Created"}: true,
		{Path: src2, UpdateType: "Deleted"}:           true,
		{Path: item2.FilePath, UpdateType: "Created"}: true,
	}
	for _, u := range batch {
		if !want[u] {
			t.Errorf("unexpected entry in combined batch: %+v", u)
		}
		delete(want, u)
	}
	if len(want) != 0 {
		t.Errorf("combined batch missing the committed-but-errored item's changes: %v", want)
	}
}

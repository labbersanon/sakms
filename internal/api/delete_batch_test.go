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
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/labbersanon/sakms/internal/connections"
	"github.com/labbersanon/sakms/internal/dbtest"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/organizeevents"
	"github.com/labbersanon/sakms/internal/proposals"
	"github.com/labbersanon/sakms/internal/secrets"
	"github.com/labbersanon/sakms/internal/sectionlock"
	"github.com/labbersanon/sakms/internal/serviceconn"
	"github.com/labbersanon/sakms/internal/settings"
	"github.com/labbersanon/sakms/internal/webhooks"
)

// delete_batch_test.go — POST /api/proposals/delete-batch, Rename's Delete
// action. This is the only route in the app that permanently destroys a file,
// so the tests here are weighted toward the two properties a code read cannot
// keep true on its own:
//
//   - TestDeleteBatch_AdultLockedRefusedBeforeFileRemoved pins the
//     mode.Build-before-DeleteSource security ordering. Its load-bearing
//     assertion is os.Stat succeeding, not the error string.
//   - TestDeleteBatch_LogsDeleteBatchOrganizeEvent pins the workflow/mode
//     capture. Its load-bearing assertions are Workflow == "rename" and
//     retrieval through Store.List's real filtered query — a version checking
//     only Kind and the message passes with the empty-workflow bug present.

// deleteFailStore wraps a real *proposals.Store and forces Delete to fail for
// one specific proposal id, leaving every other method delegating to the real
// store. It exercises the partial-failure branch a real store cannot produce:
// os.Remove already committed, so the file really is gone, yet the row delete
// errored — the change must still reach the grouped NotifyPlayers even though
// the item reports OK:false. Same seam and same reasoning as
// markAppliedFailStore in apply_batch_test.go.
type deleteFailStore struct {
	*proposals.Store
	failID int64
}

func (s deleteFailStore) Delete(ctx context.Context, id int64) error {
	if id == s.failID {
		return fmt.Errorf("simulated row-delete failure for proposal %d", id)
	}
	return s.Store.Delete(ctx, id)
}

// deleteBatchMux mounts ONLY the delete-batch route, so nothing else in NewMux
// can account for an observed side effect.
func deleteBatchMux(
	connStore *connections.Store,
	scStore *serviceconn.Store,
	settingsStore *settings.Store,
	propStore proposalDeleteStore,
) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/proposals/delete-batch",
		deleteBatchHandler(testHTTPClient(), connStore, scStore, settingsStore, propStore))
	return mux
}

// postDeleteBatchRaw POSTs and returns the status plus the raw body, for the
// tests that assert a non-200 or inspect the JSON shape directly.
func postDeleteBatchRaw(t *testing.T, srv *httptest.Server, body []byte) (int, []byte) {
	t.Helper()
	resp, err := http.Post(srv.URL+"/api/proposals/delete-batch", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("delete-batch POST failed: %v", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, respBody
}

// postDeleteBatch POSTs and decodes, asserting the always-200 contract:
// per-item success lives in the body, never in the HTTP status. A partial
// failure is still a 200.
func postDeleteBatch(t *testing.T, srv *httptest.Server, items []deleteBatchItem) deleteBatchResponse {
	t.Helper()
	body, err := json.Marshal(deleteBatchRequest{Items: items})
	if err != nil {
		t.Fatalf("marshalling request: %v", err)
	}
	status, respBody := postDeleteBatchRaw(t, srv, body)
	if status != http.StatusOK {
		t.Fatalf("expected 200 from delete-batch, got %d: %s", status, respBody)
	}
	var out deleteBatchResponse
	if err := json.Unmarshal(respBody, &out); err != nil {
		t.Fatalf("decoding delete-batch response: %v", err)
	}
	return out
}

func writeTempFile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("creating parent of %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte("data"), 0o644); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}

func assertGone(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("expected %s to be deleted, stat returned: %v", path, err)
	}
}

func assertStillOnDisk(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Errorf("expected %s to still exist on disk, stat returned: %v", path, err)
	}
}

func assertRowGone(t *testing.T, propStore *proposals.Store, id int64) {
	t.Helper()
	if _, err := propStore.Get(context.Background(), id); err == nil {
		t.Errorf("expected proposal row %d to be destroyed, but Get succeeded", id)
	}
}

func assertRowPresent(t *testing.T, propStore *proposals.Store, id int64) {
	t.Helper()
	if _, err := propStore.Get(context.Background(), id); err != nil {
		t.Errorf("expected proposal row %d to survive, Get returned: %v", id, err)
	}
}

// --- 1. Happy path -------------------------------------------------------

func TestDeleteBatch_HappyPath(t *testing.T) {
	dir := t.TempDir()
	paths := []string{
		filepath.Join(dir, "one.mkv"),
		filepath.Join(dir, "two.mkv"),
		filepath.Join(dir, "three.mkv"),
	}
	for _, p := range paths {
		writeTempFile(t, p)
	}

	connStore, propStore, settingsStore, _, _, _, _, _, _, _, scStore := testStoresWithRegistry(t)
	ctx := context.Background()

	saved, err := propStore.ReplacePending(ctx, mode.Movies, proposals.Rename, []proposals.Proposal{
		{Status: proposals.Pending, SourceName: "one", SourcePath: paths[0]},
		{Status: proposals.Pending, SourceName: "two", SourcePath: paths[1]},
		{Status: proposals.Pending, SourceName: "three", SourcePath: paths[2]},
	})
	if err != nil {
		t.Fatalf("seeding proposals: %v", err)
	}

	srv := httptest.NewServer(deleteBatchMux(connStore, scStore, settingsStore, propStore))
	defer srv.Close()

	out := postDeleteBatch(t, srv, []deleteBatchItem{
		{ID: saved[0].ID}, {ID: saved[1].ID}, {ID: saved[2].ID},
	})

	if len(out.Results) != 3 {
		t.Fatalf("expected 3 per-item results, got %d: %+v", len(out.Results), out.Results)
	}
	for i, r := range out.Results {
		if r.ID != saved[i].ID {
			t.Fatalf("results out of request order at %d: %+v", i, out.Results)
		}
		if !r.OK {
			t.Errorf("expected item %d to succeed, got %+v", i, r)
		}
	}
	for i, p := range paths {
		assertGone(t, p)
		assertRowGone(t, propStore, saved[i].ID)
	}
}

// --- 2. The result item carries no proposal field ------------------------

// A deleted item has no row to refresh, and that absence IS the success
// condition — so the result shape must not carry a proposal key at all, not
// even a null one. Asserted on the REAL wire bytes, not on the Go struct.
func TestDeleteBatch_ResultItemHasNoProposalField(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "only.mkv")
	writeTempFile(t, src)

	connStore, propStore, settingsStore, _, _, _, _, _, _, _, scStore := testStoresWithRegistry(t)
	ctx := context.Background()

	saved, err := propStore.ReplacePending(ctx, mode.Movies, proposals.Rename, []proposals.Proposal{
		{Status: proposals.Pending, SourceName: "only", SourcePath: src},
	})
	if err != nil {
		t.Fatalf("seeding proposals: %v", err)
	}

	srv := httptest.NewServer(deleteBatchMux(connStore, scStore, settingsStore, propStore))
	defer srv.Close()

	body, _ := json.Marshal(deleteBatchRequest{Items: []deleteBatchItem{{ID: saved[0].ID}}})
	status, raw := postDeleteBatchRaw(t, srv, body)
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", status, raw)
	}

	var envelope struct {
		Results []map[string]json.RawMessage `json:"results"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("decoding raw response: %v", err)
	}
	if len(envelope.Results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(envelope.Results))
	}
	got := envelope.Results[0]
	if _, present := got["proposal"]; present {
		t.Fatalf("an OK delete result must carry NO proposal key at all, got keys %v", keysOf(got))
	}
	want := map[string]bool{"id": true, "ok": true}
	for k := range got {
		if !want[k] {
			t.Errorf("unexpected key %q on an OK delete result: %v", k, keysOf(got))
		}
		delete(want, k)
	}
	if len(want) != 0 {
		t.Errorf("OK delete result missing keys: %v", want)
	}
}

func keysOf(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// --- 3. Skip and continue ------------------------------------------------

// Four items, two of which fail for two DIFFERENT reasons, and the two good
// deletions must be genuinely unaffected.
//
// The forced os.Remove failure uses a path with a non-directory component
// (<dir>/regular-file/child.mkv where <dir>/regular-file is a regular file):
// os.Remove returns ENOTDIR, which os.IsNotExist reports as FALSE, so it is a
// genuine non-tolerated failure. Deliberately NOT a chmod-permissions setup
// (silently succeeds as root, making the test vacuous) and deliberately NOT a
// missing parent directory (that is ENOENT, which this feature treats as
// tolerated success — asserting a failure that never happens).
func TestDeleteBatch_SkipAndContinue_MixedOutcomes(t *testing.T) {
	dir := t.TempDir()
	good1 := filepath.Join(dir, "good1.mkv")
	good2 := filepath.Join(dir, "good2.mkv")
	appliedFile := filepath.Join(dir, "applied.mkv")
	blocker := filepath.Join(dir, "regular-file")
	enotdir := filepath.Join(blocker, "child.mkv")
	for _, p := range []string{good1, good2, appliedFile, blocker} {
		writeTempFile(t, p)
	}

	connStore, propStore, settingsStore, _, _, _, _, _, _, _, scStore := testStoresWithRegistry(t)
	ctx := context.Background()

	saved, err := propStore.ReplacePending(ctx, mode.Movies, proposals.Rename, []proposals.Proposal{
		{Status: proposals.Pending, SourceName: "good1", SourcePath: good1},
		{Status: proposals.Applied, SourceName: "applied", SourcePath: appliedFile},
		{Status: proposals.Pending, SourceName: "enotdir", SourcePath: enotdir},
		{Status: proposals.Pending, SourceName: "good2", SourcePath: good2},
	})
	if err != nil {
		t.Fatalf("seeding proposals: %v", err)
	}

	srv := httptest.NewServer(deleteBatchMux(connStore, scStore, settingsStore, propStore))
	defer srv.Close()

	out := postDeleteBatch(t, srv, []deleteBatchItem{
		{ID: saved[0].ID}, {ID: saved[1].ID}, {ID: saved[2].ID}, {ID: saved[3].ID},
	})

	if len(out.Results) != 4 {
		t.Fatalf("expected 4 results (never an early abort), got %d: %+v", len(out.Results), out.Results)
	}
	for i := range out.Results {
		if out.Results[i].ID != saved[i].ID {
			t.Fatalf("results out of request order: %+v", out.Results)
		}
	}
	if !out.Results[0].OK {
		t.Errorf("expected the first deletable item to succeed, got %+v", out.Results[0])
	}
	if out.Results[1].OK || !strings.Contains(out.Results[1].Error, "applied") {
		t.Errorf("expected the Applied item to be refused with an error naming its status, got %+v", out.Results[1])
	}
	if out.Results[2].OK || out.Results[2].Error == "" {
		t.Errorf("expected the ENOTDIR item to fail with a real os.Remove error, got %+v", out.Results[2])
	}
	if strings.Contains(strings.ToLower(out.Results[2].Error), "no such file") {
		t.Errorf("the forced failure must NOT be ENOENT (that is tolerated as success): %q", out.Results[2].Error)
	}
	if !out.Results[3].OK {
		t.Errorf("expected the last deletable item to succeed despite two earlier failures, got %+v", out.Results[3])
	}

	// The two good deletions really happened and are unaffected by the two
	// failures on either side of them.
	assertGone(t, good1)
	assertGone(t, good2)
	assertRowGone(t, propStore, saved[0].ID)
	assertRowGone(t, propStore, saved[3].ID)

	// Neither failure destroyed anything.
	assertStillOnDisk(t, appliedFile)
	assertStillOnDisk(t, blocker)
	assertRowPresent(t, propStore, saved[1].ID)
	assertRowPresent(t, propStore, saved[2].ID)
}

// --- 4. Grouped notify ---------------------------------------------------

// AC #8's direct assertion: a per-item endpoint (the dismiss-shaped design
// that was rejected) would show THREE inbound calls here instead of one.
func TestDeleteBatch_NotifyPlayersFiresOncePerMode(t *testing.T) {
	dir := t.TempDir()
	paths := []string{
		filepath.Join(dir, "a.mkv"),
		filepath.Join(dir, "b.mkv"),
		filepath.Join(dir, "c.mkv"),
	}
	for _, p := range paths {
		writeTempFile(t, p)
	}

	connStore, propStore, settingsStore, _, _, _, _, _, _, _, scStore := testStoresWithRegistry(t)
	ctx := context.Background()
	jf := newFakeJellyfin(0)
	seedJellyfinPlayer(t, scStore, jf.Server(t).URL, "jf-key", "movies")

	saved, err := propStore.ReplacePending(ctx, mode.Movies, proposals.Rename, []proposals.Proposal{
		{Status: proposals.Pending, SourceName: "a", SourcePath: paths[0]},
		{Status: proposals.Pending, SourceName: "b", SourcePath: paths[1]},
		{Status: proposals.Pending, SourceName: "c", SourcePath: paths[2]},
	})
	if err != nil {
		t.Fatalf("seeding proposals: %v", err)
	}

	srv := httptest.NewServer(deleteBatchMux(connStore, scStore, settingsStore, propStore))
	defer srv.Close()

	out := postDeleteBatch(t, srv, []deleteBatchItem{
		{ID: saved[0].ID}, {ID: saved[1].ID}, {ID: saved[2].ID},
	})
	for i, r := range out.Results {
		if !r.OK {
			t.Fatalf("item %d failed, the notify assertion below would be meaningless: %+v", i, r)
		}
	}

	if jf.CallCount() != 1 {
		t.Fatalf("expected exactly ONE grouped NotifyPlayers call for the whole batch, got %d: %+v", jf.CallCount(), jf.Batches())
	}
	batch := jf.Batches()[0]
	if len(batch) != 3 {
		t.Fatalf("expected the single batch to carry all 3 deleted paths, got %d: %+v", len(batch), batch)
	}
	want := map[jellyfinUpdate]bool{
		{Path: paths[0], UpdateType: "Deleted"}: true,
		{Path: paths[1], UpdateType: "Deleted"}: true,
		{Path: paths[2], UpdateType: "Deleted"}: true,
	}
	for _, u := range batch {
		if !want[u] {
			t.Errorf("unexpected entry in the grouped batch: %+v", u)
		}
		delete(want, u)
	}
	if len(want) != 0 {
		t.Errorf("grouped batch missing entries: %v", want)
	}
}

// --- 5. Committed delete + failed row delete -----------------------------

// Pins the "append changes BEFORE checking err" ordering: the file genuinely
// went away, so the players must be told even though the item is OK:false.
func TestDeleteBatch_NotifyPlayersFiresForCommittedDeleteWhenRowDeleteFailed(t *testing.T) {
	dir := t.TempDir()
	okPath := filepath.Join(dir, "ok.mkv")
	failPath := filepath.Join(dir, "rowfail.mkv")
	writeTempFile(t, okPath)
	writeTempFile(t, failPath)

	connStore, propStore, settingsStore, _, _, _, _, _, _, _, scStore := testStoresWithRegistry(t)
	ctx := context.Background()
	jf := newFakeJellyfin(0)
	seedJellyfinPlayer(t, scStore, jf.Server(t).URL, "jf-key", "movies")

	saved, err := propStore.ReplacePending(ctx, mode.Movies, proposals.Rename, []proposals.Proposal{
		{Status: proposals.Pending, SourceName: "ok", SourcePath: okPath},
		{Status: proposals.Pending, SourceName: "rowfail", SourcePath: failPath},
	})
	if err != nil {
		t.Fatalf("seeding proposals: %v", err)
	}

	failStore := deleteFailStore{Store: propStore, failID: saved[1].ID}
	srv := httptest.NewServer(deleteBatchMux(connStore, scStore, settingsStore, failStore))
	defer srv.Close()

	out := postDeleteBatch(t, srv, []deleteBatchItem{{ID: saved[0].ID}, {ID: saved[1].ID}})
	if len(out.Results) != 2 {
		t.Fatalf("expected 2 results, got %+v", out.Results)
	}
	if !out.Results[0].OK {
		t.Errorf("expected item 1 to fully succeed, got %+v", out.Results[0])
	}
	if out.Results[1].OK || out.Results[1].Error == "" {
		t.Errorf("expected item 2 to be OK:false with the row-delete error, got %+v", out.Results[1])
	}

	// The file really is gone even though its row survives — the degraded
	// state §5.1 documents.
	assertGone(t, failPath)
	assertRowPresent(t, propStore, saved[1].ID)

	if jf.CallCount() != 1 {
		t.Fatalf("expected exactly one grouped notify, got %d: %+v", jf.CallCount(), jf.Batches())
	}
	batch := jf.Batches()[0]
	found := false
	for _, u := range batch {
		if u.Path == failPath && u.UpdateType == "Deleted" {
			found = true
		}
	}
	if !found {
		t.Errorf("the committed deletion of the OK:false item must still reach the players, batch was %+v", batch)
	}
}

// --- 6. Adult section lock, refused BEFORE the file is removed -----------

// withLockedAdult injects the same resolved Decision auth.Middleware (Layer 1)
// puts in the request context in production. mode.Build's Layer 2
// (sectionlock.RequireMode) reads only that decision — and treats an ABSENT
// decision as ALLOW, which is why the decision must actually be installed and
// must carry Enforcing:true or this test proves nothing.
func withLockedAdult(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d := sectionlock.Decision{
			Enforcing: true,
			Locked:    sectionlock.NewSet(sectionlock.SectionAdultContent),
		}
		h.ServeHTTP(w, r.WithContext(sectionlock.WithDecision(r.Context(), d)))
	})
}

// THE load-bearing ordering test. /api/proposals/* classifies as {organize}
// only, so Layer 1 cannot see that this proposal's row is Adult; mode.Build is
// the ONLY enforcement. Deferring mode.Build until after DeleteSource — the
// tempting "build sessions lazily, notify is post-loop anyway" optimization —
// would delete the file and only then discover the lock.
//
// The os.Stat assertion is the entire point: an error-string-only test would
// still pass against a handler that removed the file first and reported the
// lock afterwards.
func TestDeleteBatch_AdultLockedRefusedBeforeFileRemoved(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "Some Scene.mp4")
	writeTempFile(t, src)

	connStore, propStore, settingsStore, _, _, _, _, _, _, _, scStore := testStoresWithRegistry(t)
	ctx := context.Background()

	saved, err := propStore.ReplacePending(ctx, mode.Adult, proposals.Rename, []proposals.Proposal{
		{Status: proposals.Pending, SourceName: "Some Scene", SourcePath: src},
	})
	if err != nil {
		t.Fatalf("seeding the Adult proposal: %v", err)
	}

	srv := httptest.NewServer(withLockedAdult(deleteBatchMux(connStore, scStore, settingsStore, propStore)))
	defer srv.Close()

	out := postDeleteBatch(t, srv, []deleteBatchItem{{ID: saved[0].ID}})
	if len(out.Results) != 1 {
		t.Fatalf("expected 1 result, got %+v", out.Results)
	}
	if out.Results[0].OK {
		t.Fatal("an Adult proposal was DELETED while adult-content is locked")
	}
	if !strings.Contains(out.Results[0].Error, sectionlock.ErrSectionLocked.Error()) {
		t.Fatalf("expected the section-lock error (not some unrelated Build failure), got %q", out.Results[0].Error)
	}

	// The whole point: the file was never touched.
	assertStillOnDisk(t, src)
	assertRowPresent(t, propStore, saved[0].ID)
}

// The pair. Without the lock the SAME request deletes the file — which is what
// proves the refusal above came from the lock and not from an Adult mode.Build
// that was going to fail in this harness regardless.
func TestDeleteBatch_AdultUnlockedDeletes(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "Some Scene.mp4")
	writeTempFile(t, src)

	connStore, propStore, settingsStore, _, _, _, _, _, _, _, scStore := testStoresWithRegistry(t)
	ctx := context.Background()

	saved, err := propStore.ReplacePending(ctx, mode.Adult, proposals.Rename, []proposals.Proposal{
		{Status: proposals.Pending, SourceName: "Some Scene", SourcePath: src},
	})
	if err != nil {
		t.Fatalf("seeding the Adult proposal: %v", err)
	}

	srv := httptest.NewServer(deleteBatchMux(connStore, scStore, settingsStore, propStore))
	defer srv.Close()

	out := postDeleteBatch(t, srv, []deleteBatchItem{{ID: saved[0].ID}})
	if len(out.Results) != 1 || !out.Results[0].OK {
		t.Fatalf("an unlocked Adult delete must succeed, got %+v", out.Results)
	}
	assertGone(t, src)
}

// --- 7. Hand-crafted requests cannot bypass the dropdown gating ----------

func TestDeleteBatch_RejectsAppliedAndDismissedViaHandCraftedRequest(t *testing.T) {
	dir := t.TempDir()
	appliedFile := filepath.Join(dir, "applied.mkv")
	dismissedFile := filepath.Join(dir, "dismissed.mkv")
	writeTempFile(t, appliedFile)
	writeTempFile(t, dismissedFile)

	connStore, propStore, settingsStore, _, _, _, _, _, _, _, scStore := testStoresWithRegistry(t)
	ctx := context.Background()

	saved, err := propStore.ReplacePending(ctx, mode.Movies, proposals.Rename, []proposals.Proposal{
		{Status: proposals.Applied, SourceName: "applied", SourcePath: appliedFile},
		{Status: proposals.Dismissed, SourceName: "dismissed", SourcePath: dismissedFile},
	})
	if err != nil {
		t.Fatalf("seeding proposals: %v", err)
	}

	srv := httptest.NewServer(deleteBatchMux(connStore, scStore, settingsStore, propStore))
	defer srv.Close()

	// A hand-crafted client naming both ids directly — no UI, no dropdown
	// gating, nothing between the request and the handler.
	out := postDeleteBatch(t, srv, []deleteBatchItem{{ID: saved[0].ID}, {ID: saved[1].ID}})
	if len(out.Results) != 2 {
		t.Fatalf("expected 2 results, got %+v", out.Results)
	}
	for i, r := range out.Results {
		if r.OK {
			t.Errorf("item %d must be refused server-side, got %+v", i, r)
		}
	}
	assertStillOnDisk(t, appliedFile)
	assertStillOnDisk(t, dismissedFile)
	assertRowPresent(t, propStore, saved[0].ID)
	assertRowPresent(t, propStore, saved[1].ID)
}

// --- 8. Workflow scoping -------------------------------------------------

// THE test for the property that keeps this from being a generic "delete any
// workflow's source file" primitive. A Dedup deletion routed here would remove
// a file and leave its library row intact; a Purge deletion would bypass
// Purge's own TrackedID/libStore bookkeeping.
func TestDeleteBatch_RejectsDedupAndPurgeProposals(t *testing.T) {
	dir := t.TempDir()
	dedupFile := filepath.Join(dir, "dedup.mkv")
	purgeFile := filepath.Join(dir, "purge.mkv")
	writeTempFile(t, dedupFile)
	writeTempFile(t, purgeFile)

	connStore, propStore, settingsStore, _, _, _, _, _, _, _, scStore := testStoresWithRegistry(t)
	ctx := context.Background()

	dedupSaved, err := propStore.ReplacePending(ctx, mode.Movies, proposals.Dedup, []proposals.Proposal{
		{Status: proposals.Pending, SourceName: "dedup", SourcePath: dedupFile},
	})
	if err != nil {
		t.Fatalf("seeding the Dedup proposal: %v", err)
	}
	purgeSaved, err := propStore.ReplacePending(ctx, mode.Movies, proposals.Purge, []proposals.Proposal{
		{Status: proposals.Pending, SourceName: "purge", SourcePath: purgeFile},
	})
	if err != nil {
		t.Fatalf("seeding the Purge proposal: %v", err)
	}

	srv := httptest.NewServer(deleteBatchMux(connStore, scStore, settingsStore, propStore))
	defer srv.Close()

	out := postDeleteBatch(t, srv, []deleteBatchItem{{ID: dedupSaved[0].ID}, {ID: purgeSaved[0].ID}})
	if len(out.Results) != 2 {
		t.Fatalf("expected 2 results, got %+v", out.Results)
	}
	for i, r := range out.Results {
		if r.OK {
			t.Errorf("a non-Rename proposal must be refused, item %d got %+v", i, r)
		}
		if !strings.Contains(r.Error, "not rename") {
			t.Errorf("item %d's error should name the workflow mismatch, got %q", i, r.Error)
		}
	}
	assertStillOnDisk(t, dedupFile)
	assertStillOnDisk(t, purgeFile)
	assertRowPresent(t, propStore, dedupSaved[0].ID)
	assertRowPresent(t, propStore, purgeSaved[0].ID)
}

// --- 9. Two items resolving to the same path -----------------------------

// DOCUMENTED DEVIATION from the plan's §5.2 point 4, which describes "two
// items in the same batch resolving to the same source_path (possible via the
// GetLiveBySourcePath fallback if ids rotated mid-batch)". That literal setup
// is UNCONSTRUCTIBLE: migration 0004's unique index on
// (mode, workflow, source_path) WHERE status IN ('pending','unmatched')
// forbids two live rows sharing a path within one (mode, workflow), and the
// fallback variant cannot yield the plan's expected {ok:true} either — once
// the first item destroys the only live row at that path, the fallback finds
// nothing and the second item honestly fails.
//
// So this test pins the closest CONSTRUCTIBLE analogue: two live Rename rows
// in different modes pointing at one file, each reached by its own id. What it
// genuinely proves is the property §5.2 point 4 exists to protect — the second
// os.Remove hits ENOENT, which this feature deliberately tolerates as success,
// so neither item reports a spurious failure and the file is deleted exactly
// once. It exercises the cross-mode gate/session/changesByMode paths as a
// bonus. What it does NOT exercise is the id-miss fallback; that is covered
// separately by TestDeleteBatch_IDMissResolvesBySourcePath.
func TestDeleteBatch_DuplicatePathInOneBatch(t *testing.T) {
	dir := t.TempDir()
	shared := filepath.Join(dir, "shared.mkv")
	writeTempFile(t, shared)

	connStore, propStore, settingsStore, _, _, _, _, _, _, _, scStore := testStoresWithRegistry(t)
	ctx := context.Background()

	moviesSaved, err := propStore.ReplacePending(ctx, mode.Movies, proposals.Rename, []proposals.Proposal{
		{Status: proposals.Pending, SourceName: "shared", SourcePath: shared},
	})
	if err != nil {
		t.Fatalf("seeding the Movies proposal: %v", err)
	}
	seriesSaved, err := propStore.ReplacePending(ctx, mode.Series, proposals.Rename, []proposals.Proposal{
		{Status: proposals.Pending, SourceName: "shared", SourcePath: shared},
	})
	if err != nil {
		t.Fatalf("seeding the Series proposal: %v", err)
	}

	srv := httptest.NewServer(deleteBatchMux(connStore, scStore, settingsStore, propStore))
	defer srv.Close()

	out := postDeleteBatch(t, srv, []deleteBatchItem{
		{ID: moviesSaved[0].ID, SourcePath: shared},
		{ID: seriesSaved[0].ID, SourcePath: shared},
	})
	if len(out.Results) != 2 {
		t.Fatalf("expected 2 results, got %+v", out.Results)
	}
	for i, r := range out.Results {
		if !r.OK {
			t.Errorf("both items must succeed (the second is ENOENT-tolerated), item %d got %+v", i, r)
		}
	}
	assertGone(t, shared)
	assertRowGone(t, propStore, moviesSaved[0].ID)
	assertRowGone(t, propStore, seriesSaved[0].ID)
}

// --- 10/11. Request-level rejections -------------------------------------

func TestDeleteBatch_EmptyItemsRejected(t *testing.T) {
	connStore, propStore, settingsStore, _, _, _, _, _, _, _, scStore := testStoresWithRegistry(t)
	srv := httptest.NewServer(deleteBatchMux(connStore, scStore, settingsStore, propStore))
	defer srv.Close()

	status, body := postDeleteBatchRaw(t, srv, []byte(`{"items":[]}`))
	if status != http.StatusBadRequest {
		t.Fatalf("expected 400 for an empty items array, got %d: %s", status, body)
	}
}

// The cap is a DELIBERATE divergence from Rename's uncapped apply-batch — an
// uncapped destructive request is not the same risk class as an uncapped
// rename. It rejects the whole request rather than skipping per item, and the
// error names the cap so the operator knows to split the selection.
func TestDeleteBatch_ItemCapEnforced(t *testing.T) {
	connStore, propStore, settingsStore, _, _, _, _, _, _, _, scStore := testStoresWithRegistry(t)
	srv := httptest.NewServer(deleteBatchMux(connStore, scStore, settingsStore, propStore))
	defer srv.Close()

	items := make([]deleteBatchItem, proposals.MaxProposalPageSize+1)
	for i := range items {
		items[i] = deleteBatchItem{ID: int64(i + 1)}
	}
	body, _ := json.Marshal(deleteBatchRequest{Items: items})
	status, raw := postDeleteBatchRaw(t, srv, body)
	if status != http.StatusBadRequest {
		t.Fatalf("expected 400 for %d items, got %d: %s", len(items), status, raw)
	}
	if !strings.Contains(string(raw), fmt.Sprintf("%d", proposals.MaxProposalPageSize)) {
		t.Errorf("the cap rejection must name the %d-item cap, got %q", proposals.MaxProposalPageSize, raw)
	}
}

// --- 12. The id-miss fallback --------------------------------------------

// A concurrent rescan can rotate proposal ids between the operator opening the
// confirm modal and confirming it, so the client also sends sourcePath.
//
// NOTE the two-item shape: resolveBatchProposal's fallback iterates the ACTIVE
// gate keys, and the gate is seeded from a successful Get of req.Items[0].ID.
// A single-item batch with a stale id therefore has an empty gate and no
// fallback to run — item 0 here is a live proposal that seeds it, exactly as a
// real same-screen batch would.
func TestDeleteBatch_IDMissResolvesBySourcePath(t *testing.T) {
	dir := t.TempDir()
	liveFile := filepath.Join(dir, "live.mkv")
	rotatedFile := filepath.Join(dir, "rotated.mkv")
	writeTempFile(t, liveFile)
	writeTempFile(t, rotatedFile)

	connStore, propStore, settingsStore, _, _, _, _, _, _, _, scStore := testStoresWithRegistry(t)
	ctx := context.Background()

	saved, err := propStore.ReplacePending(ctx, mode.Movies, proposals.Rename, []proposals.Proposal{
		{Status: proposals.Pending, SourceName: "live", SourcePath: liveFile},
		{Status: proposals.Pending, SourceName: "rotated", SourcePath: rotatedFile},
	})
	if err != nil {
		t.Fatalf("seeding proposals: %v", err)
	}
	staleID := saved[1].ID

	// Rotate the second row's id out from under the client: destroy it, then
	// re-scan the same two paths so a FRESH row is inserted for rotatedFile
	// (ReplacePending upserts the surviving row by source_path, so item 0 keeps
	// its id).
	if err := propStore.Delete(ctx, staleID); err != nil {
		t.Fatalf("destroying the row to rotate its id: %v", err)
	}
	rescanned, err := propStore.ReplacePending(ctx, mode.Movies, proposals.Rename, []proposals.Proposal{
		{Status: proposals.Pending, SourceName: "live", SourcePath: liveFile},
		{Status: proposals.Pending, SourceName: "rotated", SourcePath: rotatedFile},
	})
	if err != nil {
		t.Fatalf("rescanning: %v", err)
	}
	if rescanned[1].ID == staleID {
		t.Fatalf("expected the rotated row to get a NEW id, still %d", staleID)
	}

	srv := httptest.NewServer(deleteBatchMux(connStore, scStore, settingsStore, propStore))
	defer srv.Close()

	out := postDeleteBatch(t, srv, []deleteBatchItem{
		{ID: rescanned[0].ID, SourcePath: liveFile},
		{ID: staleID, SourcePath: rotatedFile}, // stale id, correct current path
	})
	if len(out.Results) != 2 {
		t.Fatalf("expected 2 results, got %+v", out.Results)
	}
	if !out.Results[1].OK {
		t.Fatalf("the stale id should have re-resolved by sourcePath, got %+v", out.Results[1])
	}
	assertGone(t, rotatedFile)
	assertRowGone(t, propStore, rescanned[1].ID)
}

// --- 13. The audit log ---------------------------------------------------

// installTestEventStore points organizeevents' process-wide default store at a
// test-scoped one and restores it afterwards. Tests using it must NOT call
// t.Parallel() — SetDefault mutates global state and a parallel sibling would
// race it. Forgetting the install fails loudly rather than silently: Log is a
// no-op with a nil default, so the assertions see zero events.
func installTestEventStore(t *testing.T) *organizeevents.Store {
	t.Helper()
	evStore := organizeevents.New(dbtest.New(t))
	organizeevents.SetDefault(evStore)
	t.Cleanup(func() { organizeevents.SetDefault(nil) })
	return evStore
}

// The only durable trace a delete leaves anywhere: the proposal row is
// destroyed and the file is gone.
//
// The Workflow/Mode assertions ARE this test. A version checking only Kind and
// the message string passes even with the audit-log bug fully present — i.e.
// even if the handler copied applyBatchHandler's post-loop
// propStore.Get(req.Items[0].ID), which returns ErrNotFound here because the
// row it looks up is exactly the row the delete just destroyed, leaving
// workflow empty. The List(ctx, "rename", 40) call is the same filtered query
// OrganizeChrome.tsx's activity log makes, so it is the closest thing to an
// end-to-end proof the event will actually render on the Rename screen.
func TestDeleteBatch_LogsDeleteBatchOrganizeEvent(t *testing.T) {
	dir := t.TempDir()
	paths := []string{filepath.Join(dir, "x.mkv"), filepath.Join(dir, "y.mkv")}
	for _, p := range paths {
		writeTempFile(t, p)
	}

	connStore, propStore, settingsStore, _, _, _, _, _, _, _, scStore := testStoresWithRegistry(t)
	ctx := context.Background()
	evStore := installTestEventStore(t)

	saved, err := propStore.ReplacePending(ctx, mode.Movies, proposals.Rename, []proposals.Proposal{
		{Status: proposals.Pending, SourceName: "x", SourcePath: paths[0]},
		{Status: proposals.Pending, SourceName: "y", SourcePath: paths[1]},
	})
	if err != nil {
		t.Fatalf("seeding proposals: %v", err)
	}

	srv := httptest.NewServer(deleteBatchMux(connStore, scStore, settingsStore, propStore))
	defer srv.Close()

	out := postDeleteBatch(t, srv, []deleteBatchItem{{ID: saved[0].ID}, {ID: saved[1].ID}})
	for i, r := range out.Results {
		if !r.OK {
			t.Fatalf("item %d failed, the log assertions would be testing the wrong thing: %+v", i, r)
		}
	}

	all, err := evStore.List(ctx, "", 40)
	if err != nil {
		t.Fatalf("listing all events: %v", err)
	}
	deletes := filterKind(all, organizeevents.KindDeleteBatch)
	if len(deletes) != 1 {
		t.Fatalf("expected exactly one delete_batch event, got %d: %+v", len(deletes), all)
	}
	ev := deletes[0]
	if ev.Kind != "delete_batch" {
		t.Errorf("kind = %q, want %q", ev.Kind, "delete_batch")
	}
	if ev.Message != "delete-batch 2/2 ok" {
		t.Errorf("message = %q, want %q", ev.Message, "delete-batch 2/2 ok")
	}
	// The two assertions that catch the audit-log bug.
	if ev.Workflow != string(proposals.Rename) {
		t.Fatalf("workflow = %q, want %q — an empty workflow matches NO screen's filter and the event never renders", ev.Workflow, proposals.Rename)
	}
	if ev.Mode == "" {
		t.Fatalf("mode is empty; the event must be attributed to the batch's mode")
	}

	// Retrieval through the REAL filtered query path OrganizeChrome.tsx uses.
	filtered, err := evStore.List(ctx, "rename", 40)
	if err != nil {
		t.Fatalf("listing rename events: %v", err)
	}
	if len(filterKind(filtered, organizeevents.KindDeleteBatch)) != 1 {
		t.Fatalf("the delete_batch event is not returned by List(ctx, \"rename\", 40) — it would never render on the Rename activity log: %+v", filtered)
	}
}

// Pins the "first item that RESOLVES, not literally req.Items[0]" half of the
// capture: item 0's id does not exist at all, so only item 1 can supply the
// workflow.
func TestDeleteBatch_LogsCorrectWorkflowWhenFirstItemFailsToResolve(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "second.mkv")
	writeTempFile(t, src)

	connStore, propStore, settingsStore, _, _, _, _, _, _, _, scStore := testStoresWithRegistry(t)
	ctx := context.Background()
	evStore := installTestEventStore(t)

	saved, err := propStore.ReplacePending(ctx, mode.Movies, proposals.Rename, []proposals.Proposal{
		{Status: proposals.Pending, SourceName: "second", SourcePath: src},
	})
	if err != nil {
		t.Fatalf("seeding the proposal: %v", err)
	}

	srv := httptest.NewServer(deleteBatchMux(connStore, scStore, settingsStore, propStore))
	defer srv.Close()

	const nonexistentID = int64(987654321)
	out := postDeleteBatch(t, srv, []deleteBatchItem{
		{ID: nonexistentID}, // no sourcePath — cannot resolve by any route
		{ID: saved[0].ID},
	})
	if len(out.Results) != 2 {
		t.Fatalf("expected 2 results, got %+v", out.Results)
	}
	if out.Results[0].OK {
		t.Fatalf("item 0 names an id that does not exist; it must fail: %+v", out.Results[0])
	}
	if !out.Results[1].OK {
		t.Fatalf("item 1 is a valid Rename proposal and must succeed: %+v", out.Results[1])
	}

	all, err := evStore.List(ctx, "", 40)
	if err != nil {
		t.Fatalf("listing events: %v", err)
	}
	deletes := filterKind(all, organizeevents.KindDeleteBatch)
	if len(deletes) != 1 {
		t.Fatalf("expected one delete_batch event, got %d: %+v", len(deletes), all)
	}
	if deletes[0].Workflow != string(proposals.Rename) {
		t.Fatalf("workflow = %q, want %q — the capture must use the first RESOLVED item, not req.Items[0]", deletes[0].Workflow, proposals.Rename)
	}
	if deletes[0].Mode != string(mode.Movies) {
		t.Errorf("mode = %q, want %q", deletes[0].Mode, mode.Movies)
	}
	if deletes[0].Message != "delete-batch 1/2 ok" {
		t.Errorf("message = %q, want %q", deletes[0].Message, "delete-batch 1/2 ok")
	}
}

func filterKind(evs []organizeevents.Event, kind string) []organizeevents.Event {
	out := []organizeevents.Event{}
	for _, e := range evs {
		if e.Kind == kind {
			out = append(out, e)
		}
	}
	return out
}

// --- 14. No webhook dispatch ---------------------------------------------

// Structural half: deleteBatchHandler's signature takes no *webhooks.Store at
// all, so no dispatch is reachable from it by construction. This assignment
// fails to compile the moment a webhook store is threaded in.
var _ = func(
	httpClient *http.Client,
	connStore *connections.Store,
	scStore *serviceconn.Store,
	settingsStore *settings.Store,
	propStore proposalDeleteStore,
) http.HandlerFunc {
	return deleteBatchHandler(httpClient, connStore, scStore, settingsStore, propStore)
}

// Behavioral half, in the TestAdultDescriptionMakesNoProwlarrCall house style:
// route through the REAL NewMux (which does wire whStore) with a live
// rename.applied subscription, and assert zero deliveries. The positive control
// first proves the recorder would have seen one — without it, "zero hits" is
// indistinguishable from a broken recorder.
func TestDeleteBatch_FiresNoWebhook(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "hooked.mkv")
	writeTempFile(t, src)

	var hits atomic.Int64
	recorder := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer recorder.Close()

	connStore, propStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, rowStore, releaseStore, rssFeedsStore, scStore := testStoresWithRegistry(t)
	ctx := context.Background()

	secretStore, err := secrets.New(make([]byte, 32))
	if err != nil {
		t.Fatalf("building secret store: %v", err)
	}
	whStore := webhooks.New(dbtest.New(t), secretStore)
	if _, err := whStore.Create(ctx, recorder.URL, "", []string{webhooks.EventRenameApplied}, true); err != nil {
		t.Fatalf("creating the webhook subscription: %v", err)
	}

	// Positive control: the recorder really does receive a rename.applied
	// dispatch through this exact store. Its measured latency sets the absence
	// window below — a fixed short sleep would let a dispatch that DID fire land
	// after the check and pass vacuously.
	controlStart := time.Now()
	whStore.Dispatch(webhooks.EventRenameApplied, map[string]any{"probe": true})
	waitForHits(t, &hits, 1)
	controlLatency := time.Since(controlStart)
	hits.Store(0)

	saved, err := propStore.ReplacePending(ctx, mode.Movies, proposals.Rename, []proposals.Proposal{
		{Status: proposals.Pending, SourceName: "hooked", SourcePath: src},
	})
	if err != nil {
		t.Fatalf("seeding the proposal: %v", err)
	}

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, scStore, propStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, rowStore, releaseStore, testFeedHealth(), rssFeedsStore, nil, whStore, nil, nil, nil, nil, nil, nil))
	defer srv.Close()

	out := postDeleteBatch(t, srv, []deleteBatchItem{{ID: saved[0].ID}})
	if len(out.Results) != 1 || !out.Results[0].OK {
		t.Fatalf("expected the delete to succeed, got %+v", out.Results)
	}
	assertGone(t, src)

	// Give any dispatch that WAS fired a window comfortably wider than the one
	// the control actually needed — a fixed short sleep would make "zero hits"
	// mean "the check ran first" under load rather than "nothing was sent".
	absenceWindow := 10 * controlLatency
	if absenceWindow < 2*time.Second {
		absenceWindow = 2 * time.Second
	}
	time.Sleep(absenceWindow)
	if n := hits.Load(); n != 0 {
		t.Fatalf("delete-batch must dispatch NO webhook (workflowEvent(Rename) means \"a rename was applied\"), recorder saw %d", n)
	}
}

func waitForHits(t *testing.T, hits *atomic.Int64, want int64) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if hits.Load() >= want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("positive control never fired: expected %d webhook delivery, got %d — the absence assertion below would be vacuous", want, hits.Load())
}

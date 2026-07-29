package imageproxy

import (
	"context"
	"database/sql"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// This file is the US-7 durable-store test suite. It reuses the shared
// harness already built for imageproxy_test.go's US-3 Fetch/FetchAndPersist
// tests (newTestDurable, durableRowCount, adultImageCacheDDL) rather than
// duplicating it -- those helpers build a real DurableStore backed by a
// temp-file SQLite DB mirroring migration 0046 exactly.

// TestDurableStore_PutGet_RoundTrip is the baseline: bytes and content-type
// survive a Put followed by a Get for the same key.
func TestDurableStore_PutGet_RoundTrip(t *testing.T) {
	store, _ := newTestDurable(t)
	ctx := context.Background()
	url := "https://cdn.example/round-trip.png"
	wantBody := []byte("round-trip-bytes")
	wantCT := "image/png"

	if err := store.Put(ctx, url, wantCT, wantBody); err != nil {
		t.Fatalf("Put: %v", err)
	}
	body, ct, found, err := store.Get(ctx, url)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !found {
		t.Fatal("Get found = false, want true")
	}
	if string(body) != string(wantBody) {
		t.Fatalf("Get body = %q, want %q", body, wantBody)
	}
	if ct != wantCT {
		t.Fatalf("Get content-type = %q, want %q", ct, wantCT)
	}
}

// TestDurableStore_KeyNormalization covers DurableStore's own key handling
// given two already-normalized strings: an identical normalized string used
// twice (as two different raw inputs would collapse to after validate's
// u.String()) must collide onto the SAME entry, and a sizing-query
// difference (?w=300 vs ?w=600) -- a semantically different image -- must
// produce a distinct entry.
func TestDurableStore_KeyNormalization(t *testing.T) {
	store, db := newTestDurable(t)
	ctx := context.Background()

	normalized := "https://cdn.example/img.jpg?w=300"
	if err := store.Put(ctx, normalized, "image/jpeg", []byte("v1")); err != nil {
		t.Fatalf("first Put: %v", err)
	}
	if err := store.Put(ctx, normalized, "image/jpeg", []byte("v2-updated")); err != nil {
		t.Fatalf("second Put (same normalized key): %v", err)
	}
	if n := durableRowCount(t, db); n != 1 {
		t.Fatalf("rows after two Puts of the identical normalized key = %d, want 1 (collide onto one entry)", n)
	}
	body, _, found, err := store.Get(ctx, normalized)
	if err != nil || !found {
		t.Fatalf("Get after collide = (found=%v, err=%v), want (true, nil)", found, err)
	}
	if string(body) != "v2-updated" {
		t.Fatalf("Get after collide returned %q, want the second Put's bytes (upsert/overwrite semantics)", body)
	}

	distinct := "https://cdn.example/img.jpg?w=600"
	if err := store.Put(ctx, distinct, "image/jpeg", []byte("v-600")); err != nil {
		t.Fatalf("Put distinct sizing-query key: %v", err)
	}
	if n := durableRowCount(t, db); n != 2 {
		t.Fatalf("rows after Put of a distinct sizing-query key = %d, want 2 (distinct entry)", n)
	}
}

// forceRenameFailure pre-creates the on-disk destination for url AS A
// DIRECTORY, so a subsequent store.Put(url, ...) fails at the final
// os.Rename step (EISDIR: newpath is an existing directory, oldpath is not)
// -- CreateTemp/write/sync/close have all already succeeded by that point,
// giving a clean, portable way to force a rename failure without touching
// production code or relying on filesystem-specific permission tricks.
func forceRenameFailure(t *testing.T, store *DurableStore, url string) (shard, file string) {
	t.Helper()
	shard, file = store.pathFor(shaOf(url))
	if err := os.MkdirAll(file, 0o755); err != nil {
		t.Fatalf("pre-creating %s as a directory: %v", file, err)
	}
	return shard, file
}

// TestDurableStore_Put_AtomicOnRenameFailure covers the atomic-write
// invariant: a failure between temp-write and rename must leave no indexed
// row and no partial file at the final path, and must self-clean the temp
// file (Put's own defer), never leaking one for Reconcile to find later.
func TestDurableStore_Put_AtomicOnRenameFailure(t *testing.T) {
	store, db := newTestDurable(t)
	ctx := context.Background()
	url := "https://cdn.example/atomic-fail.jpg"

	shard, file := forceRenameFailure(t, store, url)

	err := store.Put(ctx, url, "image/jpeg", []byte("should never be indexed"))
	if err == nil {
		t.Fatal("Put over a directory-occupied destination = nil error, want a rename failure")
	}

	if n := durableRowCount(t, db); n != 0 {
		t.Fatalf("index rows after a failed Put = %d, want 0 (a torn write is never indexed)", n)
	}
	if _, _, found, getErr := store.Get(ctx, url); getErr != nil || found {
		t.Fatalf("Get after a failed Put = (found=%v, err=%v), want (false, nil)", found, getErr)
	}

	// No partial file at the final path: the pre-created directory must be
	// untouched -- rename never moved anything there.
	info, statErr := os.Stat(file)
	if statErr != nil || !info.IsDir() {
		t.Fatalf("final path after a failed Put: stat err=%v isDir=%v, want an untouched directory", statErr, info != nil && info.IsDir())
	}

	// Self-clean: Put's defer must not leak the temp file anywhere under the
	// shard dir (or anywhere else under baseDir).
	var strays []string
	_ = filepath.WalkDir(shard, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if strings.Contains(d.Name(), tempSuffix) {
			strays = append(strays, path)
		}
		return nil
	})
	if len(strays) != 0 {
		t.Fatalf("stray temp files left behind after a failed Put: %v, want none (self-clean)", strays)
	}
}

// TestDurableStore_Put_EXDEVSafeTempLocation is the AC #12 white-box test.
//
// Put's own defer (os.Remove(tmp.Name())) self-cleans the temp file on every
// return path, success or failure -- so no file survives on disk after a
// completed Put() call to assert on directly (a real strength of the
// implementation, not a gap). Instead this recovers the exact path
// CreateTemp produced from the *os.LinkError os.Rename returns on failure
// (LinkError.Old == oldpath == tmp.Name()), which Put wraps with %w. That
// sidesteps the defer entirely and proves, filesystem-independently and
// without any st_dev comparison (rejected by the plan as vacuous in CI,
// where t.TempDir() and os.TempDir() usually share a filesystem), that
// CreateTemp targeted the shard dir baseDir/<sha[:2]>/ and never
// os.TempDir().
func TestDurableStore_Put_EXDEVSafeTempLocation(t *testing.T) {
	store, _ := newTestDurable(t)
	ctx := context.Background()
	url := "https://cdn.example/exdev.jpg"

	shard, _ := forceRenameFailure(t, store, url)

	err := store.Put(ctx, url, "image/jpeg", []byte("data"))
	if err == nil {
		t.Fatal("Put over a directory-occupied destination = nil error, want a rename failure")
	}

	var linkErr *os.LinkError
	if !errors.As(err, &linkErr) {
		t.Fatalf("Put error = %v, want it to wrap an *os.LinkError from the failed os.Rename", err)
	}

	// The primary, decisive assertion: CreateTemp's directory must be exactly
	// the shard dir. This alone already catches an os.CreateTemp("", ...)
	// regression (its dir would resolve to os.TempDir(), not shard).
	if got := filepath.Dir(linkErr.Old); got != shard {
		t.Fatalf("CreateTemp's directory = %q, want the shard dir %q (temp must be created inside baseDir/<sha[:2]>/, not os.TempDir())", got, shard)
	}
	// Belt-and-suspenders, stated as an exact-equality check against
	// os.TempDir() rather than strings.HasPrefix: t.TempDir() (this test's own
	// baseDir) is itself nested under os.TempDir() in most environments (e.g.
	// both under /tmp), so a prefix check would be vacuously true here even
	// for a correctly shard-local temp file -- exactly the shared-tmpfs
	// false-positive class the plan rejects st_dev for. Equality against the
	// OS temp root itself has no such nesting problem.
	if filepath.Clean(filepath.Dir(linkErr.Old)) == filepath.Clean(os.TempDir()) {
		t.Fatalf("CreateTemp's temp file %q was created directly in os.TempDir() %q -- the EXDEV-unsafe bug this test guards against", linkErr.Old, os.TempDir())
	}
	if !strings.Contains(filepath.Base(linkErr.Old), tempSuffix) {
		t.Fatalf("temp file name %q does not contain the expected tempSuffix %q", linkErr.Old, tempSuffix)
	}
}

// TestDurableStore_Get_SelfHealsOnMissingFile covers the read-side self-heal:
// an index row present with its backing file removed out-of-band must report
// a clean miss AND delete the stale row (so Has stops lying afterward).
func TestDurableStore_Get_SelfHealsOnMissingFile(t *testing.T) {
	store, db := newTestDurable(t)
	ctx := context.Background()
	url := "https://cdn.example/self-heal.jpg"

	if err := store.Put(ctx, url, "image/png", []byte("bytes")); err != nil {
		t.Fatalf("seeding Put: %v", err)
	}
	_, file := store.pathFor(shaOf(url))
	if err := os.Remove(file); err != nil {
		t.Fatalf("removing backing file out-of-band: %v", err)
	}

	body, ct, found, err := store.Get(ctx, url)
	if err != nil || found || body != nil || ct != "" {
		t.Fatalf("Get with a missing backing file = (body=%v ct=%q found=%v err=%v), want (nil, \"\", false, nil)", body, ct, found, err)
	}
	if n := durableRowCount(t, db); n != 0 {
		t.Fatalf("index rows after self-heal = %d, want 0 (stale row deleted)", n)
	}

	// A second Get confirms the row is really gone, not just skipped once.
	if _, _, found, err := store.Get(ctx, url); err != nil || found {
		t.Fatalf("second Get after self-heal = (found=%v, err=%v), want (false, nil)", found, err)
	}
}

// TestDurableStore_Reconcile is the AC #13 test: seed all three drift types
// (a stray *.tmp-* file, an index row whose file was deleted, and an orphan
// data file with no index row) and assert Reconcile removes all three,
// returning counts {1,1,1}.
func TestDurableStore_Reconcile(t *testing.T) {
	store, db := newTestDurable(t)
	ctx := context.Background()

	// (a) a stray *.tmp-* file (interrupted-write orphan).
	strayShard, _ := store.pathFor(strings.Repeat("a", 64))
	if err := os.MkdirAll(strayShard, 0o755); err != nil {
		t.Fatalf("mkdir stray shard: %v", err)
	}
	strayTemp := filepath.Join(strayShard, strings.Repeat("a", 64)+tempSuffix+"x")
	if err := os.WriteFile(strayTemp, []byte("partial"), 0o644); err != nil {
		t.Fatalf("seeding stray temp: %v", err)
	}

	// (b) an index row whose backing file has been deleted out-of-band.
	deletedURL := "https://cdn.example/deleted.jpg"
	if err := store.Put(ctx, deletedURL, "image/jpeg", []byte("gone")); err != nil {
		t.Fatalf("seeding row-with-deleted-file: %v", err)
	}
	_, deletedFile := store.pathFor(shaOf(deletedURL))
	if err := os.Remove(deletedFile); err != nil {
		t.Fatalf("deleting backing file: %v", err)
	}

	// (c) an orphan data file with no index row.
	orphanShard, orphanFile := store.pathFor(strings.Repeat("b", 64))
	if err := os.MkdirAll(orphanShard, 0o755); err != nil {
		t.Fatalf("mkdir orphan shard: %v", err)
	}
	if err := os.WriteFile(orphanFile, make([]byte, 50), 0o644); err != nil {
		t.Fatalf("seeding orphan file: %v", err)
	}

	strayTemps, orphanRows, orphanFiles, err := store.Reconcile(ctx)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if strayTemps != 1 || orphanRows != 1 || orphanFiles != 1 {
		t.Fatalf("Reconcile counts = {strayTemps:%d orphanRows:%d orphanFiles:%d}, want {1,1,1}", strayTemps, orphanRows, orphanFiles)
	}

	if _, statErr := os.Stat(strayTemp); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("stray temp file after Reconcile: stat err=%v, want os.ErrNotExist", statErr)
	}
	if _, statErr := os.Stat(orphanFile); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("orphan file after Reconcile: stat err=%v, want os.ErrNotExist", statErr)
	}
	if n := durableRowCount(t, db); n != 0 {
		t.Fatalf("index rows after Reconcile = %d, want 0 (the orphaned row was deleted)", n)
	}
}

// TestDurableStore_TotalBytes_WalkVsIndexSum is the second half of AC #13:
// TotalBytes is a directory walk, not an index SUM, so an orphan file (no
// index row) makes the two diverge -- and they must converge again once
// Reconcile removes the orphan.
func TestDurableStore_TotalBytes_WalkVsIndexSum(t *testing.T) {
	store, db := newTestDurable(t)
	ctx := context.Background()

	if err := store.Put(ctx, "https://cdn.example/tb-a.jpg", "image/jpeg", make([]byte, 100)); err != nil {
		t.Fatalf("seeding a: %v", err)
	}
	if err := store.Put(ctx, "https://cdn.example/tb-b.jpg", "image/jpeg", make([]byte, 50)); err != nil {
		t.Fatalf("seeding b: %v", err)
	}

	indexSum := func() int64 {
		t.Helper()
		var sum sql.NullInt64
		if err := db.QueryRow(`SELECT SUM(byte_size) FROM adult_image_cache`).Scan(&sum); err != nil {
			t.Fatalf("summing index byte_size: %v", err)
		}
		return sum.Int64
	}

	if got, err := store.TotalBytes(ctx); err != nil || got != indexSum() {
		t.Fatalf("TotalBytes before orphan = %d (err=%v), want %d (walk must match index sum with no drift)", got, err, indexSum())
	}

	// An orphan data file (no index row) inflates the walk but is invisible to
	// a naive index SUM -- exactly the drift the disk cap must never be fooled by.
	orphanShard, orphanFile := store.pathFor(strings.Repeat("c", 64))
	if err := os.MkdirAll(orphanShard, 0o755); err != nil {
		t.Fatalf("mkdir orphan shard: %v", err)
	}
	if err := os.WriteFile(orphanFile, make([]byte, 1000), 0o644); err != nil {
		t.Fatalf("seeding orphan file: %v", err)
	}

	walkTotal, err := store.TotalBytes(ctx)
	if err != nil {
		t.Fatalf("TotalBytes with orphan: %v", err)
	}
	if walkTotal == indexSum() {
		t.Fatalf("walk-total (%d) == index-SUM (%d) with an orphan present, want them to diverge", walkTotal, indexSum())
	}
	if want := int64(100 + 50 + 1000); walkTotal != want {
		t.Fatalf("walk-total with orphan = %d, want %d (100+50+1000)", walkTotal, want)
	}

	if _, _, _, err := store.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	walkTotal, err = store.TotalBytes(ctx)
	if err != nil {
		t.Fatalf("TotalBytes after Reconcile: %v", err)
	}
	if walkTotal != indexSum() {
		t.Fatalf("walk-total (%d) != index-SUM (%d) after Reconcile removed the orphan, want equal", walkTotal, indexSum())
	}
}

// TestDurableStore_PruneToCap_EvictsOldestFirst covers eviction order: over
// cap, PruneToCap must evict the oldest fetched_at entries first, keeping
// the newest.
func TestDurableStore_PruneToCap_EvictsOldestFirst(t *testing.T) {
	store, db := newTestDurable(t)
	ctx := context.Background()

	urls := []string{
		"https://cdn.example/prune-oldest.jpg",
		"https://cdn.example/prune-middle.jpg",
		"https://cdn.example/prune-newest.jpg",
	}
	body := make([]byte, 100)
	for _, u := range urls {
		if err := store.Put(ctx, u, "image/jpeg", body); err != nil {
			t.Fatalf("seeding %s: %v", u, err)
		}
		time.Sleep(2 * time.Millisecond) // distinct RFC3339Nano fetched_at per entry
	}

	// 300 bytes total; cap=150 must evict the two oldest, keeping only the newest.
	if err := store.PruneToCap(ctx, 150); err != nil {
		t.Fatalf("PruneToCap: %v", err)
	}

	if n := durableRowCount(t, db); n != 1 {
		t.Fatalf("index rows after PruneToCap = %d, want 1 (only the newest survives)", n)
	}
	if has, err := store.Has(ctx, urls[2]); err != nil || !has {
		t.Fatalf("Has(newest) after PruneToCap = (%v, %v), want (true, nil)", has, err)
	}
	for _, u := range urls[:2] {
		if has, err := store.Has(ctx, u); err != nil || has {
			t.Fatalf("Has(%s) after PruneToCap = (%v, %v), want (false, nil) -- oldest entries must be evicted first", u, has, err)
		}
	}
}

// TestDurableStore_PruneToCap_TerminatesWhenIndexExhausted covers the
// termination guard: once the index has no more candidates to evict,
// PruneToCap must return promptly rather than loop forever, even though
// walk-based usage is still nominally over cap (the remainder is an
// unindexed orphan that only Reconcile can clear).
func TestDurableStore_PruneToCap_TerminatesWhenIndexExhausted(t *testing.T) {
	store, db := newTestDurable(t)
	ctx := context.Background()

	if err := store.Put(ctx, "https://cdn.example/prune-indexed.jpg", "image/jpeg", make([]byte, 50)); err != nil {
		t.Fatalf("seeding indexed entry: %v", err)
	}
	orphanShard, orphanFile := store.pathFor(strings.Repeat("d", 64))
	if err := os.MkdirAll(orphanShard, 0o755); err != nil {
		t.Fatalf("mkdir orphan shard: %v", err)
	}
	if err := os.WriteFile(orphanFile, make([]byte, 500), 0o644); err != nil {
		t.Fatalf("seeding orphan file: %v", err)
	}

	// cap is below even the orphan alone: once the one indexed row is evicted,
	// nominal usage (the orphan) is still over cap, but PruneToCap must return
	// promptly rather than spin re-checking usage with no rows left to evict.
	done := make(chan error, 1)
	go func() { done <- store.PruneToCap(ctx, 10) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("PruneToCap: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("PruneToCap did not return within 5s -- termination guard failed to stop with the index exhausted")
	}

	if n := durableRowCount(t, db); n != 0 {
		t.Fatalf("index rows after PruneToCap = %d, want 0 (the only indexed entry was evicted)", n)
	}
	total, err := store.TotalBytes(ctx)
	if err != nil {
		t.Fatalf("TotalBytes: %v", err)
	}
	if total != 500 {
		t.Fatalf("TotalBytes after PruneToCap = %d, want 500 (only the untouchable orphan remains, still nominally over cap)", total)
	}
}

// TestDurableStore_Has_IsIndexOnly proves Has never opens/stats the file: with
// the backing file removed out-of-band and no Reconcile having run yet, Has
// still reports true immediately after, because it only ever consults the
// index.
func TestDurableStore_Has_IsIndexOnly(t *testing.T) {
	store, _ := newTestDurable(t)
	ctx := context.Background()
	url := "https://cdn.example/has-index-only.jpg"

	if err := store.Put(ctx, url, "image/jpeg", []byte("bytes")); err != nil {
		t.Fatalf("seeding Put: %v", err)
	}
	_, file := store.pathFor(shaOf(url))
	if err := os.Remove(file); err != nil {
		t.Fatalf("removing backing file out-of-band: %v", err)
	}

	has, err := store.Has(ctx, url)
	if err != nil || !has {
		t.Fatalf("Has after out-of-band file deletion = (%v, %v), want (true, nil) -- Has is index-only", has, err)
	}
}

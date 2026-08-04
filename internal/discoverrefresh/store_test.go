package discoverrefresh

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/labbersanon/sakms/internal/db"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	sqlDB, err := db.Open(filepath.Join(t.TempDir(), "sakms.db"))
	if err != nil {
		t.Fatalf("opening db: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	return NewStore(sqlDB)
}

// items builds n distinguishable item bodies, so a test can tell which slice of
// the flat list it got back rather than only how many elements.
func items(n int) []json.RawMessage {
	out := make([]json.RawMessage, 0, n)
	for i := range n {
		out = append(out, json.RawMessage(fmt.Sprintf(`{"id":%d}`, i)))
	}
	return out
}

// T-2 — Store round-trip.
func TestStore_PutThenGet_RoundTrips(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	payload := items(37)

	if err := s.Put(ctx, "tmdb", "movies:trending", payload, 20, 3, false); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, err := s.Get(ctx, "tmdb", "movies:trending")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	if got.Source != "tmdb" || got.CacheKey != "movies:trending" {
		t.Errorf("identity = %q/%q, want tmdb/movies:trending", got.Source, got.CacheKey)
	}
	if len(got.Payload) != len(payload) {
		t.Fatalf("payload length = %d, want %d", len(got.Payload), len(payload))
	}
	for i := range payload {
		if string(got.Payload[i]) != string(payload[i]) {
			t.Errorf("payload[%d] = %s, want %s", i, got.Payload[i], payload[i])
		}
	}
	// item_count is derived from the blob by Put, never passed in, so the two
	// cannot disagree.
	if got.ItemCount != 37 {
		t.Errorf("ItemCount = %d, want 37", got.ItemCount)
	}
	if got.PageSize != 20 {
		t.Errorf("PageSize = %d, want 20", got.PageSize)
	}
	if got.RawPages != 3 {
		t.Errorf("RawPages = %d, want 3", got.RawPages)
	}
	if got.Exhausted {
		t.Error("Exhausted = true, want false")
	}
	if got.RefreshedAt == "" {
		t.Error("RefreshedAt should be stamped by a successful Put")
	}
	if got.AttemptedAt == "" {
		t.Error("AttemptedAt should be stamped by a successful Put")
	}
	if got.LastError != "" {
		t.Errorf("LastError = %q, want empty after a successful Put", got.LastError)
	}

	// The composite primary key makes a second Put an UPDATE, not a new row.
	if err := s.Put(ctx, "tmdb", "movies:trending", items(5), 20, 1, true); err != nil {
		t.Fatalf("second Put: %v", err)
	}
	got, err = s.Get(ctx, "tmdb", "movies:trending")
	if err != nil {
		t.Fatalf("Get after upsert: %v", err)
	}
	if len(got.Payload) != 5 || got.ItemCount != 5 || !got.Exhausted {
		t.Errorf("upsert did not replace the row: len=%d count=%d exhausted=%v",
			len(got.Payload), got.ItemCount, got.Exhausted)
	}
}

func TestStore_Get_MissingRowIsErrNotFound(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.Get(context.Background(), "tmdb", "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get on an empty table = %v, want ErrNotFound", err)
	}
}

// T-3 — MarkFailure NEVER creates a row.
//
// This is the test that guarantees cold-miss fall-through: if MarkFailure were
// an upsert, the first failed refresh of a never-populated key would create an
// empty row, and the read path would then serve a phantom empty cache entry
// forever instead of falling through to the live handler that still works.
func TestStore_MarkFailure_NeverCreatesARow(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if err := s.MarkFailure(ctx, "tmdb", "movies:trending", errors.New("tmdb exploded")); err != nil {
		t.Fatalf("MarkFailure on an empty table should be a silent no-op, got %v", err)
	}
	if _, err := s.Get(ctx, "tmdb", "movies:trending"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after MarkFailure on a cold key, Get = %v, want ErrNotFound (an upsert here "+
			"would create the phantom empty row that breaks cold-miss fall-through)", err)
	}

	// Repeating it cannot accumulate a row either, and neither can a nil cause.
	if err := s.MarkFailure(ctx, "tmdb", "movies:trending", nil); err != nil {
		t.Fatalf("MarkFailure with a nil cause: %v", err)
	}
	if _, err := s.Get(ctx, "tmdb", "movies:trending"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after a second MarkFailure, Get = %v, want ErrNotFound", err)
	}

	// Nor for any other source.
	for _, source := range []string{"slider", "trakt", "stashbox"} {
		if err := s.MarkFailure(ctx, source, "", errors.New("boom")); err != nil {
			t.Fatalf("MarkFailure(%q): %v", source, err)
		}
		if _, err := s.Get(ctx, source, ""); !errors.Is(err, ErrNotFound) {
			t.Errorf("after MarkFailure(%q), Get = %v, want ErrNotFound", source, err)
		}
	}
}

// T-4 — MarkFailure preserves the payload (spec AC4, stale-but-available).
func TestStore_MarkFailure_PreservesPayload(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	payload := items(24)

	if err := s.Put(ctx, "stashbox", "fansdb:trending", payload, 20, 2, false); err != nil {
		t.Fatalf("Put: %v", err)
	}
	before, err := s.Get(ctx, "stashbox", "fansdb:trending")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	if err := s.MarkFailure(ctx, "stashbox", "fansdb:trending", errors.New("upstream 500")); err != nil {
		t.Fatalf("MarkFailure: %v", err)
	}
	after, err := s.Get(ctx, "stashbox", "fansdb:trending")
	if err != nil {
		t.Fatalf("Get after MarkFailure: %v", err)
	}

	if len(after.Payload) != len(payload) {
		t.Fatalf("payload length = %d after a failed refresh, want %d — a failure must never "+
			"blank a row", len(after.Payload), len(payload))
	}
	for i := range payload {
		if string(after.Payload[i]) != string(payload[i]) {
			t.Errorf("payload[%d] = %s, want %s", i, after.Payload[i], payload[i])
		}
	}
	if after.ItemCount != before.ItemCount {
		t.Errorf("ItemCount = %d, want %d unchanged", after.ItemCount, before.ItemCount)
	}
	// refreshed_at is the "last SUCCESS" stamp and must not move on a failure.
	if after.RefreshedAt != before.RefreshedAt {
		t.Errorf("RefreshedAt = %q, want %q unchanged — it moves only on success",
			after.RefreshedAt, before.RefreshedAt)
	}
	if after.PageSize != before.PageSize || after.RawPages != before.RawPages ||
		after.Exhausted != before.Exhausted {
		t.Errorf("window metadata changed on failure: page_size %d→%d raw_pages %d→%d exhausted %v→%v",
			before.PageSize, after.PageSize, before.RawPages, after.RawPages,
			before.Exhausted, after.Exhausted)
	}
	if after.LastError != "upstream 500" {
		t.Errorf("LastError = %q, want %q", after.LastError, "upstream 500")
	}
	if after.AttemptedAt == "" {
		t.Error("AttemptedAt should be stamped by MarkFailure")
	}

	// A later success clears the error and moves refreshed_at again.
	if err := s.Put(ctx, "stashbox", "fansdb:trending", items(20), 20, 1, true); err != nil {
		t.Fatalf("recovery Put: %v", err)
	}
	recovered, err := s.Get(ctx, "stashbox", "fansdb:trending")
	if err != nil {
		t.Fatalf("Get after recovery: %v", err)
	}
	if recovered.LastError != "" {
		t.Errorf("LastError = %q, want cleared by a successful Put", recovered.LastError)
	}
}

// T-5 — Slice boundaries, including the exact case the original single-clamped
// formula panicked on (37 items, perPage 20, page 3 → payload[40:37]).
func TestEntry_Slice_Boundaries(t *testing.T) {
	// cachedPages = ceil(37/20) = 2.
	base := func(exhausted bool) *Entry {
		return &Entry{Payload: items(37), ItemCount: 37, PageSize: 20, RawPages: 3, Exhausted: exhausted}
	}

	t.Run("page 1 serves a full page", func(t *testing.T) {
		got, hit, live := base(false).Slice(1)
		if !hit || live != 0 {
			t.Fatalf("Slice(1) = hit %v live %d, want true/0", hit, live)
		}
		if len(got) != 20 {
			t.Fatalf("Slice(1) returned %d items, want 20", len(got))
		}
		if string(got[0]) != `{"id":0}` || string(got[19]) != `{"id":19}` {
			t.Errorf("Slice(1) window = %s…%s, want items 0…19", got[0], got[19])
		}
	})

	t.Run("page 2 serves the short tail, still a hit", func(t *testing.T) {
		got, hit, live := base(false).Slice(2)
		if !hit || live != 0 {
			t.Fatalf("Slice(2) = hit %v live %d, want true/0", hit, live)
		}
		if len(got) != 17 {
			t.Fatalf("Slice(2) returned %d items, want 17", len(got))
		}
		if string(got[0]) != `{"id":20}` || string(got[16]) != `{"id":36}` {
			t.Errorf("Slice(2) window = %s…%s, want items 20…36", got[0], got[16])
		}
	})

	t.Run("past the window, exhausted serves an empty hit", func(t *testing.T) {
		got, hit, live := base(true).Slice(3)
		if !hit {
			t.Fatal("Slice(3) on an exhausted row must be a HIT — the upstream genuinely has " +
				"no more, so serving [] is what stops a pointless live call")
		}
		if len(got) != 0 {
			t.Errorf("Slice(3) returned %d items, want 0", len(got))
		}
		if live != 0 {
			t.Errorf("Slice(3) live page = %d, want 0 on a hit", live)
		}
	})

	t.Run("past the window, not exhausted falls through at the rewritten page", func(t *testing.T) {
		got, hit, live := base(false).Slice(3)
		if hit {
			t.Fatal("Slice(3) on a non-exhausted row must be a MISS")
		}
		if got != nil {
			t.Errorf("Slice(3) items = %v, want nil on a miss", got)
		}
		// raw_pages(3) + (page 3 - cachedPages 2) = 4.
		if live != 4 {
			t.Errorf("Slice(3) live page = %d, want 4 (raw_pages + page - cachedPages)", live)
		}
	})

	t.Run("no panic for any page, including out-of-range ones", func(t *testing.T) {
		// The regression net for the single-clamped formula. Pages 0 and -1 are
		// included because only some of the four calling handlers normalize the
		// requested page before calling.
		for _, exhausted := range []bool{false, true} {
			for page := -1; page <= 10; page++ {
				func() {
					defer func() {
						if r := recover(); r != nil {
							t.Fatalf("Slice(%d) with exhausted=%v panicked: %v", page, exhausted, r)
						}
					}()
					base(exhausted).Slice(page)
				}()
			}
		}
	})

	t.Run("a non-positive page is a no-opinion miss", func(t *testing.T) {
		for _, page := range []int{0, -1} {
			got, hit, live := base(false).Slice(page)
			if hit || got != nil || live != 0 {
				t.Errorf("Slice(%d) = (%v, %v, %d), want (nil, false, 0)", page, got, hit, live)
			}
		}
	})

	t.Run("a tampered page_size of 0 is a miss, not a divide by zero", func(t *testing.T) {
		e := &Entry{Payload: items(10), ItemCount: 10, PageSize: 0, RawPages: 1}
		got, hit, live := e.Slice(1)
		if hit || got != nil || live != 0 {
			t.Errorf("Slice on page_size=0 = (%v, %v, %d), want (nil, false, 0)", got, hit, live)
		}
	})

	t.Run("an empty exhausted row serves an empty hit at page 1", func(t *testing.T) {
		// cachedPages = 0, so even page 1 is past the window. This is the branch
		// that gives an empty Trakt watchlist or stash-box row zero external
		// calls instead of a live fetch on every load.
		e := &Entry{Payload: []json.RawMessage{}, PageSize: 1, RawPages: 1, Exhausted: true}
		got, hit, live := e.Slice(1)
		if !hit || len(got) != 0 || live != 0 {
			t.Errorf("Slice(1) on an empty exhausted row = (%d items, %v, %d), want (0, true, 0)",
				len(got), hit, live)
		}
	})

	t.Run("an unpaginated row serves its whole list on page 1", func(t *testing.T) {
		// refreshTrakt stores pageSize = max(len(payload), 1), so cachedPages is
		// 1 and page 1 is the entire watchlist however long it is. A fixed
		// pageSize of 20 would truncate a 34-item watchlist to 20 forever,
		// because the Trakt row never requests page 2.
		e := &Entry{Payload: items(34), ItemCount: 34, PageSize: 34, RawPages: 1, Exhausted: true}
		got, hit, _ := e.Slice(1)
		if !hit || len(got) != 34 {
			t.Errorf("Slice(1) on an unpaginated row = (%d items, %v), want (34, true)", len(got), hit)
		}
	})
}

// T-5c — exiting the cached window maps to the right UPSTREAM page.
//
// With filtering enabled the flat list over-delivers, so raw_pages and
// cachedPages diverge: 70 survivors at perPage 20 is 4 cached pages built from
// 5 raw upstream pages. A request for page 5 must ask the live path for
// upstream page 6 — asking for 5 would re-fetch a page whose survivors are
// already inside cached pages 1-4.
func TestEntry_Slice_WindowExitMapsToNextUpstreamPage(t *testing.T) {
	e := &Entry{Payload: items(70), ItemCount: 70, PageSize: 20, RawPages: 5, Exhausted: false}

	// The window is the row's OWN width (4), which deliberately EXCEEDS
	// carouselCachedPages (3) — nothing here may gate on the depth constant.
	if cachedPages := (len(e.Payload) + e.PageSize - 1) / e.PageSize; cachedPages != 4 {
		t.Fatalf("test premise: cachedPages = %d, want 4", cachedPages)
	}
	if carouselCachedPages >= 4 {
		t.Fatalf("test premise: carouselCachedPages = %d, want a value the window can exceed",
			carouselCachedPages)
	}

	// Page 4 is inside the window even though it is past K.
	page4, hit, live := e.Slice(4)
	if !hit || live != 0 {
		t.Fatalf("Slice(4) = hit %v live %d, want true/0 — the read path must not gate on K",
			hit, live)
	}
	if len(page4) != 10 {
		t.Errorf("Slice(4) returned %d items, want 10", len(page4))
	}

	// Page 5 exits the window and rewrites the upstream page: 5 + (5 - 4) = 6.
	got, hit, live := e.Slice(5)
	if hit || got != nil {
		t.Fatalf("Slice(5) = (%v, %v), want a miss", got, hit)
	}
	if live != 6 {
		t.Errorf("Slice(5) live page = %d, want 6 (raw_pages 5 + 1), not the requested 5", live)
	}

	// Every accumulated item is reachable exactly once across the window — no
	// gaps, no duplicates — which is what makes "the live path continues at
	// raw_pages + 1" exact rather than approximate.
	seen := map[string]int{}
	for page := 1; page <= 4; page++ {
		batch, hit, _ := e.Slice(page)
		if !hit {
			t.Fatalf("Slice(%d) should be inside the cached window", page)
		}
		for _, it := range batch {
			seen[string(it)]++
		}
	}
	if len(seen) != 70 {
		t.Errorf("the cached window covered %d distinct items, want all 70", len(seen))
	}
	for body, n := range seen {
		if n != 1 {
			t.Errorf("item %s served %d times across the window, want exactly once", body, n)
		}
	}

	// A source that filters nothing has raw_pages == cachedPages, so the
	// rewrite reduces to the requested page — today's behaviour exactly.
	unfiltered := &Entry{Payload: items(60), ItemCount: 60, PageSize: 20, RawPages: 3}
	if _, _, live := unfiltered.Slice(4); live != 4 {
		t.Errorf("unfiltered Slice(4) live page = %d, want 4 (unchanged)", live)
	}
}

func TestStore_DeleteMethods(t *testing.T) {
	ctx := context.Background()

	seed := func(t *testing.T, s *Store) {
		t.Helper()
		rows := []struct{ source, key string }{
			{"tmdb", "movies:trending"},
			{"tmdb", "series:popular"},
			{"slider", "1"},
			{"slider", "2"},
			{"slider", "3"},
			{"trakt", ""},
			{"stashbox", "fansdb:trending"},
		}
		for _, r := range rows {
			if err := s.Put(ctx, r.source, r.key, items(2), 20, 1, true); err != nil {
				t.Fatalf("seeding %s/%s: %v", r.source, r.key, err)
			}
		}
	}
	count := func(t *testing.T, s *Store, source string) int {
		t.Helper()
		var n int
		q := `SELECT COUNT(*) FROM discover_row_cache`
		var err error
		if source == "" {
			err = s.db.QueryRowContext(ctx, q).Scan(&n)
		} else {
			err = s.db.QueryRowContext(ctx, q+` WHERE source = ?`, source).Scan(&n)
		}
		if err != nil {
			t.Fatalf("counting rows: %v", err)
		}
		return n
	}

	t.Run("Delete is idempotent and removes exactly one row", func(t *testing.T) {
		s := newTestStore(t)
		seed(t, s)
		if err := s.Delete(ctx, "slider", "2"); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		if _, err := s.Get(ctx, "slider", "2"); !errors.Is(err, ErrNotFound) {
			t.Errorf("after Delete, Get = %v, want ErrNotFound", err)
		}
		if got := count(t, s, "slider"); got != 2 {
			t.Errorf("slider rows = %d, want 2", got)
		}
		if err := s.Delete(ctx, "slider", "2"); err != nil {
			t.Errorf("deleting an absent row should be a no-op, got %v", err)
		}
	})

	t.Run("DeleteBySource clears one source only", func(t *testing.T) {
		s := newTestStore(t)
		seed(t, s)
		if err := s.DeleteBySource(ctx, "tmdb"); err != nil {
			t.Fatalf("DeleteBySource: %v", err)
		}
		if got := count(t, s, "tmdb"); got != 0 {
			t.Errorf("tmdb rows = %d, want 0", got)
		}
		if got := count(t, s, "slider"); got != 3 {
			t.Errorf("slider rows = %d, want 3 — DeleteBySource must not touch other sources", got)
		}
	})

	t.Run("DeleteAll purges the whole table", func(t *testing.T) {
		s := newTestStore(t)
		seed(t, s)
		if err := s.DeleteAll(ctx); err != nil {
			t.Fatalf("DeleteAll: %v", err)
		}
		if got := count(t, s, ""); got != 0 {
			t.Errorf("rows after DeleteAll = %d, want 0 — the interval-off purge must restore "+
				"live behaviour for every source", got)
		}
	})

	t.Run("DeleteOrphanSliders keeps only the configured ids", func(t *testing.T) {
		s := newTestStore(t)
		seed(t, s)
		if err := s.DeleteOrphanSliders(ctx, []int{1, 3}); err != nil {
			t.Fatalf("DeleteOrphanSliders: %v", err)
		}
		for _, id := range []string{"1", "3"} {
			if _, err := s.Get(ctx, "slider", id); err != nil {
				t.Errorf("slider %s should have survived the sweep: %v", id, err)
			}
		}
		if _, err := s.Get(ctx, "slider", "2"); !errors.Is(err, ErrNotFound) {
			t.Errorf("orphan slider 2 = %v, want swept", err)
		}
		if got := count(t, s, "tmdb"); got != 2 {
			t.Errorf("tmdb rows = %d, want 2 — the sweep is slider-only", got)
		}
	})

	t.Run("DeleteOrphanSliders with no configured ids sweeps them all", func(t *testing.T) {
		// Every slider deleted or disabled. "NOT IN ()" is a SQLite syntax
		// error, so this is the case the obvious single-query shape fails on —
		// and it is exactly when there is most to clean up.
		s := newTestStore(t)
		seed(t, s)
		if err := s.DeleteOrphanSliders(ctx, nil); err != nil {
			t.Fatalf("DeleteOrphanSliders(nil): %v", err)
		}
		if got := count(t, s, "slider"); got != 0 {
			t.Errorf("slider rows = %d, want 0", got)
		}
		if got := count(t, s, ""); got != 4 {
			t.Errorf("total rows = %d, want the 4 non-slider rows untouched", got)
		}
	})
}

package recheck

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/labbersanon/sakms/internal/db"
	"github.com/labbersanon/sakms/internal/mode"
)

// newTestWatchStore builds a WatchStore against a real, freshly migrated
// SQLite file — exercising the actual SQL, not a mock, matching every other
// store test in this repo (see internal/library's newTestStore).
func newTestWatchStore(t *testing.T) *WatchStore {
	t.Helper()
	sqlDB, err := db.Open(filepath.Join(t.TempDir(), "sakms.db"))
	if err != nil {
		t.Fatalf("opening db: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	return NewWatchStore(sqlDB)
}

func TestAdd_IsIdempotentPerIdentity(t *testing.T) {
	s := newTestWatchStore(t)
	ctx := context.Background()

	cases := []struct {
		name string
		w    Watch
	}{
		{"movie", Watch{Mode: mode.Movies, TMDBID: 550}},
		{"series episode", Watch{Mode: mode.Series, TMDBID: 1399, Season: 1, Episode: 2}},
		{"adult", Watch{Mode: mode.Adult, Studio: "Studio X", Title: "Some Scene"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			first, err := s.Add(ctx, c.w)
			if err != nil {
				t.Fatalf("first add: %v", err)
			}
			if first.ID == 0 || first.AddedAt == "" {
				t.Fatalf("expected id/added_at populated, got %+v", first)
			}
			second, err := s.Add(ctx, c.w)
			if err != nil {
				t.Fatalf("second add: %v", err)
			}
			if second.ID != first.ID {
				t.Errorf("expected re-adding the same identity to return the same row (id %d), got %d", first.ID, second.ID)
			}
		})
	}

	all, err := s.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) != len(cases) {
		t.Fatalf("expected %d distinct entries after idempotent re-adds, got %d", len(cases), len(all))
	}
}

func TestListDue_NeverCheckedAlwaysDue_RecentNotDue(t *testing.T) {
	s := newTestWatchStore(t)
	ctx := context.Background()

	fresh, err := s.Add(ctx, Watch{Mode: mode.Adult, Studio: "A", Title: "Never Checked"})
	if err != nil {
		t.Fatalf("add fresh: %v", err)
	}
	checked, err := s.Add(ctx, Watch{Mode: mode.Adult, Studio: "B", Title: "Just Checked"})
	if err != nil {
		t.Fatalf("add checked: %v", err)
	}

	now := time.Now().UTC()
	// Mark the second entry as checked right now.
	if err := s.UpdateResult(ctx, checked.ID, true, now.Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("update result: %v", err)
	}

	// Due = checked before (now - 1h). The never-checked entry is always due;
	// the one checked "now" is not.
	due, err := s.ListDue(ctx, now.Add(-time.Hour))
	if err != nil {
		t.Fatalf("list due: %v", err)
	}
	if len(due) != 1 {
		t.Fatalf("expected exactly 1 due entry, got %d", len(due))
	}
	if due[0].ID != fresh.ID {
		t.Errorf("expected the never-checked entry (id %d) to be due, got id %d", fresh.ID, due[0].ID)
	}

	// A cutoff in the future makes even the just-checked entry due again.
	dueLater, err := s.ListDue(ctx, now.Add(time.Hour))
	if err != nil {
		t.Fatalf("list due later: %v", err)
	}
	if len(dueLater) != 2 {
		t.Fatalf("expected both entries due against a future cutoff, got %d", len(dueLater))
	}
}

func TestUpdateResult_FlipsFlagAndStampsCheckedAt(t *testing.T) {
	s := newTestWatchStore(t)
	ctx := context.Background()

	w, err := s.Add(ctx, Watch{Mode: mode.Movies, TMDBID: 27205})
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if w.LastAvailable || w.LastCheckedAt != "" {
		t.Fatalf("expected a fresh entry to be unchecked and unavailable, got %+v", w)
	}

	checkedAt := time.Now().UTC().Format(time.RFC3339Nano)
	if err := s.UpdateResult(ctx, w.ID, true, checkedAt); err != nil {
		t.Fatalf("update result: %v", err)
	}

	all, err := s.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(all))
	}
	if !all[0].LastAvailable {
		t.Errorf("expected last_available to flip to true")
	}
	if all[0].LastCheckedAt != checkedAt {
		t.Errorf("expected last_checked_at %q, got %q", checkedAt, all[0].LastCheckedAt)
	}

	// Updating a nonexistent id is a no-op, not an error.
	if err := s.UpdateResult(ctx, 99999, false, checkedAt); err != nil {
		t.Errorf("updating a missing id should be a no-op, got %v", err)
	}
}

func TestRemove_DeletesAndIsIdempotent(t *testing.T) {
	s := newTestWatchStore(t)
	ctx := context.Background()

	w, err := s.Add(ctx, Watch{Mode: mode.Adult, Studio: "A", Title: "Doomed"})
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if err := s.Remove(ctx, w.ID); err != nil {
		t.Fatalf("remove: %v", err)
	}
	all, err := s.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) != 0 {
		t.Fatalf("expected 0 entries after remove, got %d", len(all))
	}
	// Removing a missing id is not an error.
	if err := s.Remove(ctx, w.ID); err != nil {
		t.Errorf("removing a missing id should be a no-op, got %v", err)
	}
}

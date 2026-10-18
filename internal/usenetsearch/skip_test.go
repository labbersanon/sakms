package usenetsearch

import (
	"context"
	"testing"

	"github.com/labbersanon/sakms/internal/dbtest"
)

func TestDropSkipped_FiltersSkippedKeys(t *testing.T) {
	sqlDB := dbtest.New(t)
	idx, err := OpenIndex(sqlDB)
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()
	ctx := context.Background()
	const g = "alt.binaries.movies"
	if _, err := sqlDB.ExecContext(ctx, `
INSERT INTO usenet_nntp_skips (group_name, release_key, reason, first_msg, last_msg, skipped_at)
VALUES (?, 'Stale.Partial.2020', 'incomplete', 101, 150, 1)`, g); err != nil {
		t.Fatal(err)
	}
	rows := append(
		fileRows(g, "Stale.Partial.2020", "s.mkv", "s", 200, 10, 5, 0),
		fileRows(g, "Fresh.Movie.2020", "f.mkv", "f", 300, 10, 5, 0)...,
	)
	kept, dropped, err := idx.DropSkipped(ctx, g, rows)
	if err != nil {
		t.Fatal(err)
	}
	if dropped != 5 || len(kept) != 5 {
		t.Fatalf("kept=%d dropped=%d, want 5/5", len(kept), dropped)
	}
	for _, h := range kept {
		if h.ReleaseName != "Fresh.Movie.2020" {
			t.Fatalf("kept %q, want only Fresh.Movie.2020", h.ReleaseName)
		}
	}
}

func TestDropSkipped_Empty(t *testing.T) {
	kept, n, err := (&Index{}).DropSkipped(context.Background(), "g", nil)
	if err != nil || n != 0 || len(kept) != 0 {
		t.Fatalf("kept=%d n=%d err=%v", len(kept), n, err)
	}
}

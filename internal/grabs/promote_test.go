package grabs

import (
	"context"
	"testing"
	"time"
)

func TestPromoteToFront_PendingRetryBecomesDueNow(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	g, err := s.Create(ctx, pendingRetry(Grab{
		Mode: "movies", Title: "Some Movie", TMDBID: 42, RootFolderPath: "/movies",
	}, now.Add(30*24*time.Hour)))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	countBefore := g.RetryCount

	if err := s.PromoteToFront(ctx, g.ID, now, "operator promoted to top of schedule"); err != nil {
		t.Fatalf("PromoteToFront: %v", err)
	}
	got, err := s.Get(ctx, g.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status != PendingRetry {
		t.Fatalf("status = %q, want pending_retry", got.Status)
	}
	if got.RetryAfter != FormatTime(now) {
		t.Fatalf("retry_after = %q, want %q", got.RetryAfter, FormatTime(now))
	}
	if got.RetryCount != countBefore {
		t.Fatalf("retry_count = %d, want unchanged %d", got.RetryCount, countBefore)
	}
	due, err := s.DueForRetry(ctx, now.Add(time.Second))
	if err != nil {
		t.Fatalf("DueForRetry: %v", err)
	}
	if len(due) != 1 || due[0].ID != g.ID {
		t.Fatalf("expected promoted row due, got %+v", due)
	}
}

func TestPromoteToFront_ScheduledHoldIsPasted(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	g, err := s.Create(ctx, pendingRetry(Grab{
		Mode: "movies", Title: "Upcoming", TMDBID: 99, RootFolderPath: "/movies",
		HoldUntil: FormatTime(now.Add(10 * 24 * time.Hour)),
	}, now.Add(10*24*time.Hour)))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := s.PromoteToFront(ctx, g.ID, now, "operator promoted to top of schedule"); err != nil {
		t.Fatalf("PromoteToFront: %v", err)
	}
	got, err := s.Get(ctx, g.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.HoldUntil != FormatTime(now) {
		t.Fatalf("hold_until = %q, want pasted to %q", got.HoldUntil, FormatTime(now))
	}
	due, err := s.DueForRetry(ctx, now.Add(time.Second))
	if err != nil {
		t.Fatalf("DueForRetry: %v", err)
	}
	if len(due) != 1 || due[0].ID != g.ID {
		t.Fatalf("held row must become due after promote, got %+v", due)
	}
}

func TestPromoteToFront_QueuedWithoutCounting(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	g, err := s.Create(ctx, Grab{
		Mode: "movies", Title: "Queued Movie", TMDBID: 7, RootFolderPath: "/movies",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := s.SetDownloadGID(ctx, g.ID, "gid-live"); err != nil {
		t.Fatalf("set gid: %v", err)
	}
	if err := s.PromoteToFront(ctx, g.ID, now, "operator promoted to top of schedule"); err != nil {
		t.Fatalf("PromoteToFront: %v", err)
	}
	got, err := s.Get(ctx, g.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status != PendingRetry || got.DownloadGID != "" {
		t.Fatalf("want pending_retry with cleared gid, got status=%q gid=%q", got.Status, got.DownloadGID)
	}
	if got.RetryCount != 0 {
		t.Fatalf("retry_count = %d, want 0 (promote must not count an attempt)", got.RetryCount)
	}
}

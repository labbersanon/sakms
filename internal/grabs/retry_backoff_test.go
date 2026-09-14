package grabs

import (
	"context"
	"testing"
	"time"

	"github.com/labbersanon/sakms/internal/mode"
)

func TestRetryBackoffSchedule(t *testing.T) {
	cases := []struct {
		count int
		want  time.Duration
	}{
		{-1, 24 * time.Hour},
		{0, 24 * time.Hour},
		{1, 3 * 24 * time.Hour},
		{2, 10 * 24 * time.Hour},
		{3, 30 * 24 * time.Hour},
		{4, 60 * 24 * time.Hour},
		{5, 90 * 24 * time.Hour},
		{6, 90 * 24 * time.Hour},
		{50, 90 * 24 * time.Hour},
		{1000, 90 * 24 * time.Hour},
	}
	var previous time.Duration
	for _, c := range cases {
		got := RetryBackoff(c.count)
		if got != c.want {
			t.Errorf("RetryBackoff(%d) = %s, want %s", c.count, got, c.want)
		}
		if got <= 0 {
			t.Errorf("RetryBackoff(%d) = %s — zero would make the row due immediately", c.count, got)
		}
		if c.count < 0 {
			continue
		}
		if got < previous {
			t.Errorf("RetryBackoff(%d) = %s is shorter than previous %s; schedule must be non-decreasing", c.count, got, previous)
		}
		previous = got
	}
}

func TestParkWithBackoff_UsesPostIncrementCount(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)

	g, err := s.Create(ctx, Grab{
		Mode: mode.Movies, Title: "Backoff Movie", TMDBID: 99, RootFolderPath: "/movies",
		Status: PendingRetry, RetryAfter: FormatTime(now), RetryReason: "seed",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if g.RetryCount != 0 {
		t.Fatalf("fresh pending_retry count = %d, want 0", g.RetryCount)
	}

	// First SetPendingRetry park: pre-count 0 → post-count 1 → 3d, not 24h.
	if err := s.ParkWithBackoff(ctx, g.ID, now, "no candidate cleared the quality floor"); err != nil {
		t.Fatalf("ParkWithBackoff: %v", err)
	}
	got, err := s.Get(ctx, g.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.RetryCount != 1 {
		t.Fatalf("retry_count = %d, want 1", got.RetryCount)
	}
	wantAfter := FormatTime(now.Add(3 * 24 * time.Hour))
	if got.RetryAfter != wantAfter {
		t.Fatalf("retry_after = %q, want %q (post-increment count 1 → 3d; 24h would mean pre-count was used)", got.RetryAfter, wantAfter)
	}
	if got.DownloadGID != "" {
		t.Fatalf("download_gid must be cleared on park, got %q", got.DownloadGID)
	}
}

func TestParkWithBackoff_WalksFullSchedule(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)

	g, err := s.Create(ctx, Grab{
		Mode: mode.Movies, Title: "Walk", TMDBID: 7, RootFolderPath: "/movies",
		Status: PendingRetry, RetryAfter: FormatTime(now),
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	wantDelays := []time.Duration{
		3 * 24 * time.Hour,  // count 1
		10 * 24 * time.Hour, // count 2
		30 * 24 * time.Hour, // count 3
		60 * 24 * time.Hour, // count 4
		90 * 24 * time.Hour, // count 5
		90 * 24 * time.Hour, // count 6 ceiling
	}
	for i, wantDelay := range wantDelays {
		step := now.Add(time.Duration(i) * time.Hour)
		if err := s.ParkWithBackoff(ctx, g.ID, step, "still nothing"); err != nil {
			t.Fatalf("park %d: %v", i+1, err)
		}
		got, err := s.Get(ctx, g.ID)
		if err != nil {
			t.Fatalf("get %d: %v", i+1, err)
		}
		if got.RetryCount != i+1 {
			t.Fatalf("park %d: retry_count = %d, want %d", i+1, got.RetryCount, i+1)
		}
		wantAfter := FormatTime(step.Add(wantDelay))
		if got.RetryAfter != wantAfter {
			t.Fatalf("park %d: retry_after = %q, want %q", i+1, got.RetryAfter, wantAfter)
		}
	}
}

package grabs

import (
	"context"
	"errors"
	"testing"
	"time"
)

// pendingRetry builds the row shape §2.3.1 specifies for an auto-grab that
// found nothing worth grabbing: no GID, no download URL, count 0.
func pendingRetry(g Grab, after time.Time) Grab {
	g.Status = PendingRetry
	g.RetryAfter = FormatTime(after)
	g.RetryReason = "no candidate cleared the quality floor"
	return g
}

func TestCreate_PendingRetryStartsAtCountZero(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	due := time.Now().Add(24 * time.Hour)

	g, err := s.Create(ctx, pendingRetry(Grab{
		Mode: "movies", Title: "Some Movie", TMDBID: 42, RootFolderPath: "/movies",
	}, due))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if g.Status != PendingRetry {
		t.Errorf("a pending_retry create must not be forced to queued, got %q", g.Status)
	}
	if g.RetryCount != 0 {
		t.Errorf("a freshly created retry row starts at 0 attempts, got %d", g.RetryCount)
	}

	stored, err := s.Get(ctx, g.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if stored.Status != PendingRetry || stored.RetryAfter != FormatTime(due) || stored.RetryCount != 0 {
		t.Errorf("row did not persist as written: %+v", stored)
	}
	if stored.DownloadGID != "" || stored.DownloadURL != "" {
		t.Errorf("nothing was dispatched, so gid and download url must be empty: %+v", stored)
	}
}

func TestCreate_EveryOtherStatusIsStillForcedToQueued(t *testing.T) {
	s := newTestStore(t)
	g, err := s.Create(context.Background(), Grab{
		Mode: "movies", Title: "Some Movie", Status: Imported, RetryCount: 9, RetryReason: "nope",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if g.Status != Queued {
		t.Errorf("a caller must not be able to fabricate a non-queued grab, got %q", g.Status)
	}
	if g.RetryCount != 0 || g.RetryReason != "" {
		t.Errorf("retry state belongs only to pending_retry rows, got %+v", g)
	}
}

// TestCreate_DownloadURLIsEncryptedAtRest proves the URL round-trips through
// the store while never sitting in the DB in plaintext — it commonly embeds an
// indexer API key.
func TestCreate_DownloadURLIsEncryptedAtRest(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	const url = "https://indexer.example.com/nzb/1?apikey=supersecret"

	g, err := s.Create(ctx, Grab{Mode: "movies", Title: "M", DownloadURL: url})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := s.Get(ctx, g.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.DownloadURL != url {
		t.Errorf("download url should round-trip decrypted, got %q", got.DownloadURL)
	}

	var stored string
	if err := s.db.QueryRow(`SELECT download_url_encrypted FROM grabs WHERE id = ?`, g.ID).Scan(&stored); err != nil {
		t.Fatalf("reading raw column: %v", err)
	}
	if stored == "" || stored == url {
		t.Errorf("download url must be encrypted at rest, raw column holds %q", stored)
	}
}

// TestFindPendingRetry_SeasonZeroIsNotNoSeason is the Season-0/Specials
// collision this codebase already fixed once for grabs generally: TMDB uses
// season 0 for Specials, which is the same int as "no season was picked", and
// SeasonSpecified is the only thing that separates them. A dedup key omitting
// it would silently collapse the two.
func TestFindPendingRetry_SeasonZeroIsNotNoSeason(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	due := time.Now().Add(time.Hour)

	specials, err := s.Create(ctx, pendingRetry(Grab{
		Mode: "series", Title: "Some Show", TMDBID: 7, SeasonNumber: 0, SeasonSpecified: true,
	}, due))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	seriesWide, err := s.Create(ctx, pendingRetry(Grab{
		Mode: "series", Title: "Some Show", TMDBID: 7, SeasonNumber: 0, SeasonSpecified: false,
	}, due))
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	got, err := s.FindPendingRetry(ctx, "series", 7, "Some Show", 0, true, 0)
	if err != nil {
		t.Fatalf("find (specials): %v", err)
	}
	if got.ID != specials.ID {
		t.Errorf("a deliberate Season 0 must not match the no-season row, got id %d want %d", got.ID, specials.ID)
	}
	got, err = s.FindPendingRetry(ctx, "series", 7, "Some Show", 0, false, 0)
	if err != nil {
		t.Fatalf("find (series-wide): %v", err)
	}
	if got.ID != seriesWide.ID {
		t.Errorf("the no-season row must not match a deliberate Season 0, got id %d want %d", got.ID, seriesWide.ID)
	}
}

// TestFindPendingRetry_AdultKeysOnTitle covers the other half of the corrected
// key: Adult scenes carry no TMDB id, so without a title discriminator every
// Adult retry row would collapse onto one key and clobber the others.
func TestFindPendingRetry_AdultKeysOnTitle(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	due := time.Now().Add(time.Hour)

	first, err := s.Create(ctx, pendingRetry(Grab{Mode: "adult", Title: "Scene One"}, due))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	second, err := s.Create(ctx, pendingRetry(Grab{Mode: "adult", Title: "Scene Two"}, due))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if first.ID == second.ID {
		t.Fatal("expected two distinct rows")
	}

	got, err := s.FindPendingRetry(ctx, "adult", 0, "Scene Two", 0, false, 0)
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if got.ID != second.ID {
		t.Errorf("two Adult scenes must not share a key, got id %d want %d", got.ID, second.ID)
	}
	// Title normalization is excludes.Key's (lowercase + trim), so a
	// pending-retry key and a Requests/exclusion key match byte-for-byte.
	if got, err = s.FindPendingRetry(ctx, "adult", 0, "  scene TWO ", 0, false, 0); err != nil || got.ID != second.ID {
		t.Errorf("title match should be case/space-insensitive, got %v (err %v)", got, err)
	}
	if _, err := s.FindPendingRetry(ctx, "adult", 0, "Scene Three", 0, false, 0); !errors.Is(err, ErrNotFound) {
		t.Errorf("want ErrNotFound for an unknown title, got %v", err)
	}
}

func TestFindPendingRetry_IgnoresOtherModesAndStatuses(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	due := time.Now().Add(time.Hour)

	if _, err := s.Create(ctx, pendingRetry(Grab{Mode: "movies", Title: "Dune", TMDBID: 5}, due)); err != nil {
		t.Fatalf("create: %v", err)
	}
	// Same TMDB id, different mode — the key is mode-scoped.
	if _, err := s.FindPendingRetry(ctx, "series", 5, "Dune", 0, false, 0); !errors.Is(err, ErrNotFound) {
		t.Errorf("the key must be mode-scoped, got %v", err)
	}
	// An in-flight (non-pending_retry) grab is not a retry row.
	if _, err := s.Create(ctx, Grab{Mode: "movies", Title: "Arrival", TMDBID: 6}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := s.FindPendingRetry(ctx, "movies", 6, "Arrival", 0, false, 0); !errors.Is(err, ErrNotFound) {
		t.Errorf("only pending_retry rows are dedup candidates, got %v", err)
	}
}

func TestDueForRetry_OnlyDueGidlessRows(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now()

	due, err := s.Create(ctx, pendingRetry(Grab{Mode: "movies", Title: "Due", TMDBID: 1}, now.Add(-time.Hour)))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := s.Create(ctx, pendingRetry(Grab{Mode: "movies", Title: "Not yet", TMDBID: 2}, now.Add(time.Hour))); err != nil {
		t.Fatalf("create: %v", err)
	}
	// A pending row that already dispatched holds a GID and rejoins the normal
	// ActiveByDownloadGID-guarded lifecycle — returning it would double-grab.
	dispatched := pendingRetry(Grab{Mode: "movies", Title: "Dispatched", TMDBID: 3}, now.Add(-time.Hour))
	dispatched.DownloadGID = "nzb-1"
	if _, err := s.Create(ctx, dispatched); err != nil {
		t.Fatalf("create: %v", err)
	}
	// A pending row with no retry_after at all: '' sorts below every real
	// timestamp, so an unguarded comparison would wrongly call it due.
	noAfter := Grab{Mode: "movies", Title: "Never parked", TMDBID: 4, Status: PendingRetry}
	if _, err := s.Create(ctx, noAfter); err != nil {
		t.Fatalf("create: %v", err)
	}

	got, err := s.DueForRetry(ctx, now)
	if err != nil {
		t.Fatalf("due for retry: %v", err)
	}
	if len(got) != 1 || got[0].ID != due.ID {
		t.Fatalf("want only the due gid-less parked row, got %+v", got)
	}
}

// TestSetPendingRetry_NeverGivesUp covers the 2026-08-01 decision to remove
// the retry-attempt cap: a permanently ungradeable item (a Series season
// pack, a duration-less Adult scene) now retries indefinitely instead of
// eventually becoming Failed. retry_count keeps incrementing (observability
// only) but never drives a status transition, well past what was previously
// the give-up threshold.
func TestSetPendingRetry_NeverGivesUp(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now()

	g, err := s.Create(ctx, pendingRetry(Grab{Mode: "series", Title: "Show", TMDBID: 8, SeasonNumber: 2, SeasonSpecified: true}, now))
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	const attemptsWellPastTheOldCap = 12
	for i := 1; i <= attemptsWellPastTheOldCap; i++ {
		if err := s.SetPendingRetry(ctx, g.ID, now.Add(24*time.Hour), "still nothing qualified"); err != nil {
			t.Fatalf("attempt %d: %v", i, err)
		}
		got, err := s.Get(ctx, g.ID)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if got.Status != PendingRetry {
			t.Fatalf("attempt %d should stay pending (no cap), got %q", i, got.Status)
		}
		if got.RetryCount != i {
			t.Fatalf("attempt %d should be counted, got %d", i, got.RetryCount)
		}
		if got.RetryAfter == "" {
			t.Errorf("attempt %d should still be parked for a future retry, got empty RetryAfter", i)
		}
		if got.RetryReason != "still nothing qualified" {
			t.Errorf("attempt %d reason should be the real reason, not a give-up message, got %q", i, got.RetryReason)
		}
	}

	// Never becomes due-forever-excluded: it should still show up as due once
	// its retry_after arrives, even this many cycles in.
	if due, err := s.DueForRetry(ctx, now.Add(48*time.Hour)); err != nil || len(due) != 1 || due[0].ID != g.ID {
		t.Errorf("a grab past the old cap must still come back due, got %+v (err %v)", due, err)
	}
}

func TestSetPendingRetry_ErrNotFound(t *testing.T) {
	s := newTestStore(t)
	if err := s.SetPendingRetry(context.Background(), 404, time.Now(), "x"); !errors.Is(err, ErrNotFound) {
		t.Errorf("want ErrNotFound, got %v", err)
	}
}

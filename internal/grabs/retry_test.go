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

// parkedWithGIDAndCount seeds the one row shape that can tell SetRetryAfter and
// SetPendingRetry apart: pending_retry, retry_count already non-zero, and a
// non-empty download_gid.
//
// The order is load-bearing and not the obvious one. Create starts every row at
// retry_count 0, and SetPendingRetry CLEARS download_gid — so seeding the GID
// first would leave it blank by the time the assertions run, making the
// "download_gid unchanged" check vacuously true against a method that clears it.
// Park first (which bumps the count), then set the GID.
func parkedWithGIDAndCount(t *testing.T, s *Store) Grab {
	t.Helper()
	ctx := context.Background()

	g, err := s.Create(ctx, pendingRetry(Grab{
		Mode: "series", Title: "Show", TMDBID: 88, SeasonNumber: 3, EpisodeNumber: 4, SeasonSpecified: true,
	}, time.Now()))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := s.SetPendingRetry(ctx, g.ID, time.Now().Add(24*time.Hour), "nothing qualified"); err != nil {
		t.Fatalf("seeding a counted attempt: %v", err)
	}
	if err := s.SetDownloadGID(ctx, g.ID, "gid-1234"); err != nil {
		t.Fatalf("seeding a gid: %v", err)
	}

	seeded, err := s.Get(ctx, g.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if seeded.RetryCount != 1 || seeded.DownloadGID != "gid-1234" || seeded.Status != PendingRetry {
		t.Fatalf("seed did not produce the intended row shape: %+v", seeded)
	}
	return *seeded
}

// TestSetRetryAfter_ReschedulesWithoutCountingAnAttempt pins the two methods'
// contracts against EACH OTHER rather than only describing the difference in a
// doc comment.
//
// SetRetryAfter exists because the air-date backoff sweep runs after
// parkPendingRetry has already incremented retry_count for the same cycle's
// search. If it were to call SetPendingRetry instead, a searched cycle would
// advance the count by 2, halving the documented backoff schedule — and it
// would additionally clear a GID that rescheduling says nothing about.
func TestSetRetryAfter_ReschedulesWithoutCountingAnAttempt(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	seeded := parkedWithGIDAndCount(t, s)
	rescheduled := time.Now().Add(72 * time.Hour)

	if err := s.SetRetryAfter(ctx, seeded.ID, rescheduled, "air-date backoff"); err != nil {
		t.Fatalf("set retry after: %v", err)
	}
	got, err := s.Get(ctx, seeded.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}

	// The two fields it DOES write.
	if got.RetryAfter != FormatTime(rescheduled) {
		t.Errorf("retry_after = %q, want %q", got.RetryAfter, FormatTime(rescheduled))
	}
	if got.RetryReason != "air-date backoff" {
		t.Errorf("retry_reason = %q, want %q", got.RetryReason, "air-date backoff")
	}

	// The three it must not.
	if got.RetryCount != seeded.RetryCount {
		t.Errorf("retry_count = %d, want %d unchanged — rescheduling is not an attempt", got.RetryCount, seeded.RetryCount)
	}
	if got.Status != seeded.Status {
		t.Errorf("status = %q, want %q unchanged", got.Status, seeded.Status)
	}
	if got.DownloadGID != seeded.DownloadGID {
		t.Errorf("download_gid = %q, want %q unchanged", got.DownloadGID, seeded.DownloadGID)
	}
}

// TestSetPendingRetry_ContrastsWithSetRetryAfter is the other half of the pair:
// the SAME seeded row shape, through SetPendingRetry, DOES increment the count
// and DOES clear the GID. Without this case the test above would pass equally
// well against two methods that behave identically.
func TestSetPendingRetry_ContrastsWithSetRetryAfter(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	seeded := parkedWithGIDAndCount(t, s)

	if err := s.SetPendingRetry(ctx, seeded.ID, time.Now().Add(72*time.Hour), "another attempt"); err != nil {
		t.Fatalf("set pending retry: %v", err)
	}
	got, err := s.Get(ctx, seeded.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.RetryCount != seeded.RetryCount+1 {
		t.Errorf("retry_count = %d, want %d — SetPendingRetry counts an attempt", got.RetryCount, seeded.RetryCount+1)
	}
	if got.DownloadGID != "" {
		t.Errorf("download_gid = %q, want cleared — SetPendingRetry parks a dead download", got.DownloadGID)
	}
}

func TestSetRetryAfter_ErrNotFound(t *testing.T) {
	s := newTestStore(t)
	if err := s.SetRetryAfter(context.Background(), 404, time.Now(), "x"); !errors.Is(err, ErrNotFound) {
		t.Errorf("want ErrNotFound, got %v", err)
	}
}

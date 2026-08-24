package grabs

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/labbersanon/sakms/internal/dbtest"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/secrets"
)

// newTestStore builds a Store against a real, freshly migrated SQLite file —
// exercising the actual SQL, not a mock, matching every other store test in
// this repo.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	sqlDB := dbtest.New(t)
	secretStore, err := secrets.New(make([]byte, 32))
	if err != nil {
		t.Fatalf("building secret store: %v", err)
	}
	return New(sqlDB, secretStore)
}

func TestCreate_StartsQueuedWithTimestampsPopulated(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	g, err := s.Create(ctx, Grab{
		Mode: mode.Movies, Title: "Some Movie", TMDBID: 123,
		Indexer: "SomeIndexer", Protocol: "torrent", DownloadClient: "anacrolix",
		ClientRef: "abc123", RootFolderPath: "/movies",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if g.ID == 0 {
		t.Error("expected a nonzero ID")
	}
	if g.Status != Queued {
		t.Errorf("expected new grab to start Queued, got %q", g.Status)
	}
	if g.CreatedAt == "" || g.UpdatedAt == "" {
		t.Error("expected CreatedAt/UpdatedAt to be populated")
	}
}

func TestGet_RoundTripsEveryField(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	created, err := s.Create(ctx, Grab{
		Mode: mode.Series, Title: "Some Show S01E01", TVDBID: 456,
		SeasonNumber: 1, EpisodeNumber: 1, SeasonSpecified: true,
		Indexer: "SomeUsenetIndexer", Protocol: "usenet", DownloadClient: "nntp",
		ClientRef: "42", RootFolderPath: "/tv",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := s.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Mode != mode.Series || got.Title != "Some Show S01E01" || got.TVDBID != 456 ||
		got.SeasonNumber != 1 || got.EpisodeNumber != 1 || !got.SeasonSpecified ||
		got.Indexer != "SomeUsenetIndexer" || got.Protocol != "usenet" || got.DownloadClient != "nntp" ||
		got.ClientRef != "42" || got.RootFolderPath != "/tv" || got.Status != Queued {
		t.Errorf("unexpected round-tripped grab: %+v", got)
	}
}

func TestCreate_SeasonSpecifiedDefaultsFalse(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	created, err := s.Create(ctx, Grab{
		Mode: mode.Movies, Title: "Some Movie", TMDBID: 123,
		Indexer: "SomeIndexer", Protocol: "torrent", DownloadClient: "anacrolix",
		RootFolderPath: "/movies",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if created.SeasonSpecified {
		t.Error("expected SeasonSpecified to default false when not set")
	}
}

// mustCreateWithGID creates a grab and stamps its download GID — the shape the
// grab handlers produce (Create, then SetDownloadGID once the download client
// assigns the GID).
func mustCreateWithGID(t *testing.T, s *Store, m mode.Mode, title, gid string) Grab {
	t.Helper()
	ctx := context.Background()
	g, err := s.Create(ctx, Grab{Mode: m, Title: title, Indexer: "I", Protocol: "torrent", DownloadClient: "anacrolix", RootFolderPath: "/x"})
	if err != nil {
		t.Fatalf("creating grab %q: %v", title, err)
	}
	if err := s.SetDownloadGID(ctx, g.ID, gid); err != nil {
		t.Fatalf("setting gid for %q: %v", title, err)
	}
	g.DownloadGID = gid
	return g
}

// TestActiveByDownloadGID_FindsInFlightScopedByModeAndGID proves the guard
// lookup is keyed on (mode, download_gid): it finds the in-flight grab for a
// GID, does NOT conflate two distinct GIDs (so genuinely distinct downloads
// never block each other), and is scoped per-mode.
func TestActiveByDownloadGID_FindsInFlightScopedByModeAndGID(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	a := mustCreateWithGID(t, s, mode.Movies, "Movie A", "gid-a")
	b := mustCreateWithGID(t, s, mode.Movies, "Movie B", "gid-b")
	c := mustCreateWithGID(t, s, mode.Series, "Show C", "gid-a") // same GID, different mode

	got, err := s.ActiveByDownloadGID(ctx, mode.Movies, "gid-a")
	if err != nil || got.ID != a.ID {
		t.Errorf("expected grab %d for (movies, gid-a), got %+v err=%v", a.ID, got, err)
	}
	got, err = s.ActiveByDownloadGID(ctx, mode.Movies, "gid-b")
	if err != nil || got.ID != b.ID {
		t.Errorf("a distinct GID must not be conflated: expected grab %d for (movies, gid-b), got %+v err=%v", b.ID, got, err)
	}
	got, err = s.ActiveByDownloadGID(ctx, mode.Series, "gid-a")
	if err != nil || got.ID != c.ID {
		t.Errorf("expected mode scoping: grab %d for (series, gid-a), got %+v err=%v", c.ID, got, err)
	}
	if _, err := s.ActiveByDownloadGID(ctx, mode.Movies, "gid-none"); !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound for an unknown GID, got %v", err)
	}
}

// TestActiveByDownloadGID_IgnoresTerminalStates proves a grab that reached a
// terminal state (imported or failed) no longer counts as active — a fresh
// re-grab of that same release is legitimate and must not be blocked — while a
// still-in-flight grab (including completed-but-not-yet-imported) does count.
func TestActiveByDownloadGID_IgnoresTerminalStates(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	for _, st := range []Status{Imported, Failed} {
		g := mustCreateWithGID(t, s, mode.Movies, "Movie "+string(st), "gid-"+string(st))
		if err := s.UpdateStatus(ctx, g.ID, st); err != nil {
			t.Fatalf("updating status to %s: %v", st, err)
		}
		if _, err := s.ActiveByDownloadGID(ctx, mode.Movies, "gid-"+string(st)); !errors.Is(err, ErrNotFound) {
			t.Errorf("a %s grab must not count as active (a fresh re-grab is legitimate), got %v", st, err)
		}
	}

	g := mustCreateWithGID(t, s, mode.Movies, "Movie completed", "gid-completed")
	if err := s.UpdateStatus(ctx, g.ID, Completed); err != nil {
		t.Fatalf("updating status to completed: %v", err)
	}
	got, err := s.ActiveByDownloadGID(ctx, mode.Movies, "gid-completed")
	if err != nil || got.ID != g.ID {
		t.Errorf("a completed (not-yet-imported) grab must still count as active, got %+v err=%v", got, err)
	}
}

// TestActiveByDownloadGID_EmptyGIDNotFound proves an empty GID is never a dedup
// key (many grabs legitimately have download_gid='').
func TestActiveByDownloadGID_EmptyGIDNotFound(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.ActiveByDownloadGID(context.Background(), mode.Movies, ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound for an empty GID, got %v", err)
	}
}

func TestGet_NotFound(t *testing.T) {
	s := newTestStore(t)
	_, err := s.Get(context.Background(), 999)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestUpdateStatus_ChangesStatusAndTimestamp(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	created, err := s.Create(ctx, Grab{Mode: mode.Movies, Title: "X", Indexer: "I", Protocol: "torrent", DownloadClient: "anacrolix", RootFolderPath: "/movies"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if err := s.UpdateStatus(ctx, created.ID, Downloading); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := s.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Status != Downloading {
		t.Errorf("expected status Downloading, got %q", got.Status)
	}
}

func TestUpdateStatus_NotFound(t *testing.T) {
	s := newTestStore(t)
	err := s.UpdateStatus(context.Background(), 999, Completed)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestFlag_RoundTripsAndDefaultsUnflagged(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	created, err := s.Create(ctx, Grab{
		Mode: mode.Movies, Title: "Some Movie", TMDBID: 123,
		Indexer: "I", Protocol: "torrent", DownloadClient: "anacrolix", RootFolderPath: "/movies",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// A brand-new grab is unflagged by default.
	if created.FlaggedForReview || created.FlagReason != "" {
		t.Fatalf("new grab should be unflagged, got flagged=%v reason=%q", created.FlaggedForReview, created.FlagReason)
	}

	const reason = "imported file runs 4 min but TMDB lists 100 min — possible mislabel or wrong content"
	if err := s.Flag(ctx, created.ID, reason); err != nil {
		t.Fatalf("Flag: %v", err)
	}

	got, err := s.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !got.FlaggedForReview || got.FlagReason != reason {
		t.Fatalf("after Flag: flagged=%v reason=%q, want true/%q", got.FlaggedForReview, got.FlagReason, reason)
	}
	// The flag must NOT touch the lifecycle status — the import still succeeded.
	if got.Status != Queued {
		t.Fatalf("Flag changed status to %q; it must leave the lifecycle status alone", got.Status)
	}
}

func TestFlag_NotFound(t *testing.T) {
	s := newTestStore(t)
	if err := s.Flag(context.Background(), 999, "x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound flagging a missing grab, got %v", err)
	}
}

func TestList_ScopedByModeAndOrderedNewestFirst(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if _, err := s.Create(ctx, Grab{Mode: mode.Movies, Title: "Movie A", Indexer: "I", Protocol: "torrent", DownloadClient: "anacrolix", RootFolderPath: "/movies"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := s.Create(ctx, Grab{Mode: mode.Movies, Title: "Movie B", Indexer: "I", Protocol: "torrent", DownloadClient: "anacrolix", RootFolderPath: "/movies"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := s.Create(ctx, Grab{Mode: mode.Series, Title: "Show A", Indexer: "I", Protocol: "usenet", DownloadClient: "nntp", RootFolderPath: "/tv"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	movies, err := s.List(ctx, mode.Movies)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(movies) != 2 {
		t.Fatalf("expected 2 movie grabs, got %d", len(movies))
	}
	if movies[0].Title != "Movie B" {
		t.Errorf("expected most recently created grab first, got %q", movies[0].Title)
	}
}

func TestList_EmptyIsNotNil(t *testing.T) {
	s := newTestStore(t)
	got, err := s.List(context.Background(), mode.Adult)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got == nil {
		t.Error("expected an empty slice, not nil, so it serializes as [] not null")
	}
}

// held builds the row shape §5.2.2 specifies for a Calendar pre-release
// request: pending_retry, a real hold_until, and retry_after DELIBERATELY
// EMPTY — the empty retry_after is what keeps the row invisible to DueForRetry
// until releaseDueGrabs promotes it.
func held(g Grab, until time.Time) Grab {
	g.Status = PendingRetry
	g.HoldUntil = FormatTime(until)
	g.RetryReason = "held until its release date"
	return g
}

// TestHoldUntil_EveryReadSiteRoundTripsIt is the lockstep guard for migration
// 0057: every SELECT in this file must carry hold_until, because scanGrab now
// has a 27th destination.
//
// THE FAILURE MODE THIS EXISTS FOR IS AN ERROR, NOT A WRONG VALUE. A SELECT
// that missed the column returns "sql: expected 26 destination arguments in
// Scan, not 27" — not a panic, not a compile failure. GetByDownloadGID and
// ActiveByDownloadGID are the two that matter most: DownloadCompleteImporter
// and UsenetCompleteImporter both log-and-return on any non-ErrNotFound error
// from GetByDownloadGID, so a missed column there means EVERY completed
// download, torrent and usenet, across all three modes, silently stops
// importing — grabs stranded at 'downloading' forever, one log line the only
// symptom. So the `err != nil` check on each reader is the real assertion here;
// the HoldUntil value check is secondary.
//
// EIGHT readers, not the seven the plan's change list enumerates: that table
// omits FindHeldRequest, which is itself one of the three methods the same
// section adds. Verified by grep, per the plan's own instruction to trust the
// grep over its prose.
func TestHoldUntil_EveryReadSiteRoundTripsIt(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now()
	past := now.Add(-24 * time.Hour)

	// Three fixtures, because the readers' own guards are mutually exclusive:
	// DueForRelease demands no GID and no retry_after, DueForRetry demands a
	// real retry_after, and the GID lookups demand a GID.
	heldRow, err := s.Create(ctx, held(Grab{Mode: mode.Movies, Title: "Held Movie", TMDBID: 101}, past))
	if err != nil {
		t.Fatalf("creating held row: %v", err)
	}
	promoted := held(Grab{Mode: mode.Movies, Title: "Promoted Movie", TMDBID: 102}, past)
	promoted.RetryAfter = FormatTime(past)
	if _, err := s.Create(ctx, promoted); err != nil {
		t.Fatalf("creating promoted row: %v", err)
	}
	withGID := held(Grab{Mode: mode.Movies, Title: "Dispatched Movie", TMDBID: 103}, past)
	dispatched, err := s.Create(ctx, withGID)
	if err != nil {
		t.Fatalf("creating dispatched row: %v", err)
	}
	if err := s.SetDownloadGID(ctx, dispatched.ID, "gid-hold-1"); err != nil {
		t.Fatalf("seeding gid: %v", err)
	}

	// 1. List
	listed, err := s.List(ctx, mode.Movies)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(listed) != 3 {
		t.Fatalf("List returned %d rows, want 3", len(listed))
	}
	for _, g := range listed {
		if g.HoldUntil == "" {
			t.Errorf("List dropped hold_until for %q", g.Title)
		}
	}

	// 2. Get
	got, err := s.Get(ctx, heldRow.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.HoldUntil != FormatTime(past) {
		t.Errorf("Get hold_until = %q, want %q", got.HoldUntil, FormatTime(past))
	}

	// 3. GetByDownloadGID — the one that silently stops all importing.
	got, err = s.GetByDownloadGID(ctx, "gid-hold-1")
	if err != nil {
		t.Fatalf("GetByDownloadGID: %v — a scan-arity error here silently stops every completed download from importing", err)
	}
	if got.HoldUntil == "" {
		t.Error("GetByDownloadGID dropped hold_until")
	}

	// 4. ActiveByDownloadGID — the duplicate-grab idempotency guard.
	got, err = s.ActiveByDownloadGID(ctx, mode.Movies, "gid-hold-1")
	if err != nil {
		t.Fatalf("ActiveByDownloadGID: %v", err)
	}
	if got.HoldUntil == "" {
		t.Error("ActiveByDownloadGID dropped hold_until")
	}

	// 5. DueForRetry — reached only by the PROMOTED row (the held one carries no
	// retry_after by design), which doubles as proof the new OR arm lets a
	// promoted row rejoin the ordinary retry track.
	due, err := s.DueForRetry(ctx, now)
	if err != nil {
		t.Fatalf("DueForRetry: %v", err)
	}
	if len(due) != 1 || due[0].Title != "Promoted Movie" {
		t.Fatalf("DueForRetry should return only the promoted row, got %+v", due)
	}
	if due[0].HoldUntil == "" {
		t.Error("DueForRetry dropped hold_until")
	}

	// 6. FindPendingRetry
	found, err := s.FindPendingRetry(ctx, mode.Movies, 101, "Held Movie", 0, false, 0)
	if err != nil {
		t.Fatalf("FindPendingRetry: %v", err)
	}
	if found.HoldUntil == "" {
		t.Error("FindPendingRetry dropped hold_until")
	}

	// 7. FindHeldRequest
	found, err = s.FindHeldRequest(ctx, mode.Movies, 101)
	if err != nil {
		t.Fatalf("FindHeldRequest: %v", err)
	}
	if found.HoldUntil == "" {
		t.Error("FindHeldRequest dropped hold_until")
	}

	// 8. DueForRelease
	release, err := s.DueForRelease(ctx, now)
	if err != nil {
		t.Fatalf("DueForRelease: %v", err)
	}
	if len(release) != 1 || release[0].ID != heldRow.ID {
		t.Fatalf("DueForRelease should return only the un-promoted held row, got %+v", release)
	}
	if release[0].HoldUntil == "" {
		t.Error("DueForRelease dropped hold_until")
	}
}

// TestCreate_PersistsHoldUntilOnPendingRetryRows is the write-site guard. If
// Create's INSERT had been left unmodified the whole feature would be INERT and
// report success: every held row would be born hold_until = '' AND
// retry_after = '', making it invisible to DueForRetry (needs retry_after != '')
// and invisible to DueForRelease (needs hold_until != ''). The request would
// never fire, ever, and nothing would error.
func TestCreate_PersistsHoldUntilOnPendingRetryRows(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	release := time.Now().Add(30 * 24 * time.Hour)

	g, err := s.Create(ctx, held(Grab{Mode: mode.Movies, Title: "Unreleased Movie", TMDBID: 555}, release))
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	stored, err := s.Get(ctx, g.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if stored.HoldUntil != FormatTime(release) {
		t.Errorf("hold_until = %q, want %q", stored.HoldUntil, FormatTime(release))
	}
	// §5.2.2's row shape: the hold is set, retry_after is deliberately NOT.
	if stored.RetryAfter != "" {
		t.Errorf("a held row's retry_after must stay empty, got %q", stored.RetryAfter)
	}
	if stored.Status != PendingRetry || stored.DownloadGID != "" || stored.TMDBID != 555 {
		t.Errorf("unexpected held row shape: %+v", stored)
	}
}

// TestCreate_ZeroesHoldUntilOnNonPendingRetryRows covers the reset arm. It is
// the same fabrication guard that already zeroes RetryAfter/RetryCount/
// RetryReason: without it a caller could mint a `queued` row carrying a hold,
// and since hold_until is this feature's ORIGIN MARKER, that row would be
// permanently misread as a Calendar pre-release request it never was.
func TestCreate_ZeroesHoldUntilOnNonPendingRetryRows(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	g, err := s.Create(ctx, Grab{
		Mode: mode.Movies, Title: "Fabricated", TMDBID: 77,
		Status: Imported, HoldUntil: FormatTime(time.Now().Add(24 * time.Hour)),
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if g.Status != Queued {
		t.Errorf("status = %q, want queued", g.Status)
	}
	if g.HoldUntil != "" {
		t.Errorf("returned HoldUntil = %q, want empty — a caller must not be able to fabricate a held non-retry row", g.HoldUntil)
	}

	stored, err := s.Get(ctx, g.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if stored.HoldUntil != "" {
		t.Errorf("stored hold_until = %q, want empty", stored.HoldUntil)
	}
	// The corollary that actually matters: such a row is not a pre-release
	// request, so the origin-marker query must not find it.
	if _, err := s.FindHeldRequest(ctx, mode.Movies, 77); !errors.Is(err, ErrNotFound) {
		t.Errorf("a fabricated row must not register as a held request, got %v", err)
	}
}

// TestDueForRetry_NeverReturnsAHeldRow is THE HOLD, asserted against the real
// store. The guarantee is UNCONDITIONAL, not time-dependent: a held row is
// excluded the day before its release date AND the day after, because
// retry_after is empty and DueForRetry's retry_after != '' guard excludes it
// outright. That is strictly stronger than a date comparison, and it is what
// spec Constraint 5 ("must not fire search/grab attempts before the release
// date") actually requires.
func TestDueForRetry_NeverReturnsAHeldRow(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	release := time.Now().Add(48 * time.Hour)

	if _, err := s.Create(ctx, held(Grab{Mode: mode.Movies, Title: "Unreleased", TMDBID: 900}, release)); err != nil {
		t.Fatalf("create: %v", err)
	}

	for _, tc := range []struct {
		name string
		when time.Time
	}{
		{"the day before the release date", release.Add(-24 * time.Hour)},
		{"the day after the release date", release.Add(24 * time.Hour)},
		{"a year after the release date", release.Add(365 * 24 * time.Hour)},
	} {
		got, err := s.DueForRetry(ctx, tc.when)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if len(got) != 0 {
			t.Errorf("%s: a held row must never be due for retry, got %+v", tc.name, got)
		}
	}
}

// TestDueForRetry_HoldConjunctExcludesADueRowThatIsStillHeld is the test for
// the conjunct itself — remedy (ii). Without it this feature's DueForRetry
// change is untested, because the case above passes on the pre-existing
// retry_after != '' guard alone.
//
// The row it seeds is the escape path a Critic pass found: a held row that
// somehow acquired a REAL, DUE retry_after while its hold_until is still in the
// future. The traced route is parkPendingRetry, whose FindPendingRetry lookup
// is keyed on (mode, tmdb_id, season…) and ignores ExistingGrabID — so an
// unrelated failing retry for the same movie can stamp a retry_after onto the
// held row. Without the conjunct DueForRetry hands that row straight back and
// the unreleased film is searched, and possibly dispatched, before its release
// date. The conjunct is defence-in-depth against ANY writer of retry_after, not
// just that one path, which is why this test seeds the end state directly
// rather than reproducing the route.
func TestDueForRetry_HoldConjunctExcludesADueRowThatIsStillHeld(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now()

	stillHeld := held(Grab{Mode: mode.Movies, Title: "Not Out Yet", TMDBID: 901}, now.Add(30*24*time.Hour))
	stillHeld.RetryAfter = FormatTime(now.Add(-time.Hour)) // due, and it must not matter
	if _, err := s.Create(ctx, stillHeld); err != nil {
		t.Fatalf("create: %v", err)
	}

	got, err := s.DueForRetry(ctx, now)
	if err != nil {
		t.Fatalf("due for retry: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("a row whose hold_until is still in the future must not be due for retry even with a due retry_after, got %+v", got)
	}

	// Once the hold passes, the same row IS due — the conjunct is an OR, not a
	// permanent exclusion. A promoted row must rejoin the ordinary retry track,
	// since hold_until is never cleared and is inert provenance from then on.
	got, err = s.DueForRetry(ctx, now.Add(31*24*time.Hour))
	if err != nil {
		t.Fatalf("due for retry after the hold: %v", err)
	}
	if len(got) != 1 || got[0].TMDBID != 901 {
		t.Errorf("once hold_until has passed the row must be due again, got %+v", got)
	}
}

// TestDueForRelease_PromotionBoundary pins the exact moment a held row becomes
// a promotion candidate.
func TestDueForRelease_PromotionBoundary(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	release := time.Now().Add(24 * time.Hour)

	g, err := s.Create(ctx, held(Grab{Mode: mode.Movies, Title: "Releases Tomorrow", TMDBID: 910}, release))
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	before, err := s.DueForRelease(ctx, release.Add(-time.Hour))
	if err != nil {
		t.Fatalf("due for release (before): %v", err)
	}
	if len(before) != 0 {
		t.Errorf("nothing is due before the release date, got %+v", before)
	}

	after, err := s.DueForRelease(ctx, release.Add(time.Hour))
	if err != nil {
		t.Fatalf("due for release (after): %v", err)
	}
	if len(after) != 1 || after[0].ID != g.ID {
		t.Fatalf("the row must be due once its release date passes, got %+v", after)
	}
}

// TestDueForRelease_RespectsAllFourGuards seeds one row per guard, all with a
// PAST hold_until so only the guard under test can exclude them.
func TestDueForRelease_RespectsAllFourGuards(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now()
	past := now.Add(-24 * time.Hour)

	// The one row that should come back.
	want, err := s.Create(ctx, held(Grab{Mode: mode.Movies, Title: "Eligible", TMDBID: 920}, past))
	if err != nil {
		t.Fatalf("create eligible: %v", err)
	}

	// Guard 1 — download_gid = '': a dispatched row has rejoined the normal
	// ActiveByDownloadGID-guarded lifecycle; returning it would double-grab.
	dispatched, err := s.Create(ctx, held(Grab{Mode: mode.Movies, Title: "Dispatched", TMDBID: 921}, past))
	if err != nil {
		t.Fatalf("create dispatched: %v", err)
	}
	if err := s.SetDownloadGID(ctx, dispatched.ID, "gid-release-1"); err != nil {
		t.Fatalf("seeding gid: %v", err)
	}

	// Guard 2 — retry_after = '': the row already had its one promotion attempt
	// and was re-parked onto the ordinary retry track.
	attempted := held(Grab{Mode: mode.Movies, Title: "Already Attempted", TMDBID: 922}, past)
	attempted.RetryAfter = FormatTime(now.Add(24 * time.Hour))
	if _, err := s.Create(ctx, attempted); err != nil {
		t.Fatalf("create attempted: %v", err)
	}

	// Guard 3 — hold_until != '': a row with no hold at all. RetryAfter is left
	// empty too, so guard 2 (retry_after = '') does NOT also exclude it — this
	// row must be excluded ONLY because it was never held, isolating the guard
	// under test. (No current writer produces this exact shape, but the guard
	// becomes load-bearing the moment a promotion-track writer exists.)
	ordinary := Grab{Mode: mode.Movies, Title: "Ordinary Retry", TMDBID: 923, Status: PendingRetry}
	if _, err := s.Create(ctx, ordinary); err != nil {
		t.Fatalf("create ordinary: %v", err)
	}

	// Guard 4 — status = pending_retry: a terminal row is not a candidate.
	terminal, err := s.Create(ctx, held(Grab{Mode: mode.Movies, Title: "Terminal", TMDBID: 924}, past))
	if err != nil {
		t.Fatalf("create terminal: %v", err)
	}
	if err := s.UpdateStatus(ctx, terminal.ID, Failed); err != nil {
		t.Fatalf("seeding terminal status: %v", err)
	}

	got, err := s.DueForRelease(ctx, now)
	if err != nil {
		t.Fatalf("due for release: %v", err)
	}
	if len(got) != 1 || got[0].ID != want.ID {
		titles := make([]string, len(got))
		for i, g := range got {
			titles[i] = g.Title
		}
		t.Fatalf("want only the eligible row, got %v", titles)
	}
}

// TestDueForRelease_PromotionFiresExactlyOnce is why hold_until never needs
// clearing. After a promotion attempt the row carries either a real retry_after
// (parkPendingRetry on a no-match) or a real GID (a successful dispatch), and
// either one removes it from this query permanently. Eligibility ends;
// provenance survives — the row still reads as a Calendar pre-release request
// forever, which is what FindHeldRequest depends on.
func TestDueForRelease_PromotionFiresExactlyOnce(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now()
	past := now.Add(-24 * time.Hour)

	t.Run("after a no-match re-park", func(t *testing.T) {
		g, err := s.Create(ctx, held(Grab{Mode: mode.Movies, Title: "No Match", TMDBID: 930}, past))
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		if due, err := s.DueForRelease(ctx, now); err != nil || len(due) != 1 {
			t.Fatalf("precondition: want exactly one due row, got %+v (err %v)", due, err)
		}

		// What parkPendingRetry does on a no-match.
		if err := s.SetPendingRetry(ctx, g.ID, now.Add(24*time.Hour), "no candidate cleared the quality floor"); err != nil {
			t.Fatalf("parking: %v", err)
		}

		due, err := s.DueForRelease(ctx, now)
		if err != nil {
			t.Fatalf("due for release: %v", err)
		}
		if len(due) != 0 {
			t.Errorf("a promoted row must never be returned again, got %+v", due)
		}
		// Provenance survives the promotion — this is the property that lets a
		// second click still find the original request.
		stored, err := s.Get(ctx, g.ID)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if stored.HoldUntil != FormatTime(past) {
			t.Errorf("hold_until must survive promotion as provenance, got %q", stored.HoldUntil)
		}
	})

	t.Run("after a successful dispatch", func(t *testing.T) {
		g, err := s.Create(ctx, held(Grab{Mode: mode.Movies, Title: "Dispatched OK", TMDBID: 931}, past))
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		if err := s.Relaunch(ctx, g.ID, Dispatch{
			Indexer: "I", Protocol: "usenet", DownloadClient: "nntp",
			RootFolderPath: "/movies", DownloadURL: "https://x/1.nzb", GID: "gid-931",
		}); err != nil {
			t.Fatalf("relaunch: %v", err)
		}

		due, err := s.DueForRelease(ctx, now)
		if err != nil {
			t.Fatalf("due for release: %v", err)
		}
		for _, d := range due {
			if d.ID == g.ID {
				t.Error("a successfully dispatched row must never be returned again")
			}
		}
		stored, err := s.Get(ctx, g.ID)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if stored.HoldUntil != FormatTime(past) {
			t.Errorf("hold_until must survive dispatch as provenance, got %q", stored.HoldUntil)
		}
	})
}

// TestSetHoldUntil_WritesOnlyTheHoldAndTheReason pins SetHoldUntil's contract
// against the same seeded row shape TestSetRetryAfter_ReschedulesWithoutCountingAnAttempt
// uses, so the three narrow writers are held to each other rather than only
// described in doc comments.
//
// retry_count is the one that matters most: a re-click is NOT an attempt, so the
// re-request path must call this and never SetPendingRetry, which increments.
// retry_after matters nearly as much — writing one here would park the row and
// fire the very search the hold exists to prevent.
func TestSetHoldUntil_WritesOnlyTheHoldAndTheReason(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	seeded := parkedWithGIDAndCount(t, s)
	release := time.Now().Add(90 * 24 * time.Hour)

	if err := s.SetHoldUntil(ctx, seeded.ID, release, "held until its release date"); err != nil {
		t.Fatalf("set hold until: %v", err)
	}
	got, err := s.Get(ctx, seeded.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}

	// The two fields it DOES write.
	if got.HoldUntil != FormatTime(release) {
		t.Errorf("hold_until = %q, want %q", got.HoldUntil, FormatTime(release))
	}
	if got.RetryReason != "held until its release date" {
		t.Errorf("retry_reason = %q, want the hold copy", got.RetryReason)
	}

	// The four it must not.
	if got.RetryCount != seeded.RetryCount {
		t.Errorf("retry_count = %d, want %d unchanged — a re-click is not an attempt", got.RetryCount, seeded.RetryCount)
	}
	if got.RetryAfter != seeded.RetryAfter {
		t.Errorf("retry_after = %q, want %q unchanged — writing one would park the row and fire the search the hold prevents", got.RetryAfter, seeded.RetryAfter)
	}
	if got.Status != seeded.Status {
		t.Errorf("status = %q, want %q unchanged", got.Status, seeded.Status)
	}
	if got.DownloadGID != seeded.DownloadGID {
		t.Errorf("download_gid = %q, want %q unchanged", got.DownloadGID, seeded.DownloadGID)
	}
}

func TestSetHoldUntil_ErrNotFound(t *testing.T) {
	s := newTestStore(t)
	if err := s.SetHoldUntil(context.Background(), 404, time.Now(), "x"); !errors.Is(err, ErrNotFound) {
		t.Errorf("want ErrNotFound, got %v", err)
	}
}

// TestFindHeldRequest_IsNotStatusScoped is the single most important property of
// the whole feature: the query finds a pre-release request no matter how far the
// row has travelled, because hold_until is never cleared.
//
// The status-scoped alternative (FindPendingRetry) misses a row that has already
// promoted and flipped to queued, and a second click on that title would mint a
// SECOND row carrying an already-past date — which the very next cycle would
// dispatch as a duplicate download, a direct hit on the mission's "no
// duplicates" bar. This test walks the row through every status to prove the
// lookup survives all of them.
func TestFindHeldRequest_IsNotStatusScoped(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	g, err := s.Create(ctx, held(Grab{Mode: mode.Movies, Title: "Long Lived", TMDBID: 940}, time.Now().Add(-time.Hour)))
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	for _, status := range []Status{PendingRetry, Queued, Downloading, Completed, Imported, Failed} {
		if err := s.UpdateStatus(ctx, g.ID, status); err != nil {
			t.Fatalf("moving row to %q: %v", status, err)
		}
		found, err := s.FindHeldRequest(ctx, mode.Movies, 940)
		if err != nil {
			t.Fatalf("a held request must be findable at status %q, got %v", status, err)
		}
		if found.ID != g.ID {
			t.Errorf("at status %q: found id %d, want %d", status, found.ID, g.ID)
		}
	}
}

// TestFindHeldRequest_OnlyMatchesHeldRows is the regression test for the
// clobber bug the origin marker makes structurally impossible: a Calendar click
// must never be able to reach an unrelated live retry row and suspend a real
// request for months.
//
// Under the column this cannot fail, and that IS the point — the test documents
// that the bug class is now impossible rather than merely avoided. Anyone who
// reintroduces an origin test based on status, retry_count or a reason string
// has re-created exactly what this schema change was chosen to prevent.
func TestFindHeldRequest_OnlyMatchesHeldRows(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now()

	// An ordinary, live retry row for the same movie — no hold.
	if _, err := s.Create(ctx, Grab{
		Mode: mode.Movies, Title: "Contested", TMDBID: 950,
		Status: PendingRetry, RetryAfter: FormatTime(now.Add(24 * time.Hour)),
	}); err != nil {
		t.Fatalf("create unheld: %v", err)
	}
	if _, err := s.FindHeldRequest(ctx, mode.Movies, 950); !errors.Is(err, ErrNotFound) {
		t.Errorf("an unheld retry row must be unreachable from the held-request lookup, got %v", err)
	}

	// Scoping: a held row in another mode, and another tmdb id, are both misses.
	if _, err := s.Create(ctx, held(Grab{Mode: mode.Series, Title: "Other Mode", TMDBID: 950}, now)); err != nil {
		t.Fatalf("create other-mode: %v", err)
	}
	if _, err := s.FindHeldRequest(ctx, mode.Movies, 950); !errors.Is(err, ErrNotFound) {
		t.Errorf("the lookup is mode-scoped, got %v", err)
	}
	if _, err := s.FindHeldRequest(ctx, mode.Movies, 951); !errors.Is(err, ErrNotFound) {
		t.Errorf("the lookup is tmdb-scoped, got %v", err)
	}
}

// TestCreate_RefusesASecondHeldRequestForOneFilm proves the partial unique index
// idx_grabs_held_request (migration 0057) is real, at the DB layer, and that
// Create translates its violation into ErrHeldRequestExists.
//
// REWRITTEN — this test used to be TestFindHeldRequest_ReturnsTheOriginalRequest,
// which seeded TWO held rows for one (mode, tmdb_id) and asserted ORDER BY id ASC
// returned the earlier. That premise no longer holds: the index makes a second
// held row impossible to create, so the test's setup now fails at its own second
// Create. What it was really guarding — "a re-click always resolves to ONE
// deterministic row" — is now guaranteed by the constraint outright rather than
// by a tiebreak, so the test asserts the stronger property instead.
// FindHeldRequest keeps its ORDER BY id ASC as belt-and-braces; nothing depends
// on it any more.
//
// Why the constraint matters more than dedup tidiness: the route's step 2/step 3
// is a check-then-act, so two concurrent clicks can both miss FindHeldRequest and
// both reach Create. Nothing downstream could repair the result — nonHeldMovieWork
// self-excludes EVERY held row, not just the row being evaluated, so promotion
// cannot tell two held rows apart and both would dispatch the same film.
func TestCreate_RefusesASecondHeldRequestForOneFilm(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now()

	first, err := s.Create(ctx, held(Grab{Mode: mode.Movies, Title: "Dupe", TMDBID: 960}, now))
	if err != nil {
		t.Fatalf("create first: %v", err)
	}

	_, err = s.Create(ctx, held(Grab{Mode: mode.Movies, Title: "Dupe", TMDBID: 960}, now.Add(time.Hour)))
	if !errors.Is(err, ErrHeldRequestExists) {
		t.Fatalf("a second held row for one (mode, tmdb_id) must be refused with ErrHeldRequestExists, got %v", err)
	}

	// The winner is untouched — the loser's date did not overwrite anything.
	found, err := s.FindHeldRequest(ctx, mode.Movies, 960)
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if found.ID != first.ID {
		t.Errorf("found id %d, want the surviving original (%d)", found.ID, first.ID)
	}
	if found.HoldUntil != FormatTime(now) {
		t.Errorf("hold_until = %q, want the winner's %q — a refused insert must not mutate the existing row", found.HoldUntil, FormatTime(now))
	}
}

// TestCreate_HeldRequestUniquenessIsScopedAndPartial pins the two ways the index
// must NOT over-reach. Both are load-bearing: a total unique index on
// (mode, tmdb_id) would break ordinary grabs outright, and a tmdb_id-only one
// would make a Movies request block a Series one.
func TestCreate_HeldRequestUniquenessIsScopedAndPartial(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now()

	// PARTIAL: unheld grabs repeat a (mode, tmdb_id) pair all the time — a
	// re-grab, or a retry that minted a fresh row.
	ordinary := Grab{
		Mode: mode.Movies, Title: "Ordinary", TMDBID: 961,
		Indexer: "I", Protocol: "torrent", DownloadClient: "anacrolix", RootFolderPath: "/movies",
	}
	for i := range 2 {
		if _, err := s.Create(ctx, ordinary); err != nil {
			t.Fatalf("unheld grab %d must be allowed to repeat a (mode, tmdb_id) pair: %v", i, err)
		}
	}

	// MODE-SCOPED: the same tmdb id held in another mode is a different request.
	if _, err := s.Create(ctx, held(Grab{Mode: mode.Movies, Title: "Shared Id", TMDBID: 962}, now)); err != nil {
		t.Fatalf("create movies held: %v", err)
	}
	if _, err := s.Create(ctx, held(Grab{Mode: mode.Series, Title: "Shared Id", TMDBID: 962}, now)); err != nil {
		t.Fatalf("a held row for the same tmdb id in another mode must be allowed: %v", err)
	}
}

// TestRearmHeldRequest_ResetsEveryGuardDueForReleaseChecks is the store half of
// the terminal-dead-end fix.
//
// SetHoldUntil writes hold_until and the reason and nothing else, which is
// exactly right for a row that is still promotable and exactly wrong for a Failed
// one: the Failed row keeps status=failed and its dead download_gid, so it fails
// two of DueForRelease's four guards permanently while the API reports success.
// RearmHeldRequest resets all three non-hold guards at once, and the assertion
// that matters is the last one — the row is actually RETURNED by DueForRelease.
func TestRearmHeldRequest_ResetsEveryGuardDueForReleaseChecks(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now()

	g, err := s.Create(ctx, held(Grab{Mode: mode.Movies, Title: "Taken Down", TMDBID: 970}, now.Add(-48*time.Hour)))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// Promote and dispatch, then take it down permanently — the real sequence.
	if err := s.Relaunch(ctx, g.ID, Dispatch{
		Indexer: "I", Protocol: "usenet", DownloadClient: "nntp",
		RootFolderPath: "/movies", DownloadURL: "https://x/1.nzb", GID: "gid-970",
	}); err != nil {
		t.Fatalf("relaunch: %v", err)
	}
	if err := s.UpdateStatus(ctx, g.ID, Failed); err != nil {
		t.Fatalf("update status: %v", err)
	}
	before, err := s.Get(ctx, g.ID)
	if err != nil {
		t.Fatalf("get before: %v", err)
	}
	if before.DownloadGID == "" {
		t.Fatal("precondition: a Failed row keeps its dead download_gid — UpdateStatus writes status only")
	}

	release := now.Add(-time.Hour) // already due, so DueForRelease should return it
	if err := s.RearmHeldRequest(ctx, g.ID, release, "held until its release date"); err != nil {
		t.Fatalf("rearm: %v", err)
	}

	got, err := s.Get(ctx, g.ID)
	if err != nil {
		t.Fatalf("get after: %v", err)
	}
	if got.Status != PendingRetry {
		t.Errorf("status = %q, want %q — a re-armed request has not been attempted since", got.Status, PendingRetry)
	}
	if got.DownloadGID != "" {
		t.Errorf("download_gid = %q, want cleared — a non-empty GID means 'already dispatched, do not promote'", got.DownloadGID)
	}
	if got.RetryAfter != "" {
		t.Errorf("retry_after = %q, want cleared — the empty retry_after IS the hold", got.RetryAfter)
	}
	if got.HoldUntil != FormatTime(release) {
		t.Errorf("hold_until = %q, want %q", got.HoldUntil, FormatTime(release))
	}
	if got.RetryCount != before.RetryCount {
		t.Errorf("retry_count = %d, want %d kept — it is this request's attempt history, and a re-click is not an attempt", got.RetryCount, before.RetryCount)
	}

	// THE POINT: the row is promotable again, not a silent dead end.
	due, err := s.DueForRelease(ctx, now)
	if err != nil {
		t.Fatalf("due for release: %v", err)
	}
	var found bool
	for _, d := range due {
		if d.ID == g.ID {
			found = true
		}
	}
	if !found {
		t.Errorf("a re-armed request must be returned by DueForRelease, got %+v", due)
	}
}

// TestRearmHeldRequest_RefusesEveryNonFailedStatus proves the WHERE clause's
// status guard, which is what stops a re-arm stranding a LIVE download: clearing
// a Queued/Downloading row's GID while the downloader still owns it would leave
// the row promotable and dispatch a second copy of the same film.
//
// Completed/Imported are refused for the adjacent reason — the film was already
// delivered, so re-arming would re-download something the operator has. Only
// Failed is genuinely inert.
func TestRearmHeldRequest_RefusesEveryNonFailedStatus(t *testing.T) {
	ctx := context.Background()
	now := time.Now()

	for i, status := range []Status{Queued, Downloading, Completed, Imported, PendingRetry} {
		t.Run(string(status), func(t *testing.T) {
			s := newTestStore(t)
			g, err := s.Create(ctx, held(Grab{Mode: mode.Movies, Title: "Live", TMDBID: 980 + i}, now))
			if err != nil {
				t.Fatalf("create: %v", err)
			}
			if err := s.SetDownloadGID(ctx, g.ID, "gid-live"); err != nil {
				t.Fatalf("set gid: %v", err)
			}
			if err := s.UpdateStatus(ctx, g.ID, status); err != nil {
				t.Fatalf("update status: %v", err)
			}

			if err := s.RearmHeldRequest(ctx, g.ID, now.Add(24*time.Hour), "x"); !errors.Is(err, ErrNotFound) {
				t.Fatalf("re-arming a %s row must be refused, got %v", status, err)
			}
			got, err := s.Get(ctx, g.ID)
			if err != nil {
				t.Fatalf("get: %v", err)
			}
			if got.Status != status {
				t.Errorf("status = %q, want %q untouched by the refused re-arm", got.Status, status)
			}
			if got.DownloadGID != "gid-live" {
				t.Errorf("download_gid = %q, want it untouched — clearing a live download's GID is the harm this guard prevents", got.DownloadGID)
			}
		})
	}
}

// TestRearmHeldRequest_RefusesANonHeldRow keeps the method scoped to this
// feature: a Failed ORDINARY grab (a takedown on a Discover grab, say) is not a
// pre-release request and must not be resurrected into one. The hold_until != ''
// conjunct is the origin predicate doing that work, the same one FindHeldRequest
// relies on.
func TestRearmHeldRequest_RefusesANonHeldRow(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	g, err := s.Create(ctx, Grab{
		Mode: mode.Movies, Title: "Ordinary", TMDBID: 990,
		Indexer: "I", Protocol: "torrent", DownloadClient: "anacrolix", RootFolderPath: "/movies",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := s.UpdateStatus(ctx, g.ID, Failed); err != nil {
		t.Fatalf("update status: %v", err)
	}

	if err := s.RearmHeldRequest(ctx, g.ID, time.Now(), "x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("re-arming a non-held row must be refused, got %v", err)
	}
	got, err := s.Get(ctx, g.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status != Failed || got.HoldUntil != "" {
		t.Errorf("the ordinary row must be untouched, got status %q hold_until %q", got.Status, got.HoldUntil)
	}
}

func TestRearmHeldRequest_ErrNotFound(t *testing.T) {
	s := newTestStore(t)
	if err := s.RearmHeldRequest(context.Background(), 404, time.Now(), "x"); !errors.Is(err, ErrNotFound) {
		t.Errorf("want ErrNotFound, got %v", err)
	}
}

// --- monitor_entity_key round-trip ---

func TestSetMonitorEntityKey_RoundTrips(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	g, err := s.Create(ctx, Grab{
		Mode: mode.Adult, Title: "A Scene", TMDBID: 0,
		Indexer: "I", Protocol: "usenet", DownloadClient: "nntp", RootFolderPath: "/adult",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if g.MonitorEntityKey != "" {
		t.Errorf("MonitorEntityKey should be empty on create; got %q", g.MonitorEntityKey)
	}

	key := "performer\x1ftpdb\x1fabc123"
	if err := s.SetMonitorEntityKey(ctx, g.ID, key); err != nil {
		t.Fatalf("SetMonitorEntityKey: %v", err)
	}

	got, err := s.Get(ctx, g.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.MonitorEntityKey != key {
		t.Errorf("MonitorEntityKey round-trip: got %q; want %q", got.MonitorEntityKey, key)
	}
}

func TestSetMonitorEntityKey_ErrNotFound(t *testing.T) {
	s := newTestStore(t)
	if err := s.SetMonitorEntityKey(context.Background(), 999999, "k"); !errors.Is(err, ErrNotFound) {
		t.Errorf("want ErrNotFound for missing grab; got %v", err)
	}
}

func TestMonitorEntityKey_AppearsInList(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	g, err := s.Create(ctx, Grab{
		Mode: mode.Adult, Title: "Listed Scene", TMDBID: 0,
		Indexer: "I", Protocol: "usenet", DownloadClient: "nntp", RootFolderPath: "/adult",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	key := "studio\x1ffansdb\x1fstudio-id"
	if err := s.SetMonitorEntityKey(ctx, g.ID, key); err != nil {
		t.Fatalf("SetMonitorEntityKey: %v", err)
	}

	list, err := s.List(ctx, mode.Adult)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, row := range list {
		if row.ID == g.ID {
			if row.MonitorEntityKey != key {
				t.Errorf("List MonitorEntityKey: got %q; want %q", row.MonitorEntityKey, key)
			}
			return
		}
	}
	t.Fatalf("grab %d not found in List", g.ID)
}

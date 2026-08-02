package api

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"testing"
	"time"

	"github.com/labbersanon/sakms/internal/downloader"
	"github.com/labbersanon/sakms/internal/excludes"
	"github.com/labbersanon/sakms/internal/grabs"
	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/quality"
	"github.com/labbersanon/sakms/internal/settings"
)

// preReleaseEnv is one fully wired promotion-pass fixture: real stores, a fake
// TMDB with a real runtime (the bitrate scorer's denominator) and a
// call-counting fake Prowlarr, so a test can assert not just what happened but
// whether an indexer was ever touched.
type preReleaseEnv struct {
	grabs    *grabs.Store
	lib      *library.Store
	settings *settings.Store
	deps     AutoGrabDeps
	build    sessionBuilderFunc
	dl       *downloader.Manager
	prowlarr *prowlarrStats
}

func newPreReleaseEnv(t *testing.T, prowlarrBody string) *preReleaseEnv {
	t.Helper()
	ctx := context.Background()
	connStore, _, _, settingsStore, grabsStore, libStore, _, _, _, _, _, scStore := testStoresWithRegistry(t)
	dl := newTestDownloader("gid-prerelease", t.TempDir())
	tmdbSrv := fakeTMDBMovieRuntime(t, 100) // 6000 s runtime → a release can grade
	prowlarrSrv, stats := fakeProwlarrTracking(t, 0, func(url.Values) (int, string) {
		return 200, prowlarrBody
	})

	overrideFixedURL(t, "tmdb", tmdbSrv.URL)
	for _, c := range []struct{ service, url string }{{"tmdb", tmdbSrv.URL}, {"prowlarr", prowlarrSrv.URL}} {
		if err := connStore.Upsert(ctx, c.service, c.url, "key"); err != nil {
			t.Fatalf("upserting %s: %v", c.service, err)
		}
	}
	if err := settingsStore.Set(ctx, qualityTierKey(mode.Movies), string(quality.Low)); err != nil {
		t.Fatalf("setting the quality tier: %v", err)
	}
	if err := settingsStore.Set(ctx, moviesLibraryRootFolderKey, "/movies"); err != nil {
		t.Fatalf("setting the movies root folder: %v", err)
	}
	setAutoGrabToggle(t, settingsStore, true)

	env := &preReleaseEnv{
		grabs: grabsStore, lib: libStore, settings: settingsStore, dl: dl,
		prowlarr: stats,
		deps:     AutoGrabDeps{SettingsStore: settingsStore, GrabsStore: grabsStore},
	}
	env.build = func(ctx context.Context, m mode.Mode) (*mode.Session, error) {
		return mode.Build(ctx, connStore, scStore, settingsStore, testHTTPClient(), dl, m)
	}
	return env
}

// heldRow mints a held pre-release request through the SAME function the route
// uses — never a hand-seeded fixture. A hand-seeded row would pass even if
// grabs.Create silently dropped hold_until from its INSERT, which is precisely
// the failure that makes this whole feature inert while reporting success.
func heldRow(t *testing.T, grabsStore *grabs.Store, title string, tmdbID int, until time.Time) grabs.Grab {
	t.Helper()
	g, err := parkPreReleaseRequest(context.Background(), grabsStore, mode.Movies, title, tmdbID, until)
	if err != nil {
		t.Fatalf("parking a pre-release request: %v", err)
	}
	if g.HoldUntil == "" {
		t.Fatalf("the held row came back with an empty hold_until — grabs.Create is not persisting the column, and every held row is invisible to BOTH DueForRetry and DueForRelease")
	}
	return g
}

// TestReleaseDueGrabsReturnsImmediatelyWithoutALibraryStore: runUsenetRetryCycle
// documents libStore as nillable and six existing tests pass nil. This pass
// needs it for the duplicate-suppression check, so it must return before it
// dereferences anything — a guard that is vacuously true today (no nil-libStore
// fixture has a held row) and becomes a real nil deref the moment one grows one.
func TestReleaseDueGrabsReturnsImmediatelyWithoutALibraryStore(t *testing.T) {
	ctx := context.Background()
	env := newPreReleaseEnv(t, healthyMovieRelease)
	g := heldRow(t, env.grabs, "Due Film", 42, time.Now().Add(-time.Hour))

	failIfBuilt := func(context.Context, mode.Mode) (*mode.Session, error) {
		t.Fatal("a nil library store must stop the pass before it builds a session")
		return nil, nil
	}
	runUsenetRetryCycle(ctx, env.deps, failIfBuilt, nil, nil, nil, time.Now())

	after, err := env.grabs.Get(ctx, g.ID)
	if err != nil {
		t.Fatalf("reloading: %v", err)
	}
	if after.RetryAfter != "" || after.Status != grabs.PendingRetry || after.HoldUntil != g.HoldUntil {
		t.Errorf("the row was modified by a pass that should have returned immediately: %+v", after)
	}
}

// TestReleaseDueGrabsIsGatedByTheToggle is T-3.6, the staged-for-approval
// assertion as executable code: with usenet_autograb_enabled off, a due held row
// produces Gated and NOTHING is searched, scored, dispatched or recorded.
//
// It also proves the thing this task must not get wrong — TriggerPreRelease
// reaches RunAutoGrab's ONE existing gate with no gating path of its own. The
// zero-Prowlarr-call assertion is what makes that observable: the gate returns
// before the search, so any second gating path (or a missing one) shows up here.
func TestReleaseDueGrabsIsGatedByTheToggle(t *testing.T) {
	ctx := context.Background()
	env := newPreReleaseEnv(t, healthyMovieRelease)
	setAutoGrabToggle(t, env.settings, false)
	g := heldRow(t, env.grabs, "Due Film", 42, time.Now().Add(-time.Hour))

	releaseDueGrabs(ctx, env.deps, env.build, env.lib, nil, time.Now())

	if total, _ := env.prowlarr.snapshot(); total != 0 {
		t.Errorf("%d Prowlarr searches fired with the toggle off — a gated trigger must never reach an indexer", total)
	}
	if got := len(env.dl.List()); got != 0 {
		t.Errorf("%d downloads dispatched with the toggle off", got)
	}
	after, err := env.grabs.Get(ctx, g.ID)
	if err != nil {
		t.Fatalf("reloading: %v", err)
	}
	if after.RetryAfter != "" || after.HoldUntil != g.HoldUntil || after.Status != grabs.PendingRetry {
		t.Errorf("a gated row was modified: %+v", after)
	}
	// Still promotable: nothing happened, so a later cycle (after the operator
	// opts in) picks it up unchanged.
	due, err := env.grabs.DueForRelease(ctx, time.Now())
	if err != nil {
		t.Fatalf("listing due for release: %v", err)
	}
	if len(due) != 1 {
		t.Errorf("a gated row must stay promotable, got %+v", due)
	}
}

// TestReleaseDueGrabsPromotesTheSameRow is T-3.5 plus T-3.2c's successful half.
// Three properties in one cycle:
//
//   - the promoted row is RE-ARMED (ExistingGrabID), never duplicated — a second
//     row would leave the first still promotable and, since AddNZB mints a fresh
//     GID per call, the GID dedup guard could not catch the duplicate download;
//   - hold_until SURVIVES dispatch as inert provenance, so a later click still
//     finds this request;
//   - promotion is IDEMPOTENT: with a GID set, DueForRelease can never return
//     the row again. That is what lets hold_until stay set forever.
func TestReleaseDueGrabsPromotesTheSameRow(t *testing.T) {
	ctx := context.Background()
	env := newPreReleaseEnv(t, healthyMovieRelease)
	g := heldRow(t, env.grabs, "Some Movie", 42, time.Now().Add(-time.Hour))

	runUsenetRetryCycle(ctx, env.deps, env.build, nil, env.lib, nil, time.Now())

	list, err := env.grabs.List(ctx, mode.Movies)
	if err != nil {
		t.Fatalf("listing grabs: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("promotion created a second row (%d rows) — the held one would stay promotable and dispatch again: %+v", len(list), list)
	}
	got := list[0]
	if got.ID != g.ID {
		t.Fatalf("the surviving row is a new one (id %d, want %d)", got.ID, g.ID)
	}
	if got.Status != grabs.Queued || got.DownloadGID == "" {
		t.Fatalf("the promoted row did not rejoin the normal lifecycle: %+v", got)
	}
	if got.HoldUntil != g.HoldUntil {
		t.Errorf("holdUntil = %q, want %q preserved — it is provenance and is never cleared", got.HoldUntil, g.HoldUntil)
	}
	if n := len(env.dl.List()); n != 1 {
		t.Errorf("expected exactly one download-client add, got %d", n)
	}
	due, err := env.grabs.DueForRelease(ctx, time.Now())
	if err != nil {
		t.Fatalf("listing due for release: %v", err)
	}
	if len(due) != 0 {
		t.Errorf("a dispatched row is STILL due for release — the next cycle would grab it again: %+v", due)
	}
}

// TestReleaseDueGrabsPromotionIsIdempotentAfterANoMatch is T-3.2c's other half.
// A promotion attempt that finds nothing leaves a real retry_after behind
// (parkPendingRetry), and DueForRelease's retry_after = ” guard is what turns
// that into "promotion fires exactly once". The request is not lost: it has
// joined the ordinary retry track, which is where a released film belongs.
func TestReleaseDueGrabsPromotionIsIdempotentAfterANoMatch(t *testing.T) {
	ctx := context.Background()
	env := newPreReleaseEnv(t, `[]`) // nothing to score
	g := heldRow(t, env.grabs, "Some Movie", 42, time.Now().Add(-time.Hour))

	releaseDueGrabs(ctx, env.deps, env.build, env.lib, nil, time.Now())

	after, err := env.grabs.Get(ctx, g.ID)
	if err != nil {
		t.Fatalf("reloading: %v", err)
	}
	if after.RetryAfter == "" {
		t.Fatal("a no-match promotion left the row unparked — it would be promoted again every single cycle")
	}
	if after.HoldUntil != g.HoldUntil {
		t.Errorf("holdUntil = %q, want %q preserved", after.HoldUntil, g.HoldUntil)
	}
	due, err := env.grabs.DueForRelease(ctx, time.Now())
	if err != nil {
		t.Fatalf("listing due for release: %v", err)
	}
	if len(due) != 0 {
		t.Errorf("the row is still due for release after an attempt: %+v", due)
	}
	// It now belongs to the ordinary retry track.
	retryDue, err := env.grabs.DueForRetry(ctx, time.Now().Add(48*time.Hour))
	if err != nil {
		t.Fatalf("listing due for retry: %v", err)
	}
	if len(retryDue) != 1 || retryDue[0].ID != g.ID {
		t.Errorf("a promoted row must rejoin the normal retry track, got %+v", retryDue)
	}
}

// TestReleaseDueGrabsParksAnAlreadyGrabbingRow pins the branch that had no
// state write at all before 2026-08-02.
//
// The trace it closes: the promoted row's dispatch returns a GID some OTHER
// in-flight grab already holds, so RunAutoGrab reports AlreadyGrabbing and
// records nothing. Every other terminal branch of this switch (err, Gated,
// Grabbed, NoMatch) leaves the row in a state that ends its promotion
// eligibility; a bare log leaves retry_after empty, which is one of
// DueForRelease's four guards — so the SAME row comes back every single cycle,
// forever, permanently consuming one of the twenty per-cycle cap slots. Twenty
// such rows halt the whole feature.
//
// Fixture shape, and the part that makes this test non-vacuous: the decoy grab
// carries a DIFFERENT TMDB id from the held row. Sharing one would make
// nonHeldMovieWork suppress the held row at the pre-filter, so it would never
// reach RunAutoGrab at all and the retry_after assertion below would pass for
// entirely the wrong reason. The Prowlarr assertion is what proves the row got
// as far as a real dispatch attempt.
func TestReleaseDueGrabsParksAnAlreadyGrabbingRow(t *testing.T) {
	ctx := context.Background()
	env := newPreReleaseEnv(t, healthyMovieRelease)

	// The decoy: an active, non-held grab already holding the GID the test
	// downloader hands back for every add.
	if _, err := env.grabs.Create(ctx, grabs.Grab{
		Mode: mode.Movies, Title: "Unrelated Film", TMDBID: 99,
		Indexer: "I", Protocol: "torrent", DownloadClient: "torrent",
		RootFolderPath: "/movies", DownloadURL: "magnet:?xt=x",
		DownloadGID: "gid-prerelease",
	}); err != nil {
		t.Fatalf("seeding the in-flight decoy grab: %v", err)
	}
	g := heldRow(t, env.grabs, "Some Movie", 42, time.Now().Add(-time.Hour))

	releaseDueGrabs(ctx, env.deps, env.build, env.lib, nil, time.Now())

	if total, _ := env.prowlarr.snapshot(); total == 0 {
		t.Fatal("no Prowlarr search fired — the row never reached a dispatch attempt, so this test is not exercising the AlreadyGrabbing branch at all")
	}
	after, err := env.grabs.Get(ctx, g.ID)
	if err != nil {
		t.Fatalf("reloading: %v", err)
	}
	if after.RetryAfter == "" {
		t.Error("an AlreadyGrabbing promotion left the row unparked — DueForRelease will return it every cycle forever, permanently consuming one of the cap's twenty slots")
	}
	if after.HoldUntil != g.HoldUntil {
		t.Errorf("holdUntil = %q, want %q preserved", after.HoldUntil, g.HoldUntil)
	}
	due, err := env.grabs.DueForRelease(ctx, time.Now())
	if err != nil {
		t.Fatalf("listing due for release: %v", err)
	}
	if len(due) != 0 {
		t.Errorf("the row is still due for release after an AlreadyGrabbing attempt: %+v", due)
	}
	// The redundant row must not have been duplicated either: exactly the decoy
	// and the promoted row survive.
	list, err := env.grabs.List(ctx, mode.Movies)
	if err != nil {
		t.Fatalf("listing grabs: %v", err)
	}
	if len(list) != 2 {
		t.Errorf("expected exactly the decoy and the held row, got %d: %+v", len(list), list)
	}
}

// TestReleaseDueGrabsSuppressesADuplicate is T-3.4 / §5.3(b): a hold lasts
// months, and in that time the operator may simply get the film another way.
// Promoting then would buy a second copy — a direct hit on the mission's "no
// duplicates" bar.
//
// The termination must carry a REASON. grabs.Failed means "this release was
// taken down" everywhere else in this app, so a bare status flip reads to an
// operator as a DMCA takedown of a film they already own.
func TestReleaseDueGrabsSuppressesADuplicate(t *testing.T) {
	for _, tc := range []struct {
		name string
		seed func(t *testing.T, env *preReleaseEnv)
	}{
		{
			name: "the film is now tracked in the library",
			seed: func(t *testing.T, env *preReleaseEnv) {
				if _, err := env.lib.Upsert(context.Background(), library.Item{
					Mode: mode.Movies, TMDBID: 42, Title: "Some Movie",
					FilePath: "/movies/42.mkv", RootFolderPath: "/movies",
				}); err != nil {
					t.Fatalf("seeding the library: %v", err)
				}
			},
		},
		{
			name: "the film already has an active, non-held grab",
			seed: func(t *testing.T, env *preReleaseEnv) {
				if _, err := env.grabs.Create(context.Background(), grabs.Grab{
					Mode: mode.Movies, Title: "Some Movie", TMDBID: 42,
					Indexer: "I", Protocol: "usenet", DownloadClient: "usenet",
					RootFolderPath: "/movies", DownloadURL: "https://x/nzb",
				}); err != nil {
					t.Fatalf("seeding the grab: %v", err)
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			env := newPreReleaseEnv(t, healthyMovieRelease)
			g := heldRow(t, env.grabs, "Some Movie", 42, time.Now().Add(-time.Hour))
			tc.seed(t, env)

			releaseDueGrabs(ctx, env.deps, env.build, env.lib, nil, time.Now())

			if total, _ := env.prowlarr.snapshot(); total != 0 {
				t.Errorf("%d Prowlarr searches fired for a suppressed row — the pre-filter runs before any dispatch attempt", total)
			}
			if n := len(env.dl.List()); n != 0 {
				t.Errorf("%d downloads dispatched for a title the operator already has", n)
			}
			after, err := env.grabs.Get(ctx, g.ID)
			if err != nil {
				t.Fatalf("reloading: %v", err)
			}
			if after.Status != grabs.Failed {
				t.Errorf("status = %q, want %q — a superseded request must be terminated, not left promotable forever", after.Status, grabs.Failed)
			}
			if after.RetryReason != preReleaseSupersededReason {
				t.Errorf("retryReason = %q, want %q — a bare Failed flip with no explanation reads as a DMCA takedown", after.RetryReason, preReleaseSupersededReason)
			}
			due, err := env.grabs.DueForRelease(ctx, time.Now())
			if err != nil {
				t.Fatalf("listing due for release: %v", err)
			}
			if len(due) != 0 {
				t.Errorf("a terminated row is still due for release: %+v", due)
			}
		})
	}
}

// TestReleaseDueGrabsCapsDispatchesPerCycle is T-3.6b / M-F. The moment this
// bounds is real: while the toggle is off the cycle never runs at all, so held
// rows accumulate for as long as an operator keeps click-requesting. The first
// cycle after the toggle is switched on would otherwise dispatch the whole
// backlog at once.
//
// A failing session build stands in for a dispatch attempt because it is
// countable and costs no fixture — and it is legitimately an attempt: it
// re-parks the row, which ends that row's promotion just as a real search does.
func TestReleaseDueGrabsCapsDispatchesPerCycle(t *testing.T) {
	ctx := context.Background()
	env := newPreReleaseEnv(t, healthyMovieRelease)
	const rows = maxPreReleaseGrabsPerCycle + 5
	for i := 0; i < rows; i++ {
		heldRow(t, env.grabs, fmt.Sprintf("Film %02d", i), 1000+i, time.Now().Add(-time.Hour))
	}

	attempts := 0
	failing := func(context.Context, mode.Mode) (*mode.Session, error) {
		attempts++
		return nil, fmt.Errorf("prowlarr isn't configured")
	}
	releaseDueGrabs(ctx, env.deps, failing, env.lib, nil, time.Now())

	if attempts != maxPreReleaseGrabsPerCycle {
		t.Errorf("%d dispatch attempts in one cycle, want the cap of %d", attempts, maxPreReleaseGrabsPerCycle)
	}
	due, err := env.grabs.DueForRelease(ctx, time.Now())
	if err != nil {
		t.Fatalf("listing due for release: %v", err)
	}
	if len(due) != rows-maxPreReleaseGrabsPerCycle {
		t.Errorf("%d rows still held, want %d — the remainder must survive for the next cycle", len(due), rows-maxPreReleaseGrabsPerCycle)
	}
}

// TestReleaseDueGrabsSkipsDoNotConsumeBudget is the other half of the cap's
// contract, and the easier one to get wrong: the cap exists to bound PROWLARR
// SEARCHES, so a row skipped for exclusion or suppression must cost nothing.
// Checking the counter at the top of the loop instead of after the skips would
// let 20 excluded rows starve every real request in the queue — silently, since
// every other cap assertion would still pass.
func TestReleaseDueGrabsSkipsDoNotConsumeBudget(t *testing.T) {
	ctx := context.Background()
	env := newPreReleaseEnv(t, healthyMovieRelease)

	// 25 skippable rows: excluded titles and already-tracked ones, interleaved
	// so neither kind can be dismissed as an ordering artifact.
	excluded := map[string]bool{}
	for i := 0; i < 25; i++ {
		title, tmdbID := fmt.Sprintf("Skipped %02d", i), 2000+i
		heldRow(t, env.grabs, title, tmdbID, time.Now().Add(-time.Hour))
		if i%2 == 0 {
			excluded[excludes.Key(string(mode.Movies), tmdbID, title)] = true
			continue
		}
		if _, err := env.lib.Upsert(ctx, library.Item{
			Mode: mode.Movies, TMDBID: tmdbID, Title: title,
			FilePath: fmt.Sprintf("/movies/%d.mkv", tmdbID), RootFolderPath: "/movies",
		}); err != nil {
			t.Fatalf("seeding the library: %v", err)
		}
	}
	// Three genuine requests behind them.
	const real = 3
	for i := 0; i < real; i++ {
		heldRow(t, env.grabs, fmt.Sprintf("Real %02d", i), 3000+i, time.Now().Add(-time.Hour))
	}

	attempts := 0
	failing := func(context.Context, mode.Mode) (*mode.Session, error) {
		attempts++
		return nil, fmt.Errorf("prowlarr isn't configured")
	}
	releaseDueGrabs(ctx, env.deps, failing, env.lib, excluded, time.Now())

	if attempts != real {
		t.Errorf("%d dispatch attempts, want %d — 25 skipped rows consumed the cycle's budget and starved the real requests", attempts, real)
	}
}

// TestParkPendingRetryPrefersTheExistingGrabID is T-3.2d, the C3 regression
// test, and it is the reason this task exists at all.
//
// The trace it pins, verbatim from the design:
//
//	H = a HELD pre-release row for tmdb_id N (retry_after '', hold_until future)
//	R = a separately Discover-grabbed row for the SAME N, at a HIGHER id
//
// R's retrieval fails, R comes due, its re-search finds nothing — and
// parkPendingRetry's FindPendingRetry lookup (keyed on mode+tmdb_id+season,
// ORDER BY id ASC) hands back H, the LOWER id. Without the ExistingGrabID
// preference, R's failure is written onto H: an unreleased film parked with a
// real retry_after, its reason and attempt count replaced with an unrelated
// grab's failure data.
//
// Reverting that preference makes the three field assertions below fail. The
// DueForRetry assertion keeps passing, because grabs.DueForRetry's independent
// hold conjunct also blocks the early search — which is exactly why the design
// calls for BOTH remedies and says neither is sufficient alone.
func TestParkPendingRetryPrefersTheExistingGrabID(t *testing.T) {
	ctx := context.Background()
	env := newPreReleaseEnv(t, `[]`) // R's re-search finds nothing

	h := heldRow(t, env.grabs, "Some Movie", 42, time.Now().Add(90*24*time.Hour))
	r, err := env.grabs.Create(ctx, grabs.Grab{
		Mode: mode.Movies, Title: "Some Movie", TMDBID: 42, RootFolderPath: "/movies",
		Status:      grabs.PendingRetry,
		RetryAfter:  grabs.FormatTime(time.Now().Add(-time.Hour)),
		RetryReason: articlesUnavailableReason,
	})
	if err != nil {
		t.Fatalf("seeding the separately-grabbed row: %v", err)
	}
	if r.ID <= h.ID {
		t.Fatalf("fixture is wrong: R (id %d) must sort after H (id %d) for FindPendingRetry's id ASC to prefer H", r.ID, h.ID)
	}

	runUsenetRetryCycle(ctx, env.deps, env.build, nil, env.lib, nil, time.Now())

	afterH, err := env.grabs.Get(ctx, h.ID)
	if err != nil {
		t.Fatalf("reloading H: %v", err)
	}
	if afterH.RetryAfter != "" {
		t.Errorf("H.retryAfter = %q, want empty — an unrelated grab's failure parked the held row, and the unreleased film would be searched before its release date", afterH.RetryAfter)
	}
	if afterH.RetryReason != heldRequestReason {
		t.Errorf("H.retryReason = %q, want %q — the held row is showing an unrelated grab's failure on the Requests screen", afterH.RetryReason, heldRequestReason)
	}
	if afterH.RetryCount != 0 {
		t.Errorf("H.retryCount = %d, want 0 — the held request has never been attempted", afterH.RetryCount)
	}
	if afterH.HoldUntil != h.HoldUntil {
		t.Errorf("H.holdUntil = %q, want %q", afterH.HoldUntil, h.HoldUntil)
	}
	due, err := env.grabs.DueForRetry(ctx, time.Now())
	if err != nil {
		t.Fatalf("listing due for retry: %v", err)
	}
	for _, g := range due {
		if g.ID == h.ID {
			t.Fatal("DueForRetry returned the held row — the hold is escapable")
		}
	}

	// R itself must have absorbed its own failure, which is the half that proves
	// the preference targeted the right row rather than simply skipping the write.
	afterR, err := env.grabs.Get(ctx, r.ID)
	if err != nil {
		t.Fatalf("reloading R: %v", err)
	}
	if afterR.RetryReason != noQualifyingCandidateReason || afterR.RetryCount != 1 {
		t.Errorf("R did not absorb its own failed re-search: %+v", afterR)
	}
	list, err := env.grabs.List(ctx, mode.Movies)
	if err != nil {
		t.Fatalf("listing grabs: %v", err)
	}
	if len(list) != 2 {
		t.Errorf("expected exactly the two seeded rows, got %d: %+v", len(list), list)
	}
}

// TestParkPendingRetryDoesNotCreateForAMissingExistingGrabID pins the other
// half of the ExistingGrabID preference: a caller that names a row which no
// longer exists gets an error, NOT a freshly minted row.
//
// Falling through to the Create arm here would be the same class of mistake the
// preference exists to close — the caller asked for one specific row, and
// quietly substituting a different one is exactly how a held request ends up
// with an unrelated grab's state. Both production callers read the id from the
// store microseconds earlier, so this path means something is genuinely wrong
// and should surface, not be papered over with a duplicate.
func TestParkPendingRetryDoesNotCreateForAMissingExistingGrabID(t *testing.T) {
	ctx := context.Background()
	env := newPreReleaseEnv(t, `[]`)

	_, err := parkPendingRetry(ctx, env.deps, AutoGrabRequest{
		Mode: mode.Movies, Title: "Gone", TMDBID: 99,
		ExistingGrabID: 4242, // never existed
	}, noQualifyingCandidateReason)
	if err == nil {
		t.Fatal("parking against a missing ExistingGrabID succeeded — it silently created a substitute row")
	}
	if !errors.Is(err, grabs.ErrNotFound) {
		t.Errorf("error = %v, want grabs.ErrNotFound", err)
	}
	list, err := env.grabs.List(ctx, mode.Movies)
	if err != nil {
		t.Fatalf("listing grabs: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("a row was created for a missing ExistingGrabID: %+v", list)
	}
}

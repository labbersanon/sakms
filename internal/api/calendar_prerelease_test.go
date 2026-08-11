package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/labbersanon/sakms/internal/apidto"
	"github.com/labbersanon/sakms/internal/grabs"
	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
)

// preReleaseMux wires POST /api/calendar/prerelease-request against real
// (empty unless a test seeds them) grab and library stores, and returns both so
// a test can seed the outstanding-work inputs and read the row back.
func preReleaseMux(t *testing.T) (*http.ServeMux, *grabs.Store, *library.Store) {
	t.Helper()
	_, _, _, grabsStore, libStore, _, _, _, _, _ := testStores(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/calendar/prerelease-request", preReleaseRequestHandler(grabsStore, libStore))
	return mux, grabsStore, libStore
}

func postPreRelease(t *testing.T, srvURL string, body apidto.PreReleaseRequestRequest) (int, apidto.PreReleaseRequestResponse) {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshalling request: %v", err)
	}
	resp, err := http.Post(srvURL+"/api/calendar/prerelease-request", "application/json", strings.NewReader(string(raw)))
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return resp.StatusCode, apidto.PreReleaseRequestResponse{}
	}
	var out apidto.PreReleaseRequestResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	return resp.StatusCode, out
}

// releaseDay is a fixed future date, far enough out that no test's wall clock
// can drift past it.
const releaseDay = "2099-06-15"

// TestPreReleaseRequest_CreatesAHeldRow is T-3.1: the row shape, field by
// field. The EMPTY RetryAfter is the load-bearing one — it is the entire hold,
// because grabs.DueForRetry filters on retry_after != ” and therefore cannot
// see this row at all.
func TestPreReleaseRequest_CreatesAHeldRow(t *testing.T) {
	ctx := context.Background()
	mux, grabsStore, _ := preReleaseMux(t)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	status, out := postPreRelease(t, srv.URL, apidto.PreReleaseRequestRequest{
		TMDBID: 42, Title: "Unreleased Film", ReleaseDate: releaseDay,
	})
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if out.AlreadyRequested {
		t.Error("a first request reported alreadyRequested")
	}
	if out.GrabID == 0 {
		t.Fatal("response carried no grab id")
	}

	g, err := grabsStore.Get(ctx, out.GrabID)
	if err != nil {
		t.Fatalf("reloading the held row: %v", err)
	}
	if g.Status != grabs.PendingRetry {
		t.Errorf("status = %q, want %q — a held row must be admitted by isActiveGrab so it shows on the Requests worklist", g.Status, grabs.PendingRetry)
	}
	if g.RetryAfter != "" {
		t.Errorf("retryAfter = %q, want empty — a non-empty retry_after is exactly what makes DueForRetry return the row and search an unreleased film", g.RetryAfter)
	}
	wantHold := grabs.FormatTime(time.Date(2099, 6, 15, 0, 0, 0, 0, time.UTC))
	if g.HoldUntil != wantHold {
		t.Errorf("holdUntil = %q, want %q (TMDB's bare date parsed as UTC midnight)", g.HoldUntil, wantHold)
	}
	if out.HeldUntil != wantHold {
		t.Errorf("response heldUntil = %q, want %q", out.HeldUntil, wantHold)
	}
	if g.DownloadGID != "" || g.TMDBID != 42 || g.Mode != mode.Movies {
		t.Errorf("unexpected row: %+v", g)
	}
	if g.RetryReason != heldRequestReason {
		t.Errorf("retryReason = %q, want %q (operator-facing copy only)", g.RetryReason, heldRequestReason)
	}
	if g.RetryCount != 0 {
		t.Errorf("retryCount = %d, want 0 — nothing has been attempted", g.RetryCount)
	}
}

// TestPreReleaseRequest_HeldRowIsInvisibleToDueForRetry is T-3.2, the single
// most important assertion in this feature: DueForRetry never returns a held
// row — not the day before the release date, and NOT THE DAY AFTER EITHER. The
// exclusion is unconditional (retry_after is empty), not time-dependent, which
// is a strictly stronger guarantee than a date comparison would give.
func TestPreReleaseRequest_HeldRowIsInvisibleToDueForRetry(t *testing.T) {
	ctx := context.Background()
	mux, grabsStore, _ := preReleaseMux(t)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	postPreRelease(t, srv.URL, apidto.PreReleaseRequestRequest{
		TMDBID: 42, Title: "Unreleased Film", ReleaseDate: releaseDay,
	})

	release := time.Date(2099, 6, 15, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name string
		now  time.Time
	}{
		{"the day before its release", release.Add(-24 * time.Hour)},
		{"the day after its release", release.Add(24 * time.Hour)},
		{"a decade after its release", release.Add(10 * 365 * 24 * time.Hour)},
	} {
		due, err := grabsStore.DueForRetry(ctx, tc.now)
		if err != nil {
			t.Fatalf("listing due grabs: %v", err)
		}
		if len(due) != 0 {
			t.Errorf("%s: DueForRetry returned the held row: %+v", tc.name, due)
		}
	}
}

// TestPreReleaseRequest_SecondClickRefreshesTheHold is T-3.3 + T-3.11′: a
// re-click updates hold_until in place and creates NO second row — and it
// leaves retry_count alone, i.e. it went through SetHoldUntil and not
// SetPendingRetry. A corrected TMDB release date is a real case; an inflated
// attempt count for a request that has never been searched is a lie.
func TestPreReleaseRequest_SecondClickRefreshesTheHold(t *testing.T) {
	ctx := context.Background()
	mux, grabsStore, _ := preReleaseMux(t)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	_, first := postPreRelease(t, srv.URL, apidto.PreReleaseRequestRequest{
		TMDBID: 42, Title: "Unreleased Film", ReleaseDate: releaseDay,
	})

	status, second := postPreRelease(t, srv.URL, apidto.PreReleaseRequestRequest{
		TMDBID: 42, Title: "Unreleased Film", ReleaseDate: "2099-08-01",
	})
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if !second.AlreadyRequested {
		t.Error("a re-click must report alreadyRequested")
	}
	if second.GrabID != first.GrabID {
		t.Errorf("re-click returned grab %d, want the original %d", second.GrabID, first.GrabID)
	}

	list, err := grabsStore.List(ctx, mode.Movies)
	if err != nil {
		t.Fatalf("listing grabs: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("a re-click minted a second row (%d rows) — the next cycle would dispatch a duplicate: %+v", len(list), list)
	}
	g := list[0]
	if want := grabs.FormatTime(time.Date(2099, 8, 1, 0, 0, 0, 0, time.UTC)); g.HoldUntil != want {
		t.Errorf("holdUntil = %q, want the refreshed %q", g.HoldUntil, want)
	}
	if g.RetryCount != 0 {
		t.Errorf("retryCount = %d, want 0 — a re-click is not an attempt, so SetHoldUntil (not SetPendingRetry) must be what wrote this", g.RetryCount)
	}
	if g.RetryAfter != "" {
		t.Errorf("retryAfter = %q, want empty — refreshing a hold must never park the row for retry", g.RetryAfter)
	}
}

// TestPreReleaseRequest_LeavesAnUnrelatedRetryRowUntouched is T-3.8, the M1
// regression test. It is kept precisely BECAUSE it is now trivially satisfiable:
// FindHeldRequest filters on hold_until != ”, so an unrelated live retry row
// cannot be reached by the dedup path at all. The test documents that the bug
// class is structurally impossible rather than merely avoided.
func TestPreReleaseRequest_LeavesAnUnrelatedRetryRowUntouched(t *testing.T) {
	ctx := context.Background()
	mux, grabsStore, _ := preReleaseMux(t)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	existing, err := grabsStore.Create(ctx, grabs.Grab{
		Mode: mode.Movies, Title: "Unreleased Film", TMDBID: 42,
		Status:      grabs.PendingRetry,
		RetryAfter:  grabs.FormatTime(time.Now().Add(24 * time.Hour)),
		RetryReason: noQualifyingCandidateReason,
	})
	if err != nil {
		t.Fatalf("seeding the unrelated retry row: %v", err)
	}

	status, out := postPreRelease(t, srv.URL, apidto.PreReleaseRequestRequest{
		TMDBID: 42, Title: "Unreleased Film", ReleaseDate: releaseDay,
	})
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	// The film already has outstanding work by another route, so the click is
	// refused rather than duplicated.
	if !out.AlreadyRequested {
		t.Error("a film already being requested must report alreadyRequested")
	}

	after, err := grabsStore.Get(ctx, existing.ID)
	if err != nil {
		t.Fatalf("reloading the unrelated row: %v", err)
	}
	if after.RetryAfter != existing.RetryAfter || after.RetryReason != existing.RetryReason ||
		after.RetryCount != existing.RetryCount || after.HoldUntil != "" || after.Status != existing.Status {
		t.Errorf("the click rewrote an unrelated live retry row — a real outstanding request would be silently suspended.\nbefore: %+v\nafter:  %+v", existing, after)
	}
	list, err := grabsStore.List(ctx, mode.Movies)
	if err != nil {
		t.Fatalf("listing grabs: %v", err)
	}
	if len(list) != 1 {
		t.Errorf("expected no new row, got %d: %+v", len(list), list)
	}
}

// TestPreReleaseRequest_OutstandingWorkIsNeverDuplicated is T-3.10, the C1
// regression test, across every shape of "already handled" — including the two
// a status-scoped dedup query would miss (a promoted-and-dispatched held row,
// and an imported grab).
func TestPreReleaseRequest_OutstandingWorkIsNeverDuplicated(t *testing.T) {
	for _, tc := range []struct {
		name string
		// seed returns the number of grabs rows it created.
		seed          func(t *testing.T, grabsStore *grabs.Store, libStore *library.Store) int
		wantSameGrabB bool // the response should name the seeded grab row
	}{
		{
			name: "a held row that already promoted and dispatched (queued, with a GID)",
			seed: func(t *testing.T, grabsStore *grabs.Store, libStore *library.Store) int {
				ctx := context.Background()
				g, err := grabsStore.Create(ctx, grabs.Grab{
					Mode: mode.Movies, Title: "Unreleased Film", TMDBID: 42,
					Status: grabs.PendingRetry, HoldUntil: grabs.FormatTime(time.Now().Add(-24 * time.Hour)),
				})
				if err != nil {
					t.Fatalf("seeding: %v", err)
				}
				if err := grabsStore.Relaunch(ctx, g.ID, grabs.Dispatch{
					Indexer: "I", Protocol: "usenet", DownloadClient: "usenet",
					RootFolderPath: "/m", DownloadURL: "https://x/nzb", GID: "nzb-1",
				}); err != nil {
					t.Fatalf("relaunching: %v", err)
				}
				return 1
			},
			wantSameGrabB: true,
		},
		{
			name: "an imported grab with no hold (the operator grabbed it from Discover)",
			seed: func(t *testing.T, grabsStore *grabs.Store, libStore *library.Store) int {
				ctx := context.Background()
				g, err := grabsStore.Create(ctx, grabs.Grab{
					Mode: mode.Movies, Title: "Unreleased Film", TMDBID: 42,
					Indexer: "I", Protocol: "usenet", DownloadClient: "usenet",
					RootFolderPath: "/m", DownloadURL: "https://x/nzb",
				})
				if err != nil {
					t.Fatalf("seeding: %v", err)
				}
				if err := grabsStore.UpdateStatus(ctx, g.ID, grabs.Imported); err != nil {
					t.Fatalf("importing: %v", err)
				}
				return 1
			},
		},
		{
			name: "the movie is already tracked in the library",
			seed: func(t *testing.T, grabsStore *grabs.Store, libStore *library.Store) int {
				if _, err := libStore.Upsert(context.Background(), library.Item{
					Mode: mode.Movies, TMDBID: 42, Title: "Unreleased Film",
					FilePath: "/m/42.mkv", RootFolderPath: "/m",
				}); err != nil {
					t.Fatalf("seeding the library: %v", err)
				}
				return 0
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			mux, grabsStore, libStore := preReleaseMux(t)
			srv := httptest.NewServer(mux)
			defer srv.Close()

			seeded := tc.seed(t, grabsStore, libStore)

			status, out := postPreRelease(t, srv.URL, apidto.PreReleaseRequestRequest{
				TMDBID: 42, Title: "Unreleased Film", ReleaseDate: releaseDay,
			})
			if status != http.StatusOK {
				t.Fatalf("status = %d, want 200", status)
			}
			if !out.AlreadyRequested {
				t.Error("expected alreadyRequested")
			}
			list, err := grabsStore.List(ctx, mode.Movies)
			if err != nil {
				t.Fatalf("listing grabs: %v", err)
			}
			if len(list) != seeded {
				t.Fatalf("expected no new row (%d seeded), got %d: %+v", seeded, len(list), list)
			}
			if tc.wantSameGrabB && out.GrabID == 0 {
				t.Error("a re-click on an existing held row must report its grab id")
			}
		})
	}
}

// TestPreReleaseRequest_AFailedGrabDoesNotCountAsOutstandingWork is the
// deliberate HOLE in the test case above, and it had no coverage of its own.
//
// nonHeldMovieWork admits only isActiveGrab(status) or Imported. Failed is
// excluded ON PURPOSE — a failed grab is a title the operator can no longer get
// by that route, and counting it as outstanding work would make the exclusion
// PERMANENT: every future click on that film would be swallowed as
// "already requested" and no held row could ever be minted again, for a film
// nobody has and nothing is fetching.
//
// The assertion that makes this non-vacuous is `!out.AlreadyRequested` together
// with the new row's hold_until. Adding grabs.Failed to nonHeldMovieWork's
// predicate flips both.
func TestPreReleaseRequest_AFailedGrabDoesNotCountAsOutstandingWork(t *testing.T) {
	ctx := context.Background()
	mux, grabsStore, _ := preReleaseMux(t)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// A non-held Discover grab for the same film that went terminally failed.
	failed, err := grabsStore.Create(ctx, grabs.Grab{
		Mode: mode.Movies, Title: "Unreleased Film", TMDBID: 42,
		Indexer: "I", Protocol: "usenet", DownloadClient: "usenet",
		RootFolderPath: "/m", DownloadURL: "https://x/nzb",
	})
	if err != nil {
		t.Fatalf("seeding the failed grab: %v", err)
	}
	if err := grabsStore.UpdateStatus(ctx, failed.ID, grabs.Failed); err != nil {
		t.Fatalf("failing the seeded grab: %v", err)
	}

	status, out := postPreRelease(t, srv.URL, apidto.PreReleaseRequestRequest{
		TMDBID: 42, Title: "Unreleased Film", ReleaseDate: releaseDay,
	})
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if out.AlreadyRequested {
		t.Fatal("a failed grab was treated as outstanding work — the film is suppressed permanently, and no click can ever request it again")
	}
	if out.GrabID == 0 || out.GrabID == failed.ID {
		t.Fatalf("grabId = %d — a fresh held row must be minted, not the failed one reported back", out.GrabID)
	}

	held, err := grabsStore.Get(ctx, out.GrabID)
	if err != nil {
		t.Fatalf("reloading the held row: %v", err)
	}
	if held.HoldUntil == "" || held.Status != grabs.PendingRetry || held.RetryAfter != "" {
		t.Errorf("the minted row is not a held pre-release request: %+v", held)
	}
	// The failed row is left exactly as it was — the new request is a second,
	// independent row, not a resurrection of it.
	afterFailed, err := grabsStore.Get(ctx, failed.ID)
	if err != nil {
		t.Fatalf("reloading the failed grab: %v", err)
	}
	if afterFailed.Status != grabs.Failed || afterFailed.HoldUntil != "" {
		t.Errorf("the failed grab was modified: %+v", afterFailed)
	}
}

// TestPreReleaseRequest_RejectsBadInput: the route validates before it writes.
// A malformed date matters most — grabs.FormatTime takes a time.Time, so an
// unparsed string would either be stored as-is (breaking every lexicographic
// hold comparison) or silently become the zero time, which is in the past and
// would promote the request on the very next cycle.
func TestPreReleaseRequest_RejectsBadInput(t *testing.T) {
	mux, grabsStore, _ := preReleaseMux(t)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	for _, tc := range []struct {
		name string
		body apidto.PreReleaseRequestRequest
	}{
		{"no tmdb id", apidto.PreReleaseRequestRequest{Title: "T", ReleaseDate: releaseDay}},
		{"no title", apidto.PreReleaseRequestRequest{TMDBID: 42, ReleaseDate: releaseDay}},
		{"no release date", apidto.PreReleaseRequestRequest{TMDBID: 42, Title: "T"}},
		{"a full timestamp rather than a bare date", apidto.PreReleaseRequestRequest{TMDBID: 42, Title: "T", ReleaseDate: "2099-06-15T00:00:00Z"}},
		{"nonsense", apidto.PreReleaseRequestRequest{TMDBID: 42, Title: "T", ReleaseDate: "soon"}},
	} {
		status, _ := postPreRelease(t, srv.URL, tc.body)
		if status != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", tc.name, status)
		}
	}
	list, err := grabsStore.List(context.Background(), mode.Movies)
	if err != nil {
		t.Fatalf("listing grabs: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("a rejected request wrote a row: %+v", list)
	}
}

// TestGrabStatusLabel is T-3.7′ — §5.6's table, all three rows.
//
// The middle case is the one a future change is most likely to get wrong:
// provenance does NOT override current state. A promoted row's hold_until stays
// set forever (it is never cleared, deliberately), so a label keyed on "has a
// hold_until" rather than "has a hold_until IN THE FUTURE" would report
// "Scheduled" for a row that is genuinely retrying right now.
func TestGrabStatusLabel(t *testing.T) {
	future := grabs.FormatTime(time.Now().Add(90 * 24 * time.Hour))
	past := grabs.FormatTime(time.Now().Add(-90 * 24 * time.Hour))

	for _, tc := range []struct {
		name string
		g    grabs.Grab
		want string
	}{
		{"a held request, hold_until in the future", grabs.Grab{Status: grabs.PendingRetry, HoldUntil: future}, "Scheduled"},
		{"a promoted request, hold_until in the past", grabs.Grab{Status: grabs.PendingRetry, HoldUntil: past}, "Pending Retry"},
		{"an ordinary retry row with no hold", grabs.Grab{Status: grabs.PendingRetry}, "Pending Retry"},
		{"a queued grab carrying inert hold provenance", grabs.Grab{Status: grabs.Queued, HoldUntil: past}, "Downloading"},
		{"an ordinary in-flight grab", grabs.Grab{Status: grabs.Downloading}, "Downloading"},
	} {
		if got := grabStatusLabel(tc.g); got != tc.want {
			t.Errorf("%s: label = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestRequestsHandlerRendersAHeldRowAsScheduled is T-4.5 + T-4.6 from the
// worklist's side: a held row reaches /api/requests through PASS 2 (grab-
// derived), never as a pass-1 library row — a film that is not out yet is in
// nobody's library — and it carries a non-empty holdUntil so the UI can render
// the date instead of a blank "Scheduled" chip.
func TestRequestsHandlerRendersAHeldRowAsScheduled(t *testing.T) {
	ctx := context.Background()
	grabsStore, libStore, excludesStore := requestsTestStores(t)

	held, err := parkPreReleaseRequest(ctx, grabsStore, mode.Movies, "Unreleased Film", 42,
		time.Now().Add(90*24*time.Hour))
	if err != nil {
		t.Fatalf("parking the held request: %v", err)
	}

	rec := httptest.NewRecorder()
	requestsHandler(grabsStore, libStore, excludesStore)(rec, httptest.NewRequest(http.MethodGet, "/api/requests", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %q", rec.Code, rec.Body.String())
	}
	var out apidto.RequestStatusResponse
	if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if len(out.Items) != 1 {
		t.Fatalf("expected exactly one worklist row, got %d: %+v", len(out.Items), out.Items)
	}
	item := out.Items[0]
	if item.GrabID != held.ID {
		t.Errorf("the row is not grab-derived (grabId %d, want %d) — AC8's 'Missing-status' wording is satisfiable only on the loose reading, via pass 2", item.GrabID, held.ID)
	}
	if item.Status != "Scheduled" {
		t.Errorf("status = %q, want %q — 'Pending Retry' on a film released in months reads as a repeated failure", item.Status, "Scheduled")
	}
	if item.HoldUntil != held.HoldUntil {
		t.Errorf("holdUntil = %q, want %q — without it the UI ships a Scheduled chip with a blank date", item.HoldUntil, held.HoldUntil)
	}
}

// TestPreReleaseRequest_ReClickResurrectsATerminallyFailedRequest reproduces the
// exact reachable sequence that used to make a request vanish while the API
// reported success — promote, terminal-fail, re-click — and asserts the outcome
// that actually matters: the row is picked up by a FUTURE DueForRelease call.
//
// The pre-fix failure, spelled out because "returns 200" hid it completely:
//
//  1. The held row promotes and dispatches. Relaunch writes status=queued and a
//     download_gid, and deliberately LEAVES hold_until alone (provenance outlives
//     the hold), so the row is still what FindHeldRequest returns.
//  2. The download takes a permanent 451/DMCA classification. UpdateStatus writes
//     status ONLY, so download_gid survives non-empty.
//  3. The operator re-clicks. Step 1 skips the row (nonHeldMovieWork self-excludes
//     every held row); step 2 finds it and called SetHoldUntil, which writes
//     hold_until and the reason and NOTHING else.
//
// The row was then status=failed + non-empty download_gid + empty retry_after +
// a future hold_until — failing TWO of DueForRelease's four guards, permanently.
// And FindHeldRequest's ORDER BY id ASC meant that same row kept winning for this
// TMDB id forever, so no later click could route around it either. The request
// was gone; the operator was told it was scheduled.
//
// The last assertion is the one that would have caught it. Checking only the
// status/GID/retry_after fields would pass against a future re-introduction that
// reset them but broke a different guard; asking DueForRelease directly tests the
// property the operator actually depends on.
func TestPreReleaseRequest_ReClickResurrectsATerminallyFailedRequest(t *testing.T) {
	ctx := context.Background()
	mux, grabsStore, _ := preReleaseMux(t)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// The original click.
	_, first := postPreRelease(t, srv.URL, apidto.PreReleaseRequestRequest{
		TMDBID: 42, Title: "Unreleased Film", ReleaseDate: releaseDay,
	})
	if first.GrabID == 0 {
		t.Fatal("the first click minted no row")
	}

	// Step 1 — it promotes and dispatches. Exactly what releaseDueGrabs does on a
	// successful promotion.
	if err := grabsStore.Relaunch(ctx, first.GrabID, grabs.Dispatch{
		Indexer: "I", Protocol: "usenet", DownloadClient: "nntp",
		RootFolderPath: "/movies", DownloadURL: "https://x/1.nzb", GID: "gid-42",
	}); err != nil {
		t.Fatalf("promoting/dispatching: %v", err)
	}

	// Step 2 — a terminal 451/DMCA failure. classifyDownloadState maps this
	// straight to failed and it is never re-searched.
	if err := grabsStore.UpdateStatus(ctx, first.GrabID, grabs.Failed); err != nil {
		t.Fatalf("failing the download: %v", err)
	}
	dead, err := grabsStore.Get(ctx, first.GrabID)
	if err != nil {
		t.Fatalf("reloading the failed row: %v", err)
	}
	if dead.DownloadGID == "" || dead.HoldUntil == "" {
		t.Fatalf("precondition: a failed row keeps its GID and its hold provenance, got %+v", dead)
	}

	// Step 3 — the operator, seeing the film still absent, clicks it again.
	status, second := postPreRelease(t, srv.URL, apidto.PreReleaseRequestRequest{
		TMDBID: 42, Title: "Unreleased Film", ReleaseDate: releaseDay,
	})
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if !second.AlreadyRequested || second.GrabID != first.GrabID {
		t.Errorf("re-click returned %+v, want alreadyRequested on the original row %d", second, first.GrabID)
	}

	// No second row: the index and FindHeldRequest both keep this to one.
	list, err := grabsStore.List(ctx, mode.Movies)
	if err != nil {
		t.Fatalf("listing grabs: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("want exactly one row, got %d: %+v", len(list), list)
	}

	g := list[0]
	if g.Status != grabs.PendingRetry {
		t.Errorf("status = %q, want %q — a re-click on a terminal row must resurrect it, not refresh a corpse", g.Status, grabs.PendingRetry)
	}
	if g.DownloadGID != "" {
		t.Errorf("downloadGid = %q, want cleared — DueForRelease requires an empty one", g.DownloadGID)
	}
	if g.RetryAfter != "" {
		t.Errorf("retryAfter = %q, want empty — the empty retry_after IS the hold", g.RetryAfter)
	}

	// THE ASSERTION THAT MATTERS: once the (refreshed) release date arrives, the
	// promotion pass actually returns this row. Pre-fix it never would again.
	afterRelease := time.Date(2099, 7, 1, 0, 0, 0, 0, time.UTC)
	due, err := grabsStore.DueForRelease(ctx, afterRelease)
	if err != nil {
		t.Fatalf("due for release: %v", err)
	}
	var found bool
	for _, d := range due {
		if d.ID == first.GrabID {
			found = true
		}
	}
	if !found {
		t.Fatalf("the re-clicked request must be promotable again, but DueForRelease returned %+v — the operator's request silently vanished into a row that will never be searched, scored or dispatched", due)
	}
}

// TestPreReleaseRequest_ConcurrentClicksMintExactlyOneHeldRow drives the real
// race: steps 2 and 3 are a check-then-act, so genuinely concurrent clicks (a
// double-click, or two tabs) can all miss FindHeldRequest and all reach Create.
//
// Nothing downstream could repair a duplicate. The shared nonHeldMovieWork
// predicate self-excludes EVERY held row rather than just the one being
// evaluated, so promotion-time suppression cannot tell two held rows apart —
// both would promote and dispatch, and one film would download twice. The
// partial unique index is the only thing standing in the way.
//
// Two assertions, and the second is as important as the first: exactly one row
// survives, AND no client saw a 500. A raw constraint error leaking to the
// operator would be a "failed" request that actually succeeded.
func TestPreReleaseRequest_ConcurrentClicksMintExactlyOneHeldRow(t *testing.T) {
	ctx := context.Background()
	mux, grabsStore, _ := preReleaseMux(t)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// The POST is inlined rather than reusing postPreRelease because that helper
	// calls t.Fatalf, and FailNow must come from the goroutine running the test —
	// from any other it only runs runtime.Goexit. Results are collected and
	// asserted after Wait instead.
	const clicks = 8
	type clickResult struct {
		status int
		id     int64
		err    error
	}
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		results []clickResult
	)
	body, err := json.Marshal(apidto.PreReleaseRequestRequest{
		TMDBID: 42, Title: "Unreleased Film", ReleaseDate: releaseDay,
	})
	if err != nil {
		t.Fatalf("marshalling request: %v", err)
	}
	start := make(chan struct{})
	for range clicks {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start // release them together, to actually overlap the check-then-act
			var res clickResult
			resp, err := http.Post(srv.URL+"/api/calendar/prerelease-request", "application/json", strings.NewReader(string(body)))
			if err != nil {
				res.err = err
			} else {
				defer resp.Body.Close()
				res.status = resp.StatusCode
				if resp.StatusCode == http.StatusOK {
					var out apidto.PreReleaseRequestResponse
					res.err = json.NewDecoder(resp.Body).Decode(&out)
					res.id = out.GrabID
				}
			}
			mu.Lock()
			defer mu.Unlock()
			results = append(results, res)
		}()
	}
	close(start)
	wg.Wait()

	ids := make([]int64, 0, len(results))
	for i, res := range results {
		if res.err != nil {
			t.Errorf("click %d failed: %v", i, res.err)
			continue
		}
		if res.status != http.StatusOK {
			t.Errorf("click %d got status %d, want 200 — a lost race is an already-requested answer, not a server error", i, res.status)
		}
		ids = append(ids, res.id)
	}

	list, err := grabsStore.List(ctx, mode.Movies)
	if err != nil {
		t.Fatalf("listing grabs: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("%d concurrent clicks minted %d held rows — every one of them would promote and dispatch the same film: %+v", clicks, len(list), list)
	}

	// Every client was told about the row that actually exists, not the one it
	// personally failed to insert.
	for i, id := range ids {
		if id != list[0].ID {
			t.Errorf("click %d was told grab id %d, want the surviving row %d", i, id, list[0].ID)
		}
	}
}

// TestParkPreReleaseRequest_LostRaceIsATypedError is the deterministic half of
// the test above: the concurrent test can only prove the invariant holds, and
// cannot guarantee it scheduled its goroutines so that the conflict branch was
// actually taken. This one drives that branch directly.
//
// errors.Is rather than a substring match: the handler branches on the sentinel,
// so it is the sentinel that must survive Create's wrapping.
func TestParkPreReleaseRequest_LostRaceIsATypedError(t *testing.T) {
	ctx := context.Background()
	_, grabsStore, _ := preReleaseMux(t)
	until := time.Date(2099, 6, 15, 0, 0, 0, 0, time.UTC)

	if _, err := parkPreReleaseRequest(ctx, grabsStore, mode.Movies, "Unreleased Film", 42, until); err != nil {
		t.Fatalf("first park: %v", err)
	}
	_, err := parkPreReleaseRequest(ctx, grabsStore, mode.Movies, "Unreleased Film", 42, until)
	if !errors.Is(err, grabs.ErrHeldRequestExists) {
		t.Fatalf("a second held row for one film must fail with ErrHeldRequestExists, got %v", err)
	}
}

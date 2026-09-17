package api

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/labbersanon/sakms/internal/grabs"
	"github.com/labbersanon/sakms/internal/usenet"
)

// usenetErrorDeps pulls the two stores handleUsenetError needs out of
// testStores' ten-value fixture. The grabs store is returned separately so a
// test can read back the row it just parked.
func usenetErrorDeps(t *testing.T) (AutoGrabDeps, *grabs.Store) {
	t.Helper()
	_, _, settingsStore, grabsStore, _, _, _, _, _, _ := testStores(t)
	return AutoGrabDeps{SettingsStore: settingsStore, GrabsStore: grabsStore}, grabsStore
}

func TestHandleUsenetError_ClassifiesFailures(t *testing.T) {
	for _, tc := range []struct {
		name       string
		failure    error
		wantStatus grabs.Status
		wantReason string
	}{
		{
			name:       "430 parks for retry",
			failure:    fmt.Errorf("segment: %w", usenet.ErrArticleNotFound),
			wantStatus: grabs.PendingRetry,
			wantReason: articlesUnavailableReason,
		},
		{
			name:       "unclassified parks for retry",
			failure:    errors.New("dial timeout"),
			wantStatus: grabs.PendingRetry,
			wantReason: retrievalFailedReason,
		},
		{
			name:       "451 fails permanently",
			failure:    fmt.Errorf("segment: %w", usenet.ErrArticleRemoved),
			wantStatus: grabs.Failed,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			deps, grabsStore := usenetErrorDeps(t)
			g := dispatchedUsenetGrab(t, grabsStore, "nzb-err-1")

			handleUsenetError(ctx, deps, "nzb-err-1", tc.failure, parkGrabForRetry)

			got, err := grabsStore.Get(ctx, g.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got.Status != tc.wantStatus {
				t.Fatalf("status = %q, want %q", got.Status, tc.wantStatus)
			}
			if tc.wantReason != "" && got.RetryReason != tc.wantReason {
				t.Fatalf("retryReason = %q, want %q", got.RetryReason, tc.wantReason)
			}
		})
	}
}

func TestHandleUsenetError_UnknownGIDAndNilFailureAreNoOps(t *testing.T) {
	ctx := context.Background()
	deps, _ := usenetErrorDeps(t)

	handleUsenetError(ctx, deps, "nzb-missing", usenet.ErrArticleNotFound, parkGrabForRetry)
	handleUsenetError(ctx, deps, "nzb-anything", nil, parkGrabForRetry)
}

func TestHandleUsenetError_IgnoresAlreadyTerminal(t *testing.T) {
	ctx := context.Background()
	deps, grabsStore := usenetErrorDeps(t)
	g := dispatchedUsenetGrab(t, grabsStore, "nzb-terminal")
	if err := parkGrabForRetry(ctx, deps, g.ID, articlesUnavailableReason); err != nil {
		t.Fatal(err)
	}
	before, err := grabsStore.Get(ctx, g.ID)
	if err != nil {
		t.Fatal(err)
	}

	handleUsenetError(ctx, deps, "nzb-terminal", usenet.ErrArticleNotFound, parkGrabForRetry)

	after, err := grabsStore.Get(ctx, g.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.RetryCount != before.RetryCount {
		t.Fatalf("retry_count bumped on already-parked row: %d → %d", before.RetryCount, after.RetryCount)
	}
}

func TestHandleUsenetError_ParkFailureDoesNotCrash(t *testing.T) {
	ctx := context.Background()
	deps, grabsStore := usenetErrorDeps(t)
	dispatchedUsenetGrab(t, grabsStore, "nzb-park-fail")

	failing := func(context.Context, AutoGrabDeps, int64, string) error {
		return errors.New("park boom")
	}
	// Claude 2026-09-17: must use a generic (non-430, non-transport) error so
	// the test exercises the park grabParker path. ErrArticleNotFound routes to
	// SetPendingRetryWithScope (the 430 escalation path added later), which clears
	// download_gid and bypasses the park param entirely — that path always succeeds
	// and leaves the grab in pending_retry, not in-flight.
	// Review if: the 430 path is refactored to delegate to park.
	handleUsenetError(ctx, deps, "nzb-park-fail", errors.New("generic retrieval error"), failing)

	got, err := grabsStore.GetByDownloadGID(ctx, "nzb-park-fail")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != grabs.Queued && got.Status != grabs.Downloading {
		t.Fatalf("status should stay in-flight after park failure, got %q", got.Status)
	}
}

func TestHandleUsenetError_ThenSweepDoesNotDoublePark(t *testing.T) {
	ctx := context.Background()
	deps, grabsStore := usenetErrorDeps(t)
	g := dispatchedUsenetGrab(t, grabsStore, "nzb-double")
	failure := fmt.Errorf("segment: %w", usenet.ErrArticleNotFound)

	handleUsenetError(ctx, deps, "nzb-double", failure, parkGrabForRetry)
	afterHandler, err := grabsStore.Get(ctx, g.ID)
	if err != nil {
		t.Fatal(err)
	}
	if afterHandler.Status != grabs.PendingRetry {
		t.Fatalf("handler status = %q", afterHandler.Status)
	}

	// Sweep still sees the engine error, but the grab is no longer queued/
	// downloading so it must leave retry_count alone.
	sweepUsenetFailures(ctx, deps, staticLookup("nzb-double", &usenet.Download{
		GID: "nzb-double", Status: "error", Err: failure,
	}))
	afterSweep, err := grabsStore.Get(ctx, g.ID)
	if err != nil {
		t.Fatal(err)
	}
	if afterSweep.RetryCount != afterHandler.RetryCount {
		t.Fatalf("sweep double-parked: retry_count %d → %d", afterHandler.RetryCount, afterSweep.RetryCount)
	}
}

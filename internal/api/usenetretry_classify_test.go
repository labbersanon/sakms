// This file guards two Phase-4 review findings that the rest of the retry
// tests could not observe:
//
//  1. A transient usenet retrieval failure (dial/decode) must be RETRIED, not
//     permanently failed. The natural home for this assertion would be
//     internal/usenet's own test, whose "a dial failure must stay unclassified
//     so it is retried" message claims exactly this — but that package cannot
//     see classifyDownloadState (internal/api imports internal/usenet, so the
//     reverse import is a cycle). The claim is therefore split: the usenet side
//     asserts the error stays unclassified, and this file asserts unclassified
//     really means retryable.
//  2. A failed retry's retry_reason must not embed the underlying error, which
//     routinely carries a credentialed URL and lands in a plaintext DB column,
//     the server log, and the Requests screen.
package api

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/labbersanon/sakms/internal/grabs"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/usenet"
)

// TestClassifyDownloadStateTransientFailureIsRetryable is the regression guard
// for the HIGH finding. Before the fix, ONLY ErrArticleNotFound was retryable,
// so any other non-nil error fell through to the state switch, where "error"
// mapped to grabs.Failed — terminal, never re-searched. A provider that was
// merely unreachable therefore permanently lost the download.
func TestClassifyDownloadStateTransientFailureIsRetryable(t *testing.T) {
	for _, tc := range []struct {
		name    string
		failure error
		want    grabs.Status
	}{
		{
			name:    "a dial failure is retryable, not permanently failed",
			failure: fmt.Errorf("segment <abc@news>: %w", errors.New("dial tcp 10.0.0.1:563: connect: connection refused")),
			want:    grabs.PendingRetry,
		},
		{
			name:    "a decode failure is retryable, not permanently failed",
			failure: fmt.Errorf("segment <abc@news>: %w", errors.New("yenc: crc mismatch")),
			want:    grabs.PendingRetry,
		},
		{
			// The mixed case named in the review: one subscription answered
			// 451 while another was unreachable, so fetchSegmentAny returns
			// the raw transport error. Nothing PROVED a takedown here.
			name:    "451 from one provider plus an unreachable one is retryable",
			failure: fmt.Errorf("segment <abc@news>: %w", errors.New("dial tcp 10.0.0.2:563: i/o timeout")),
			want:    grabs.PendingRetry,
		},
		{
			name:    "a confirmed 451 takedown is still permanent",
			failure: fmt.Errorf("segment <abc@news>: %w", usenet.ErrArticleRemoved),
			want:    grabs.Failed,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyDownloadState("error", tc.failure); got != tc.want {
				t.Errorf("classifyDownloadState(\"error\", %v) = %q, want %q", tc.failure, got, tc.want)
			}
		})
	}
}

// TestSweepUsenetFailuresParksATransientFailure is the same guard one level up:
// the authoritative headless transition must park a dial failure for re-search
// (with a retry_after, or DueForRetry never sees it) instead of failing it.
func TestSweepUsenetFailuresParksATransientFailure(t *testing.T) {
	ctx := context.Background()
	_, _, _, settingsStore, grabsStore, _, _, _, _, _, _ := testStores(t)
	g := dispatchedUsenetGrab(t, grabsStore, "nzb-1")
	deps := AutoGrabDeps{SettingsStore: settingsStore, GrabsStore: grabsStore}

	sweepUsenetFailures(ctx, deps, staticLookup("nzb-1", &usenet.Download{
		GID: "nzb-1", Status: "error",
		Err: fmt.Errorf("segment <abc@news>: %w", errors.New("dial tcp 10.0.0.1:563: connect: connection refused")),
	}))

	got, err := grabsStore.Get(ctx, g.ID)
	if err != nil {
		t.Fatalf("reloading grab: %v", err)
	}
	if got.Status != grabs.PendingRetry {
		t.Fatalf("status = %q, want %q — a transient retrieval failure is terminal and never re-searched", got.Status, grabs.PendingRetry)
	}
	if got.RetryAfter == "" {
		t.Error("parked without a retry_after — DueForRetry ignores such a row, so it would never be retried")
	}
	if got.RetryReason != retrievalFailedReason {
		t.Errorf("retryReason = %q, want %q — an unreachable subscription is not proof that no subscription holds the articles", got.RetryReason, retrievalFailedReason)
	}
}

// TestReparkFailedRetryDoesNotLeakTheCause guards the Security-Medium finding.
// The cause here is the exact shape the review named: an unwrapped *url.Error
// from a TMDB call, whose Error() renders the full URL including api_key.
// retry_reason is a plaintext column sitting beside download_url_encrypted and
// is rendered verbatim in the browser, so it must carry no part of it.
func TestReparkFailedRetryDoesNotLeakTheCause(t *testing.T) {
	ctx := context.Background()
	_, _, _, settingsStore, grabsStore, _, _, _, _, _, _ := testStores(t)
	setAutoGrabToggle(t, settingsStore, true)
	g := dueRetryRow(t, grabsStore, mode.Movies, "Some Movie", 0)

	secret := "s3cr3t-tmdb-key"
	leaky := func(context.Context, mode.Mode) (*mode.Session, error) {
		return nil, &url.Error{
			Op:  "Get",
			URL: "https://api.themoviedb.org/3/movie/42?api_key=" + secret,
			Err: errors.New("dial tcp: i/o timeout"),
		}
	}
	runUsenetRetryCycle(ctx, AutoGrabDeps{SettingsStore: settingsStore, GrabsStore: grabsStore},
		leaky, nil, nil, time.Now())

	got, err := grabsStore.Get(ctx, g.ID)
	if err != nil {
		t.Fatalf("reloading grab: %v", err)
	}
	if strings.Contains(got.RetryReason, secret) || strings.Contains(got.RetryReason, "api_key") {
		t.Fatalf("retry_reason leaked the failing request's credentials: %q", got.RetryReason)
	}
	if got.RetryReason != retrySearchFailedReason {
		t.Errorf("retryReason = %q, want the classified %q", got.RetryReason, retrySearchFailedReason)
	}
	// The attempt must still be counted, or maxRetryAttempts never retires a
	// row that can never succeed — the classification must not cost that.
	if got.RetryCount != 1 {
		t.Errorf("retryCount = %d, want 1", got.RetryCount)
	}
}

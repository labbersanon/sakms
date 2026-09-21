package api

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/labbersanon/sakms/internal/grabs"
	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/usenet"
)

// fakeForgetEngine satisfies contentForgetEngine for tests. No real staging
// dirs are created — only the in-memory state is tracked.
type fakeForgetEngine struct {
	forgotGIDs []string
	stagingDir string
}

func (f *fakeForgetEngine) Forget(gid string) bool {
	f.forgotGIDs = append(f.forgotGIDs, gid)
	return true
}
func (f *fakeForgetEngine) StagingDir() string { return f.stagingDir }

// TestContentUnusableFailure is the 5-arm routing table that pins every branch
// of contentUnusableFailure.
func TestContentUnusableFailure(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "nil is not content",
			err:  nil,
			want: false,
		},
		{
			name: "ErrTransport wins tie — not content",
			err:  fmt.Errorf("%w: dial timeout", usenet.ErrTransport),
			want: false,
		},
		{
			name: "ErrUnpackToolMissing — environment fault, not content",
			err:  usenet.ErrUnpackToolMissing,
			want: false,
		},
		{
			name: "ErrContentUnusable (PAR2/unpack) IS content",
			err:  fmt.Errorf("%w: par2 corrupt", usenet.ErrContentUnusable),
			want: true,
		},
		{
			name: "ErrNoVideoFile (hollow import) IS content",
			err:  fmt.Errorf("%w: no video file found under /staging/nzb-x", library.ErrNoVideoFile),
			want: true,
		},
		{
			name: "ErrContentUnusable wrapping ErrTransport: transport wins",
			// This cannot happen by construction (the wrap sites are disjoint)
			// but the defence-in-depth rule says transport outranks content.
			err:  fmt.Errorf("%w: %w", usenet.ErrContentUnusable, usenet.ErrTransport),
			want: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := contentUnusableFailure(tc.err); got != tc.want {
				t.Errorf("contentUnusableFailure = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestParkUsenetContentFailure_HappyPath asserts the park write: due-now,
// GID cleared, transport_retry_count=0, keys appended, Forget called.
func TestParkUsenetContentFailure_HappyPath(t *testing.T) {
	ctx := context.Background()
	_, _, settingsStore, grabsStore, _, _, _, _, _, _ := testStores(t)
	deps := AutoGrabDeps{SettingsStore: settingsStore, GrabsStore: grabsStore}

	_ = dispatchedUsenetGrab(t, grabsStore, "nzb-happy")
	// Reload to get the actual DownloadGID in the struct (dispatchedUsenetGrab
	// returns the pre-SetDownloadGID state; the guard checks the nzb- prefix).
	gLoaded, err := grabsStore.GetByDownloadGID(ctx, "nzb-happy")
	if err != nil {
		t.Fatalf("GetByDownloadGID: %v", err)
	}
	g := *gLoaded
	eng := &fakeForgetEngine{stagingDir: t.TempDir()}

	before := time.Now()
	handled, err := parkUsenetContentFailure(ctx, deps, g, usenet.ErrContentUnusable, eng)
	after := time.Now()
	if err != nil {
		t.Fatalf("parkUsenetContentFailure: %v", err)
	}
	if !handled {
		t.Fatal("handled = false, want true")
	}

	got, err := grabsStore.Get(ctx, g.ID)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got.Status != grabs.PendingRetry {
		t.Errorf("status = %q, want pending_retry", got.Status)
	}
	if got.DownloadGID != "" {
		t.Errorf("download_gid = %q after park, want ''", got.DownloadGID)
	}
	if got.TransportRetryCount != 0 {
		t.Errorf("transport_retry_count = %d, want 0", got.TransportRetryCount)
	}
	ra, err := grabs.ParseTime(got.RetryAfter)
	if err != nil {
		t.Fatalf("parse retry_after: %v", err)
	}
	if ra.Before(before.Add(-time.Second)) || ra.After(after.Add(5*time.Second)) {
		t.Errorf("retry_after = %v, want ≈ now", ra)
	}
	if grabs.AlternateAttempts(grabs.ParseTriedReleaseKeys(got.TriedReleaseKeys)) != 1 {
		t.Errorf("AlternateAttempts = %d, want 1 (u: entry added)", grabs.AlternateAttempts(grabs.ParseTriedReleaseKeys(got.TriedReleaseKeys)))
	}
	if len(eng.forgotGIDs) == 0 || eng.forgotGIDs[0] != "nzb-happy" {
		t.Errorf("engine.Forget not called with %q; got %v", "nzb-happy", eng.forgotGIDs)
	}
}

// TestParkUsenetContentFailure_NonNZBGIDFallsThrough asserts that a non-nzb-
// prefixed GID returns (false, nil) — falls through to days ladder.
func TestParkUsenetContentFailure_NonNZBGIDFallsThrough(t *testing.T) {
	ctx := context.Background()
	_, _, settingsStore, grabsStore, _, _, _, _, _, _ := testStores(t)
	deps := AutoGrabDeps{SettingsStore: settingsStore, GrabsStore: grabsStore}

	g, err := grabsStore.Create(ctx, grabs.Grab{
		Mode: "movies", Title: "Non-NZB", TMDBID: 44,
		Indexer: "I", Protocol: "torrent", DownloadClient: "torrent",
		RootFolderPath: "/movies", DownloadURL: "https://i.example/torrent",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := grabsStore.SetDownloadGID(ctx, g.ID, "torrent-abc"); err != nil {
		t.Fatalf("setGID: %v", err)
	}
	handled, err := parkUsenetContentFailure(ctx, deps, g, usenet.ErrContentUnusable, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if handled {
		t.Error("handled = true for non-nzb- GID, want false")
	}
}

// TestParkUsenetContentFailure_EmptyURLFallsThrough asserts that an empty
// DownloadURL returns (false, nil) — no u: key possible.
func TestParkUsenetContentFailure_EmptyURLFallsThrough(t *testing.T) {
	ctx := context.Background()
	_, _, settingsStore, grabsStore, _, _, _, _, _, _ := testStores(t)
	deps := AutoGrabDeps{SettingsStore: settingsStore, GrabsStore: grabsStore}

	g, err := grabsStore.Create(ctx, grabs.Grab{
		Mode: "movies", Title: "No URL", TMDBID: 45,
		Indexer: "I", Protocol: "usenet", DownloadClient: "usenet",
		RootFolderPath: "/movies",
		// DownloadURL intentionally empty
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := grabsStore.SetDownloadGID(ctx, g.ID, "nzb-nourl"); err != nil {
		t.Fatalf("setGID: %v", err)
	}
	handled, err := parkUsenetContentFailure(ctx, deps, g, usenet.ErrContentUnusable, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if handled {
		t.Error("handled = true with empty URL, want false")
	}
}

// TestParkUsenetContentFailure_CapFallsThrough asserts that reaching
// MaxAlternateReleaseAttempts returns (false, nil).
func TestParkUsenetContentFailure_CapFallsThrough(t *testing.T) {
	ctx := context.Background()
	_, _, settingsStore, grabsStore, _, _, _, _, _, _ := testStores(t)
	deps := AutoGrabDeps{SettingsStore: settingsStore, GrabsStore: grabsStore}

	g := dispatchedUsenetGrab(t, grabsStore, "nzb-cap1")
	eng := &fakeForgetEngine{stagingDir: t.TempDir()}

	// Exhaust the cap via repeated parks, each with a distinct NZB URL to
	// simulate the drain dispatching a different release between failures.
	// Without distinct URLs, ParkForAlternateRelease's dedup collapses all
	// u: keys to the same hash and the attempt counter never advances past 1.
	for i := 0; i < grabs.MaxAlternateReleaseAttempts; i++ {
		// Simulate Relaunch with a fresh NZB URL (different release).
		gid := fmt.Sprintf("nzb-cap%d", i+1)
		url := fmt.Sprintf("https://indexer.example/nzb-cap?idx=%d", i+1)
		if err := grabsStore.Relaunch(ctx, g.ID, grabs.Dispatch{
			Indexer: "I", Protocol: "usenet", DownloadClient: "usenet",
			RootFolderPath: "/movies", DownloadURL: url, GID: gid,
		}); err != nil {
			t.Fatalf("Relaunch iter %d: %v", i, err)
		}
		current, err := grabsStore.Get(ctx, g.ID)
		if err != nil {
			t.Fatalf("get iter %d: %v", i, err)
		}
		handled, err := parkUsenetContentFailure(ctx, deps, *current, usenet.ErrContentUnusable, eng)
		if err != nil {
			t.Fatalf("parkUsenetContentFailure iter %d: %v", i, err)
		}
		if !handled {
			t.Fatalf("iter %d: handled=false before cap reached", i)
		}
	}

	// Re-dispatch one more release — cap is now exhausted.
	if err := grabsStore.Relaunch(ctx, g.ID, grabs.Dispatch{
		Indexer: "I", Protocol: "usenet", DownloadClient: "usenet",
		RootFolderPath: "/movies",
		DownloadURL:    "https://indexer.example/nzb-cap?idx=99",
		GID:            "nzb-cap-over",
	}); err != nil {
		t.Fatalf("Relaunch at cap: %v", err)
	}
	atCap, err := grabsStore.Get(ctx, g.ID)
	if err != nil {
		t.Fatalf("get at cap: %v", err)
	}
	handled, err := parkUsenetContentFailure(ctx, deps, *atCap, usenet.ErrContentUnusable, eng)
	if err != nil {
		t.Fatalf("unexpected error at cap: %v", err)
	}
	if handled {
		t.Errorf("handled = true after cap exhausted, want false")
	}
}

// TestParkContentFailureOrDaysLadder_CapFallsToDaysLadder is the regression
// for the live Love Is Blind stuck-queued bug: when the alternate-release
// episode is exhausted, the import/reconcile helper must ParkWithBackoff
// (clear GID, pending_retry) rather than leave the grab queued.
func TestParkContentFailureOrDaysLadder_CapFallsToDaysLadder(t *testing.T) {
	ctx := context.Background()
	_, _, settingsStore, grabsStore, _, _, _, _, _, _ := testStores(t)
	deps := AutoGrabDeps{SettingsStore: settingsStore, GrabsStore: grabsStore}
	eng := &fakeForgetEngine{stagingDir: t.TempDir()}

	g := dispatchedUsenetGrab(t, grabsStore, "nzb-import-cap0")
	// Exhaust the alternate-release budget the same way the live path does.
	for i := 0; i < grabs.MaxAlternateReleaseAttempts; i++ {
		gid := fmt.Sprintf("nzb-import-cap%d", i+1)
		url := fmt.Sprintf("https://indexer.example/nzb-import-cap?idx=%d", i+1)
		if err := grabsStore.Relaunch(ctx, g.ID, grabs.Dispatch{
			Indexer: "I", Protocol: "usenet", DownloadClient: "usenet",
			RootFolderPath: "/movies", DownloadURL: url, GID: gid,
		}); err != nil {
			t.Fatalf("Relaunch iter %d: %v", i, err)
		}
		current, err := grabsStore.Get(ctx, g.ID)
		if err != nil {
			t.Fatalf("get iter %d: %v", i, err)
		}
		if err := parkContentFailureOrDaysLadder(ctx, deps, *current, library.ErrNoVideoFile, eng); err != nil {
			t.Fatalf("park iter %d: %v", i, err)
		}
		parked, err := grabsStore.Get(ctx, g.ID)
		if err != nil {
			t.Fatalf("reload iter %d: %v", i, err)
		}
		if parked.Status != grabs.PendingRetry || parked.DownloadGID != "" {
			t.Fatalf("iter %d: status=%q gid=%q, want pending_retry with empty gid", i, parked.Status, parked.DownloadGID)
		}
		if grabs.AlternateAttempts(grabs.ParseTriedReleaseKeys(parked.TriedReleaseKeys)) != i+1 {
			t.Fatalf("iter %d: attempts=%d, want %d", i, grabs.AlternateAttempts(grabs.ParseTriedReleaseKeys(parked.TriedReleaseKeys)), i+1)
		}
	}

	// Cap exhausted: next content failure must take the days ladder.
	if err := grabsStore.Relaunch(ctx, g.ID, grabs.Dispatch{
		Indexer: "I", Protocol: "usenet", DownloadClient: "usenet",
		RootFolderPath: "/movies",
		DownloadURL:    "https://indexer.example/nzb-import-cap?idx=99",
		GID:            "nzb-import-cap-over",
	}); err != nil {
		t.Fatalf("Relaunch at cap: %v", err)
	}
	atCap, err := grabsStore.Get(ctx, g.ID)
	if err != nil {
		t.Fatalf("get at cap: %v", err)
	}
	beforeCount := atCap.RetryCount
	if err := parkContentFailureOrDaysLadder(ctx, deps, *atCap, library.ErrNoVideoFile, eng); err != nil {
		t.Fatalf("park at cap: %v", err)
	}
	got, err := grabsStore.Get(ctx, g.ID)
	if err != nil {
		t.Fatalf("reload at cap: %v", err)
	}
	if got.Status != grabs.PendingRetry {
		t.Errorf("status = %q, want pending_retry", got.Status)
	}
	if got.DownloadGID != "" {
		t.Errorf("download_gid = %q, want empty (days ladder clears it)", got.DownloadGID)
	}
	if got.RetryCount != beforeCount+1 {
		t.Errorf("retry_count = %d, want %d (ParkWithBackoff advances)", got.RetryCount, beforeCount+1)
	}
	// tried_release_keys cleared by SetPendingRetry / ParkWithBackoff.
	if got.TriedReleaseKeys != "" {
		t.Errorf("tried_release_keys = %q, want cleared after days-ladder fallthrough", got.TriedReleaseKeys)
	}
}

// TestApplyUsenetFailure_ContentCapFallsToDaysLadder pins the same fallthrough
// on the applyUsenetFailure / onError / sweep path.
func TestApplyUsenetFailure_ContentCapFallsToDaysLadder(t *testing.T) {
	ctx := context.Background()
	_, _, settingsStore, grabsStore, _, _, _, _, _, _ := testStores(t)
	deps := AutoGrabDeps{SettingsStore: settingsStore, GrabsStore: grabsStore}

	g := dispatchedUsenetGrab(t, grabsStore, "nzb-apply-cap0")
	for i := 0; i < grabs.MaxAlternateReleaseAttempts; i++ {
		gid := fmt.Sprintf("nzb-apply-cap%d", i+1)
		url := fmt.Sprintf("https://indexer.example/nzb-apply-cap?idx=%d", i+1)
		if err := grabsStore.Relaunch(ctx, g.ID, grabs.Dispatch{
			Indexer: "I", Protocol: "usenet", DownloadClient: "usenet",
			RootFolderPath: "/movies", DownloadURL: url, GID: gid,
		}); err != nil {
			t.Fatalf("Relaunch iter %d: %v", i, err)
		}
		current, err := grabsStore.Get(ctx, g.ID)
		if err != nil {
			t.Fatalf("get iter %d: %v", i, err)
		}
		if _, err := applyUsenetFailure(ctx, deps, *current, usenet.ErrContentUnusable, parkGrabForRetry); err != nil {
			t.Fatalf("applyUsenetFailure iter %d: %v", i, err)
		}
	}
	if err := grabsStore.Relaunch(ctx, g.ID, grabs.Dispatch{
		Indexer: "I", Protocol: "usenet", DownloadClient: "usenet",
		RootFolderPath: "/movies",
		DownloadURL:    "https://indexer.example/nzb-apply-cap?idx=99",
		GID:            "nzb-apply-cap-over",
	}); err != nil {
		t.Fatalf("Relaunch at cap: %v", err)
	}
	atCap, err := grabsStore.Get(ctx, g.ID)
	if err != nil {
		t.Fatalf("get at cap: %v", err)
	}
	if _, err := applyUsenetFailure(ctx, deps, *atCap, usenet.ErrContentUnusable, parkGrabForRetry); err != nil {
		t.Fatalf("applyUsenetFailure at cap: %v", err)
	}
	got, err := grabsStore.Get(ctx, g.ID)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got.Status != grabs.PendingRetry || got.DownloadGID != "" {
		t.Fatalf("status=%q gid=%q, want pending_retry with empty gid", got.Status, got.DownloadGID)
	}
	if got.TriedReleaseKeys != "" {
		t.Errorf("tried_release_keys should be cleared on days-ladder fallthrough, got %q", got.TriedReleaseKeys)
	}
}

// TestApplyUsenetFailure_ContentBranch asserts that a content-unusable error
// routes to the alternate-release park (due-now, keys set) rather than the
// days-ladder park.
func TestApplyUsenetFailure_ContentBranch(t *testing.T) {
	ctx := context.Background()
	_, _, settingsStore, grabsStore, _, _, _, _, _, _ := testStores(t)
	deps := AutoGrabDeps{SettingsStore: settingsStore, GrabsStore: grabsStore}

	_ = dispatchedUsenetGrab(t, grabsStore, "nzb-content1")
	// Reload to get the actual DownloadGID in the struct (Create returns pre-GID state).
	g, err := grabsStore.GetByDownloadGID(ctx, "nzb-content1")
	if err != nil {
		t.Fatalf("GetByDownloadGID: %v", err)
	}
	failure := fmt.Errorf("%w: par2 corrupt", usenet.ErrContentUnusable)

	before := time.Now()
	status, err := applyUsenetFailure(ctx, deps, *g, failure, parkGrabForRetry)
	after := time.Now()
	if err != nil {
		t.Fatalf("applyUsenetFailure: %v", err)
	}
	if status != grabs.PendingRetry {
		t.Errorf("status = %q, want pending_retry", status)
	}

	got, err := grabsStore.Get(ctx, g.ID)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	// due-now (not days-ladder backoff)
	ra, err := grabs.ParseTime(got.RetryAfter)
	if err != nil {
		t.Fatalf("parse retry_after: %v", err)
	}
	if ra.Before(before.Add(-time.Second)) || ra.After(after.Add(10*time.Second)) {
		t.Errorf("retry_after = %v, want ≈ now (due-now alternate park)", ra)
	}
	// tried_release_keys set
	if grabs.AlternateAttempts(grabs.ParseTriedReleaseKeys(got.TriedReleaseKeys)) != 1 {
		t.Errorf("AlternateAttempts after content park = %d, want 1", grabs.AlternateAttempts(grabs.ParseTriedReleaseKeys(got.TriedReleaseKeys)))
	}
}

// TestApplyUsenetFailure_TransportBeatsContent asserts that a failure wrapping
// both ErrTransport and ErrContentUnusable routes to the transport park, not
// the content park (§7.1: transport outranks content).
func TestApplyUsenetFailure_TransportBeatsContent(t *testing.T) {
	ctx := context.Background()
	_, _, settingsStore, grabsStore, _, _, _, _, _, _ := testStores(t)
	deps := AutoGrabDeps{SettingsStore: settingsStore, GrabsStore: grabsStore}

	_ = dispatchedUsenetGrab(t, grabsStore, "nzb-tiebreak")
	g, err := grabsStore.GetByDownloadGID(ctx, "nzb-tiebreak")
	if err != nil {
		t.Fatalf("GetByDownloadGID: %v", err)
	}
	// Construct an error that satisfies both — in practice this can't happen
	// (the wrap sites are disjoint), but the routing predicate must be robust.
	tie := fmt.Errorf("%w: %w", usenet.ErrTransport, usenet.ErrContentUnusable)

	_, err = applyUsenetFailure(ctx, deps, *g, tie, parkGrabForRetry)
	if err != nil {
		t.Fatalf("applyUsenetFailure: %v", err)
	}

	got, err := grabsStore.Get(ctx, g.ID)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	// transport park keeps the GID; content park clears it.
	if got.DownloadGID == "" {
		t.Error("GID was cleared — content park fired; transport should have won the tie")
	}
	// No tried_release_keys — transport path.
	if got.TriedReleaseKeys != "" {
		t.Errorf("tried_release_keys = %q after transport park, want ''", got.TriedReleaseKeys)
	}
}

// TestApplyUsenetFailure_UnpackToolMissingFallsToLadder asserts that
// ErrUnpackToolMissing takes the days-ladder path, not the alternate-release path.
func TestApplyUsenetFailure_UnpackToolMissingFallsToLadder(t *testing.T) {
	ctx := context.Background()
	_, _, settingsStore, grabsStore, _, _, _, _, _, _ := testStores(t)
	deps := AutoGrabDeps{SettingsStore: settingsStore, GrabsStore: grabsStore}

	_ = dispatchedUsenetGrab(t, grabsStore, "nzb-toolmissing")
	g, err := grabsStore.GetByDownloadGID(ctx, "nzb-toolmissing")
	if err != nil {
		t.Fatalf("GetByDownloadGID: %v", err)
	}
	failure := usenet.ErrUnpackToolMissing

	_, err = applyUsenetFailure(ctx, deps, *g, failure, parkGrabForRetry)
	if err != nil {
		t.Fatalf("applyUsenetFailure: %v", err)
	}

	got, err := grabsStore.Get(ctx, g.ID)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	// Days-ladder clears tried_release_keys (was empty, stays empty).
	if got.TriedReleaseKeys != "" {
		t.Errorf("tried_release_keys = %q, want '' (days-ladder path)", got.TriedReleaseKeys)
	}
	// retry_after is far in the future (days-ladder backoff, not due-now).
	ra, err := grabs.ParseTime(got.RetryAfter)
	if err != nil {
		t.Fatalf("parse retry_after: %v", err)
	}
	if !ra.After(time.Now().Add(time.Hour)) {
		t.Errorf("retry_after = %v, want > 1h from now (days-ladder backoff)", ra)
	}
}

// TestUsenetErrorHandler_ContentFailureParksForAlternate is the end-to-end
// assertion that a PAR2 error reaching the error handler parks due-now.
func TestUsenetErrorHandler_ContentFailureParksForAlternate(t *testing.T) {
	ctx := context.Background()
	_, _, settingsStore, grabsStore, _, _, _, _, _, _ := testStores(t)
	g := dispatchedUsenetGrab(t, grabsStore, "nzb-e2e-content")
	deps := AutoGrabDeps{SettingsStore: settingsStore, GrabsStore: grabsStore}

	failure := fmt.Errorf("%w: par2 parse error", usenet.ErrContentUnusable)
	before := time.Now()
	handleUsenetError(ctx, deps, "nzb-e2e-content", failure, parkGrabForRetry)
	after := time.Now()

	got, err := grabsStore.Get(ctx, g.ID)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got.Status != grabs.PendingRetry {
		t.Fatalf("status = %q, want pending_retry", got.Status)
	}
	ra, err := grabs.ParseTime(got.RetryAfter)
	if err != nil {
		t.Fatalf("parse retry_after: %v", err)
	}
	if ra.Before(before.Add(-time.Second)) || ra.After(after.Add(10*time.Second)) {
		t.Errorf("retry_after = %v, want ≈ now (content failure → due-now park)", ra)
	}
	if grabs.AlternateAttempts(grabs.ParseTriedReleaseKeys(got.TriedReleaseKeys)) != 1 {
		t.Error("expected 1 u: entry in tried_release_keys after content park")
	}
}

// TestContentNoVideoReason asserts ErrNoVideoFile maps to contentNoVideoReason.
func TestContentNoVideoReason(t *testing.T) {
	err := fmt.Errorf("%w: no video", library.ErrNoVideoFile)
	if got := contentFailureReason(err); got != contentNoVideoReason {
		t.Errorf("contentFailureReason(ErrNoVideoFile) = %q, want %q", got, contentNoVideoReason)
	}
	err2 := fmt.Errorf("%w: par2", usenet.ErrContentUnusable)
	if got := contentFailureReason(err2); got != contentUnpackFailedReason {
		t.Errorf("contentFailureReason(ErrContentUnusable) = %q, want %q", got, contentUnpackFailedReason)
	}
}

// Ensure errors package is used.
var _ = errors.Is

package api

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/labbersanon/sakms/internal/excludes"
	"github.com/labbersanon/sakms/internal/grabs"
	"github.com/labbersanon/sakms/internal/usenet"
)

// transportDeps builds the deps + grabsStore pair applyUsenetFailure needs.
func transportDeps(t *testing.T) (AutoGrabDeps, *grabs.Store) {
	t.Helper()
	_, _, settingsStore, grabsStore, _, _, _, _, _, _ := testStores(t)
	return AutoGrabDeps{SettingsStore: settingsStore, GrabsStore: grabsStore}, grabsStore
}

// dispatchedTransportGrab creates a dispatched usenet grab with the given GID
// and a non-empty DownloadURL, as required for transport park eligibility.
func dispatchedTransportGrab(t *testing.T, grabsStore *grabs.Store, gid string) grabs.Grab {
	t.Helper()
	ctx := context.Background()
	g, err := grabsStore.Create(ctx, grabs.Grab{
		Mode:           "movies",
		Title:          "Transport Movie",
		TMDBID:         42,
		Indexer:        "I",
		Protocol:       "usenet",
		DownloadClient: "usenet",
		RootFolderPath: "/movies",
		DownloadURL:    "https://indexer.example/nzb?id=1",
	})
	if err != nil {
		t.Fatalf("creating grab: %v", err)
	}
	if err := grabsStore.SetDownloadGID(ctx, g.ID, gid); err != nil {
		t.Fatalf("setting download gid: %v", err)
	}
	return g
}

// TestApplyUsenetFailure_TransportWrap parks for short resume when failure
// wraps ErrTransport: retry_after ≈ now+2m, GID preserved, retry_count
// unchanged, transport_retry_count==1, reason is transportRetryReason.
func TestApplyUsenetFailure_TransportWrap(t *testing.T) {
	ctx := context.Background()
	deps, grabsStore := transportDeps(t)
	g := dispatchedTransportGrab(t, grabsStore, "nzb-transport-10")

	before, err := grabsStore.Get(ctx, g.ID)
	if err != nil {
		t.Fatalf("get before: %v", err)
	}

	failure := fmt.Errorf("pipe: %w", usenet.ErrTransport)
	now := time.Now()

	status, err := applyUsenetFailure(ctx, deps, *before, failure, parkGrabForRetry)
	if err != nil {
		t.Fatalf("applyUsenetFailure: %v", err)
	}
	if status != grabs.PendingRetry {
		t.Fatalf("status = %q, want pending_retry", status)
	}

	got, err := grabsStore.Get(ctx, g.ID)
	if err != nil {
		t.Fatalf("get after: %v", err)
	}
	if got.DownloadGID != "nzb-transport-10" {
		t.Errorf("download_gid = %q, want %q (must be preserved)", got.DownloadGID, "nzb-transport-10")
	}
	if got.RetryCount != before.RetryCount {
		t.Errorf("retry_count = %d, want %d (transport park must not advance days ladder)", got.RetryCount, before.RetryCount)
	}
	if got.TransportRetryCount != 1 {
		t.Errorf("transport_retry_count = %d, want 1", got.TransportRetryCount)
	}
	if got.RetryReason != transportRetryReason {
		t.Errorf("retry_reason = %q, want %q", got.RetryReason, transportRetryReason)
	}
	// retry_after should be roughly now+2m (first rung).
	if got.RetryAfter == "" {
		t.Fatal("retry_after is empty after transport park")
	}
	retryT, parseErr := grabs.ParseTime(got.RetryAfter)
	if parseErr != nil {
		t.Fatalf("retry_after %q does not parse: %v", got.RetryAfter, parseErr)
	}
	wantMin := now.Add(grabs.TransportBackoff(1) - 5*time.Second)
	wantMax := now.Add(grabs.TransportBackoff(1) + 5*time.Second)
	if retryT.Before(wantMin) || retryT.After(wantMax) {
		t.Errorf("retry_after = %s, want ≈ now+%s", retryT, grabs.TransportBackoff(1))
	}
}

// TestApplyUsenetFailure_TransportIneligible_NoURL falls through to the days
// ladder when DownloadURL is empty (cannot resume).
func TestApplyUsenetFailure_TransportIneligible_NoURL(t *testing.T) {
	ctx := context.Background()
	_, _, settingsStore, grabsStore, _, _, _, _, _, _ := testStores(t)
	deps := AutoGrabDeps{SettingsStore: settingsStore, GrabsStore: grabsStore}

	// Create a grab without DownloadURL.
	g, err := grabsStore.Create(ctx, grabs.Grab{
		Mode: "movies", Title: "No URL Movie", TMDBID: 43,
		Indexer: "I", Protocol: "usenet", DownloadClient: "usenet",
		RootFolderPath: "/movies",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := grabsStore.SetDownloadGID(ctx, g.ID, "nzb-nourl-1"); err != nil {
		t.Fatalf("SetDownloadGID: %v", err)
	}

	failure := fmt.Errorf("pipe: %w", usenet.ErrTransport)
	before, _ := grabsStore.Get(ctx, g.ID)
	_, err = applyUsenetFailure(ctx, deps, *before, failure, parkGrabForRetry)
	if err != nil {
		t.Fatalf("applyUsenetFailure: %v", err)
	}

	after, err := grabsStore.Get(ctx, g.ID)
	if err != nil {
		t.Fatalf("get after: %v", err)
	}
	// Should have fallen through to days-ladder park: GID cleared.
	if after.DownloadGID != "" {
		t.Errorf("download_gid = %q, want '' (fell through to days ladder — GID should be cleared)", after.DownloadGID)
	}
	if after.TransportRetryCount != 0 {
		t.Errorf("transport_retry_count = %d, want 0 (ineligible path)", after.TransportRetryCount)
	}
}

// TestApplyUsenetFailure_TransportIneligible_NonNZBGID falls through when the
// GID does not carry the "nzb-" prefix.
func TestApplyUsenetFailure_TransportIneligible_NonNZBGID(t *testing.T) {
	ctx := context.Background()
	_, _, settingsStore, grabsStore, _, _, _, _, _, _ := testStores(t)
	deps := AutoGrabDeps{SettingsStore: settingsStore, GrabsStore: grabsStore}

	g, err := grabsStore.Create(ctx, grabs.Grab{
		Mode: "movies", Title: "Non-NZB GID", TMDBID: 44,
		Indexer: "I", Protocol: "usenet", DownloadClient: "usenet",
		RootFolderPath: "/movies", DownloadURL: "https://indexer.example/nzb?id=99",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// GID without the "nzb-" prefix.
	if err := grabsStore.SetDownloadGID(ctx, g.ID, "torrent-some-infohash"); err != nil {
		t.Fatalf("SetDownloadGID: %v", err)
	}

	failure := fmt.Errorf("pipe: %w", usenet.ErrTransport)
	before, _ := grabsStore.Get(ctx, g.ID)
	_, err = applyUsenetFailure(ctx, deps, *before, failure, parkGrabForRetry)
	if err != nil {
		t.Fatalf("applyUsenetFailure: %v", err)
	}

	after, err := grabsStore.Get(ctx, g.ID)
	if err != nil {
		t.Fatalf("get after: %v", err)
	}
	// Fell through to days-ladder park.
	if after.TransportRetryCount != 0 {
		t.Errorf("transport_retry_count = %d, want 0 (non-nzb GID ineligible)", after.TransportRetryCount)
	}
	if after.DownloadGID != "" {
		t.Errorf("download_gid = %q, want '' (fell through to days ladder)", after.DownloadGID)
	}
}

// TestApplyUsenetFailure_TransportIneligible_AtCap falls through to the days
// ladder when TransportRetryCount has reached MaxTransportRetries.
func TestApplyUsenetFailure_TransportIneligible_AtCap(t *testing.T) {
	ctx := context.Background()
	deps, grabsStore := transportDeps(t)
	g := dispatchedTransportGrab(t, grabsStore, "nzb-cap-1")

	// Park up to the cap so TransportRetryCount == MaxTransportRetries.
	for i := 1; i <= grabs.MaxTransportRetries; i++ {
		if err := grabsStore.ParkForTransportResume(ctx, g.ID, time.Now().Add(2*time.Minute), "drop"); err != nil {
			t.Fatalf("park %d: %v", i, err)
		}
	}
	atCap, _ := grabsStore.Get(ctx, g.ID)
	if atCap.TransportRetryCount != grabs.MaxTransportRetries {
		t.Fatalf("pre-condition: transport_retry_count = %d, want %d", atCap.TransportRetryCount, grabs.MaxTransportRetries)
	}

	failure := fmt.Errorf("pipe: %w", usenet.ErrTransport)
	_, err := applyUsenetFailure(ctx, deps, *atCap, failure, parkGrabForRetry)
	if err != nil {
		t.Fatalf("applyUsenetFailure at cap: %v", err)
	}

	after, _ := grabsStore.Get(ctx, g.ID)
	// Fell through to days-ladder: GID cleared.
	if after.DownloadGID != "" {
		t.Errorf("download_gid = %q, want '' (fell through to days ladder at cap)", after.DownloadGID)
	}
}

// TestApplyUsenetFailure_PAR2AndOtherNonTransport verifies that a non-transport,
// non-430 error goes through the normal days-ladder park (GID cleared).
func TestApplyUsenetFailure_PAR2AndOtherNonTransport(t *testing.T) {
	ctx := context.Background()
	deps, grabsStore := transportDeps(t)
	g := dispatchedTransportGrab(t, grabsStore, "nzb-par2-1")

	before, _ := grabsStore.Get(ctx, g.ID)
	failure := errors.New("PAR2 repair failed: too many missing blocks")
	_, err := applyUsenetFailure(ctx, deps, *before, failure, parkGrabForRetry)
	if err != nil {
		t.Fatalf("applyUsenetFailure: %v", err)
	}

	after, _ := grabsStore.Get(ctx, g.ID)
	// Normal retry park: GID cleared, transport counter stays 0.
	if after.DownloadGID != "" {
		t.Errorf("download_gid = %q, want '' (non-transport park clears GID)", after.DownloadGID)
	}
	if after.TransportRetryCount != 0 {
		t.Errorf("transport_retry_count = %d, want 0 for non-transport failure", after.TransportRetryCount)
	}
	if after.Status != grabs.PendingRetry {
		t.Errorf("status = %q, want pending_retry", after.Status)
	}
}

// TestApplyUsenetFailure_430KeepsCurrentBehaviour verifies that a 430
// (ErrArticleNotFound) still escalates to torrent scope and clears the GID,
// independent of transport park logic.
func TestApplyUsenetFailure_430KeepsCurrentBehaviour(t *testing.T) {
	ctx := context.Background()
	deps, grabsStore := transportDeps(t)
	g := dispatchedTransportGrab(t, grabsStore, "nzb-430-1")

	before, _ := grabsStore.Get(ctx, g.ID)
	failure := fmt.Errorf("seg: %w", usenet.ErrArticleNotFound)
	_, err := applyUsenetFailure(ctx, deps, *before, failure, parkGrabForRetry)
	if err != nil {
		t.Fatalf("applyUsenetFailure: %v", err)
	}

	after, _ := grabsStore.Get(ctx, g.ID)
	if after.DownloadGID != "" {
		t.Errorf("download_gid = %q, want '' (430 path clears GID)", after.DownloadGID)
	}
	if after.Status != grabs.PendingRetry {
		t.Errorf("status = %q, want pending_retry", after.Status)
	}
}

// --- resumeDueTransportRetries tests ---

// fakeResumeEngine implements usenetResumeEngine for unit tests.
type fakeResumeEngine struct {
	finds      map[string]*usenet.Download // gid → Download (nil means not found)
	forgotGIDs []string
	relaunchFn func(gid, url, name string) error
}

func (f *fakeResumeEngine) FindByGID(gid string) (*usenet.Download, error) {
	d, ok := f.finds[gid]
	if !ok {
		return nil, nil
	}
	return d, nil
}

func (f *fakeResumeEngine) Forget(gid string) bool {
	f.forgotGIDs = append(f.forgotGIDs, gid)
	return true
}

func (f *fakeResumeEngine) RelaunchNZB(ctx context.Context, gid, url, name string) error {
	if f.relaunchFn != nil {
		return f.relaunchFn(gid, url, name)
	}
	return nil
}

// parkTransportGrab creates a transport-parked grab with the given GID that is
// overdue (retry_after in the past).
func parkTransportGrab(t *testing.T, grabsStore *grabs.Store, gid string) grabs.Grab {
	t.Helper()
	ctx := context.Background()
	g := dispatchedTransportGrab(t, grabsStore, gid)
	past := time.Now().Add(-5 * time.Minute)
	if err := grabsStore.ParkForTransportResume(ctx, g.ID, past, transportRetryReason); err != nil {
		t.Fatalf("ParkForTransportResume: %v", err)
	}
	got, err := grabsStore.Get(ctx, g.ID)
	if err != nil {
		t.Fatalf("get after park: %v", err)
	}
	return *got
}

// TestResumeDueTransportRetries_HappyPath: RelaunchNZB succeeds → Forget called,
// row re-armed as queued.
func TestResumeDueTransportRetries_HappyPath(t *testing.T) {
	ctx := context.Background()
	_, _, settingsStore, grabsStore, _, _, _, _, _, _ := testStores(t)
	deps := AutoGrabDeps{SettingsStore: settingsStore, GrabsStore: grabsStore}

	// Set max concurrent usenet high enough that slots are available.
	if err := settingsStore.Set(ctx, UsenetMaxConcurrentDownloadsKey, "10"); err != nil {
		t.Fatalf("set max concurrent: %v", err)
	}

	g := parkTransportGrab(t, grabsStore, "nzb-resume-happy-1")

	var relaunchCalled bool
	engine := &fakeResumeEngine{
		finds: map[string]*usenet.Download{"nzb-resume-happy-1": nil}, // not found in engine
		relaunchFn: func(gid, url, name string) error {
			relaunchCalled = true
			return nil
		},
	}

	resumeDueTransportRetries(ctx, deps, engine, map[string]bool{}, time.Now())

	if !relaunchCalled {
		t.Error("RelaunchNZB was not called for the due row")
	}

	got, err := grabsStore.Get(ctx, g.ID)
	if err != nil {
		t.Fatalf("get after resume: %v", err)
	}
	if got.Status != grabs.Queued {
		t.Errorf("status = %q, want queued after successful resume", got.Status)
	}
	if got.RetryAfter != "" {
		t.Errorf("retry_after = %q, want '' after Relaunch", got.RetryAfter)
	}
}

// TestResumeDueTransportRetries_ErrArticlesUnavailable: articles are gone →
// GID cleared, row joins days ladder.
func TestResumeDueTransportRetries_ErrArticlesUnavailable(t *testing.T) {
	ctx := context.Background()
	_, _, settingsStore, grabsStore, _, _, _, _, _, _ := testStores(t)
	deps := AutoGrabDeps{SettingsStore: settingsStore, GrabsStore: grabsStore}

	if err := settingsStore.Set(ctx, UsenetMaxConcurrentDownloadsKey, "10"); err != nil {
		t.Fatalf("set max concurrent: %v", err)
	}

	g := parkTransportGrab(t, grabsStore, "nzb-articles-gone-1")

	engine := &fakeResumeEngine{
		finds: map[string]*usenet.Download{},
		relaunchFn: func(gid, url, name string) error {
			return usenet.ErrArticlesUnavailable
		},
	}

	resumeDueTransportRetries(ctx, deps, engine, map[string]bool{}, time.Now())

	got, err := grabsStore.Get(ctx, g.ID)
	if err != nil {
		t.Fatalf("get after resume: %v", err)
	}
	if got.DownloadGID != "" {
		t.Errorf("download_gid = %q, want '' (articles gone — cleared for re-search)", got.DownloadGID)
	}
	if got.Status != grabs.PendingRetry {
		t.Errorf("status = %q, want pending_retry (joined days ladder)", got.Status)
	}
}

// TestResumeDueTransportRetries_OtherError_AdvancesLadder: non-articles error
// increments the rung if not at cap.
func TestResumeDueTransportRetries_OtherError_AdvancesLadder(t *testing.T) {
	ctx := context.Background()
	_, _, settingsStore, grabsStore, _, _, _, _, _, _ := testStores(t)
	deps := AutoGrabDeps{SettingsStore: settingsStore, GrabsStore: grabsStore}

	if err := settingsStore.Set(ctx, UsenetMaxConcurrentDownloadsKey, "10"); err != nil {
		t.Fatalf("set max concurrent: %v", err)
	}

	g := parkTransportGrab(t, grabsStore, "nzb-other-err-1")

	engine := &fakeResumeEngine{
		finds: map[string]*usenet.Download{},
		relaunchFn: func(gid, url, name string) error {
			return errors.New("NZB fetch failed: 503")
		},
	}

	now := time.Now()
	resumeDueTransportRetries(ctx, deps, engine, map[string]bool{}, now)

	got, err := grabsStore.Get(ctx, g.ID)
	if err != nil {
		t.Fatalf("get after resume: %v", err)
	}
	// Should still be transport-parked (GID preserved, pending_retry).
	if got.DownloadGID != g.DownloadGID {
		t.Errorf("download_gid = %q, want %q (error on rung N<cap should keep GID)", got.DownloadGID, g.DownloadGID)
	}
	if got.Status != grabs.PendingRetry {
		t.Errorf("status = %q, want pending_retry", got.Status)
	}
}

// TestResumeDueTransportRetries_OtherError_EscalatesAtCap: at max retries,
// a non-articles error joins the days ladder.
func TestResumeDueTransportRetries_OtherError_EscalatesAtCap(t *testing.T) {
	ctx := context.Background()
	_, _, settingsStore, grabsStore, _, _, _, _, _, _ := testStores(t)
	deps := AutoGrabDeps{SettingsStore: settingsStore, GrabsStore: grabsStore}

	if err := settingsStore.Set(ctx, UsenetMaxConcurrentDownloadsKey, "10"); err != nil {
		t.Fatalf("set max concurrent: %v", err)
	}

	g := dispatchedTransportGrab(t, grabsStore, "nzb-cap-err-1")
	past := time.Now().Add(-5 * time.Minute)
	// Park at the cap so TransportRetryCount == MaxTransportRetries.
	for i := 0; i < grabs.MaxTransportRetries; i++ {
		if err := grabsStore.ParkForTransportResume(ctx, g.ID, past, transportRetryReason); err != nil {
			t.Fatalf("park %d: %v", i, err)
		}
	}
	atCap, _ := grabsStore.Get(ctx, g.ID)
	if atCap.TransportRetryCount != grabs.MaxTransportRetries {
		t.Fatalf("pre-condition: transport_retry_count = %d, want %d", atCap.TransportRetryCount, grabs.MaxTransportRetries)
	}

	engine := &fakeResumeEngine{
		finds: map[string]*usenet.Download{},
		relaunchFn: func(gid, url, name string) error {
			return errors.New("server error")
		},
	}

	resumeDueTransportRetries(ctx, deps, engine, map[string]bool{}, time.Now())

	got, _ := grabsStore.Get(ctx, g.ID)
	// At cap: escalated to days ladder → GID cleared.
	if got.DownloadGID != "" {
		t.Errorf("download_gid = %q, want '' (escalated to days ladder at cap)", got.DownloadGID)
	}
}

// TestResumeDueTransportRetries_ZeroFreeSlots: no free usenet slots → row untouched.
func TestResumeDueTransportRetries_ZeroFreeSlots(t *testing.T) {
	ctx := context.Background()
	_, _, settingsStore, grabsStore, _, _, _, _, _, _ := testStores(t)
	deps := AutoGrabDeps{SettingsStore: settingsStore, GrabsStore: grabsStore}

	// Set max concurrent to 1 and fill it with an in-flight grab.
	if err := settingsStore.Set(ctx, UsenetMaxConcurrentDownloadsKey, "1"); err != nil {
		t.Fatalf("set max concurrent: %v", err)
	}
	slot, err := grabsStore.Create(ctx, grabs.Grab{
		Mode: "movies", Title: "Slot Filler", TMDBID: 999,
		Indexer: "I", Protocol: "usenet", DownloadClient: "usenet",
		RootFolderPath: "/movies",
	})
	if err != nil {
		t.Fatalf("create in-flight grab: %v", err)
	}
	if err := grabsStore.SetDownloadGID(ctx, slot.ID, "nzb-slot-1"); err != nil {
		t.Fatalf("SetDownloadGID: %v", err)
	}
	if err := grabsStore.UpdateStatus(ctx, slot.ID, grabs.Downloading); err != nil {
		t.Fatalf("UpdateStatus downloading: %v", err)
	}

	g := parkTransportGrab(t, grabsStore, "nzb-zero-slot-1")
	before, _ := grabsStore.Get(ctx, g.ID)

	var relaunchCalled bool
	engine := &fakeResumeEngine{
		finds: map[string]*usenet.Download{},
		relaunchFn: func(gid, url, name string) error {
			relaunchCalled = true
			return nil
		},
	}

	resumeDueTransportRetries(ctx, deps, engine, map[string]bool{}, time.Now())

	if relaunchCalled {
		t.Error("RelaunchNZB should not be called when slots are full")
	}
	after, _ := grabsStore.Get(ctx, g.ID)
	if after.RetryAfter != before.RetryAfter {
		t.Errorf("retry_after changed (%q → %q): row should be untouched with zero slots", before.RetryAfter, after.RetryAfter)
	}
}

// TestResumeDueTransportRetries_ExcludedTitleSkipped: an excluded title is
// not resumed.
func TestResumeDueTransportRetries_ExcludedTitleSkipped(t *testing.T) {
	ctx := context.Background()
	_, _, settingsStore, grabsStore, _, _, _, _, _, _ := testStores(t)
	deps := AutoGrabDeps{SettingsStore: settingsStore, GrabsStore: grabsStore}

	if err := settingsStore.Set(ctx, UsenetMaxConcurrentDownloadsKey, "10"); err != nil {
		t.Fatalf("set max concurrent: %v", err)
	}

	g := parkTransportGrab(t, grabsStore, "nzb-excluded-1")
	row, _ := grabsStore.Get(ctx, g.ID)

	// Build an excludes map that contains this grab's key.
	excluded := map[string]bool{
		excludes.Key(string(row.Mode), row.TMDBID, row.Title): true,
	}

	var relaunchCalled bool
	engine := &fakeResumeEngine{
		finds: map[string]*usenet.Download{},
		relaunchFn: func(gid, url, name string) error {
			relaunchCalled = true
			return nil
		},
	}

	resumeDueTransportRetries(ctx, deps, engine, excluded, time.Now())

	if relaunchCalled {
		t.Error("RelaunchNZB should not be called for an excluded title")
	}
}

// TestResumeDueTransportRetries_NilEngine_IsNoOp: a nil engine returns without
// panic.
func TestResumeDueTransportRetries_NilEngine_IsNoOp(t *testing.T) {
	ctx := context.Background()
	_, _, settingsStore, grabsStore, _, _, _, _, _, _ := testStores(t)
	deps := AutoGrabDeps{SettingsStore: settingsStore, GrabsStore: grabsStore}

	// Must not panic.
	resumeDueTransportRetries(ctx, deps, nil, map[string]bool{}, time.Now())
}

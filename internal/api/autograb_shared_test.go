package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/labbersanon/sakms/internal/apidto"
	"github.com/labbersanon/sakms/internal/grabs"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/prowlarr"
	"github.com/labbersanon/sakms/internal/quality"
	"github.com/labbersanon/sakms/internal/settings"
	"github.com/labbersanon/sakms/internal/usenet"
)

// qualifyingRelease is a healthy, high-bitrate torrent: 8 GB over the 6000 s
// runtime the caller supplies is ~10.7 Mbps implied, which clears every 1080p
// tier floor even after the non-AV1 padding.
func qualifyingRelease() prowlarr.Release {
	return prowlarr.Release{
		GUID: "1", Title: "Some.Movie.2023.1080p.WEB-DL.x265-GROUP", Indexer: "I",
		Protocol: prowlarr.Torrent, Size: 8_000_000_000, Seeders: 50,
		DownloadURL: "magnet:?xt=urn:btih:ABCDEF1234567890abcdef1234567890abcdef12",
	}
}

// setAutoGrabToggle writes usenet_autograb_enabled explicitly. Passing false is
// NOT the same as leaving it unset, and both states are asserted separately —
// a gate that only reads correctly when the key is absent would be a real bug.
func setAutoGrabToggle(t *testing.T, settingsStore *settings.Store, on bool) {
	t.Helper()
	v := "false"
	if on {
		v = "true"
	}
	if err := settingsStore.Set(context.Background(), usenetAutoGrabEnabledKey, v); err != nil {
		t.Fatalf("setting auto-grab toggle: %v", err)
	}
}

// TestRunAutoGrabTriggerOperatorIgnoresToggleOff is THE regression test for the
// single most dangerous way to get this feature wrong.
//
// Discover's one-click Grab already ships, and the operator's click IS the
// approval. usenet_autograb_enabled is off by default, so if TriggerOperator
// were ever folded into the gate alongside TriggerRequest/TriggerRetry, the
// shipped one-click feature would silently stop working the moment this
// released — with no error, just a grab that never happens.
//
// Asserted against BOTH off states: the toggle explicitly false, and the key
// absent entirely.
func TestRunAutoGrabTriggerOperatorIgnoresToggleOff(t *testing.T) {
	for _, tc := range []struct {
		name     string
		writeOff bool
	}{
		{"toggle explicitly false", true},
		{"toggle never set at all", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			_, _, settingsStore, grabsStore, _, _, _, _, _, _ := testStores(t)
			dl := newTestDownloader("gid-op", t.TempDir())
			if tc.writeOff {
				setAutoGrabToggle(t, settingsStore, false)
			}
			if err := settingsStore.Set(ctx, qualityTierKey(mode.Movies), string(quality.Low)); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if err := settingsStore.Set(ctx, moviesLibraryRootFolderKey, "/movies"); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			out, err := RunAutoGrab(ctx,
				AutoGrabDeps{SettingsStore: settingsStore, GrabsStore: grabsStore},
				&mode.Session{Mode: mode.Movies, Downloader: dl},
				AutoGrabRequest{
					Mode: mode.Movies, Title: "Some Movie", TMDBID: 42,
					Trigger:        TriggerOperator,
					Releases:       []prowlarr.Release{qualifyingRelease()},
					RuntimeSeconds: 6000,
				})
			if err != nil {
				t.Fatalf("RunAutoGrab: %v", err)
			}
			if out.Gated {
				t.Fatal("TriggerOperator was GATED by usenet_autograb_enabled — this silently breaks the shipped one-click Discover Grab")
			}
			if !out.Grabbed {
				t.Fatalf("expected the qualifying candidate to be dispatched, got %+v", out)
			}
			if got := len(dl.List()); got != 1 {
				t.Errorf("expected exactly one download-client add, got %d", got)
			}
			list, err := grabsStore.List(ctx, mode.Movies)
			if err != nil {
				t.Fatalf("listing grabs: %v", err)
			}
			if len(list) != 1 || list[0].Status != grabs.Queued {
				t.Fatalf("expected one queued grab, got %+v", list)
			}
		})
	}
}

// TestRunAutoGrabGatedTriggersNoOpWhenToggleOff is the other half of the
// boundary: the two unattended triggers ARE gated, and a gated no-op must be
// silent — no search, no scoring, no dispatch, no grabs row, and no error the
// retry scheduler would log every cycle.
func TestRunAutoGrabGatedTriggersNoOpWhenToggleOff(t *testing.T) {
	for _, trigger := range []AutoGrabTrigger{TriggerRequest, TriggerRetry} {
		t.Run(string(trigger), func(t *testing.T) {
			ctx := context.Background()
			_, _, settingsStore, grabsStore, _, _, _, _, _, _ := testStores(t)
			dl := newTestDownloader("gid-gated", t.TempDir())
			setAutoGrabToggle(t, settingsStore, false)

			out, err := RunAutoGrab(ctx,
				AutoGrabDeps{SettingsStore: settingsStore, GrabsStore: grabsStore},
				&mode.Session{Mode: mode.Movies, Downloader: dl},
				AutoGrabRequest{
					Mode: mode.Movies, Title: "Some Movie", TMDBID: 42,
					Trigger:        trigger,
					Releases:       []prowlarr.Release{qualifyingRelease()},
					RuntimeSeconds: 6000,
				})
			if err != nil {
				t.Fatalf("a gated no-op must not be an error: %v", err)
			}
			if !out.Gated || out.Grabbed || out.NoMatch {
				t.Fatalf("expected a silent gated no-op, got %+v", out)
			}
			if got := len(dl.List()); got != 0 {
				t.Errorf("gated trigger still dispatched %d download(s)", got)
			}
			list, err := grabsStore.List(ctx, mode.Movies)
			if err != nil {
				t.Fatalf("listing grabs: %v", err)
			}
			if len(list) != 0 {
				t.Errorf("gated trigger still wrote %d grab row(s)", len(list))
			}
		})
	}
}

// TestRunAutoGrabOperatorFallbackWritesNoRetryRow guards the phantom-row bug:
// a one-click grab that finds nothing above the floor returns a MANUAL PICK
// LIST, because a human is looking at it. Parking a pending_retry row here
// would queue an unattended retry behind every miss the operator already saw
// and decided about.
func TestRunAutoGrabOperatorFallbackWritesNoRetryRow(t *testing.T) {
	ctx := context.Background()
	_, _, settingsStore, grabsStore, _, _, _, _, _, _ := testStores(t)
	dl := newTestDownloader("gid-fb", t.TempDir())

	out, err := RunAutoGrab(ctx,
		AutoGrabDeps{SettingsStore: settingsStore, GrabsStore: grabsStore},
		&mode.Session{Mode: mode.Movies, Downloader: dl},
		AutoGrabRequest{
			Mode: mode.Movies, Title: "Some Movie", TMDBID: 42,
			Trigger:  TriggerOperator,
			Releases: []prowlarr.Release{qualifyingRelease()},
			// runtime 0 → every candidate grades unknown-bitrate → Fallback
		})
	if err != nil {
		t.Fatalf("RunAutoGrab: %v", err)
	}
	if !out.NoMatch || out.Grabbed {
		t.Fatalf("expected the fallback pick-list outcome, got %+v", out)
	}
	if len(out.Releases) != 1 || len(out.Selection.Ranked) != 1 {
		t.Errorf("expected the ranked pick list to be carried back, got %+v", out.Selection)
	}
	list, err := grabsStore.List(ctx, mode.Movies)
	if err != nil {
		t.Fatalf("listing grabs: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("TriggerOperator fallback wrote %d phantom retry row(s): %+v", len(list), list)
	}
}

// TestRunAutoGrabGatedFallbackParksOneRetryRow covers AC 15's core: a gated
// trigger that finds nothing qualifying leaves a pending_retry row behind (so
// the state is observable and DueForRetry can find it), and a SECOND identical
// search updates that row rather than inserting a duplicate.
//
// Run for all three modes. Adult is the load-bearing case: its scenes carry no
// TMDB id, so without the lowercased-title discriminator every Adult retry row
// would collapse onto one key and clobber the others.
func TestRunAutoGrabGatedFallbackParksOneRetryRow(t *testing.T) {
	for _, tc := range []struct {
		name   string
		m      mode.Mode
		tmdbID int
	}{
		{"movies", mode.Movies, 42},
		{"series", mode.Series, 77},
		{"adult (no tmdb id — title-keyed)", mode.Adult, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			_, _, settingsStore, grabsStore, _, _, _, _, _, _ := testStores(t)
			setAutoGrabToggle(t, settingsStore, true)

			req := AutoGrabRequest{
				Mode: tc.m, Title: "Some Title", TMDBID: tc.tmdbID,
				RootFolderPath: "/library", Trigger: TriggerRequest,
				Releases: []prowlarr.Release{qualifyingRelease()},
			}
			deps := AutoGrabDeps{SettingsStore: settingsStore, GrabsStore: grabsStore}
			sess := &mode.Session{Mode: tc.m}

			first, err := RunAutoGrab(ctx, deps, sess, req)
			if err != nil {
				t.Fatalf("first RunAutoGrab: %v", err)
			}
			if !first.NoMatch || first.GrabID == 0 {
				t.Fatalf("expected a parked retry row, got %+v", first)
			}
			if first.RetryStatus != grabs.PendingRetry {
				t.Errorf("expected status %q, got %q", grabs.PendingRetry, first.RetryStatus)
			}
			if first.RetryReason != noQualifyingCandidateReason {
				t.Errorf("unexpected retry reason %q", first.RetryReason)
			}

			parked, err := grabsStore.Get(ctx, first.GrabID)
			if err != nil {
				t.Fatalf("loading parked row: %v", err)
			}
			// The column contract: no candidate was selected and nothing was
			// dispatched, so there is no URL to retry and no GID to track. The
			// retry re-runs the whole search from scratch, which is what makes
			// "retry never bypasses scoring" true.
			if parked.DownloadURL != "" || parked.DownloadGID != "" {
				t.Errorf("a pending_retry row must carry neither a download url nor a gid, got %+v", parked)
			}
			if parked.RetryAfter == "" {
				t.Error("retry_after is empty — DueForRetry would never return this row")
			}
			if parked.RetryCount != 0 {
				t.Errorf("a freshly created retry row must start at retry_count 0, got %d", parked.RetryCount)
			}
			if parked.RootFolderPath != "/library" {
				t.Errorf("root folder not carried onto the retry row: %+v", parked)
			}

			second, err := RunAutoGrab(ctx, deps, sess, req)
			if err != nil {
				t.Fatalf("second RunAutoGrab: %v", err)
			}
			if second.GrabID != first.GrabID {
				t.Errorf("the re-submitted search created a SECOND row (%d vs %d) instead of updating the first", second.GrabID, first.GrabID)
			}
			list, err := grabsStore.List(ctx, tc.m)
			if err != nil {
				t.Fatalf("listing grabs: %v", err)
			}
			if len(list) != 1 {
				t.Fatalf("expected exactly one pending_retry row after two identical searches, got %d: %+v", len(list), list)
			}
			if list[0].RetryCount != 1 {
				t.Errorf("expected the retry count bumped to 1 on the second cycle, got %d", list[0].RetryCount)
			}
		})
	}
}

// TestRunAutoGrabRetryRowSeasonZeroIsNotNoSeason is the other collision the
// dedup key exists to prevent. TMDB uses season 0 for "Specials", which is the
// same int as the Go zero-value meaning "no season was picked at all" —
// SeasonSpecified is the only thing that separates them, and a key that omitted
// it would silently merge a deliberate Specials retry into a series-wide one.
func TestRunAutoGrabRetryRowSeasonZeroIsNotNoSeason(t *testing.T) {
	ctx := context.Background()
	_, _, settingsStore, grabsStore, _, _, _, _, _, _ := testStores(t)
	setAutoGrabToggle(t, settingsStore, true)

	deps := AutoGrabDeps{SettingsStore: settingsStore, GrabsStore: grabsStore}
	sess := &mode.Session{Mode: mode.Series}
	base := AutoGrabRequest{
		Mode: mode.Series, Title: "Some Show", TMDBID: 77,
		RootFolderPath: "/series", Trigger: TriggerRequest,
		Releases: []prowlarr.Release{qualifyingRelease()},
	}

	specials := base
	specials.Season, specials.SeasonSpecified = 0, true
	seriesWide := base // Season 0, SeasonSpecified false

	a, err := RunAutoGrab(ctx, deps, sess, specials)
	if err != nil {
		t.Fatalf("specials RunAutoGrab: %v", err)
	}
	b, err := RunAutoGrab(ctx, deps, sess, seriesWide)
	if err != nil {
		t.Fatalf("series-wide RunAutoGrab: %v", err)
	}
	if a.GrabID == b.GrabID {
		t.Fatal("a deliberate Season-0 (Specials) retry collapsed onto the no-season-picked row — SeasonSpecified is not discriminating")
	}
	list, err := grabsStore.List(ctx, mode.Series)
	if err != nil {
		t.Fatalf("listing grabs: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("expected two distinct retry rows, got %d", len(list))
	}
}

// TestSearchHandlerToggleOffIsUnchanged is AC 14 for Movies/Series: with the
// toggle off the endpoint behaves exactly as it ships today — the ranked
// []SearchReleaseResult list — and persists nothing.
func TestSearchHandlerToggleOffIsUnchanged(t *testing.T) {
	ctx := context.Background()
	prowlarrSrv := fakeProwlarr(t, `[{"guid":"1","title":"Some.Movie.2023.1080p.WEB-DL.x265-GROUP","indexer":"I","protocol":"torrent","size":8000000000,"seeders":50,"downloadUrl":"magnet:?xt=urn:btih:ABCDEF1234567890abcdef1234567890abcdef12"}]`)
	connStore, propStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, rowStore, releaseStore, rssFeedsStore := testStores(t)
	if err := connStore.Upsert(ctx, "prowlarr", prowlarrSrv.URL, "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	setAutoGrabToggle(t, settingsStore, false)

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, nil, propStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, rowStore, releaseStore, testFeedHealth(), rssFeedsStore, nil, nil, nil, nil, nil, nil, nil, nil, nil))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/modes/movies/search?q=some+movie")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var out []apidto.SearchReleaseResult
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("toggle-off response is not the unchanged candidate list: %v", err)
	}
	if len(out) != 1 || out[0].GUID != "1" {
		t.Fatalf("unexpected candidate list: %+v", out)
	}
	list, err := grabsStore.List(ctx, mode.Movies)
	if err != nil {
		t.Fatalf("listing grabs: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("toggle-off Search must persist nothing, wrote %d grab row(s)", len(list))
	}
}

// TestAdultSearchHandlerToggleOffIsUnchanged is AC 14's Adult half: the
// concrete-path Adult route still answers with its scene-card page.
func TestAdultSearchHandlerToggleOffIsUnchanged(t *testing.T) {
	ctx := context.Background()
	prowlarrSrv := fakeProwlarr(t, `[]`)
	connStore, propStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, rowStore, releaseStore, rssFeedsStore := testStores(t)
	if err := connStore.Upsert(ctx, "prowlarr", prowlarrSrv.URL, "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	setAutoGrabToggle(t, settingsStore, false)

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, nil, propStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, rowStore, releaseStore, testFeedHealth(), rssFeedsStore, nil, nil, nil, nil, nil, nil, nil, nil, nil))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/modes/adult/search?q=some+scene")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var page apidto.AdultSearchScenesPage
	if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
		t.Fatalf("toggle-off Adult response is not the unchanged scenes page: %v", err)
	}
	if page.Items == nil {
		t.Error("expected the unchanged AdultSearchScenesPage envelope")
	}
	list, err := grabsStore.List(ctx, mode.Adult)
	if err != nil {
		t.Fatalf("listing grabs: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("toggle-off Adult Search must persist nothing, wrote %d grab row(s)", len(list))
	}
}

// TestSearchRoutesToggleOnReturnOutcomeShape is AC 13's shape half.
// Adult's concrete search route still answers SearchAutoGrabOutcome when
// the toggle is on. Movies/Series GET /search always returns the pick list
// (auto-grab for those modes is POST /autograb from DetailPopup).
func TestSearchRoutesToggleOnReturnOutcomeShape(t *testing.T) {
	releasesJSON := `[{"guid":"1","title":"Some.Movie.2023.1080p.WEB-DL.x265-GROUP","indexer":"I","protocol":"torrent","size":8000000000,"seeders":50,"downloadUrl":"magnet:?xt=urn:btih:ABCDEF1234567890abcdef1234567890abcdef12"}]`

	t.Run("movies pick list even with toggle on", func(t *testing.T) {
		ctx := context.Background()
		prowlarrSrv := fakeProwlarr(t, releasesJSON)
		connStore, propStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, rowStore, releaseStore, rssFeedsStore := testStores(t)
		if err := connStore.Upsert(ctx, "prowlarr", prowlarrSrv.URL, "key"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		setAutoGrabToggle(t, settingsStore, true)
		if err := settingsStore.Set(ctx, moviesLibraryRootFolderKey, "/library"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, nil, propStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, rowStore, releaseStore, testFeedHealth(), rssFeedsStore, nil, nil, nil, nil, nil, nil, nil, nil, nil))
		defer srv.Close()

		resp, err := http.Get(srv.URL + "/api/modes/movies/search?q=some+movie")
		if err != nil {
			t.Fatalf("GET failed: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200, got %d", resp.StatusCode)
		}
		var list []apidto.SearchReleaseResult
		if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
			t.Fatalf("decoding pick list: %v", err)
		}
		if len(list) != 1 || list[0].Title == "" {
			t.Fatalf("expected a scored pick list, got %+v", list)
		}
		grabsList, err := grabsStore.List(ctx, mode.Movies)
		if err != nil {
			t.Fatalf("listing grabs: %v", err)
		}
		if len(grabsList) != 0 {
			t.Errorf("movies GET /search must not park a grab, wrote %d row(s)", len(grabsList))
		}
	})

	t.Run("adult via its own concrete route", func(t *testing.T) {
		ctx := context.Background()
		prowlarrSrv := fakeProwlarr(t, releasesJSON)
		connStore, propStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, rowStore, releaseStore, rssFeedsStore := testStores(t)
		if err := connStore.Upsert(ctx, "prowlarr", prowlarrSrv.URL, "key"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		setAutoGrabToggle(t, settingsStore, true)
		if err := settingsStore.Set(ctx, adultLibraryRootFolderKey, "/library"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, nil, propStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, rowStore, releaseStore, testFeedHealth(), rssFeedsStore, nil, nil, nil, nil, nil, nil, nil, nil, nil))
		defer srv.Close()

		resp, err := http.Get(srv.URL + "/api/modes/adult/search?q=some+scene")
		if err != nil {
			t.Fatalf("GET failed: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200, got %d", resp.StatusCode)
		}
		var outcome apidto.SearchAutoGrabOutcome
		if err := json.NewDecoder(resp.Body).Decode(&outcome); err != nil {
			t.Fatalf("decoding outcome: %v", err)
		}
		if outcome.Outcome != "pending_retry" || outcome.GrabID == 0 {
			t.Fatalf("expected an observable pending_retry outcome, got %+v", outcome)
		}
		if outcome.AutoGrabbed {
			t.Error("nothing qualified, so autoGrabbed must be false")
		}
		if outcome.Reason != noQualifyingCandidateReason {
			t.Errorf("unexpected reason %q", outcome.Reason)
		}

		g, err := grabsStore.Get(ctx, outcome.GrabID)
		if err != nil {
			t.Fatalf("loading the row the outcome names: %v", err)
		}
		if g.Status != grabs.PendingRetry || g.Mode != mode.Adult {
			t.Fatalf("unexpected parked row: %+v", g)
		}
	})
}

// TestRunAutoGrabTriggerRequestGrabsWhenRuntimeKnown is AC 13's dispatch half.
// It exercises the gated TriggerRequest path with the toggle ON and a known
// runtime, proving the gate lets a qualifying candidate through to
// dispatchToDownloadClient with no staging/review step in between.
func TestRunAutoGrabTriggerRequestGrabsWhenRuntimeKnown(t *testing.T) {
	ctx := context.Background()
	_, _, settingsStore, grabsStore, _, _, _, _, _, _ := testStores(t)
	dl := newTestDownloader("gid-req", t.TempDir())
	setAutoGrabToggle(t, settingsStore, true)
	if err := settingsStore.Set(ctx, qualityTierKey(mode.Movies), string(quality.Low)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	out, err := RunAutoGrab(ctx,
		AutoGrabDeps{SettingsStore: settingsStore, GrabsStore: grabsStore},
		&mode.Session{Mode: mode.Movies, Downloader: dl},
		AutoGrabRequest{
			Mode: mode.Movies, Title: "Some Movie", TMDBID: 42,
			RootFolderPath: "/movies", Trigger: TriggerRequest,
			Releases:       []prowlarr.Release{qualifyingRelease()},
			RuntimeSeconds: 6000,
		})
	if err != nil {
		t.Fatalf("RunAutoGrab: %v", err)
	}
	if !out.Grabbed || out.GrabID == 0 {
		t.Fatalf("expected the qualifying candidate to be auto-dispatched, got %+v", out)
	}
	if got := len(dl.List()); got != 1 {
		t.Fatalf("expected exactly one download-client add, got %d", got)
	}
	list, err := grabsStore.List(ctx, mode.Movies)
	if err != nil {
		t.Fatalf("listing grabs: %v", err)
	}
	if len(list) != 1 || list[0].Status == grabs.PendingRetry {
		t.Fatalf("expected one real (non-retry) grab row, got %+v", list)
	}
}

// TestRunAutoGrabEmptyReleasesParksRetryNotSearch pins the nil-vs-empty
// contract. A Search that legitimately returned zero releases must reach
// Selection.Fallback, NOT fall through to RunAutoGrab's internal search — that
// search is TMDB-id-driven and this route carries only ?q=, so it would fail
// instead of parking the retry AC 15 wants observable.
func TestRunAutoGrabEmptyReleasesParksRetryNotSearch(t *testing.T) {
	ctx := context.Background()
	_, _, settingsStore, grabsStore, _, _, _, _, _, _ := testStores(t)
	setAutoGrabToggle(t, settingsStore, true)

	out, err := RunAutoGrab(ctx,
		AutoGrabDeps{SettingsStore: settingsStore, GrabsStore: grabsStore},
		// A nil Prowlarr/TMDB session: reaching the internal search at all
		// would panic or error, which is exactly what this asserts against.
		&mode.Session{Mode: mode.Movies},
		AutoGrabRequest{
			Mode: mode.Movies, Title: "Some Movie",
			RootFolderPath: "/movies", Trigger: TriggerRequest,
			Releases: []prowlarr.Release{},
		})
	if err != nil {
		t.Fatalf("an empty candidate list must park a retry, not error: %v", err)
	}
	if !out.NoMatch || out.GrabID == 0 {
		t.Fatalf("expected a parked retry row, got %+v", out)
	}
}

// TestParkGrabForRetryStaysVisibleToDueForRetry is the round-trip the
// classifier test alone cannot catch.
//
// A grab parked after an ASYNCHRONOUS retrieval failure already holds a
// download GID, but DueForRetry filters on download_gid = ” — so if parking
// left the stale GID in place, the row would be parked and then never retried:
// invisible to the exact cycle it was parked for, with no error anywhere.
// (SetPendingRetry clears it; see its doc for the second guard this also
// protects, ActiveByDownloadGID's torrent re-grab false positive.)
func TestParkGrabForRetryStaysVisibleToDueForRetry(t *testing.T) {
	ctx := context.Background()
	_, _, settingsStore, grabsStore, _, _, _, _, _, _ := testStores(t)

	created, err := grabsStore.Create(ctx, grabs.Grab{
		Mode: mode.Movies, Title: "Some Movie", TMDBID: 42,
		Indexer: "I", Protocol: "usenet", DownloadClient: "nntp", RootFolderPath: "/movies",
	})
	if err != nil {
		t.Fatalf("creating grab: %v", err)
	}
	if err := grabsStore.SetDownloadGID(ctx, created.ID, "nzb-abc"); err != nil {
		t.Fatalf("setting gid: %v", err)
	}

	deps := AutoGrabDeps{SettingsStore: settingsStore, GrabsStore: grabsStore}
	if err := parkGrabForRetry(ctx, deps, created.ID, articlesUnavailableReason); err != nil {
		t.Fatalf("parking for retry: %v", err)
	}

	parked, err := grabsStore.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("loading parked row: %v", err)
	}
	if parked.Status != grabs.PendingRetry {
		t.Fatalf("expected pending_retry, got %q", parked.Status)
	}
	if parked.DownloadGID != "" {
		t.Errorf("the dead GID survived parking (%q) — DueForRetry will never see this row", parked.DownloadGID)
	}

	// The retry cycle must actually find it once retry_after has arrived.
	due, err := grabsStore.DueForRetry(ctx, time.Now().Add(2*defaultUsenetRetryIntervalSeconds*time.Second))
	if err != nil {
		t.Fatalf("DueForRetry: %v", err)
	}
	if len(due) != 1 || due[0].ID != created.ID {
		t.Fatalf("the parked grab is invisible to the retry cycle: %+v", due)
	}

	// ...and a re-grab of the same release must not be rejected as a duplicate
	// by the GID guard, since nothing is in flight any more.
	if _, err := grabsStore.ActiveByDownloadGID(ctx, mode.Movies, "nzb-abc"); err == nil {
		t.Error("a parked (not in-flight) grab still blocks a re-grab of the same GID")
	}
}

// TestClassifyDownloadStateUsenetFailures covers the permanent-vs-transient
// split. A 451 takedown is permanent and must never be retried; a 430 from
// every configured subscription is transient and must be.
func TestClassifyDownloadStateUsenetFailures(t *testing.T) {
	for _, tc := range []struct {
		name    string
		state   string
		failure error
		want    grabs.Status
	}{
		{"451 removed is permanent", "error", usenet.ErrArticleRemoved, grabs.Failed},
		{"430 everywhere is retryable", "error", usenet.ErrArticleNotFound, grabs.PendingRetry},
		{"no failure object (torrent path) with state=error stays Failed", "error", nil, grabs.Failed},
		{"complete is unaffected", "complete", nil, grabs.Completed},
		{"waiting is unaffected", "waiting", nil, grabs.Queued},
		{"active is unaffected", "active", nil, grabs.Downloading},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyDownloadState(tc.state, tc.failure); got != tc.want {
				t.Errorf("classifyDownloadState(%q, %v) = %q, want %q", tc.state, tc.failure, got, tc.want)
			}
		})
	}
}

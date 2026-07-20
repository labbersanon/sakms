package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/labbersanon/sakms/internal/apidto"
)

// fakeProwlarrRecording is fakeProwlarr's sibling: serves a static body but
// also records the last request's query string, so a test can assert the
// handler actually dispatched the right id-scoped/free-text search shape
// (mirrors search_test.go's fakeProwlarr, kept local since only this file's
// tests need the recorded query).
func fakeProwlarrRecording(t *testing.T, body string) (*httptest.Server, *url.Values) {
	t.Helper()
	var lastQuery url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lastQuery = r.URL.Query()
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, &lastQuery
}

// TestDiscoverAvailabilityHandler_Movies_BasicFetch is the Movies path:
// SearchByID (id-scoped, not free-text) + one MovieDetails runtime fetch,
// filtered through releasematch, graded, and placed in the right (resolution,
// tier, protocol) cell.
func TestDiscoverAvailabilityHandler_Movies_BasicFetch(t *testing.T) {
	tmdbSrv := fakeTMDBMovieRuntime(t, 100) // 100 min = 6000 s
	prowlarr, lastQuery := fakeProwlarrRecording(t, `[{"guid":"1","title":"Some.Movie.2023.1080p.WEB-DL.x265-GROUP","indexer":"I","protocol":"torrent","size":8000000000,"seeders":50,"downloadUrl":"magnet:?xt=urn:btih:ABCDEF1234567890abcdef1234567890abcdef12"}]`)

	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	ctx := context.Background()
	overrideFixedURL(t, "tmdb", tmdbSrv.URL)
	if err := connStore.Upsert(ctx, "tmdb", tmdbSrv.URL, "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := connStore.Upsert(ctx, "prowlarr", prowlarr.URL, "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, nil, nil, nil, nil, nil))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/modes/movies/discover/availability?tmdbId=42&title=" + url.QueryEscape("Some Movie"))
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	// SearchByID's id-scoped movie query, not a free-text Search — see
	// prowlarr.SearchByID's query-building doc.
	if got := lastQuery.Get("tmdbid"); got != "42" {
		t.Errorf("expected an id-scoped search carrying tmdbid=42, got query %v", lastQuery)
	}
	// Regression: the id params alone weren't reliably honored as a precise
	// filter by every indexer (see prowlarr.SearchByIDParams' Query field
	// doc) — the title must travel alongside them.
	if got := lastQuery.Get("query"); got != "Some Movie" {
		t.Errorf("expected the title to travel alongside the id params as query=, got %q", got)
	}
	if got := lastQuery.Get("type"); got != "movie" {
		t.Errorf("expected type=movie for a Movies id-scoped search, got %q", got)
	}

	var out apidto.AvailabilityPreview
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	// 8 GB / 6000 s x265 1080p clears every tier's 1080p floor (same fixture
	// as the autograb handler's qualified test).
	if out.Res1080.Low.Torrent == nil {
		t.Fatalf("expected a res1080/low/torrent candidate, got %+v", out.Res1080)
	}
	if out.Res1080.Low.Torrent.GUID != "1" {
		t.Errorf("unexpected candidate GUID: %+v", out.Res1080.Low.Torrent)
	}
	if out.Res1080.Low.Usenet != nil {
		t.Errorf("expected no usenet candidate (release was a torrent), got %+v", out.Res1080.Low.Usenet)
	}
	if out.Res720.Low.Torrent != nil || out.Res480.Low.Torrent != nil || out.Res2160.Low.Torrent != nil {
		t.Errorf("expected the 1080p release to appear in ONLY the res1080 bucket, got %+v", out)
	}
}

// TestDiscoverAvailabilityHandler_Series_SeasonEpisodeParams is the Series
// path: season/episode query params drive the id-scoped tvsearch AND the
// per-episode runtime lookup (seriesEpisodeRuntimeSeconds), exactly like
// autoGrabSearch's existing Series branch.
func TestDiscoverAvailabilityHandler_Series_SeasonEpisodeParams(t *testing.T) {
	tmdbSrv := fakeTMDBSeriesRuntime(t, 5, 58) // episode 5, 58 min = 3480 s
	prowlarr, lastQuery := fakeProwlarrRecording(t, `[{"guid":"2","title":"Some.Show.S03E05.1080p.WEB-DL.x265-GROUP","indexer":"I","protocol":"torrent","size":900000000,"seeders":50,"downloadUrl":"magnet:?xt=urn:btih:BBBBBB1234567890abcdef1234567890abcdef12"}]`)

	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	ctx := context.Background()
	overrideFixedURL(t, "tmdb", tmdbSrv.URL)
	if err := connStore.Upsert(ctx, "tmdb", tmdbSrv.URL, "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := connStore.Upsert(ctx, "prowlarr", prowlarr.URL, "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, nil, nil, nil, nil, nil))
	defer srv.Close()

	reqURL := srv.URL + "/api/modes/series/discover/availability?tmdbId=100&season=3&episode=5&title=" + urlQueryEscape("Some Show")
	resp, err := http.Get(reqURL)
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	if got := lastQuery.Get("tvdbid"); got != "789" { // fakeTMDBSeriesRuntime's external_ids stub
		t.Errorf("expected an id-scoped tvsearch carrying tvdbid=789, got query %v", lastQuery)
	}
	if got := lastQuery.Get("query"); got != "Some Show" {
		t.Errorf("expected the title to travel alongside the id params as query=, got %q", got)
	}
	if got := lastQuery.Get("season"); got != "3" {
		t.Errorf("expected season=3, got %q", got)
	}
	if got := lastQuery.Get("ep"); got != "5" {
		t.Errorf("expected ep=5, got %q", got)
	}

	var out apidto.AvailabilityPreview
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	// 900 MB / 3480 s x265 1080p clears the Low 1080p floor (same fixture as
	// the autograb handler's single-episode-qualifies test).
	if out.Res1080.Low.Torrent == nil || out.Res1080.Low.Torrent.GUID != "2" {
		t.Fatalf("expected the single episode to populate res1080/low/torrent, got %+v", out.Res1080)
	}
}

// TestDiscoverAvailabilityHandler_Adult_StudioTitleDuration_NoTMDBCall is the
// Adult path: studio+title free-text query (mirroring
// availability.CheckAdultScene) and durationSeconds supplying runtime
// directly — no TMDB connection is configured at all, proving the handler
// never requires (or calls) TMDB for Adult.
func TestDiscoverAvailabilityHandler_Adult_StudioTitleDuration_NoTMDBCall(t *testing.T) {
	prowlarr, lastQuery := fakeProwlarrRecording(t, `[{"guid":"3","title":"Some Studio - Wild Scene Title","indexer":"I","protocol":"torrent","size":900000000,"seeders":50,"downloadUrl":"magnet:?xt=urn:btih:CCCCCC1234567890abcdef1234567890abcdef12"}]`)

	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	ctx := context.Background()
	// Deliberately NOT configuring "tmdb" — the Adult path must not require it.
	if err := connStore.Upsert(ctx, "prowlarr", prowlarr.URL, "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, nil, nil, nil, nil, nil))
	defer srv.Close()

	reqURL := srv.URL + "/api/modes/adult/discover/availability?studio=" + urlQueryEscape("Some Studio") +
		"&title=" + urlQueryEscape("Wild Scene Title") + "&durationSeconds=3480"
	resp, err := http.Get(reqURL)
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	// availability.CheckAdultScene's free-text query shape: studio+title,
	// category 6000 (adultAutoGrabCategory) — not an id-scoped search.
	if got := lastQuery.Get("query"); got == "" {
		t.Errorf("expected a free-text query param, got query %v", lastQuery)
	}
	if got := lastQuery.Get("categories"); got != "6000" {
		t.Errorf("expected Adult's 6000 category, got %q", got)
	}

	var out apidto.AvailabilityPreview
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	// 900 MB / 3480 s (durationSeconds) x264-default (no codec tag → x264
	// baseline) at an unrecognized (0) resolution — release.Parse finds no
	// resolution token in "Some Studio - Wild Scene Title", so it lands in
	// NO resolution bucket at all. Assert the response decodes cleanly
	// (proves the Adult path ran end-to-end without a TMDB dependency) and
	// every bucket is empty, matching that expectation.
	if out.Res480.Low.Torrent != nil || out.Res720.Low.Torrent != nil ||
		out.Res1080.Low.Torrent != nil || out.Res2160.Low.Torrent != nil {
		t.Errorf("expected an unrecognized-resolution release to land in no bucket, got %+v", out)
	}
}

// TestDiscoverAvailabilityHandler_Adult_QueryIsPunctuationNormalized proves
// the punctuation-stripping fix (normalizeAdultQuery) reaches the actual
// Prowlarr request end-to-end through this handler, not just the unit-level
// TestNormalizeAdultQuery — a real "Adult downloads never resolve" report
// found the raw, unnormalized studio+title text almost never matching how
// trackers name Adult releases.
func TestDiscoverAvailabilityHandler_Adult_QueryIsPunctuationNormalized(t *testing.T) {
	prowlarr, lastQuery := fakeProwlarrRecording(t, `[]`)

	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	ctx := context.Background()
	if err := connStore.Upsert(ctx, "prowlarr", prowlarr.URL, "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, nil, nil, nil, nil, nil))
	defer srv.Close()

	reqURL := srv.URL + "/api/modes/adult/discover/availability?studio=" + urlQueryEscape("Private Classics") +
		"&title=" + urlQueryEscape("Franky Knight: Curvy And Horny, Looking For A Stallion") + "&durationSeconds=1800"
	resp, err := http.Get(reqURL)
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	want := "Private Classics Franky Knight Curvy And Horny Looking For A Stallion"
	if got := lastQuery.Get("query"); got != want {
		t.Errorf("query sent to Prowlarr = %q, want punctuation-stripped %q", got, want)
	}
}

// TestDiscoverAvailabilityHandler_Adult_ReleaseTitlePreferredOverStudioTitle
// is the regression test for the "still no downloads after the duration
// fix" report (2026-07-15): a query reconstructed from TPDB's own
// studio+title metadata includes tokens (e.g. TPDB's "S6:E10" episode
// notation) real indexer release filenames never contain, so it can find
// zero raw releases even when the exact release that matched the entity is
// still available. When releaseTitle is present, it must be used verbatim
// (normalized) instead of studio+title.
func TestDiscoverAvailabilityHandler_Adult_ReleaseTitlePreferredOverStudioTitle(t *testing.T) {
	prowlarr, lastQuery := fakeProwlarrRecording(t, `[]`)

	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	ctx := context.Background()
	if err := connStore.Upsert(ctx, "prowlarr", prowlarr.URL, "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, nil, nil, nil, nil, nil))
	defer srv.Close()

	reqURL := srv.URL + "/api/modes/adult/discover/availability?studio=" + urlQueryEscape("Step Siblings Caught") +
		"&title=" + urlQueryEscape("June 2026 Flavor Of The Month Poppy Applegate - S6:E10") +
		"&releaseTitle=" + urlQueryEscape("Step.Siblings.Caught.26.06.01.Poppy.Applegate.XXX.1080p-GROUP") +
		"&durationSeconds=1863"
	resp, err := http.Get(reqURL)
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	want := "Step Siblings Caught 26 06 01 Poppy Applegate XXX 1080p GROUP"
	if got := lastQuery.Get("query"); got != want {
		t.Errorf("query sent to Prowlarr = %q, want the normalized releaseTitle %q (studio+title must not be used when releaseTitle is present)", got, want)
	}
}

// urlQueryEscape is a tiny local alias so the test bodies above read cleanly
// without repeating the net/url import qualifier inline.
func urlQueryEscape(s string) string { return url.QueryEscape(s) }

// fakeTMDBSeriesSeasonRuntime is fakeTMDBSeriesRuntime's whole-season sibling
// — returns multiple episodes (not one) from /tv/{id}/season/{n}, for
// proving seriesSeasonTotalRuntimeSeconds sums every episode's runtime
// rather than resolving just one.
func fakeTMDBSeriesSeasonRuntime(t *testing.T, episodeRuntimeMinutes []int) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/tv/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/external_ids") {
			w.Write([]byte(`{"tvdb_id":789}`))
			return
		}
		episodes := make([]map[string]any, len(episodeRuntimeMinutes))
		for i, rt := range episodeRuntimeMinutes {
			episodes[i] = map[string]any{"episode_number": i + 1, "name": "Ep", "air_date": "2022-01-01", "runtime": rt}
		}
		json.NewEncoder(w).Encode(map[string]any{"episodes": episodes})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// TestDiscoverAvailabilityHandler_Series_WholeSeasonUsesSeasonTotalRuntime is
// the regression test for a real "nothing is being found to grab" report:
// autoGrabSearch/seriesEpisodeRuntimeSeconds deliberately returns 0 runtime
// for a whole-season request (episode=0, correct for auto-grab's safety
// posture), but this endpoint must substitute the season's TOTAL runtime
// instead — otherwise every candidate grades as unknown-bitrate and the grid
// is always empty for any whole-season check. 4 episodes x 30 min = 7200s
// total; a 1.7GB x265 1080p pack clears the Low floor (score ~3.02) but not
// Medium (~5) against that total — if the old bug were still present
// (runtime=0), EVERY cell would be nil instead.
func TestDiscoverAvailabilityHandler_Series_WholeSeasonUsesSeasonTotalRuntime(t *testing.T) {
	tmdbSrv := fakeTMDBSeriesSeasonRuntime(t, []int{30, 30, 30, 30}) // 7200s total
	prowlarr := fakeProwlarr(t, `[{"guid":"pack1","title":"Some.Show.S03.COMPLETE.1080p.WEB-DL.x265-GROUP","indexer":"I","protocol":"torrent","size":1700000000,"seeders":50,"downloadUrl":"magnet:?xt=urn:btih:DDDDDD1234567890abcdef1234567890abcdef12"}]`)

	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	ctx := context.Background()
	overrideFixedURL(t, "tmdb", tmdbSrv.URL)
	if err := connStore.Upsert(ctx, "tmdb", tmdbSrv.URL, "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := connStore.Upsert(ctx, "prowlarr", prowlarr.URL, "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, nil, nil, nil, nil, nil))
	defer srv.Close()

	// episode=0 (or omitted) is the whole-season case — the one that always
	// returned an empty grid before this fix.
	reqURL := srv.URL + "/api/modes/series/discover/availability?tmdbId=100&season=3&title=" + urlQueryEscape("Some Show")
	resp, err := http.Get(reqURL)
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var out apidto.AvailabilityPreview
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if out.Res1080.Low.Torrent == nil || out.Res1080.Low.Torrent.GUID != "pack1" {
		t.Fatalf("expected the season pack to populate res1080/low/torrent using the season's TOTAL runtime, got %+v (this is exactly the bug shape: an always-empty grid for whole-season checks)", out.Res1080)
	}
	if out.Res1080.Medium.Torrent != nil {
		t.Errorf("expected the pack to clear ONLY the Low floor against the season total, got Medium=%+v", out.Res1080.Medium)
	}
}

// TestDiscoverAvailabilityHandler_ResolutionAndTierAxesDistinguished is the
// plan's explicit two-axis proof: a high-bitrate 2160p AV1 release must
// light up EVERY tier at res2160 (its bitrate clears even the Lossless
// floor) while a low-bitrate 480p release only qualifies at res480/low —
// and neither release may appear in the OTHER release's resolution bucket,
// proving resolution and tier are graded as genuinely independent axes, not
// conflated.
func TestDiscoverAvailabilityHandler_ResolutionAndTierAxesDistinguished(t *testing.T) {
	tmdbSrv := fakeTMDBMovieRuntime(t, 100) // 100 min = 6000 s
	// 20 GB / 6000 s AV1 2160p: implied ~26.67 Mbps, x264-equiv ~76.19 Mbps
	// (AV1 divisor 0.35, no non-AV1 padding) — clears every tier's 2160p
	// floor (8/20/40/70).
	//
	// 375 MB / 6000 s x264 480p: implied 0.5 Mbps, x264-equiv 0.5, padded
	// score 0.4 — clears ONLY the Low 480p floor (0.3), not Medium (0.8).
	prowlarr := fakeProwlarr(t, `[
	  {"guid":"hi2160","title":"Some.Movie.2023.2160p.WEB-DL.DDP5.1.Atmos.HDR.DV.AV1-GROUP","indexer":"I","protocol":"torrent","size":20000000000,"seeders":50,"downloadUrl":"magnet:?xt=urn:btih:AAAAAA1234567890abcdef1234567890abcdef12"},
	  {"guid":"lo480","title":"Some.Movie.2023.480p.WEBRip.x264-GROUP","indexer":"I","protocol":"torrent","size":375000000,"seeders":50,"downloadUrl":"magnet:?xt=urn:btih:BBBBBB1234567890abcdef1234567890abcdef12"}
	]`)

	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	ctx := context.Background()
	overrideFixedURL(t, "tmdb", tmdbSrv.URL)
	if err := connStore.Upsert(ctx, "tmdb", tmdbSrv.URL, "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := connStore.Upsert(ctx, "prowlarr", prowlarr.URL, "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, nil, nil, nil, nil, nil))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/modes/movies/discover/availability?tmdbId=42&title=" + urlQueryEscape("Some Movie"))
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var out apidto.AvailabilityPreview
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decoding response: %v", err)
	}

	// The 2160p AV1 release must light up EVERY tier at res2160...
	for tierName, tier := range map[string]apidto.TierAvailability{
		"low": out.Res2160.Low, "medium": out.Res2160.Medium,
		"high": out.Res2160.High, "lossless": out.Res2160.Lossless,
	} {
		if tier.Torrent == nil || tier.Torrent.GUID != "hi2160" {
			t.Errorf("expected the 2160p AV1 release to qualify at res2160/%s, got %+v", tierName, tier)
		}
	}
	// ...and must NOT appear in res720/res1080/res480 at all (wrong
	// resolution bucket entirely).
	for resName, res := range map[string]apidto.ResolutionAvailability{
		"res480": out.Res480, "res720": out.Res720, "res1080": out.Res1080,
	} {
		if res.Low.Torrent != nil && res.Low.Torrent.GUID == "hi2160" {
			t.Errorf("2160p release leaked into %s/low — resolution buckets not isolated", resName)
		}
	}

	// The 480p release qualifies ONLY at res480/low...
	if out.Res480.Low.Torrent == nil || out.Res480.Low.Torrent.GUID != "lo480" {
		t.Fatalf("expected the 480p release to qualify at res480/low, got %+v", out.Res480.Low)
	}
	// ...and must NOT qualify at Medium/High/Lossless within res480 (bitrate
	// too low for those floors).
	if out.Res480.Medium.Torrent != nil || out.Res480.High.Torrent != nil || out.Res480.Lossless.Torrent != nil {
		t.Errorf("expected the 480p release to clear ONLY the Low floor, got %+v", out.Res480)
	}
	// Sanity: res720/res1080 have no candidates of either resolution, so
	// every cell there must be empty.
	if out.Res720.Low.Torrent != nil || out.Res1080.Low.Torrent != nil {
		t.Errorf("expected res720/res1080 to have no candidates at all, got res720=%+v res1080=%+v", out.Res720, out.Res1080)
	}
}

package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/labbersanon/sakms/internal/adultnewest"
	"github.com/labbersanon/sakms/internal/apidto"
	"github.com/labbersanon/sakms/internal/autograb"
	"github.com/labbersanon/sakms/internal/prowlarr"
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

	connStore, propStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	ctx := context.Background()
	overrideFixedURL(t, "tmdb", tmdbSrv.URL)
	if err := connStore.Upsert(ctx, "tmdb", tmdbSrv.URL, "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := connStore.Upsert(ctx, "prowlarr", prowlarr.URL, "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, nil, propStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), rssFeedsStore, nil, nil, nil, nil, nil, nil, nil, nil, nil))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/modes/movies/discover/availability?tmdbId=42&title=" + url.QueryEscape("Some Movie"))
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	// SearchByID's id-scoped movie query, not a free-text Search — ids
	// travel as {TmdbId:...}/{ImdbId:...} brace tokens inside query=, not
	// standalone tmdbid=/imdbid= params (see prowlarr.SearchByID's
	// query-building doc — confirmed against Prowlarr's own source).
	// Regression: the id params alone weren't reliably honored as a precise
	// filter by every indexer (see prowlarr.SearchByIDParams' Query field
	// doc) — the title must travel alongside them too.
	wantQuery := "{TmdbId:42} {ImdbId:tt1234567} Some Movie"
	if got := lastQuery.Get("query"); got != wantQuery {
		t.Errorf("expected an id-scoped search carrying query=%q, got %q (full query %v)", wantQuery, got, lastQuery)
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

// Claude 2026-08-11: pin the availability grid's Grab-field boundary.
// Reason: a quality-qualified release is still unusable without both its
// download URL and protocol; the grid must only expose candidates Grab accepts.
// Troubleshooting: reproduces the populated-cell/empty-downloadUrl production bug.
// Review if: Prowlarr Release makes these fields non-empty by construction.
func TestBuildAvailabilityPreview_SkipsMissingDownloadURLOrProtocol(t *testing.T) {
	const validURL = "magnet:?xt=urn:btih:CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC"
	valid := prowlarr.Release{
		GUID: "valid", Title: "Some.Movie.2023.1080p.WEB-DL.x265-GROUP",
		Protocol: prowlarr.Torrent, Size: 8_000_000_000, Seeders: 10, DownloadURL: validURL,
	}
	tests := []struct {
		name    string
		invalid prowlarr.Release
	}{
		{
			name: "empty DownloadURL",
			invalid: prowlarr.Release{
				GUID: "missing-url", Title: "Some.Movie.2023.1080p.WEB-DL.x265-GROUP",
				Protocol: prowlarr.Torrent, Size: 8_000_000_000, Seeders: 100,
			},
		},
		{
			name: "empty Protocol",
			invalid: prowlarr.Release{
				GUID: "missing-protocol", Title: "Some.Movie.2023.1080p.WEB-DL.x265-GROUP",
				Size: 8_000_000_000, Seeders: 100, DownloadURL: validURL,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			onlyInvalid := []prowlarr.Release{tt.invalid}
			preview := buildAvailabilityPreview(buildAutoGrabCandidates(onlyInvalid, 6_000, false), onlyInvalid, 1, nil)
			if got := countPopulatedCells(preview); got != 0 {
				t.Fatalf("ungrabbable release populated %d cell(s): %+v", got, preview)
			}

			withValid := []prowlarr.Release{tt.invalid, valid}
			preview = buildAvailabilityPreview(buildAutoGrabCandidates(withValid, 6_000, false), withValid, 1, nil)
			got := preview.Res1080.Low.Torrent
			if got == nil || got.GUID != valid.GUID || got.DownloadURL != validURL {
				t.Fatalf("valid neighbor was not selected after filtering invalid release: %+v", got)
			}
		})
	}
}

// TestDiscoverAvailabilityHandler_Series_SeasonEpisodeParams is the Series
// path: season/episode query params drive the id-scoped tvsearch AND the
// per-episode runtime lookup (seriesEpisodeRuntimeSeconds), exactly like
// autoGrabSearch's existing Series branch.
func TestDiscoverAvailabilityHandler_Series_SeasonEpisodeParams(t *testing.T) {
	tmdbSrv := fakeTMDBSeriesRuntime(t, 5, 58) // episode 5, 58 min = 3480 s
	prowlarr, lastQuery := fakeProwlarrRecording(t, `[{"guid":"2","title":"Some.Show.S03E05.1080p.WEB-DL.x265-GROUP","indexer":"I","protocol":"torrent","size":900000000,"seeders":50,"downloadUrl":"magnet:?xt=urn:btih:BBBBBB1234567890abcdef1234567890abcdef12"}]`)

	connStore, propStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	ctx := context.Background()
	overrideFixedURL(t, "tmdb", tmdbSrv.URL)
	if err := connStore.Upsert(ctx, "tmdb", tmdbSrv.URL, "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := connStore.Upsert(ctx, "prowlarr", prowlarr.URL, "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, nil, propStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), rssFeedsStore, nil, nil, nil, nil, nil, nil, nil, nil, nil))
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

	// {TvdbId:789} (fakeTMDBSeriesRuntime's external_ids stub) +
	// {Season:3} + {Episode:5} + the title — see prowlarr.SearchByID's doc:
	// ids/season/episode travel as brace tokens inside query=, not as
	// standalone tvdbid=/season=/ep= params.
	wantQuery := "{TvdbId:789} {Season:3} {Episode:5} Some Show"
	if got := lastQuery.Get("query"); got != wantQuery {
		t.Errorf("expected an id-scoped tvsearch carrying query=%q, got %q (full query %v)", wantQuery, got, lastQuery)
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

	connStore, propStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	ctx := context.Background()
	// Deliberately NOT configuring "tmdb" — the Adult path must not require it.
	if err := connStore.Upsert(ctx, "prowlarr", prowlarr.URL, "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, nil, propStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), rssFeedsStore, nil, nil, nil, nil, nil, nil, nil, nil, nil))
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
// found the raw, unnormalized text almost never matching how trackers name
// Adult releases. Also proves exactly ONE Prowlarr search fires (via
// fakeProwlarrPerQuery's ordered-query list, not fakeProwlarrRecording,
// which would silently hide a second call if one crept back in) — a
// regression guard for the query-cascade simplification below.
func TestDiscoverAvailabilityHandler_Adult_QueryIsPunctuationNormalized(t *testing.T) {
	prowlarr, queries := fakeProwlarrPerQuery(t, nil)

	connStore, propStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	ctx := context.Background()
	if err := connStore.Upsert(ctx, "prowlarr", prowlarr.URL, "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, nil, propStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), rssFeedsStore, nil, nil, nil, nil, nil, nil, nil, nil, nil))
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

	want := "Franky Knight Curvy And Horny Looking For A Stallion"
	if len(*queries) != 1 {
		t.Fatalf("expected exactly 1 Prowlarr search (title-only), got %d: %q", len(*queries), *queries)
	}
	if (*queries)[0] != want {
		t.Errorf("query = %q, want punctuation-stripped, title-only %q", (*queries)[0], want)
	}
}

// TestDiscoverAvailabilityHandler_Adult_QueryIsTitleOnly is the current-
// behavior test for what were previously two separate cascade-order tests —
// ReleaseTitlePreferredOverStudioTitle (2026-07-15) and
// ReleaseTitleQueryCleaned (2026-07-31), both retired 2026-08-11 when the
// releaseTitle-preferred / Studio+Title-fallback query cascade they pinned
// was replaced outright by a single title-only query (see autoGrabSearch's
// doc comment for the full three-attempt revision history and why). Both
// old tests asserted the QUERY SENT differed depending on whether/how
// releaseTitle was cleaned; that distinction no longer exists, since
// releaseTitle is not read for query construction at all any more. This
// test proves exactly that: studio AND releaseTitle are BOTH present in the
// request (using each old test's own fixture values, concatenated) and
// BOTH are ignored — only title reaches Prowlarr, normalized.
func TestDiscoverAvailabilityHandler_Adult_QueryIsTitleOnly(t *testing.T) {
	prowlarr, queries := fakeProwlarrPerQuery(t, nil)

	connStore, propStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	ctx := context.Background()
	if err := connStore.Upsert(ctx, "prowlarr", prowlarr.URL, "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, nil, propStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), rssFeedsStore, nil, nil, nil, nil, nil, nil, nil, nil, nil))
	defer srv.Close()

	reqURL := srv.URL + "/api/modes/adult/discover/availability?studio=" + urlQueryEscape("Taboo Heat") +
		"&title=" + urlQueryEscape("June 2026 Flavor Of The Month Poppy Applegate - S6:E10") +
		"&releaseTitle=" + urlQueryEscape("TabooHeat.26.07.18.Cory.Chase.In.Step.Mom.Has.One.Wish.BBC.Gangbang.XXX.720p.HEVC.x265.PRT") +
		"&durationSeconds=1800"
	resp, err := http.Get(reqURL)
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	// The dash and colon in "... Applegate - S6:E10" are non-alnum and
	// normalize to spaces; neither "Taboo Heat" (studio) nor any token from
	// releaseTitle ("TabooHeat", "Cory", "Chase", "Gangbang", ...) appears.
	want := "June 2026 Flavor Of The Month Poppy Applegate S6 E10"
	if len(*queries) != 1 {
		t.Fatalf("expected exactly 1 Prowlarr search (title-only, studio and releaseTitle both ignored), got %d: %q", len(*queries), *queries)
	}
	if (*queries)[0] != want {
		t.Errorf("query = %q, want title-only %q", (*queries)[0], want)
	}
}

// fakeProwlarrPerQuery is fakeProwlarrRecording's two-query sibling: it answers
// each request from bodies keyed by the exact `query` param and appends every
// query it saw to an ordered slice. fakeProwlarrRecording can't express this —
// it serves one static body and OVERWRITES its recorded query — and both
// properties are needed to prove a fallback: a first query that finds nothing,
// a second that finds something, and the order they went out in.
func fakeProwlarrPerQuery(t *testing.T, bodyByQuery map[string]string) (*httptest.Server, *[]string) {
	t.Helper()
	var queries []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("query")
		queries = append(queries, q)
		w.Header().Set("Content-Type", "application/json")
		body, ok := bodyByQuery[q]
		if !ok {
			body = `[]`
		}
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, &queries
}

// TestDiscoverAvailabilityHandler_Adult_TitleOnlyQuerySurfacesNZBAlongsideTorrent
// is the regression test for the real 2026-08-11 live bug (Discover's
// "Teasing Cheerleader" scene) that motivated dropping the whole
// releaseTitle/Studio+Title query cascade for a single title-only query —
// see autoGrabSearch's doc comment for the full three-attempt revision
// history (a truncated pooled releaseTitle, then a disproven
// no-space-studio variant, then this). Confirmed live: NZBGeek (a real
// configured Usenet indexer) only ever matched a query with the studio
// dropped entirely; the old cascade never sent one. Discover showed 2
// torrent results, both correctly rejected for too few seeders, and
// reported that as the whole story, while 3 real NZB releases sat in
// Prowlarr the entire time. This test's single-query fixture returns BOTH a
// torrent and an NZB release for the one title-only query, and asserts both
// independently populate the grid (one per protocol) — proving the real
// fix, not just that a query fired.
func TestDiscoverAvailabilityHandler_Adult_TitleOnlyQuerySurfacesNZBAlongsideTorrent(t *testing.T) {
	// 900 MB / 3480 s x265 1080p — the same fixture this file already
	// documents as clearing the Low 1080p floor, for both protocols.
	const body = `[` +
		`{"guid":"9","title":"CathysCraving.Pixie.Smalls.Teasing.Cheerleader.XXX.1080p.x265-GROUP","indexer":"I","protocol":"torrent","size":900000000,"seeders":50,"downloadUrl":"magnet:?xt=urn:btih:DDDDDD1234567890abcdef1234567890abcdef12"},` +
		`{"guid":"10","title":"CathysCraving.Pixie.Smalls.Teasing.Cheerleader.XXX.1080p.x265-NZBGRP","indexer":"NZBGeek","protocol":"usenet","size":900000000,"downloadUrl":"https://nzbgeek.example/dl/10"}` +
		`]`

	prowlarr, queries := fakeProwlarrPerQuery(t, map[string]string{
		"Pixie Smalls Teasing Cheerleader": body,
	})

	connStore, propStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	ctx := context.Background()
	if err := connStore.Upsert(ctx, "prowlarr", prowlarr.URL, "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, nil, propStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), rssFeedsStore, nil, nil, nil, nil, nil, nil, nil, nil, nil))
	defer srv.Close()

	reqURL := srv.URL + "/api/modes/adult/discover/availability?studio=" + urlQueryEscape("Cathys Craving") +
		"&title=" + urlQueryEscape("Pixie Smalls Teasing Cheerleader") +
		"&releaseTitle=" + urlQueryEscape("CathysCraving 26 02 08 Scene 1000 Pixie Smalls Teasing Cheerlea") +
		"&durationSeconds=3480"
	resp, err := http.Get(reqURL)
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	if len(*queries) != 1 {
		t.Fatalf("expected exactly 1 Prowlarr search (title-only), got %d: %q", len(*queries), *queries)
	}
	if want := "Pixie Smalls Teasing Cheerleader"; (*queries)[0] != want {
		t.Errorf("query = %q, want title-only %q (studio and releaseTitle both ignored)", (*queries)[0], want)
	}

	var out apidto.AvailabilityPreview
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if out.Res1080.Low.Torrent == nil || out.Res1080.Low.Torrent.GUID != "9" {
		t.Fatalf("expected the torrent release to populate res1080/low/torrent, got %+v", out.Res1080)
	}
	if out.Res1080.Low.Usenet == nil || out.Res1080.Low.Usenet.GUID != "10" {
		t.Fatalf("expected the NZB release to populate res1080/low/usenet — this is the exact bug fixed 2026-08-11, got %+v", out.Res1080)
	}
}

// urlQueryEscape is a tiny local alias so the test bodies above read cleanly
// without repeating the net/url import qualifier inline.
func urlQueryEscape(s string) string { return url.QueryEscape(s) }

// fakeProwlarrCounting is a fake Prowlarr server that counts calls and returns
// the given body for every request. The counter is safe for concurrent access.
// Use it to assert the exact number of Prowlarr calls made per test.
func fakeProwlarrCounting(t *testing.T, body string) (*httptest.Server, *int32) {
	t.Helper()
	var count int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&count, 1)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, &count
}

// testAdultAvailabilitySetup builds a test server wired to a real ReleaseStore,
// optionally registering prowlarrURL as the Prowlarr connection. Call it from
// DB-requiring AC tests instead of constructing stores by hand each time.
func testAdultAvailabilitySetup(t *testing.T, prowlarrURL string) (*httptest.Server, *adultnewest.ReleaseStore) {
	t.Helper()
	connStore, propStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	if prowlarrURL != "" {
		if err := connStore.Upsert(context.Background(), "prowlarr", prowlarrURL, "key"); err != nil {
			t.Fatalf("registering prowlarr: %v", err)
		}
	}
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, nil, propStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), rssFeedsStore, nil, nil, nil, nil, nil, nil, nil, nil, nil))
	t.Cleanup(srv.Close)
	return srv, adultNewestReleaseStore
}

// TestDiscoverAvailability_Adult_FirstOpenSearchesAndPersistsRawList (AC1).
// A 4-release Prowlarr response (1 title-mismatch) is persisted in full —
// all 4 rows land in adult_release_cache; the grid only shows the 3 that
// pass FilterReleases' title match.
func TestDiscoverAvailability_Adult_FirstOpenSearchesAndPersistsRawList(t *testing.T) {
	const sceneTitle = "Teasing Cheerleader"
	const body = `[
		{"guid":"r1","title":"CathysCraving.Teasing.Cheerleader.XXX.1080p.x265-A","indexer":"NZBGeek","protocol":"usenet","size":900000000,"downloadUrl":"https://nzb.example/r1"},
		{"guid":"r2","title":"CathysCraving.Teasing.Cheerleader.XXX.1080p.x265-B","indexer":"NZBGeek","protocol":"usenet","size":900000000,"downloadUrl":"https://nzb.example/r2"},
		{"guid":"r3","title":"CathysCraving.Teasing.Cheerleader.XXX.1080p.x265-C","indexer":"NZBGeek","protocol":"usenet","size":900000000,"downloadUrl":"https://nzb.example/r3"},
		{"guid":"r4","title":"CompletelyUnrelated.Random.Movie.2020-GROUP","indexer":"NZBGeek","protocol":"usenet","size":900000000,"downloadUrl":"https://nzb.example/r4"}
	]`

	prowlarrSrv, prowlarrCount := fakeProwlarrCounting(t, body)
	srv, releaseStore := testAdultAvailabilitySetup(t, prowlarrSrv.URL)

	reqURL := fmt.Sprintf("%s/api/modes/adult/discover/availability?box=stashdb&sceneId=ac1-scene&title=%s&durationSeconds=3480",
		srv.URL, urlQueryEscape(sceneTitle))
	resp, err := http.Get(reqURL)
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	if n := atomic.LoadInt32(prowlarrCount); n != 1 {
		t.Errorf("expected exactly 1 Prowlarr call on first open, got %d", n)
	}

	// All 4 raw releases (including the title-mismatch) must be in the cache.
	ctx := context.Background()
	fresh, err := releaseStore.FreshReleasesForScene(ctx, "stashdb:ac1-scene", time.Now())
	if err != nil {
		t.Fatalf("querying cache: %v", err)
	}
	if len(fresh) != 4 {
		t.Errorf("expected all 4 raw releases persisted (including title-mismatch), got %d", len(fresh))
	}
}

// TestDiscoverAvailability_Adult_SecondOpenMakesZeroProwlarrCalls (AC3).
// Two identical GETs for the same scene: first open fires one Prowlarr call
// and persists; second open reads from cache — zero new calls.
func TestDiscoverAvailability_Adult_SecondOpenMakesZeroProwlarrCalls(t *testing.T) {
	const body = `[{"guid":"ac3","title":"CathysCraving.Teasing.Cheerleader.XXX.1080p.x265-AC3","indexer":"NZBGeek","protocol":"usenet","size":900000000,"downloadUrl":"https://nzb.example/ac3"}]`

	prowlarrSrv, prowlarrCount := fakeProwlarrCounting(t, body)
	srv, releaseStore := testAdultAvailabilitySetup(t, prowlarrSrv.URL)

	reqURL := fmt.Sprintf("%s/api/modes/adult/discover/availability?box=stashdb&sceneId=ac3-scene&title=%s&durationSeconds=3480",
		srv.URL, urlQueryEscape("Teasing Cheerleader"))

	// First open — expect exactly 1 Prowlarr call.
	resp1, err := http.Get(reqURL)
	if err != nil {
		t.Fatalf("first GET failed: %v", err)
	}
	defer resp1.Body.Close()
	if resp1.StatusCode != http.StatusOK {
		t.Fatalf("first GET: expected 200, got %d", resp1.StatusCode)
	}

	if n := atomic.LoadInt32(prowlarrCount); n != 1 {
		t.Fatalf("expected 1 Prowlarr call after first open, got %d", n)
	}

	// Guard: verify the row is actually in the cache before the second open.
	ctx := context.Background()
	fresh, err := releaseStore.FreshReleasesForScene(ctx, "stashdb:ac3-scene", time.Now())
	if err != nil {
		t.Fatalf("checking cache: %v", err)
	}
	if len(fresh) == 0 {
		t.Fatalf("expected fresh cache rows after first open; PersistReleases/link path is broken")
	}

	// Second open — must NOT add a Prowlarr call.
	resp2, err := http.Get(reqURL)
	if err != nil {
		t.Fatalf("second GET failed: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("second GET: expected 200, got %d", resp2.StatusCode)
	}

	if n := atomic.LoadInt32(prowlarrCount); n != 1 {
		t.Errorf("expected 0 additional Prowlarr calls on cache-hit second open, total count = %d (AC3)", n)
	}
}

// TestDiscoverAvailability_Adult_NoStudioNoPerformersStillReturnsGrid is the
// b2b7db4 production-regression guard. The old aiConfirmAdultReleases with no
// studio/no AI client returned an empty list — a matching release was silently
// dropped, leaving an empty grid. Without that gate, the title-matching release
// now survives FilterReleases and populates the grid.
func TestDiscoverAvailability_Adult_NoStudioNoPerformersStillReturnsGrid(t *testing.T) {
	const body = `[{"guid":"b2b7","title":"CathysCraving.Teasing.Cheerleader.XXX.1080p.x265-A","indexer":"NZBGeek","protocol":"usenet","size":900000000,"downloadUrl":"https://nzb.example/b2b7"}]`

	prowlarrSrv, _ := fakeProwlarrCounting(t, body)
	srv, _ := testAdultAvailabilitySetup(t, prowlarrSrv.URL)

	// No studio, no performers — the shape that produced an empty grid at b2b7db4.
	reqURL := fmt.Sprintf("%s/api/modes/adult/discover/availability?title=%s&durationSeconds=3480",
		srv.URL, urlQueryEscape("Teasing Cheerleader"))
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
	if countPopulatedCells(out) == 0 {
		t.Error("expected at least one populated grid cell — b2b7db4 regression: aiConfirmAdultReleases with no AI/studio returned empty")
	}
}

// Claude 2026-08-11: regression coverage for card-enclosure availability.
// Reason: a valid RSS/Show More link must survive a zero-result Prowlarr search
// and remain manually grabbable even when the torrent has no seeder metadata.
// Troubleshooting: this failed in production as the popup's zero-release empty
// state despite the Discover card already carrying a valid enclosure.
// Review if: known enclosures move to a separate manual-candidate response field.
func TestDiscoverAvailability_Adult_KnownEnclosureSurvivesZeroProwlarrResults(t *testing.T) {
	prowlarrSrv, prowlarrCount := fakeProwlarrCounting(t, `[]`)
	srv, _ := testAdultAvailabilitySetup(t, prowlarrSrv.URL)

	const (
		title       = "Known Enclosure Scene"
		releaseName = "Vixen.Known.Enclosure.Scene.XXX.1080p.x265-GROUP"
		downloadURL = "magnet:?xt=urn:btih:KNOWNENCLOSURE"
	)
	reqURL := fmt.Sprintf(
		"%s/api/modes/adult/discover/availability?title=%s&releaseTitle=%s&durationSeconds=1800&downloadUrl=%s&protocol=torrent&sizeBytes=900000000",
		srv.URL, urlQueryEscape(title), urlQueryEscape(releaseName), urlQueryEscape(downloadURL),
	)
	resp, err := http.Get(reqURL)
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if n := atomic.LoadInt32(prowlarrCount); n != 1 {
		t.Fatalf("expected one zero-result Prowlarr search, got %d", n)
	}

	var out apidto.AvailabilityPreview
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if countPopulatedCells(out) == 0 {
		t.Fatal("expected the known enclosure to force a populated availability grid")
	}
	got := out.Res1080.Low.Torrent
	if got == nil {
		t.Fatalf("expected the known 1080p torrent in res1080/low/torrent, got %+v", out.Res1080)
	}
	if got.DownloadURL != downloadURL || got.Protocol != "torrent" || got.Size != 900000000 {
		t.Errorf("forced enclosure fields = %+v, want URL/protocol/size from the card", got)
	}
	if out.Diagnostics.RawReleaseCount != 1 || out.Diagnostics.MatchedReleaseCount != 1 {
		t.Errorf("diagnostics = %+v, want one known raw/matched enclosure", out.Diagnostics)
	}
}

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

	connStore, propStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	ctx := context.Background()
	overrideFixedURL(t, "tmdb", tmdbSrv.URL)
	if err := connStore.Upsert(ctx, "tmdb", tmdbSrv.URL, "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := connStore.Upsert(ctx, "prowlarr", prowlarr.URL, "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, nil, propStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), rssFeedsStore, nil, nil, nil, nil, nil, nil, nil, nil, nil))
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

	connStore, propStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	ctx := context.Background()
	overrideFixedURL(t, "tmdb", tmdbSrv.URL)
	if err := connStore.Upsert(ctx, "tmdb", tmdbSrv.URL, "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := connStore.Upsert(ctx, "prowlarr", prowlarr.URL, "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, nil, propStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), rssFeedsStore, nil, nil, nil, nil, nil, nil, nil, nil, nil))
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

// TestDiscoverAvailabilityHandler_DiagnosticsDistinguishEmptyGridCauses pins
// the response field the popup needs to tell an empty grid's two causes
// apart. Before it existed, "Prowlarr found nothing" and "Prowlarr found
// releases and every one was rejected" both arrived as an identical all-nil
// grid, so the popup could only render silently-disabled selectors with no
// explanation at all (a real operator report). Both cases go through the
// Adult path deliberately: Adult's own lower seeder floor (adultMinSeeders,
// autograb.go) is the single most common real-world rejection cause here.
func TestDiscoverAvailabilityHandler_DiagnosticsDistinguishEmptyGridCauses(t *testing.T) {
	// 8 GB / 3480 s at 1080p clears every tier's bitrate floor comfortably,
	// so seeders=1 (under adultMinSeeders' 3) is the ONLY thing rejecting it
	// — GradeCandidate checks the seeder floor before the bitrate floor, so
	// the status is unambiguous.
	const rejectedBody = `[{"guid":"9","title":"Some Studio - Wild Scene Title 1080p","indexer":"I","protocol":"torrent","size":8000000000,"seeders":1,"downloadUrl":"magnet:?xt=urn:btih:EEEEEE1234567890abcdef1234567890abcdef12"}]`

	tests := []struct {
		name        string
		body        string
		wantRaw     int
		wantMatched int
		wantReason  string // "" = expect no reasons at all
	}{
		{name: "no releases found at all", body: `[]`, wantRaw: 0, wantMatched: 0},
		{name: "found but rejected on the seeder floor", body: rejectedBody, wantRaw: 1, wantMatched: 1, wantReason: string(autograb.StatusLowSeeders)},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			prowlarr, _ := fakeProwlarrRecording(t, tc.body)

			connStore, propStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
			if err := connStore.Upsert(context.Background(), "prowlarr", prowlarr.URL, "key"); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, nil, propStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), rssFeedsStore, nil, nil, nil, nil, nil, nil, nil, nil, nil))
			defer srv.Close()

			// No releaseTitle param — this exercises the plain studio+title
			// query, so US-001's zero-result fallback never fires and the raw
			// count reflects exactly one search.
			resp, err := http.Get(srv.URL + "/api/modes/adult/discover/availability?studio=" + urlQueryEscape("Some Studio") +
				"&title=" + urlQueryEscape("Wild Scene Title") + "&durationSeconds=3480")
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

			if got := countPopulatedCells(out); got != 0 {
				t.Fatalf("expected an empty grid for this fixture, got %d populated cells", got)
			}
			if out.Diagnostics.RawReleaseCount != tc.wantRaw {
				t.Errorf("rawReleaseCount = %d, want %d", out.Diagnostics.RawReleaseCount, tc.wantRaw)
			}
			if out.Diagnostics.MatchedReleaseCount != tc.wantMatched {
				t.Errorf("matchedReleaseCount = %d, want %d", out.Diagnostics.MatchedReleaseCount, tc.wantMatched)
			}
			if tc.wantReason == "" {
				if len(out.Diagnostics.RejectionReasons) != 0 {
					t.Errorf("expected no rejection reasons when nothing reached grading, got %v", out.Diagnostics.RejectionReasons)
				}
				return
			}
			if !slices.Contains(out.Diagnostics.RejectionReasons, tc.wantReason) {
				t.Errorf("expected rejection reasons to name %q, got %v", tc.wantReason, out.Diagnostics.RejectionReasons)
			}
		})
	}
}

package api

// Route-REGISTRATION coverage for the three Calendar endpoints, driven through
// the real NewMux rather than a bare http.NewServeMux with one handler bolted
// onto it.
//
// Why this file exists at all: every other test for these handlers constructs
// its own mux and registers the handler function directly, which bypasses
// handler.go entirely. Delete all three registrations from NewMux and the whole
// Go suite stays green while the feature 404s in production. This is the
// project's established convention elsewhere (adult_mode_test.go,
// adultdiscover_test.go, adult_dedup_test.go all drive httptest.NewServer over
// NewMux) — it was simply never applied to these three.
//
// SCOPE, deliberately narrow: these assert only that each pattern is REACHABLE
// through the fully-wired mux — a non-404 with the route's own verb. Handler
// behaviour is thoroughly covered by calendar_upcoming_test.go and
// calendar_prerelease_test.go and is not re-tested here.
//
// Two ways to write this test wrong, both of which pass vacuously:
//
//   - Omitting from/to on the GETs. calendarWindow rejects that with 400, which
//     is a PASS under a != 404 assertion, so it would prove nothing about
//     registration — the request never had to match a pattern to fail that way.
//     Real params are supplied so a miss is the only thing 404 can mean.
//   - Using the wrong verb. Go 1.22 method-scoped patterns answer a GET on a
//     POST-only route with 405, not 404, so a GET against prerelease-request
//     would report "registered" even if it were not.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCalendarRoutesAreRegisteredOnTheRealMux(t *testing.T) {
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, nil, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), rssFeedsStore, nil, nil, nil, nil, nil, nil))
	defer srv.Close()

	const window = "?from=2026-08-01&to=2026-08-31"
	for _, tc := range []struct {
		name, method, path, body string
	}{
		{
			name:   "upcoming movies",
			method: http.MethodGet,
			path:   "/api/calendar/upcoming/movies" + window,
		},
		{
			name:   "upcoming series",
			method: http.MethodGet,
			path:   "/api/calendar/upcoming/series" + window,
		},
		{
			name:   "pre-release request",
			method: http.MethodPost,
			path:   "/api/calendar/prerelease-request",
			body:   `{"tmdbId":42,"title":"Some Movie","releaseDate":"2026-12-01"}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequest(tc.method, srv.URL+tc.path, strings.NewReader(tc.body))
			if err != nil {
				t.Fatalf("building the request: %v", err)
			}
			req.Header.Set("Content-Type", "application/json")
			resp, err := srv.Client().Do(req)
			if err != nil {
				t.Fatalf("%s %s: %v", tc.method, tc.path, err)
			}
			defer resp.Body.Close()

			// Any status but 404/405 means the pattern matched and the handler
			// ran. What it then decided (400 for an unconfigured TMDB, 200, …)
			// is another file's business — see this file's header.
			if resp.StatusCode == http.StatusNotFound {
				t.Errorf("%s %s returned 404 — the route is not registered on NewMux, so this endpoint does not exist in production no matter what its handler tests say", tc.method, tc.path)
			}
			if resp.StatusCode == http.StatusMethodNotAllowed {
				t.Errorf("%s %s returned 405 — the route is registered under a different method than the frontend calls", tc.method, tc.path)
			}
		})
	}
}

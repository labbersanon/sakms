package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/labbersanon/sakms/internal/apidto"
	"github.com/labbersanon/sakms/internal/auth"
	"github.com/labbersanon/sakms/internal/excludes"
	"github.com/labbersanon/sakms/internal/grabs"
	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/sectionlock"
	"github.com/labbersanon/sakms/internal/settings"
)

// sectionlock_w2_test.go — W2's coverage closure: §4.3's explicit Layer 3
// checks, §4.5's SSE re-check (SL-18's integration half) and §4.4's
// PUT /api/auth/mode PIN gate (SL-20).
//
// The W2-a cases all run against newLayer2Fixture, which wraps the REAL
// NewMux in a gated auth.Middleware with adult-content locked and a PIN set.
// That matters: every route below classifies as something OTHER than
// adult-content ({discover}, {queue}, {organize}), so Layer 1 lets all of
// them through by design and whatever denies them is the explicit check.
//
// Where a route has a legitimate Adult use, the case is a PAIR: the same
// request carrying a correct X-Section-Pin must stop being denied. That is
// what separates "the lock denied it" from "this was going to fail anyway."

// w2Do issues an arbitrary-method request against a fixture's server.
func w2Do(t *testing.T, srvURL, method, path string, body any, decorate ...func(*http.Request)) *http.Response {
	t.Helper()
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshalling body: %v", err)
		}
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequest(method, srvURL+path, reader)
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	for _, d := range decorate {
		d(req)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

// --- W2-a: /api/discover/rss-feeds* (§4.3 item 1, named by AC6) ----------

// createAdultFeed seeds one target:"adult" feed THROUGH the API, presenting
// the PIN — so the seeding path is itself a proof that the create check
// admits a correctly credentialed request rather than refusing outright.
//
// Protocol is set explicitly to skip createRssFeedHandler's auto-detect
// fetch, which would otherwise reach for a feed URL that does not exist.
func createAdultFeed(t *testing.T, srvURL, title string) int {
	t.Helper()
	protocol := "torrent"
	resp := w2Do(t, srvURL, http.MethodPost, "/api/discover/rss-feeds", apidto.RssFeedCreateRequest{
		Title:    title,
		FeedURL:  "https://example.invalid/adult.xml",
		Target:   "adult",
		Protocol: &protocol,
		Enabled:  true,
	}, withKey, withPinHeader(sectionLockTestPin))
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("seeding adult feed = %d (%s), want 200", resp.StatusCode, body)
	}
	var created apidto.RssFeed
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatalf("decoding created feed: %v", err)
	}
	return created.ID
}

func listRssFeeds(t *testing.T, srvURL string, decorate ...func(*http.Request)) []apidto.RssFeed {
	t.Helper()
	resp := w2Do(t, srvURL, http.MethodGet, "/api/discover/rss-feeds", nil, decorate...)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET rss-feeds = %d, want 200", resp.StatusCode)
	}
	var feeds []apidto.RssFeed
	if err := json.NewDecoder(resp.Body).Decode(&feeds); err != nil {
		t.Fatalf("decoding feeds: %v", err)
	}
	return feeds
}

// The READ half filters rather than refusing: a 403 on the list would take
// Mainstream's feed configuration away whenever Adult alone was locked, which
// is the outcome AC6's closing clause forbids.
func TestSectionLockW2_RssFeedListFiltersAdultRowsButKeepsMainstream(t *testing.T) {
	f := newLayer2Fixture(t)
	adultID := createAdultFeed(t, f.srv.URL, "Adult Feed")

	protocol := "torrent"
	resp := w2Do(t, f.srv.URL, http.MethodPost, "/api/discover/rss-feeds", apidto.RssFeedCreateRequest{
		Title: "Movie Feed", FeedURL: "https://example.invalid/movies.xml",
		Target: "movie", Protocol: &protocol, Enabled: true,
	}, withKey)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("creating a MAINSTREAM feed while Adult is locked = %d, want 200", resp.StatusCode)
	}

	locked := listRssFeeds(t, f.srv.URL, withKey)
	for _, feed := range locked {
		if feed.Target == "adult" {
			t.Fatalf("an adult feed (id %d) leaked into the locked list", feed.ID)
		}
	}
	if len(locked) != 1 {
		t.Fatalf("locked list has %d feeds, want only the mainstream one", len(locked))
	}

	unlocked := listRssFeeds(t, f.srv.URL, withKey, withPinHeader(sectionLockTestPin))
	if len(unlocked) != 2 {
		t.Fatalf("unlocked list has %d feeds, want 2", len(unlocked))
	}
	var sawAdult bool
	for _, feed := range unlocked {
		if feed.ID == adultID {
			sawAdult = true
		}
	}
	if !sawAdult {
		t.Fatal("the adult feed is missing from the list even with the PIN")
	}
}

func TestSectionLockW2_RssFeedAdultMovieIsLockedLikeAdult(t *testing.T) {
	f := newLayer2Fixture(t)
	protocol := "torrent"
	denied := w2Do(t, f.srv.URL, http.MethodPost, "/api/discover/rss-feeds", apidto.RssFeedCreateRequest{
		Title: "Adult Movies", FeedURL: "https://example.invalid/adult-movies.xml",
		Target: "adult-movie", Protocol: &protocol, Enabled: true,
	}, withKey)
	assertSectionLockedJSON(t, denied)

	created := w2Do(t, f.srv.URL, http.MethodPost, "/api/discover/rss-feeds", apidto.RssFeedCreateRequest{
		Title: "Adult Movies", FeedURL: "https://example.invalid/adult-movies.xml",
		Target: "adult-movie", Protocol: &protocol, Enabled: true,
	}, withKey, withPinHeader(sectionLockTestPin))
	if created.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(created.Body)
		t.Fatalf("PIN create adult-movie = %d (%s), want 200", created.StatusCode, body)
	}
	var feed apidto.RssFeed
	if err := json.NewDecoder(created.Body).Decode(&feed); err != nil {
		t.Fatalf("decoding: %v", err)
	}

	locked := listRssFeeds(t, f.srv.URL, withKey)
	for _, row := range locked {
		if row.Target == "adult-movie" || row.Target == "adult" {
			t.Fatalf("adult-movie feed leaked into the locked list: %+v", row)
		}
	}

	got := w2Do(t, f.srv.URL, http.MethodDelete, "/api/discover/rss-feeds/"+strconv.Itoa(feed.ID), nil, withKey)
	assertSectionLockedJSON(t, got)
}

// Every WRITE addressed to an Adult feed refuses with the one rejection shape
// §6 defines, so the frontend can raise its PIN overlay.
func TestSectionLockW2_RssFeedWritesRefuseAdult(t *testing.T) {
	f := newLayer2Fixture(t)
	id := createAdultFeed(t, f.srv.URL, "Adult Feed")
	path := "/api/discover/rss-feeds/" + strconv.Itoa(id)

	cases := []struct {
		name, method, path string
		body               any
	}{
		{"create", http.MethodPost, "/api/discover/rss-feeds", apidto.RssFeedCreateRequest{
			Title: "Another", FeedURL: "https://example.invalid/a.xml", Target: "adult", Enabled: true,
		}},
		{"update", http.MethodPut, path, apidto.RssFeedUpdateRequest{
			Title: "Renamed", Target: "adult", Protocol: "torrent", Enabled: true,
		}},
		{"delete", http.MethodDelete, path, nil},
		{"rescan", http.MethodPost, path + "/rescan", nil},
		{"resolve", http.MethodGet, path + "/resolve", nil},
		// Reorder is refused WHOLE: Store.Reorder demands every existing
		// feed's id exactly once across every target, so a reorder issued
		// while Adult is locked necessarily rearranges the Adult rows too.
		{"reorder", http.MethodPost, "/api/discover/rss-feeds/reorder", apidto.RssFeedReorderRequest{IDs: []int{id}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := w2Do(t, f.srv.URL, tc.method, tc.path, tc.body, withKey)
			assertSectionLockedJSON(t, resp)
		})
	}
}

// Re-pointing a MAINSTREAM feed at Adult is a write that creates Adult
// configuration from behind the lock — checked on the incoming target, not
// just the stored row.
func TestSectionLockW2_RssFeedUpdateRefusesRetargetingToAdult(t *testing.T) {
	f := newLayer2Fixture(t)
	protocol := "torrent"
	resp := w2Do(t, f.srv.URL, http.MethodPost, "/api/discover/rss-feeds", apidto.RssFeedCreateRequest{
		Title: "Movie Feed", FeedURL: "https://example.invalid/movies.xml",
		Target: "movie", Protocol: &protocol, Enabled: true,
	}, withKey)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("seeding mainstream feed = %d, want 200", resp.StatusCode)
	}
	var created apidto.RssFeed
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatalf("decoding created feed: %v", err)
	}

	got := w2Do(t, f.srv.URL, http.MethodPut, "/api/discover/rss-feeds/"+strconv.Itoa(created.ID),
		apidto.RssFeedUpdateRequest{Title: "Now Adult", Target: "adult", Protocol: "torrent", Enabled: true},
		withKey)
	assertSectionLockedJSON(t, got)
}

// --- W2-a: /api/requests* (§4.3 item 3) ---------------------------------

// newW2RequestsFixture mounts NewRequestsMux behind the gated middleware,
// both exact and subtree exactly as cmd/sakms/main.go does.
//
// It cannot reuse newLayer2Fixture: GET /api/requests and its exclude routes
// live on their OWN mux (they need an *excludes.Store, a dependency NewMux
// does not carry), so a test driven through NewMux answers 404 and would pass
// against a check that was never wired.
func newW2RequestsFixture(t *testing.T) (*httptest.Server, *sectionlock.Store, *grabs.Store) {
	t.Helper()
	ctx := context.Background()

	authStore, secretStore, sqlDB := testAuthStoreWithDB(t)
	if err := authStore.SetAuthMode(ctx, auth.ModePassword); err != nil {
		t.Fatalf("SetAuthMode: %v", err)
	}
	authStore.UseEnvAPIKey(sectionLockTestAPIKey)

	lockStore := sectionlock.NewStore(settings.New(sqlDB))
	if err := lockStore.SetPin(ctx, sectionLockTestPin); err != nil {
		t.Fatalf("SetPin: %v", err)
	}
	if err := lockStore.SetSections(ctx, []string{sectionlock.SectionAdultContent}); err != nil {
		t.Fatalf("SetSections: %v", err)
	}

	grabsStore := grabs.New(sqlDB, secretStore)
	requestsMux := NewRequestsMux(grabsStore, library.New(sqlDB), excludes.New(sqlDB))
	gated := auth.Middleware(secretStore, authStore, requestsMux,
		auth.WithSectionGate(sectionlock.NewGate(lockStore, secretStore)))

	top := http.NewServeMux()
	top.Handle("/api/requests", gated)
	top.Handle("/api/requests/", gated)

	srv := httptest.NewServer(top)
	t.Cleanup(srv.Close)
	return srv, lockStore, grabsStore
}

// The READ half filters: GET /api/requests is a CROSS-MODE rollup, so
// refusing it would take the whole Queue worklist away whenever Adult alone
// was locked — the outcome AC6's closing clause forbids.
func TestSectionLockW2_RequestsWorklistFiltersAdultRows(t *testing.T) {
	srv, _, grabsStore := newW2RequestsFixture(t)
	ctx := context.Background()

	for _, g := range []grabs.Grab{
		{Mode: mode.Adult, Title: "Some Scene"},
		{Mode: mode.Movies, TMDBID: 42, Title: "A Movie"},
	} {
		if _, err := grabsStore.Create(ctx, g); err != nil {
			t.Fatalf("seeding a %s grab: %v", g.Mode, err)
		}
	}

	worklist := func(t *testing.T, decorate ...func(*http.Request)) apidto.RequestStatusResponse {
		t.Helper()
		resp := w2Do(t, srv.URL, http.MethodGet, "/api/requests", nil, decorate...)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET /api/requests = %d, want 200", resp.StatusCode)
		}
		var got apidto.RequestStatusResponse
		if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
			t.Fatalf("decoding worklist: %v", err)
		}
		return got
	}

	locked := worklist(t, withKey)
	var sawMovie bool
	for _, item := range locked.Items {
		if item.Mode == string(mode.Adult) {
			t.Fatalf("an adult row (%q) leaked into the locked worklist", item.Title)
		}
		if item.Mode == string(mode.Movies) {
			sawMovie = true
		}
	}
	if !sawMovie {
		t.Fatal("the mainstream row vanished too — the lock refused the whole worklist")
	}

	unlocked := worklist(t, withKey, withPinHeader(sectionLockTestPin))
	if len(unlocked.Items) != 2 {
		t.Fatalf("the unlocked worklist has %d rows, want 2", len(unlocked.Items))
	}
}

// The write half refuses; the mode is in the BODY, which Layer 1's {queue}
// classification cannot see.
func TestSectionLockW2_RequestsExcludeRefusesAdult(t *testing.T) {
	srv, _, _ := newW2RequestsFixture(t)

	adult := w2Do(t, srv.URL, http.MethodPost, "/api/requests/exclude",
		apidto.ExcludeTitleRequest{Mode: "adult", Title: "Some Scene"}, withKey)
	assertSectionLockedJSON(t, adult)

	// The batch is refused WHOLE when ANY item is Adult, rather than reported
	// as a per-item error: a per-item error string can never raise the
	// frontend's PIN overlay, which keys on the code below.
	batch := w2Do(t, srv.URL, http.MethodPost, "/api/requests/exclude-batch",
		apidto.ExcludeTitlesBatchRequest{Items: []apidto.ExcludeTitleRequest{
			{Mode: "movies", TMDBID: 1, Title: "A Movie"},
			{Mode: "adult", Title: "Some Scene"},
		}}, withKey)
	assertSectionLockedJSON(t, batch)

	// A Mainstream-only batch is untouched — AC6's "without also locking
	// Mainstream", asserted rather than assumed.
	clean := w2Do(t, srv.URL, http.MethodPost, "/api/requests/exclude-batch",
		apidto.ExcludeTitlesBatchRequest{Items: []apidto.ExcludeTitleRequest{
			{Mode: "movies", TMDBID: 1, Title: "A Movie"},
		}}, withKey)
	if clean.StatusCode != http.StatusOK {
		t.Fatalf("a mainstream-only batch = %d, want 200", clean.StatusCode)
	}

	// With the PIN, the Adult exclusion goes through.
	withPin := w2Do(t, srv.URL, http.MethodPost, "/api/requests/exclude",
		apidto.ExcludeTitleRequest{Mode: "adult", Title: "Some Scene"},
		withKey, withPinHeader(sectionLockTestPin))
	if withPin.StatusCode != http.StatusNoContent {
		t.Fatalf("excluding an adult title WITH the pin = %d, want 204", withPin.StatusCode)
	}
}

// --- W2-a: proposals dismiss (§4.3 item 4) ------------------------------

// dismissProposalHandler calls no mode.Build, so Layer 2 never sees it, and
// /api/proposals/* is {organize} only.
func TestSectionLockW2_DismissAdultProposalDenied(t *testing.T) {
	f := newLayer2Fixture(t)
	p := f.insertAdultProposal(t)
	path := "/api/proposals/" + strconv.FormatInt(p.ID, 10) + "/dismiss"

	denied := w2Do(t, f.srv.URL, http.MethodPost, path, nil, withKey)
	assertSectionLockedJSON(t, denied)

	allowed := w2Do(t, f.srv.URL, http.MethodPost, path, nil, withKey, withPinHeader(sectionLockTestPin))
	if allowed.StatusCode != http.StatusNoContent {
		t.Fatalf("dismissing an adult proposal WITH the pin = %d, want 204", allowed.StatusCode)
	}
}

// --- W2-a: row-order / row-hidden (§4.3 item 2, belt-and-braces) --------

// These four are ALREADY covered by Layer 1 ({screen}=="adult" adds
// adult-content in classifyDiscover), so this asserts the outcome rather
// than which layer produced it — which is the whole point of
// defence-in-depth. The mainstream sibling must stay reachable.
func TestSectionLockW2_AdultRowConfigDeniedMainstreamAllowed(t *testing.T) {
	f := newLayer2Fixture(t)

	for _, path := range []string{"/api/discover/row-order/adult", "/api/discover/row-hidden/adult"} {
		resp := w2Do(t, f.srv.URL, http.MethodGet, path, nil, withKey)
		assertSectionLockedJSON(t, resp)
	}
	for _, path := range []string{"/api/discover/row-order/mainstream", "/api/discover/row-hidden/mainstream"} {
		resp := w2Do(t, f.srv.URL, http.MethodGet, path, nil, withKey)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s = %d, want 200 — Adult's lock leaked onto Mainstream", path, resp.StatusCode)
		}
	}
}

// --- SL-18 (integration half) and SL-20 ---------------------------------

// w2GateFixture mounts an SSE stream and the auth-mode mux behind ONE gated
// auth.Middleware over ONE gate, the way cmd/sakms/main.go does. Sharing the
// gate is load-bearing for SL-18: a second gate would carry its own settings
// cache, so a lock written here would be invisible to the stream's ticker
// until restart — and the test would pass while production did not.
type w2GateFixture struct {
	srv       *httptest.Server
	client    *http.Client
	lockStore *sectionlock.Store
	authStore *auth.Store
	session   func(*http.Request)
}

// w2Post issues a session-authenticated POST and returns the status code. The
// client carries a cookie jar, so an unlock's Set-Cookie is retained for every
// later request the same way a browser retains it.
func (f *w2GateFixture) w2Post(t *testing.T, path, body string) int {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req, err := http.NewRequest(http.MethodPost, f.srv.URL+path, rdr)
	if err != nil {
		t.Fatalf("building POST %s: %v", path, err)
	}
	f.session(req)
	resp, err := f.client.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

func newW2GateFixture(t *testing.T, recheck time.Duration) *w2GateFixture {
	t.Helper()
	ctx := context.Background()

	authStore, secretStore, sqlDB := testAuthStoreWithDB(t)
	if err := authStore.SetAuthMode(ctx, auth.ModePassword); err != nil {
		t.Fatalf("SetAuthMode: %v", err)
	}
	authStore.UseEnvAPIKey(sectionLockTestAPIKey)

	lockStore := sectionlock.NewStore(settings.New(sqlDB))
	if err := lockStore.SetPin(ctx, sectionLockTestPin); err != nil {
		t.Fatalf("SetPin: %v", err)
	}
	gate := sectionlock.NewGate(lockStore, secretStore)

	inner := http.NewServeMux()
	// A nil hub yields a nil channel and a no-op unsubscribe, so the handler
	// simply holds the connection open — exactly the "opened and held for the
	// tab's lifetime" state §4.5 is about.
	inner.HandleFunc("GET /api/modes/{mode}/dedup/scan/stream", dedupScanStreamHandler(nil, recheck))
	inner.Handle("/api/auth/mode", NewAuthModeMux(authStore, gate, false))
	// The lock's own control surface, over the SAME gate — required for the
	// explicit-revocation test below, which has to reach the real POST
	// /unlock and POST /lock handlers rather than call Gate.Revoke directly.
	inner.Handle("/api/section-lock/", NewSectionLockMux(gate, authStore, false))

	gated := auth.Middleware(secretStore, authStore, inner, auth.WithSectionGate(gate))
	srv := httptest.NewServer(gated)
	t.Cleanup(srv.Close)

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookiejar: %v", err)
	}
	token, err := auth.IssueToken(secretStore)
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}
	return &w2GateFixture{
		srv:       srv,
		client:    &http.Client{Jar: jar},
		lockStore: lockStore,
		authStore: authStore,
		session:   func(r *http.Request) { r.AddCookie(&http.Cookie{Name: auth.CookieName, Value: token}) },
	}
}

// SL-18, integration half: an SSE stream that Layer 1 allowed at open is torn
// down by the re-check ticker once a config change locks its section. The TTL
// half is covered in internal/sectionlock/stream_test.go, where an
// already-expired ticket can be forged without waiting 30 real minutes.
func TestSectionLockW2_SL18_OpenStreamTerminatesOnConfigChangeRelock(t *testing.T) {
	f := newW2GateFixture(t, 20*time.Millisecond)

	req, err := http.NewRequest(http.MethodGet, f.srv.URL+"/api/modes/adult/dedup/scan/stream", nil)
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	f.session(req)
	resp, err := f.client.Do(req)
	if err != nil {
		t.Fatalf("opening stream: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("opening stream = %d, want 200 (nothing is locked yet)", resp.StatusCode)
	}

	reader := bufio.NewReader(resp.Body)
	line, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("reading the connect comment: %v", err)
	}
	if line != ": connected\n" {
		t.Fatalf("first line = %q, want the connect comment", line)
	}

	// The re-lock the frozen request-context snapshot structurally cannot
	// see. Written through the SAME store the gate reads, which is what
	// invalidates the cache.
	if err := f.lockStore.SetSections(context.Background(), []string{sectionlock.SectionAdultContent}); err != nil {
		t.Fatalf("SetSections: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := io.ReadAll(reader)
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("stream ended with an error rather than a clean close: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the stream was still open long after its section was locked")
	}
}

// QA-GATE-1 step 7, end to end and through the real HTTP handlers: open
// /api/modes/adult/dedup/scan/stream while unlocked, then POST
// /api/section-lock/lock, and the stream must terminate.
//
// This is the case a stateless ticket cannot express on its own. POST /lock
// deletes the BROWSER's cookie — visible below, in that the cookie jar stops
// sending it — but the stream's own *http.Request froze its cookie header at
// open time, so its ticker keeps re-validating a ticket that is still
// perfectly good crypto. Before sectionlock.Gate.Revoke existed this test
// hung until its timeout while the operator watched "Re-lock now" do nothing.
func TestSectionLockW2_SL18_OpenStreamTerminatesOnExplicitRelock(t *testing.T) {
	f := newW2GateFixture(t, 20*time.Millisecond)

	if err := f.lockStore.SetSections(context.Background(), []string{sectionlock.SectionAdultContent}); err != nil {
		t.Fatalf("SetSections: %v", err)
	}
	if code := f.w2Post(t, "/api/section-lock/unlock", `{"pin":"`+sectionLockTestPin+`"}`); code != http.StatusNoContent {
		t.Fatalf("POST /unlock = %d, want 204", code)
	}

	req, err := http.NewRequest(http.MethodGet, f.srv.URL+"/api/modes/adult/dedup/scan/stream", nil)
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	f.session(req)
	resp, err := f.client.Do(req) // the jar supplies sakms_unlock
	if err != nil {
		t.Fatalf("opening stream: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("opening stream = %d, want 200 — the unlock ticket should have opened a locked section", resp.StatusCode)
	}
	reader := bufio.NewReader(resp.Body)
	if line, err := reader.ReadString('\n'); err != nil {
		t.Fatalf("reading the connect comment: %v", err)
	} else if line != ": connected\n" {
		t.Fatalf("first line = %q, want the connect comment", line)
	}

	if code := f.w2Post(t, "/api/section-lock/lock", ""); code != http.StatusNoContent {
		t.Fatalf("POST /lock = %d, want 204", code)
	}

	done := make(chan error, 1)
	go func() {
		_, err := io.ReadAll(reader)
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("stream ended with an error rather than a clean close: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the stream was still open long after POST /api/section-lock/lock — " +
			"clearing the cookie does not reach a ticket already captured by an open stream")
	}
}

// A stream whose section is NOT locked keeps running — the ticker must not be
// a general stream killer.
func TestSectionLockW2_UnaffectedStreamKeepsRunning(t *testing.T) {
	f := newW2GateFixture(t, 20*time.Millisecond)

	req, err := http.NewRequest(http.MethodGet, f.srv.URL+"/api/modes/movies/dedup/scan/stream", nil)
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	f.session(req)
	resp, err := f.client.Do(req)
	if err != nil {
		t.Fatalf("opening stream: %v", err)
	}
	defer resp.Body.Close()

	reader := bufio.NewReader(resp.Body)
	if _, err := reader.ReadString('\n'); err != nil {
		t.Fatalf("reading the connect comment: %v", err)
	}
	// Only adult-content is locked; a movies stream classifies as {organize}
	// and must survive.
	if err := f.lockStore.SetSections(context.Background(), []string{sectionlock.SectionAdultContent}); err != nil {
		t.Fatalf("SetSections: %v", err)
	}

	closed := make(chan struct{})
	go func() {
		io.ReadAll(reader)
		close(closed)
	}()
	select {
	case <-closed:
		t.Fatal("a movies stream was terminated by an adult-content lock")
	case <-time.After(300 * time.Millisecond):
		// Fifteen ticks' worth of re-checks with no termination.
	}
}

// SL-20 — PUT /api/auth/mode is the section lock's own disarm surface:
// switching to "none" makes the lock inert (GATE-1), so before §4.4's gate a
// single request from any session cookie permanently disarmed the feature.
//
// Note the route classifies as NO section, so Layer 1 lets every one of these
// through; the explicit PIN check is what answers.
func TestSectionLockW2_SL20_AuthModeSwitchToNoneRequiresThePin(t *testing.T) {
	f := newW2GateFixture(t, time.Hour)
	body := authModeRequest{Mode: auth.ModeNone, AcknowledgeInsecure: true}

	// No PIN header at all. 400, not 403: an ABSENT PIN is not a wrong one
	// and must never touch the brute-force counter, so it gets the same
	// "a PIN is required" answer the /api/section-lock/* routes give.
	missing := w2Do(t, f.srv.URL, http.MethodPut, "/api/auth/mode", body, f.session)
	if missing.StatusCode != http.StatusBadRequest {
		t.Fatalf("switching to none with NO pin = %d, want 400", missing.StatusCode)
	}
	assertAuthModeUnchanged(t, f, auth.ModePassword)

	// A WRONG pin gets the 403 section_locked shape, with an EMPTY section:
	// routes.go classifies /api/auth/* as no section at all, so there is no
	// id to name — the credential being refused guards the lock's own
	// configuration, not any one locked section. assertSectionLockedJSON is
	// deliberately NOT reused here; it insists on a non-empty section, which
	// is right for a path-gated rejection and wrong for this one.
	wrong := w2Do(t, f.srv.URL, http.MethodPut, "/api/auth/mode", body, f.session, withPinHeader("not-the-pin"))
	if wrong.StatusCode != http.StatusForbidden {
		t.Fatalf("switching to none with a WRONG pin = %d, want 403", wrong.StatusCode)
	}
	var refusal struct {
		Code    string `json:"code"`
		Section string `json:"section"`
	}
	if err := json.NewDecoder(wrong.Body).Decode(&refusal); err != nil {
		t.Fatalf("the 403 body is not JSON: %v", err)
	}
	if refusal.Code != "section_locked" {
		t.Fatalf("code = %q, want section_locked", refusal.Code)
	}
	if refusal.Section != "" {
		t.Fatalf("section = %q, want empty on an unclassified route", refusal.Section)
	}
	assertAuthModeUnchanged(t, f, auth.ModePassword)

	// The pair: the same request with the correct PIN succeeds, so the
	// refusals above are the LOCK and not some unrelated precondition.
	ok := w2Do(t, f.srv.URL, http.MethodPut, "/api/auth/mode", body, f.session, withPinHeader(sectionLockTestPin))
	if ok.StatusCode != http.StatusNoContent {
		out, _ := io.ReadAll(ok.Body)
		t.Fatalf("switching to none WITH the pin = %d (%s), want 204", ok.StatusCode, out)
	}
	assertAuthModeUnchanged(t, f, auth.ModeNone)
}

// With no PIN set the route behaves exactly as it did before the lock
// existed. Asserted because a gate that fired unconditionally would break
// every install that never enabled the feature.
func TestSectionLockW2_AuthModeSwitchUngatedWhenNoPinIsSet(t *testing.T) {
	f := newW2GateFixture(t, time.Hour)
	if err := f.lockStore.ClearPin(context.Background()); err != nil {
		t.Fatalf("ClearPin: %v", err)
	}
	resp := w2Do(t, f.srv.URL, http.MethodPut, "/api/auth/mode",
		authModeRequest{Mode: auth.ModeNone, AcknowledgeInsecure: true}, f.session)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("switching to none with no PIN configured = %d, want 204", resp.StatusCode)
	}
}

func assertAuthModeUnchanged(t *testing.T, f *w2GateFixture, want string) {
	t.Helper()
	got, err := f.authStore.AuthMode(context.Background())
	if err != nil {
		t.Fatalf("AuthMode: %v", err)
	}
	if got != want {
		t.Fatalf("auth mode = %q, want %q", got, want)
	}
}

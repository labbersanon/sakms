package discoverrefresh

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/labbersanon/sakms/internal/db"
	"github.com/labbersanon/sakms/internal/secrets"
	"github.com/labbersanon/sakms/internal/trakt"
)

// newTraktDeps wires a Deps whose TraktStore and Cache share ONE migrated
// SQLite file (as they do in production, where both are built over sqlDB) and
// whose TraktBaseURL points at h. The returned counter is incremented on every
// request the fake Trakt server receives, which is how the "zero external
// calls" assertions are made rather than asserted.
//
// Deliberately separate from store_test.go's newTestStore: that one builds only
// the cache Store, and this source needs a real trakt.Store with a real
// secrets.Store behind it (the same "exercise actual encryption and actual SQL"
// convention internal/trakt/store_test.go follows).
func newTraktDeps(t *testing.T, h http.HandlerFunc) (Deps, *trakt.Store, *atomic.Int32) {
	t.Helper()

	sqlDB, err := db.Open(filepath.Join(t.TempDir(), "sakms.db"))
	if err != nil {
		t.Fatalf("opening db: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })

	secretStore, err := secrets.New(make([]byte, 32))
	if err != nil {
		t.Fatalf("building secret store: %v", err)
	}
	traktStore := trakt.NewStore(sqlDB, secretStore)

	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		h(w, r)
	}))
	t.Cleanup(srv.Close)

	return Deps{
		HTTPClient:   srv.Client(),
		TraktStore:   traktStore,
		TraktBaseURL: srv.URL,
		Cache:        NewStore(sqlDB),
	}, traktStore, &calls
}

// linkTrakt puts the store in the only state refreshTrakt actually fetches in:
// credentials saved AND an unexpired access token, so Session.ensureFreshToken
// short-circuits and the watchlist call is the only request the fake sees.
func linkTrakt(t *testing.T, store *trakt.Store) {
	t.Helper()
	ctx := context.Background()
	secret := "secret-xyz"
	if err := store.SaveCredentials(ctx, "client-abc", &secret); err != nil {
		t.Fatalf("SaveCredentials: %v", err)
	}
	if err := store.SaveTokens(ctx, "access-token", "refresh-token", time.Now().Add(24*time.Hour)); err != nil {
		t.Fatalf("SaveTokens: %v", err)
	}
}

// traktWatchlistJSON builds n distinguishable GET /sync/watchlist entries —
// distinguishable so a truncation assertion can name WHICH items survived
// rather than only how many.
func traktWatchlistJSON(n int) string {
	entries := make([]string, 0, n)
	for i := range n {
		entries = append(entries, fmt.Sprintf(
			`{"type":"movie","movie":{"title":"Movie %d","year":%d,"ids":{"tmdb":%d}}}`,
			i, 2000+i, 1000+i))
	}
	return "[" + strings.Join(entries, ",") + "]"
}

// serveTraktJSON is source-scoped by name on purpose: BE-4/BE-5/BE-7 are landing
// their own *_test.go files in this package, and a bare serveJSON would be the
// name any of them reached for too — a duplicate declaration breaks the whole
// package's tests, not just this file's.
func serveTraktJSON(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(body))
	}
}

// T-12g — a long Trakt watchlist is not truncated.
//
// A fixed pageSize of 20 makes cachedPages = 2 and Slice(1) return the first 20
// items only; no client ever asks for page 2 (TraktWatchlistRow passes no
// onLoadMore and frontend/src/api/trakt.ts sends no page param), so the other 14
// would be permanently invisible. This is the only assertion that catches it.
//
// Scope note: §9.1 states T-12g against GET /api/trakt/watchlist. That half
// cannot exist yet — NewMux has no discoverCache parameter and
// lookupDiscoverCache is not written (BE-10/BE-13, Wave 3). Asserted here at the
// package level instead, through the exact two calls the handler will make
// (Store.Get then Entry.Slice(1)), and seeded through refreshTrakt rather than a
// direct Put so it is genuinely refreshTrakt's page-size arithmetic under test.
func TestRefreshTrakt_LongWatchlistIsNotTruncated(t *testing.T) {
	const want = 34

	d, store, calls := newTraktDeps(t, serveTraktJSON(traktWatchlistJSON(want)))
	linkTrakt(t, store)
	ctx := context.Background()

	if err := refreshTrakt(ctx, d); err != nil {
		t.Fatalf("refreshTrakt: %v", err)
	}
	afterRefresh := calls.Load()

	entry, err := d.Cache.Get(ctx, sourceTrakt, traktCacheKey)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if entry.PageSize != want {
		t.Errorf("page_size = %d, want %d — a fixed page size truncates the watchlist to that many items forever", entry.PageSize, want)
	}
	if entry.ItemCount != want {
		t.Errorf("item_count = %d, want %d", entry.ItemCount, want)
	}
	if entry.RawPages != 1 || !entry.Exhausted {
		t.Errorf("raw_pages/exhausted = %d/%v, want 1/true — the watchlist is unpaginated, so the row is always complete", entry.RawPages, entry.Exhausted)
	}

	items, hit, liveRawPage := entry.Slice(1)
	if !hit || liveRawPage != 0 {
		t.Fatalf("Slice(1) = hit %v, liveRawPage %d; want a hit with no fall-through", hit, liveRawPage)
	}
	if len(items) != want {
		t.Fatalf("Slice(1) returned %d items, want all %d in ONE response", len(items), want)
	}

	// Count is not enough: assert the TAIL survived, since a truncation keeps
	// the head and would pass any head-only check.
	var last struct {
		Type   string `json:"type"`
		Title  string `json:"title"`
		Year   int    `json:"year"`
		TMDBID int    `json:"tmdbId"`
	}
	if err := json.Unmarshal(items[want-1], &last); err != nil {
		t.Fatalf("decoding last item: %v", err)
	}
	if last.Title != fmt.Sprintf("Movie %d", want-1) || last.TMDBID != 1000+want-1 {
		t.Errorf("last item = %+v, want the %dth entry", last, want)
	}
	if last.Type != "movie" || last.Year != 2000+want-1 {
		t.Errorf("last item wire shape = %+v, want the apidto.TraktWatchlistItem field set", last)
	}

	// Serving that page made no external call — the AC1 property.
	if got := calls.Load(); got != afterRefresh {
		t.Errorf("serving from cache made %d external call(s), want 0", got-afterRefresh)
	}
}

// T-12f (Trakt half) — the empty-result rule is SOURCE-SPECIFIC.
//
// The slider source writes NOTHING on an empty accumulation (its live encoder
// emits null). Trakt is the opposite: an empty watchlist is a legitimate, stable
// state, so it writes a row with pageSize=1/exhausted=true and a subsequent read
// serves [] FROM CACHE with zero external calls. Skipping the write would cost a
// live Trakt call on every Discover load forever.
func TestRefreshTrakt_EmptyWatchlistWritesARow(t *testing.T) {
	d, store, calls := newTraktDeps(t, serveTraktJSON(`[]`))
	linkTrakt(t, store)
	ctx := context.Background()

	if err := refreshTrakt(ctx, d); err != nil {
		t.Fatalf("refreshTrakt: %v", err)
	}
	afterRefresh := calls.Load()

	entry, err := d.Cache.Get(ctx, sourceTrakt, traktCacheKey)
	if errors.Is(err, ErrNotFound) {
		t.Fatal("no row written for an empty watchlist — that is the SLIDER rule; trakt must cache empty so the read path does not fall through live forever")
	}
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if entry.PageSize != 1 {
		t.Errorf("page_size = %d, want 1 — Slice divides by it, so 0 is not storable", entry.PageSize)
	}
	if !entry.Exhausted {
		t.Error("exhausted = false, want true — an unpaginated row is always complete")
	}
	if entry.ItemCount != 0 || len(entry.Payload) != 0 {
		t.Errorf("item_count/payload = %d/%d, want 0/0", entry.ItemCount, len(entry.Payload))
	}
	if entry.LastError != "" {
		t.Errorf("last_error = %q, want empty — an empty watchlist is a success, not a failure", entry.LastError)
	}

	items, hit, liveRawPage := entry.Slice(1)
	if !hit {
		t.Fatal("Slice(1) missed on an empty cached watchlist — the read path would make a live call on every load")
	}
	if len(items) != 0 || liveRawPage != 0 {
		t.Errorf("Slice(1) = %d items, liveRawPage %d; want 0 items served from cache", len(items), liveRawPage)
	}
	if got := calls.Load(); got != afterRefresh {
		t.Errorf("serving [] from cache made %d external call(s), want 0", got-afterRefresh)
	}
}

// Not configured is not a failure (§3.7): no row, no last_error, no request.
func TestRefreshTrakt_NotConfiguredIsSilent(t *testing.T) {
	d, _, calls := newTraktDeps(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("expected no HTTP request when Trakt is not configured")
	})
	ctx := context.Background()

	if err := refreshTrakt(ctx, d); err != nil {
		t.Fatalf("refreshTrakt: %v", err)
	}
	if _, err := d.Cache.Get(ctx, sourceTrakt, traktCacheKey); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get = %v, want ErrNotFound — an unconfigured source must leave no row at all", err)
	}
	if calls.Load() != 0 {
		t.Errorf("made %d external call(s), want 0", calls.Load())
	}
}

// Configured but never linked is skipped locally, via Tokens.Linked, without a
// request — the same posture traktWatchlistHandler takes.
func TestRefreshTrakt_ConfiguredButNotLinkedIsSilent(t *testing.T) {
	d, store, calls := newTraktDeps(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("expected no HTTP request when no account is linked")
	})
	ctx := context.Background()
	secret := "secret-xyz"
	if err := store.SaveCredentials(ctx, "client-abc", &secret); err != nil {
		t.Fatalf("SaveCredentials: %v", err)
	}

	if err := refreshTrakt(ctx, d); err != nil {
		t.Fatalf("refreshTrakt: %v", err)
	}
	if _, err := d.Cache.Get(ctx, sourceTrakt, traktCacheKey); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get = %v, want ErrNotFound", err)
	}
	if calls.Load() != 0 {
		t.Errorf("made %d external call(s), want 0", calls.Load())
	}
}

// A real upstream failure marks the row and PRESERVES the previous payload
// (AC4) — the write-path invariant, exercised through this source's own path
// rather than through Store.MarkFailure directly.
func TestRefreshTrakt_UpstreamFailureKeepsStalePayload(t *testing.T) {
	const good = 3
	fail := false
	d, store, _ := newTraktDeps(t, func(w http.ResponseWriter, r *http.Request) {
		if fail {
			http.Error(w, "trakt is down", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(traktWatchlistJSON(good)))
	})
	linkTrakt(t, store)
	ctx := context.Background()

	if err := refreshTrakt(ctx, d); err != nil {
		t.Fatalf("first refreshTrakt: %v", err)
	}

	fail = true
	if err := refreshTrakt(ctx, d); err == nil {
		t.Fatal("refreshTrakt returned nil on a 500 — a genuine fetch failure must be reported")
	}

	entry, err := d.Cache.Get(ctx, sourceTrakt, traktCacheKey)
	if err != nil {
		t.Fatalf("Get after failure: %v", err)
	}
	if entry.ItemCount != good || len(entry.Payload) != good {
		t.Errorf("payload after a failed refresh = %d items, want the stale %d — a failure must never blank the row", len(entry.Payload), good)
	}
	if entry.LastError == "" {
		t.Error("last_error is empty after a failed refresh, want the cause recorded")
	}
}

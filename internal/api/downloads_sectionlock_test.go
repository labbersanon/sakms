package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labbersanon/sakms/internal/apidto"
	"github.com/labbersanon/sakms/internal/grabs"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/sectionlock"
	"github.com/labbersanon/sakms/internal/settings"
	"github.com/labbersanon/sakms/internal/webhooks"
)

// downloads_sectionlock_test.go — the Downloads queue and the notifications
// stream must not leak Adult material while adult-content is locked.
//
// # Why Layer 1 cannot do this
//
// /api/downloads and /api/notifications/stream carry no {mode} in their paths,
// so Classify gives them {queue} and {} respectively — adult-content is never
// added, and the gate never fires. An in-progress Adult grab's RELEASE NAME,
// the most identifying string the app holds, was therefore plainly visible in
// the Downloads tab and pushed live into the browser, with only Adult locked.
//
// FILTER, not refuse: Queue itself may be unlocked, and the notifications
// stream is global, so refusing would take Movies and Series away too.

// An Adult download is dropped; a mainstream one survives.
func TestFilterAdultDownloadsDropsAdultRows(t *testing.T) {
	ctx := context.Background()
	store := newDownloadsFilterGrabs(t)

	adultGID := seedGrabWithGID(t, store, mode.Adult, "Some.Studio.Scene.2026.1080p", "gid-adult")
	movieGID := seedGrabWithGID(t, store, mode.Movies, "A Mainstream Movie 2026", "gid-movie")

	rows := []apidto.Download{
		{GID: adultGID, Filename: "Some.Studio.Scene.2026.1080p.mkv"},
		{GID: movieGID, Filename: "A.Mainstream.Movie.2026.mkv"},
	}

	got := filterAdultDownloads(ctx, store, rows)
	if len(got) != 1 {
		t.Fatalf("filtered queue has %d rows, want 1: %+v", len(got), got)
	}
	if got[0].GID != movieGID {
		t.Fatalf("surviving row = %q, want the mainstream download %q", got[0].GID, movieGID)
	}
	for _, row := range got {
		if row.Filename == "Some.Studio.Scene.2026.1080p.mkv" {
			t.Fatal("an Adult release name survived the filter — this is the exact string " +
				"the lock exists to hide")
		}
	}
}

// A row whose mode cannot be determined is DROPPED, not shown. Hiding a
// mainstream row is a visible bug; showing an Adult one is a silent
// confidentiality failure.
func TestFilterAdultDownloadsFailsClosed(t *testing.T) {
	ctx := context.Background()
	store := newDownloadsFilterGrabs(t)

	got := filterAdultDownloads(ctx, store, []apidto.Download{
		{GID: "gid-with-no-grab-row", Filename: "Unattributable.mkv"},
	})
	if len(got) != 0 {
		t.Fatalf("a download with no resolvable mode survived the filter (%+v) — the filter "+
			"fails OPEN, so an Adult row that lost its grabs row is visible while locked", got)
	}

	// A nil store cannot resolve anything, so nothing may pass.
	if got := filterAdultDownloads(ctx, nil, []apidto.Download{{GID: "x"}}); len(got) != 0 {
		t.Fatalf("rows survived with no grabs store to resolve them: %+v", got)
	}
}

// End to end through the real handler: with adult-content locked, GET
// /api/downloads still SUCCEEDS (Queue is unlocked) but carries no Adult row.
func TestListDownloadsFiltersAdultWhenLocked(t *testing.T) {
	store := newDownloadsFilterGrabs(t)
	adultGID := seedGrabWithGID(t, store, mode.Adult, "Some.Studio.Scene", "gid-adult")
	movieGID := seedGrabWithGID(t, store, mode.Movies, "A Movie", "gid-movie")

	dl := newTestDownloader(adultGID, t.TempDir())
	seedActive(dl, adultGID)
	seedActive(dl, movieGID)

	handler := listDownloadsHandler(dl, nil, store)

	t.Run("unlocked shows both", func(t *testing.T) {
		rows := downloadsVia(t, handler, nil)
		if len(rows) != 2 {
			t.Fatalf("unlocked queue has %d rows, want 2", len(rows))
		}
	})

	t.Run("adult locked hides the adult row", func(t *testing.T) {
		locked := sectionlock.Decision{
			Enforcing: true,
			Locked:    sectionlock.NewSet(sectionlock.SectionAdultContent),
		}
		rows := downloadsVia(t, handler, &locked)
		if len(rows) != 1 {
			t.Fatalf("locked queue has %d rows, want 1 (the request must still SUCCEED — "+
				"filter, do not refuse): %+v", len(rows), rows)
		}
		if rows[0].GID != movieGID {
			t.Fatalf("surviving row = %q, want the mainstream download", rows[0].GID)
		}
	})
}

// --- notifications stream -----------------------------------------------

// The event-level Adult discriminator.
func TestEventIsAdult(t *testing.T) {
	for _, tc := range []struct {
		name string
		ev   webhooks.BroadcastEvent
		want bool
	}{
		{"an adult grab", webhooks.BroadcastEvent{
			Event: "grab.completed",
			Data:  map[string]any{"mode": "adult", "title": "Some.Studio.Scene"},
		}, true},
		{"a movie grab", webhooks.BroadcastEvent{
			Event: "grab.completed",
			Data:  map[string]any{"mode": "movies", "title": "A Movie"},
		}, false},
		{"no mode at all", webhooks.BroadcastEvent{
			Event: "rename.applied",
			Data:  map[string]any{"title": "A Movie"},
		}, false},
		{"a payload that is not a map", webhooks.BroadcastEvent{
			Event: "custom",
			Data:  "a bare string",
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := eventIsAdult(tc.ev); got != tc.want {
				t.Fatalf("eventIsAdult = %v, want %v", got, tc.want)
			}
		})
	}
}

// An Adult event is withheld while adult-content is locked, and delivered when
// it is not. A non-Adult event is delivered either way — the stream is global,
// so filtering must never silence Movies and Series.
func TestAdultEventHiddenOnlyWhileLocked(t *testing.T) {
	adult := webhooks.BroadcastEvent{
		Event: "grab.completed",
		Data:  map[string]any{"mode": "adult", "title": "Some.Studio.Scene.2026.1080p"},
	}
	movie := webhooks.BroadcastEvent{
		Event: "grab.completed",
		Data:  map[string]any{"mode": "movies", "title": "A Movie"},
	}

	unlocked := httptest.NewRequest(http.MethodGet, "/api/notifications/stream", nil)
	if adultEventHidden(unlocked, adult) {
		t.Fatal("an Adult event was withheld from a request with no lock decision at all — " +
			"absent-allows is the rule Layers 2 and 3 share")
	}

	locked := requestWithDecision(sectionlock.Decision{
		Enforcing: true,
		Locked:    sectionlock.NewSet(sectionlock.SectionAdultContent),
	})
	if !adultEventHidden(locked, adult) {
		t.Fatal("an Adult event was pushed to the browser while adult-content was locked — " +
			"the scene title leaks live over the notifications stream")
	}
	if adultEventHidden(locked, movie) {
		t.Fatal("a MOVIES event was withheld because Adult was locked — this is one global " +
			"stream, so filtering Adult must not silence the other modes")
	}
}

// --- helpers ------------------------------------------------------------

func newDownloadsFilterGrabs(t *testing.T) *grabs.Store {
	t.Helper()
	_, secretStore, sqlDB := testAuthStoreWithDB(t)
	return grabs.New(sqlDB, secretStore)
}

// seedGrabWithGID creates a grab in m and attaches gid, the way every real
// dispatch path does (Create, then SetDownloadGID).
func seedGrabWithGID(t *testing.T, store *grabs.Store, m mode.Mode, title, gid string) string {
	t.Helper()
	ctx := context.Background()
	created, err := store.Create(ctx, grabs.Grab{Mode: m, Title: title, Indexer: "test", Protocol: "torrent"})
	if err != nil {
		t.Fatalf("creating %s grab: %v", m, err)
	}
	if err := store.SetDownloadGID(ctx, created.ID, gid); err != nil {
		t.Fatalf("SetDownloadGID: %v", err)
	}
	return gid
}

func requestWithDecision(d sectionlock.Decision) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/api/notifications/stream", nil)
	return r.WithContext(sectionlock.WithDecision(r.Context(), d))
}

func downloadsVia(t *testing.T, handler http.HandlerFunc, d *sectionlock.Decision) []apidto.Download {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/downloads", nil)
	if d != nil {
		req = req.WithContext(sectionlock.WithDecision(req.Context(), *d))
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/downloads = %d, want 200 — the request must still succeed", rec.Code)
	}
	var rows []apidto.Download
	if err := json.NewDecoder(rec.Body).Decode(&rows); err != nil {
		t.Fatalf("decoding downloads: %v", err)
	}
	return rows
}

// --- the LIVE branch: a section locked AFTER the stream opened -----------

// The case the frozen context Decision structurally cannot see, and the whole
// reason adultHiddenNow consults StreamRevoked rather than adultLocked alone.
//
// A stream that opened while nothing was locked carries a Decision saying so
// forever. /api/downloads/stream classifies as {queue} and the notifications
// stream as {}, so the re-check ticker never terminates either on an
// adult-content lock — without the live read, Adult keeps flowing to an
// already-open tab until it is reloaded.
func TestAdultHiddenNowSeesASectionLockedAfterTheStreamOpened(t *testing.T) {
	adult := webhooks.BroadcastEvent{
		Event: "grab.completed",
		Data:  map[string]any{"mode": "adult", "title": "Some.Studio.Scene.2026.1080p"},
	}

	t.Run("locked after open", func(t *testing.T) {
		// The gate's CURRENT state: adult-content is locked.
		r := streamOpenedCleanThen(t, sectionlock.SectionAdultContent)

		// The frozen Decision still says nothing is locked, so adultLocked alone
		// is false — this is precisely the gap.
		if adultLocked(r.Context()) {
			t.Fatal("the frozen decision already reports Adult locked; this test is not " +
				"exercising the live branch it exists to cover")
		}
		if !adultHiddenNow(r) {
			t.Fatal("a section locked AFTER the stream opened was not observed — Adult " +
				"release names keep streaming to an already-open tab until it is reloaded")
		}
		if !adultEventHidden(r, adult) {
			t.Fatal("an Adult notification event was pushed after adult-content was locked " +
				"mid-stream")
		}
	})

	t.Run("nothing locked stays open", func(t *testing.T) {
		r := streamOpenedCleanThen(t)
		if adultHiddenNow(r) {
			t.Fatal("Adult was hidden while nothing was locked at all")
		}
		if adultEventHidden(r, adult) {
			t.Fatal("an Adult event was withheld while nothing was locked")
		}
	})

	t.Run("a section other than adult-content does not hide Adult", func(t *testing.T) {
		r := streamOpenedCleanThen(t, sectionlock.SectionOrganize)
		if adultHiddenNow(r) {
			t.Fatal("locking Organize hid Adult — the live read is asking about the wrong " +
				"section set")
		}
	})
}

// streamOpenedCleanThen builds a request as it would look INSIDE a long-lived
// SSE handler that opened while nothing was locked, against a gate whose store
// has since had lockNow locked.
//
// The Decision is deliberately the clean one (empty Locked, Unlocked false,
// Epoch 0 matching the fresh gate) — that is what Layer 1 wrote at open time,
// and it is frozen there for the stream's whole life.
func streamOpenedCleanThen(t *testing.T, lockNow ...string) *http.Request {
	t.Helper()
	ctx := context.Background()

	_, secretStore, sqlDB := testAuthStoreWithDB(t)
	lockStore := sectionlock.NewStore(settings.New(sqlDB))
	if err := lockStore.SetPin(ctx, "135790"); err != nil {
		t.Fatalf("SetPin: %v", err)
	}
	if len(lockNow) > 0 {
		if err := lockStore.SetSections(ctx, lockNow); err != nil {
			t.Fatalf("SetSections: %v", err)
		}
	}
	gate := sectionlock.NewGate(lockStore, secretStore)

	r := httptest.NewRequest(http.MethodGet, "/api/downloads/stream", nil)
	rctx := sectionlock.WithDecision(r.Context(), sectionlock.Decision{
		Enforcing: true,
		Locked:    sectionlock.NewSet(), // nothing was locked when the stream opened
		Epoch:     gate.Epoch(),
	})
	rctx = sectionlock.WithLive(rctx, gate)
	return r.WithContext(rctx)
}

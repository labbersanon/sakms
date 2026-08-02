package identify

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/labbersanon/sakms/internal/stashbox"
	"github.com/labbersanon/sakms/internal/throttle"
	"github.com/labbersanon/sakms/internal/tpdbrest"
)

// stashboxDetailCounts tallies a fake stash-box server's traffic. total is the
// load-bearing one for the AC4 / Option-A assertions: "this box's server saw
// ZERO requests" is only meaningful when a real, counting server is wired under
// that box key (a nil client would produce zero requests too, vacuously).
type stashboxDetailCounts struct {
	total     int32
	findScene int32
}

// tpdbDetailCounts tallies which TPDB REST endpoint each fake request hit.
type tpdbDetailCounts struct {
	total           int32
	sceneByID       int32 // GET /scenes/{id}
	performerSearch int32 // GET /performers?q=
	siteSearch      int32 // GET /sites?q=
	performerByID   int32 // GET /performers/{id}
	siteByID        int32 // GET /sites/{id}
}

func stashboxDetailFake(c *stashboxDetailCounts, findSceneJSON string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&c.total, 1)
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(string(body), "findScene") {
			atomic.AddInt32(&c.findScene, 1)
			_, _ = w.Write([]byte(findSceneJSON))
			return
		}
		_, _ = w.Write([]byte(`{"data":{}}`))
	}
}

func tpdbDetailFake(c *tpdbDetailCounts) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&c.total, 1)
		p := r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		switch {
		case p == "/performers":
			atomic.AddInt32(&c.performerSearch, 1)
			_, _ = w.Write([]byte(tpdbPerformerSearchJSON))
		case p == "/sites":
			atomic.AddInt32(&c.siteSearch, 1)
			_, _ = w.Write([]byte(tpdbSiteSearchJSON))
		case strings.HasPrefix(p, "/scenes/"):
			atomic.AddInt32(&c.sceneByID, 1)
			_, _ = w.Write([]byte(tpdbSceneDetailJSON))
		case strings.HasPrefix(p, "/performers/"):
			atomic.AddInt32(&c.performerByID, 1)
			_, _ = w.Write([]byte(tpdbPerformerDetailJSON))
		case strings.HasPrefix(p, "/sites/"):
			atomic.AddInt32(&c.siteByID, 1)
			_, _ = w.Write([]byte(tpdbSiteDetailJSON))
		default:
			_, _ = w.Write([]byte(`{"data":[]}`))
		}
	}
}

const (
	// rawSiteEntry.ID decodes "uuid" (NOT "_id"), while rawPerformer.ID decodes
	// "_id" — a live-verified asymmetry in internal/tpdbrest. A fixture that
	// unifies them silently yields an empty entity id, so resolveEntityInBox
	// returns "" and the detail fetch never fires.
	tpdbSiteSearchJSON      = `{"data":[{"uuid":"site1","name":"Brazzers"}]}`
	tpdbSiteDetailJSON      = `{"data":{"uuid":"site1","name":"Brazzers","description":"TPDB STUDIO BLURB"}}`
	tpdbPerformerSearchJSON = `{"data":[{"_id":"p1","name":"Riley Reid"}]}`
	tpdbPerformerDetailJSON = `{"data":{"_id":"p1","name":"Riley Reid","bio":"TPDB PERFORMER BIO"}}`
	tpdbSceneDetailJSON     = `{"data":{"_id":"tsc1","title":"Some Scene Title","description":"TPDB SCENE DESCRIPTION"}}`

	stashdbSceneDetailJSON = `{"data":{"findScene":{"id":"sc1","title":"Some Scene Title","details":"STASHDB SCENE DETAILS"}}}`
	fansdbSceneDetailJSON  = `{"data":{"findScene":{"id":"sc1","title":"Some Scene Title","details":"FANSDB SCENE DETAILS"}}}`
)

// newDetailIdentifier builds an Identifier with any subset of the three boxes
// backed by fake servers. Unlike newBoxSearcherWithFakes (which only wires
// "stashdb"), this one can configure fansdb too — required by the AC4
// one-box-only assertion.
func newDetailIdentifier(t *testing.T, stashdbHandler, fansdbHandler, tpdbHandler http.HandlerFunc) *Identifier {
	t.Helper()
	boxes := map[string]*stashbox.Client{}
	newBox := func(h http.HandlerFunc) *stashbox.Client {
		srv := httptest.NewServer(h)
		t.Cleanup(srv.Close)
		return stashbox.New(stashbox.Config{Endpoint: srv.URL, APIKey: "k"}, &http.Client{Timeout: 5 * time.Second})
	}
	if stashdbHandler != nil {
		boxes["stashdb"] = newBox(stashdbHandler)
	}
	if fansdbHandler != nil {
		boxes["fansdb"] = newBox(fansdbHandler)
	}
	var tpdb *tpdbrest.Client
	if tpdbHandler != nil {
		srv := httptest.NewServer(tpdbHandler)
		t.Cleanup(srv.Close)
		tpdb = tpdbrest.New(srv.URL, "k", &http.Client{Timeout: 5 * time.Second})
	}
	return &Identifier{Boxes: NewBoxSearcher(boxes, tpdb), Throttle: throttle.New(0)}
}

// TestSceneDescriptionTPDB — box "tpdb" makes exactly one GetSceneByID call and
// returns the scene's Description.
func TestSceneDescriptionTPDB(t *testing.T) {
	var tc tpdbDetailCounts
	id := newDetailIdentifier(t, nil, nil, tpdbDetailFake(&tc))

	got, err := id.SceneDescription(context.Background(), "tpdb", "tsc1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "TPDB SCENE DESCRIPTION" {
		t.Errorf("description = %q, want %q", got, "TPDB SCENE DESCRIPTION")
	}
	if n := atomic.LoadInt32(&tc.sceneByID); n != 1 {
		t.Errorf("GetSceneByID: expected exactly 1 call, got %d", n)
	}
	if n := atomic.LoadInt32(&tc.total); n != 1 {
		t.Errorf("expected exactly 1 upstream call in total, got %d", n)
	}
}

// TestSceneDescriptionStashBox — box "stashdb" makes exactly one FindScene call
// and returns the scene's Details.
func TestSceneDescriptionStashBox(t *testing.T) {
	var sc stashboxDetailCounts
	id := newDetailIdentifier(t, stashboxDetailFake(&sc, stashdbSceneDetailJSON), nil, nil)

	got, err := id.SceneDescription(context.Background(), "stashdb", "sc1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "STASHDB SCENE DETAILS" {
		t.Errorf("description = %q, want %q", got, "STASHDB SCENE DETAILS")
	}
	if n := atomic.LoadInt32(&sc.findScene); n != 1 {
		t.Errorf("FindScene: expected exactly 1 call, got %d", n)
	}
	if n := atomic.LoadInt32(&sc.total); n != 1 {
		t.Errorf("expected exactly 1 upstream call in total, got %d", n)
	}
}

// TestSceneDescriptionOneBoxOnly is the AC4 invariant: with all three boxes
// configured and all three holding data, ONLY the named box's server is hit.
// Never a fan-out, never a merge — that is what makes the one-upstream-call
// bound structural rather than reviewed.
func TestSceneDescriptionOneBoxOnly(t *testing.T) {
	cases := []struct {
		box  string
		want string
	}{
		{"stashdb", "STASHDB SCENE DETAILS"},
		{"fansdb", "FANSDB SCENE DETAILS"},
		{"tpdb", "TPDB SCENE DESCRIPTION"},
	}
	for _, tc := range cases {
		t.Run(tc.box, func(t *testing.T) {
			var stashdb, fansdb stashboxDetailCounts
			var tpdb tpdbDetailCounts
			id := newDetailIdentifier(t,
				stashboxDetailFake(&stashdb, stashdbSceneDetailJSON),
				stashboxDetailFake(&fansdb, fansdbSceneDetailJSON),
				tpdbDetailFake(&tpdb),
			)

			got, err := id.SceneDescription(context.Background(), tc.box, "sc1")
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("description = %q, want %q", got, tc.want)
			}
			totals := map[string]int32{
				"stashdb": atomic.LoadInt32(&stashdb.total),
				"fansdb":  atomic.LoadInt32(&fansdb.total),
				"tpdb":    atomic.LoadInt32(&tpdb.total),
			}
			for box, n := range totals {
				want := int32(0)
				if box == tc.box {
					want = 1
				}
				if n != want {
					t.Errorf("box %q server saw %d requests, want %d (drill named %q)", box, n, want, tc.box)
				}
			}
		})
	}
}

// TestSceneDescriptionSoftFails — every failure path is best-effort: ("", nil),
// never an error the caller has to handle.
func TestSceneDescriptionSoftFails(t *testing.T) {
	boom := func(w http.ResponseWriter, r *http.Request) { http.Error(w, "boom", http.StatusInternalServerError) }

	t.Run("stashbox upstream 500", func(t *testing.T) {
		id := newDetailIdentifier(t, boom, nil, nil)
		assertEmptyNoError(t)(id.SceneDescription(context.Background(), "stashdb", "sc1"))
	})

	t.Run("tpdb upstream 500", func(t *testing.T) {
		id := newDetailIdentifier(t, nil, nil, boom)
		assertEmptyNoError(t)(id.SceneDescription(context.Background(), "tpdb", "tsc1"))
	})

	t.Run("unknown box makes no call", func(t *testing.T) {
		// "prowlarr" is routinely reachable — a Show More drill-down scene is
		// emitted with that source and no id.
		var stashdb, fansdb stashboxDetailCounts
		var tpdb tpdbDetailCounts
		id := newDetailIdentifier(t,
			stashboxDetailFake(&stashdb, stashdbSceneDetailJSON),
			stashboxDetailFake(&fansdb, fansdbSceneDetailJSON),
			tpdbDetailFake(&tpdb),
		)
		assertEmptyNoError(t)(id.SceneDescription(context.Background(), "prowlarr", "sc1"))
		if n := atomic.LoadInt32(&stashdb.total) + atomic.LoadInt32(&fansdb.total) + atomic.LoadInt32(&tpdb.total); n != 0 {
			t.Errorf("an unknown box must make zero upstream calls, got %d", n)
		}
	})

	t.Run("box not configured", func(t *testing.T) {
		var tpdb tpdbDetailCounts
		id := newDetailIdentifier(t, nil, nil, tpdbDetailFake(&tpdb))
		assertEmptyNoError(t)(id.SceneDescription(context.Background(), "stashdb", "sc1"))
		if n := atomic.LoadInt32(&tpdb.total); n != 0 {
			t.Errorf("a nil stash-box client must never fall back to another box, got %d tpdb calls", n)
		}
	})

	t.Run("empty box and empty scene id", func(t *testing.T) {
		var tpdb tpdbDetailCounts
		id := newDetailIdentifier(t, nil, nil, tpdbDetailFake(&tpdb))
		assertEmptyNoError(t)(id.SceneDescription(context.Background(), "", "tsc1"))
		assertEmptyNoError(t)(id.SceneDescription(context.Background(), "tpdb", ""))
		if n := atomic.LoadInt32(&tpdb.total); n != 0 {
			t.Errorf("expected zero upstream calls for empty args, got %d", n)
		}
	})

	t.Run("nil identifier and nil boxes", func(t *testing.T) {
		var nilID *Identifier
		assertEmptyNoError(t)(nilID.SceneDescription(context.Background(), "tpdb", "tsc1"))
		assertEmptyNoError(t)((&Identifier{}).SceneDescription(context.Background(), "tpdb", "tsc1"))
	})
}

// TestEntityBioTPDBPerformer — resolve-by-name, then GetPerformerByID, returning
// Bio. Exactly two upstream calls, never more.
func TestEntityBioTPDBPerformer(t *testing.T) {
	var tc tpdbDetailCounts
	id := newDetailIdentifier(t, nil, nil, tpdbDetailFake(&tc))

	got, err := id.EntityBio(context.Background(), "performer", "Riley Reid", "tpdb")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "TPDB PERFORMER BIO" {
		t.Errorf("bio = %q, want %q", got, "TPDB PERFORMER BIO")
	}
	if n := atomic.LoadInt32(&tc.performerSearch); n != 1 {
		t.Errorf("SearchPerformers: expected 1 call, got %d", n)
	}
	if n := atomic.LoadInt32(&tc.performerByID); n != 1 {
		t.Errorf("GetPerformerByID: expected 1 call, got %d", n)
	}
	if n := atomic.LoadInt32(&tc.total); n != 2 {
		t.Errorf("expected exactly 2 upstream calls (resolve + detail), got %d", n)
	}
	if n := atomic.LoadInt32(&tc.siteSearch) + atomic.LoadInt32(&tc.siteByID); n != 0 {
		t.Errorf("a performer drill must never touch the site endpoints, got %d", n)
	}
}

// TestEntityBioTPDBStudio — resolve-by-name, then GetSiteByID, returning
// Description. Exactly two upstream calls, never more.
func TestEntityBioTPDBStudio(t *testing.T) {
	var tc tpdbDetailCounts
	id := newDetailIdentifier(t, nil, nil, tpdbDetailFake(&tc))

	got, err := id.EntityBio(context.Background(), "studio", "Brazzers", "tpdb")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "TPDB STUDIO BLURB" {
		t.Errorf("description = %q, want %q", got, "TPDB STUDIO BLURB")
	}
	if n := atomic.LoadInt32(&tc.siteSearch); n != 1 {
		t.Errorf("SearchSites: expected 1 call, got %d", n)
	}
	if n := atomic.LoadInt32(&tc.siteByID); n != 1 {
		t.Errorf("GetSiteByID: expected 1 call, got %d", n)
	}
	if n := atomic.LoadInt32(&tc.total); n != 2 {
		t.Errorf("expected exactly 2 upstream calls (resolve + detail), got %d", n)
	}
	if n := atomic.LoadInt32(&tc.performerSearch) + atomic.LoadInt32(&tc.performerByID); n != 0 {
		t.Errorf("a studio drill must never touch the performer endpoints, got %d", n)
	}
}

// TestEntityBioStashBoxCoercesToTPDB is §8's TestEntityBioStashBoxMakesNoCall
// INVERTED per GATE-0 Option A (Wade, 2026-08-02). Under strict AC4 a
// stash-box-sourced entity made zero calls and could never show a banner;
// EntityBio now coerces box to "tpdb" unconditionally. The stash-box server is
// real and counting on purpose — asserting "zero requests" against a nil client
// would pass vacuously and prove nothing about the coercion.
func TestEntityBioStashBoxCoercesToTPDB(t *testing.T) {
	cases := []struct {
		name string
		box  string
		kind string
		want string
	}{
		{"stashdb performer", "stashdb", "performer", "TPDB PERFORMER BIO"},
		{"fansdb studio", "fansdb", "studio", "TPDB STUDIO BLURB"},
		// Pins FUNCTION-level behavior the handler never exercises: the caller
		// returns 200-empty on an empty box, short-circuiting ahead of the
		// coercion (R2-5). Do not read this case as licence to drop that
		// short-circuit.
		{"empty box", "", "performer", "TPDB PERFORMER BIO"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stashdb, fansdb stashboxDetailCounts
			var tpdb tpdbDetailCounts
			id := newDetailIdentifier(t,
				stashboxDetailFake(&stashdb, stashdbSceneDetailJSON),
				stashboxDetailFake(&fansdb, fansdbSceneDetailJSON),
				tpdbDetailFake(&tpdb),
			)

			entity := "Riley Reid"
			if tc.kind == "studio" {
				entity = "Brazzers"
			}
			got, err := id.EntityBio(context.Background(), tc.kind, entity, tc.box)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("bio = %q, want %q — box %q must be coerced to tpdb", got, tc.want, tc.box)
			}
			if n := atomic.LoadInt32(&stashdb.total) + atomic.LoadInt32(&fansdb.total); n != 0 {
				t.Errorf("stash-box servers must see zero requests (no bio field exists there), got %d", n)
			}
			if n := atomic.LoadInt32(&tpdb.total); n != 2 {
				t.Errorf("expected exactly 2 TPDB calls (resolve + detail), got %d", n)
			}
		})
	}
}

// TestEntityBioUnresolvableName — no exact-name match means no entity id, so the
// detail fetch never fires and the result is ("", nil).
func TestEntityBioUnresolvableName(t *testing.T) {
	var tc tpdbDetailCounts
	id := newDetailIdentifier(t, nil, nil, tpdbDetailFake(&tc))

	// The fake's performer search only ever answers with "Riley Reid".
	assertEmptyNoError(t)(id.EntityBio(context.Background(), "performer", "Riley Reid X", "tpdb"))
	if n := atomic.LoadInt32(&tc.performerSearch); n != 1 {
		t.Errorf("the resolve search must still fire once, got %d", n)
	}
	if n := atomic.LoadInt32(&tc.performerByID); n != 0 {
		t.Errorf("expected NO detail fetch after an unresolvable name, got %d", n)
	}
}

// TestEntityBioNilGuards — a nil Identifier, a nil BoxSearcher, and an empty
// name are all clean no-ops (sess.Identify is explicitly nil-able).
func TestEntityBioNilGuards(t *testing.T) {
	var nilID *Identifier
	assertEmptyNoError(t)(nilID.EntityBio(context.Background(), "performer", "Riley Reid", "tpdb"))
	assertEmptyNoError(t)((&Identifier{}).EntityBio(context.Background(), "performer", "Riley Reid", "tpdb"))

	var tc tpdbDetailCounts
	id := newDetailIdentifier(t, nil, nil, tpdbDetailFake(&tc))
	assertEmptyNoError(t)(id.EntityBio(context.Background(), "performer", "", "tpdb"))
	if n := atomic.LoadInt32(&tc.total); n != 0 {
		t.Errorf("expected zero upstream calls for an empty name, got %d", n)
	}
}

// assertEmptyNoError is curried so a call reads
// assertEmptyNoError(t)(id.SceneDescription(...)) — Go only allows a
// multi-value call to be spread into a function taking exactly those values.
func assertEmptyNoError(t *testing.T) func(string, error) {
	t.Helper()
	return func(got string, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("expected a best-effort no-op, got error %v", err)
		}
		if got != "" {
			t.Errorf("expected an empty result, got %q", got)
		}
	}
}

package api

import (
	"context"
	"net/http"
	"time"

	"github.com/labbersanon/sakms/internal/adultnewest"
	"github.com/labbersanon/sakms/internal/apidto"
	"github.com/labbersanon/sakms/internal/connections"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/serviceconn"
	"github.com/labbersanon/sakms/internal/settings"
)

// descriptionTimeout bounds one description/bio lookup end to end. A var, not a
// const, purely so tests can tighten it to prove the handler returns within the
// bound against a catalog that never responds — the same convention
// newestScenesOutboundTimeout and newestScenesEnrichTimeout follow
// (adultdiscover_newest_scenes.go); nothing mutates it in production.
//
// It backs BOTH descClient.Timeout AND the context deadline below, and the
// pairing is the point: http.Client.Timeout caps the whole round trip
// independently of any context deadline and whichever is shorter wins, so
// bounding only one of them is not a bound at all. That was confirmed the hard
// way, live, on the Show More path (see newestScenesOutboundTimeout's doc: a fix
// that bumped only the context deadline changed nothing, because the shared
// client's own Timeout kept firing first).
//
// 6s matches newestScenesEnrichTimeout — both bound at most two throttled
// catalog calls.
var descriptionTimeout = 6 * time.Second

// adultDescriptionHandler backs the Adult Discover pop-up / drill-down
// enrichment read — GET /api/modes/adult/discover/description.
//
//	kind=scene              → source (tpdb|stashdb|fansdb) + id, both required
//	kind=performer|studio   → name required; source optional (a box hint from a
//	                          drill that already knows it — when absent the
//	                          server resolves it from the matched-entity pool)
//
// Call budget: ZERO Prowlarr calls, EVER — this handler never READS OR CALLS a
// Prowlarr client, which makes it narrower than the drill-down carve-out beside
// it (adultdiscover_newest_scenes.go), not a widening of it. Note this is a
// behavioral guarantee (no call site), not a structural one (a nil client):
// mode.Build still allocates sess.Prowlarr from the connection store whenever a
// Prowlarr connection is configured — a plain struct constructor, no I/O — this
// handler simply never touches the field. Do NOT "verify" this rule by asserting
// sess.Prowlarr == nil; that assertion is false whenever Prowlarr is configured.
// TestAdultDescriptionMakesNoProwlarrCall is what actually enforces it, by
// counting requests against a fake Prowlarr server. At most two upstream
// catalog calls per request, structurally: exactly one for a scene (the named box
// only, never a fan-out), and two for a performer/studio (an exact-name resolve
// against TPDB, then a detail fetch). It fires once per explicit operator action
// — a DetailPopup open or a drill-down open — never per card, never per scroll,
// never on a PaginatedStrip page advance.
//
// EVERY response is 200. A 400 is reserved for exactly one case: a
// malformed/absent kind. Every no-data condition — an unconfigured Adult
// install, a mode.Build failure, a missing/unrecognised scene source, an empty
// id or name, an unknown entity, an upstream error — returns 200 with an empty
// DTO instead. This is deliberate and load-bearing, not defensive politeness:
// the frontend has NO ErrorBoundary anywhere in its tree, and a non-2xx pushes
// the calling Solid resource into an errored state that re-throws on every
// subsequent read — an SPA-wide crash class this shape removes at the source.
//
// The unrecognised-scene-source case is the one worth spelling out, because
// "unknown enum value → 400" is the reflex and it is wrong here: a Show More
// drill-down scene is routinely emitted with Source "prowlarr" and no ID at all
// (adultdiscover_newest_scenes.go's page>1 mapping), and those items open
// DetailPopup through this exact path. A 400 there would fire on every Show More
// scene's popup open, swallowed silently by the frontend's catch.
func adultDescriptionHandler(connStore *connections.Store, scStore *serviceconn.Store, settingsStore *settings.Store, releaseStore *adultnewest.ReleaseStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		q := r.URL.Query()

		kind := q.Get("kind")
		if kind != "scene" && kind != "performer" && kind != "studio" {
			http.Error(w, "kind must be one of scene, performer, studio", http.StatusBadRequest)
			return
		}

		source := q.Get("source")
		id := q.Get("id")
		name := q.Get("name")

		// Everything below this line answers 200. Resolve as much as possible
		// BEFORE building a session or a client, so a request that cannot
		// possibly reach a catalog makes structurally zero upstream calls
		// rather than incidentally zero.
		if kind == "scene" && (id == "" || !knownDescriptionBox(source)) {
			writeJSON(w, apidto.AdultDescription{})
			return
		}
		if kind != "scene" && name == "" {
			writeJSON(w, apidto.AdultDescription{})
			return
		}

		tctx, cancel := context.WithTimeout(ctx, descriptionTimeout)
		defer cancel()

		if kind != "scene" && source == "" {
			// No box hint from the caller: fall back to the box that originally
			// identified this entity in the matched-entity pool. A DB error is a
			// no-data condition like any other here.
			box, err := releaseStore.EntityBox(tctx, adultnewest.RowType(kind), name)
			if err != nil || box == "" {
				writeJSON(w, apidto.AdultDescription{})
				return
			}
			source = box
		}

		// Deliberately its own *http.Client, not the app-wide shared one — that
		// client's own Timeout would bound this round trip independently of
		// descriptionTimeout, whichever is shorter, so reusing it would silently
		// cap (or, worse, quietly un-cap) this handler. Same reasoning, same
		// live-debugged precedent, as showMoreClient.
		descClient := &http.Client{Timeout: descriptionTimeout}
		sess, err := mode.Build(tctx, connStore, scStore, settingsStore, descClient, nil, mode.Adult)
		if err != nil || sess == nil || sess.Identify == nil || sess.Identify.Boxes == nil {
			// A build failure and a nil Identify are two different paths and BOTH
			// answer 200-empty. sess.Identify is explicitly nil-able (an Adult
			// install with no catalog configured at all), so this is the ordinary
			// unconfigured case, not an error.
			writeJSON(w, apidto.AdultDescription{})
			return
		}

		var text string
		if kind == "scene" {
			text, _ = sess.Identify.SceneDescription(tctx, source, id)
		} else {
			text, _ = sess.Identify.EntityBio(tctx, kind, name, source)
			// EntityBio returns no provenance, so the box actually consulted is
			// derived rather than echoed — and for an entity it is always TPDB
			// when there is any text at all. Live schema introspection
			// (2026-08-02) proves neither stash-box endpoint exposes a performer
			// bio or a studio description field, so a stash-box box can only ever
			// yield "" here; a non-empty bio came from TPDB by construction,
			// whether the caller's hint said so or not. Do NOT "fix" this into
			// echoing the requested source — reporting source "tpdb" for a
			// stash-box-sourced entity is the intended honest provenance.
			if text != "" {
				source = "tpdb"
			}
		}

		// Blank Source whenever Text is blank, so a no-data response is exactly
		// {"text":"","source":""} rather than a source echo with nothing behind it.
		if text == "" {
			source = ""
		}
		writeJSON(w, apidto.AdultDescription{Text: text, Source: source})
	}
}

// knownDescriptionBox reports whether s names a catalog this endpoint can fetch a
// scene description from. Anything else — "prowlarr", "", a typo — is a no-data
// condition, never a request error; see the handler doc.
func knownDescriptionBox(s string) bool {
	return s == "tpdb" || s == "stashdb" || s == "fansdb"
}

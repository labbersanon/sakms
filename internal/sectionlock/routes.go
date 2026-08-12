package sectionlock

import (
	"path"
	"strings"
)

// Classify maps a request path to the set of sections that gate it.
//
// It returns a SET, not a single id, because one route can belong to a tab
// section AND to adult-content at the same time:
// /api/modes/adult/rename/scan is {organize, adult-content}, and is gated
// when EITHER is locked. That is what makes "lock the Organize tab" and
// "lock Adult everywhere" independently meaningful.
//
// # The input is cleaned here, never by the caller
//
// Classify runs path.Clean on rawPath itself so no call site can forget
// to. Without it, /api/modes/movies/../adult/rename/scan would classify as
// {organize} on the movies branch while http.ServeMux routes it to the
// adult handler — a bypass. Every traversal variant of a path therefore
// classifies identically to its canonical form.
//
// # NEVER classify from r.PathValue
//
// The single most important structural fact about this package's primary
// call site: auth.Middleware wraps the mux, and http.ServeMux populates
// path values during its OWN ServeHTTP — after the middleware has run. So
// r.PathValue("mode") is "" inside auth.Middleware. Reading it there
// yields an empty string and silently fails OPEN. Adult scope is computed
// by parsing r.URL.Path's segments, and only that way.
//
// # Unrecognised paths classify as nothing, deliberately
//
// A fail-closed default (unknown path ⇒ some section) would break every
// route this table has not enumerated the moment anything is locked, on an
// app with ~200 routes. Allow-by-default is made safe not by hope but by
// the classification-completeness static test (SL-9), which AST-parses
// every route pattern literal in internal/api and cmd/sakms and fails if
// any lacks an explicit classification here. If that test is ever deleted,
// this default becomes a real hole.
//
// # Attribution rule for routes two screens share
//
// Several endpoints serve two screens (Discover renders the sliders that
// Settings manages; the Trakt watchlist row reads the status Settings'
// connection card writes). Where a route is genuinely shared, it is
// attributed to the LESS restrictive screen — because gating it under the
// more restrictive one breaks the other screen while it is still supposed
// to be unlocked, and the more restrictive screen is gated by its own
// routes regardless.
func Classify(rawPath string) Set {
	out := make(Set, 2)
	segs := segments(rawPath)
	if len(segs) < 2 || segs[0] != "api" {
		// Not an API path: the SPA shell, static assets, /healthz. The
		// section lock is a backend data boundary, not a routing one —
		// locked tabs stay visible and render a PIN overlay.
		return out
	}

	switch segs[1] {
	case "modes":
		classifyModes(segs[2:], out)
	case "discover":
		classifyDiscover(segs[2:], out)
	case "settings":
		out.Add(SectionSettings)
		// /api/settings/adult-* — adult-mode-enabled and
		// adult-newest-scan-interval today. A prefix rule rather than a
		// literal list so a future adult-* setting is covered on the day
		// it is added rather than the day someone remembers this file.
		if len(segs) >= 3 && strings.HasPrefix(segs[2], "adult-") {
			out.Add(SectionAdultContent)
		}
	case "downloads", "requests", "grabs", "calendar":
		out.Add(SectionQueue)
	case "proposals":
		// Rename/Purge/Dedup review queues. Note the shape: apply-batch is
		// POST /api/proposals/apply-batch with NO {id} segment, so a
		// prefix rule keyed on /api/proposals/{id}/ would miss it. Keying
		// on segs[1] alone covers both shapes.
		out.Add(SectionOrganize)
	case "organize":
		// Claude 2026-08-05: GET /api/organize/events (organize audit log)
		// Reason: sibling of proposals queues; must be gated with Organize section
		// Troubleshooting: SL-9 failed — route unclassified after organize_events mux
		// Review if: organize subtree moves under /api/proposals/
		out.Add(SectionOrganize)
	case "autograb-batch":
		// Discover's select-mode bulk grab. Adult items inside the body
		// are Layer 2's job (mode.Build reads the per-item mode); the path
		// alone cannot see them.
		out.Add(SectionDiscover)
	case "trakt":
		classifyTrakt(segs[2:], out)
	case "admin":
		classifyAdmin(segs[2:], out)
	case "connections", "service-connections", "webhooks", "nodes",
		"netscan", "browse", "downloader", "ollama":
		out.Add(SectionSettings)
	case "pruning-rules":
		// Claude 2026-08-11: RECLASSIFIED {settings} -> {organize}.
		// Reason: the rules builder moved off Settings entirely onto the
		// Organize screen's Clean-up tab (the Rules card), and the Settings >
		// Pruning tab was removed. The 2026-08-03 classification turned on
		// picking "the LESS restrictive of the two screens they touch" — there
		// is now only ONE screen, so that tie-break no longer applies. Its
		// sibling routes on that same screen, /api/modes/{mode}/purge/*, are
		// already {organize}. Leaving it {settings} would produce two
		// incoherent states: a locked-Settings install could not edit rules
		// from an otherwise-unlocked Clean-up screen, and an unlocked-Settings
		// + locked-Organize install could edit them through an API whose only
		// UI is behind the lock.
		// Troubleshooting: a rule's own mode lives in the request BODY, never
		// the path, so an adult-scoped rule is NOT gated by Layer 1's adult
		// rule here.
		// Review if: the rules card ever moves back to Settings, or these
		// routes gain a mode segment in their path.
		out.Add(SectionOrganize)
	case "stashbox-databases":
		// Claude 2026-08-04: added for the configurable stash-box registry
		// (Stage 5, plan .omc/plans/autopilot-impl-stage5-stashboxdb-ui.md).
		// Reason: same attribution as "connections" above — this IS the
		// connections surface for stash-box rows, authored on Settings →
		// Library → Adult, and it carries API keys, so it must be locked
		// exactly when Settings is. Deliberately NOT {adult} as well: the
		// routes are addressed by row id with no mode in the path, and pairing
		// them with adult would leave a locked-Adult install unable to manage
		// its own credentials from an otherwise-unlocked Settings screen.
		// Review if: the registry ever gains a mode-scoped route.
		out.Add(SectionSettings)
	case "images", "notifications", "apikey", "auth", "setup",
		"section-lock", "openapi.yaml":
		// Deliberately unclassified, each for its own reason:
		//
		//   images/proxy      mode-agnostic by construction; gating it
		//                     would break every poster in the app (R-1).
		//   notifications     one global stream, not a per-section one
		//                     (R-3); §4.5 bounds its duration, not its
		//                     content.
		//   apikey, auth      governed by §4.4's explicit per-route table
		//   section-lock      (PUT /api/auth/mode is PIN-gated there),
		//                     not by path classification.
		//   setup             pre-auth boot path.
		//   openapi.yaml      unauthenticated already (R-5).
	}
	return out
}

// classifyModes handles /api/modes/{mode}/... — rest is the path after
// "modes".
//
// The Adult rule is the primary Adult mechanism in the whole feature: the
// segment immediately after /api/modes/ equalling "adult" adds
// adult-content. It covers roughly 25 {mode} routes that call no
// mode.Build at all, which is precisely why Layer 1 cannot be replaced by
// Layer 2.
func classifyModes(rest []string, out Set) {
	if len(rest) == 0 {
		return
	}
	if rest[0] == "adult" {
		out.Add(SectionAdultContent)
	}
	if len(rest) < 2 {
		return
	}
	switch rest[1] {
	case "collections":
		out.Add(SectionCollections)
	// Claude 2026-08-09: added "proposals" to the Organize case.
	// Reason: the workflow-agnostic per-proposal media route (today:
	// /video, generalizing the Dedup-only video-preview endpoint to serve
	// Rename and any future workflow's proposals too) is Organize-screen
	// data, and the {mode} segment in front of it in the URL path is what
	// keeps the existing Adult-content section-lock rule reachable for
	// this route family.
	// Troubleshooting: TestSectionLock_SL9_EveryRoutePatternIsClassified
	// fails without this — /api/modes/{mode}/proposals/{id}/video
	// classified into {adult-content} for Adult and nothing at all for
	// Movies/Series.
	// Review if: this route family moves off /api/modes/{mode}/proposals/.
	case "rename", "purge", "dedup", "proposals":
		out.Add(SectionOrganize)
	case "discover", "search", "autograb", "tmdb-search", "tvdb-search",
		"performers", "studios", "newest-rows":
		// performers/studios/newest-rows are Adult Discover's own rows and
		// drill-downs. newest-rows is also configured from Settings; per
		// the shared-route rule above it is attributed to Discover, the
		// less restrictive of the two.
		out.Add(SectionDiscover)
	case "grabs":
		out.Add(SectionQueue)
	case "scene-search", "scene-resolve":
		// Claude 2026-08-08: cross-mode move's Adult candidate picker.
		// Reason: attributed to Organize (the only screens that call it — Rename
		//   and Dedup), per the shared-route attribution rule. The adult-content
		//   section is added anyway by the rest[0]=="adult" rule above, and that
		//   is deliberate: this route RENDERS Adult scene metadata, so the
		//   view-only PIN gate applies to it even though the move/commit endpoint
		//   it feeds is ungated (see .omc/plans/autopilot-impl.md D-1).
		// Review if: AC7 is ever widened to ungate the Adult search too.
		out.Add(SectionOrganize)
	case "tags", "items", "scenes":
		// items/{itemId}/tags and scenes/{sceneId}/tags are tag CRUD;
		// neither prefix has any non-tag route under it today.
		out.Add(SectionTag)
	case "tracked":
		out.Add(SectionLibrary)
	case "library":
		// /library/root-folder{,/test} is a Settings control; everything
		// else under /library (series seasons, monitored flags) is the
		// Library screen's own data.
		if len(rest) >= 3 && rest[2] == "root-folder" {
			out.Add(SectionSettings)
		} else {
			out.Add(SectionLibrary)
		}
	case "identify-enabled", "naming-preset", "phash-threshold",
		"quality-prefs", "rename-match-config":
		out.Add(SectionSettings)
	case "poster":
		// Same reasoning as images/proxy: a poster is fetched by whichever
		// screen is rendering a card, so attributing it to one section
		// would blank posters across the others. An ADULT poster is still
		// gated, by the adult rule above.
	}
}

// classifyDiscover handles /api/discover/... — rest is the path after
// "discover".
func classifyDiscover(rest []string, out Set) {
	out.Add(SectionDiscover)
	if len(rest) == 0 {
		return
	}
	// row-order/{screen} and row-hidden/{screen} validate screen ∈
	// {mainstream, adult}; the adult one is Adult row configuration.
	// Belt-and-braces: Layer 3 re-checks these handlers explicitly.
	switch rest[0] {
	case "row-order", "row-hidden":
		if len(rest) >= 2 && rest[1] == "adult" {
			out.Add(SectionAdultContent)
		}
	}
}

// classifyTrakt handles /api/trakt/... — the watchlist row lives on
// Discover while the credentials/device-link flow lives in Settings, so
// the two halves are attributed separately rather than lumped together.
func classifyTrakt(rest []string, out Set) {
	if len(rest) == 0 {
		out.Add(SectionSettings)
		return
	}
	switch rest[0] {
	case "status", "watchlist":
		out.Add(SectionDiscover)
	default: // credentials, device/*, disconnect
		out.Add(SectionSettings)
	}
}

// classifyAdmin handles /api/admin/... — split because the Dashboard's own
// widgets live under the same prefix as genuinely administrative controls.
func classifyAdmin(rest []string, out Set) {
	if len(rest) == 0 {
		out.Add(SectionSettings)
		return
	}
	switch rest[0] {
	case "sysinfo", "storage-allocation":
		out.Add(SectionDashboard)
	default: // watch-folders, entity-sync, recheck
		out.Add(SectionSettings)
	}
}

// segments splits a cleaned request path into its non-empty segments.
func segments(rawPath string) []string {
	cleaned := path.Clean(rawPath)
	cleaned = strings.TrimPrefix(cleaned, "/")
	if cleaned == "" || cleaned == "." {
		return nil
	}
	return strings.Split(cleaned, "/")
}

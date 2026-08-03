package api

// SL-9 — classification completeness.
//
// sectionlock.Classify's default is ALLOW: an unrecognised path classifies as
// no section, so it is never gated. That default is what keeps a ~200-route
// app from breaking the moment anything is locked, and it is safe only while
// something proves every route was actually CONSIDERED. This is that proof:
// it AST-parses every route pattern literal registered across internal/api
// and cmd/sakms and fails if one neither classifies into a section nor
// appears in the explicit exempt list below. Deleting this test turns the
// allow-by-default into a real hole.
//
// # This test is NOT the plan's original SL-9, and the difference is recorded
//
// §8.2 specified SL-9 as additionally requiring a hoist of handler.go's
// []string{"stashdb","fansdb"} loop to a package-level var the test could
// expand — because a loop-built pattern has no literal in source for a
// scanner to find. THAT LOOP NO LONGER EXISTS: the Adult row-ordering
// unification deleted all twelve stash-box browse routes outright (see
// CLAUDE.md's "two drill-down entry points now, not four" correction). There
// is nothing left to hoist, `adultStashBoxes` appears nowhere in the tree,
// and NewMux has no loop-built patterns at all — every registration is a
// literal. So this test covers what §8.2 wanted it to cover, by a shorter
// route than §8.2 could have known about.
//
// # What it does NOT prove
//
// That a classified route is REACHABLE by the gate. A pattern can carry a
// perfectly good label and still be mounted on a mux no auth.Middleware
// wraps. That is SL-10's job (enforcement coverage), and SL-10 now EXISTS:
// sectionlock_sl10_test.go. §4.2's residual risk R-9 ("absent scope ⇒ ALLOW")
// names SL-10 as what closes it, so cite that file for enforcement coverage,
// not this one — this test still proves only that every route was CONSIDERED.
//
// Claude 2026-08-03: this paragraph previously said SL-10 "DOES NOT EXIST YET"
// and that R-9 was therefore open.
// Reason: SL-10 has since been written and passes — it is what caught
// NewNodesMux building its own auth.Middleware, so seven operator routes
// classified as `settings` were never actually reachable by the gate.
// Troubleshooting: the stale note told a reader R-9 was unclosed and pointed
// at nothing, so the natural next step was to rewrite a test that already
// existed.
// Review if: SL-10 is ever renamed or merged into this file.

import (
	"go/ast"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"

	"github.com/labbersanon/sakms/internal/sectionlock"
)

// sl9Packages are the two places routes are registered. cmd/sakms is included
// deliberately: three of its routes (/healthz, /api/openapi.yaml,
// /api/nodes/pair) are registered outside internal/api entirely, and a test
// scoped to this package would never see them.
var sl9Packages = []string{
	"github.com/labbersanon/sakms/internal/api",
	"github.com/labbersanon/sakms/cmd/sakms",
}

// sl9Exempt is the deliberately-unclassified set — every entry mirrors a
// documented decision in sectionlock.Classify or the plan, and each is keyed
// on the SUBSTITUTED pattern (see sl9Substitute).
//
// Adding a route here is a security decision, not a test fix: it declares
// that the route may be reached while ANY section is locked.
var sl9Exempt = map[string]string{
	// Mount patterns and non-API paths. The lock is a backend data boundary,
	// not a routing one — a locked tab still renders and shows a PIN overlay.
	// "/api/" is cmd/sakms's catch-all mount, not a route of its own: every
	// path it serves is registered individually inside NewMux and appears in
	// this scan on its own terms.
	"/":                  "the SPA shell and its static assets",
	"/api/":              "cmd/sakms's catch-all mount for NewMux, not a route",
	"/healthz":           "unauthenticated liveness probe, no data (cmd/sakms)",
	"/api/auth/":         "NewAuthMux's public mount",
	"/api/apikey":        "NewAPIKeyMux's exact mount",
	"/api/apikey/":       "NewAPIKeyMux's subtree mount",
	"/api/section-lock":  "the lock's own control surface — the way OUT of the gate",
	"/api/section-lock/": "the lock's own control surface",

	// Pre-auth boot paths. Gating any of these would make a locked instance
	// unbootable rather than merely locked.
	"/api/auth/setup":      "first-run bootstrap",
	"/api/auth/login":      "pre-session",
	"/api/auth/logout":     "giving up a credential is never privileged",
	"/api/auth/status":     "the one public probe",
	"/api/setup/status":    "setup wizard state",
	"/api/setup/dismissed": "setup wizard state",

	// §4.4 governs these by an explicit per-route table rather than by path
	// classification.
	"/api/auth/mode":          "PIN-GATED IN THE HANDLER — §4.4's critical row, not exempt in substance",
	"/api/auth/oidc":          "§4.4: changing OIDC config does not disable the lock",
	"/api/auth/oidc/login":    "pre-session IdP redirect",
	"/api/auth/oidc/callback": "pre-session IdP redirect",
	"/api/apikey/regenerate":  "§4.4: no escalation — a fresh key still needs X-Section-Pin",

	// The lock's own control surface. Exempt so the panel can be reached and
	// the badges drawn while `settings` itself is locked (§4.4's table); the
	// three mutating routes carry their own currentPin check instead.
	"/api/section-lock/status":   "chicken-and-egg: the frontend must read lock state to draw badges",
	"/api/section-lock/unlock":   "the way out of the gate",
	"/api/section-lock/lock":     "clearing a ticket is never privileged",
	"/api/section-lock/pin":      "gated by currentPin in the handler, not by path",
	"/api/section-lock/sections": "gated by currentPin in the handler, not by path",

	// Residual risks §14 accepts explicitly rather than solves.
	"/api/images/proxy":         "R-1: mode-agnostic by construction; gating blanks every poster",
	"/api/modes/movies/poster":  "R-1 again: a poster is fetched by whichever screen renders a card. The ADULT form of this same route IS gated, by classifyModes' adult rule — which is why the {mode} substitution here is deliberately mainstream",
	"/api/notifications/stream": "R-3: one global stream, not a per-section one; §4.5 bounds its duration",
	"/api/openapi.yaml":         "R-5: pre-existing unauthenticated route (cmd/sakms)",
	"/api/nodes/pair":           "node bearer enrolment, no operator data (cmd/sakms)",
}

// sl9Substitute turns a registered ServeMux pattern into a concrete request
// path Classify can be asked about: it drops the method prefix and fills in
// every wildcard.
//
// {mode} becomes "movies", never "adult" — deliberately. Anything that
// classifies non-empty for a mainstream mode also classifies for adult (the
// adult rule only ADDS a section), so the mainstream substitution is the
// strictly harder question and the one worth asking.
func sl9Substitute(pattern string) string {
	path := pattern
	if i := strings.Index(path, " "); i >= 0 {
		path = path[i+1:]
	}
	if i := strings.Index(path, "{$}"); i >= 0 {
		path = path[:i]
	}
	segments := strings.Split(path, "/")
	for i, seg := range segments {
		if !strings.HasPrefix(seg, "{") || !strings.HasSuffix(seg, "}") {
			continue
		}
		switch seg {
		case "{mode}":
			segments[i] = "movies"
		case "{screen}":
			segments[i] = "mainstream"
		default:
			segments[i] = "1"
		}
	}
	return strings.Join(segments, "/")
}

func TestSectionLock_SL9_EveryRoutePatternIsClassified(t *testing.T) {
	patterns := sl9RoutePatterns(t)
	if len(patterns) < 100 {
		// A guard against the scan silently finding nothing — a vacuous pass
		// is the failure mode a static test of this shape actually has.
		t.Fatalf("found only %d route patterns; the AST scan is not seeing the route table", len(patterns))
	}

	for _, pattern := range patterns {
		path := sl9Substitute(pattern)
		if sectionlock.Classify(path).Len() > 0 {
			continue
		}
		if _, ok := sl9Exempt[path]; ok {
			continue
		}
		t.Errorf("route %q (as %q) classifies into NO section and is not in sl9Exempt.\n"+
			"Either give it a classification in internal/sectionlock/routes.go, or add it to\n"+
			"sl9Exempt with the reason it may be reached while every section is locked.",
			pattern, path)
	}
}

// Every exemption must correspond to a route that actually exists. Without
// this, a deleted route leaves a stale entry behind that silently exempts a
// future route registered at the same path.
func TestSectionLock_SL9_NoStaleExemptions(t *testing.T) {
	live := map[string]bool{}
	for _, pattern := range sl9RoutePatterns(t) {
		live[sl9Substitute(pattern)] = true
	}
	for path := range sl9Exempt {
		if !live[path] {
			t.Errorf("sl9Exempt lists %q, which no longer matches any registered route — remove it", path)
		}
	}
}

// sl9RoutePatterns collects the first string-literal argument of every
// Handle/HandleFunc call in the two packages.
//
// A literal is required: a pattern built from a variable is invisible here,
// which is exactly the gap §8.2's hoist requirement existed to close. There
// are none today (every registration in NewMux and main.go is a literal), and
// the count guard in the test above is what notices if that changes
// wholesale.
func sl9RoutePatterns(t *testing.T) []string {
	t.Helper()

	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("resolving module root: %v", err)
	}
	pkgs, err := packages.Load(&packages.Config{
		Mode: packages.NeedName | packages.NeedSyntax | packages.NeedFiles,
		Dir:  root,
	}, sl9Packages...)
	if err != nil {
		t.Fatalf("loading packages: %v", err)
	}

	seen := map[string]bool{}
	var out []string
	for _, pkg := range pkgs {
		if len(pkg.Syntax) == 0 {
			t.Fatalf("package %s parsed to no files", pkg.PkgPath)
		}
		for _, file := range pkg.Syntax {
			ast.Inspect(file, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok || len(call.Args) == 0 {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				if sel.Sel.Name != "Handle" && sel.Sel.Name != "HandleFunc" {
					return true
				}
				lit, ok := call.Args[0].(*ast.BasicLit)
				if !ok {
					return true
				}
				pattern := strings.Trim(lit.Value, `"`)
				if pattern == "" || seen[pattern] {
					return true
				}
				seen[pattern] = true
				out = append(out, pattern)
				return true
			})
		}
	}
	return out
}

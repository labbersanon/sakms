package api

// SL-10 — enforcement coverage.
//
// SL-9 (sectionlock_static_test.go) proves every registered route pattern has a
// CLASSIFICATION. That is necessary and it is not sufficient: a pattern can
// carry a perfectly good label and still be mounted on a mux that no gated
// auth.Middleware wraps, in which case the label gates nothing at all. This
// test is the other half — it proves every classified pattern is REACHED
// through an auth.Middleware constructed WITH auth.WithSectionGate.
//
// It is what closes §14's residual risk R-9 ("Layer 2's absent-scope-means-
// ALLOW is safe because Layer 1 is independent and primary; SL-10 closes the
// reachable case"). Until this file existed that closure was asserted by the
// risk register and built nowhere.
//
// # "Behind auth.Middleware" is NOT the question — "behind a GATED one" is
//
// §8.2's original wording was "mounted behind an auth.Middleware-wrapped
// handler", and taken literally that wording passes on wiring this test
// deliberately fails: internal/api/nodes_mux.go wraps its operator routes in
// their OWN auth.Middleware, which satisfies PRIMARY auth and — before the
// change this test forced — carried no section gate at all. Primary auth
// answers "is this the operator?"; the section gate answers "has the operator
// entered the PIN?". Only the second one is this feature. So the assertion
// here is specifically that the auth.Middleware call carries the gate.
//
// # Why the analysis is per-MUX rather than per-route
//
// Every route pattern lives in exactly one mux constructor, and every mux
// constructor has exactly one mount expression in cmd/sakms's run(). That
// collapses reachability into a short identifier chase inside one function
// body:
//
//	apiMux       := api.NewMux(...)                                   (ctor)
//	protectedAPI := auth.Middleware(enc, store, apiMux, sectionGate...) (gated wrap)
//	top.Handle("/api/", protectedAPI)                                  (mount)
//
// so no ServeMux longest-prefix matching has to be reimplemented here. The one
// mux that does NOT follow the pattern — NewNodesMux, mounted bare because its
// routes each carry their own credential type — gets its own inner analysis of
// the three per-handler wrappers it defines.
//
// # What it does NOT prove
//
// That the gate is ARMED at runtime. `sectionGate...` spreads to zero options
// under SAKMS_SECTION_LOCK_DISABLE=1, which is the documented full-disarm path
// and is correct. This is a static test of the WIRING; the runtime arming is
// SL-22's job.

import (
	"go/ast"
	"go/token"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"

	"github.com/labbersanon/sakms/internal/sectionlock"
)

// sl10Allowlist is the set of CLASSIFIED patterns that are deliberately NOT
// reached through a gated auth.Middleware. It is a different question from
// sl9Exempt and therefore a different list: sl9Exempt means "classifies into no
// section"; this means "classifies into a section, and is still ungated on
// purpose".
//
// Keyed on the substituted path, same as sl9Exempt. Adding an entry here is a
// security decision: it declares that the route is reachable while the section
// it classifies into is locked.
//
// Every entry below is §8.2's corrected allowlist, re-verified against the live
// cmd/sakms/main.go and internal/api/nodes_mux.go rather than taken from the
// plan verbatim. Two items §8.2 listed are absent on purpose:
//
//   - NewAuthMux's public routes, GET /healthz and GET /api/openapi.yaml all
//     classify into NO section, so SL-9 owns them and this test never asks.
//   - nodes_mux.go's five operator routes and the dualAuth OPERATOR branch are
//     NOT allowlisted. §8.2 said the test "must recognise that they wrap
//     auth.Middleware internally" — under the gated-middleware definition above
//     that recognition is exactly what they failed, so they were fixed (the
//     gate is threaded into NewNodesMux) rather than exempted.
var sl10Allowlist = map[string]string{
	// The four node-agent routes: authenticated by a per-node bearer key
	// through auth.NodeKeyMiddleware, never by an operator credential. A node
	// agent holds no PIN and there is no interactive surface on which to enter
	// one, so gating these would break every node the moment `settings` was
	// locked. They classify as `settings` only because /api/nodes/* does.
	"/api/nodes/stream":          "node bearer key only — an agent has no PIN to present",
	"/api/nodes/heartbeat":       "node bearer key only",
	"/api/nodes/jobs/1/result":   "node bearer key only",
	"/api/nodes/browse/1/result": "node bearer key only",
	"/api/nodes/pair":            "R-8 sibling: unauthenticated pre-pairing SSE, no operator data (registered in cmd/sakms)",
}

// sl10MinChecked guards against the vacuous pass this test's shape actually
// has — an AST walk that quietly matches nothing reports success. SL-9 uses the
// same device with its own threshold.
const sl10MinChecked = 80

func TestSectionLock_SL10_EveryClassifiedRouteIsGated(t *testing.T) {
	wiring := sl10AnalyzeMain(t)
	routes := sl10AnalyzeAPI(t)

	// Anti-vacuity, part 1: the identifier auth.WithSectionGate is appended
	// into must have been found. If a rename made this empty, every gated-wrap
	// detection below would silently report false and the test would fail
	// loudly rather than pass green — but say so explicitly, because a reader
	// of a wall of failures should not have to infer it.
	if wiring.gateVar == "" {
		t.Fatal("no identifier carrying auth.WithSectionGate(...) was found in cmd/sakms — " +
			"the gated-mount analysis cannot work, so this test proves nothing")
	}
	// Anti-vacuity, part 2: NewMux carries the overwhelming majority of the
	// route surface. If it is not resolving as gated, the identifier chase is
	// broken, not the wiring.
	if !wiring.gatedCtors["NewMux"] {
		t.Fatalf("NewMux did not resolve to a gated auth.Middleware mount; the analysis is broken.\n"+
			"gate variable: %q, gated constructors: %v", wiring.gateVar, wiring.gatedCtorNames())
	}

	checked := 0
	for _, rt := range routes {
		path := sl9Substitute(rt.pattern)
		if sectionlock.Classify(path).Len() == 0 {
			continue // SL-9's territory, not this test's
		}
		if _, ok := sl10Allowlist[path]; ok {
			continue
		}
		checked++
		if wiring.gatedCtors[rt.fn] {
			continue
		}
		if rt.wrapper != "" && rt.wrapperGated {
			continue
		}
		t.Errorf("route %q (as %q, registered in %s.%s) classifies into %v but is NOT reached\n"+
			"through an auth.Middleware carrying auth.WithSectionGate — the section gate cannot see it.\n"+
			"Either thread the gate into its mount, or add it to sl10Allowlist with the reason it may be\n"+
			"reached while its section is locked.",
			rt.pattern, path, rt.pkg, rt.fn, sectionlock.Classify(path).Sorted())
	}

	if checked < sl10MinChecked {
		t.Fatalf("only %d classified routes were checked (expected at least %d); the AST scan is not seeing the route table",
			checked, sl10MinChecked)
	}

	// Reconciliation against SL-9's list, which also covers cmd/sakms.
	//
	// Without this, a classified route registered DIRECTLY on the top-level mux
	// — the way GET /healthz, GET /api/openapi.yaml and GET /api/nodes/pair
	// already are — would be demanded a classification by SL-9 and no gating by
	// anything at all, because the analysis above only walks internal/api. That
	// is precisely the hole this test exists to close, so it must not have a
	// blind spot shaped like the one place routes escape the package.
	known := map[string]bool{}
	for _, rt := range routes {
		known[sl9Substitute(rt.pattern)] = true
	}
	for _, pattern := range sl9RoutePatterns(t) {
		if wiring.mountPatterns[pattern] {
			continue // a delegation; its inner routes carry the verdict
		}
		path := sl9Substitute(pattern)
		if sectionlock.Classify(path).Len() == 0 {
			continue
		}
		if _, ok := sl10Allowlist[path]; ok || known[path] {
			continue
		}
		t.Errorf("route %q (as %q) is registered outside internal/api's mux constructors, classifies\n"+
			"into %v, and no gated auth.Middleware was found for it. Mount it behind one, or add it to\n"+
			"sl10Allowlist with the reason it may be reached while its section is locked.",
			pattern, path, sectionlock.Classify(path).Sorted())
	}
}

// Every allowlist entry must correspond to a route that still exists, for the
// same reason SL-9 asserts it: a stale entry silently exempts whatever future
// route lands on that path.
func TestSectionLock_SL10_NoStaleAllowlistEntries(t *testing.T) {
	live := map[string]bool{}
	for _, rt := range sl10AnalyzeAPI(t) {
		live[sl9Substitute(rt.pattern)] = true
	}
	for _, pattern := range sl9RoutePatterns(t) {
		live[sl9Substitute(pattern)] = true
	}
	for path := range sl10Allowlist {
		if !live[path] {
			t.Errorf("sl10Allowlist lists %q, which no longer matches any registered route — remove it", path)
		}
	}
}

// --- the analysis -----------------------------------------------------------

// sl10Route is one route registration and everything needed to decide whether
// the gate can see it.
type sl10Route struct {
	pattern string
	pkg     string
	fn      string // the enclosing function — a mux constructor
	// wrapper is the name of a locally-declared func-literal wrapper applied to
	// the handler (nodes_mux.go's op/nodeKey/dualAuth), "" when the handler is
	// registered directly.
	wrapper      string
	wrapperGated bool
}

// sl10Wiring is what cmd/sakms's run() says about which muxes are gated.
type sl10Wiring struct {
	// gateVar is the identifier auth.WithSectionGate(...) is appended into
	// ("sectionGate"), found rather than hard-coded so a rename fails loudly.
	gateVar string
	// gatedCtors maps a constructor's NAME (NewMux, NewSectionLockMux, …) to
	// whether some var it produced is wrapped in a gate-carrying middleware.
	gatedCtors map[string]bool
	// mountPatterns are the top-level patterns whose handler resolves to a mux
	// CONSTRUCTOR rather than to a leaf handler — "/api/", "/api/nodes/",
	// "/api/requests", and so on. They are delegations, not routes, so the
	// reconciliation below must not demand a gate of them: the routes they
	// delegate to are each analysed on their own terms inside internal/api.
	//
	// This distinction is not cosmetic. "/api/nodes/" and "/api/requests/" both
	// classify non-empty (path.Clean drops the trailing slash) and neither
	// appears in internal/api's own route table, which lists only the slashless
	// forms — so without this set they are two guaranteed false positives.
	mountPatterns map[string]bool
}

func (w sl10Wiring) gatedCtorNames() []string {
	var out []string
	for name := range w.gatedCtors {
		out = append(out, name)
	}
	return out
}

func sl10AnalyzeMain(t *testing.T) sl10Wiring {
	t.Helper()
	pkg := sl10Package(t, "github.com/labbersanon/sakms/cmd/sakms")

	wiring := sl10Wiring{gatedCtors: map[string]bool{}, mountPatterns: map[string]bool{}}

	// PASS 1 — find the identifier auth.WithSectionGate(...) is appended into.
	// A separate pass rather than one fused walk, because pass 2's gated-wrap
	// test compares against this name by IDENTITY: relying on the append
	// happening to precede every auth.Middleware call in source order would
	// make the whole analysis quietly order-dependent.
	for _, file := range pkg.Syntax {
		ast.Inspect(file, func(n ast.Node) bool {
			assign, ok := n.(*ast.AssignStmt)
			if !ok || len(assign.Lhs) != 1 || len(assign.Rhs) != 1 {
				return true
			}
			lhs, ok := assign.Lhs[0].(*ast.Ident)
			if !ok {
				return true
			}
			call, ok := assign.Rhs[0].(*ast.CallExpr)
			if !ok {
				return true
			}
			if fn, ok := call.Fun.(*ast.Ident); !ok || fn.Name != "append" {
				return true
			}
			for _, arg := range call.Args {
				if sl10IsSelectorCall(arg, "auth", "WithSectionGate") {
					wiring.gateVar = lhs.Name
				}
			}
			return true
		})
	}

	// PASS 2 — the constructor and middleware-wrap tables.
	//
	// ctorOf: varName -> constructor name, from `apiMux := api.NewMux(...)`.
	ctorOf := map[string]string{}
	// wrapInner/wrapGated: varName -> the handler it wraps, and whether the
	// wrapping auth.Middleware carried THE gate — spreading the specific
	// identifier found above, not merely spreading something.
	wrapInner := map[string]string{}
	wrapGated := map[string]bool{}

	for _, file := range pkg.Syntax {
		ast.Inspect(file, func(n ast.Node) bool {
			assign, ok := n.(*ast.AssignStmt)
			if !ok || len(assign.Lhs) != 1 || len(assign.Rhs) != 1 {
				return true
			}
			lhs, ok := assign.Lhs[0].(*ast.Ident)
			if !ok {
				return true
			}
			call, ok := assign.Rhs[0].(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			recv, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}
			switch {
			case recv.Name == "api" && strings.HasPrefix(sel.Sel.Name, "New"):
				ctorOf[lhs.Name] = sel.Sel.Name
			case recv.Name == "auth" && sel.Sel.Name == "Middleware" && len(call.Args) >= 3:
				if inner, ok := call.Args[2].(*ast.Ident); ok {
					wrapInner[lhs.Name] = inner.Name
				}
				wrapGated[lhs.Name] = sl10SpreadsGate(call, wiring.gateVar)
			}
			return true
		})
	}

	// A gated wrap marks whatever constructor it ultimately wraps as gated.
	// The chase is at most two hops today; the loop bound keeps a future
	// self-referential assignment from hanging the test.
	resolveCtor := func(name string) (string, bool) {
		cur := name
		for hop := 0; hop < 8 && cur != ""; hop++ {
			if ctor, ok := ctorOf[cur]; ok {
				return ctor, true
			}
			cur = wrapInner[cur]
		}
		return "", false
	}
	for v, gated := range wrapGated {
		if !gated {
			continue
		}
		if ctor, ok := resolveCtor(wrapInner[v]); ok {
			wiring.gatedCtors[ctor] = true
		}
	}

	// PASS 3 — which top-level patterns are DELEGATIONS rather than routes.
	for _, file := range pkg.Syntax {
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) < 2 {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || (sel.Sel.Name != "Handle" && sel.Sel.Name != "HandleFunc") {
				return true
			}
			lit, ok := call.Args[0].(*ast.BasicLit)
			if !ok {
				return true
			}
			handler, ok := call.Args[1].(*ast.Ident)
			if !ok {
				// A call expression (api.OpenapiHandler(), web.Handler()) or a
				// func literal — a leaf handler, so the pattern IS a route and
				// must answer for itself.
				return true
			}
			if _, ok := resolveCtor(handler.Name); ok {
				wiring.mountPatterns[strings.Trim(lit.Value, `"`)] = true
			}
			return true
		})
	}
	return wiring
}

// sl10SpreadsGate reports whether call spreads the section-gate options slice —
// `auth.Middleware(enc, store, mux, sectionGate...)`.
//
// It requires the spread argument to be the SPECIFIC identifier
// auth.WithSectionGate is appended into, not merely any variadic spread. A
// looser "call.Ellipsis is set" test would accept a mount that spreads some
// unrelated option slice and report it gated.
func sl10SpreadsGate(call *ast.CallExpr, gateVar string) bool {
	if call.Ellipsis == token.NoPos || len(call.Args) < 4 || gateVar == "" {
		return false
	}
	last, ok := call.Args[len(call.Args)-1].(*ast.Ident)
	return ok && last.Name == gateVar
}

// sl10AnalyzeAPI collects every route registration in internal/api together
// with its enclosing constructor and any local wrapper applied to the handler.
func sl10AnalyzeAPI(t *testing.T) []sl10Route {
	t.Helper()
	pkg := sl10Package(t, "github.com/labbersanon/sakms/internal/api")

	var out []sl10Route
	for _, file := range pkg.Syntax {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			// Local func-literal wrappers declared in this constructor —
			// nodes_mux.go's op/nodeKey/dualAuth. Collected first so a
			// registration can be resolved against them. Without this
			// restriction `op(listNodesHandler(...))` is syntactically
			// indistinguishable from `listNodesHandler(...)` itself.
			wrappers := map[string]bool{} // name -> carries a gated auth.Middleware
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				assign, ok := n.(*ast.AssignStmt)
				if !ok || len(assign.Lhs) != 1 || len(assign.Rhs) != 1 {
					return true
				}
				name, ok := assign.Lhs[0].(*ast.Ident)
				if !ok {
					return true
				}
				lit, ok := assign.Rhs[0].(*ast.FuncLit)
				if !ok {
					return true
				}
				wrappers[name.Name] = sl10BodyHasGatedMiddleware(lit.Body)
				return true
			})

			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok || len(call.Args) < 2 {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || (sel.Sel.Name != "Handle" && sel.Sel.Name != "HandleFunc") {
					return true
				}
				lit, ok := call.Args[0].(*ast.BasicLit)
				if !ok {
					return true
				}
				rt := sl10Route{
					pattern: strings.Trim(lit.Value, `"`),
					pkg:     "api",
					fn:      fn.Name.Name,
				}
				if handler, ok := call.Args[1].(*ast.CallExpr); ok {
					if ident, ok := handler.Fun.(*ast.Ident); ok {
						if gated, isWrapper := wrappers[ident.Name]; isWrapper {
							rt.wrapper = ident.Name
							rt.wrapperGated = gated
						}
					}
				}
				out = append(out, rt)
				return true
			})
		}
	}
	return out
}

// sl10BodyHasGatedMiddleware reports whether body contains an auth.Middleware
// call carrying section-gate options — either a forwarded variadic
// (`sectionGate...`) or a literal auth.WithSectionGate(...) argument.
func sl10BodyHasGatedMiddleware(body *ast.BlockStmt) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || !sl10IsSelectorCall(call, "auth", "Middleware") {
			return true
		}
		if call.Ellipsis != token.NoPos && len(call.Args) > 3 {
			found = true
			return false
		}
		for _, arg := range call.Args[min(3, len(call.Args)):] {
			if sl10IsSelectorCall(arg, "auth", "WithSectionGate") {
				found = true
				return false
			}
		}
		return true
	})
	return found
}

func sl10IsSelectorCall(e ast.Expr, recv, name string) bool {
	call, ok := e.(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != name {
		return false
	}
	x, ok := sel.X.(*ast.Ident)
	return ok && x.Name == recv
}

func sl10Package(t *testing.T, path string) *packages.Package {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("resolving module root: %v", err)
	}
	pkgs, err := packages.Load(&packages.Config{
		Mode: packages.NeedName | packages.NeedSyntax | packages.NeedFiles,
		Dir:  root,
	}, path)
	if err != nil {
		t.Fatalf("loading %s: %v", path, err)
	}
	if len(pkgs) != 1 || len(pkgs[0].Syntax) == 0 {
		t.Fatalf("loading %s: expected one package with parsed files, got %d", path, len(pkgs))
	}
	return pkgs[0]
}

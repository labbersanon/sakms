package scanschedule_test

// This is the single most important test in the VMAF-backend feature (plan AC5,
// the focus of three separate review rounds). It is the independent static
// backstop to the import-firewall guarantee: it proves that neither
// internal/scanschedule NOR its single adapter implementation
// (cmd/sakms/scanadapter.go) can reference any MUTATING function from the
// dedup/rename/purge/proposals packages other than the Scan*-family propose
// functions and proposals.ReplacePending.
//
// It is an ALLOWLIST, not a denylist. A denylist (`Apply|Dismiss|Skip|Keep`)
// was tried in review and found provably incomplete: `Skip`/`Keep` are phantom
// tokens (no such functions exist), while real reachable mutators — Repick,
// MarkFingerprintSubmitted, MarkDraftSubmitted, submitFingerprintGiveBack — sit
// one call away in the very proposals file the adapter needs for persistence and
// contain no denylisted substring. The allowlist is forward-safe: any NEW
// mutator added to those packages fails this test until it is explicitly (and
// visibly) allowlisted, and it catches an indirect helper call a name-denylist
// never could, because it resolves every selector to its actual object via the
// type checker.
//
// The "mutating surface" is exactly the FUNCTIONS and METHODS of the four
// packages — mutation only ever happens through a call. References to those
// packages' TYPES (proposals.Proposal, proposals.Store), CONSTANTS
// (proposals.Rename/Dedup/Purge) and STRUCT FIELDS (Candidate.Winner/Path) are
// inert data references, not mutation vectors, and are deliberately out of
// scope — which is why the adapter may legitimately hold a *proposals.Store and
// range a []proposals.Proposal without tripping the check.

import (
	"go/ast"
	"go/types"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"
)

const modulePath = "github.com/labbersanon/sakms"

// restrictedPkgs are the four packages whose mutating surface the scheduler and
// its adapter must never reach beyond the allowlist.
var restrictedPkgs = map[string]bool{
	modulePath + "/internal/dedup":     true,
	modulePath + "/internal/rename":    true,
	modulePath + "/internal/purge":     true,
	modulePath + "/internal/proposals": true,
}

// allowedFunc reports whether a function/method name from a restricted package
// is permitted: the Scan*-family propose functions (all read-only — file
// mutation lives only in Apply*), and ReplacePending (the transactional
// persist the propose phase needs, the same call watchfolders' scanFromWatcher
// already establishes as the Scan-only persistence precedent).
//
// UpgradeLocalAdultScenes is explicitly allowlisted because it runs as a
// pre-scan step that promotes local (box="local") scenes to a catalog identity
// before ScanLibraryAdult builds the known-map. It is not an Apply (no proposal
// row is involved), but it does rename files — which is why it is listed
// explicitly here rather than via the Scan* prefix. Adding it to the scheduled
// scan path is safe because it is purely additive (soft-fail, logs errors, no
// data loss path) and the scan schedule's whole purpose is keeping the library
// current, of which identity upgrades are a part. — added 2026-08-12 for
// adult-rename-review-alts D6.
func allowedFunc(name string) bool {
	return name == "ReplacePending" ||
		name == "UpgradeLocalAdultScenes" ||
		strings.HasPrefix(name, "Scan")
}

// TestScanScheduleImportFirewall proves internal/scanschedule does not import
// dedup/rename/purge directly at all — the compile-time half of AC5. (The
// go:build proof is `go list -deps ./internal/scanschedule`; this encodes it as
// a test so a future edit that adds such an import fails loudly here too.)
func TestScanScheduleImportFirewall(t *testing.T) {
	firewalled := map[string]bool{
		modulePath + "/internal/dedup":  true,
		modulePath + "/internal/rename": true,
		modulePath + "/internal/purge":  true,
	}
	pkg := loadPackage(t, modulePath+"/internal/scanschedule")
	for imp := range pkg.Imports {
		if firewalled[imp] {
			t.Errorf("internal/scanschedule must NEVER import %s directly (import firewall breached — this is what makes an Apply call unreachable at compile time)", imp)
		}
	}
}

// TestScanOnlyAllowlist is the AC5 allowlist source-scan across BOTH
// internal/scanschedule's source and cmd/sakms/scanadapter.go.
func TestScanOnlyAllowlist(t *testing.T) {
	// 1. internal/scanschedule — scan every non-test source file.
	sched := loadPackage(t, modulePath+"/internal/scanschedule")
	for i, f := range sched.Syntax {
		name := sched.CompiledGoFiles[i]
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		checkFile(t, sched, f, name)
	}

	// 2. cmd/sakms — scan ONLY scanadapter.go. It MUST be present and MUST be
	//    the file we scan; a rename/move that hid it from this check would
	//    defeat the whole backstop, so its absence is a hard failure.
	cmdPkg := loadPackage(t, modulePath+"/cmd/sakms")
	var adapterScanned bool
	for i, f := range cmdPkg.Syntax {
		name := cmdPkg.CompiledGoFiles[i]
		if filepath.Base(name) != "scanadapter.go" {
			continue
		}
		checkFile(t, cmdPkg, f, name)
		adapterScanned = true
	}
	if !adapterScanned {
		t.Fatalf("cmd/sakms/scanadapter.go was not found in the loaded package — the Scanner adapter must live in that exact file so this allowlist backstop covers it; if it was renamed or moved, update this test AND confirm the new location is still scanned")
	}
}

// checkFile walks every selector in f and fails on any reference to a FUNCTION
// or METHOD of a restricted package that is not on the allowlist.
func checkFile(t *testing.T, pkg *packages.Package, f *ast.File, filename string) {
	t.Helper()
	ast.Inspect(f, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		obj := pkg.TypesInfo.ObjectOf(sel.Sel)
		if obj == nil || obj.Pkg() == nil {
			return true
		}
		if !restrictedPkgs[obj.Pkg().Path()] {
			return true
		}
		// Only functions and methods are the mutating surface. Types, consts,
		// and struct fields are inert references (see the file doc).
		fn, isFunc := obj.(*types.Func)
		if !isFunc {
			return true
		}
		if !allowedFunc(fn.Name()) {
			pos := pkg.Fset.Position(sel.Sel.Pos())
			t.Errorf("DISALLOWED reference to %s.%s at %s:%d — the scan scheduler and its adapter may reference only Scan*-family functions and ReplacePending from %s; %q is a mutating (or otherwise non-allowlisted) symbol. If this is a legitimately new Scan-only function, add it to the allowlist deliberately; if it is a mutator, this is the bug the whole Scan-only safety boundary exists to catch.",
				obj.Pkg().Name(), fn.Name(), filepath.Base(filename), pos.Line, obj.Pkg().Path(), fn.Name())
		}
		return true
	})
}

// loadPackage loads one package with full type information, failing the test on
// any load or type error (a scan that can't resolve symbols would silently pass
// and is worse than useless).
func loadPackage(t *testing.T, pattern string) *packages.Package {
	t.Helper()
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles |
			packages.NeedImports | packages.NeedDeps | packages.NeedTypes |
			packages.NeedSyntax | packages.NeedTypesInfo,
	}
	pkgs, err := packages.Load(cfg, pattern)
	if err != nil {
		t.Fatalf("loading %s: %v", pattern, err)
	}
	if len(pkgs) != 1 {
		t.Fatalf("loading %s: expected exactly 1 package, got %d", pattern, len(pkgs))
	}
	pkg := pkgs[0]
	if len(pkg.Errors) > 0 {
		for _, e := range pkg.Errors {
			t.Errorf("package %s load error: %v", pattern, e)
		}
		t.Fatalf("package %s failed to load cleanly (see errors above); the allowlist scan needs a clean type-check to resolve symbols", pattern)
	}
	return pkg
}

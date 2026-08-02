package api

// T-6.1 — the AC10 proof, in internal/scanschedule/allowlist_test.go's idiom
// (golang.org/x/tools/go/packages + an AST walk resolved through the type
// checker). That package is studied elsewhere in this feature as a REJECTED
// scheduler shape; its TEST shape is adopted anyway, deliberately, because
// proving a structural property of source code is exactly what "no new
// scheduler was introduced" needs and exactly what a behavioral test cannot
// show.
//
// WHAT THIS TEST PROVES — stated at the narrow width the plan corrected it to,
// quoted verbatim from §9's "T-6.1 in detail" so a future reader cannot be left
// with the earlier, overclaiming framing:
//
//	T-6.1 proves that `internal/api/airdatemonitor.go` itself contains no
//	goroutine launch, no stdlib timer/ticker construction, no interval-setting
//	access, and no declared interval const, and that `cmd/sakms/main.go`
//	references nothing declared in it. It is a backstop against the specific,
//	likely regression — a future session "helpfully" giving this pass its own
//	ticker and `main.go` launch — not a general proof about the whole codebase.
//
// WHAT IT DOES NOT PROVE, spelled out because a test that overstates its
// guarantee is worse than no test (the next reviewer stops looking): it
// inspects ONE file for a FIXED list of syntactic indicators. A scheduler
// introduced any other way passes every assertion below — a `go` statement in a
// different file, a sync-package or third-party ticker wrapper, a recursive
// time.Sleep loop, a cron library, or a goroutine started inside a helper that
// airdatemonitor.go merely calls. Do not cite this test as evidence for any of
// those.

import (
	"go/ast"
	"go/token"
	"go/types"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"
)

const (
	airDateStaticModulePath = "github.com/labbersanon/sakms"

	// airDateStaticFile is the one file this backstop covers. Its absence is a
	// hard failure rather than a skip — mirroring allowlist_test.go's "the
	// adapter must live in that exact file" assertion — so a rename or a move
	// cannot silently hollow the whole test out into a vacuous pass.
	airDateStaticFile = "airdatemonitor.go"

	// airDateStaticMainFile is the launch site the fourth assertion covers: "not
	// launched as a scheduler" concretely means main.go names nothing from
	// airdatemonitor.go, so the pass is reachable only through
	// runUsenetRetryCycle.
	airDateStaticMainFile = "main.go"
)

// airDateBannedTimeFuncs are the package-level `time` functions that would give
// this pass a cadence of its own. time.After and time.Sleep are on the list for
// the same reason as the ticker/timer constructors: they are the easiest things
// to reach for when writing a "just wait a bit" loop.
//
// The check resolves each reference through TypesInfo, never by string match,
// and is scoped to these FUNCTION OBJECTS specifically — airdatemonitor.go
// legitimately uses time.Time and time.Duration throughout, so an assertion on
// the `time` import as a whole would be permanently red.
var airDateBannedTimeFuncs = map[string]bool{
	"NewTicker": true,
	"Tick":      true,
	"NewTimer":  true,
	"AfterFunc": true,
	"After":     true,
	"Sleep":     true,
}

// TestAirDateMonitorHasNoSchedulerOfItsOwn is assertions 1, 2, 3 and 4: the file
// exists, launches no goroutine syntactically, constructs no stdlib
// timer/ticker, reads or writes no interval setting, and declares no interval
// const.
func TestAirDateMonitorHasNoSchedulerOfItsOwn(t *testing.T) {
	apiPkg, _ := airDateStaticLoad(t)
	file := airDateStaticFindFile(t, apiPkg, airDateStaticFile)

	// Assertion 2 — zero goroutines launched syntactically in this file.
	ast.Inspect(file, func(n ast.Node) bool {
		if goStmt, ok := n.(*ast.GoStmt); ok {
			pos := apiPkg.Fset.Position(goStmt.Pos())
			t.Errorf("%s:%d launches a goroutine — this pass is deliberately a plain function called as the last step of runUsenetRetryCycle, and AC5 forbids a new goroutine for this feature. If a scheduler is genuinely wanted here, that is a spec change, not a test update.",
				airDateStaticFile, pos.Line)
		}
		return true
	})

	// Assertion 4, second half — no declared const whose name contains
	// "Interval". Kept literally as specified; do not broaden it.
	ast.Inspect(file, func(n ast.Node) bool {
		decl, ok := n.(*ast.GenDecl)
		if !ok || decl.Tok != token.CONST {
			return true
		}
		for _, spec := range decl.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for _, name := range vs.Names {
				if strings.Contains(name.Name, "Interval") {
					pos := apiPkg.Fset.Position(name.Pos())
					t.Errorf("%s:%d declares const %q — this feature adds NO new interval setting (AC5's second half); its cadence comes entirely from airDateRetryBackoff, which reads no interval key at all.",
						airDateStaticFile, pos.Line, name.Name)
				}
			}
		}
		return true
	})

	// Assertions 3 and 4's first half share one ident walk, because both resolve
	// a reference to its object rather than matching a name.
	//
	// loadIntervalSeconds/storeIntervalSeconds live in the SAME package
	// (internal/api/interval.go), so a reference to either is a bare identifier,
	// never a selector — a selector-only walk would make this assertion vacuous.
	// Their absence from the package scope is a hard failure for the same reason
	// the file's absence is: a rename must not silently retire the check.
	loadInterval := airDateStaticLookup(t, apiPkg, "loadIntervalSeconds")
	storeInterval := airDateStaticLookup(t, apiPkg, "storeIntervalSeconds")

	ast.Inspect(file, func(n ast.Node) bool {
		ident, ok := n.(*ast.Ident)
		if !ok {
			return true
		}
		obj := apiPkg.TypesInfo.Uses[ident]
		if obj == nil {
			return true
		}
		pos := apiPkg.Fset.Position(ident.Pos())

		if obj == loadInterval || obj == storeInterval {
			t.Errorf("%s:%d references %s — this feature reads and writes NO interval setting (AC5's second half). Running inside the existing cycle is the whole point; a new interval key would be a new scheduler in all but name.",
				airDateStaticFile, pos.Line, obj.Name())
			return true
		}

		fn, isFunc := obj.(*types.Func)
		if !isFunc || fn.Pkg() == nil || fn.Pkg().Path() != "time" {
			return true
		}
		// Package-level functions only. (time.Time).After is a *types.Func in
		// package "time" named "After" too, and comparing two air dates with it
		// is legitimate — the receiver is what tells the two apart.
		if sig, ok := fn.Type().(*types.Signature); !ok || sig.Recv() != nil {
			return true
		}
		if airDateBannedTimeFuncs[fn.Name()] {
			t.Errorf("%s:%d references time.%s — this pass has no cadence of its own by design; it runs when runUsenetRetryCycle runs. A wait/tick here is the exact regression this backstop exists to catch.",
				airDateStaticFile, pos.Line, fn.Name())
		}
		return true
	})
}

// TestMainDoesNotReferenceTheAirDateMonitor is the fourth assertion: cmd/sakms's
// main.go names no symbol declared in airdatemonitor.go. That is the concrete
// meaning of "not launched as a scheduler" — every other background pass in this
// codebase is started from main.go's launch block, and this one is reachable
// only through runUsenetRetryCycle.
//
// Every declaration in airdatemonitor.go is currently unexported, so this holds
// by language rule today as much as by design. It is kept because the regression
// it guards is a rename-to-exported plus a main.go launch, which is exactly the
// shape the sibling torrent-behavior plan originally recommended.
func TestMainDoesNotReferenceTheAirDateMonitor(t *testing.T) {
	apiPkg, cmdPkg := airDateStaticLoad(t)
	monitorFile := airDateStaticFindFile(t, apiPkg, airDateStaticFile)
	mainFile := airDateStaticFindFile(t, cmdPkg, airDateStaticMainFile)

	// Everything DECLARED in airdatemonitor.go, by object identity and by
	// (package path, name) pair. Both are checked: the two packages are loaded in
	// ONE packages.Load call so object identity is meaningful, and the name pair
	// is a cheap independent backstop in case that ever stops being true — a
	// separate load would build a separate type-checker universe in which no
	// object is ever pointer-equal and this assertion would pass for the wrong
	// reason.
	declared := map[types.Object]bool{}
	declaredNames := map[string]bool{}
	monitorPath := apiPkg.Fset.Position(monitorFile.Pos()).Filename
	for ident, obj := range apiPkg.TypesInfo.Defs {
		if obj == nil || obj.Pkg() == nil {
			continue
		}
		if apiPkg.Fset.Position(ident.Pos()).Filename != monitorPath {
			continue
		}
		declared[obj] = true
		declaredNames[obj.Name()] = true
	}
	if len(declared) == 0 {
		t.Fatalf("no declarations were collected from %s — the type info is empty, so this assertion would pass vacuously", airDateStaticFile)
	}

	ast.Inspect(mainFile, func(n ast.Node) bool {
		ident, ok := n.(*ast.Ident)
		if !ok {
			return true
		}
		obj := cmdPkg.TypesInfo.Uses[ident]
		if obj == nil || obj.Pkg() == nil {
			return true
		}
		if obj.Pkg().Path() != airDateStaticModulePath+"/internal/api" {
			return true
		}
		if !declared[obj] && !declaredNames[obj.Name()] {
			return true
		}
		pos := cmdPkg.Fset.Position(ident.Pos())
		t.Errorf("%s:%d references %s, which is declared in %s — air-date monitoring must NOT be launched or wired from main.go. It is the third pass inside runUsenetRetryCycle; adding it to main.go's launch block would make it the seventh interval-driven scheduler, which AC5 forbids.",
			airDateStaticMainFile, pos.Line, obj.Name(), airDateStaticFile)
		return true
	})
}

// airDateStaticLoad loads internal/api and cmd/sakms in ONE call, with full type
// information. One call is load-bearing, not tidiness: separate calls build
// separate type-checker universes, and the cross-package object-identity check
// above would then always be false.
func airDateStaticLoad(t *testing.T) (apiPkg, cmdPkg *packages.Package) {
	t.Helper()
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles |
			packages.NeedImports | packages.NeedDeps | packages.NeedTypes |
			packages.NeedSyntax | packages.NeedTypesInfo,
	}
	apiPattern := airDateStaticModulePath + "/internal/api"
	cmdPattern := airDateStaticModulePath + "/cmd/sakms"
	pkgs, err := packages.Load(cfg, apiPattern, cmdPattern)
	if err != nil {
		t.Fatalf("loading %s and %s: %v", apiPattern, cmdPattern, err)
	}
	for _, pkg := range pkgs {
		if len(pkg.Errors) > 0 {
			for _, e := range pkg.Errors {
				t.Errorf("package %s load error: %v", pkg.PkgPath, e)
			}
			t.Fatalf("package %s failed to load cleanly (see errors above); a scan that cannot resolve symbols would silently pass and is worse than useless", pkg.PkgPath)
		}
		switch pkg.PkgPath {
		case apiPattern:
			apiPkg = pkg
		case cmdPattern:
			cmdPkg = pkg
		}
	}
	if apiPkg == nil || cmdPkg == nil {
		t.Fatalf("expected both %s and %s to load; got %d packages", apiPattern, cmdPattern, len(pkgs))
	}
	return apiPkg, cmdPkg
}

// airDateStaticFindFile returns the named file's syntax tree, failing hard if it
// is not among the package's compiled files.
func airDateStaticFindFile(t *testing.T, pkg *packages.Package, base string) *ast.File {
	t.Helper()
	for i, name := range pkg.CompiledGoFiles {
		if filepath.Base(name) != base {
			continue
		}
		if i >= len(pkg.Syntax) {
			break
		}
		return pkg.Syntax[i]
	}
	t.Fatalf("%s was not found in %s's compiled files — this backstop covers that exact file, so a rename or move would silently defeat it. If the file legitimately moved, update this test AND confirm the new location is still scanned.", base, pkg.PkgPath)
	return nil
}

// airDateStaticLookup resolves a package-scope symbol, failing hard if it is
// gone. A missing symbol must not degrade an assertion about it into a
// no-reference-found pass.
func airDateStaticLookup(t *testing.T, pkg *packages.Package, name string) types.Object {
	t.Helper()
	obj := pkg.Types.Scope().Lookup(name)
	if obj == nil {
		t.Fatalf("%s is no longer declared in %s — the interval-access assertion resolves references to this exact object, so its absence would make that check vacuous. If it was renamed, update this test.", name, pkg.PkgPath)
	}
	return obj
}

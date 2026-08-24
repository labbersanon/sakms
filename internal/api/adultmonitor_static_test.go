// Static AST backstop for adultmonitor.go — mirrors airdatemonitor_static_test.go
// in structure and intent. Proves that adultmonitor.go itself launches no
// goroutine, constructs no stdlib timer/ticker, and that cmd/sakms/main.go
// references nothing declared in it (because this pass is only reachable as
// the fifth step of runUsenetRetryCycle, not as an independent scheduler).
//
// WHAT THIS TEST PROVES (narrow, verbatim scope):
//
//	T-static proves that internal/api/adultmonitor.go contains no goroutine
//	launch, no stdlib timer/ticker construction, and that cmd/sakms/main.go
//	references nothing declared in it. It is a backstop against the specific
//	likely regression — a future session "helpfully" giving this pass its own
//	ticker and main.go launch.
//
// WHAT IT DOES NOT PROVE: see the parallel comment in airdatemonitor_static_test.go.
package api

import (
	"go/ast"
	"go/types"
	"path/filepath"
	"testing"

	"golang.org/x/tools/go/packages"
)

const (
	adultMonitorStaticModulePath = "github.com/labbersanon/sakms"
	adultMonitorStaticFile       = "adultmonitor.go"
	adultMonitorStaticMainFile   = "main.go"
)

var adultMonitorBannedTimeFuncs = map[string]bool{
	"NewTicker": true,
	"Tick":      true,
	"NewTimer":  true,
	"AfterFunc": true,
	"After":     true,
	"Sleep":     true,
}

// TestAdultMonitorHasNoSchedulerOfItsOwn asserts that adultmonitor.go:
//  1. Launches no goroutines syntactically.
//  2. Constructs no stdlib timer/ticker.
func TestAdultMonitorHasNoSchedulerOfItsOwn(t *testing.T) {
	apiPkg, _ := adultMonitorStaticLoad(t)
	file := adultMonitorStaticFindFile(t, apiPkg, adultMonitorStaticFile)

	// Assertion 1 — no goroutines.
	ast.Inspect(file, func(n ast.Node) bool {
		if goStmt, ok := n.(*ast.GoStmt); ok {
			pos := apiPkg.Fset.Position(goStmt.Pos())
			t.Errorf("%s:%d launches a goroutine — this pass is deliberately a plain function called as the fifth step of runUsenetRetryCycle. A new goroutine here is the exact regression this backstop exists to prevent.",
				adultMonitorStaticFile, pos.Line)
		}
		return true
	})

	// Assertion 2 — no banned time functions (ticker/timer/sleep).
	ast.Inspect(file, func(n ast.Node) bool {
		ident, ok := n.(*ast.Ident)
		if !ok {
			return true
		}
		obj := apiPkg.TypesInfo.Uses[ident]
		if obj == nil {
			return true
		}
		fn, isFunc := obj.(*types.Func)
		if !isFunc || fn.Pkg() == nil || fn.Pkg().Path() != "time" {
			return true
		}
		// Package-level functions only — method receivers excluded.
		if sig, ok := fn.Type().(*types.Signature); !ok || sig.Recv() != nil {
			return true
		}
		if adultMonitorBannedTimeFuncs[fn.Name()] {
			pos := apiPkg.Fset.Position(ident.Pos())
			t.Errorf("%s:%d references time.%s — this pass runs inside runUsenetRetryCycle, not on its own cadence.",
				adultMonitorStaticFile, pos.Line, fn.Name())
		}
		return true
	})
}

// TestMainDoesNotReferenceAdultMonitor asserts that cmd/sakms/main.go references
// nothing declared in adultmonitor.go, ensuring the pass is reachable only
// through runUsenetRetryCycle and not wired as a standalone scheduler.
func TestMainDoesNotReferenceAdultMonitor(t *testing.T) {
	apiPkg, cmdPkg := adultMonitorStaticLoad(t)
	monitorFile := adultMonitorStaticFindFile(t, apiPkg, adultMonitorStaticFile)
	mainFile := adultMonitorStaticFindFile(t, cmdPkg, adultMonitorStaticMainFile)

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
		t.Fatalf("no declarations collected from %s — type info is empty, assertion would pass vacuously", adultMonitorStaticFile)
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
		if obj.Pkg().Path() != adultMonitorStaticModulePath+"/internal/api" {
			return true
		}
		if !declared[obj] && !declaredNames[obj.Name()] {
			return true
		}
		pos := cmdPkg.Fset.Position(ident.Pos())
		t.Errorf("%s:%d references %s declared in %s — adult monitor dispatch must NOT be launched from main.go; it is the fifth pass inside runUsenetRetryCycle.",
			adultMonitorStaticMainFile, pos.Line, obj.Name(), adultMonitorStaticFile)
		return true
	})
}

func adultMonitorStaticLoad(t *testing.T) (apiPkg, cmdPkg *packages.Package) {
	t.Helper()
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles |
			packages.NeedImports | packages.NeedDeps | packages.NeedTypes |
			packages.NeedSyntax | packages.NeedTypesInfo,
	}
	apiPattern := adultMonitorStaticModulePath + "/internal/api"
	cmdPattern := adultMonitorStaticModulePath + "/cmd/sakms"
	pkgs, err := packages.Load(cfg, apiPattern, cmdPattern)
	if err != nil {
		t.Fatalf("loading packages: %v", err)
	}
	for _, pkg := range pkgs {
		if len(pkg.Errors) > 0 {
			for _, e := range pkg.Errors {
				t.Errorf("package %s load error: %v", pkg.PkgPath, e)
			}
			t.Fatalf("package %s failed to load cleanly", pkg.PkgPath)
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

func adultMonitorStaticFindFile(t *testing.T, pkg *packages.Package, base string) *ast.File {
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
	t.Fatalf("%s was not found in %s's compiled files — if the file moved, update this test.", base, pkg.PkgPath)
	return nil
}

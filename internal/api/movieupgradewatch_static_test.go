// Static AST backstop for movieupgradewatch.go — mirrors adultmonitor_static_test.go.
// Proves that movieupgradewatch.go launches no goroutine, constructs no stdlib
// timer/ticker, and that cmd/sakms/main.go references nothing declared in it
// (the pass is only reachable as the sixth step of runUsenetRetryCycle).
package api

import (
	"go/ast"
	"go/types"
	"path/filepath"
	"testing"

	"golang.org/x/tools/go/packages"
)

const (
	qualityWatchStaticModulePath = "github.com/labbersanon/sakms"
	qualityWatchStaticFile       = "movieupgradewatch.go"
	qualityWatchStaticMainFile   = "main.go"
)

var qualityWatchBannedTimeFuncs = map[string]bool{
	"NewTicker": true,
	"Tick":      true,
	"NewTimer":  true,
	"AfterFunc": true,
	"After":     true,
	"Sleep":     true,
}

func TestMovieUpgradeWatchHasNoSchedulerOfItsOwn(t *testing.T) {
	apiPkg, _ := qualityWatchStaticLoad(t)
	file := qualityWatchStaticFindFile(t, apiPkg, qualityWatchStaticFile)

	ast.Inspect(file, func(n ast.Node) bool {
		if goStmt, ok := n.(*ast.GoStmt); ok {
			pos := apiPkg.Fset.Position(goStmt.Pos())
			t.Errorf("%s:%d launches a goroutine — this pass is a plain function called as the sixth step of runUsenetRetryCycle.",
				qualityWatchStaticFile, pos.Line)
		}
		return true
	})

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
		if sig, ok := fn.Type().(*types.Signature); !ok || sig.Recv() != nil {
			return true
		}
		if qualityWatchBannedTimeFuncs[fn.Name()] {
			pos := apiPkg.Fset.Position(ident.Pos())
			t.Errorf("%s:%d references time.%s — this pass runs inside runUsenetRetryCycle, not on its own cadence.",
				qualityWatchStaticFile, pos.Line, fn.Name())
		}
		return true
	})
}

func TestMainDoesNotReferenceMovieUpgradeWatch(t *testing.T) {
	apiPkg, cmdPkg := qualityWatchStaticLoad(t)
	monitorFile := qualityWatchStaticFindFile(t, apiPkg, qualityWatchStaticFile)
	mainFile := qualityWatchStaticFindFile(t, cmdPkg, qualityWatchStaticMainFile)

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
		t.Fatalf("no declarations collected from %s — type info is empty, assertion would pass vacuously", qualityWatchStaticFile)
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
		if obj.Pkg().Path() != qualityWatchStaticModulePath+"/internal/api" {
			return true
		}
		if !declared[obj] && !declaredNames[obj.Name()] {
			return true
		}
		pos := cmdPkg.Fset.Position(ident.Pos())
		t.Errorf("%s:%d references %s declared in %s — movie upgrade-watch must NOT be launched from main.go; it is the sixth pass inside runUsenetRetryCycle.",
			qualityWatchStaticMainFile, pos.Line, obj.Name(), qualityWatchStaticFile)
		return true
	})
}

func qualityWatchStaticLoad(t *testing.T) (apiPkg, cmdPkg *packages.Package) {
	t.Helper()
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles |
			packages.NeedImports | packages.NeedDeps | packages.NeedTypes |
			packages.NeedSyntax | packages.NeedTypesInfo,
	}
	apiPattern := qualityWatchStaticModulePath + "/internal/api"
	cmdPattern := qualityWatchStaticModulePath + "/cmd/sakms"
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

func qualityWatchStaticFindFile(t *testing.T, pkg *packages.Package, base string) *ast.File {
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

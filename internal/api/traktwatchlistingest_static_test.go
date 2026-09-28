// Static AST backstop for traktwatchlistingest.go — mirrors
// movieupgradewatch_static_test.go. Proves that traktwatchlistingest.go
// launches no goroutine, constructs no stdlib timer/ticker, and that
// cmd/sakms/main.go references nothing declared in it (the pass is only
// reachable as the seventh step of runUsenetRetryCycle).
package api

import (
	"go/ast"
	"go/types"
	"path/filepath"
	"testing"

	"golang.org/x/tools/go/packages"
)

const (
	traktIngestStaticModulePath = "github.com/labbersanon/sakms"
	traktIngestStaticFile       = "traktwatchlistingest.go"
	traktIngestStaticMainFile   = "main.go"
)

var traktIngestBannedTimeFuncs = map[string]bool{
	"NewTicker": true,
	"Tick":      true,
	"NewTimer":  true,
	"AfterFunc": true,
	"After":     true,
	"Sleep":     true,
}

func TestTraktWatchlistIngestHasNoSchedulerOfItsOwn(t *testing.T) {
	apiPkg, _ := traktIngestStaticLoad(t)
	file := traktIngestStaticFindFile(t, apiPkg, traktIngestStaticFile)

	ast.Inspect(file, func(n ast.Node) bool {
		if goStmt, ok := n.(*ast.GoStmt); ok {
			pos := apiPkg.Fset.Position(goStmt.Pos())
			t.Errorf("%s:%d launches a goroutine — this pass is a plain function called as the seventh step of runUsenetRetryCycle.",
				traktIngestStaticFile, pos.Line)
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
		if traktIngestBannedTimeFuncs[fn.Name()] {
			pos := apiPkg.Fset.Position(ident.Pos())
			t.Errorf("%s:%d references time.%s — this pass runs inside runUsenetRetryCycle, not on its own cadence.",
				traktIngestStaticFile, pos.Line, fn.Name())
		}
		return true
	})
}

func TestMainDoesNotReferenceTraktWatchlistIngest(t *testing.T) {
	apiPkg, cmdPkg := traktIngestStaticLoad(t)
	monitorFile := traktIngestStaticFindFile(t, apiPkg, traktIngestStaticFile)
	mainFile := traktIngestStaticFindFile(t, cmdPkg, traktIngestStaticMainFile)

	declared := map[types.Object]bool{}
	monitorPath := apiPkg.Fset.Position(monitorFile.Pos()).Filename
	for ident, obj := range apiPkg.TypesInfo.Defs {
		if obj == nil || obj.Pkg() == nil {
			continue
		}
		if apiPkg.Fset.Position(ident.Pos()).Filename != monitorPath {
			continue
		}
		declared[obj] = true
	}
	if len(declared) == 0 {
		t.Fatalf("no declarations collected from %s — type info is empty, assertion would pass vacuously", traktIngestStaticFile)
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
		if obj.Pkg().Path() != traktIngestStaticModulePath+"/internal/api" {
			return true
		}
		if !declared[obj] {
			return true
		}
		pos := cmdPkg.Fset.Position(ident.Pos())
		t.Errorf("%s:%d references %s declared in %s — Trakt watchlist ingest must NOT be launched from main.go; it is the seventh pass inside runUsenetRetryCycle.",
			traktIngestStaticMainFile, pos.Line, obj.Name(), traktIngestStaticFile)
		return true
	})
}

func traktIngestStaticLoad(t *testing.T) (apiPkg, cmdPkg *packages.Package) {
	t.Helper()
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles |
			packages.NeedImports | packages.NeedDeps | packages.NeedTypes |
			packages.NeedSyntax | packages.NeedTypesInfo,
	}
	apiPattern := traktIngestStaticModulePath + "/internal/api"
	cmdPattern := traktIngestStaticModulePath + "/cmd/sakms"
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

func traktIngestStaticFindFile(t *testing.T, pkg *packages.Package, base string) *ast.File {
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

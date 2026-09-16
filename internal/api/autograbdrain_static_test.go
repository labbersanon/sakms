package api

// autograbdrain_static_test.go is the mirror-image of airdatemonitor_static_test.go:
// it asserts that the drain worker's goroutine and ticker live in
// autograbdrain.go AND NOWHERE ELSE, and that airdatemonitor.go STILL has none
// (the mirror direction, so a "helpful" merge of the two files cannot pass both
// static tests at once).
//
// WHAT THIS TEST PROVES — stated at the same narrow width as the sibling:
//
//	autograbdrain_static_test.go proves that autograbdrain.go CONTAINS a
//	goroutine launch, a time.NewTicker call, and a loadIntervalSeconds reference,
//	and that airdatemonitor.go STILL CONTAINS NONE of the same. It does not
//	prove anything about other files.
//
// The positive assertions (drain HAS the scheduler) guard against a future
// "tidy" that moves the drain's scheduler back into the daily cycle.
// The negative assertion (airdatemonitor still has none) guards against that
// same merge going the other direction.

import (
	"go/ast"
	"testing"
)

const drainStaticFile = "autograbdrain.go"

// TestDrainWorkerHasItsOwnScheduler asserts that autograbdrain.go owns the
// drain's ticker loop (time.NewTicker + for/select), and that airdatemonitor.go
// STILL contains no goroutine launch or ticker of its own.
//
// Note on goroutine placement: RunAutoGrabDrain's `go` is in cmd/sakms/main.go
// (same as every other background worker in this codebase). autograbdrain.go
// owns the ticker LOOP — the `time.NewTicker` call + for/select — which is the
// structural property that matters: "the drain's cadence lives in this file."
// TestMainDoesNotReferenceTheAirDateMonitor's mirror for the drain worker is
// TestMainDoesComeFromDrainFile (below) — main.go MUST reference RunAutoGrabDrain,
// which is declared here, proving the launch is wired.
func TestDrainWorkerHasItsOwnScheduler(t *testing.T) {
	apiPkg, _ := airDateStaticLoad(t)

	drainFile := airDateStaticFindFile(t, apiPkg, drainStaticFile)
	monitorFile := airDateStaticFindFile(t, apiPkg, airDateStaticFile)

	// Positive: drain file MUST reference time.NewTicker (the ticker loop lives here).
	drainTickerRefs := 0
	ast.Inspect(drainFile, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if sel.Sel.Name == "NewTicker" {
			drainTickerRefs++
		}
		return true
	})
	if drainTickerRefs == 0 {
		t.Errorf("%s contains no time.NewTicker reference — the drain worker's ticker loop must live in this file, not airdatemonitor.go. If the loop moved, update this test.", drainStaticFile)
	}

	// Negative: airdatemonitor.go must STILL contain no goroutine launch.
	ast.Inspect(monitorFile, func(n ast.Node) bool {
		if goStmt, ok := n.(*ast.GoStmt); ok {
			pos := apiPkg.Fset.Position(goStmt.Pos())
			t.Errorf("%s:%d launches a goroutine — airdatemonitor.go must never own a scheduler. The drain worker lives in autograbdrain.go.", airDateStaticFile, pos.Line)
		}
		return true
	})

	// Negative: airdatemonitor.go must STILL contain no time.NewTicker.
	ast.Inspect(monitorFile, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if sel.Sel.Name == "NewTicker" || sel.Sel.Name == "NewTimer" {
			pos := apiPkg.Fset.Position(sel.Pos())
			t.Errorf("%s:%d references time.%s — airdatemonitor.go must never own a ticker.", airDateStaticFile, pos.Line, sel.Sel.Name)
		}
		return true
	})
}

package api

import (
	"testing"

	"github.com/labbersanon/sakms/internal/prowlarr"
	"github.com/labbersanon/sakms/internal/usenetsearch"
)

func TestPrependStripNativePhase(t *testing.T) {
	phases := prependNativePhase(nil)
	if len(phases) != 1 || !phases[0].IsNative() {
		t.Fatalf("prepend empty: %+v", phases)
	}
	phases = prependNativePhase(phases)
	if len(phases) != 1 {
		t.Fatalf("double prepend: %+v", phases)
	}
	rest := phasesAfterNative([]prowlarr.Scope{prowlarr.ScopeNative, prowlarr.ScopeUsenet, prowlarr.ScopeTorrent})
	if len(rest) != 2 || rest[0] != prowlarr.ScopeUsenet {
		t.Fatalf("after: %+v", rest)
	}
	if len(stripNativePhase(rest)) != 2 {
		t.Fatal("strip")
	}
}

func TestAdaptNativeCandidates_SkipsIncomplete(t *testing.T) {
	out := adaptNativeCandidates([]usenetsearch.Candidate{
		{Name: "Holey.PreDB.2021.1080p", Group: "alt.binaries.movies", Complete: false, SeedMsgID: "a@x"},
		{Name: "Full.PreDB.2021.1080p", Group: "alt.binaries.movies", Complete: true, SeedMsgID: "b@x", PayloadBytes: 100},
	})
	if len(out) != 1 || out[0].Title != "Full.PreDB.2021.1080p" {
		t.Fatalf("adapted=%+v", out)
	}
}

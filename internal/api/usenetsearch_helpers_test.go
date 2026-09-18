package api

import (
	"testing"

	"github.com/labbersanon/sakms/internal/prowlarr"
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

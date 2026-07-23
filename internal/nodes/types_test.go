package nodes

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestNodeSettings_CPUCapPercent_JSONRoundTrip proves the operator-owned CPU cap
// survives a server→node marshal/unmarshal on the SSE settings frame, and — the
// load-bearing part — that an explicit 0 is still emitted on the wire (the field
// has NO omitempty, exactly like MaxJobs). If 0 were dropped, a node could never
// be told to CLEAR a previously-applied cap: an omitted key is indistinguishable
// from "leave it", so the clear signal would silently never arrive.
func TestNodeSettings_CPUCapPercent_JSONRoundTrip(t *testing.T) {
	for _, cap := range []int{0, 50, 100} {
		want := NodeSettings{
			PathMap:       []PathMapping{{Server: "/srv/movies", Local: "/mnt/movies"}},
			MaxJobs:       4,
			CPUCapPercent: cap,
			PauseDispatch: true,
		}

		raw, err := json.Marshal(want)
		if err != nil {
			t.Fatalf("marshal (cap=%d): %v", cap, err)
		}
		// The key must always be present, even for 0 — proves no omitempty.
		if !strings.Contains(string(raw), `"cpuCapPercent":`) {
			t.Fatalf("cap=%d: wire JSON dropped cpuCapPercent (must be present even at 0): %s", cap, raw)
		}

		var got NodeSettings
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatalf("unmarshal (cap=%d): %v", cap, err)
		}
		if got.CPUCapPercent != cap {
			t.Errorf("cap=%d: round-trip CPUCapPercent = %d, want %d", cap, got.CPUCapPercent, cap)
		}
		if got.MaxJobs != 4 || !got.PauseDispatch {
			t.Errorf("cap=%d: sibling fields corrupted: MaxJobs=%d PauseDispatch=%v", cap, got.MaxJobs, got.PauseDispatch)
		}
	}
}

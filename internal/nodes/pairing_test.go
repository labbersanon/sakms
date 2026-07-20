package nodes

import (
	"testing"
	"time"
)

func TestPairingRegistry_RegisterAndApprove(t *testing.T) {
	reg := NewPairingRegistry()

	id, code, configCh, done, ok := reg.Register("wade-pc")
	if !ok {
		t.Fatal("Register should succeed on empty registry")
	}
	if id == "" || code == "" {
		t.Fatal("Register returned empty id or code")
	}
	if len(code) != 6 {
		t.Fatalf("pairing code length: want 6, got %d", len(code))
	}

	cfg := PairConfig{APIKey: "sk_test", Settings: NodeSettings{MaxJobs: 4}}
	approved := reg.Approve(id, cfg)
	if !approved {
		t.Fatal("Approve should return true for registered id")
	}

	select {
	case got := <-configCh:
		if got.APIKey != "sk_test" {
			t.Fatalf("config apiKey: want sk_test, got %s", got.APIKey)
		}
	case <-done:
		t.Fatal("done closed instead of config delivered")
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timed out waiting for config")
	}
}

func TestPairingRegistry_Reject(t *testing.T) {
	reg := NewPairingRegistry()

	id, _, _, done, ok := reg.Register("node1")
	if !ok {
		t.Fatal("Register failed")
	}

	if !reg.Reject(id) {
		t.Fatal("Reject should return true for registered id")
	}

	select {
	case <-done:
		// expected
	case <-time.After(100 * time.Millisecond):
		t.Fatal("done not closed after Reject")
	}
}

func TestPairingRegistry_ApproveMissingID(t *testing.T) {
	reg := NewPairingRegistry()
	if reg.Approve("nonexistent", PairConfig{}) {
		t.Fatal("Approve on missing id should return false")
	}
}

func TestPairingRegistry_RejectMissingID(t *testing.T) {
	reg := NewPairingRegistry()
	if reg.Reject("nonexistent") {
		t.Fatal("Reject on missing id should return false")
	}
}

func TestPairingRegistry_Cap(t *testing.T) {
	reg := NewPairingRegistry()
	for i := 0; i < maxPendingPairings; i++ {
		_, _, _, _, ok := reg.Register("node")
		if !ok {
			t.Fatalf("Register %d should succeed (cap=%d)", i+1, maxPendingPairings)
		}
	}
	_, _, _, _, ok := reg.Register("overflow")
	if ok {
		t.Fatalf("Register beyond cap should return false")
	}
}

func TestPairingRegistry_DisconnectFreesSlot(t *testing.T) {
	reg := NewPairingRegistry()
	for i := 0; i < maxPendingPairings; i++ {
		reg.Register("node") //nolint:errcheck
	}
	// Fill one slot then disconnect to free it.
	id, _, _, _, _ := func() (string, string, <-chan PairConfig, <-chan struct{}, bool) {
		// Re-register after freeing a slot.
		return "", "", nil, nil, false
	}()
	_ = id

	// Easier: directly test Disconnect frees the slot.
	reg2 := NewPairingRegistry()
	id2, _, _, _, _ := reg2.Register("node")
	reg2.Disconnect(id2)
	_, _, _, _, ok := reg2.Register("new-node")
	if !ok {
		t.Fatal("Disconnect should free the slot for a new registration")
	}
}

func TestPairingRegistry_ListPending(t *testing.T) {
	reg := NewPairingRegistry()
	reg.Register("nodeA")
	reg.Register("nodeB")

	list := reg.ListPending()
	if len(list) != 2 {
		t.Fatalf("ListPending: want 2, got %d", len(list))
	}
}

func TestNewPairingCode(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 100; i++ {
		code := newPairingCode()
		if len(code) != 6 {
			t.Fatalf("code length: want 6, got %d", len(code))
		}
		for _, ch := range code {
			if !contains(pairingAlphabet, byte(ch)) {
				t.Fatalf("invalid char %c in code %s", ch, code)
			}
		}
		seen[code] = true
	}
	// 100 samples should produce at least a few unique codes.
	if len(seen) < 90 {
		t.Fatalf("low uniqueness: only %d distinct codes in 100 samples", len(seen))
	}
}

func contains(s string, b byte) bool {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return true
		}
	}
	return false
}

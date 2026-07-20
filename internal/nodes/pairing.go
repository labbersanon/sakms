package nodes

import (
	"crypto/rand"
	"sync"
	"time"
)

const (
	maxPendingPairings = 5
	pendingTTL         = 10 * time.Minute

	// pairingAlphabet is Crockford base32 minus ambiguous characters (0, 1, I, L, O).
	// 256 % 32 == 0 so uniform modulo sampling is exact — no bias.
	pairingAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
)

// pendingEntry is the server-side state for one unconfirmed node.
type pendingEntry struct {
	name        string
	pairingCode string
	requestedAt time.Time
	configCh    chan PairConfig // buffered cap 1; Approve sends here
	done        chan struct{}   // closed on TTL expiry or Reject
	closeOnce   sync.Once
	timer       *time.Timer
}

func (pe *pendingEntry) closeDone() {
	pe.closeOnce.Do(func() { close(pe.done) })
}

// PairingRegistry tracks nodes waiting for operator approval. All state is
// in-memory; nodes re-pair automatically after a server restart.
type PairingRegistry struct {
	mu      sync.Mutex
	pending map[string]*pendingEntry
}

// NewPairingRegistry returns an empty PairingRegistry.
func NewPairingRegistry() *PairingRegistry {
	return &PairingRegistry{pending: make(map[string]*pendingEntry)}
}

// Register adds a new pending node. Returns (id, pairingCode, configCh, done, true)
// on success, or ("", "", nil, nil, false) when the pending-pairing cap is
// already reached.
func (p *PairingRegistry) Register(name string) (id, code string, configCh <-chan PairConfig, done <-chan struct{}, ok bool) {
	p.mu.Lock()
	if len(p.pending) >= maxPendingPairings {
		p.mu.Unlock()
		return "", "", nil, nil, false
	}
	id = rand.Text()
	code = newPairingCode()
	pe := &pendingEntry{
		name:        name,
		pairingCode: code,
		requestedAt: time.Now(),
		configCh:    make(chan PairConfig, 1),
		done:        make(chan struct{}),
	}
	pe.timer = time.AfterFunc(pendingTTL, func() { p.expire(id) })
	p.pending[id] = pe
	p.mu.Unlock()
	return id, code, pe.configCh, pe.done, true
}

// Approve delivers cfg to the waiting pair stream and removes the pending
// entry. Returns false when the id is not found (already expired or rejected).
func (p *PairingRegistry) Approve(id string, cfg PairConfig) bool {
	p.mu.Lock()
	pe, ok := p.pending[id]
	if ok {
		delete(p.pending, id)
		pe.timer.Stop()
	}
	p.mu.Unlock()
	if !ok {
		return false
	}
	select {
	case pe.configCh <- cfg:
		return true
	default:
		// channel already full — the node reconnected and registered again;
		// treat as a lost race and report failure so the caller can revoke.
		return false
	}
}

// Reject removes the pending entry and signals the waiting pair stream to
// close without delivering a config. Returns false when the id is not found.
func (p *PairingRegistry) Reject(id string) bool {
	p.mu.Lock()
	pe, ok := p.pending[id]
	if ok {
		delete(p.pending, id)
		pe.timer.Stop()
	}
	p.mu.Unlock()
	if !ok {
		return false
	}
	pe.closeDone()
	return true
}

// Disconnect is called by the pair stream handler on return (deferred). It
// cleans up the pending entry if still present, preventing slot leaks when a
// node disconnects before the operator acts.
func (p *PairingRegistry) Disconnect(id string) {
	p.mu.Lock()
	pe, ok := p.pending[id]
	if ok {
		delete(p.pending, id)
		pe.timer.Stop()
	}
	p.mu.Unlock()
	if ok {
		pe.closeDone()
	}
}

// Name returns the device name for a pending node. Used by approveNodeHandler
// to retrieve the name before creating the per-node key.
func (p *PairingRegistry) Name(id string) (string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	pe, ok := p.pending[id]
	if !ok {
		return "", false
	}
	return pe.name, true
}

// ListPending returns a snapshot of all pending nodes for display in the UI.
func (p *PairingRegistry) ListPending() []PendingNodeInfo {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]PendingNodeInfo, 0, len(p.pending))
	for id, pe := range p.pending {
		out = append(out, PendingNodeInfo{
			ID:          id,
			Name:        pe.name,
			PairingCode: pe.pairingCode,
			RequestedAt: pe.requestedAt,
		})
	}
	return out
}

// expire is called by the TTL timer. It removes the entry and signals the
// waiting stream to close.
func (p *PairingRegistry) expire(id string) {
	p.mu.Lock()
	pe, ok := p.pending[id]
	if ok {
		delete(p.pending, id)
	}
	p.mu.Unlock()
	if ok {
		pe.closeDone()
	}
}

// newPairingCode returns a 6-character pairing code from pairingAlphabet.
func newPairingCode() string {
	b := make([]byte, 6)
	rand.Read(b) //nolint:errcheck // crypto/rand never returns an error on supported platforms
	code := make([]byte, 6)
	for i := range code {
		code[i] = pairingAlphabet[int(b[i])%len(pairingAlphabet)]
	}
	return string(code)
}

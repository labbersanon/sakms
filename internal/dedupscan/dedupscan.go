// Package dedupscan owns the process-lifetime live-progress hub for Dedup
// scans. A single Hub, constructed once in cmd/sakms/main.go and injected into
// api.NewMux (mirroring downloader.Manager and webhooks.Store), fans out
// per-file scan progress to SSE subscribers and tracks which modes have a scan
// in flight (for the concurrent-scan guard, reconnect priming, and the status
// endpoint).
//
// # Two-mutex, strictly non-nested locking discipline
//
// The Hub holds two independent locks and NEVER acquires one while holding the
// other:
//
//   - subsMu (sync.RWMutex) guards the subscriber fan-out map. Its read lock is
//     held for the WHOLE duration of a Publish/PublishTerminal fan-out so a send
//     can never race a concurrent unsubscribe's close — the exact discipline of
//     internal/webhooks/webhooks.go's broadcaster (which itself mirrors
//     internal/downloader). The write lock guards Subscribe's register and
//     unsubscribe's delete+close.
//   - stateMu (sync.Mutex) guards the in-flight map, the last-event priming
//     map, and the base context — like downloader.Manager.mu guards its entries
//     map.
//
// Publish and PublishTerminal do the subscriber fan-out and the state update as
// two SEPARATE, non-nested critical sections; Subscribe reads the priming state
// under stateMu, releases it, then registers under subsMu. No method holds
// subsMu while acquiring stateMu or vice versa. This non-nesting rule is what
// makes the Hub provably deadlock-free.
//
// # Terminal vs. progress delivery
//
// Progress events use a NON-BLOCKING send (a full/slow subscriber is silently
// skipped — a dropped liveness frame is harmless). Terminal done/error events
// use a BOUNDED-BLOCKING send (Publish blocks up to terminalSendTimeout per
// subscriber) so a briefly-full 32-buffer still reliably receives the one frame
// the frontend's scanning=false transition depends on. A permanently-stuck
// subscriber is skipped after the timeout so the publisher can never wedge.
//
// A nil *Hub is safe for every method (tests can wire nil): Subscribe returns a
// nil channel + no-op unsubscribe, TryStart returns true, Inflight returns
// false, Publish/PublishTerminal/Finish/Start are no-ops, and BaseContext
// returns context.Background().
//
// This package imports only stdlib — no import cycle risk.
package dedupscan

import (
	"context"
	"sync"
	"time"
)

// terminalSendTimeout bounds how long PublishTerminal waits for a single
// subscriber's buffered channel to accept a done/error frame before giving up
// on that subscriber. On the order of a second or two: long enough for a
// briefly-full 32-buffer to drain, short enough that a truly-dead subscriber
// cannot wedge the publisher for long. Declared as a var (not a const) only so
// tests can shorten it; production never mutates it.
var terminalSendTimeout = 2 * time.Second

// subscriberBuffer is the per-subscriber channel capacity (per the plan).
const subscriberBuffer = 32

// Event is one SSE frame delivered to a mode's live subscribers.
type Event struct {
	Type    string `json:"type"` // "progress" | "done" | "error"
	Mode    string `json:"mode"`
	Current int    `json:"current,omitempty"`
	Total   int    `json:"total,omitempty"` // progress: denominator; done: authoritative final count
	Name    string `json:"name,omitempty"`
	Phase   string `json:"phase,omitempty"` // progress: "hashing"|"identifying"|"comparing"|"starting"
	Count   int    `json:"count,omitempty"` // groups found, on "done"
	Error   string `json:"error,omitempty"` // message, on "error"
}

type subscriber struct {
	mode string
	ch   chan Event
}

// Hub is the process-lifetime live-progress hub. See the package doc for the
// two-mutex non-nesting locking discipline.
type Hub struct {
	// subsMu guards the fan-out map; its read lock is held for the DURATION of
	// a publish fan-out so a send can never race a concurrent unsubscribe's
	// close.
	subsMu sync.RWMutex
	subs   map[int]subscriber
	nextID int

	// stateMu guards in-flight + last-event + baseCtx. Kept separate from
	// subsMu so publish never mutates state under the fan-out read lock.
	stateMu  sync.Mutex
	inflight map[string]bool
	last     map[string]Event // last PROGRESS event per in-flight mode (reconnect priming)
	baseCtx  context.Context  // set by Start(ctx); background scans derive from it
}

// New constructs an empty Hub.
func New() *Hub {
	return &Hub{
		subs:     map[int]subscriber{},
		inflight: map[string]bool{},
		last:     map[string]Event{},
	}
}

// Start records the server's signal-driven shutdown context as the base for
// background scans. Called once from main.go after signal.NotifyContext exists.
// Safe on a nil *Hub (no-op).
func (h *Hub) Start(ctx context.Context) {
	if h == nil {
		return
	}
	h.stateMu.Lock()
	defer h.stateMu.Unlock()
	h.baseCtx = ctx
}

// BaseContext returns the stored base context, or context.Background() if Start
// was never called (nil-Hub / test wiring).
func (h *Hub) BaseContext() context.Context {
	if h == nil {
		return context.Background()
	}
	h.stateMu.Lock()
	defer h.stateMu.Unlock()
	if h.baseCtx == nil {
		return context.Background()
	}
	return h.baseCtx
}

// TryStart marks mode in-flight AND seeds a synthetic "starting" progress event
// into the priming state under the SAME stateMu critical section, so a client
// that reconnects after a scan begins but before the first real progress event
// is still primed into the scanning state. Returns false if a scan for that
// mode is already running (the concurrent-same-mode guard). Nil-Hub returns
// true.
func (h *Hub) TryStart(mode string) bool {
	if h == nil {
		return true
	}
	h.stateMu.Lock()
	defer h.stateMu.Unlock()
	if h.inflight[mode] {
		return false
	}
	h.inflight[mode] = true
	h.last[mode] = Event{Type: "progress", Mode: mode, Phase: "starting"}
	return true
}

// Finish clears mode's in-flight flag and its primed last-event. Nil-Hub no-op.
func (h *Hub) Finish(mode string) {
	if h == nil {
		return
	}
	h.stateMu.Lock()
	defer h.stateMu.Unlock()
	delete(h.inflight, mode)
	delete(h.last, mode)
}

// Inflight reports whether a scan for mode is currently running. Nil-Hub false.
func (h *Hub) Inflight(mode string) bool {
	if h == nil {
		return false
	}
	h.stateMu.Lock()
	defer h.stateMu.Unlock()
	return h.inflight[mode]
}

// Publish fans out a PROGRESS event to subscribers of ev.Mode with a
// NON-BLOCKING send (a full/slow subscriber is skipped, never blocks the
// caller), then records it as the priming last-event for ev.Mode. The fan-out
// (subsMu) and the state update (stateMu) are two SEPARATE, non-nested critical
// sections. Terminal done/error events must go through PublishTerminal instead.
// Nil-Hub no-op.
func (h *Hub) Publish(ev Event) {
	if h == nil {
		return
	}

	// Critical section 1: fan-out under subsMu only.
	h.subsMu.RLock()
	for _, sub := range h.subs {
		if sub.mode != ev.Mode {
			continue
		}
		select {
		case sub.ch <- ev:
		default: // full/slow subscriber — skip, never block
		}
	}
	h.subsMu.RUnlock()

	// Critical section 2: record for reconnect priming under stateMu only.
	// Gated on inflight so a stray publish outside a scan cannot leak a
	// last-event that Finish never clears.
	h.stateMu.Lock()
	if h.inflight[ev.Mode] {
		h.last[ev.Mode] = ev
	}
	h.stateMu.Unlock()
}

// PublishTerminal fans out a done/error event to subscribers of ev.Mode with a
// BOUNDED-BLOCKING send per subscriber (up to terminalSendTimeout), holding
// subsMu.RLock for the whole fan-out exactly like Publish — so a briefly-full
// buffer still reliably receives the frame while a truly-dead subscriber is
// skipped after the timeout and can never wedge the publisher. Terminal events
// are deliberately NOT recorded in the priming state (only real progress and
// the synthetic "starting" seed are). Nil-Hub no-op.
func (h *Hub) PublishTerminal(ev Event) {
	if h == nil {
		return
	}

	h.subsMu.RLock()
	defer h.subsMu.RUnlock()
	for _, sub := range h.subs {
		if sub.mode != ev.Mode {
			continue
		}
		timer := time.NewTimer(terminalSendTimeout)
		select {
		case sub.ch <- ev:
			timer.Stop()
		case <-timer.C: // subscriber still full after the bound — skip it
		}
	}
}

// Subscribe registers a live subscriber for one mode, returning its receive
// channel (buffered, cap 32) and an unsubscribe closure. If mode is currently
// in-flight, the primed last event (real progress or the synthetic "starting"
// seed) is enqueued immediately so a late/reconnecting client is primed into
// the scanning state right away. Nil-Hub returns a nil channel and a no-op
// unsubscribe.
//
// The priming read (stateMu) and the registration (subsMu) are two SEPARATE,
// non-nested critical sections.
func (h *Hub) Subscribe(mode string) (<-chan Event, func()) {
	if h == nil {
		return nil, func() {}
	}

	// Critical section 1: read the priming state under stateMu only.
	var primed Event
	prime := false
	h.stateMu.Lock()
	if h.inflight[mode] {
		if ev, ok := h.last[mode]; ok {
			primed, prime = ev, true
		}
	}
	h.stateMu.Unlock()

	// Critical section 2: register under subsMu only.
	ch := make(chan Event, subscriberBuffer)
	h.subsMu.Lock()
	if h.subs == nil {
		h.subs = map[int]subscriber{}
	}
	id := h.nextID
	h.nextID++
	h.subs[id] = subscriber{mode: mode, ch: ch}
	h.subsMu.Unlock()

	// Enqueue the primed event on the fresh, empty cap-32 channel (cannot
	// block) after both locks are released.
	if prime {
		ch <- primed
	}

	unsubscribe := func() {
		h.subsMu.Lock()
		defer h.subsMu.Unlock()
		if sub, ok := h.subs[id]; ok {
			delete(h.subs, id)
			close(sub.ch)
		}
	}
	return ch, unsubscribe
}

// SubscriberCount returns the number of live subscribers — for leak-detection
// tests. Nil-Hub returns 0.
func (h *Hub) SubscriberCount() int {
	if h == nil {
		return 0
	}
	h.subsMu.RLock()
	defer h.subsMu.RUnlock()
	return len(h.subs)
}

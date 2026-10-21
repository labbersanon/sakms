package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/labbersanon/sakms/internal/apidto"
)

// pathmapPushDebounce is the quiet-window a pending path-mapping change waits
// before its HTTP push fires. Reset on every new change for the same key
// (time.AfterFunc + Stop, the same per-key debounce shape internal/api's
// watchfolders.go uses), so a rapid burst of edits to one key coalesces into a
// single push rather than one push per keystroke/click (D5 / Stage 2 scope).
const pathmapPushDebounce = 1500 * time.Millisecond

// pathmapOp is the kind of change queued for a key.
type pathmapOp int

const (
	opSet   pathmapOp = iota // author/replace this key's node path
	opClear                  // remove this key's mapping (D7 clear signal)
)

// pathmapPusher debounces and coalesces node-authored path-mapping changes into
// single-key node-auth pushes to PUT /api/nodes/{id}/settings.
//
// Coalescing is per key: pending holds only the latest op per key, and the
// authored NodePath + MediaRoots self-report are read FRESH from cfg at fire
// time (never a stale snapshot captured at schedule time — D9), so N rapid sets
// to one key send exactly one push carrying the final value. The wire contract
// is strictly one key per push (apidto.NodeSettingsRequest with a single-entry
// PathMap), matching the server's single-key-delta verification.
//
// The fire goroutine NEVER holds cfg.mu across the HTTP round trip: it takes one
// locked cfg.pushInputs snapshot, releases, then does network I/O — preserving
// the single-locked-critical-section discipline (commit 97af02f) and never
// freezing executeJob/executeBrowse/GET-/status behind a slow/stuck push.
type pathmapPusher struct {
	cfg      *NodeConfig
	sess     *nodeSession
	client   *http.Client
	debounce time.Duration

	mu      sync.Mutex
	pending map[string]pathmapOp
	timers  map[string]*time.Timer
	gen     map[string]uint64 // AfterFunc generation; stale callbacks no-op
	lastErr string            // most recent push failure, surfaced (read-only) via GET /pathmap for Stage 3

	// pushHook, when non-nil, is invoked after every push attempt with the key
	// and the resulting error (nil on success). Test observability seam only —
	// production leaves it nil.
	pushHook func(key string, op pathmapOp, err error)
}

// newPathmapPusher builds a pusher. debounce <= 0 falls back to the production
// default; tests pass a short window.
func newPathmapPusher(cfg *NodeConfig, sess *nodeSession, client *http.Client, debounce time.Duration) *pathmapPusher {
	if debounce <= 0 {
		debounce = pathmapPushDebounce
	}
	return &pathmapPusher{
		cfg:      cfg,
		sess:     sess,
		client:   client,
		debounce: debounce,
		pending:  make(map[string]pathmapOp),
		timers:   make(map[string]*time.Timer),
		gen:      make(map[string]uint64),
	}
}

// schedule records a pending change for key (latest op wins) and (re)arms its
// debounce timer. Safe to call from the control-socket handlers.
func (p *pathmapPusher) schedule(key string, op pathmapOp) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pending[key] = op
	// Claude 2026-10-21: generation so a fired AfterFunc cannot push twice.
	// Reason: Timer.Stop is false once the callback is queued; CI then saw
	//   TestPathMap_DebounceCoalescesRapidEdits "second push fired" when an
	//   80ms quiet window elapsed between HTTP sets AND when Stop lost the race.
	// Troubleshooting: a second push for one burst → gen mismatch not checked.
	// Review if: pathmapPushDebounce is driven by a fake clock in tests.
	p.gen[key]++
	g := p.gen[key]
	if t, ok := p.timers[key]; ok {
		t.Stop()
	}
	p.timers[key] = time.AfterFunc(p.debounce, func() { p.fire(key, g) })
}

// lastError returns the most recent push failure message (empty when the last
// push for every key succeeded, or none has run). Read by GET /pathmap so a
// future tray (Stage 3) can surface "last push failed" without this stage
// building the tray itself.
func (p *pathmapPusher) lastError() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.lastErr
}

// fire performs one key's debounced push. It reads the latest op + fresh cfg
// inputs, builds a single-key node-auth request, POSTs it, and records success
// or failure. A failed push NEVER mutates cfg — the last-known-good local
// AuthoredPaths/PathMap persisted at set/clear time stays intact; only lastErr
// is updated for later surfacing.
func (p *pathmapPusher) fire(key string, g uint64) {
	p.mu.Lock()
	if p.gen[key] != g {
		p.mu.Unlock()
		return
	}
	op, ok := p.pending[key]
	delete(p.pending, key)
	delete(p.timers, key)
	p.mu.Unlock()
	if !ok {
		return
	}

	err := p.push(key, op)

	p.mu.Lock()
	if err != nil {
		p.lastErr = fmt.Sprintf("push for %q failed: %v", key, err)
	} else {
		p.lastErr = ""
	}
	hook := p.pushHook
	p.mu.Unlock()

	if err != nil {
		log.Printf("sakms-node: path-mapping push for %q failed (local state preserved, will retry on next edit): %v", key, err)
	}
	if hook != nil {
		hook(key, op, err)
	}
}

// push builds and sends one single-key node-auth settings push. It reads
// nodePath + MediaRoots fresh from cfg (D9) and fails fast, WITHOUT a round
// trip, when the node has no usable mediaRoots — the same presence rule the
// server enforces (422), caught locally so the failure is recorded rather than
// wasting a request. A returned error is surfaced (never swallowed): a server
// 422/5xx or a transport error both propagate to lastErr.
func (p *pathmapPusher) push(key string, op pathmapOp) error {
	nodePath, authored, mediaRoots, serverURL, apiKey := p.cfg.pushInputs(key)

	// D9 fail-fast: a set with no real containment boundary is rejected locally
	// before the push. Applies to a clear too — the server gates every node-auth
	// PathMap write on a non-trivial mediaRoots self-report, so pushing a clear
	// with empty/trivial mediaRoots would 422 just the same.
	if err := mediaRootsUsable(mediaRoots); err != nil {
		return err
	}

	entry := apidto.NodePathMappingInput{Key: apidto.LibraryPathKey(key)}
	switch op {
	case opClear:
		entry.Clear = true
	default:
		if !authored {
			// The key was cleared out from under a still-pending set (latest-wins
			// should have turned this into opClear, but guard anyway): nothing to
			// send, and sending NodePath:"" would be a no-op skip server-side.
			return nil
		}
		entry.NodePath = nodePath
	}

	body := apidto.NodeSettingsRequest{
		PathMap:    []apidto.NodePathMappingInput{entry},
		MediaRoots: mediaRoots,
	}
	buf, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshalling push: %w", err)
	}

	id := p.sess.id()
	if id == "" {
		// Route-pattern formality only; the server keys by the bearer identity
		// and ignores the URL id (D2). Before the first connect we have none.
		id = "self"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, serverURL+"/api/nodes/"+id+"/settings", bytes.NewReader(buf))
	if err != nil {
		return fmt.Errorf("building push request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("sending push: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		return fmt.Errorf("server rejected push: status %d", resp.StatusCode)
	}
	return nil
}

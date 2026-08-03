package api

import (
	"fmt"
	"net/http"
	"time"

	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/sectionlock"
	"github.com/labbersanon/sakms/internal/webhooks"
)

// notificationsStreamHandler streams live notification events (rename.applied,
// purge.applied, dedup.applied, grab.completed) as server-sent events. It
// subscribes to the webhooks Store's in-process broadcaster, which publishes on
// every Dispatch regardless of whether any outbound webhook is configured.
//
// Unlike downloadsStreamHandler there is NO initial-snapshot paint: this is a
// pure event stream with no current state to render on connect. Events fired
// during a client disconnect/reconnect window are lost — there is no replay
// buffer — an accepted limitation for this best-effort, foreground-only
// feature (the browser's EventSource auto-reconnects regardless).
//
// # The section-lock re-check ticker is inert HERE, deliberately (§4.5)
//
// sectionlock.Classify maps /api/notifications/stream to NO section (R-3:
// one global stream, not a per-section one), so StreamRevoked can never
// terminate this particular stream. That is the correct outcome, not an
// oversight: Layer 1 does not gate this route either, so a terminated
// stream would be re-established by the browser's own EventSource
// reconnect within a second — churn, not enforcement. The ticker is wired
// anyway so the behaviour follows automatically if that classification
// ever changes. §4.5's "bounds duration, not content" is therefore
// achieved for the two streams Layer 1 does gate (downloads, dedup) and
// not for this one.
func notificationsStreamHandler(whStore *webhooks.Store, recheckInterval ...time.Duration) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming not supported", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")

		// Flush an SSE comment immediately so the 200 + headers are sent on
		// connect. Unlike downloadsStreamHandler there is no initial snapshot to
		// paint (this is a pure event stream), so without this the response would
		// send no bytes until the first event — the browser's EventSource would
		// stay CONNECTING and an intermediary proxy could reap an idle stream that
		// never sent headers. The comment doubles as an initial keepalive.
		fmt.Fprint(w, ": connected\n\n")
		flusher.Flush()

		ctx := r.Context()

		// A nil whStore yields a nil channel + no-op unsubscribe (see
		// webhooks.Store.Subscribe); a nil channel blocks forever in the select,
		// so the loop simply waits on ctx.Done() — never busy-spins.
		ch, unsubscribe := whStore.Subscribe()
		defer unsubscribe()

		sections := sectionlock.Classify(r.URL.Path)
		recheck := time.NewTicker(streamRecheckInterval(recheckInterval))
		defer recheck.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-recheck.C:
				if sectionlock.StreamRevoked(r, sections) {
					return
				}
			case ev := <-ch:
				if adultEventHidden(r, ev) {
					continue
				}
				writeSSEData(w, flusher, ev)
			}
		}
	}
}

// Claude 2026-08-03: notification events carrying mode "adult" are now dropped
// while adult-content is locked.
// Reason: this route classifies as NO section (R-3 — one global stream, not a
// per-section one), so Layer 1 never gates it and StreamRevoked can never
// terminate it. The events themselves carry {"mode":"adult","title":"<release
// name>"} — dispatched from search.go and proposals.go — and were forwarded
// verbatim to every subscriber. So an operator who locked adult-content still
// got Adult scene titles pushed into the browser live, which is precisely the
// confidentiality the lock exists to provide.
// Troubleshooting: R-3 bounded this stream's DURATION and said nothing about
// its CONTENT; the content half had no control at all.
// Review if: this route ever gains a section classification, which would let
// Layer 1 gate it directly.
//
// FILTER, not refuse — the same rule requests.go and downloads.go state. This
// is one global stream carrying every mode's events, so hanging it up would
// take Movies and Series notifications away too, whenever Adult alone was
// locked.
//
// Read LIVE, not from the frozen context Decision: a stream outlives the
// request that opened it, so a lock applied afterwards is invisible to the
// snapshot. adultHiddenNow (downloads.go) is the shared live read.
func adultEventHidden(r *http.Request, ev webhooks.BroadcastEvent) bool {
	if !eventIsAdult(ev) {
		return false
	}
	return adultHiddenNow(r)
}

// eventIsAdult reports whether ev's payload declares mode "adult".
//
// Dispatch sites build Data as a map literal, so a map assertion covers every
// current producer. An event whose Data is some other shape carries no mode
// this can read and is treated as non-Adult — the same absent-allows rule the
// rest of Layers 2 and 3 use. Keep this in step with the dispatch sites: a
// producer that starts sending a struct instead of a map would silently stop
// being filtered.
func eventIsAdult(ev webhooks.BroadcastEvent) bool {
	data, ok := ev.Data.(map[string]any)
	if !ok {
		return false
	}
	m, _ := data["mode"].(string)
	return m == string(mode.Adult)
}

package dedupscan

import (
	"context"
	"sync"
	"testing"
	"time"
)

// recvWithin blocks up to d for an event on ch, failing the test if none
// arrives.
func recvWithin(t *testing.T, ch <-chan Event, d time.Duration) Event {
	t.Helper()
	select {
	case ev := <-ch:
		return ev
	case <-time.After(d):
		t.Fatal("timed out waiting for event")
		return Event{}
	}
}

// assertNoRecv fails if any event arrives on ch within d.
func assertNoRecv(t *testing.T, ch <-chan Event, d time.Duration) {
	t.Helper()
	select {
	case ev := <-ch:
		t.Fatalf("expected no event, got %+v", ev)
	case <-time.After(d):
	}
}

func TestSubscribePublishDelivery(t *testing.T) {
	h := New()
	ch, unsub := h.Subscribe("movies")
	defer unsub()

	want := Event{Type: "progress", Mode: "movies", Current: 3, Total: 10, Name: "a.mkv", Phase: "hashing"}
	h.Publish(want)

	got := recvWithin(t, ch, time.Second)
	if got != want {
		t.Fatalf("delivered %+v, want %+v", got, want)
	}
}

func TestPerModeFiltering(t *testing.T) {
	h := New()
	moviesCh, unsub1 := h.Subscribe("movies")
	defer unsub1()
	seriesCh, unsub2 := h.Subscribe("series")
	defer unsub2()

	h.Publish(Event{Type: "progress", Mode: "movies", Name: "m.mkv"})

	got := recvWithin(t, moviesCh, time.Second)
	if got.Mode != "movies" {
		t.Fatalf("movies subscriber got mode %q, want movies", got.Mode)
	}
	// A Series subscriber must never receive a Movies event.
	assertNoRecv(t, seriesCh, 100*time.Millisecond)
}

func TestTryStartConcurrentGuard(t *testing.T) {
	h := New()
	if !h.TryStart("movies") {
		t.Fatal("first TryStart(movies) returned false, want true")
	}
	if h.TryStart("movies") {
		t.Fatal("second TryStart(movies) returned true while in-flight, want false")
	}
	// A different mode is independently keyed and may start concurrently.
	if !h.TryStart("series") {
		t.Fatal("TryStart(series) returned false, want true (independent mode)")
	}
	h.Finish("movies")
	if !h.TryStart("movies") {
		t.Fatal("TryStart(movies) after Finish returned false, want true")
	}
}

func TestTryStartSeedsStartingEvent(t *testing.T) {
	h := New()
	h.TryStart("movies")

	// A subscriber connecting after TryStart but before any real progress
	// event must still be primed with the synthetic "starting" seed.
	ch, unsub := h.Subscribe("movies")
	defer unsub()

	got := recvWithin(t, ch, time.Second)
	if got.Phase != "starting" {
		t.Fatalf("primed event phase = %q, want starting (event: %+v)", got.Phase, got)
	}
	if got.Type != "progress" || got.Mode != "movies" {
		t.Fatalf("primed seed = %+v, want a movies progress event", got)
	}
}

func TestReconnectPriming(t *testing.T) {
	h := New()
	h.TryStart("movies")

	last := Event{Type: "progress", Mode: "movies", Current: 7, Total: 20, Name: "seven.mkv", Phase: "hashing"}
	h.Publish(last)

	// Subscribing while the mode is in-flight immediately receives the last
	// real progress event.
	ch, unsub := h.Subscribe("movies")
	defer unsub()

	got := recvWithin(t, ch, time.Second)
	if got != last {
		t.Fatalf("reconnect priming delivered %+v, want %+v", got, last)
	}
}

func TestSubscriberCountLeakDetection(t *testing.T) {
	h := New()
	if n := h.SubscriberCount(); n != 0 {
		t.Fatalf("initial SubscriberCount = %d, want 0", n)
	}
	_, unsub1 := h.Subscribe("movies")
	_, unsub2 := h.Subscribe("series")
	if n := h.SubscriberCount(); n != 2 {
		t.Fatalf("SubscriberCount after 2 subscribes = %d, want 2", n)
	}
	unsub1()
	unsub2()
	if n := h.SubscriberCount(); n != 0 {
		t.Fatalf("SubscriberCount after unsubscribe = %d, want 0 (leak)", n)
	}
	// Double-unsubscribe must be safe (idempotent).
	unsub1()
	if n := h.SubscriberCount(); n != 0 {
		t.Fatalf("SubscriberCount after double-unsub = %d, want 0", n)
	}
}

func TestProgressSendNonBlockingSkip(t *testing.T) {
	h := New()
	ch, unsub := h.Subscribe("movies")
	defer unsub()

	// Fill the 32-buffer and then publish well past capacity WITHOUT draining.
	// A blocking send would hang here; the test asserts Publish returns.
	done := make(chan struct{})
	go func() {
		for i := 0; i < subscriberBuffer+16; i++ {
			h.Publish(Event{Type: "progress", Mode: "movies", Current: i})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Publish blocked on a full subscriber buffer (should be non-blocking)")
	}

	// The buffer holds exactly the first 32; the overflow was silently skipped.
	count := 0
	for {
		select {
		case <-ch:
			count++
			continue
		default:
		}
		break
	}
	if count != subscriberBuffer {
		t.Fatalf("buffered %d progress events, want %d (overflow should be skipped)", count, subscriberBuffer)
	}
}

func TestPublishTerminalDrainsWithinTimeout(t *testing.T) {
	restore := terminalSendTimeout
	terminalSendTimeout = time.Second
	defer func() { terminalSendTimeout = restore }()

	h := New()
	ch, unsub := h.Subscribe("movies")
	defer unsub()

	// Fill the buffer to capacity so the terminal send cannot proceed
	// immediately.
	for i := 0; i < subscriberBuffer; i++ {
		h.Publish(Event{Type: "progress", Mode: "movies", Current: i})
	}

	// A briefly-slow subscriber frees a slot after a short delay; the bounded
	// terminal send must wait and then reliably deliver.
	go func() {
		time.Sleep(50 * time.Millisecond)
		<-ch // free one slot
	}()

	term := Event{Type: "done", Mode: "movies", Count: 5, Total: 42}
	sent := make(chan struct{})
	go func() {
		h.PublishTerminal(term)
		close(sent)
	}()
	select {
	case <-sent:
	case <-time.After(2 * time.Second):
		t.Fatal("PublishTerminal did not return within the bound")
	}

	// Drain the channel and confirm the terminal event was actually delivered.
	deadline := time.After(time.Second)
	for {
		select {
		case ev := <-ch:
			if ev.Type == "done" && ev == term {
				return // delivered
			}
		case <-deadline:
			t.Fatal("terminal event was not delivered to the subscriber")
		}
	}
}

func TestPublishTerminalSkipsStuckSubscriber(t *testing.T) {
	restore := terminalSendTimeout
	terminalSendTimeout = 100 * time.Millisecond
	defer func() { terminalSendTimeout = restore }()

	h := New()
	ch, unsub := h.Subscribe("movies")
	defer unsub()

	// Fill to capacity and NEVER drain — the subscriber is permanently stuck.
	for i := 0; i < subscriberBuffer; i++ {
		h.Publish(Event{Type: "progress", Mode: "movies", Current: i})
	}
	_ = ch // deliberately never drained

	// PublishTerminal must still return in bounded time (skip the dead
	// subscriber after the timeout), never wedging the publisher.
	sent := make(chan struct{})
	go func() {
		h.PublishTerminal(Event{Type: "done", Mode: "movies"})
		close(sent)
	}()
	select {
	case <-sent:
	case <-time.After(2 * time.Second):
		t.Fatal("PublishTerminal wedged on a permanently-stuck subscriber")
	}
}

func TestInflightReflectsTransitions(t *testing.T) {
	h := New()
	if h.Inflight("movies") {
		t.Fatal("Inflight(movies) = true before any start, want false")
	}
	h.TryStart("movies")
	if !h.Inflight("movies") {
		t.Fatal("Inflight(movies) = false after TryStart, want true")
	}
	if h.Inflight("series") {
		t.Fatal("Inflight(series) = true, want false (unrelated mode)")
	}
	h.Finish("movies")
	if h.Inflight("movies") {
		t.Fatal("Inflight(movies) = true after Finish, want false")
	}
}

func TestFinishClearsPriming(t *testing.T) {
	h := New()
	h.TryStart("movies")
	h.Publish(Event{Type: "progress", Mode: "movies", Current: 1, Total: 2})
	h.Finish("movies")

	// After Finish, a new subscriber must NOT be primed with a stale event
	// (the mode is idle).
	ch, unsub := h.Subscribe("movies")
	defer unsub()
	assertNoRecv(t, ch, 100*time.Millisecond)
}

func TestNilHubSafety(t *testing.T) {
	var h *Hub // nil

	if !h.TryStart("movies") {
		t.Fatal("nil Hub TryStart returned false, want true")
	}
	if h.Inflight("movies") {
		t.Fatal("nil Hub Inflight returned true, want false")
	}
	if n := h.SubscriberCount(); n != 0 {
		t.Fatalf("nil Hub SubscriberCount = %d, want 0", n)
	}
	if h.BaseContext() == nil {
		t.Fatal("nil Hub BaseContext returned nil, want context.Background()")
	}
	ch, unsub := h.Subscribe("movies")
	if ch != nil {
		t.Fatal("nil Hub Subscribe returned a non-nil channel, want nil")
	}
	// These must all be safe no-ops (no panic).
	unsub()
	h.Start(nil)
	h.Publish(Event{Type: "progress", Mode: "movies"})
	h.PublishTerminal(Event{Type: "done", Mode: "movies"})
	h.Finish("movies")
}

// TestConcurrentStress hammers every method from many goroutines at once so the
// -race detector has a real concurrent surface to inspect across the two-mutex
// design (fan-out under subsMu, state under stateMu, subscribe/unsubscribe
// racing publishes and terminal sends). It is iteration-bounded (guaranteed to
// converge) and every subscriber drains continuously via `for range ch`, so a
// buffer never wedges a bounded-blocking terminal send. It asserts no
// panic/deadlock/race, not any specific delivery outcome.
func TestConcurrentStress(t *testing.T) {
	restore := terminalSendTimeout
	terminalSendTimeout = 5 * time.Millisecond
	defer func() { terminalSendTimeout = restore }()

	h := New()
	modes := []string{"movies", "series", "adult"}
	const iters = 2000
	var wg sync.WaitGroup

	// Progress publishers — non-blocking sends.
	for _, m := range modes {
		wg.Add(1)
		go func(m string) {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				h.Publish(Event{Type: "progress", Mode: m, Current: i})
			}
		}(m)
	}

	// Terminal publishers — bounded-blocking sends.
	for _, m := range modes {
		wg.Add(1)
		go func(m string) {
			defer wg.Done()
			for i := 0; i < iters/10; i++ {
				h.PublishTerminal(Event{Type: "done", Mode: m})
			}
		}(m)
	}

	// State churners — TryStart/Finish/Inflight.
	for _, m := range modes {
		wg.Add(1)
		go func(m string) {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				h.TryStart(m)
				h.Inflight(m)
				h.Finish(m)
			}
		}(m)
	}

	// Subscribers: subscribe, drain continuously until unsubscribe closes the
	// channel, then unsubscribe — repeated. `for range ch` exits cleanly on
	// close, so no buffer is ever left full to wedge a terminal send.
	for s := 0; s < 8; s++ {
		wg.Add(1)
		go func(s int) {
			defer wg.Done()
			m := modes[s%len(modes)]
			for i := 0; i < iters/20; i++ {
				ch, unsub := h.Subscribe(m)
				drained := make(chan struct{})
				go func() {
					for range ch { // exits when unsub closes ch
					}
					close(drained)
				}()
				h.SubscriberCount()
				unsub()
				<-drained
			}
		}(s)
	}

	wg.Wait()
}

func TestBaseContext(t *testing.T) {
	h := New()
	// Before Start, BaseContext is context.Background().
	if h.BaseContext() == nil {
		t.Fatal("BaseContext returned nil before Start")
	}
	type ctxKey struct{}
	ctx := context.WithValue(context.Background(), ctxKey{}, "v")
	h.Start(ctx)
	if got := h.BaseContext(); got != ctx {
		t.Fatal("BaseContext did not return the context stored by Start")
	}
}

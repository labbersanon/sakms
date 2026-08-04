package webhooks

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/labbersanon/sakms/internal/dbtest"
	"github.com/labbersanon/sakms/internal/secrets"
)

// newTestStore builds a Store backed by a freshly migrated temp-file database
// and a real secret store, the same way the api package's handler tests do.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	sqlDB := dbtest.New(t)
	secretStore, err := secrets.New(make([]byte, 32))
	if err != nil {
		t.Fatalf("building secret store: %v", err)
	}
	return New(sqlDB, secretStore)
}

// TestDispatchBroadcastsWithZeroWebhooks is the Rev-1-defect guard: a subscriber
// must receive the broadcast even when the store has ZERO configured webhooks
// (the default state for most installs). This asserts the broadcast fires
// unconditionally, before listEnabled and its early returns — not after the
// outbound-webhook loop.
func TestDispatchBroadcastsWithZeroWebhooks(t *testing.T) {
	s := newTestStore(t)

	// Sanity: no webhooks configured.
	list, err := s.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("expected zero webhooks, got %d", len(list))
	}

	ch, unsubscribe := s.Subscribe()
	defer unsubscribe()

	s.Dispatch(EventRenameApplied, map[string]any{"title": "The Matrix"})

	select {
	case ev := <-ch:
		if ev.Event != EventRenameApplied {
			t.Fatalf("event = %q, want %q", ev.Event, EventRenameApplied)
		}
		data, ok := ev.Data.(map[string]any)
		if !ok || data["title"] != "The Matrix" {
			t.Fatalf("data = %+v, want title=The Matrix", ev.Data)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for broadcast event with zero webhooks configured")
	}
}

// TestDispatchDoesNotBlockOnFullSubscriber asserts a full/stuck subscriber
// channel never blocks or delays Dispatch's return — the non-blocking
// select/default send drops rather than waits.
func TestDispatchDoesNotBlockOnFullSubscriber(t *testing.T) {
	s := newTestStore(t)

	// Subscribe but never read: after the buffer (8) fills, every further
	// publish must be dropped, not block.
	_, unsubscribe := s.Subscribe()
	defer unsubscribe()

	done := make(chan struct{})
	go func() {
		for i := 0; i < 1000; i++ {
			s.Dispatch(EventGrabCompleted, map[string]any{"i": i})
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Dispatch blocked on a full subscriber channel")
	}
}

// TestOutboundDeliveryUnaffectedBySubscribers is the regression test: existing
// subscription-gated outbound-webhook delivery must fire unchanged whether or
// not any SSE subscriber exists.
func TestOutboundDeliveryUnaffectedBySubscribers(t *testing.T) {
	var hits int32
	got := make(chan struct{}, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		got <- struct{}{}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	s := newTestStore(t)
	if _, err := s.Create(context.Background(), srv.URL, "", []string{EventRenameApplied}, true); err != nil {
		t.Fatalf("Create webhook: %v", err)
	}

	// (1) No SSE subscriber present — outbound delivery still fires.
	s.Dispatch(EventRenameApplied, map[string]any{"title": "A"})
	select {
	case <-got:
	case <-time.After(2 * time.Second):
		t.Fatal("outbound webhook did not fire with no SSE subscriber")
	}

	// (2) An SSE subscriber present — outbound delivery still fires, AND the
	// subscriber also receives the broadcast.
	ch, unsubscribe := s.Subscribe()
	defer unsubscribe()
	s.Dispatch(EventRenameApplied, map[string]any{"title": "B"})

	select {
	case <-got:
	case <-time.After(2 * time.Second):
		t.Fatal("outbound webhook did not fire with an SSE subscriber present")
	}
	select {
	case ev := <-ch:
		if ev.Event != EventRenameApplied {
			t.Fatalf("subscriber event = %q, want %q", ev.Event, EventRenameApplied)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("SSE subscriber did not receive the broadcast")
	}

	if n := atomic.LoadInt32(&hits); n != 2 {
		t.Fatalf("outbound hits = %d, want 2", n)
	}
}

// TestConcurrentSubscribeUnsubscribeDuringDispatch exercises the mutex-guarded
// subscriber collection under concurrent subscribe/unsubscribe against a hot
// Dispatch loop. Run under `go test -race`: it must report zero races and not
// crash (an unguarded map would fatal-panic here).
func TestConcurrentSubscribeUnsubscribeDuringDispatch(t *testing.T) {
	s := newTestStore(t)

	stop := make(chan struct{})
	var wg sync.WaitGroup

	// Hot publish loop.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				s.Dispatch(EventDedupApplied, map[string]any{"x": 1})
			}
		}
	}()

	// Churn: many goroutines subscribing, draining, and unsubscribing.
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				ch, unsubscribe := s.Subscribe()
				select {
				case <-ch:
				default:
				}
				unsubscribe()
			}
		}()
	}

	// Let the churn run, then stop the publisher.
	time.Sleep(200 * time.Millisecond)
	close(stop)
	wg.Wait()
}

// TestSubscribeNilStoreIsSafe asserts a nil *Store returns a nil channel (which
// blocks forever in a select, never busy-spins) and a no-op unsubscribe, and
// that Dispatch on a nil store is a no-op — matching the handler nil-wiring
// convention.
func TestSubscribeNilStoreIsSafe(t *testing.T) {
	var s *Store
	ch, unsubscribe := s.Subscribe()
	if ch != nil {
		t.Fatal("nil store Subscribe should return a nil channel")
	}
	// Must not panic.
	unsubscribe()
	s.Dispatch(EventRenameApplied, nil)
}

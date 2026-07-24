package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/labbersanon/sakms/internal/db"
	"github.com/labbersanon/sakms/internal/secrets"
	"github.com/labbersanon/sakms/internal/webhooks"
)

// TestNotificationsStreamUnsubscribesOnDisconnect asserts the SSE handler's
// deferred unsubscribe fires when the client's request context is cancelled —
// no subscriber (and therefore no goroutine) leak per connection.
func TestNotificationsStreamUnsubscribesOnDisconnect(t *testing.T) {
	sqlDB, err := db.Open(filepath.Join(t.TempDir(), "sakms.db"))
	if err != nil {
		t.Fatalf("opening db: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	secretStore, err := secrets.New(make([]byte, 32))
	if err != nil {
		t.Fatalf("building secret store: %v", err)
	}
	whStore := webhooks.New(sqlDB, secretStore)

	srv := httptest.NewServer(notificationsStreamHandler(whStore))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel() // idempotent with the explicit cancel() below; guarantees
	// the client goroutine's <-ctx.Done() unblocks even on an early t.Fatalf.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatalf("building request: %v", err)
	}

	// Fire the request in the background — the handler blocks streaming, so
	// Do won't return until the connection closes. We observe subscriber count
	// to know when the handler has subscribed and, later, unsubscribed.
	//
	// Body.Close() must NOT happen right after Do() returns: Do() returns as
	// soon as response headers arrive (right after the handler's connect-time
	// flush), which races the handler's very next line (Subscribe). Closing
	// the body immediately can trigger the server's disconnect detection
	// before — or while — the handler subscribes, so the "subscribed" check
	// below can race a disconnect that was never supposed to happen until the
	// explicit cancel() call further down. Hold the connection open until ctx
	// is actually cancelled, matching the real intended trigger.
	headers := make(chan int, 1)
	go func() {
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			headers <- resp.StatusCode
			<-ctx.Done()
			resp.Body.Close()
		}
	}()

	// The connect-time flush must send the 200 + headers immediately, before
	// any event fires — without it the client would block in CONNECTING.
	select {
	case code := <-headers:
		if code != http.StatusOK {
			t.Fatalf("connect status = %d, want 200", code)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("handler never flushed response headers on connect (no data sent)")
	}

	// Wait for the handler to subscribe.
	if !waitFor(t, 2*time.Second, func() bool { return whStore.SubscriberCount() == 1 }) {
		t.Fatalf("handler never subscribed (count=%d)", whStore.SubscriberCount())
	}

	// Cancel the client request; the handler's ctx.Done fires and its deferred
	// unsubscribe must run.
	cancel()

	if !waitFor(t, 2*time.Second, func() bool { return whStore.SubscriberCount() == 0 }) {
		t.Fatalf("handler did not unsubscribe on disconnect (count=%d)", whStore.SubscriberCount())
	}
}

func waitFor(t *testing.T, d time.Duration, cond func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return cond()
}

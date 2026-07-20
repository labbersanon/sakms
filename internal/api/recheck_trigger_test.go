package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/labbersanon/sakms/internal/connections"
	"github.com/labbersanon/sakms/internal/db"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/recheck"
	"github.com/labbersanon/sakms/internal/secrets"
)

// newRecheckTriggerTestStores mirrors internal/recheck's own newTestStores
// convention: a WatchStore and a connections.Store backed by the same
// freshly-migrated SQLite file, real SQL and real encryption, no mocks.
func newRecheckTriggerTestStores(t *testing.T) (*recheck.WatchStore, *connections.Store) {
	t.Helper()
	sqlDB, err := db.Open(filepath.Join(t.TempDir(), "sakms.db"))
	if err != nil {
		t.Fatalf("opening db: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	secretStore, err := secrets.New(make([]byte, 32))
	if err != nil {
		t.Fatalf("building secret store: %v", err)
	}
	return recheck.NewWatchStore(sqlDB), connections.New(sqlDB, secretStore)
}

// TestRecheckTrigger_AsyncAndActuallyRuns drives the real mux end-to-end: a
// slow-to-respond fake Prowlarr proves the handler returns 202 WITHOUT
// waiting for the recheck cycle to finish (the same asynchronous contract as
// triggerEntitySyncHandler), and — once the fake unblocks — that the
// background goroutine genuinely ran and flipped the watched entry's state,
// not just that the HTTP layer accepted the request.
func TestRecheckTrigger_AsyncAndActuallyRuns(t *testing.T) {
	watchStore, connStore := newRecheckTriggerTestStores(t)
	ctx := context.Background()

	release := make(chan struct{})
	prow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release // blocks until the test signals it, proving the HTTP
		// response below can't be waiting on this request to complete.
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[{"guid":"abc","title":"Studio X - Some Scene","protocol":"torrent","seeders":10}]`))
	}))
	defer prow.Close()
	if err := connStore.Upsert(ctx, "prowlarr", prow.URL, "key"); err != nil {
		t.Fatalf("configuring prowlarr: %v", err)
	}

	w, err := watchStore.Add(ctx, recheck.Watch{Mode: mode.Adult, Studio: "Studio X", Title: "Some Scene"})
	if err != nil {
		t.Fatalf("add watch: %v", err)
	}

	srv := httptest.NewServer(NewRecheckTriggerMux(connStore, watchStore))
	defer srv.Close()

	done := make(chan *http.Response, 1)
	go func() {
		resp, err := http.Post(srv.URL+"/api/admin/recheck/trigger", "application/json", nil)
		if err != nil {
			t.Errorf("POST failed: %v", err)
			return
		}
		done <- resp
	}()

	select {
	case resp := <-done:
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusAccepted {
			t.Fatalf("expected 202 Accepted, got %d", resp.StatusCode)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("handler blocked waiting on the recheck cycle instead of returning immediately")
	}

	close(release) // let the background cycle actually complete
	deadline := time.Now().Add(2 * time.Second)
	for {
		all, err := watchStore.List(ctx)
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		for _, got := range all {
			if got.ID == w.ID && got.LastAvailable {
				return // the background TriggerOnce genuinely ran and flipped it
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("watched entry never flipped to available — background recheck did not run")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

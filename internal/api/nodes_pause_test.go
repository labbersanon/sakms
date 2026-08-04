package api

import (
	"context"
	"testing"

	"github.com/labbersanon/sakms/internal/dbtest"
	"github.com/labbersanon/sakms/internal/nodes"
	"github.com/labbersanon/sakms/internal/nodesettings"
)

// newPauseTestStore builds a nodesettings.Store on a fresh temp DB.
func newPauseTestStore(t *testing.T) *nodesettings.Store {
	t.Helper()
	sqlDB := dbtest.New(t)
	return nodesettings.New(sqlDB)
}

// TestSeedNodePause_SeedsPausedFromPersisted simulates a reconnect: with a
// persisted pause=true, seedNodePause must set the live connectedNode.paused so
// Dispatch excludes it. connectedNode.paused is unexported, so this is asserted
// behaviorally via Dispatch returning ok=false — the observable effect of the
// seed, exactly the reconnect-durability guarantee P4c requires.
func TestSeedNodePause_SeedsPausedFromPersisted(t *testing.T) {
	ctx := context.Background()
	store := newPauseTestStore(t)
	reg := nodes.New()
	nodeID := "node-a-id"

	if err := store.SetPauseDispatch(ctx, nodeID, true); err != nil {
		t.Fatalf("SetPauseDispatch: %v", err)
	}

	// The node reconnects: a fresh connectedNode (paused defaults false)...
	_, _, _, disconnect := reg.Connect(nodeID, "node-a", nil)
	defer disconnect()

	// ...then the connect path seeds pause from the persisted store.
	if err := seedNodePause(ctx, reg, store, nodeID); err != nil {
		t.Fatalf("seedNodePause: %v", err)
	}

	if _, _, ok := reg.Dispatch(nodes.Job{ID: "j1", Type: nodes.JobTypePhash}); ok {
		t.Fatal("Dispatch selected a node whose persisted pause should have been seeded")
	}
}

// TestSeedNodePause_UnpausedPersisted confirms a persisted pause=false (or a
// node with a max_jobs row but no pause) leaves the reconnected node eligible.
func TestSeedNodePause_UnpausedPersisted(t *testing.T) {
	ctx := context.Background()
	store := newPauseTestStore(t)
	reg := nodes.New()
	nodeID := "node-a-id"

	if err := store.SetPauseDispatch(ctx, nodeID, false); err != nil {
		t.Fatalf("SetPauseDispatch(false): %v", err)
	}

	jobs, _, _, disconnect := reg.Connect(nodeID, "node-a", nil)
	defer disconnect()

	if err := seedNodePause(ctx, reg, store, nodeID); err != nil {
		t.Fatalf("seedNodePause: %v", err)
	}

	if _, _, ok := reg.Dispatch(nodes.Job{ID: "j1", Type: nodes.JobTypePhash}); !ok {
		t.Fatal("Dispatch excluded a node whose persisted pause is false")
	}
	drainPauseJob(t, jobs)
	reg.ClearPending("j1")
}

// TestSeedNodePause_NothingPersisted confirms a node with no persisted settings
// at all is left eligible (seedNodePause is a no-op on ok=false, and the fresh
// connectedNode's zero-value paused=false is already correct).
func TestSeedNodePause_NothingPersisted(t *testing.T) {
	ctx := context.Background()
	store := newPauseTestStore(t)
	reg := nodes.New()
	nodeID := "node-a-id"

	jobs, _, _, disconnect := reg.Connect(nodeID, "node-a", nil)
	defer disconnect()

	if err := seedNodePause(ctx, reg, store, nodeID); err != nil {
		t.Fatalf("seedNodePause: %v", err)
	}

	if _, _, ok := reg.Dispatch(nodes.Job{ID: "j1", Type: nodes.JobTypePhash}); !ok {
		t.Fatal("Dispatch excluded a node with no persisted settings")
	}
	drainPauseJob(t, jobs)
	reg.ClearPending("j1")
}

// drainPauseJob reads one job off a node channel so a successful Dispatch does
// not leave the buffered send unconsumed.
func drainPauseJob(t *testing.T, jobs <-chan nodes.Job) {
	t.Helper()
	select {
	case <-jobs:
	default:
		t.Fatal("expected a job on the node channel after a successful Dispatch")
	}
}

package downloader

import (
	"context"
	"sync"
	"testing"
	"time"
)

// TestManager_Subscribe_FanoutsOnSeededState verifies that a subscriber
// receives a snapshot as soon as the poll loop detects state that differs from
// its empty initial baseline.
func TestManager_Subscribe_FanoutsOnSeededState(t *testing.T) {
	m := NewForTesting("")
	m.SeedState(Download{
		GID:    "g1",
		Status: "active",
		Files:  []string{"/staging/a.mkv"},
	})

	ch, cancel := m.Subscribe()
	defer cancel()

	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	go m.pollLoop(ctx)

	select {
	case snap := <-ch:
		if len(snap) != 1 || snap[0].GID != "g1" {
			t.Fatalf("snapshot = %v, want one download with GID g1", snap)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("subscriber never received a snapshot")
	}
}

// TestManager_Unsubscribe_ClosesChannel verifies that calling the cancel func
// returned by Subscribe closes the channel.
func TestManager_Unsubscribe_ClosesChannel(t *testing.T) {
	m := NewForTesting("")
	ch, cancel := m.Subscribe()
	cancel()
	select {
	case _, ok := <-ch:
		if ok {
			t.Error("channel delivered a value after unsubscribe")
		}
	case <-time.After(time.Second):
		t.Error("channel not closed after unsubscribe")
	}
}

// TestManager_SetOnComplete_CallbackFires verifies that SetOnComplete wires
// the callback and that it is invoked with the expected arguments.
func TestManager_SetOnComplete_CallbackFires(t *testing.T) {
	m := NewForTesting("")

	var mu sync.Mutex
	var completedGIDs []string
	var completedFiles [][]string
	m.SetOnComplete(func(gid string, files []string) {
		mu.Lock()
		completedGIDs = append(completedGIDs, gid)
		completedFiles = append(completedFiles, files)
		mu.Unlock()
	})

	// Call directly — package-internal access; this is the same call path
	// watchTorrent uses when a real download finishes.
	m.onComplete("test-gid", []string{"/staging/movie.mkv"})

	mu.Lock()
	defer mu.Unlock()
	if len(completedGIDs) != 1 || completedGIDs[0] != "test-gid" {
		t.Fatalf("completedGIDs = %v, want [test-gid]", completedGIDs)
	}
	if len(completedFiles[0]) != 1 || completedFiles[0][0] != "/staging/movie.mkv" {
		t.Fatalf("completedFiles[0] = %v, want [/staging/movie.mkv]", completedFiles[0])
	}
}

// TestManager_SeedState_VisibleViaListAndFind verifies that seeded entries are
// immediately readable via List and FindByGID.
func TestManager_SeedState_VisibleViaListAndFind(t *testing.T) {
	m := NewForTesting("")
	m.SeedState(Download{
		GID:             "abc",
		Status:          "active",
		TotalLength:     1000,
		CompletedLength: 400,
		Files:           []string{"/staging/show.mkv"},
	})

	list := m.List()
	if len(list) != 1 || list[0].GID != "abc" {
		t.Fatalf("List: got %v", list)
	}

	d, err := m.FindByGID("abc")
	if err != nil {
		t.Fatalf("FindByGID: %v", err)
	}
	if d == nil {
		t.Fatal("FindByGID: got nil, want entry")
	}
	if d.Status != "active" || d.TotalLength != 1000 || d.CompletedLength != 400 {
		t.Fatalf("FindByGID: got %+v", d)
	}
}

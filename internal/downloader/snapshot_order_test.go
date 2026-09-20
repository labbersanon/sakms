package downloader

import (
	"slices"
	"testing"
	"time"
)

func TestReadSnapshot_StableOldestFirst(t *testing.T) {
	m := &Manager{entries: map[string]*entry{}}
	newer := time.Now()
	older := newer.Add(-time.Minute)
	m.entries["gid-b"] = &entry{filename: "B", status: "active", addedAt: newer}
	m.entries["gid-a"] = &entry{filename: "A", status: "active", addedAt: older}
	m.entries["gid-c"] = &entry{filename: "C", status: "waiting", addedAt: older}

	want := []string{"gid-a", "gid-c", "gid-b"}
	for i := 0; i < 20; i++ {
		snap := m.readSnapshot()
		got := make([]string, len(snap))
		for j, d := range snap {
			got[j] = d.GID
		}
		if !slices.Equal(got, want) {
			t.Fatalf("iter %d order=%v want %v", i, got, want)
		}
	}
}

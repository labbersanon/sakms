package usenet

import (
	"slices"
	"testing"
	"time"
)

func TestSnapshot_StableOldestFirst(t *testing.T) {
	m := &Manager{downloads: map[string]*dlState{}}
	newer := time.Now()
	older := newer.Add(-time.Minute)
	m.downloads["nzb-b"] = &dlState{gid: "nzb-b", name: "B", status: "active", addedAt: newer}
	m.downloads["nzb-a"] = &dlState{gid: "nzb-a", name: "A", status: "active", addedAt: older}
	m.downloads["nzb-c"] = &dlState{gid: "nzb-c", name: "C", status: "active", addedAt: older}

	want := []string{"nzb-a", "nzb-c", "nzb-b"}
	for i := 0; i < 20; i++ {
		snap := m.snapshot()
		got := make([]string, len(snap))
		for j, d := range snap {
			got[j] = d.GID
		}
		if !slices.Equal(got, want) {
			t.Fatalf("iter %d order=%v want %v", i, got, want)
		}
	}
}

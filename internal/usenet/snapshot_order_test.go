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

func TestSnapshot_IncludesPhase(t *testing.T) {
	m := &Manager{downloads: map[string]*dlState{}}
	started := time.Date(2026, 9, 21, 16, 0, 0, 0, time.UTC)
	m.downloads["nzb-a"] = &dlState{
		gid: "nzb-a", status: "active", phase: phaseRepairing,
		phaseDone: 2, phaseTotal: 5, phaseStartedAt: started,
	}
	snap := m.snapshot()
	if len(snap) != 1 {
		t.Fatalf("len=%d want 1", len(snap))
	}
	if snap[0].Phase != phaseRepairing {
		t.Fatalf("Phase=%q want %q", snap[0].Phase, phaseRepairing)
	}
	if snap[0].PhaseDone != 2 || snap[0].PhaseTotal != 5 {
		t.Fatalf("progress=%d/%d want 2/5", snap[0].PhaseDone, snap[0].PhaseTotal)
	}
	if !snap[0].PhaseStartedAt.Equal(started) {
		t.Fatalf("PhaseStartedAt=%v want %v", snap[0].PhaseStartedAt, started)
	}
}

func TestSameDownloads_DetectsPhaseChange(t *testing.T) {
	a := []Download{{GID: "n", Status: "active", Phase: phaseDownloading}}
	b := []Download{{GID: "n", Status: "active", Phase: phaseRepairing}}
	if sameDownloads(a, b) {
		t.Fatal("phase change must not compare equal (SSE would skip repairing/unpacking)")
	}
}

func TestSameDownloads_DetectsPhaseProgressChange(t *testing.T) {
	a := []Download{{GID: "n", Status: "active", Phase: phaseRepairing, PhaseDone: 1, PhaseTotal: 4}}
	b := []Download{{GID: "n", Status: "active", Phase: phaseRepairing, PhaseDone: 2, PhaseTotal: 4}}
	if sameDownloads(a, b) {
		t.Fatal("phaseDone change must not compare equal (SSE would skip percent ticks)")
	}
}

func TestSetPhaseProgress_ClampsAndSnapshots(t *testing.T) {
	m := &Manager{downloads: map[string]*dlState{
		"nzb-a": {gid: "nzb-a", status: "active", phase: phaseUnpacking},
	}}
	m.setPhaseProgress("nzb-a", 9, 4)
	snap := m.snapshot()
	if len(snap) != 1 {
		t.Fatalf("len=%d want 1", len(snap))
	}
	if snap[0].PhaseDone != 4 || snap[0].PhaseTotal != 4 {
		t.Fatalf("clamped progress=%d/%d want 4/4", snap[0].PhaseDone, snap[0].PhaseTotal)
	}
}

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

func TestSnapshot_IncludesPhase(t *testing.T) {
	m := &Manager{downloads: map[string]*dlState{}}
	started := time.Date(2026, 9, 21, 16, 0, 0, 0, time.UTC)
	m.downloads["nzb-a"] = &dlState{
		gid: "nzb-a", status: "active", phase: phaseRepairing,
		phaseDone: 2, phaseTotal: 5, phaseStartedAt: started,
	}
	snap := m.readSnapshot()
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
	snap := m.readSnapshot()
	if len(snap) != 1 {
		t.Fatalf("len=%d want 1", len(snap))
	}
	if snap[0].PhaseDone != 4 || snap[0].PhaseTotal != 4 {
		t.Fatalf("clamped progress=%d/%d want 4/4", snap[0].PhaseDone, snap[0].PhaseTotal)
	}
}

func TestList_DoesNotCorruptDownloadSpeed(t *testing.T) {
	m := &Manager{downloads: map[string]*dlState{}}
	dl := &dlState{
		gid:       "nzb-a",
		status:    "active",
		phase:     phaseDownloading,
		total:     10_000,
		completed: 1_000,
		prevBytes: 0,
		prevTime:  time.Now().Add(-500 * time.Millisecond),
	}
	m.downloads["nzb-a"] = dl

	poll := m.pollSnapshot()
	if len(poll) != 1 {
		t.Fatalf("poll len=%d want 1", len(poll))
	}
	if poll[0].DownloadSpeed <= 0 {
		t.Fatalf("poll DownloadSpeed=%d want > 0 after 1000 bytes in ~500ms", poll[0].DownloadSpeed)
	}
	wantSpeed := poll[0].DownloadSpeed

	// Simulate downloadsStreamHandler's mergedDownloads → List() after fanout.
	dl.completed = 1_500
	listed := m.List()
	if len(listed) != 1 {
		t.Fatalf("list len=%d want 1", len(listed))
	}
	if listed[0].DownloadSpeed != wantSpeed {
		t.Fatalf("List DownloadSpeed=%d want cached %d — List must not recompute/zero speed",
			listed[0].DownloadSpeed, wantSpeed)
	}
	if listed[0].CompletedLength != 1_500 {
		t.Fatalf("List CompletedLength=%d want 1500", listed[0].CompletedLength)
	}

	// A second List still must not roll the delta base forward.
	_ = m.List()
	time.Sleep(50 * time.Millisecond)
	dl.completed = 2_500
	again := m.pollSnapshot()
	if again[0].DownloadSpeed <= 0 {
		t.Fatalf("second poll DownloadSpeed=%d want > 0 — List must not have advanced prevBytes",
			again[0].DownloadSpeed)
	}
}

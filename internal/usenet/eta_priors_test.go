package usenet

import (
	"testing"
	"time"
)

func TestObserve_UpdatesEMAFromDefaults(t *testing.T) {
	m := New(Config{StagingDir: t.TempDir()})
	repair, unpack, rn, un := m.Priors()
	if repair != DefaultHardwareRepairBps || unpack != DefaultHardwareUnpackBps {
		t.Fatalf("seeded priors = %d/%d, want %d/%d", repair, unpack, DefaultHardwareRepairBps, DefaultHardwareUnpackBps)
	}
	if rn != 0 || un != 0 {
		t.Fatalf("sample counts = %d/%d, want 0/0", rn, un)
	}

	// 50 MiB in 1s against a 25 MiB/s seed → 0.2*50 + 0.8*25 = 30 MiB/s.
	m.Observe(phaseRepairing, 50<<20, time.Second)
	repair, _, rn, _ = m.Priors()
	want := int64(30 << 20)
	if repair != want {
		t.Fatalf("repair EMA = %d, want %d", repair, want)
	}
	if rn != 1 {
		t.Fatalf("repairN = %d, want 1", rn)
	}

	// 80 MiB/s unpack vs 40 MiB/s seed → 0.2*80 + 0.8*40 = 48 MiB/s.
	m.Observe(phaseUnpacking, 80<<20, time.Second)
	_, unpack, _, un = m.Priors()
	wantUnpack := int64(48 << 20)
	if unpack != wantUnpack {
		t.Fatalf("unpack EMA = %d, want %d", unpack, wantUnpack)
	}
	if un != 1 {
		t.Fatalf("unpackN = %d, want 1", un)
	}
}

func TestObserve_IgnoresDownloadingAndZero(t *testing.T) {
	m := New(Config{StagingDir: t.TempDir()})
	m.Observe(phaseDownloading, 50<<20, time.Second)
	m.Observe(phaseRepairing, 0, time.Second)
	m.Observe(phaseRepairing, 50<<20, 0)
	repair, unpack, rn, un := m.Priors()
	if repair != DefaultHardwareRepairBps || unpack != DefaultHardwareUnpackBps {
		t.Fatalf("priors mutated: %d/%d", repair, unpack)
	}
	if rn != 0 || un != 0 {
		t.Fatalf("counts mutated: %d/%d", rn, un)
	}
}

func TestObserve_IgnoresDurationBelow250ms(t *testing.T) {
	m := New(Config{StagingDir: t.TempDir()})
	// 36ms + 400 MiB is the skip-unpack poison case (GB/s effective).
	m.Observe(phaseRepairing, 400<<20, 36*time.Millisecond)
	m.Observe(phaseUnpacking, 400<<20, 249*time.Millisecond)
	repair, unpack, rn, un := m.Priors()
	if repair != DefaultHardwareRepairBps || unpack != DefaultHardwareUnpackBps {
		t.Fatalf("priors mutated by short samples: %d/%d", repair, unpack)
	}
	if rn != 0 || un != 0 {
		t.Fatalf("counts mutated: %d/%d", rn, un)
	}
}

func TestPriors_NilManagerReturnsDefaults(t *testing.T) {
	var m *Manager
	repair, unpack, rn, un := m.Priors()
	if repair != DefaultHardwareRepairBps || unpack != DefaultHardwareUnpackBps || rn != 0 || un != 0 {
		t.Fatalf("nil Priors = %d/%d n=%d/%d", repair, unpack, rn, un)
	}
	cal, at := m.Calibration()
	if cal || !at.IsZero() {
		t.Fatalf("nil Calibration = %t %v", cal, at)
	}
}

func TestSetPriors_ReplaceNotEMA(t *testing.T) {
	m := New(Config{StagingDir: t.TempDir()})
	m.Observe(phaseRepairing, 50<<20, time.Second)
	wantRepair := int64(94 << 20)
	wantUnpack := int64(40 << 20)
	m.SetPriors(wantRepair, wantUnpack)
	repair, unpack, rn, un := m.Priors()
	if repair != wantRepair || unpack != wantUnpack {
		t.Fatalf("SetPriors = %d/%d, want exact REPLACE %d/%d (not EMA toward defaults)", repair, unpack, wantRepair, wantUnpack)
	}
	if rn != 0 || un != 0 {
		t.Fatalf("sample counts after REPLACE = %d/%d, want 0/0", rn, un)
	}
	cal, at := m.Calibration()
	if !cal {
		t.Fatal("expected calibrated=true after SetPriors")
	}
	if at.IsZero() {
		t.Fatal("expected calibratedAt stamped")
	}
}

func TestSetPriors_ZeroSideLeavesPrevious(t *testing.T) {
	m := New(Config{StagingDir: t.TempDir()})
	m.SetPriors(94<<20, 0)
	repair, unpack, rn, un := m.Priors()
	if repair != 94<<20 {
		t.Fatalf("repair = %d, want REPLACE", repair)
	}
	if unpack != DefaultHardwareUnpackBps {
		t.Fatalf("unpack = %d, want default left in place", unpack)
	}
	if rn != 0 || un != 0 {
		t.Fatalf("counts = %d/%d", rn, un)
	}
}

func TestLoadPersistedPriors_CorruptBothSidesNoOp(t *testing.T) {
	m := New(Config{StagingDir: t.TempDir()})
	m.LoadPersistedPriors(0, -1, time.Now())
	repair, unpack, _, _ := m.Priors()
	if repair != DefaultHardwareRepairBps || unpack != DefaultHardwareUnpackBps {
		t.Fatalf("corrupt load mutated priors: %d/%d", repair, unpack)
	}
	cal, _ := m.Calibration()
	if cal {
		t.Fatal("corrupt load must leave calibrated=false")
	}
}

func TestLoadPersistedPriors_StampsGivenTime(t *testing.T) {
	m := New(Config{StagingDir: t.TempDir()})
	at := time.Date(2026, 9, 21, 18, 0, 0, 0, time.UTC)
	m.LoadPersistedPriors(30<<20, 50<<20, at)
	repair, unpack, rn, un := m.Priors()
	if repair != 30<<20 || unpack != 50<<20 || rn != 0 || un != 0 {
		t.Fatalf("loaded = %d/%d n=%d/%d", repair, unpack, rn, un)
	}
	cal, gotAt := m.Calibration()
	if !cal || !gotAt.Equal(at) {
		t.Fatalf("calibration = %t %v, want true %v", cal, gotAt, at)
	}
}

func TestLogPhaseTiming_UnsampledDoesNotObserve(t *testing.T) {
	m := New(Config{StagingDir: t.TempDir()})
	started := time.Now().Add(-time.Second)
	m.logPhaseTiming("g1", phaseRepairing, 400<<20, started, false)
	m.logPhaseTiming("g1", phaseUnpacking, 400<<20, started, false)
	_, _, rn, un := m.Priors()
	if rn != 0 || un != 0 {
		t.Fatalf("unsampled logPhaseTiming incremented N: %d/%d", rn, un)
	}
}

func TestLogPhaseTiming_SampledObserves(t *testing.T) {
	m := New(Config{StagingDir: t.TempDir()})
	started := time.Now().Add(-time.Second)
	m.logPhaseTiming("g1", phaseRepairing, 50<<20, started, true)
	repair, _, rn, _ := m.Priors()
	if rn != 1 {
		t.Fatalf("repairN = %d, want 1", rn)
	}
	if repair == DefaultHardwareRepairBps {
		t.Fatal("sampled logPhaseTiming must move repair prior off the compiled default")
	}
}

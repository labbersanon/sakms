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

func TestPriors_NilManagerReturnsDefaults(t *testing.T) {
	var m *Manager
	repair, unpack, rn, un := m.Priors()
	if repair != DefaultHardwareRepairBps || unpack != DefaultHardwareUnpackBps || rn != 0 || un != 0 {
		t.Fatalf("nil Priors = %d/%d n=%d/%d", repair, unpack, rn, un)
	}
}

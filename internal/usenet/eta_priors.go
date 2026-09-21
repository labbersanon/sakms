package usenet

import (
	"log"
	"time"
)

// Claude 2026-09-21: in-memory EMA hardware priors for Usenet PAR2/unrar ETA.
// Reason: client 25/40 MiB/s constants were a first guess; go-forward samples
//   from this host should pull the next job's prior without claiming a
//   historical accuracy number (no paired past projections exist).
// Troubleshooting: Downloads repair/unpack countdown systematically high/low.
// Review if: priors are persisted or an operator setting replaces the EMA.

const (
	// DefaultHardwareRepairBps matches frontend HARDWARE_REPAIR_BPS (25 MiB/s).
	DefaultHardwareRepairBps int64 = 25 << 20
	// DefaultHardwareUnpackBps matches frontend HARDWARE_UNPACK_BPS (40 MiB/s).
	DefaultHardwareUnpackBps int64 = 40 << 20
	hardwarePriorEMAAlpha          = 0.2
)

// Observe folds a completed repairing/unpacking phase rate into the in-memory
// EMA prior. Downloading is ignored. bytes<=0 or dur<=0 is a no-op.
func (m *Manager) Observe(phase string, bytes int64, dur time.Duration) {
	if m == nil || bytes <= 0 || dur <= 0 {
		return
	}
	observed := bytes * int64(time.Second) / int64(dur)
	if observed <= 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	switch phase {
	case phaseRepairing:
		cur := m.repairBps
		if cur <= 0 {
			cur = DefaultHardwareRepairBps
		}
		m.repairBps = emaInt64(cur, observed, hardwarePriorEMAAlpha)
		m.repairN++
	case phaseUnpacking:
		cur := m.unpackBps
		if cur <= 0 {
			cur = DefaultHardwareUnpackBps
		}
		m.unpackBps = emaInt64(cur, observed, hardwarePriorEMAAlpha)
		m.unpackN++
	}
}

// Priors returns the current repair/unpack byte-rate priors and how many
// completed samples have updated each EMA. Seeded at the frontend constants
// until the first Observe.
func (m *Manager) Priors() (repairBps, unpackBps int64, repairN, unpackN int) {
	if m == nil {
		return DefaultHardwareRepairBps, DefaultHardwareUnpackBps, 0, 0
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	repairBps, unpackBps = m.repairBps, m.unpackBps
	if repairBps <= 0 {
		repairBps = DefaultHardwareRepairBps
	}
	if unpackBps <= 0 {
		unpackBps = DefaultHardwareUnpackBps
	}
	return repairBps, unpackBps, m.repairN, m.unpackN
}

func emaInt64(prior, observed int64, alpha float64) int64 {
	return int64(alpha*float64(observed) + (1-alpha)*float64(prior))
}

// logPhaseTiming writes a structured O2 line when a phase ends (success or
// fail-after-running) and updates the EMA for repairing/unpacking.
func (m *Manager) logPhaseTiming(gid, phase string, bytes int64, started time.Time) {
	if started.IsZero() {
		return
	}
	dur := time.Since(started)
	if dur < 0 {
		dur = 0
	}
	durMs := dur.Milliseconds()
	var effective int64
	if durMs > 0 && bytes > 0 {
		effective = bytes * 1000 / durMs
	}
	repairBps, unpackBps, _, _ := m.Priors()
	var prior int64
	switch phase {
	case phaseRepairing:
		prior = repairBps
	case phaseUnpacking:
		prior = unpackBps
	}
	log.Printf("usenet: phase timing gid=%s phase=%s bytes=%d dur_ms=%d effective_bps=%d prior_bps=%d",
		gid, phase, bytes, durMs, effective, prior)
	m.Observe(phase, bytes, dur)
}

func dlPayloadBytes(dl *dlState) int64 {
	if dl == nil {
		return 0
	}
	if dl.completed > 0 {
		return dl.completed
	}
	return dl.total
}

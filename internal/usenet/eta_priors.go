package usenet

import (
	"log"
	"os"
	"time"
)

// Claude 2026-09-21: EMA hardware priors for Usenet PAR2/unrar ETA.
// Reason: calibration REPLACE-writes a persisted base (settings KV); live
//   Observe EMA still refines in-process only and is discarded on restart.
//   Never persist live EMA — skip-unpack already proved garbage samples.
// Troubleshooting: Downloads repair/unpack countdown systematically high/low.
// Review if: hardware priors become a typed settings struct or the engine
//   emits its own ETA.

const (
	// DefaultHardwareRepairBps matches frontend HARDWARE_REPAIR_BPS (25 MiB/s).
	DefaultHardwareRepairBps int64 = 25 << 20
	// DefaultHardwareUnpackBps matches frontend HARDWARE_UNPACK_BPS (40 MiB/s).
	DefaultHardwareUnpackBps int64 = 40 << 20
	hardwarePriorEMAAlpha          = 0.2
	// hardwarePriorMinSample drops no-op / NVMe-cached blips (36ms skip-unpack
	// of a multi-hundred-MB payload produced GB/s rates that poisoned EMA).
	hardwarePriorMinSample = 250 * time.Millisecond
)

// Observe folds a completed repairing/unpacking phase rate into the in-memory
// EMA prior. Downloading is ignored. bytes<=0, dur<=0, or dur below
// hardwarePriorMinSample is a no-op.
func (m *Manager) Observe(phase string, bytes int64, dur time.Duration) {
	if m == nil || bytes <= 0 || dur <= 0 {
		return
	}
	// Claude 2026-09-21: skip sub-250ms samples so live EMA cannot undo REPLACE.
	// Reason: skip-PAR2 / skip-unpack logged ~36ms at absurd bps and drifted
	//   the next job's prior; calibration REPLACE is the persisted base.
	// Troubleshooting: unpackN climbing after flat-video (no archive) jobs.
	// Review if: hardware priors become a typed settings struct or the engine
	//   emits its own ETA.
	if dur < hardwarePriorMinSample {
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
// until calibration REPLACE or the first gated Observe.
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

// Calibration reports whether a synthetic hardware bench has REPLACE-written
// the in-memory base (and, after boot load, the persisted settings keys).
func (m *Manager) Calibration() (calibrated bool, at time.Time) {
	if m == nil {
		return false, time.Time{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.hwCalibrated, m.hwCalibratedAt
}

// CalibrationInProgress is true while CalibrateHardware holds the in-flight lock.
func (m *Manager) CalibrationInProgress() bool {
	if m == nil {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.hwBenchRunning
}

// SetPriors REPLACE-assigns successful sides (bps>0) and zeros that side's
// sample count. A zero argument leaves that side at its previous/default rate.
// Marks the manager calibrated at time.Now UTC.
func (m *Manager) SetPriors(repairBps, unpackBps int64) {
	if m == nil {
		return
	}
	m.LoadPersistedPriors(repairBps, unpackBps, time.Now().UTC())
}

// LoadPersistedPriors REPLACE-assigns bps>0 sides, zeros those sample counts,
// and stamps calibrated metadata with at (boot path). Both bps<=0 is a no-op
// so corrupt settings keep compiled defaults and calibrated=false.
func (m *Manager) LoadPersistedPriors(repairBps, unpackBps int64, at time.Time) {
	if m == nil {
		return
	}
	if repairBps <= 0 && unpackBps <= 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if repairBps > 0 {
		m.repairBps = repairBps
		m.repairN = 0
	}
	if unpackBps > 0 {
		m.unpackBps = unpackBps
		m.unpackN = 0
	}
	m.hwCalibrated = true
	m.hwCalibratedAt = at
}

func emaInt64(prior, observed int64, alpha float64) int64 {
	return int64(alpha*float64(observed) + (1-alpha)*float64(prior))
}

// logPhaseTiming writes a structured O2 line when a phase ends (success or
// fail-after-running). Observe runs only when sampled is true; skip-PAR2 and
// skip-unpack callers pass false so a 36ms no-op cannot EMA the next job.
func (m *Manager) logPhaseTiming(gid, phase string, bytes int64, started time.Time, sampled bool) {
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
	log.Printf("usenet: phase timing gid=%s phase=%s bytes=%d dur_ms=%d effective_bps=%d prior_bps=%d sampled=%t",
		gid, phase, bytes, durMs, effective, prior, sampled)
	// Claude 2026-09-21: Observe is gated; the unconditional call poisoned EMA.
	// m.Observe(phase, bytes, dur)
	// Reason: skip-PAR2 / skip-unpack / sub-250ms samples must not REPLACE the
	//   calibrated base. Callers pass sampled=false when no work happened.
	// Troubleshooting: unpackBps jumping to GB/s after a flat-video job.
	// Review if: hardware priors become a typed settings struct or the engine
	//   emits its own ETA.
	if sampled {
		m.Observe(phase, bytes, dur)
	}
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

// repairPhaseSampled is true when verifyAndRepair will do real PAR2 work
// (at least one file is a PAR2 payload). Obfuscated .par2 names whose magic
// is video/archive are not sampled — verifyAndRepair returns immediately.
func repairPhaseSampled(files []string) bool {
	for _, p := range files {
		if isPar2Payload(p) {
			return true
		}
	}
	return false
}

// unpackPhaseSampled is true when unpackArchives will attempt at least one
// archive leader. ReadDir failure is treated as no sample (the 250ms Observe
// floor still covers a fast error path if a caller passes sampled=true).
func unpackPhaseSampled(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		names = append(names, e.Name())
	}
	return len(archiveLeaders(names)) > 0
}

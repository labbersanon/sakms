package usenet

import (
	"archive/zip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// Claude 2026-09-21: synthetic hardware bench for Usenet repair/unpack priors.
// Reason: compiled 25/40 MiB/s were a first guess; this measures the configured
//   staging disk + CPU. Calibration REPLACE-writes the persisted base; live
//   Observe EMA still refines in-process only and is never written to settings.
// Troubleshooting: first-import ETAs stuck on 25 MiB/s while live repair is ~94.
// Review if: hardware priors become a typed settings struct or the engine
//   emits its own ETA.

const (
	hwBenchPayloadBytes  = 32 << 20
	hwBenchTimeout       = 60 * time.Second
	hwBenchRepairMinWall = 500 * time.Millisecond
)

// ErrCalibrationInProgress is returned when CalibrateHardware is already running.
var ErrCalibrationInProgress = errors.New("hardware calibration already running")

// SetCalibrationHold, when given a non-nil channel, makes the next
// CalibrateHardware wait on it after taking the in-flight lock. Tests use this
// to overlap a second call and expect ErrCalibrationInProgress / HTTP 409.
func (m *Manager) SetCalibrationHold(ch <-chan struct{}) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.hwBenchHold = ch
	m.mu.Unlock()
}

// CalibrateHardware runs a 32 MiB incompressible zip extract (if 7z is on PATH)
// plus a ReadFile+SHA-256 repair proxy on the configured staging dir. Successful
// sides REPLACE in-memory priors (sample counts reset). Does not take the
// download semaphore. Timeout 60s.
func (m *Manager) CalibrateHardware(ctx context.Context) (repairBps, unpackBps int64, err error) {
	if m == nil {
		return 0, 0, errors.New("usenet manager is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, hwBenchTimeout)
	defer cancel()

	m.mu.Lock()
	if m.hwBenchRunning {
		m.mu.Unlock()
		return 0, 0, ErrCalibrationInProgress
	}
	m.hwBenchRunning = true
	staging := m.stagingDir
	hold := m.hwBenchHold
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		m.hwBenchRunning = false
		m.mu.Unlock()
	}()

	if hold != nil {
		select {
		case <-hold:
		case <-ctx.Done():
			return 0, 0, ctx.Err()
		}
	}

	if staging == "" {
		return 0, 0, errors.New("usenet: hwbench: staging dir is empty")
	}
	if err := os.MkdirAll(staging, 0o755); err != nil {
		return 0, 0, fmt.Errorf("usenet: hwbench: mkdir staging: %w", err)
	}

	// Claude 2026-09-21: temp dir name is .sakms-hwbench-*, never nzb-*, and
	//   we do not write .sakms-owned so stagingsweep will not delete it.
	// Reason: sweep.go only eats IsOwnedStagingPath (nzb-* GID or marker).
	// Troubleshooting: calibration dir vanishing mid-bench.
	// Review if: hardware priors become a typed settings struct or the engine
	//   emits its own ETA.
	dir, err := os.MkdirTemp(staging, ".sakms-hwbench-*")
	if err != nil {
		return 0, 0, fmt.Errorf("usenet: hwbench: mkdir: %w", err)
	}
	defer os.RemoveAll(dir)

	started := time.Now()
	payloadPath, zipPath, err := writeHWBenchFixture(dir)
	if err != nil {
		return 0, 0, err
	}

	unpackBps, unpackErr := benchUnpack(ctx, dir, zipPath)
	repairBps, repairErr := benchRepair(ctx, payloadPath)
	if repairBps <= 0 && unpackBps <= 0 {
		if repairErr == nil {
			repairErr = errors.New("no repair sample")
		}
		if unpackErr == nil {
			unpackErr = errors.New("no unpack sample")
		}
		return 0, 0, fmt.Errorf("usenet: hwbench: both sides failed: repair: %v; unpack: %v", repairErr, unpackErr)
	}

	m.SetPriors(repairBps, unpackBps)
	log.Printf("usenet: hwbench repair_bps=%d unpack_bps=%d dur_ms=%d",
		repairBps, unpackBps, time.Since(started).Milliseconds())
	return repairBps, unpackBps, nil
}

func writeHWBenchFixture(dir string) (payloadPath, zipPath string, err error) {
	payload := make([]byte, hwBenchPayloadBytes)
	if _, err := rand.Read(payload); err != nil {
		return "", "", fmt.Errorf("usenet: hwbench: rand: %w", err)
	}
	payloadPath = filepath.Join(dir, "payload.bin")
	if err := os.WriteFile(payloadPath, payload, 0o644); err != nil {
		return "", "", fmt.Errorf("usenet: hwbench: write payload: %w", err)
	}
	zipPath = filepath.Join(dir, "fixture.zip")
	f, err := os.Create(zipPath)
	if err != nil {
		return "", "", fmt.Errorf("usenet: hwbench: create zip: %w", err)
	}
	zw := zip.NewWriter(f)
	w, err := zw.Create("payload.bin")
	if err != nil {
		_ = zw.Close()
		_ = f.Close()
		return "", "", fmt.Errorf("usenet: hwbench: zip entry: %w", err)
	}
	if _, err := w.Write(payload); err != nil {
		_ = zw.Close()
		_ = f.Close()
		return "", "", fmt.Errorf("usenet: hwbench: zip write: %w", err)
	}
	if err := zw.Close(); err != nil {
		_ = f.Close()
		return "", "", fmt.Errorf("usenet: hwbench: zip close: %w", err)
	}
	if err := f.Close(); err != nil {
		return "", "", fmt.Errorf("usenet: hwbench: zip file close: %w", err)
	}
	return payloadPath, zipPath, nil
}

func benchUnpack(ctx context.Context, dir, zipPath string) (int64, error) {
	seven, err := lookPath("7z")
	if err != nil {
		log.Printf("usenet: hwbench unpack skipped: no 7z/unrar")
		return 0, nil
	}
	outDir := filepath.Join(dir, "unpacked")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return 0, fmt.Errorf("mkdir unpack dest: %w", err)
	}
	start := time.Now()
	cmd := exec.CommandContext(ctx, seven, "x", "-y", "-o"+outDir, zipPath)
	out, runErr := cmd.CombinedOutput()
	elapsed := time.Since(start)
	if runErr != nil {
		return 0, fmt.Errorf("7z x: %w (%s)", runErr, out)
	}
	if elapsed <= 0 {
		return 0, errors.New("unpack elapsed 0")
	}
	bps := int64(hwBenchPayloadBytes) * int64(time.Second) / int64(elapsed)
	if bps <= 0 {
		return 0, errors.New("unpack bps 0")
	}
	return bps, nil
}

func benchRepair(ctx context.Context, payloadPath string) (int64, error) {
	h := sha256.New()
	var total int64
	start := time.Now()
	for {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		data, err := os.ReadFile(payloadPath)
		if err != nil {
			return 0, fmt.Errorf("read payload: %w", err)
		}
		h.Reset()
		if _, err := h.Write(data); err != nil {
			return 0, fmt.Errorf("sha256: %w", err)
		}
		_ = h.Sum(nil)
		total += int64(len(data))
		if time.Since(start) >= hwBenchRepairMinWall {
			break
		}
	}
	elapsed := time.Since(start)
	if elapsed <= 0 {
		return 0, errors.New("repair elapsed 0")
	}
	bps := total * int64(time.Second) / int64(elapsed)
	if bps <= 0 {
		return 0, errors.New("repair bps 0")
	}
	return bps, nil
}

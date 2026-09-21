package usenet

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestCalibrateHardware_RepairAlways(t *testing.T) {
	staging := t.TempDir()
	m := New(Config{StagingDir: staging})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	repairBps, unpackBps, err := m.CalibrateHardware(ctx)
	if err != nil {
		t.Fatalf("CalibrateHardware: %v", err)
	}
	if repairBps <= 0 {
		t.Fatalf("repair bps = %d, want finite > 0", repairBps)
	}
	if _, err := exec.LookPath("7z"); err == nil {
		if unpackBps <= 0 {
			t.Fatalf("7z on PATH but unpack bps = %d", unpackBps)
		}
	} else {
		t.Log("7z not on PATH; unpack sample skipped (documented)")
		if unpackBps != 0 {
			t.Fatalf("unpack bps = %d, want 0 when 7z missing", unpackBps)
		}
	}

	gotRepair, gotUnpack, rn, un := m.Priors()
	if gotRepair != repairBps {
		t.Fatalf("in-memory repair = %d, want REPLACE %d (not EMA toward 25 MiB/s)", gotRepair, repairBps)
	}
	if unpackBps > 0 && gotUnpack != unpackBps {
		t.Fatalf("in-memory unpack = %d, want REPLACE %d", gotUnpack, unpackBps)
	}
	if rn != 0 || un != 0 {
		t.Fatalf("sample counts after bench = %d/%d, want 0/0", rn, un)
	}
	cal, at := m.Calibration()
	if !cal || at.IsZero() {
		t.Fatalf("calibration = %t %v", cal, at)
	}

	leftovers, err := filepath.Glob(filepath.Join(staging, ".sakms-hwbench-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(leftovers) != 0 {
		t.Fatalf("bench dir not cleaned: %v", leftovers)
	}
}

func TestCalibrateHardware_OverlapReturnsInProgress(t *testing.T) {
	m := New(Config{StagingDir: t.TempDir()})
	hold := make(chan struct{})
	m.SetCalibrationHold(hold)
	var once sync.Once
	release := func() { once.Do(func() { close(hold) }) }
	t.Cleanup(release)

	errc := make(chan error, 1)
	go func() {
		_, _, err := m.CalibrateHardware(context.Background())
		errc <- err
	}()

	deadline := time.Now().Add(2 * time.Second)
	for !m.CalibrationInProgress() {
		if time.Now().After(deadline) {
			t.Fatal("first CalibrateHardware did not take the in-flight lock")
		}
		time.Sleep(5 * time.Millisecond)
	}

	_, _, err := m.CalibrateHardware(context.Background())
	if err != ErrCalibrationInProgress {
		t.Fatalf("overlapping err = %v, want ErrCalibrationInProgress", err)
	}
	release()
	if err := <-errc; err != nil {
		t.Fatalf("held calibration: %v", err)
	}
}

func TestCalibrateHardware_DoesNotOwnStagingName(t *testing.T) {
	staging := t.TempDir()
	dir, err := os.MkdirTemp(staging, ".sakms-hwbench-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	if IsOwnedStagingPath(staging, dir) {
		t.Fatalf("hwbench dir %s must not be owned (sweep would eat it)", dir)
	}
}

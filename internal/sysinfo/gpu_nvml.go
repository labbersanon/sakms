//go:build cgo

package sysinfo

// GPU NVML enrichment — CGO-enabled builds only.
//
// go-nvml's type definitions live in CGO bridge files; the package fails to
// compile with CGO_ENABLED=0 (Docker builds use this flag). This file and its
// stub counterpart (gpu_nvml_nocgo.go) gate the import behind the cgo build
// tag so the Docker image builds cleanly while local/CGO builds get real NVML.

import (
	"fmt"

	"github.com/NVIDIA/go-nvml/pkg/nvml"
)

// nvmlInit is the function used to initialize NVML. Package-level var so tests
// can inject a stub without real hardware.
var nvmlInit = nvml.Init

// enrichNVIDIAWithNVML upgrades the metrics of NVIDIA sysfs entries (those left
// at UtilPercent == -1 by readNVIDIAGPU) using NVML when the driver is
// reachable. Every step is soft: an unreachable driver, a failed count, or a
// per-device query error leaves the sysfs entries untouched rather than
// aborting — a GPU read failure must never blank out the rest of the sample.
func enrichNVIDIAWithNVML(gpus []GPURaw) []GPURaw {
	if ret := nvmlInit(); ret != nvml.SUCCESS {
		return gpus // driver unavailable or not exposed to container — graceful
	}
	defer nvml.Shutdown()

	count, ret := nvml.DeviceGetCount()
	if ret != nvml.SUCCESS {
		return gpus
	}

	// Build name-keyed index of NVIDIA sysfs entries (util=-1) for matching.
	byName := make(map[string]int) // GPU name → gpus slice index
	for i, g := range gpus {
		if g.UtilPercent == -1 {
			byName[g.Name] = i
		}
	}

	matched := make(map[int]bool)
	for i := 0; i < count; i++ {
		device, ret := nvml.DeviceGetHandleByIndex(i)
		if ret != nvml.SUCCESS {
			continue
		}
		name, ret := nvml.DeviceGetName(device)
		if ret != nvml.SUCCESS {
			name = fmt.Sprintf("NVIDIA GPU %d", i)
		}

		utilPct := -1
		if util, ret := nvml.DeviceGetUtilizationRates(device); ret == nvml.SUCCESS {
			utilPct = int(util.Gpu)
		}

		var vramUsed, vramTotal int64
		if mem, ret := nvml.DeviceGetMemoryInfo(device); ret == nvml.SUCCESS {
			vramUsed = int64(mem.Used)
			vramTotal = int64(mem.Total)
		}

		var power int64
		if p, ret := nvml.DeviceGetPowerUsage(device); ret == nvml.SUCCESS {
			power = int64(p) * 1000 // milliwatts → microwatts
		}

		g := GPURaw{Name: name, UtilPercent: utilPct,
			VRAMUsedBytes: vramUsed, VRAMTotalBytes: vramTotal,
			PowerMicrowatts: power}

		if idx, ok := byName[name]; ok && !matched[idx] {
			gpus[idx] = g // update existing sysfs entry
			matched[idx] = true
		}
		// Claude 2026-08-04: do not append NVML-only devices here.
		// Reason: appending injected host GPUs into unit tests that pass a
		// TempDir drm tree (and could double-count on real hosts).
		// Troubleshooting: TestReadGPUs_* saw len=2 / want 1 with CGO+NVML.
		// Review if: a host with NVML but no /sys/class/drm NVIDIA card needs
		// discovery — then add an explicit Sample()-level path, not enrich.
	}
	return gpus
}

package sysinfo

// GPU metrics read straight from /sys/class/drm — a point-in-time level (util
// %, VRAM used/total, board power), not a cumulative counter, so it needs no
// delta pass in ComputeRates the way CPU/net/disk do. Everything here is soft:
// a host with no GPU, an unreadable optional file, or an unrecognized vendor
// yields a shorter/empty slice, never an error that would abort the whole
// sample (a GPU read failure must not blank out CPU/RAM/disk on the dashboard).
//
// UNVERIFIED ASSUMPTION: the AMD amdgpu sysfs attribute names
// (gpu_busy_percent, mem_info_vram_used/total, hwmon*/power1_input) and the
// NVIDIA /proc/driver/nvidia/gpus/*/information "Model:" line are modeled from
// kernel docs, not confirmed against every driver version on the real host.
// Any missing/renamed file is treated as "unavailable" (field left at 0/-1),
// so a wrong guess degrades gracefully rather than crashing the stream.

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/NVIDIA/go-nvml/pkg/nvml"
)

// GPURaw is one GPU's point-in-time reading. UtilPercent is -1 when
// utilization is unavailable (NVIDIA/Intel have no sysfs util path without a
// vendor library); PowerMicrowatts is 0 when unavailable.
type GPURaw struct {
	Name            string
	UtilPercent     int // -1 = unavailable
	VRAMUsedBytes   int64
	VRAMTotalBytes  int64
	PowerMicrowatts int64 // 0 = unavailable
}

// baseCardRe matches a base DRM card directory (card0, card1, …) but not a
// connector subdir (card0-DP-1, card0-HDMI-A-1) or a render node (renderD128).
// A connector subdir can resolve device/vendor through its symlink and produce
// a duplicate/phantom GPU entry on any host with display outputs, so it must be
// excluded structurally, not just skipped by vendor.
var baseCardRe = regexp.MustCompile(`^card\d+$`)

// GPU vendor IDs as they appear (lowercase hex, 0x-prefixed) in device/vendor.
const (
	vendorAMD    = "0x1002"
	vendorNVIDIA = "0x10de"
	vendorIntel  = "0x8086"
)

// readGPUs enumerates DRM base cards under drmBasePath and returns one GPURaw
// per card whose vendor is recognized (AMD/NVIDIA/Intel). A card with an
// unreadable or unknown vendor yields no entry — soft failures apply only to
// the OPTIONAL per-field reads AFTER a card is classified, never to the card's
// inclusion itself.
func readGPUs(drmBasePath string) []GPURaw {
	matches, err := filepath.Glob(filepath.Join(drmBasePath, "card*"))
	if err != nil {
		return nil
	}

	var gpus []GPURaw
	for _, cardPath := range matches {
		base := filepath.Base(cardPath)
		if !baseCardRe.MatchString(base) {
			continue // skip connector subdirs and render nodes
		}

		vendor := readTrimmed(filepath.Join(cardPath, "device", "vendor"))
		switch vendor {
		case vendorAMD:
			gpus = append(gpus, readAMDGPU(cardPath, base))
		case vendorNVIDIA:
			gpus = append(gpus, readNVIDIAGPU())
		case vendorIntel:
			gpus = append(gpus, readIntelGPU(cardPath))
		default:
			// Unreadable or unrecognized vendor: not a GPU we can classify,
			// so emit nothing rather than a blank entry.
		}
	}

	gpus = enrichNVIDIAWithNVML(gpus)
	return gpus
}

// nvmlInit is the function used to initialize NVML. Overridden in tests.
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

	// Build name-keyed index of NVIDIA sysfs entries (util=-1) for matching
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
		} else {
			gpus = append(gpus, g) // NVML-only discovery (rare)
		}
	}
	return gpus
}

// readAMDGPU reads an AMD card's util %, VRAM, power, and name. Every field is
// a soft read: a missing file leaves that field at its zero/-1 default.
func readAMDGPU(cardPath, base string) GPURaw {
	device := filepath.Join(cardPath, "device")
	g := GPURaw{UtilPercent: -1}

	if v, ok := readInt(filepath.Join(device, "gpu_busy_percent")); ok {
		g.UtilPercent = int(v)
	}
	if v, ok := readInt(filepath.Join(device, "mem_info_vram_used")); ok {
		g.VRAMUsedBytes = v
	}
	if v, ok := readInt(filepath.Join(device, "mem_info_vram_total")); ok {
		g.VRAMTotalBytes = v
	}
	// power1_input lives under a hwmon* subdir whose exact name varies; take
	// the first match. Missing hwmon or file → power stays 0.
	if powerFiles, _ := filepath.Glob(filepath.Join(device, "hwmon", "hwmon*", "power1_input")); len(powerFiles) > 0 {
		if v, ok := readInt(powerFiles[0]); ok {
			g.PowerMicrowatts = v
		}
	}

	g.Name = readTrimmed(filepath.Join(device, "product_name"))
	if g.Name == "" {
		g.Name = "AMD GPU (" + base + ")"
	}
	return g
}

// readNVIDIAGPU returns an NVIDIA card entry: no sysfs utilization/VRAM/power
// path without NVML, so only the name is populated (from the proc driver info).
func readNVIDIAGPU() GPURaw {
	return GPURaw{
		Name:        nvidiaModelName(),
		UtilPercent: -1,
	}
}

// readIntelGPU returns an Intel card entry: no util path here either; name from
// product_name when present, else a generic fallback.
func readIntelGPU(cardPath string) GPURaw {
	name := readTrimmed(filepath.Join(cardPath, "device", "product_name"))
	if name == "" {
		name = "Intel GPU"
	}
	return GPURaw{
		Name:        name,
		UtilPercent: -1,
	}
}

// nvidiaModelName returns the first GPU's model from
// /proc/driver/nvidia/gpus/*/information's "Model:" line, or "NVIDIA GPU" when
// unavailable.
func nvidiaModelName() string {
	infoFiles, _ := filepath.Glob("/proc/driver/nvidia/gpus/*/information")
	for _, f := range infoFiles {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(line, "Model:") {
				name := strings.TrimSpace(strings.TrimPrefix(line, "Model:"))
				if name != "" {
					return name
				}
			}
		}
	}
	return "NVIDIA GPU"
}

// readTrimmed reads a sysfs file and returns its whitespace-trimmed contents,
// or "" on any read error (soft).
func readTrimmed(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// readInt reads a sysfs file holding a single base-10 integer. The bool is
// false on any read/parse error, so callers can leave the field at its default.
func readInt(path string) (int64, bool) {
	s := readTrimmed(path)
	if s == "" {
		return 0, false
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

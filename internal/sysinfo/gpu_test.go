package sysinfo

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/NVIDIA/go-nvml/pkg/nvml"
)

// writeCardFile writes one sysfs attribute under <base>/<card>/device/<name>,
// creating the directory chain. Used to assemble a fake DRM tree per test.
func writeCardFile(t *testing.T, base, card, name, content string) {
	t.Helper()
	full := filepath.Join(base, card, "device", name)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", full, err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", full, err)
	}
}

func TestReadGPUs_AMD(t *testing.T) {
	base := t.TempDir()
	writeCardFile(t, base, "card0", "vendor", "0x1002\n")
	writeCardFile(t, base, "card0", "gpu_busy_percent", "42\n")
	writeCardFile(t, base, "card0", "mem_info_vram_used", "1073741824\n")  // 1 GiB
	writeCardFile(t, base, "card0", "mem_info_vram_total", "8589934592\n") // 8 GiB
	writeCardFile(t, base, "card0", "product_name", "Radeon RX 7900 XT\n")
	// power1_input under a hwmon* subdir.
	power := filepath.Join(base, "card0", "device", "hwmon", "hwmon3", "power1_input")
	if err := os.MkdirAll(filepath.Dir(power), 0o755); err != nil {
		t.Fatalf("mkdir hwmon: %v", err)
	}
	if err := os.WriteFile(power, []byte("125000000\n"), 0o644); err != nil {
		t.Fatalf("write power1_input: %v", err)
	}

	gpus := readGPUs(base)
	if len(gpus) != 1 {
		t.Fatalf("readGPUs returned %d GPUs, want 1", len(gpus))
	}
	g := gpus[0]
	if g.UtilPercent != 42 {
		t.Errorf("UtilPercent = %d, want 42", g.UtilPercent)
	}
	if g.VRAMUsedBytes != 1073741824 {
		t.Errorf("VRAMUsedBytes = %d, want 1073741824", g.VRAMUsedBytes)
	}
	if g.VRAMTotalBytes != 8589934592 {
		t.Errorf("VRAMTotalBytes = %d, want 8589934592", g.VRAMTotalBytes)
	}
	if g.PowerMicrowatts != 125000000 {
		t.Errorf("PowerMicrowatts = %d, want 125000000", g.PowerMicrowatts)
	}
	if g.Name != "Radeon RX 7900 XT" {
		t.Errorf("Name = %q, want %q", g.Name, "Radeon RX 7900 XT")
	}
}

func TestReadGPUs_NVIDIA(t *testing.T) {
	base := t.TempDir()
	writeCardFile(t, base, "card0", "vendor", "0x10de\n")

	gpus := readGPUs(base)
	if len(gpus) != 1 {
		t.Fatalf("readGPUs returned %d GPUs, want 1", len(gpus))
	}
	if gpus[0].UtilPercent != -1 {
		t.Errorf("UtilPercent = %d, want -1 (NVIDIA has no sysfs util path)", gpus[0].UtilPercent)
	}
	// Name resolves from the real /proc path (usually absent in CI) → the
	// generic fallback; either way it must be non-empty and not panic.
	if gpus[0].Name == "" {
		t.Errorf("Name is empty, want a non-empty NVIDIA name")
	}
}

func TestReadGPUs_NoCards(t *testing.T) {
	base := t.TempDir()
	gpus := readGPUs(base)
	if len(gpus) != 0 {
		t.Fatalf("readGPUs returned %d GPUs, want 0 for an empty base dir", len(gpus))
	}
}

func TestReadGPUs_MissingOptionalFiles(t *testing.T) {
	base := t.TempDir()
	// AMD vendor + util, but no VRAM/power/name files at all.
	writeCardFile(t, base, "card0", "vendor", "0x1002\n")
	writeCardFile(t, base, "card0", "gpu_busy_percent", "17\n")

	gpus := readGPUs(base)
	if len(gpus) != 1 {
		t.Fatalf("readGPUs returned %d GPUs, want 1", len(gpus))
	}
	g := gpus[0]
	if g.UtilPercent != 17 {
		t.Errorf("UtilPercent = %d, want 17", g.UtilPercent)
	}
	if g.VRAMTotalBytes != 0 {
		t.Errorf("VRAMTotalBytes = %d, want 0 (file absent)", g.VRAMTotalBytes)
	}
	if g.VRAMUsedBytes != 0 {
		t.Errorf("VRAMUsedBytes = %d, want 0 (file absent)", g.VRAMUsedBytes)
	}
	if g.PowerMicrowatts != 0 {
		t.Errorf("PowerMicrowatts = %d, want 0 (file absent)", g.PowerMicrowatts)
	}
	// product_name absent → the "AMD GPU (card0)" fallback.
	if g.Name != "AMD GPU (card0)" {
		t.Errorf("Name = %q, want %q", g.Name, "AMD GPU (card0)")
	}
}

// TestEnrichNVIDIAWithNVML_Unavailable covers the graceful-fallback path: when
// NVML can't initialize (no driver, or not exposed to the container), the sysfs
// entries pass through untouched. There is deliberately NO test for the
// NVML-success path — it requires real NVIDIA hardware and a reachable driver,
// which isn't a unit test; the injected nvmlInit stub keeps this one
// hardware-independent.
func TestEnrichNVIDIAWithNVML_Unavailable(t *testing.T) {
	orig := nvmlInit
	t.Cleanup(func() { nvmlInit = orig })
	nvmlInit = func() nvml.Return { return nvml.ERROR_DRIVER_NOT_LOADED }

	input := []GPURaw{{Name: "GeForce RTX 4070", UtilPercent: -1}}
	result := enrichNVIDIAWithNVML(input)
	if len(result) != 1 || result[0].UtilPercent != -1 {
		t.Errorf("expected graceful passthrough when NVML unavailable, got %+v", result)
	}
}

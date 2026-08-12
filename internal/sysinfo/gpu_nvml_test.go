//go:build cgo

package sysinfo

// Tests for the CGO-enabled NVML enrichment path. There is deliberately no
// test for the NVML-success path — it requires real NVIDIA hardware and a
// reachable driver, which isn't a unit test; the injected nvmlInit stub keeps
// this one hardware-independent.

import (
	"os"
	"testing"

	"github.com/NVIDIA/go-nvml/pkg/nvml"
)

// Claude 2026-08-12: package-wide TestMain stubs nvmlInit before any test runs.
// Reason: readGPUs() calls enrichNVIDIAWithNVML unconditionally (gpu.go), so
// gpu_test.go's TestReadGPUs_* would otherwise hit a real driver on cgo hosts.
// Troubleshooting: per-test stubs (TestEnrichNVIDIAWithNVML_Unavailable) still
// override this default via t.Cleanup restore.
// Review if: enrichNVIDIAWithNVML is no longer called from readGPUs.
func TestMain(m *testing.M) {
	orig := nvmlInit
	nvmlInit = func() nvml.Return { return nvml.ERROR_DRIVER_NOT_LOADED }
	code := m.Run()
	nvmlInit = orig
	os.Exit(code)
}

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

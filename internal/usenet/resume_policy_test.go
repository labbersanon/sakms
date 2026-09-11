package usenet

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSweepResumeArtifacts_ClearsOwnedSidecars(t *testing.T) {
	staging := t.TempDir()
	m := New(Config{StagingDir: staging})

	owned := filepath.Join(staging, "nzb-aaaaaaaaaaaaaaaa")
	foreign := filepath.Join(staging, "not-ours")
	for _, dir := range []string{owned, foreign} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, ResumeFileName), []byte(`{"v":1}`), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeOwnedMarker(owned)

	n := m.SweepResumeArtifacts()
	if n != 1 {
		t.Fatalf("swept %d dirs, want 1", n)
	}
	if _, err := os.Stat(filepath.Join(owned, ResumeFileName)); !os.IsNotExist(err) {
		t.Fatal("owned sidecar should be gone")
	}
	if _, err := os.Stat(filepath.Join(foreign, ResumeFileName)); err != nil {
		t.Fatal("foreign sidecar must remain untouched")
	}
}

func TestSetResumePolicy_ForceFullSweepsStaging(t *testing.T) {
	staging := t.TempDir()
	m := New(Config{StagingDir: staging})
	owned := filepath.Join(staging, "nzb-bbbbbbbbbbbbbbbb")
	if err := os.MkdirAll(owned, 0o755); err != nil {
		t.Fatal(err)
	}
	writeOwnedMarker(owned)
	if err := os.WriteFile(filepath.Join(owned, ResumeFileName), []byte(`{"v":1}`), 0o644); err != nil {
		t.Fatal(err)
	}

	m.SetResumePolicy(true, true)
	enabled, force := m.ResumePolicy()
	if !enabled || !force {
		t.Fatalf("policy = (%v,%v), want (true,true)", enabled, force)
	}
	if _, err := os.Stat(filepath.Join(owned, ResumeFileName)); !os.IsNotExist(err) {
		t.Fatal("force-full should clear owned sidecar immediately")
	}
}

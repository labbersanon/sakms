package usenet

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestIsStagingMetaFile(t *testing.T) {
	cases := map[string]bool{
		OwnedMarkerFile:                    true,
		ResumeFileName:                     true,
		resumeTmpName:                      true,
		"movie.mkv":                        false,
		"release.rar":                      false,
		filepath.Join("x", ResumeFileName): true,
	}
	for name, want := range cases {
		if got := IsStagingMetaFile(name); got != want {
			t.Fatalf("IsStagingMetaFile(%q)=%v want %v", name, got, want)
		}
	}
}

func TestClearResumeArtifacts(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{ResumeFileName, resumeTmpName, "keep.mkv"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := ClearResumeArtifacts(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, ResumeFileName)); !os.IsNotExist(err) {
		t.Fatalf("resume sidecar should be gone, err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, resumeTmpName)); !os.IsNotExist(err) {
		t.Fatalf("resume tmp should be gone, err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "keep.mkv")); err != nil {
		t.Fatalf("content file should remain: %v", err)
	}
	if err := ClearResumeArtifacts(dir); err != nil {
		t.Fatalf("second clear should be no-op: %v", err)
	}
}

func TestResumeTracker_MarkAndSkip(t *testing.T) {
	dir := t.TempDir()
	tr := loadResumeTracker(dir, "nzb-test", nil, false)
	if tr.hasSegment("a.bin", "<msg1@x>") {
		t.Fatal("empty tracker should not have segment")
	}
	if err := tr.markSegment("a.bin", "<msg1@x>", 1, 0, 100, 200); err != nil {
		t.Fatal(err)
	}
	if !tr.hasSegment("a.bin", "<msg1@x>") {
		t.Fatal("expected marked segment")
	}
	if tr.skippedSegments() != 1 {
		t.Fatalf("skipped=%d want 1", tr.skippedSegments())
	}
	raw, err := os.ReadFile(filepath.Join(dir, ResumeFileName))
	if err != nil {
		t.Fatal(err)
	}
	var snap ResumeSnapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		t.Fatal(err)
	}
	if snap.GID != "nzb-test" || snap.Files["a.bin"] == nil || len(snap.Files["a.bin"].Done) != 1 {
		t.Fatalf("unexpected snapshot: %+v", snap)
	}
	tr2 := loadResumeTracker(dir, "nzb-test", nil, false)
	if !tr2.hasSegment("a.bin", "<msg1@x>") {
		t.Fatal("reloaded tracker missing segment")
	}
}

func TestResumeTracker_DisabledWritesNothing(t *testing.T) {
	dir := t.TempDir()
	tr := loadResumeTracker(dir, "nzb-test", nil, true)
	if err := tr.markSegment("a.bin", "<msg1@x>", 1, 0, 100, 200); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, ResumeFileName)); !os.IsNotExist(err) {
		t.Fatal("disabled tracker must not write sidecar")
	}
}

func TestDeleteArchiveMembers_RemovesResumeSidecar(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{OwnedMarkerFile, ResumeFileName, resumeTmpName, "release.rar", "movie.mkv"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := deleteArchiveMembers(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, OwnedMarkerFile)); err != nil {
		t.Fatalf("owned marker must remain: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ResumeFileName)); !os.IsNotExist(err) {
		t.Fatal("resume sidecar should be deleted after unpack cleanup")
	}
	if _, err := os.Stat(filepath.Join(dir, "release.rar")); !os.IsNotExist(err) {
		t.Fatal("archive should be deleted")
	}
	if _, err := os.Stat(filepath.Join(dir, "movie.mkv")); err != nil {
		t.Fatalf("video should remain: %v", err)
	}
}

func TestSegmentCovered(t *testing.T) {
	dir := t.TempDir()
	tr := loadResumeTracker(dir, "gid", nil, false)
	if err := tr.markSegment("a.bin", "<m@x>", 1, 0, 100, 200); err != nil {
		t.Fatal(err)
	}
	if tr.segmentCovered("a.bin", "<m@x>", 50) {
		t.Fatal("short file must not be covered")
	}
	if !tr.segmentCovered("a.bin", "<m@x>", 100) {
		t.Fatal("exact length should be covered")
	}
	if tr.segmentCovered("a.bin", "<missing@x>", 100) {
		t.Fatal("unknown msg must not be covered")
	}
}

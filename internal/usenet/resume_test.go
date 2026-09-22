package usenet

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
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
	tr := loadResumeTracker(dir, "nzb-test", false)
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
	tr2 := loadResumeTracker(dir, "nzb-test", false)
	if !tr2.hasSegment("a.bin", "<msg1@x>") {
		t.Fatal("reloaded tracker missing segment")
	}
}

func TestResumeTracker_DisabledWritesNothing(t *testing.T) {
	dir := t.TempDir()
	tr := loadResumeTracker(dir, "nzb-test", true)
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
	tr := loadResumeTracker(dir, "gid", false)
	if err := tr.markSegment("a.bin", "<m@x>", 1, 0, 100, 200); err != nil {
		t.Fatal(err)
	}
	if tr.segmentCovered(nil, "a.bin", "<m@x>", 50) {
		t.Fatal("short file must not be covered")
	}
	if !tr.segmentCovered(nil, "a.bin", "<m@x>", 100) {
		t.Fatal("exact length should be covered")
	}
	if tr.segmentCovered(nil, "a.bin", "<missing@x>", 100) {
		t.Fatal("unknown msg must not be covered")
	}
}

func TestSegmentCovered_RejectsHollowRange(t *testing.T) {
	dir := t.TempDir()
	tr := loadResumeTracker(dir, "gid", false)
	if err := tr.markSegment("a.bin", "<m@x>", 1, 0, 100, 200); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "a.bin")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(200); err != nil {
		t.Fatal(err)
	}
	// Size says 200 but range is a sparse/NUL hole — must NOT skip re-download.
	if tr.segmentCovered(f, "a.bin", "<m@x>", 200) {
		t.Fatal("hollow range must not be covered")
	}
	if _, err := f.WriteAt([]byte("payload-data-not-all-nul-bytes!!"), 0); err != nil {
		t.Fatal(err)
	}
	if err := f.Sync(); err != nil {
		t.Fatal(err)
	}
	if !tr.segmentCovered(f, "a.bin", "<m@x>", 200) {
		t.Fatal("populated range should be covered")
	}
	_ = f.Close()
}

func TestLoadResumeTracker_WipesV2(t *testing.T) {
	dir := t.TempDir()
	snap := ResumeSnapshot{
		Version: 2,
		GID:     "nzb-test",
		Files: map[string]*ResumeFile{
			"a.bin": {Size: 273, Done: map[string]ResumeSeg{
				"<m@x>": {Number: 1, Offset: 0, Length: 90},
			}},
		},
	}
	raw, err := json.Marshal(snap)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ResumeFileName), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	payload := filepath.Join(dir, "a.bin")
	if err := os.WriteFile(payload, []byte("packed-contiguous-v2-payload"), 0o644); err != nil {
		t.Fatal(err)
	}

	tr := loadResumeTracker(dir, "nzb-test", false)
	if tr.hasSegment("a.bin", "<m@x>") {
		t.Fatal("v2 sidecar must not be reused after resume v3")
	}
	if _, err := os.Stat(filepath.Join(dir, ResumeFileName)); !os.IsNotExist(err) {
		t.Fatal("v2 sidecar should be wiped")
	}
	if _, err := os.Stat(payload); !os.IsNotExist(err) {
		t.Fatal("v2 packed payload should be wiped")
	}
}

func attachFrozenResumeClock(tr *resumeTracker, now time.Time) {
	clock := now
	tr.now = func() time.Time { return clock }
	tr.lastPersist = now
	tr.flushMarks = resumeFlushMarks
	tr.flushInterval = resumeFlushInterval
}

func TestResumeTracker_BatchFlushesByMarkThreshold(t *testing.T) {
	dir := t.TempDir()
	tr := loadResumeTracker(dir, "nzb-batch", false)
	attachFrozenResumeClock(tr, time.Unix(1_700_000_000, 0))

	const n = 70
	for i := 0; i < n; i++ {
		msg := "<msg" + strconv.Itoa(i) + "@x>"
		if err := tr.markSegment("a.bin", msg, i+1, int64(i*100), 100, 7000); err != nil {
			t.Fatal(err)
		}
	}
	wantMax := (n + resumeFlushMarks - 1) / resumeFlushMarks
	if tr.persistWrites > wantMax {
		t.Fatalf("persistWrites=%d want at most ceil(%d/%d)=%d", tr.persistWrites, n, resumeFlushMarks, wantMax)
	}
	if tr.persistWrites != n/resumeFlushMarks {
		t.Fatalf("persistWrites=%d want %d (exact batches of %d with frozen clock)", tr.persistWrites, n/resumeFlushMarks, resumeFlushMarks)
	}
}

func TestResumeTracker_CompactJSONHasNoPrettyIndent(t *testing.T) {
	dir := t.TempDir()
	tr := loadResumeTracker(dir, "nzb-compact", false)
	if err := tr.markSegment("a.bin", "<msg1@x>", 1, 0, 100, 200); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, ResumeFileName))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("\n  ")) {
		t.Fatalf("sidecar still pretty-indented:\n%s", raw)
	}
	var snap ResumeSnapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		t.Fatal(err)
	}
	if snap.Version != resumeSchemaVersion {
		t.Fatalf("version=%d want %d", snap.Version, resumeSchemaVersion)
	}
}

func TestResumeTracker_FlushPersistsRemainingDirty(t *testing.T) {
	dir := t.TempDir()
	tr := loadResumeTracker(dir, "nzb-flush", false)
	attachFrozenResumeClock(tr, time.Unix(1_700_000_000, 0))

	if err := tr.markSegment("a.bin", "<msg1@x>", 1, 0, 100, 200); err != nil {
		t.Fatal(err)
	}
	if tr.persistWrites != 0 {
		t.Fatalf("single mark under threshold should not persist, writes=%d", tr.persistWrites)
	}
	if _, err := os.Stat(filepath.Join(dir, ResumeFileName)); !os.IsNotExist(err) {
		t.Fatal("sidecar should be absent until Flush")
	}
	if err := tr.Flush(); err != nil {
		t.Fatal(err)
	}
	if tr.persistWrites != 1 {
		t.Fatalf("Flush persistWrites=%d want 1", tr.persistWrites)
	}
	raw, err := os.ReadFile(filepath.Join(dir, ResumeFileName))
	if err != nil {
		t.Fatal(err)
	}
	var snap ResumeSnapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		t.Fatal(err)
	}
	if snap.Files["a.bin"] == nil || len(snap.Files["a.bin"].Done) != 1 {
		t.Fatalf("Flush should persist dirty segment: %+v", snap)
	}
	if err := tr.Flush(); err != nil {
		t.Fatal(err)
	}
	if tr.persistWrites != 1 {
		t.Fatalf("second Flush of clean tracker wrote again: writes=%d", tr.persistWrites)
	}
}

func TestResumeTracker_SetFileSizeForcesWrite(t *testing.T) {
	dir := t.TempDir()
	tr := loadResumeTracker(dir, "nzb-size", false)
	attachFrozenResumeClock(tr, time.Unix(1_700_000_000, 0))

	if err := tr.markSegment("a.bin", "<msg1@x>", 1, 0, 100, 200); err != nil {
		t.Fatal(err)
	}
	if tr.persistWrites != 0 {
		t.Fatalf("mark should be batched, writes=%d", tr.persistWrites)
	}
	tr.setFileSize("a.bin", 200)
	if tr.persistWrites != 1 {
		t.Fatalf("setFileSize persistWrites=%d want 1", tr.persistWrites)
	}
	raw, err := os.ReadFile(filepath.Join(dir, ResumeFileName))
	if err != nil {
		t.Fatal(err)
	}
	var snap ResumeSnapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		t.Fatal(err)
	}
	if snap.Files["a.bin"] == nil || snap.Files["a.bin"].Size != 200 {
		t.Fatalf("setFileSize should persist Size: %+v", snap.Files["a.bin"])
	}
	if _, ok := snap.Files["a.bin"].Done["<msg1@x>"]; !ok {
		t.Fatal("setFileSize force-write should include pending segment marks")
	}
}

func TestResumeTracker_TimeThresholdFlushes(t *testing.T) {
	dir := t.TempDir()
	tr := loadResumeTracker(dir, "nzb-time", false)
	now := time.Unix(1_700_000_000, 0)
	tr.now = func() time.Time { return now }
	tr.lastPersist = now
	tr.flushMarks = resumeFlushMarks
	tr.flushInterval = resumeFlushInterval

	if err := tr.markSegment("a.bin", "<msg1@x>", 1, 0, 100, 200); err != nil {
		t.Fatal(err)
	}
	if tr.persistWrites != 0 {
		t.Fatalf("mark within interval should not persist, writes=%d", tr.persistWrites)
	}
	now = now.Add(resumeFlushInterval)
	if err := tr.markSegment("a.bin", "<msg2@x>", 2, 100, 100, 200); err != nil {
		t.Fatal(err)
	}
	if tr.persistWrites != 1 {
		t.Fatalf("mark after 1s persistWrites=%d want 1", tr.persistWrites)
	}
}

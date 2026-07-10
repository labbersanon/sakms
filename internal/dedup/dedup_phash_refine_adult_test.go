package dedup

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/curtiswtaylorjr/sakms/internal/mediainfo"
	"github.com/curtiswtaylorjr/sakms/internal/phash"
	"github.com/curtiswtaylorjr/sakms/internal/proposals"
	"github.com/curtiswtaylorjr/sakms/internal/servarr"
)

// These mirror the Movies phash-refinement tests (dedup_phash_refine_test.go)
// but exercise the Servarr-backed Adult path (scanAdult) via Scan. Adult has no
// SAK-owned library row to cache against, so there is no cache-reuse analog —
// attachPHashesAdult always recomputes. refHash/nearHash/farHash are reused from
// the Movies refine test (same package). Threshold 2 matches the Movies tests:
// nearHash (0x0f, 4 bits) is inside the 2×5-frame budget, farHash (all bits set)
// is far outside it.

// adultTrackedWhisparr builds a Whisparr handler that reports one tracked scene
// (id 7, foreignID sceneUUIDA) plus the given unmappedFolders JSON — the same
// shape TestScan_Adult_TrackedPlusOrphan_GroupsByForeignID uses.
func adultTrackedWhisparr(t *testing.T, adultRoot, trackedDir, unmappedFolders string) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v3/rootfolder":
			w.Write([]byte(`[{"id":1,"path":"` + adultRoot + `","accessible":true,"freeSpace":1,"unmappedFolders":[` + unmappedFolders + `]}]`))
		case r.URL.Path == "/api/v3/movie" && r.Method == http.MethodGet:
			json.NewEncoder(w).Encode([]servarr.TrackedItem{
				{ID: 7, Title: "Some Scene", Path: trackedDir, RootFolderPath: adultRoot, ForeignID: sceneUUIDA, QualityProfileID: 4},
			})
		case r.URL.Path == "/api/v3/qualityprofile":
			w.Write([]byte(`[{"id":4,"name":"HD"}]`))
		default:
			t.Fatalf("unexpected whisparr request: %s %s", r.Method, r.URL.Path)
		}
	}
}

func TestScanAdult_PHashKeepsNearIdenticalGroup(t *testing.T) {
	dir := t.TempDir()
	adultRoot := filepath.Join(dir, "Adult")
	trackedDir := filepath.Join(adultRoot, "Some Scene")
	orphanName := "Some.Scene." + sceneUUIDA
	orphanDir := filepath.Join(adultRoot, orphanName)
	trackedFile := writeVideoFile(t, trackedDir, "scene.mkv", 100)
	orphanFile := writeVideoFile(t, orphanDir, "scene.mkv", 100)

	unmapped := `{"name":"` + orphanName + `","path":"` + orphanDir + `","relativePath":"` + orphanName + `"}`
	sess := newAdultTestSession(t, adultTrackedWhisparr(t, adultRoot, trackedDir, unmapped),
		fakeStashboxByID(t, map[string]string{sceneUUIDA: "Some Scene"}))

	prober := &fakeProber{byPath: map[string]*mediainfo.Probe{
		trackedFile: {CodecName: "h264", Width: 1280, Height: 720, BitRate: 3000},
		orphanFile:  {CodecName: "h265", Width: 1920, Height: 1080, BitRate: 8000},
	}}
	// Tracked and orphan are perceptually within threshold — the group survives
	// refinement as a real duplicate.
	hasher := &fakePHasher{byPath: map[string]string{trackedFile: refHash, orphanFile: nearHash}}

	got, err := Scan(context.Background(), sess, prober, hasher, 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || len(got[0].Candidates) != 2 {
		t.Fatalf("expected a near-identical pair to stay one 2-candidate group, got %+v", got)
	}
	winners := 0
	for _, c := range got[0].Candidates {
		if c.Winner {
			winners++
		}
	}
	if winners != 1 {
		t.Errorf("expected exactly one Winner, got %d", winners)
	}
}

func TestScanAdult_PHashDropsDivergentCandidate(t *testing.T) {
	dir := t.TempDir()
	adultRoot := filepath.Join(dir, "Adult")
	trackedDir := filepath.Join(adultRoot, "Some Scene")
	orphanName := "Some.Scene." + sceneUUIDA
	orphanDir := filepath.Join(adultRoot, orphanName)
	trackedFile := writeVideoFile(t, trackedDir, "scene.mkv", 100)
	orphanFile := writeVideoFile(t, orphanDir, "scene.mkv", 100)

	unmapped := `{"name":"` + orphanName + `","path":"` + orphanDir + `","relativePath":"` + orphanName + `"}`
	sess := newAdultTestSession(t, adultTrackedWhisparr(t, adultRoot, trackedDir, unmapped),
		fakeStashboxByID(t, map[string]string{sceneUUIDA: "Some Scene"}))

	prober := &fakeProber{byPath: map[string]*mediainfo.Probe{
		trackedFile: {CodecName: "h264", Width: 1280, Height: 720, BitRate: 3000},
		orphanFile:  {CodecName: "h265", Width: 1920, Height: 1080, BitRate: 8000},
	}}
	// The orphan resolves to the same scene but is perceptually far from the
	// tracked reference — it must be dropped, leaving a single survivor and thus
	// NO proposal (keep-both). This is the load-bearing new behavior: without the
	// gate scanAdult would have emitted a delete proposal here.
	hasher := &fakePHasher{byPath: map[string]string{trackedFile: refHash, orphanFile: farHash}}

	got, err := Scan(context.Background(), sess, prober, hasher, 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected the divergent orphan to be dropped and no proposal produced, got %+v", got)
	}
}

func TestScanAdult_PHashUsesTrackedAsReference(t *testing.T) {
	dir := t.TempDir()
	adultRoot := filepath.Join(dir, "Adult")
	trackedDir := filepath.Join(adultRoot, "Some Scene")
	nearName := "Some.Scene.HD." + sceneUUIDA
	farName := "Some.Scene.SD." + sceneUUIDA
	nearDir := filepath.Join(adultRoot, nearName)
	farDir := filepath.Join(adultRoot, farName)
	trackedFile := writeVideoFile(t, trackedDir, "scene.mkv", 100)
	nearFile := writeVideoFile(t, nearDir, "scene.mkv", 100)
	farFile := writeVideoFile(t, farDir, "scene.mkv", 100)

	unmapped := `{"name":"` + nearName + `","path":"` + nearDir + `","relativePath":"` + nearName + `"},` +
		`{"name":"` + farName + `","path":"` + farDir + `","relativePath":"` + farName + `"}`
	sess := newAdultTestSession(t, adultTrackedWhisparr(t, adultRoot, trackedDir, unmapped),
		fakeStashboxByID(t, map[string]string{sceneUUIDA: "Some Scene"}))

	prober := &fakeProber{byPath: map[string]*mediainfo.Probe{
		trackedFile: {CodecName: "h264", Width: 1280, Height: 720, BitRate: 3000},
		nearFile:    {CodecName: "h265", Width: 1920, Height: 1080, BitRate: 8000},
		farFile:     {CodecName: "h264", Width: 1280, Height: 720, BitRate: 2000},
	}}
	// Both orphans are measured against the TRACKED reference (its nonzero
	// TrackedID makes refineByPHash pick it): the near one is kept, the far one
	// dropped. If an orphan were the reference the outcome would differ.
	hasher := &fakePHasher{byPath: map[string]string{trackedFile: refHash, nearFile: nearHash, farFile: farHash}}

	got, err := Scan(context.Background(), sess, prober, hasher, 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || len(got[0].Candidates) != 2 {
		t.Fatalf("expected the tracked reference plus the near orphan, got %+v", got)
	}
	var sawTracked bool
	for _, c := range got[0].Candidates {
		if c.Path == farFile {
			t.Errorf("expected the divergent far orphan to be dropped, but it survived: %+v", c)
		}
		if c.TrackedID == 7 {
			sawTracked = true
		}
	}
	if !sawTracked {
		t.Error("expected the tracked reference to never be dropped, but it's absent from the group")
	}
}

// TestRefineByPHash_TrackedCandidateSelectedRegardlessOfPosition isolates
// refineByPHash's reference-selection logic from any caller's ordering
// convention. Every mode (Movies/Series/Adult) happens to always append its
// tracked candidate first, so a test driven through Scan/ScanLibrary can't
// tell "picked because TrackedID != 0" apart from "picked because it's
// index 0" — this calls refineByPHash directly with the tracked candidate
// placed LAST, proving selection is genuinely TrackedID-driven, not just
// position-driven.
//
// The hash arrangement is deliberately chosen so the two possible reference
// choices produce DIFFERENT survivor sets — a naive "always compare against
// nearHash/refHash, they're close to each other anyway" arrangement would
// pass even with a broken selection (verified: an earlier draft of this test
// did exactly that and passed regardless of which candidate was picked as
// reference, so it proved nothing). Here: orphan-far (farHash, maximally
// distant from everything) sits at index 0; orphan-near (nearHash, close
// ONLY to the tracked candidate's refHash) sits at index 1; the tracked
// candidate (refHash, TrackedID=7) sits last at index 2.
//   - Correct (tracked selected): compare far-vs-ref (far, dropped),
//     near-vs-ref (close, kept) -> survivors = {tracked, near}.
//   - Buggy (index 0 selected): compare near-vs-far (far, dropped),
//     tracked-vs-far (far, dropped) -> survivors = {far itself, alone}.
// These sets are disjoint, so the assertions below only pass under the
// correct, TrackedID-driven selection.
func TestRefineByPHash_TrackedCandidateSelectedRegardlessOfPosition(t *testing.T) {
	candidates := []proposals.Candidate{
		{Path: "orphan-far", TrackedID: 0, PHash: farHash},
		{Path: "orphan-near", TrackedID: 0, PHash: nearHash},
		{Path: "tracked", TrackedID: 7, PHash: refHash}, // reference, deliberately last
	}

	got := refineByPHash(candidates, phash.Frames, 2)

	var sawTracked, sawNear, sawFar bool
	for _, c := range got {
		switch c.Path {
		case "tracked":
			sawTracked = true
		case "orphan-near":
			sawNear = true
		case "orphan-far":
			sawFar = true
		}
	}
	if !sawTracked {
		t.Error("expected the TrackedID!=0 candidate to be kept as the reference even though it's last in the slice")
	}
	if !sawNear {
		t.Error("expected the near orphan (close to the tracked reference) to be kept")
	}
	if sawFar {
		t.Error("expected the far orphan (divergent from the tracked reference, and from the near orphan) to be dropped")
	}
	if len(got) != 2 {
		t.Errorf("expected exactly 2 survivors (tracked + near), got %d: %+v", len(got), got)
	}
}

// TestScanAdult_PHashAllCandidatesUncomputable is a panic regression: if every
// candidate in a same-foreignID group fails to hash, attachPHashesAdult drops
// all of them, so refineByPHash must handle a 0-length slice without panicking
// (it previously indexed candidates[0] unconditionally). Uncomputable is the
// same tolerant outcome as a divergent group: no proposal, keep-both.
func TestScanAdult_PHashAllCandidatesUncomputable(t *testing.T) {
	dir := t.TempDir()
	adultRoot := filepath.Join(dir, "Adult")
	trackedDir := filepath.Join(adultRoot, "Some Scene")
	orphanName := "Some.Scene." + sceneUUIDA
	orphanDir := filepath.Join(adultRoot, orphanName)
	trackedFile := writeVideoFile(t, trackedDir, "scene.mkv", 100)
	orphanFile := writeVideoFile(t, orphanDir, "scene.mkv", 100)

	unmapped := `{"name":"` + orphanName + `","path":"` + orphanDir + `","relativePath":"` + orphanName + `"}`
	sess := newAdultTestSession(t, adultTrackedWhisparr(t, adultRoot, trackedDir, unmapped),
		fakeStashboxByID(t, map[string]string{sceneUUIDA: "Some Scene"}))

	prober := &fakeProber{byPath: map[string]*mediainfo.Probe{
		trackedFile: {CodecName: "h264", Width: 1280, Height: 720, BitRate: 3000},
		orphanFile:  {CodecName: "h265", Width: 1920, Height: 1080, BitRate: 8000},
	}}
	// Empty byPath: every Hash call errors, so attachPHashesAdult drops the whole
	// group to a 0-length slice. No file was ever hashed, so none can be a match.
	hasher := &fakePHasher{}

	got, err := Scan(context.Background(), sess, prober, hasher, 2)
	if err != nil {
		t.Fatalf("unexpected error (must not panic): %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected no proposal when the whole group is uncomputable, got %+v", got)
	}
}

// TestScanAdult_PHashTrackedReferenceUncomputable asserts the non-destructive
// safety property directly: the tracked file is un-hashable (absent from byPath)
// while the orphan hashes fine → attachPHashesAdult drops the tracked candidate,
// the group falls below 2, and NO proposal is produced. A file that couldn't be
// hashed is never compared and never proposed for deletion.
func TestScanAdult_PHashTrackedReferenceUncomputable(t *testing.T) {
	dir := t.TempDir()
	adultRoot := filepath.Join(dir, "Adult")
	trackedDir := filepath.Join(adultRoot, "Some Scene")
	orphanName := "Some.Scene." + sceneUUIDA
	orphanDir := filepath.Join(adultRoot, orphanName)
	writeVideoFile(t, trackedDir, "scene.mkv", 100)
	orphanFile := writeVideoFile(t, orphanDir, "scene.mkv", 100)

	unmapped := `{"name":"` + orphanName + `","path":"` + orphanDir + `","relativePath":"` + orphanName + `"}`
	sess := newAdultTestSession(t, adultTrackedWhisparr(t, adultRoot, trackedDir, unmapped),
		fakeStashboxByID(t, map[string]string{sceneUUIDA: "Some Scene"}))

	trackedFile := filepath.Join(trackedDir, "scene.mkv")
	prober := &fakeProber{byPath: map[string]*mediainfo.Probe{
		trackedFile: {CodecName: "h264", Width: 1280, Height: 720, BitRate: 3000},
		orphanFile:  {CodecName: "h265", Width: 1920, Height: 1080, BitRate: 8000},
	}}
	// Only the orphan hashes; the tracked file's path is absent, so its Hash call
	// errors and attachPHashesAdult drops it. The lone remaining orphan can't form
	// a duplicate on its own.
	hasher := &fakePHasher{byPath: map[string]string{orphanFile: refHash}}

	got, err := Scan(context.Background(), sess, prober, hasher, 2)
	if err != nil {
		t.Fatalf("unexpected error (must not panic): %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected no proposal when the tracked reference is uncomputable — the tracked file must never be proposed for deletion, got %+v", got)
	}
}

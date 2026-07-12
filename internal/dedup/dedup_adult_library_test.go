package dedup

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/curtiswtaylorjr/sakms/internal/identify"
	"github.com/curtiswtaylorjr/sakms/internal/library"
	"github.com/curtiswtaylorjr/sakms/internal/mediainfo"
	"github.com/curtiswtaylorjr/sakms/internal/mode"
	"github.com/curtiswtaylorjr/sakms/internal/proposals"
	"github.com/curtiswtaylorjr/sakms/internal/stashbox"
	"github.com/curtiswtaylorjr/sakms/internal/throttle"
)

// newAdultLibraryScanSession builds an Adult session whose Identify resolves
// scenes via the direct UUID path against a fake StashDB — no Ollama needed,
// throttle disabled — but carries NO Servarr client, exactly as the
// library-backed Adult path runs once Whisparr is eliminated.
func newAdultLibraryScanSession(t *testing.T, stashboxHandler http.HandlerFunc) *mode.Session {
	t.Helper()
	ssrv := httptest.NewServer(stashboxHandler)
	t.Cleanup(ssrv.Close)
	boxes := map[string]*stashbox.Client{
		"stashdb": stashbox.New(stashbox.Config{Endpoint: ssrv.URL, APIKey: "k", HasVoteField: true}, ssrv.Client()),
	}
	return &mode.Session{
		Mode: mode.Adult,
		Identify: &identify.Identifier{
			Boxes:    identify.NewBoxSearcher(boxes, nil),
			Throttle: throttle.New(0),
		},
	}
}

func TestScanLibraryAdult_RequiresIdentifyConfigured(t *testing.T) {
	sess := &mode.Session{Mode: mode.Adult}
	if _, err := ScanLibraryAdult(context.Background(), sess, newTestLibraryStore(t), t.TempDir(), &fakeProber{}, &fakePHasher{}, 10); err == nil {
		t.Fatal("expected an error when Identify isn't configured")
	}
}

func TestScanLibraryAdult_RequiresRootFolderPath(t *testing.T) {
	sess := newAdultLibraryScanSession(t, fakeStashboxByID(t, nil))
	if _, err := ScanLibraryAdult(context.Background(), sess, newTestLibraryStore(t), "", &fakeProber{}, &fakePHasher{}, 10); err == nil {
		t.Fatal("expected an error when no root folder path is configured")
	}
}

// TestScanLibraryAdult_TrackedScenePlusOrphan_ProposesWithCorrectWinner is the
// core case: a scene tracked at one path, plus an untracked copy of the SAME
// (box, scene_id) discovered at a different path, groups as a duplicate with
// the higher-quality orphan as the winner.
func TestScanLibraryAdult_TrackedScenePlusOrphan_ProposesWithCorrectWinner(t *testing.T) {
	dir := t.TempDir()
	trackedDir := filepath.Join(dir, "Studio", "Some Scene")
	orphanName := "Some.Scene." + sceneUUIDA
	orphanDir := filepath.Join(dir, "Studio", orphanName)
	trackedFile := writeVideoFile(t, trackedDir, "scene.mkv", 100)
	orphanFile := writeVideoFile(t, orphanDir, "scene.mkv", 100)

	libStore := newTestLibraryStore(t)
	tracked, err := libStore.UpsertScene(context.Background(), library.Scene{
		Box: "stashdb", SceneID: sceneUUIDA, Title: "Some Scene", Studio: "Some Studio",
		FilePath: trackedFile, RootFolderPath: dir,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sess := newAdultLibraryScanSession(t, fakeStashboxByID(t, map[string]string{sceneUUIDA: "Some Scene"}))
	prober := &fakeProber{byPath: map[string]*mediainfo.Probe{
		trackedFile: {CodecName: "h264", Width: 1280, Height: 720, BitRate: 3000},
		orphanFile:  {CodecName: "h265", Width: 1920, Height: 1080, BitRate: 8000},
	}}

	got, err := ScanLibraryAdult(context.Background(), sess, libStore, dir, prober, matchingPHasher(trackedFile, orphanFile), 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected exactly 1 duplicate group, got %d: %+v", len(got), got)
	}
	p := got[0]
	if p.Status != proposals.Pending || p.Mode != mode.Adult || len(p.Candidates) != 2 {
		t.Fatalf("unexpected proposal: %+v", p)
	}
	if p.GiveBackBox != "stashdb" || p.GiveBackSceneID != sceneUUIDA {
		t.Errorf("expected the scene identity carried on GiveBackBox/GiveBackSceneID, got box=%q sceneID=%q", p.GiveBackBox, p.GiveBackSceneID)
	}
	if p.TMDBID != 0 {
		t.Errorf("expected no TMDBID on an Adult proposal, got %d", p.TMDBID)
	}

	var winner, loser proposals.Candidate
	for _, c := range p.Candidates {
		if c.Winner {
			winner = c
		} else {
			loser = c
		}
	}
	if winner.Path != orphanFile {
		t.Errorf("expected the higher-quality orphan to win, got winner=%+v", winner)
	}
	if loser.Path != trackedFile || loser.TrackedID != int(tracked.ID) {
		t.Errorf("expected the tracked file to be the loser, got %+v", loser)
	}
}

func TestScanLibraryAdult_SingleNewOrphanIsNotADuplicate(t *testing.T) {
	dir := t.TempDir()
	orphanName := "Some.Scene." + sceneUUIDA
	writeVideoFile(t, filepath.Join(dir, orphanName), "scene.mkv", 100)

	sess := newAdultLibraryScanSession(t, fakeStashboxByID(t, map[string]string{sceneUUIDA: "Some Scene"}))

	got, err := ScanLibraryAdult(context.Background(), sess, newTestLibraryStore(t), dir, &fakeProber{}, &fakePHasher{}, 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected no duplicate groups for a single new scene, got %+v", got)
	}
}

// TestScanLibraryAdult_PHashCacheHitAvoidsRehashingTrackedFile proves the
// decode-once win: a tracked scene whose cached PHash + file identity
// (size+mtime) still match is NOT re-hashed, while the orphan is hashed fresh.
func TestScanLibraryAdult_PHashCacheHitAvoidsRehashingTrackedFile(t *testing.T) {
	dir := t.TempDir()
	trackedFile := writeVideoFile(t, filepath.Join(dir, "Some Scene"), "scene.mkv", 100)
	orphanName := "Some.Scene." + sceneUUIDA
	orphanFile := writeVideoFile(t, filepath.Join(dir, orphanName), "scene.mkv", 100)

	// The cache is trusted only if the tracked scene's stored PHash carries the
	// scheme prefix AND its stored size+mtime exactly match the file on disk.
	fi, err := os.Stat(trackedFile)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	cachedHash := "phash64/5f:" + strings.Repeat("0", 80)

	libStore := newTestLibraryStore(t)
	if _, err := libStore.UpsertScene(context.Background(), library.Scene{
		Box: "stashdb", SceneID: sceneUUIDA, Title: "Some Scene", FilePath: trackedFile, RootFolderPath: dir,
		PHash: cachedHash, PHashFileSize: fi.Size(), PHashFileMTime: fi.ModTime().UTC().Format(time.RFC3339Nano),
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sess := newAdultLibraryScanSession(t, fakeStashboxByID(t, map[string]string{sceneUUIDA: "Some Scene"}))
	prober := &fakeProber{byPath: map[string]*mediainfo.Probe{
		trackedFile: {CodecName: "h264", Width: 1280, Height: 720, BitRate: 3000},
		orphanFile:  {CodecName: "h264", Width: 1280, Height: 720, BitRate: 3000},
	}}
	// The orphan hashes to the same cached value so the group stays a duplicate.
	hasher := &fakePHasher{byPath: map[string]string{orphanFile: cachedHash}}

	got, err := ScanLibraryAdult(context.Background(), sess, libStore, dir, prober, hasher, 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected exactly 1 duplicate group, got %+v", got)
	}
	if hasher.calls[trackedFile] != 0 {
		t.Errorf("expected the tracked file's cached hash to be reused (0 hash calls), got %d", hasher.calls[trackedFile])
	}
	if hasher.calls[orphanFile] != 1 {
		t.Errorf("expected the orphan to be hashed exactly once, got %d", hasher.calls[orphanFile])
	}
}

// TestScanLibraryAdult_PHashCacheMissComputesAndCaches proves that when a
// tracked scene has no cached hash, ScanLibraryAdult hashes it fresh AND writes
// the result back via UpdateScenePHash for the next Scan.
func TestScanLibraryAdult_PHashCacheMissComputesAndCaches(t *testing.T) {
	dir := t.TempDir()
	trackedFile := writeVideoFile(t, filepath.Join(dir, "Some Scene"), "scene.mkv", 100)
	orphanName := "Some.Scene." + sceneUUIDA
	orphanFile := writeVideoFile(t, filepath.Join(dir, orphanName), "scene.mkv", 100)

	libStore := newTestLibraryStore(t)
	tracked, err := libStore.UpsertScene(context.Background(), library.Scene{
		Box: "stashdb", SceneID: sceneUUIDA, Title: "Some Scene", FilePath: trackedFile, RootFolderPath: dir,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tracked.PHash != "" {
		t.Fatalf("expected the tracked scene to start with no cached phash, got %q", tracked.PHash)
	}

	sess := newAdultLibraryScanSession(t, fakeStashboxByID(t, map[string]string{sceneUUIDA: "Some Scene"}))
	prober := &fakeProber{byPath: map[string]*mediainfo.Probe{
		trackedFile: {CodecName: "h264", Width: 1280, Height: 720, BitRate: 3000},
		orphanFile:  {CodecName: "h264", Width: 1280, Height: 720, BitRate: 3000},
	}}
	hasher := matchingPHasher(trackedFile, orphanFile)

	got, err := ScanLibraryAdult(context.Background(), sess, libStore, dir, prober, hasher, 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected exactly 1 duplicate group, got %+v", got)
	}
	if hasher.calls[trackedFile] != 1 {
		t.Errorf("expected a cache miss to hash the tracked file once, got %d", hasher.calls[trackedFile])
	}
	// The freshly computed hash must have been written back onto the tracked row.
	reloaded, err := libStore.GetScene(context.Background(), "stashdb", sceneUUIDA)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if reloaded.PHash == "" {
		t.Error("expected the freshly computed hash to be cached back onto the tracked scene via UpdateScenePHash")
	}
}

func TestApplyLibraryAdult_KeepsWinnerByDefault_DeletesOrphanLoser(t *testing.T) {
	dir := t.TempDir()
	loserPath := writeVideoFile(t, dir, "loser.mkv", 10)

	libStore := newTestLibraryStore(t)
	tracked, err := libStore.UpsertScene(context.Background(), library.Scene{
		Box: "stashdb", SceneID: sceneUUIDA, Title: "X", FilePath: "/winner.mkv", RootFolderPath: dir,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	p := proposals.Proposal{
		ID: 1, Status: proposals.Pending, Title: "X",
		GiveBackBox: "stashdb", GiveBackSceneID: sceneUUIDA,
		Candidates: []proposals.Candidate{
			{Label: "winner", Path: "/winner.mkv", TrackedID: int(tracked.ID), Winner: true},
			{Label: "loser", Path: loserPath},
		},
	}
	id, changes, err := ApplyLibraryAdult(context.Background(), libStore, p, nil, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != tracked.ID {
		t.Errorf("expected the already-tracked winner's id (%d), got %d", tracked.ID, id)
	}
	if _, err := os.Stat(loserPath); !os.IsNotExist(err) {
		t.Error("expected the losing orphan file to be deleted")
	}
	if len(changes) != 1 || changes[0].Path != loserPath || changes[0].Kind != mode.Deleted {
		t.Errorf("expected exactly one Deleted PathChange for %q, got %+v", loserPath, changes)
	}
}

// TestApplyLibraryAdult_WinnerIsOrphan_DeletesTrackedLoserAndRegistersWinner
// proves the never-zero-tracked invariant: when an orphan wins, the tracked
// loser's file AND its library_scenes row are removed, and the winning orphan
// is registered as the new tracked scene via UpsertScene.
func TestApplyLibraryAdult_WinnerIsOrphan_DeletesTrackedLoserAndRegistersWinner(t *testing.T) {
	dir := t.TempDir()
	trackedFile := writeVideoFile(t, dir, "tracked.mkv", 10)
	winnerPath := writeVideoFile(t, dir, "winner.mkv", 10)

	libStore := newTestLibraryStore(t)
	tracked, err := libStore.UpsertScene(context.Background(), library.Scene{
		Box: "stashdb", SceneID: sceneUUIDA, Title: "Some Scene", Studio: "Some Studio",
		FilePath: trackedFile, RootFolderPath: dir,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	p := proposals.Proposal{
		ID: 1, Status: proposals.Pending, Title: "Some Scene", Studio: "Some Studio",
		GiveBackBox: "stashdb", GiveBackSceneID: sceneUUIDA, RootFolderPath: dir,
		Candidates: []proposals.Candidate{
			{Label: "tracked", Path: trackedFile, TrackedID: int(tracked.ID)},
			{Label: "winner", Path: winnerPath, Winner: true},
		},
	}
	id, changes, err := ApplyLibraryAdult(context.Background(), libStore, p, nil, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id == 0 {
		t.Error("expected a nonzero scene id for the newly registered winner")
	}
	if _, err := os.Stat(trackedFile); !os.IsNotExist(err) {
		t.Error("expected the losing tracked file to be deleted")
	}
	// The tracked loser's row is gone; the (box, scene_id) key now points at the
	// winner's file (UpsertScene overwrote it) with the same id it just deleted?
	// No — DeleteScene removed the old row, UpsertScene inserted a fresh one for
	// the same key. Either way exactly one row exists for the key, behind the
	// winner's file.
	scene, err := libStore.GetScene(context.Background(), "stashdb", sceneUUIDA)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if scene.FilePath != winnerPath || scene.ID != id {
		t.Errorf("expected the registered scene to point at the winner (%q, id=%d), got %+v", winnerPath, id, scene)
	}
	if len(changes) != 1 || changes[0].Path != trackedFile || changes[0].Kind != mode.Deleted {
		t.Errorf("expected exactly one Deleted PathChange for %q, got %+v", trackedFile, changes)
	}
}

func TestApplyLibraryAdult_KeepAll_NoMutation(t *testing.T) {
	dir := t.TempDir()
	loserPath := writeVideoFile(t, dir, "b.mkv", 10)

	libStore := newTestLibraryStore(t)
	tracked, err := libStore.UpsertScene(context.Background(), library.Scene{
		Box: "stashdb", SceneID: sceneUUIDA, Title: "X", FilePath: "/a.mkv", RootFolderPath: dir,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	p := proposals.Proposal{
		ID: 1, Status: proposals.Pending,
		Candidates: []proposals.Candidate{
			{Label: "a", Path: "/a.mkv", TrackedID: int(tracked.ID)},
			{Label: "b", Path: loserPath},
		},
	}
	id, changes, err := ApplyLibraryAdult(context.Background(), libStore, p, nil, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != tracked.ID {
		t.Errorf("expected keepAll to still report the existing tracked id, got %d", id)
	}
	if _, err := os.Stat(loserPath); err != nil {
		t.Errorf("expected keepAll to leave every file untouched, got %v", err)
	}
	if len(changes) != 0 {
		t.Errorf("expected keepAll to report zero PathChanges, got %+v", changes)
	}
}

func TestApplyLibraryAdult_RejectsNonPendingProposal(t *testing.T) {
	libStore := newTestLibraryStore(t)
	p := proposals.Proposal{
		Status:     proposals.Applied,
		Candidates: []proposals.Candidate{{Path: "/a.mkv"}, {Path: "/b.mkv"}},
	}
	if _, _, err := ApplyLibraryAdult(context.Background(), libStore, p, nil, false); err == nil {
		t.Fatal("expected ApplyLibraryAdult to refuse an already-applied proposal")
	}
}

func TestApplyLibraryAdult_RejectsFewerThanTwoCandidates(t *testing.T) {
	libStore := newTestLibraryStore(t)
	p := proposals.Proposal{Status: proposals.Pending, Candidates: []proposals.Candidate{{Path: "/a.mkv"}}}
	if _, _, err := ApplyLibraryAdult(context.Background(), libStore, p, nil, false); err == nil {
		t.Fatal("expected ApplyLibraryAdult to refuse a proposal with fewer than 2 candidates")
	}
}

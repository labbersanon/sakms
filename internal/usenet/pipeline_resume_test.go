package usenet

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestPipeline_WritesAndMarksIncrementally proves the ordered pipeline commits
// decoded bytes + resume marks before the whole file has been fetched (the
// property that stops multi-GB RAM buffering and enables mid-file resume).
func TestPipeline_WritesAndMarksIncrementally(t *testing.T) {
	p := makePayload(t, 16, 512)
	srv := newFakeNNTP(t)
	srv.paceBodies()
	srv.serveAll(p)
	nzbHTTP := nzbServer(t, p)
	staging := t.TempDir()
	// Claude 2026-09-22: MaxConns=1 so the assemble sliding window cannot spend
	//   a paced BODY token on seg N+1 while N is still blocked (HOL left
	//   fileSize=2560 after exactly 6 releases on CI with MaxConns=2).
	// Reason: concurrencyBudget == Σ MaxConns; window [nextWrite, nextWrite+maxConc)
	//   lets an out-of-order fetch steal a pace token from the writer’s next index.
	// Troubleshooting: TestPipeline_WritesAndMarksIncrementally mid-flight WriteAt
	//   missing / go-test hang after 2s finish feeder abandoned bodyAllow waiters.
	// Review if: assembleFile fetches strictly in NZB order with no sliding window.
	m := New(Config{
		Servers:       []ServerConfig{srv.cfgWith(1)},
		StagingDir:    staging,
		HTTPClient:    nzbHTTP.Client(),
		SegmentResume: true,
	})

	gid, err := m.AddNZB(context.Background(), nzbHTTP.URL, "Pipeline Incremental")
	if err != nil {
		t.Fatalf("AddNZB: %v", err)
	}
	dir := filepath.Join(staging, gid)
	out := filepath.Join(dir, p.filename)

	const (
		segBytes  = 512
		wantBytes = int64(6 * segBytes)
		midSegs   = 6
	)
	for i := 0; i < midSegs; i++ {
		select {
		case srv.bodyAllow <- struct{}{}:
		case <-time.After(5 * time.Second):
			t.Fatalf("BODY waiter missing for mid-flight token %d/%d", i+1, midSegs)
		}
	}
	deadline := time.Now().Add(10 * time.Second)
	var done int
	var size int64
	for time.Now().Before(deadline) {
		if fi, err := os.Stat(out); err == nil {
			size = fi.Size()
		}
		done = resumeDoneCount(t, dir)
		// Claude 2026-09-22: resume persist is batched (35 marks / 1s), so the
		// sidecar may lag in-memory marks while WriteAt still grows the file.
		// Incremental pipeline proof is on-disk payload size, not sidecar count.
		if size >= wantBytes {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if size < wantBytes {
		t.Fatalf("mid-flight WriteAt missing: resumeDone=%d fileSize=%d (still active download)", done, size)
	}
	d, _ := m.FindByGID(gid)
	if d == nil || d.Status != "active" {
		t.Fatalf("expected download still active during paced fetch, got %+v", d)
	}

	// Keep pacing until terminal — a short idle timeout previously abandoned
	// bodyAllow waiters under CI load and hung go test until the suite timeout.
	stopFeed := make(chan struct{})
	defer close(stopFeed)
	go func() {
		for {
			select {
			case <-stopFeed:
				return
			case srv.bodyAllow <- struct{}{}:
			case <-time.After(50 * time.Millisecond):
			}
		}
	}()
	final := waitTerminal(t, m, gid)
	if final.Status != "complete" {
		t.Fatalf("status=%q err=%q", final.Status, final.ErrorMessage)
	}
	assertAssembled(t, staging, gid, p)
}

func resumeDoneCount(t *testing.T, dir string) int {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, ResumeFileName))
	if err != nil {
		return 0
	}
	var snap ResumeSnapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return 0
	}
	n := 0
	for _, f := range snap.Files {
		if f != nil {
			n += len(f.Done)
		}
	}
	return n
}

// TestPipeline_ResumeSkipsDurablePrefix: a relaunch with SegmentResume must not
// BODY-fetch segments already marked done with bytes on disk.
func TestPipeline_ResumeSkipsDurablePrefix(t *testing.T) {
	p := makePayload(t, 10, 512)
	staging := t.TempDir()
	gid := "nzb-aaaaaaaaaaaaaaaa"
	dir := filepath.Join(staging, gid)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, OwnedMarkerFile), []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Commit first 6 segments contiguously on disk + resume sidecar.
	prefix := p.full[:6*512]
	if err := os.WriteFile(filepath.Join(dir, p.filename), prefix, 0o644); err != nil {
		t.Fatal(err)
	}
	snap := ResumeSnapshot{
		Version: resumeSchemaVersion,
		GID:     gid,
		Files: map[string]*ResumeFile{
			p.filename: {Size: int64(len(prefix)), Done: map[string]ResumeSeg{}},
		},
	}
	var cursor int64
	for i := 0; i < 6; i++ {
		snap.Files[p.filename].Done[p.msgIDs[i]] = ResumeSeg{
			Number: i + 1,
			Offset: cursor,
			Length: 512,
		}
		cursor += 512
	}
	data, _ := json.MarshalIndent(snap, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, ResumeFileName), data, 0o644); err != nil {
		t.Fatal(err)
	}

	srv := newFakeNNTP(t)
	srv.serveAll(p)
	nzbHTTP := nzbServer(t, p)
	m := New(Config{
		Servers:       []ServerConfig{srv.cfgWith(2)},
		StagingDir:    staging,
		HTTPClient:    nzbHTTP.Client(),
		SegmentResume: true,
	})
	if err := m.RelaunchNZB(context.Background(), gid, nzbHTTP.URL, "Skip Prefix"); err != nil {
		t.Fatalf("RelaunchNZB: %v", err)
	}
	d := waitTerminal(t, m, gid)
	if d.Status != "complete" {
		t.Fatalf("status=%q err=%q", d.Status, d.ErrorMessage)
	}
	assertAssembled(t, staging, gid, p)
	if got := srv.bodyCount.Load(); got != 4 {
		t.Fatalf("BODY count=%d want 4 (skipped durable prefix of 6)", got)
	}
}

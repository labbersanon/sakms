package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/labbersanon/sakms/internal/dedupscan"
	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/proposals"
)

// newVMAFTestMux wires a real migrated DB + real handlers, returning the server
// plus the two stores the VMAF endpoint reads/writes. The prober/hasher fakes
// are irrelevant to this endpoint but are what NewMux expects.
func newVMAFTestMux(t *testing.T) (*httptest.Server, *proposals.Store, *library.Store) {
	t.Helper()
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), nil, rssFeedsStore, nil, nil, nil, nil, dedupscan.New(), nil))
	t.Cleanup(srv.Close)
	return srv, propStore, libStore
}

func insertProposal(t *testing.T, propStore *proposals.Store, m mode.Mode, wf proposals.Workflow, cands []proposals.Candidate) proposals.Proposal {
	t.Helper()
	saved, err := propStore.ReplacePending(context.Background(), m, wf, []proposals.Proposal{{
		Status:     proposals.Pending,
		SourceName: "group",
		SourcePath: "/library/group",
		Candidates: cands,
	}})
	if err != nil {
		t.Fatalf("inserting %s proposal: %v", wf, err)
	}
	return saved[0]
}

func getVMAF(t *testing.T, srv *httptest.Server, m string, id int64, candidateIndex, referenceIndex int) (int, vmafScoreResponse) {
	t.Helper()
	url := srv.URL + "/api/modes/" + m + "/dedup/proposals/" + strconv.FormatInt(id, 10) +
		"/vmaf?candidateIndex=" + strconv.Itoa(candidateIndex) + "&referenceIndex=" + strconv.Itoa(referenceIndex)
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET vmaf: %v", err)
	}
	defer resp.Body.Close()
	var out vmafScoreResponse
	// error responses are plain text, not JSON — decode is best-effort.
	json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func TestVMAFHandler_Validation(t *testing.T) {
	srv, propStore, _ := newVMAFTestMux(t)
	dedup := insertProposal(t, propStore, mode.Movies, proposals.Dedup, []proposals.Candidate{
		{Label: "a", Path: "/a.mkv"}, {Label: "b", Path: "/b.mkv"},
	})
	rename := insertProposal(t, propStore, mode.Movies, proposals.Rename, nil)

	tests := []struct {
		name string
		path string
		want int
	}{
		{"unknown mode", "/api/modes/bogus/dedup/proposals/" + propID(dedup) + "/vmaf?candidateIndex=0&referenceIndex=1", http.StatusBadRequest},
		{"non-numeric id", "/api/modes/movies/dedup/proposals/abc/vmaf?candidateIndex=0&referenceIndex=1", http.StatusBadRequest},
		{"missing indices", "/api/modes/movies/dedup/proposals/" + propID(dedup) + "/vmaf", http.StatusBadRequest},
		{"equal indices", "/api/modes/movies/dedup/proposals/" + propID(dedup) + "/vmaf?candidateIndex=1&referenceIndex=1", http.StatusBadRequest},
		{"proposal not found", "/api/modes/movies/dedup/proposals/999999/vmaf?candidateIndex=0&referenceIndex=1", http.StatusNotFound},
		{"index out of range", "/api/modes/movies/dedup/proposals/" + propID(dedup) + "/vmaf?candidateIndex=0&referenceIndex=5", http.StatusBadRequest},
		{"not a dedup proposal", "/api/modes/movies/dedup/proposals/" + propID(rename) + "/vmaf?candidateIndex=0&referenceIndex=1", http.StatusBadRequest},
		{"mode mismatch", "/api/modes/series/dedup/proposals/" + propID(dedup) + "/vmaf?candidateIndex=0&referenceIndex=1", http.StatusNotFound},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := http.Get(srv.URL + tc.path)
			if err != nil {
				t.Fatalf("GET: %v", err)
			}
			resp.Body.Close()
			if resp.StatusCode != tc.want {
				t.Errorf("got %d, want %d", resp.StatusCode, tc.want)
			}
		})
	}
}

func TestVMAFHandler_CacheHit(t *testing.T) {
	srv, propStore, libStore := newVMAFTestMux(t)
	dir := t.TempDir()
	candPath := filepath.Join(dir, "candidate.mkv")
	refPath := filepath.Join(dir, "reference.mkv")
	if err := os.WriteFile(candPath, []byte("candidate-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}

	dedup := insertProposal(t, propStore, mode.Movies, proposals.Dedup, []proposals.Candidate{
		{Label: "cand", Path: candPath}, {Label: "primary", Path: refPath},
	})

	// Seed the cache exactly as a real compute would, with the candidate file's
	// current identity so GetValidVMAFScore reports a hit.
	size, mtime, err := library.VMAFFileIdentity(candPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := libStore.UpsertVMAFScore(context.Background(), library.VMAFScore{
		CandidatePath: candPath, CandidateFileSize: size, CandidateFileMTime: mtime,
		ReferencePath: refPath, Score: 87.5, ComputedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}); err != nil {
		t.Fatal(err)
	}

	// A cache hit must never shell out to ffmpeg — fail loudly if it does.
	restore := vmafCompute
	vmafCompute = func(ctx context.Context, a, b string) (float64, error) {
		t.Errorf("cache hit should not compute; computed %s vs %s", a, b)
		return 0, nil
	}
	defer func() { vmafCompute = restore }()

	status, body := getVMAF(t, srv, "movies", dedup.ID, 0, 1)
	if status != http.StatusOK {
		t.Fatalf("got %d, want 200", status)
	}
	if body.Status != "ready" || !body.Cached || body.Score != 87.5 {
		t.Fatalf("unexpected cache-hit response: %+v", body)
	}
}

func TestVMAFHandler_MissComputesAndCachesAndDedupesInflight(t *testing.T) {
	srv, propStore, libStore := newVMAFTestMux(t)
	dir := t.TempDir()
	candPath := filepath.Join(dir, "candidate.mkv")
	refPath := filepath.Join(dir, "reference.mkv")
	if err := os.WriteFile(candPath, []byte("candidate-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	dedup := insertProposal(t, propStore, mode.Movies, proposals.Dedup, []proposals.Candidate{
		{Label: "cand", Path: candPath}, {Label: "primary", Path: refPath},
	})

	// A controllable stub: block until released, count invocations. If the
	// in-flight guard works, two concurrent requests for the same pair produce
	// exactly ONE compute call.
	release := make(chan struct{})
	var calls atomic.Int32
	restore := vmafCompute
	vmafCompute = func(ctx context.Context, a, b string) (float64, error) {
		calls.Add(1)
		<-release
		return 91.25, nil
	}
	defer func() { vmafCompute = restore }()

	// First miss → background compute starts, 202 computing.
	if status, body := getVMAF(t, srv, "movies", dedup.ID, 0, 1); status != http.StatusAccepted || body.Status != "computing" {
		t.Fatalf("first miss: got %d/%q, want 202/computing", status, body.Status)
	}
	// Second request while the first is still computing → still 202 computing,
	// and crucially NO second compute (in-flight dedup, not just the semaphore).
	if status, body := getVMAF(t, srv, "movies", dedup.ID, 0, 1); status != http.StatusAccepted || body.Status != "computing" {
		t.Fatalf("in-flight request: got %d/%q, want 202/computing", status, body.Status)
	}

	close(release) // let the one computation finish and cache its result

	// Poll until the cached score is served.
	var final vmafScoreResponse
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		status, body := getVMAF(t, srv, "movies", dedup.ID, 0, 1)
		if status == http.StatusOK && body.Status == "ready" {
			final = body
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if final.Status != "ready" || final.Score != 91.25 || !final.Cached {
		t.Fatalf("expected a cached ready score after compute, got %+v", final)
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("expected exactly 1 compute for two concurrent same-pair requests (in-flight dedup), got %d", n)
	}
	// The score is actually persisted in vmaf_scores.
	if got, err := libStore.GetVMAFScore(context.Background(), candPath, refPath); err != nil || got.Score != 91.25 {
		t.Errorf("expected persisted score 91.25, got %+v err=%v", got, err)
	}
}

func TestVMAFHandler_ComputeErrorSurfacesAsError(t *testing.T) {
	srv, propStore, _ := newVMAFTestMux(t)
	dedup := insertProposal(t, propStore, mode.Movies, proposals.Dedup, []proposals.Candidate{
		{Label: "cand", Path: "/nonexistent/candidate.mkv"}, {Label: "primary", Path: "/nonexistent/reference.mkv"},
	})

	restore := vmafCompute
	vmafCompute = func(ctx context.Context, a, b string) (float64, error) {
		return 0, context.DeadlineExceeded
	}
	defer func() { vmafCompute = restore }()

	// First request kicks off the (failing) background compute.
	if status, _ := getVMAF(t, srv, "movies", dedup.ID, 0, 1); status != http.StatusAccepted {
		t.Fatalf("first request: got %d, want 202", status)
	}
	// Poll until the failure is recorded and surfaced as an error status
	// (must NOT fall back to a re-triggering cache-miss loop).
	deadline := time.Now().Add(2 * time.Second)
	var got vmafScoreResponse
	for time.Now().Before(deadline) {
		status, body := getVMAF(t, srv, "movies", dedup.ID, 0, 1)
		if status == http.StatusOK && body.Status == "error" {
			got = body
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got.Status != "error" || got.Error == "" {
		t.Fatalf("expected an error status with a message, got %+v", got)
	}
}

func propID(p proposals.Proposal) string { return strconv.FormatInt(p.ID, 10) }

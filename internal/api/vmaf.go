package api

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/proposals"
	"github.com/labbersanon/sakms/internal/settings"
	"github.com/labbersanon/sakms/internal/vmaf"
)

// vmafScoreResponse is the on-demand VMAF endpoint's poll-shaped response. The
// endpoint is asynchronous (mirroring dedupScanHandler on the same screen): a
// single VMAF computation is a full two-file decode that can run for minutes,
// far past any reverse-proxy read timeout, and viewing one Dedup group fires
// N-1 of them (star topology, AC1) — so a cache miss kicks off a background
// computation and returns "computing" immediately, and the frontend re-polls
// this same endpoint until it flips to "ready".
type vmafScoreResponse struct {
	// Status is one of "ready" (Score is populated), "computing" (a
	// computation for this exact pair is in flight — poll again), "skipped"
	// (the pair is not a phash match — VMAF is quality scoring, not duplicate
	// detection), or "error" (the last computation for this pair failed;
	// Error explains it).
	Status         string  `json:"status"`
	Score          float64 `json:"score,omitempty"`
	Cached         bool    `json:"cached,omitempty"`
	CandidateIndex int     `json:"candidateIndex"`
	ReferenceIndex int     `json:"referenceIndex"`
	Error          string  `json:"error,omitempty"`
}

// vmafCompute is the compute entry point the handler drives, defaulting to the
// real in-process ffmpeg+libvmaf path. It is a package var ONLY so handler
// tests can substitute a deterministic stub (controllable success/failure and
// timing), the same seam internal/vmaf exposes to its own tests — no test hook
// leaks into vmaf's public API.
var vmafCompute = vmaf.Compute

// vmafFailRetryAfter is how long a failed pair stays in the "error" state
// before a fresh request is allowed to re-trigger a computation. It bounds
// retriggering: without it, a permanently-failing pair (e.g. a production
// ffmpeg build with no libvmaf filter) would clear back to a cache-miss on
// every poll and start ffmpeg endlessly.
const vmafFailRetryAfter = 60 * time.Second

// vmafInflight dedupes concurrent VMAF computations for the same
// (candidatePath, referencePath) pair across HTTP requests. The internal/vmaf
// semaphore caps TOTAL concurrency, but only this map stops the SAME pair
// being computed several times at once (many browser tabs / a re-render
// re-fetching a still-computing slot). It also carries a terminal error so a
// genuinely-failing pair surfaces as "error" instead of silently falling back
// to a cache-miss and re-triggering forever (see vmafFailRetryAfter).
type vmafInflight struct {
	mu    sync.Mutex
	state map[string]*vmafPairState
}

type vmafPairState struct {
	done     bool      // false while the background computation is still running
	err      error     // non-nil once a finished computation failed
	failedAt time.Time // when err was recorded, for vmafFailRetryAfter backoff
}

func pairKey(candidatePath, referencePath string) string {
	return candidatePath + "\x00" + referencePath
}

// vmafHandler serves GET /api/modes/{mode}/dedup/proposals/{id}/vmaf?
// candidateIndex=N&referenceIndex=M. It scores the proposal's candidate at
// index N against the candidate at index M (the group's VMAF reference —
// the largest on-disk file, sent by the frontend independently of Keep-primary),
// but only when the pair is a phash match (vmaf.ShouldScoreVMAF): identity-only
// TMDB groups return status "skipped" without ffmpeg. A matching pair checks the
// vmaf_scores cache first (AC2) and computes+caches on a miss (AC1). Scoring a
// candidate against itself is rejected (candidateIndex == referenceIndex).
//
// The handler stays index-generic: it does not re-pick the largest file. The
// frontend (and eager scan) must send the size-based reference so cache keys
// match. Changing Keep-primary must not change referenceIndex.
//
// inflight is constructed once here (not per-request) so it is shared across
// every request this handler serves.
func vmafHandler(propStore *proposals.Store, libStore *library.Store, settingsStore *settings.Store) http.HandlerFunc {
	inflight := &vmafInflight{state: map[string]*vmafPairState{}}
	return func(w http.ResponseWriter, r *http.Request) {
		m := mode.Mode(r.PathValue("mode"))
		if m != mode.Movies && m != mode.Series && m != mode.Adult {
			http.Error(w, "unknown mode", http.StatusBadRequest)
			return
		}

		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.Error(w, "invalid proposal id", http.StatusBadRequest)
			return
		}
		candidateIndex, cErr := strconv.Atoi(r.URL.Query().Get("candidateIndex"))
		referenceIndex, rErr := strconv.Atoi(r.URL.Query().Get("referenceIndex"))
		if cErr != nil || rErr != nil {
			http.Error(w, "candidateIndex and referenceIndex are required integers", http.StatusBadRequest)
			return
		}
		if candidateIndex == referenceIndex {
			http.Error(w, "a candidate cannot be scored against itself", http.StatusBadRequest)
			return
		}

		ctx := r.Context()
		prop, err := propStore.Get(ctx, id)
		if err != nil {
			if errors.Is(err, proposals.ErrNotFound) {
				http.Error(w, "no proposal with that id", http.StatusNotFound)
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if prop.Workflow != proposals.Dedup {
			http.Error(w, "not a dedup proposal", http.StatusBadRequest)
			return
		}
		if prop.Mode != m {
			http.Error(w, "proposal does not belong to that mode", http.StatusNotFound)
			return
		}
		if !inRange(candidateIndex, len(prop.Candidates)) || !inRange(referenceIndex, len(prop.Candidates)) {
			http.Error(w, "candidateIndex or referenceIndex out of range for this proposal", http.StatusBadRequest)
			return
		}

		candidate := prop.Candidates[candidateIndex]
		reference := prop.Candidates[referenceIndex]
		threshold, tErr := resolvePHashThreshold(ctx, settingsStore, m)
		if tErr != nil {
			http.Error(w, tErr.Error(), http.StatusInternalServerError)
			return
		}
		// Claude 2026-08-21: this gate runs BEFORE the cache read.
		// Reason: TMDB-identity groups (crop vs open-matte) are not the same
		// picture, and a score cached before the gate existed must still
		// report skipped rather than be served back.
		// Troubleshooting: Last Crusade-style tiles still running ffmpeg means
		// the gate moved below cache/compute, or candidates_json lacks PHash.
		// Review if: Dedup stops grouping by TMDB identity.
		if !vmaf.ShouldScoreVMAF(candidate.PHash, reference.PHash, threshold) {
			writeJSON(w, vmafScoreResponse{
				Status:         "skipped",
				CandidateIndex: candidateIndex,
				ReferenceIndex: referenceIndex,
			})
			return
		}

		candidatePath := candidate.Path
		referencePath := reference.Path

		// 1. Cache first (AC2): a valid cached score serves immediately, no
		//    ffmpeg. A successful (re)compute also drops any prior in-flight
		//    error entry, so an earlier failure never masks a now-cacheable
		//    score.
		if score, ok, err := libStore.GetValidVMAFScore(ctx, candidatePath, referencePath); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		} else if ok {
			inflight.clear(pairKey(candidatePath, referencePath))
			writeJSON(w, vmafScoreResponse{
				Status: "ready", Score: score, Cached: true,
				CandidateIndex: candidateIndex, ReferenceIndex: referenceIndex,
			})
			return
		}

		// 2. Cache miss: dedupe against any in-flight / recently-failed
		//    computation for this exact pair before starting another.
		status, failErr := inflight.begin(pairKey(candidatePath, referencePath), func(bgKey string) {
			go computeAndCacheVMAF(libStore, inflight, bgKey, candidatePath, referencePath)
		})
		switch status {
		case "computing":
			writeJSONStatus(w, http.StatusAccepted, vmafScoreResponse{
				Status:         "computing",
				CandidateIndex: candidateIndex, ReferenceIndex: referenceIndex,
			})
		default: // "error"
			writeJSON(w, vmafScoreResponse{
				Status: "error", Error: failErr,
				CandidateIndex: candidateIndex, ReferenceIndex: referenceIndex,
			})
		}
	}
}

// begin decides what to do about a cache-miss request for key and, when a new
// computation is warranted, invokes start(key) to launch it. It returns
// "computing" (a computation is now, or was already, in flight) or "error"
// (this pair failed within the last vmafFailRetryAfter — failErr is the
// message). It is the single mutex-guarded decision point.
func (v *vmafInflight) begin(key string, start func(key string)) (status, failErr string) {
	v.mu.Lock()
	defer v.mu.Unlock()

	if st, ok := v.state[key]; ok {
		switch {
		case !st.done:
			return "computing", ""
		case st.err != nil && time.Since(st.failedAt) < vmafFailRetryAfter:
			return "error", st.err.Error()
		}
		// Finished-and-failed past its backoff (or a stale finished entry):
		// fall through and re-trigger a fresh computation.
	}
	v.state[key] = &vmafPairState{}
	start(key)
	return "computing", ""
}

// finish records a completed background computation's outcome. On success the
// entry is removed (the next poll is a cache hit); on failure it is retained
// with the error so the pair reports "error" until vmafFailRetryAfter elapses.
func (v *vmafInflight) finish(key string, err error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if err == nil {
		delete(v.state, key)
		return
	}
	v.state[key] = &vmafPairState{done: true, err: err, failedAt: time.Now()}
}

// clear drops any entry for key — used after a cache hit so a stale error
// entry can't linger once the score is cacheable.
func (v *vmafInflight) clear(key string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	delete(v.state, key)
}

// computeAndCacheVMAF runs one VMAF computation in the background and caches a
// successful result. It uses context.Background() (not the originating
// request's ctx): the work outlives the request that triggered it, exactly
// like dedupScanHandler's background scan — internal/vmaf.Compute applies its
// own semaphore + timeout, so it can never hang unbounded.
func computeAndCacheVMAF(libStore *library.Store, inflight *vmafInflight, key, candidatePath, referencePath string) {
	ctx := context.Background()
	score, err := vmafCompute(ctx, candidatePath, referencePath)
	if err != nil {
		log.Printf("vmaf: computing %s vs %s: %v", candidatePath, referencePath, err)
		inflight.finish(key, err)
		return
	}
	size, mtime, idErr := library.VMAFFileIdentity(candidatePath)
	if idErr != nil {
		// The file scored but vanished/changed before we could stamp its
		// identity — treat as a failed compute (nothing safe to cache).
		log.Printf("vmaf: stamping identity for %s: %v", candidatePath, idErr)
		inflight.finish(key, idErr)
		return
	}
	if err := libStore.UpsertVMAFScore(ctx, library.VMAFScore{
		CandidatePath:      candidatePath,
		CandidateFileSize:  size,
		CandidateFileMTime: mtime,
		ReferencePath:      referencePath,
		Score:              score,
		ComputedAt:         time.Now().UTC().Format(time.RFC3339Nano),
	}); err != nil {
		log.Printf("vmaf: caching score for %s vs %s: %v", candidatePath, referencePath, err)
		inflight.finish(key, err)
		return
	}
	inflight.finish(key, nil)
}

func inRange(i, n int) bool { return i >= 0 && i < n }

// writeJSONStatus writes v as JSON with an explicit status code (writeJSON
// always implies 200). Content-Type must be set before WriteHeader.
func writeJSONStatus(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

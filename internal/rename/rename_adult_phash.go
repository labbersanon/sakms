package rename

import (
	"context"
	"sync"

	"github.com/labbersanon/sakms/internal/identify"
	"github.com/labbersanon/sakms/internal/mediainfo"
	"github.com/labbersanon/sakms/internal/mode"
)

// adultHashWorkers bounds how many files identifyAdultFiles hashes+probes
// concurrently. Each Hash shells out ~25 ffmpeg frame extractions, so an
// unbounded fan-out on a large Adult library would thrash the host; a fixed
// small pool caps concurrent ffmpeg processes at 4 while still finishing far
// faster than a strictly sequential decode. Per-file wall time is already
// bounded by videophash's own internal ~2min timeout, so no Scan-wide deadline
// is imposed here (a global cutoff would wrongly dump still-unhashed files to
// the slower legacy path on a legitimately large library — see the impl plan).
const adultHashWorkers = 4

// PHasher computes a file's StashDB-compatible perceptual hash. A rename-local
// structural interface (satisfied by *videophash.Hasher) so this package never
// imports internal/videophash — the same seam pattern internal/dedup uses for
// its own injected hasher.
type PHasher interface {
	Hash(ctx context.Context, path string) (string, error)
}

// Prober reads a file's duration (among other fields) directly off disk.
// Structural, satisfied by *mediainfo.Prober, so give-back's DurationSeconds is
// sourced locally rather than from a live Stash read.
type Prober interface {
	Probe(ctx context.Context, path string) (*mediainfo.Probe, error)
}

// hashResult holds one candidate's locally-computed identification inputs.
// ok is false when the file couldn't be hashed at all — that candidate then
// degrades to the legacy AI/text pipeline on its own, never failing the batch.
type hashResult struct {
	phash    string
	duration int
	ok       bool
}

// adultFileID names one file to run through the phash-first identification
// cascade: path is hashed+probed locally, and (stem, parentName) feed the
// legacy AI/text Identify fallback used for a fingerprint-cascade miss.
type adultFileID struct {
	path       string
	stem       string
	parentName string
}

// adultIdentification is the resolved identity for one adultFileID: the
// MatchResult (nil if nothing resolved it), any error from the legacy Identify
// fallback, and the locally-computed phash/duration. hashed is false when the
// file couldn't be hashed at all — that file degraded straight to the legacy
// pipeline and carries no phash (so give-back and the filename tag are skipped
// for it downstream). The library-backed ScanLibraryAdult builds proposals from
// this one shape, so the phash-first-then-Identify cascade lives in exactly one
// place.
type adultIdentification struct {
	match    *identify.MatchResult
	err      error
	phash    string
	duration int
	hashed   bool
}

// identifyAdultFiles runs the phash-first cascade over files: a bounded
// concurrent local hash+probe phase, one batched StashDB->FansDB->TPDB
// fingerprint lookup, then the legacy AI/text Identify fallback for anything
// the cascade couldn't resolve. Shared by the library-backed path
// (ScanLibraryAdult) so the cascade lives in exactly one place — see
// rename_adult_library.go. Output is candidate-index order.
//
// Concurrency/fail-open semantics are unchanged from the original inline
// implementation: each goroutine writes only its own results[i] (no shared
// map, no mutex), a file that fails to hash falls open to the legacy pipeline
// for THAT file only, a file that hashes but fails to probe carries duration 0
// (give-back fails open for it), and a LookupFingerprints error fails the
// whole batch open into the legacy fallback while every file still keeps its
// local phash. sess.Identify is guaranteed non-nil by every caller.
func identifyAdultFiles(ctx context.Context, sess *mode.Session, hasher PHasher, prober Prober, files []adultFileID) []adultIdentification {
	results := make([]hashResult, len(files))
	sem := make(chan struct{}, adultHashWorkers)
	var wg sync.WaitGroup
	for i := range files {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, path string) {
			defer wg.Done()
			defer func() { <-sem }()
			h, err := hasher.Hash(ctx, path)
			if err != nil {
				return // ok stays false -> this file routes to legacy
			}
			r := hashResult{phash: h, ok: true}
			if pr, perr := prober.Probe(ctx, path); perr == nil {
				// float64 seconds -> int, matching the old int(f.Duration).
				r.duration = int(pr.Duration)
			}
			results[i] = r
		}(i, files[i].path)
	}
	wg.Wait()

	var phashes []string
	for i := range files {
		if results[i].ok {
			phashes = append(phashes, results[i].phash)
		}
	}

	matches, err := sess.Identify.LookupFingerprints(ctx, phashes)
	if err != nil {
		matches = nil // fail open: everything falls back, but still carries its local phash
	}

	out := make([]adultIdentification, len(files))
	for i, f := range files {
		r := results[i]
		id := adultIdentification{phash: r.phash, duration: r.duration, hashed: r.ok}
		if match, hit := matches[r.phash]; r.ok && hit {
			id.match = match
		} else {
			id.match, id.err = sess.Identify.Identify(ctx, f.stem, f.parentName)
		}
		out[i] = id
	}
	return out
}

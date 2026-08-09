package api

import (
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"

	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/proposals"
)

// proposalVideoHandler serves GET /api/modes/{mode}/proposals/{id}/video —
// the raw video bytes of one proposal's source file, for the card view's
// click-to-play preview. It is workflow-generic (Dedup, Rename, and any
// future workflow's proposals all resolve through the same handler — see
// proposalVideoPath's doc comment for the shape dispatch that makes this
// true). It resolves the file to serve entirely server-side: it looks the
// proposal up by {id} and, depending on the proposal's shape, either
// bounds-checks candidateIndex against len(p.Candidates) or falls back to
// p.SourcePath.
//
// Trust boundary is PROVENANCE, not lexical-root confinement. The path served
// is one SAK itself recorded during its own filesystem scan — it is never a
// client-supplied path. The client only ever supplies a proposal id and an
// integer index, both bounds-checked here; it cannot name a path, so there is
// no path-traversal surface to confine. No lexical-root check is
// layered on top: three such schemes (a browsableRoots allowlist, a live
// settings lookup, and the proposal's own scan-time RootFolderPath) were each
// tried and rejected across plan review rounds 1-4, because dedup's whole
// purpose is finding duplicates scattered across different directories, so no
// single root ever covers every candidate in a group — any root check
// spuriously 400s a legitimate sibling-directory candidate without adding
// real protection over the provenance check above (see
// .omc/plans/dedup-ux-refine.md pre-mortem #2).
//
// Symlinks are out of scope by design: the served paths are SAK's own recorded
// scan results (self-inflicted, not externally supplied), so there is no
// client-controlled symlink-traversal surface here — consistent with the
// existing resolveBrowsablePath precedent elsewhere in this codebase.
//
// Serving uses http.ServeContent against an os.Open handle so range/seek
// requests (video scrubbing) work and nothing is buffered in memory — unlike
// imageProxyHandler, which buffers whole external images and is the wrong
// template for a local video file.
//
// Shape dispatch: this handler never switches on p.Workflow. The proposals
// table has exactly two SHAPES — candidate-indexed (Dedup: len(p.Candidates)
// > 0) and single-source (Rename/Purge: no candidates) — and
// proposalVideoPath dispatches on that structural fact instead. A workflow
// switch would be an allowlist wearing a different hat and would need a
// backend change for every future workflow; dispatching on shape means a
// future workflow that grows a candidate array gets the indexed branch for
// free, and one that does not gets the single-source branch for free. See
// proposalVideoPath's own doc comment for the full reasoning.
//
// Security ordering: the Adult section-lock check
// (denyIfAdultLocked) runs IMMEDIATELY after propStore.Get resolves the row,
// BEFORE the mode-mismatch check and BEFORE proposalVideoPath is called. Both
// orderings are deliberate, not incidental:
//
//   - Before prop.Mode != m: a locked-Adult request for an Adult row addressed
//     through a Movies/Series path would otherwise get "proposal does not
//     belong to that mode" — which confirms the id exists and is not that
//     mode. Refusing first leaks nothing but "locked".
//   - Before proposalVideoPath: same reason, stronger. "candidateIndex out of
//     range" is a candidate-count oracle for an Adult duplicate group. A
//     locked section must not answer it.
//
// denyIfAdultLocked (Layer 3), not mode.Build (Layer 2), is used deliberately:
// this handler needs no session at all, and mode.Build would construct
// TMDB/Prowlarr/Stash clients to stream a local file. denyIfAdultLocked is
// exactly what dismissProposalHandler uses (internal/api/proposals.go) for the
// same reason — a handler whose Adult scope lives in a row rather than in the
// URL.
//
// Do not move this route to /api/proposals/{id}/video (no {mode} segment).
// That shape would strip Layer 1's Adult-content section-lock classification
// entirely — with no /modes/{mode}/ segment, classifyModes' adult rule cannot
// fire, and this handler's own denyIfAdultLocked check would become the ONLY
// protection instead of defense-in-depth. See .omc/plans/autopilot-impl-rename-preview.md
// §2.1 for the full route-shape analysis.
func proposalVideoHandler(propStore *proposals.Store) http.HandlerFunc {
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

		ctx := r.Context()
		prop, err := propStore.Get(ctx, id)
		if err != nil {
			// AC14(a): an unknown proposal id is an explicit client error (400),
			// never a silent empty response. The client picked a bad id.
			if errors.Is(err, proposals.ErrNotFound) {
				http.Error(w, "no proposal with that id", http.StatusBadRequest)
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		// SECURITY: Layer 3, FIRST thing after the row is known and BEFORE any
		// other answer is written. See the file doc comment.
		if prop.Mode == mode.Adult && denyIfAdultLocked(w, r) {
			return
		}

		if prop.Mode != m {
			http.Error(w, "proposal does not belong to that mode", http.StatusBadRequest)
			return
		}
		path, badReq := proposalVideoPath(*prop, r.URL.Query())
		if badReq != "" {
			http.Error(w, badReq, http.StatusBadRequest)
			return
		}

		f, err := os.Open(path)
		if err != nil {
			// A valid proposal+index whose file has vanished/changed on disk
			// since the scan — not a client error, and not tested by AC14.
			http.Error(w, "source file is not available", http.StatusInternalServerError)
			return
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil || info.IsDir() {
			http.Error(w, "source file is not available", http.StatusInternalServerError)
			return
		}

		// *os.File is an io.ReadSeeker, so ServeContent handles Range/If-Range
		// requests (seeking, 206 Partial Content) itself — no in-memory buffer.
		http.ServeContent(w, r, filepath.Base(path), info.ModTime(), f)
	}
}

// proposalVideoPath resolves WHICH file this request names, from the proposal's
// SHAPE rather than its workflow. Returns ("", msg) for a 400 with msg.
//
// Two shapes exist in the proposals table and this function is the whole
// difference between them:
//
//	candidate-indexed  (Dedup: len(p.Candidates) > 0)  -> p.Candidates[i].Path
//	single-source      (Rename/Purge: no candidates)   -> p.SourcePath
//
// Deliberately NOT a switch on p.Workflow: the spec requires any future
// workflow's proposal to be servable with no backend change, and a workflow
// switch is an allowlist wearing a different hat. A workflow that grows a
// candidate array gets the indexed branch automatically; one that does not gets
// the single-source branch automatically. Neither needs a line here.
//
// PRESENCE of the query parameter is the trigger, not its value: url.Values.Has
// distinguishes "?candidateIndex=" (present, empty -> 400, a malformed client)
// from an absent parameter (-> single-source). Get() alone returns "" for both
// and would silently serve the wrong file for one of them.
func proposalVideoPath(p proposals.Proposal, q url.Values) (string, string) {
	if q.Has("candidateIndex") {
		i, err := strconv.Atoi(q.Get("candidateIndex"))
		if err != nil {
			return "", "candidateIndex must be an integer"
		}
		// The bounds check IS the trust boundary (see the file doc comment).
		// len(p.Candidates) == 0 makes every index out of range, which is the
		// correct answer for a single-source proposal that was sent one.
		if !inRange(i, len(p.Candidates)) {
			return "", "candidateIndex out of range for this proposal"
		}
		return p.Candidates[i].Path, ""
	}
	if len(p.Candidates) > 0 {
		return "", "candidateIndex is required for this proposal"
	}
	if p.SourcePath == "" {
		return "", "proposal has no source file"
	}
	return p.SourcePath, ""
}

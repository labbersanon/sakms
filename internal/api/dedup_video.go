package api

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"

	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/proposals"
)

// dedupVideoHandler serves GET /api/modes/{mode}/dedup/proposals/{id}/video?
// candidateIndex=N — the raw video bytes of one Dedup candidate, for the card
// view's click-to-play preview. It resolves the file to serve entirely
// server-side: it looks the proposal up by {id}, bounds-checks candidateIndex
// against len(p.Candidates), and serves p.Candidates[candidateIndex].Path.
//
// Trust boundary is PROVENANCE, not lexical-root confinement. The path served
// is one SAK itself recorded during its own filesystem scan — it is never a
// client-supplied path. The client only ever supplies a proposal id and an
// integer index, both bounds-checked here; it cannot name a path, so there is
// no path-traversal surface to confine. No lexical-root check is layered on
// top: three such schemes (a browsableRoots allowlist, a live settings lookup,
// and the proposal's own scan-time RootFolderPath) were each tried and rejected
// across plan review rounds 1-4, because dedup's whole purpose is finding
// duplicates scattered across different directories, so no single root ever
// covers every candidate in a group — any root check spuriously 400s a
// legitimate sibling-directory candidate without adding real protection over
// the provenance check above (see .omc/plans/dedup-ux-refine.md pre-mortem #2).
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
func dedupVideoHandler(propStore *proposals.Store) http.HandlerFunc {
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
		if cErr != nil {
			http.Error(w, "candidateIndex is a required integer", http.StatusBadRequest)
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
		if prop.Workflow != proposals.Dedup {
			http.Error(w, "not a dedup proposal", http.StatusBadRequest)
			return
		}
		if prop.Mode != m {
			http.Error(w, "proposal does not belong to that mode", http.StatusBadRequest)
			return
		}
		// AC14(a): out-of-range index is an explicit 400, not a silent empty
		// response — the bounds check IS the trust boundary (see doc comment).
		if !inRange(candidateIndex, len(prop.Candidates)) {
			http.Error(w, "candidateIndex out of range for this proposal", http.StatusBadRequest)
			return
		}

		path := prop.Candidates[candidateIndex].Path
		f, err := os.Open(path)
		if err != nil {
			// A valid proposal+index whose file has vanished/changed on disk
			// since the scan — not a client error, and not tested by AC14.
			http.Error(w, "candidate file is not available", http.StatusInternalServerError)
			return
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil || info.IsDir() {
			http.Error(w, "candidate file is not available", http.StatusInternalServerError)
			return
		}

		// *os.File is an io.ReadSeeker, so ServeContent handles Range/If-Range
		// requests (seeking, 206 Partial Content) itself — no in-memory buffer.
		http.ServeContent(w, r, filepath.Base(path), info.ModTime(), f)
	}
}

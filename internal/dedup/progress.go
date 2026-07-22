package dedup

// ProgressEvent is one per-file liveness signal emitted during a scan's analyze
// phase. Deliberately lightweight: a display name + position, not an audit
// record. Current and Total are in the SAME unit in every mode — files whose
// analyze step has completed ("files processed") — so Current/Total is a
// consistent fraction that can never read over 100% (see the per-mode Total
// computation in dedup_phash_primary.go / dedup_adult_library.go).
//
// Phase stays mode-specific: Movies/Series report the hash phase ("hashing"),
// Adult reports the identify phase ("identifying"). Callers must not force a
// single uniform phase string that misfits a mode.
type ProgressEvent struct {
	Current int    // count of files whose analyze step has completed (see unit note)
	Total   int    // total files that WILL be analyzed in this scan (same unit)
	Name    string // basename of the file being analyzed
	Phase   string // "hashing" | "identifying" | "comparing"
}

// ProgressFunc receives per-file progress. A nil ProgressFunc is valid and
// means "report nothing" — every existing caller passes nil unless it wants the
// stream. A plain func callback (mirroring downloader.Manager's onComplete
// precedent), not an interface: there is only one real implementation, so an
// interface would be premature. The callback is invoked synchronously on the
// scan goroutine and must never block.
type ProgressFunc func(ProgressEvent)

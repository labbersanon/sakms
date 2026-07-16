package identify

// MatchResult is the normalized shape every lookup path produces (fingerprint
// batch, TPDB REST, StashDB/FansDB/TPDB text search, scene-by-ID).
//
// Deliberately a plain value type with only string/float/int fields (never a
// pointer to shared mutable state) — copying a MatchResult by value is always
// a safe, independent copy in Go (strings are immutable), which is what makes
// the cache in cache.go correct by construction: callers can never corrupt
// cached state as long as they keep receiving MatchResult values, not
// pointers into cache internals.
type MatchResult struct {
	Title   string
	Studio  string
	Date    string
	Type    string // "scene" or "movie"
	Source  string // e.g. "stashdb_id", "stashdb_text", "tpdb_text", "web_search", "web+stashdb_text"
	SceneID string // stash-box scene id, "" if none (e.g. source == "web_search")
	Box     string // "stashdb" | "fansdb" | "tpdb" | ""
	// Image is the matched scene's poster/thumbnail URL, when the lookup path
	// that produced this result had one in hand (SearchStashBox/SearchTPDB/
	// SceneByID all populate it for free from data they already fetched — no
	// extra round trip). "" for paths that never had display data (e.g.
	// web_search alone, before a re-search against a real box).
	Image string
	// Tags is the matched scene's genre/tag names, comma-joined into a single
	// string rather than a []string — this keeps MatchResult's "only string/
	// float/int fields, safe to copy by value" invariant intact (see this
	// struct's own doc comment) instead of introducing a shared-backing-array
	// slice field. "" if none/unavailable.
	Tags string
	// Performers is the matched scene's own performer names, comma-joined for
	// the same reason Tags is (see above) — sourced from the matched box
	// scene's own performer list (TPDB's SceneResource.performers[].name,
	// confirmed present in TPDB's live OpenAPI schema), not from any AI/
	// filename-parse guess, so it's authoritative the same way Tags is.
	// Currently only populated by the TPDB text-search paths (SearchTPDB/
	// SearchTPDBMovies) — SearchStashBox/SceneByID (StashDB/FansDB) leave
	// this "" for now; stash-box's performers shape hasn't been verified
	// against a live instance yet (see CLAUDE.md's "honesty about unverified
	// assumptions"). "" if none/unavailable.
	Performers string
	// RuntimeSeconds is the matched scene's runtime, when the lookup path that
	// produced this result had one in hand (same "populated for free, no
	// extra round trip" convention as Image/Tags above). 0 means unknown —
	// callers computing an implied bitrate MUST treat 0 as "skip the check,"
	// never as a real zero-length runtime (see tpdbrest.Scene.Duration's own
	// doc comment for the same convention). Added after a live bug: Adult's
	// auto-grab scorer (internal/autograb.GradeCandidate) never re-fetches a
	// real runtime the way Movies/Series do, so a caller building a grab
	// request from a match with RuntimeSeconds==0 will silently fail to
	// auto-qualify anything.
	RuntimeSeconds int
}

// WhisparrForeignID returns the normalized identifier Whisparr V3's
// AddRequest.ForeignID expects for this match, and whether the match has one at
// all. A match without a valid stash-box/TPDB scene id (web_search-only,
// SceneID=="" || Box=="") has no valid ForeignID — ok is false, and callers
// must not persist or register it as a scene. Raw stash-box UUID for
// stashdb/fansdb matches; "tpdbId:<id>" for TPDB-only matches (client.go's
// AddRequest doc, confirmed against Whisparr-Eros MovieResource.cs). Both
// rename (orphan-side, via classifyAdultMatch) and dedup (both sides) derive
// foreignIDs through this single method so they can never silently diverge.
func (r MatchResult) WhisparrForeignID() (id string, ok bool) {
	if r.SceneID == "" || r.Box == "" {
		return "", false
	}
	if r.Box == "tpdb" {
		return "tpdbId:" + r.SceneID, true
	}
	return r.SceneID, true
}

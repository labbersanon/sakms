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
}

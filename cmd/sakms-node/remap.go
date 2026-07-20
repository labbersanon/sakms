package main

import (
	"strings"

	"github.com/labbersanon/sakms/internal/nodes"
)

// Remap returns the local path for a server path using the configured path
// map. It matches the longest server prefix (to handle overlapping prefixes
// correctly). The match is boundary-aware: the server prefix must be followed
// by a path separator or be the entire path.
//
// When a match is found, the server prefix is replaced with the local prefix.
// If the local prefix uses backslash separators (Windows), the remainder of
// the path is converted from forward slashes to backslashes so the returned
// path is native on the local machine.
//
// Returns the original path unchanged if no prefix matches.
func Remap(entries []PathMapEntry, serverPath string) string {
	best := -1
	bestLen := -1
	for i, e := range entries {
		sp := e.Server
		if !strings.HasPrefix(serverPath, sp) {
			continue
		}
		// Boundary check: the match must end at a separator or at end of path.
		rest := serverPath[len(sp):]
		if rest != "" && rest[0] != '/' {
			continue
		}
		if len(sp) > bestLen {
			bestLen = len(sp)
			best = i
		}
	}
	if best < 0 {
		return serverPath
	}

	e := entries[best]
	rest := serverPath[len(e.Server):]

	// Detect whether the local prefix uses backslash separators (Windows).
	localUsesBackslash := strings.ContainsRune(e.Local, '\\')
	if localUsesBackslash {
		// Convert the Unix-style remainder to backslashes before joining.
		rest = strings.ReplaceAll(rest, "/", "\\")
	}
	return e.Local + rest
}

// mergePathMap overlays incoming entries onto existing by Server key (add or
// replace) and returns the merged result — a key present in existing but NOT
// in incoming is left untouched, unlike a wholesale replace. This is what
// makes the server's "only push non-empty rows" reconnect guard actually
// hold: an excluded key (an unconfigured/disabled library-path row) keeps
// whatever this node already had for it, rather than being silently dropped.
func mergePathMap(existing []PathMapEntry, incoming []nodes.PathMapping) []PathMapEntry {
	merged := make(map[string]PathMapEntry, len(existing)+len(incoming))
	for _, pm := range existing {
		merged[pm.Server] = pm
	}
	for _, pm := range incoming {
		merged[pm.Server] = PathMapEntry{Server: pm.Server, Local: pm.Local}
	}
	out := make([]PathMapEntry, 0, len(merged))
	for _, pm := range merged {
		out = append(out, pm)
	}
	return out
}

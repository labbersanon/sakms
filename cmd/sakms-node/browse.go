package main

import (
	"os"
	"path/filepath"
	"sort"

	"github.com/labbersanon/sakms/internal/nodes"
)

// browseDirectory lists the subdirectories of path on this node's own
// filesystem, for the operator to pick a node-local path against a library
// root. Mirrors the server's own GET /api/browse (internal/api/browse.go)
// response shape (dirs only, alpha-sorted) — but deliberately has NO
// allowlist restricting which paths may be listed, unlike the server's
// browse.go (which is confined to {/media,/downloads,/adult}).
//
// That's a considered difference, not an oversight: the server's allowlist
// exists because its own filesystem is a fixed, known container layout with
// specific mount points meant to be browsed. A worker node's disk layout is
// arbitrary and unknowable in advance — a hardcoded allowlist would be wrong
// for most installs, and one the operator would have to configure per-node
// adds complexity without adding real security. The operator already chose
// to install and run this daemon on this specific machine, trusting it with
// local filesystem access for phash computation; browsing directory names
// is a strictly smaller capability than that.
//
// Documented honestly, not glossed over: as of this writing,
// packaging/rpm/sakms-node.service runs the daemon as User=root, so "no
// allowlist beyond the OS's own file permissions" currently means no
// meaningful restriction in practice — the OS permission model isn't
// actually narrowing anything for a root-running process. That's a
// pre-existing packaging decision, not something this feature introduces or
// changes; it's called out here so a future reader doesn't mistake this
// comment for a security boundary that isn't really there today.
func browseDirectory(path string) ([]nodes.BrowseEntry, error) {
	infos, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}

	entries := make([]nodes.BrowseEntry, 0, len(infos))
	for _, info := range infos {
		if !info.IsDir() {
			continue
		}
		entries = append(entries, nodes.BrowseEntry{
			Name: info.Name(),
			Path: filepath.Join(path, info.Name()),
		})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	return entries, nil
}

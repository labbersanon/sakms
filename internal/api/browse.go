package api

import (
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/labbersanon/sakms/internal/apidto"
)

// browsableRoots are the only directory subtrees the Browse endpoint will
// ever list. They are the container-side mount points SAK actually has
// (see the deployed compose.yml's volumes) — everything the operator could
// legitimately pick as a library root folder lives under one of these. Any
// requested path that doesn't resolve under one of them is rejected, not
// silently clamped, so a traversal attempt fails loudly rather than quietly
// listing the wrong tree.
var browsableRoots = []string{"/media", "/downloads", "/adult"}

// resolveBrowsablePath validates a caller-supplied path and returns its
// cleaned, absolute form, or an error if it escapes every browsable root.
// The check is purely lexical (filepath.Clean, no filesystem access) so it
// stays trivially unit-testable and can't be tricked by "/media/../etc"
// style traversal — the CLEANED path's prefix is what's checked, never the
// raw string. A bare root (e.g. "/media") is itself valid; a sibling that
// merely shares a string prefix (e.g. "/mediafoo") is not.
//
// Symlink traversal (a symlink under a root pointing outside it) is out of
// scope by design: resolution is lexical under this app's single-operator
// trust model, matching the endpoint's documented contract.
func resolveBrowsablePath(root string) (string, error) {
	cleaned := filepath.Clean(root)
	if !filepath.IsAbs(cleaned) {
		return "", errPathOutsideRoots
	}
	for _, r := range browsableRoots {
		if cleaned == r || strings.HasPrefix(cleaned, r+string(filepath.Separator)) {
			return cleaned, nil
		}
	}
	return "", errPathOutsideRoots
}

// errPathOutsideRoots is returned by resolveBrowsablePath for any path that
// doesn't resolve under a browsable root — surfaced to the client as a 400.
var errPathOutsideRoots = &browseError{"path must be within one of the mounted roots: /media, /downloads, /adult"}

type browseError struct{ msg string }

func (e *browseError) Error() string { return e.msg }

// browseHandler lists the sub-directories of a path under one of the
// browsable roots, for the Settings UI's root-folder picker and its
// as-you-type autocomplete (both hit this same endpoint). Directories only
// — a file is never a valid root folder. With no (or empty) `path`, it
// returns the three roots themselves so the picker has somewhere to start.
//
// A valid-prefix path that doesn't exist yet returns 200 with no entries
// rather than an error, so the debounced autocomplete degrades gracefully
// while the operator is still mid-word.
func browseHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		raw := r.URL.Query().Get("path")
		if raw == "" {
			entries := make([]apidto.BrowseEntry, 0, len(browsableRoots))
			for _, root := range browsableRoots {
				entries = append(entries, apidto.BrowseEntry{Name: root, Path: root})
			}
			writeJSON(w, apidto.BrowseResponse{Path: "", Entries: entries})
			return
		}

		dir, err := resolveBrowsablePath(raw)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		infos, err := os.ReadDir(dir)
		if err != nil {
			if os.IsNotExist(err) {
				writeJSON(w, apidto.BrowseResponse{Path: dir, Entries: []apidto.BrowseEntry{}})
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		entries := make([]apidto.BrowseEntry, 0, len(infos))
		for _, info := range infos {
			if !info.IsDir() {
				continue
			}
			entries = append(entries, apidto.BrowseEntry{
				Name: info.Name(),
				Path: filepath.Join(dir, info.Name()),
			})
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })

		writeJSON(w, apidto.BrowseResponse{Path: dir, Entries: entries})
	}
}

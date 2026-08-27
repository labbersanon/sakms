package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"unicode/utf8"

	"github.com/labbersanon/sakms/internal/apidto"
	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/organizeevents"
)

// Claude 2026-08-27: Organize Browse tab API.
// Reason: confirm-then-mutate file manager under the same lexical allowlist
//   as GET /api/browse, with library RemapPath/ForgetPath so disk and UI
//   cannot drift. The confirm dialog is the approval, so mutations run
//   immediately instead of queueing onto Scan/Apply.
// Troubleshooting: 400 "path must be within..." — resolveBrowsablePath
//   rejected traversal or a path outside /media,/downloads,/adult.
//   EXDEV — source and dest are on different mounts; copy is not offered.
// Review if: Browse is staged onto the proposals queue.

func organizeBrowseListHandler(libStore *library.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		raw := r.URL.Query().Get("path")
		if raw == "" {
			entries := make([]apidto.OrganizeBrowseEntry, 0, len(browsableRoots))
			for _, root := range browsableRoots {
				tracked := false
				if libStore != nil {
					ok, err := libStore.HasTrackedUnder(r.Context(), root)
					if err != nil {
						http.Error(w, err.Error(), http.StatusInternalServerError)
						return
					}
					tracked = ok
				}
				entries = append(entries, apidto.OrganizeBrowseEntry{
					Name: root, Path: root, IsDir: true, Tracked: tracked,
				})
			}
			writeJSON(w, apidto.OrganizeBrowseResponse{Path: "", Entries: entries})
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
				writeJSON(w, apidto.OrganizeBrowseResponse{
					Path: dir, Parent: browseParent(dir), Entries: []apidto.OrganizeBrowseEntry{},
				})
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		var tracked []string
		if libStore != nil {
			tracked, err = libStore.TrackedPathsUnder(r.Context(), dir)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		}

		entries := make([]apidto.OrganizeBrowseEntry, 0, len(infos))
		for _, info := range infos {
			full := filepath.Join(dir, info.Name())
			ent := apidto.OrganizeBrowseEntry{
				Name:  info.Name(),
				Path:  full,
				IsDir: info.IsDir(),
			}
			if fi, err := info.Info(); err == nil {
				if !info.IsDir() {
					ent.Size = fi.Size()
				}
				ent.ModTime = fi.ModTime().UTC().Format("2006-01-02T15:04:05Z")
			}
			ent.Tracked = library.EntryTracked(full, info.IsDir(), tracked)
			entries = append(entries, ent)
		}
		sort.Slice(entries, func(i, j int) bool {
			if entries[i].IsDir != entries[j].IsDir {
				return entries[i].IsDir
			}
			return entries[i].Name < entries[j].Name
		})
		writeJSON(w, apidto.OrganizeBrowseResponse{
			Path: dir, Parent: browseParent(dir), Entries: entries,
		})
	}
}

func organizeBrowseRenameHandler(libStore *library.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req apidto.OrganizeBrowseRenameRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		src, err := resolveBrowsablePath(req.Path)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if isBrowsableRoot(src) {
			http.Error(w, "cannot rename a mounted root", http.StatusBadRequest)
			return
		}
		newName := strings.TrimSpace(req.NewName)
		if err := validateBaseName(newName); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		dest := filepath.Join(filepath.Dir(src), newName)
		if _, err := resolveBrowsablePath(dest); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		item := mutatePath(r, libStore, src, dest)
		if item.OK {
			logBrowseOK(r.Context(), organizeevents.KindFileRename, src+" → "+dest)
		}
		writeJSON(w, apidto.OrganizeBrowseOpResponse{Results: []apidto.OrganizeBrowseOpItem{item}})
	}
}

func organizeBrowseMoveHandler(libStore *library.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req apidto.OrganizeBrowseMoveRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		if len(req.Paths) == 0 {
			http.Error(w, "paths is required", http.StatusBadRequest)
			return
		}
		destDir, err := resolveBrowsablePath(req.DestDir)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		st, err := os.Stat(destDir)
		if err != nil || !st.IsDir() {
			http.Error(w, "destDir must be an existing directory", http.StatusBadRequest)
			return
		}

		results := make([]apidto.OrganizeBrowseOpItem, 0, len(req.Paths))
		okAll := true
		fail := func(path, msg string) {
			results = append(results, apidto.OrganizeBrowseOpItem{Path: path, Error: msg})
			okAll = false
		}
		for _, p := range req.Paths {
			src, err := resolveBrowsablePath(p)
			if err != nil {
				fail(p, err.Error())
				continue
			}
			if isBrowsableRoot(src) {
				fail(src, "cannot move a mounted root")
				continue
			}
			if src == destDir || strings.HasPrefix(destDir, src+string(os.PathSeparator)) {
				fail(src, "cannot move a directory into itself")
				continue
			}
			item := mutatePath(r, libStore, src, filepath.Join(destDir, filepath.Base(src)))
			if !item.OK {
				okAll = false
			}
			results = append(results, item)
		}
		if okAll {
			logBrowseOK(r.Context(), organizeevents.KindFileMove,
				"moved "+strings.Join(req.Paths, ", ")+" → "+destDir)
		}
		writeJSON(w, apidto.OrganizeBrowseOpResponse{Results: results})
	}
}

func organizeBrowseDeleteHandler(libStore *library.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req apidto.OrganizeBrowseDeleteRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		if len(req.Paths) == 0 {
			http.Error(w, "paths is required", http.StatusBadRequest)
			return
		}
		// Longest path first, so a nested selection deletes the child before
		// the RemoveAll on its parent makes it vanish.
		paths := append([]string(nil), req.Paths...)
		sort.Slice(paths, func(i, j int) bool { return len(paths[i]) > len(paths[j]) })

		results := make([]apidto.OrganizeBrowseOpItem, 0, len(paths))
		okAll := true
		for _, p := range paths {
			src, err := resolveBrowsablePath(p)
			if err != nil {
				results = append(results, apidto.OrganizeBrowseOpItem{Path: p, Error: err.Error()})
				okAll = false
				continue
			}
			item := deletePath(r, libStore, src)
			if !item.OK {
				okAll = false
			}
			results = append(results, item)
		}
		if okAll {
			logBrowseOK(r.Context(), organizeevents.KindFileDelete,
				"deleted "+strings.Join(req.Paths, ", "))
		}
		writeJSON(w, apidto.OrganizeBrowseOpResponse{Results: results})
	}
}

func mutatePath(r *http.Request, libStore *library.Store, src, dest string) apidto.OrganizeBrowseOpItem {
	item := apidto.OrganizeBrowseOpItem{Path: src, Dest: dest}
	if src == dest {
		item.OK = true
		return item
	}
	if _, err := os.Stat(dest); err == nil {
		item.Error = "destination already exists"
		return item
	} else if !os.IsNotExist(err) {
		item.Error = err.Error()
		return item
	}
	if err := os.Rename(src, dest); err != nil {
		item.Error = renameError(err)
		return item
	}
	if libStore != nil {
		tracked, err := libStore.RemapPath(r.Context(), src, dest)
		if err != nil {
			if rb := os.Rename(dest, src); rb != nil {
				item.Error = "library remap failed (" + err.Error() + "); rollback also failed: " + rb.Error()
				return item
			}
			item.Error = "library remap failed, rename rolled back: " + err.Error()
			return item
		}
		item.Tracked = tracked
	}
	item.OK = true
	return item
}

func deletePath(r *http.Request, libStore *library.Store, src string) apidto.OrganizeBrowseOpItem {
	item := apidto.OrganizeBrowseOpItem{Path: src}
	if isBrowsableRoot(src) {
		item.Error = "cannot delete a mounted root"
		return item
	}
	st, err := os.Lstat(src)
	if err != nil {
		if os.IsNotExist(err) {
			item.OK = true
			return item
		}
		item.Error = err.Error()
		return item
	}
	if st.IsDir() {
		err = os.RemoveAll(src)
	} else {
		err = os.Remove(src)
	}
	if err != nil && !os.IsNotExist(err) {
		item.Error = err.Error()
		return item
	}
	if libStore != nil {
		tracked, err := libStore.ForgetPath(r.Context(), src)
		if err != nil {
			item.Error = "deleted on disk but library update failed: " + err.Error()
			return item
		}
		item.Tracked = tracked
	}
	item.OK = true
	return item
}

func logBrowseOK(ctx context.Context, kind, message string) {
	ok := true
	organizeevents.Log(ctx, organizeevents.Event{
		Workflow: "browse", Kind: kind, OK: &ok, Message: message,
	})
}

func validateBaseName(name string) error {
	switch {
	case name == "":
		return errors.New("newName is required")
	case strings.ContainsRune(name, '/') || strings.ContainsRune(name, filepath.Separator):
		return errors.New("newName must be a basename (no slashes)")
	case name == ".", name == "..", strings.ContainsRune(name, '\x00'), !utf8.ValidString(name):
		return errors.New("newName is not a valid basename")
	}
	return nil
}

func renameError(err error) string {
	// *os.LinkError unwraps to its syscall errno, so this covers os.Rename.
	if errors.Is(err, syscall.EXDEV) {
		return "cannot move across filesystems"
	}
	return err.Error()
}

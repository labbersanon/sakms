package place

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"syscall"
)

// Claude 2026-08-11: cross-device Move for grab import (iSCSI staging → CIFS roots).
// Reason: os.Rename fails with EXDEV across mount points; Adult/Series imports
// were stuck in triage with "invalid cross-device link".
// Troubleshooting: downloader import log on grab 7 (Step Sis → /adult).
// Review if: all library relocate call sites stop using raw os.Rename.

// Claude 2026-09-30: distinct error when EXDEV copy worked but unlink did not.
// Reason: callers must keep dest and retry source cleanup — not fail the import.
// Troubleshooting: errors.Is(err, ErrCopiedSourceRemains); dest exists.
// Review if: AcceptCopiedDest grows a durable leftover queue.
// ErrCopiedSourceRemains is returned when an EXDEV copy finished and dest is
// the canonical file, but removing src failed. Dest must not be rolled back.
var ErrCopiedSourceRemains = errors.New("place: copy succeeded but source remains")

// CopiedSourceRemainsError carries both paths after a successful cross-device copy.
type CopiedSourceRemainsError struct {
	Src, Dst string
	Remove   error
}

func (e *CopiedSourceRemainsError) Error() string {
	return fmt.Sprintf("copied to %q but removing source %q failed: %v", e.Dst, e.Src, e.Remove)
}

func (e *CopiedSourceRemainsError) Unwrap() error { return e.Remove }

func (e *CopiedSourceRemainsError) Is(target error) bool {
	return target == ErrCopiedSourceRemains
}

// CopiedSource extracts src/dst from an ErrCopiedSourceRemains chain.
func CopiedSource(err error) (src, dst string, ok bool) {
	var e *CopiedSourceRemainsError
	if errors.As(err, &e) && e != nil {
		return e.Src, e.Dst, true
	}
	return "", "", false
}

// AcceptCopiedDest keeps dest when Move copied across devices but could not
// unlink src. Retries the unlink; leftovers are logged, dest is still returned.
func AcceptCopiedDest(dest string, err error) (string, error) {
	if err == nil {
		return dest, nil
	}
	if dest == "" || !errors.Is(err, ErrCopiedSourceRemains) {
		return dest, err
	}
	if src, _, ok := CopiedSource(err); ok && src != "" {
		if rm := removeAll(src); rm != nil && !errors.Is(rm, os.ErrNotExist) {
			log.Printf("place: dest %s is in place; leftover source %s: %v", dest, src, rm)
		}
	}
	return dest, nil
}

// renameFile is os.Rename in production; tests swap it via SetRenameForTest.
var renameFile = os.Rename

// removeAll is os.RemoveAll in production; tests swap it via SetRemoveAllForTest.
var removeAll = os.RemoveAll

// SetRenameForTest replaces the rename used by Move. Restores the previous
// function when the returned cleanup is called. Tests only.
func SetRenameForTest(fn func(oldpath, newpath string) error) (restore func()) {
	prev := renameFile
	renameFile = fn
	return func() { renameFile = prev }
}

// SetRemoveAllForTest replaces the unlink used after an EXDEV copy. Tests only.
func SetRemoveAllForTest(fn func(path string) error) (restore func()) {
	prev := removeAll
	removeAll = fn
	return func() { removeAll = prev }
}

// Move relocates src to dst. Same-filesystem paths use rename; when rename
// fails with EXDEV (cross-device), it copies then removes the source so
// grab import works from iSCSI staging onto CIFS library roots.
func Move(src, dst string) error {
	if err := renameFile(src, dst); err == nil {
		return nil
	} else if !isEXDEV(err) {
		return err
	}
	if err := copyPath(src, dst); err != nil {
		_ = removeAll(dst)
		return fmt.Errorf("copying across devices from %q to %q: %w", src, dst, err)
	}
	// Claude 2026-09-30: dest is canonical after a successful EXDEV copy.
	// Reason: returning a generic error made import/Apply treat dest as failed
	//   and retry into UniquePath .2 while the library already held the file.
	// Troubleshooting: "copy succeeded but source remains"; dest exists, src may linger.
	// Review if: a durable leftover-source sweeper replaces the one retry in AcceptCopiedDest.
	if err := removeAll(src); err != nil {
		return &CopiedSourceRemainsError{Src: src, Dst: dst, Remove: err}
	}
	return nil
}

func isEXDEV(err error) bool {
	var linkErr *os.LinkError
	if errors.As(err, &linkErr) && errors.Is(linkErr.Err, syscall.EXDEV) {
		return true
	}
	var pathErr *os.PathError
	if errors.As(err, &pathErr) && errors.Is(pathErr.Err, syscall.EXDEV) {
		return true
	}
	return false
}

func copyPath(src, dst string) error {
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing to copy symlink %q", src)
	}
	if info.IsDir() {
		return copyDir(src, dst)
	}
	return copyFile(src, dst, info.Mode())
}

func copyFile(src, dst string, mode fs.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode.Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func copyDir(src, dst string) error {
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		target := filepath.Join(dst, rel)
		if d.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing to copy symlink %q", path)
		}
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		return copyFile(path, target, info.Mode())
	})
}

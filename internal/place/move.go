package place

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

// Claude 2026-08-11: cross-device Move for grab import (iSCSI staging → CIFS roots).
// Reason: os.Rename fails with EXDEV across mount points; Adult/Series imports
// were stuck in triage with "invalid cross-device link".
// Troubleshooting: downloader import log on grab 7 (Step Sis → /adult).
// Review if: all library relocate call sites stop using raw os.Rename.

// renameFile is os.Rename in production; tests swap it via SetRenameForTest.
var renameFile = os.Rename

// SetRenameForTest replaces the rename used by Move. Restores the previous
// function when the returned cleanup is called. Tests only.
func SetRenameForTest(fn func(oldpath, newpath string) error) (restore func()) {
	prev := renameFile
	renameFile = fn
	return func() { renameFile = prev }
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
		_ = os.RemoveAll(dst)
		return fmt.Errorf("copying across devices from %q to %q: %w", src, dst, err)
	}
	if err := os.RemoveAll(src); err != nil {
		return fmt.Errorf("copied to %q but removing source %q failed: %w", dst, src, err)
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

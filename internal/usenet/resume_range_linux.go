//go:build linux

package usenet

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// rangeHasDataExtents returns false when [start, end) is entirely (or starts
// as) a sparse hole. Filesystems that reject SEEK_DATA with EINVAL fall
// through as "unknown — let the sample decide" (return true).
func rangeHasDataExtents(f *os.File, start, end int64) bool {
	fd := int(f.Fd())
	off := start
	for off < end {
		data, err := unix.Seek(fd, off, unix.SEEK_DATA)
		if err != nil {
			if errors.Is(err, unix.ENXIO) {
				return false
			}
			// EINVAL / unsupported → defer to byte sample.
			return true
		}
		if data > off {
			// Hole from off to data — our range starts hollow.
			return false
		}
		if data >= end {
			return false
		}
		hole, err := unix.Seek(fd, data, unix.SEEK_HOLE)
		if err != nil {
			return true
		}
		if hole >= end {
			return true
		}
		// Hole begins inside the range; keep scanning after it.
		off = hole
	}
	return false
}

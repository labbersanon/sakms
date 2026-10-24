//go:build linux

package playback

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// Claude 2026-10-23: SEEK_DATA/SEEK_HOLE for Truncate-up usenet files.
// Reason: assemble Truncates to FileSize then WriteAt; Stat size is the whole
//   film while offset 0 is still a hole. GrowReader must not feed those NULs.
// Troubleshooting: HLS 500 on Play at +2s — ffprobe of a hollow mkv.
// Review if: assembly stops Truncate-up (then this is just append-only).

func extentAt(f *os.File, off, size int64) (end int64, isData, known bool) {
	if f == nil || off < 0 || off >= size {
		return size, false, true
	}
	fd := int(f.Fd())
	data, err := unix.Seek(fd, off, unix.SEEK_DATA)
	if err != nil {
		if errors.Is(err, unix.ENXIO) {
			return size, false, true
		}
		return 0, false, false
	}
	if data > off {
		return min(data, size), false, true
	}
	hole, err := unix.Seek(fd, off, unix.SEEK_HOLE)
	if err != nil {
		return 0, false, false
	}
	if hole > size {
		hole = size
	}
	if hole < off {
		hole = off
	}
	return hole, true, true
}

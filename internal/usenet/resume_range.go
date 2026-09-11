package usenet

import (
	"os"
)

// rangeLooksPopulated reports whether [offset, offset+length) has real payload
// rather than a sparse hole or dense all-NUL fill left by Truncate-up + skip.
//
// Two checks:
//  1. SEEK_DATA/SEEK_HOLE extents when the filesystem supports them (Linux).
//  2. A short sample read — if every sampled byte is 0, treat as hollow.
//
// Legitimate all-zero yEnc segments are vanishingly rare for video payloads;
// false re-downloads are safer than importing hollow MKVs.
func rangeLooksPopulated(f *os.File, offset, length int64) bool {
	if f == nil || length <= 0 || offset < 0 {
		return false
	}
	end := offset + length
	if !rangeHasDataExtents(f, offset, end) {
		return false
	}
	n := length
	if n > 4096 {
		n = 4096
	}
	buf := make([]byte, n)
	got, err := f.ReadAt(buf, offset)
	if got == 0 {
		return false
	}
	_ = err // short read / EOF still samples what we got
	for _, b := range buf[:got] {
		if b != 0 {
			return true
		}
	}
	return false
}

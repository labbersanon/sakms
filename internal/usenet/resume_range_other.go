//go:build !linux

package usenet

import "os"

// rangeHasDataExtents is a no-op on non-Linux — sample bytes decide.
func rangeHasDataExtents(f *os.File, start, end int64) bool {
	return true
}

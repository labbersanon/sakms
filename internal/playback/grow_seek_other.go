//go:build !linux

package playback

import "os"

func extentAt(f *os.File, off, size int64) (end int64, isData, known bool) {
	return 0, false, false
}

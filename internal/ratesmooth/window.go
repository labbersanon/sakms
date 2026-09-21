// Package ratesmooth computes a rolling bytes/sec rate from timestamped
// progress samples. Used by the Usenet and torrent poll loops so DownloadSpeed
// is averaged over ~10s instead of a single 500ms tick.
package ratesmooth

import "time"

// DefaultWindow is the wall-clock span used for DownloadSpeed (balanced
// smoothness vs responsiveness for Usenet/NNTP burstiness).
const DefaultWindow = 10 * time.Second

// Sample is one (time, completed-bytes) point.
type Sample struct {
	At time.Time
	N  int64
}

// Window holds recent samples. Zero value is ready to use with DefaultWindow.
type Window struct {
	samples []Sample
	span    time.Duration
}

// Add appends a sample, drops points older than the window, and returns
// bytes/sec over the remaining span. Returns 0 until two distinct samples
// span a positive duration with forward progress.
func (w *Window) Add(at time.Time, n int64) int64 {
	if w.span <= 0 {
		w.span = DefaultWindow
	}
	w.samples = append(w.samples, Sample{At: at, N: n})
	cutoff := at.Add(-w.span)
	i := 0
	for i < len(w.samples) && w.samples[i].At.Before(cutoff) {
		i++
	}
	// Keep at least two samples when possible so a single old point does not
	// leave the window empty mid-transfer.
	if i > 0 && len(w.samples)-i < 2 && len(w.samples) >= 2 {
		i = len(w.samples) - 2
	}
	if i > 0 {
		w.samples = append([]Sample(nil), w.samples[i:]...)
	}
	if len(w.samples) < 2 {
		return 0
	}
	first := w.samples[0]
	last := w.samples[len(w.samples)-1]
	dt := last.At.Sub(first.At).Seconds()
	if dt <= 0 {
		return 0
	}
	delta := last.N - first.N
	if delta <= 0 {
		return 0
	}
	return int64(float64(delta) / dt)
}

// Reset clears samples (pause / leave active).
func (w *Window) Reset() {
	w.samples = nil
}

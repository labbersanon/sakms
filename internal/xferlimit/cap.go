// Package xferlimit is the shared download bandwidth cap for torrent + Usenet.
package xferlimit

import (
	"context"
	"sync"

	"golang.org/x/time/rate"
)

// Claude 2026-09-18: one Limiter shared by torrent client + Usenet fetches.
// Reason: operator asked for a global Mbps cap across both download engines.
// Troubleshooting: both engines slow → check Cap Mbps (0 = unlimited).
// Review if: per-engine caps return (split Limiter ownership again).

const (
	// UnlimitedBurst must stay positive — see downloader.unlimitedBurst rationale
	// (anviltorrent rejects connections when limiter burst/tokens go wrong).
	UnlimitedBurst = 1 << 20
	minBurst       = 1 << 20
)

// Cap is a process-wide download rate limit in megabits/sec (Mbps).
// 0 Mbps = unlimited. Thread-safe.
type Cap struct {
	mu   sync.Mutex
	mbps int
	lim  *rate.Limiter
}

// New returns a Cap at mbps (0 = unlimited).
func New(mbps int) *Cap {
	c := &Cap{lim: rate.NewLimiter(rate.Inf, UnlimitedBurst)}
	c.SetMbps(mbps)
	return c
}

// Limiter is the shared *rate.Limiter (never nil). Pass to the torrent client.
func (c *Cap) Limiter() *rate.Limiter {
	if c == nil {
		return rate.NewLimiter(rate.Inf, UnlimitedBurst)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lim
}

// Mbps returns the configured cap (0 = unlimited).
func (c *Cap) Mbps() int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.mbps
}

// BytesPerSec is the token rate used by WaitN / torrent (0 = unlimited).
func (c *Cap) BytesPerSec() int {
	return MbpsToBytesPerSec(c.Mbps())
}

// SetMbps updates the shared limiter in place. mbps < 0 is treated as 0.
func (c *Cap) SetMbps(mbps int) {
	if c == nil {
		return
	}
	if mbps < 0 {
		mbps = 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.mbps = mbps
	bps := MbpsToBytesPerSec(mbps)
	if bps <= 0 {
		c.lim.SetBurst(UnlimitedBurst)
		c.lim.SetLimit(rate.Inf)
		return
	}
	burst := bps
	if burst < minBurst {
		burst = minBurst
	}
	c.lim.SetBurst(burst)
	c.lim.SetLimit(rate.Limit(bps))
}

// WaitN blocks until n bytes are allowed under the cap (no-op when unlimited).
func (c *Cap) WaitN(ctx context.Context, n int) error {
	if c == nil || n <= 0 {
		return nil
	}
	c.mu.Lock()
	lim := c.lim
	mbps := c.mbps
	c.mu.Unlock()
	if mbps <= 0 {
		return nil
	}
	return lim.WaitN(ctx, n)
}

// MbpsToBytesPerSec converts decimal megabits/sec to bytes/sec (1 Mbps = 125000 B/s).
func MbpsToBytesPerSec(mbps int) int {
	if mbps <= 0 {
		return 0
	}
	return mbps * 125000
}

// BytesPerSecToMbps rounds bytes/sec down to whole Mbps for soft-migrate display.
func BytesPerSecToMbps(bps int) int {
	if bps <= 0 {
		return 0
	}
	return bps / 125000
}

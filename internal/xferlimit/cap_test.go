package xferlimit

import (
	"context"
	"testing"
)

func TestMbpsToBytesPerSec(t *testing.T) {
	if MbpsToBytesPerSec(0) != 0 {
		t.Fatal("0 Mbps must be unlimited (0 B/s)")
	}
	if MbpsToBytesPerSec(1) != 125000 {
		t.Fatalf("1 Mbps = %d, want 125000", MbpsToBytesPerSec(1))
	}
	if MbpsToBytesPerSec(8) != 1000000 {
		t.Fatalf("8 Mbps = %d, want 1000000", MbpsToBytesPerSec(8))
	}
}

func TestBytesPerSecToMbps(t *testing.T) {
	if BytesPerSecToMbps(0) != 0 {
		t.Fatal()
	}
	if BytesPerSecToMbps(125000) != 1 {
		t.Fatal()
	}
	if BytesPerSecToMbps(200000) != 1 { // rounds down
		t.Fatal()
	}
}

func TestCap_SetMbpsUnlimited(t *testing.T) {
	c := New(10)
	c.SetMbps(0)
	if c.Mbps() != 0 || c.BytesPerSec() != 0 {
		t.Fatalf("got mbps=%d bps=%d", c.Mbps(), c.BytesPerSec())
	}
	if err := c.WaitN(context.Background(), 1<<20); err != nil {
		t.Fatal(err)
	}
}

func TestCap_LimiterStable(t *testing.T) {
	c := New(1)
	a := c.Limiter()
	c.SetMbps(2)
	b := c.Limiter()
	if a != b {
		t.Fatal("Limiter pointer must stay stable across SetMbps")
	}
}

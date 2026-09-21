package downloadstate

import (
	"math"
	"testing"
	"time"

	"github.com/labbersanon/sakms/internal/dbtest"
)

func TestStore_SeedRoundTrip(t *testing.T) {
	s := New(dbtest.New(t))
	started := time.Now().UTC().Truncate(time.Millisecond)
	if err := s.SaveSeed("gid-1", started, 100, 1000); err != nil {
		t.Fatal(err)
	}
	gotStarted, base, total, ok, err := s.LoadSeed("gid-1")
	if err != nil || !ok {
		t.Fatalf("LoadSeed: ok=%v err=%v", ok, err)
	}
	if base != 100 || total != 1000 || gotStarted.IsZero() {
		t.Fatalf("unexpected seed %+v %d %d", gotStarted, base, total)
	}
	if err := s.ClearSeed("gid-1"); err != nil {
		t.Fatal(err)
	}
	_, _, _, ok, err = s.LoadSeed("gid-1")
	if err != nil || ok {
		t.Fatalf("cleared seed should be missing, ok=%v err=%v", ok, err)
	}
}

func TestStore_SeedLargeTotal(t *testing.T) {
	s := New(dbtest.New(t))
	started := time.Now().UTC().Truncate(time.Millisecond)
	const big = int64(math.MaxInt32) + 123456789
	if err := s.SaveSeed("gid-big", started, big/2, big); err != nil {
		t.Fatal(err)
	}
	_, base, total, ok, err := s.LoadSeed("gid-big")
	if err != nil || !ok {
		t.Fatalf("LoadSeed: ok=%v err=%v", ok, err)
	}
	if base != big/2 || total != big {
		t.Fatalf("got base=%d total=%d want %d %d", base, total, big/2, big)
	}
}

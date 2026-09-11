package downloader

import (
	"sync"
	"testing"
	"time"
)

type memSeedStore struct {
	mu       sync.Mutex
	started  time.Time
	credited int64
	total    int64
	ok       bool
}

func (s *memSeedStore) SaveSeed(gid string, startedAt time.Time, baselineUp, totalBytes int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.started = startedAt
	s.credited = baselineUp
	s.total = totalBytes
	s.ok = true
	return nil
}

func (s *memSeedStore) LoadSeed(gid string) (time.Time, int64, int64, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.started, s.credited, s.total, s.ok, nil
}

func (s *memSeedStore) ClearSeed(gid string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ok = false
	return nil
}

// TestBeginSeeding_RestoresCreditedUpload proves baseline may be negative so
// uploaded = written - baseline recovers prior credit when the new handle
// starts at BytesWrittenData≈0.
func TestBeginSeeding_RestoresCreditedUpload(t *testing.T) {
	const credited int64 = 1 << 30 // 1 GiB
	store := &memSeedStore{
		started:  time.Now().UTC().Add(-time.Hour),
		credited: credited,
		total:    2 << 30,
		ok:       true,
	}
	m := &Manager{
		cfg:     Config{SeedStore: store},
		entries: map[string]*entry{"gid-1": {}},
	}
	m.beginSeeding("gid-1", nil, nil, 2<<30)

	e := m.entries["gid-1"]
	if e.seedBaselineUp != -credited {
		t.Fatalf("seedBaselineUp=%d, want %d (negative baseline)", e.seedBaselineUp, -credited)
	}
	// Simulate new handle written=0 → uploaded must equal prior credit.
	uploaded := int64(0) - e.seedBaselineUp
	if uploaded != credited {
		t.Fatalf("uploaded=%d, want restored credit %d", uploaded, credited)
	}
	store.mu.Lock()
	got := store.credited
	store.mu.Unlock()
	if got != credited {
		t.Fatalf("SaveSeed credited=%d, want %d (must not wipe on restore)", got, credited)
	}
}

func TestPersistSeedCredits_WritesOutsideLockShape(t *testing.T) {
	store := &memSeedStore{}
	m := &Manager{cfg: Config{SeedStore: store}}
	started := time.Now().UTC().Add(-time.Minute)
	m.persistSeedCredits([]seedCreditSnap{{
		gid: "gid-x", started: started, credited: 42, total: 100,
	}})
	store.mu.Lock()
	defer store.mu.Unlock()
	if !store.ok || store.credited != 42 || store.total != 100 {
		t.Fatalf("store = ok=%v credited=%d total=%d", store.ok, store.credited, store.total)
	}
	if !store.started.Equal(started) {
		t.Fatalf("started = %v want %v", store.started, started)
	}
}

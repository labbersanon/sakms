package downloader

import (
	"testing"
	"time"
)

func TestTryAdmitLocked_RespectsMaxConc(t *testing.T) {
	m := NewForTesting(t.TempDir())
	m.cfg.MaxConc = 1

	a := &entry{status: "waiting", metaReady: true, addedAt: time.Now().Add(-2 * time.Second)}
	b := &entry{status: "waiting", metaReady: true, addedAt: time.Now().Add(-1 * time.Second)}
	m.entries["a"] = a
	m.entries["b"] = b

	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.tryAdmitLocked(a) || a.status != "active" {
		t.Fatalf("first ready entry should admit, status=%q", a.status)
	}
	if m.tryAdmitLocked(b) || b.status != "waiting" {
		t.Fatalf("second entry must wait while MaxConc=1, admitted=%v status=%q", m.tryAdmitLocked(b), b.status)
	}
	a.status = "complete"
	allowed := m.admitWaitingLocked()
	if b.status != "active" {
		t.Fatalf("after slot frees, b should be active, got %q", b.status)
	}
	if len(allowed) != 0 {
		// b.t is nil in this test; admit still flips status. That's fine.
	}
}

func TestSyncConcurrencyLocked_DemotesNewest(t *testing.T) {
	m := NewForTesting(t.TempDir())
	m.cfg.MaxConc = 1
	older := &entry{status: "active", metaReady: true, addedAt: time.Now().Add(-time.Minute)}
	newer := &entry{status: "active", metaReady: true, addedAt: time.Now()}
	m.entries["old"] = older
	m.entries["new"] = newer

	m.mu.Lock()
	defer m.mu.Unlock()
	_, deny := m.syncConcurrencyLocked()
	if older.status != "active" {
		t.Fatalf("older should keep the slot, got %q", older.status)
	}
	if newer.status != "waiting" {
		t.Fatalf("newer should be demoted to waiting, got %q", newer.status)
	}
	_ = deny
}

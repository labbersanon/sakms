package downloader

import (
	"net/http"
	"testing"
	"time"
)

func TestForget_DropsCompleteOnly(t *testing.T) {
	m := New(testConfig(t.TempDir()), http.DefaultClient)
	m.mu.Lock()
	m.entries["c"] = &entry{status: "complete"}
	m.entries["a"] = &entry{status: "active"}
	m.entries["e"] = &entry{status: "error"}
	m.mu.Unlock()

	if !m.Forget("c") {
		t.Fatal("Forget complete want true")
	}
	if m.Forget("a") {
		t.Fatal("Forget active want false")
	}
	if !m.Forget("e") {
		t.Fatal("Forget error want true")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.entries["a"]; !ok {
		t.Fatal("active entry must remain")
	}
	if _, ok := m.entries["c"]; ok {
		t.Fatal("complete entry should be gone")
	}
}

func TestScheduleDismissComplete_RemovesAfterDelay(t *testing.T) {
	old := dismissCompleteAfter
	dismissCompleteAfter = 20 * time.Millisecond
	t.Cleanup(func() { dismissCompleteAfter = old })

	m := New(testConfig(t.TempDir()), http.DefaultClient)
	m.mu.Lock()
	m.entries["g1"] = &entry{status: "complete"}
	m.mu.Unlock()

	m.scheduleDismissComplete("g1")
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		m.mu.Lock()
		_, ok := m.entries["g1"]
		m.mu.Unlock()
		if !ok {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("complete entry was not dismissed")
}

func TestScheduleDismissComplete_KeepsErrors(t *testing.T) {
	old := dismissCompleteAfter
	dismissCompleteAfter = 5 * time.Millisecond
	t.Cleanup(func() { dismissCompleteAfter = old })

	m := New(testConfig(t.TempDir()), http.DefaultClient)
	m.mu.Lock()
	m.entries["e1"] = &entry{status: "error"}
	m.mu.Unlock()

	m.scheduleDismissComplete("e1")
	time.Sleep(30 * time.Millisecond)

	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.entries["e1"]; !ok {
		t.Fatal("error entry must not be auto-dismissed")
	}
}

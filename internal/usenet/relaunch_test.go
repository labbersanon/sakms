package usenet

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRelaunchNZB_ReusesExistingStagingDir(t *testing.T) {
	staging := t.TempDir()
	gid := "nzb-dddddddddddddddd"
	dlDir := filepath.Join(staging, gid)
	if err := os.MkdirAll(dlDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Pre-existing partial artifact in the owned staging dir — relaunch must keep it.
	leftover := filepath.Join(dlDir, "partial.bin")
	if err := os.WriteFile(leftover, []byte("partial"), 0o644); err != nil {
		t.Fatal(err)
	}

	nzbXML := `<?xml version="1.0" encoding="utf-8"?>
<!DOCTYPE nzb PUBLIC "-//newzBin//DTD NZB 1.1//EN" "http://www.newzbin.com/DTD/nzb/nzb-1.1.dtd">
<nzb xmlns="http://www.newzbin.com/DTD/2003/nzb">
  <file poster="p" date="1" subject="a.mkv">
    <groups><group>alt.binaries.test</group></groups>
    <segments>
      <segment bytes="100" number="1">seg1@test</segment>
    </segments>
  </file>
</nzb>`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-nzb")
		w.Header().Set("X-DNZB-Name", "Relaunch Test")
		_, _ = w.Write([]byte(nzbXML))
	}))
	t.Cleanup(srv.Close)

	m := New(Config{StagingDir: staging, HTTPClient: srv.Client()})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.Start(ctx)

	if err := m.RelaunchNZB(context.Background(), gid, srv.URL, "Relaunch Test"); err != nil {
		t.Fatalf("RelaunchNZB: %v", err)
	}
	live, err := m.FindByGID(gid)
	if err != nil {
		t.Fatal(err)
	}
	if live == nil {
		t.Fatal("expected live download after relaunch")
	}
	if _, err := os.Stat(leftover); err != nil {
		t.Fatalf("relaunch must reuse existing staging dir; leftover gone: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dlDir, OwnedMarkerFile)); err != nil {
		t.Fatalf("owned marker missing after relaunch: %v", err)
	}
	// Second relaunch is a no-op while live.
	if err := m.RelaunchNZB(context.Background(), gid, srv.URL, "Relaunch Test"); err != nil {
		t.Fatalf("second RelaunchNZB: %v", err)
	}
	// Refuse non-owned names.
	if err := m.RelaunchNZB(context.Background(), "../escape", srv.URL, "x"); err == nil || !strings.Contains(err.Error(), "non-owned") {
		t.Fatalf("expected non-owned refusal, got %v", err)
	}
	cancel()
	time.Sleep(20 * time.Millisecond)
}

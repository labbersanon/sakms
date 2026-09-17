package grabs

import (
	"context"
	"testing"
	"time"

	"github.com/labbersanon/sakms/internal/mode"
)

// TestTransportBackoffLadder pins the full 1→2m, 2→5m, 3→15m, 4→30m schedule
// and confirms that ≥5 returns 0 (exhausted — escalate to days ladder).
func TestTransportBackoffLadder(t *testing.T) {
	cases := []struct {
		n    int
		want time.Duration
	}{
		{1, 2 * time.Minute},
		{2, 5 * time.Minute},
		{3, 15 * time.Minute},
		{4, 30 * time.Minute},
		{5, 0}, // cap: escalate to days ladder
		{6, 0},
		{100, 0},
	}
	for _, c := range cases {
		got := TransportBackoff(c.n)
		if got != c.want {
			t.Errorf("TransportBackoff(%d) = %s, want %s", c.n, got, c.want)
		}
	}
}

// TestTransportBackoff_ZeroAtCap confirms the sentinel value is exactly 0.
func TestTransportBackoff_ZeroAtCap(t *testing.T) {
	if got := TransportBackoff(MaxTransportRetries + 1); got != 0 {
		t.Errorf("TransportBackoff(%d) = %s, want 0 (cap sentinel)", MaxTransportRetries+1, got)
	}
}

// TestParkForTransportResume_PreservesGIDAndRetryCount confirms that a
// transport park does NOT clear download_gid and does NOT advance retry_count,
// and that it increments transport_retry_count.
func TestParkForTransportResume_PreservesGIDAndRetryCount(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC)

	g, err := s.Create(ctx, Grab{
		Mode: mode.Movies, Title: "Transport Movie", TMDBID: 200,
		RootFolderPath: "/movies", DownloadURL: "https://indexer.example/nzb?id=42",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	const gid = "nzb-transport-1"
	if err := s.SetDownloadGID(ctx, g.ID, gid); err != nil {
		t.Fatalf("SetDownloadGID: %v", err)
	}

	before, err := s.Get(ctx, g.ID)
	if err != nil {
		t.Fatalf("get before: %v", err)
	}
	initialRetryCount := before.RetryCount

	after := now.Add(TransportBackoff(1))
	if err := s.ParkForTransportResume(ctx, g.ID, after, "transport drop"); err != nil {
		t.Fatalf("ParkForTransportResume: %v", err)
	}

	parked, err := s.Get(ctx, g.ID)
	if err != nil {
		t.Fatalf("get after park: %v", err)
	}
	if parked.DownloadGID != gid {
		t.Errorf("download_gid = %q, want %q (must be preserved for resume)", parked.DownloadGID, gid)
	}
	if parked.RetryCount != initialRetryCount {
		t.Errorf("retry_count = %d, want %d (transport park must not advance days ladder)", parked.RetryCount, initialRetryCount)
	}
	if parked.TransportRetryCount != 1 {
		t.Errorf("transport_retry_count = %d, want 1", parked.TransportRetryCount)
	}
	if parked.Status != PendingRetry {
		t.Errorf("status = %q, want pending_retry", parked.Status)
	}
	wantAfter := FormatTime(after)
	if parked.RetryAfter != wantAfter {
		t.Errorf("retry_after = %q, want %q", parked.RetryAfter, wantAfter)
	}
}

// TestParkForTransportResume_IncrementsTransportRetryCountEachPark verifies
// successive parks each advance transport_retry_count.
func TestParkForTransportResume_IncrementsTransportRetryCountEachPark(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC)

	g, err := s.Create(ctx, Grab{
		Mode: mode.Movies, Title: "Multi-Transport", TMDBID: 201,
		RootFolderPath: "/movies", DownloadURL: "https://indexer.example/nzb?id=43",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := s.SetDownloadGID(ctx, g.ID, "nzb-multi-1"); err != nil {
		t.Fatalf("SetDownloadGID: %v", err)
	}

	for i := 1; i <= MaxTransportRetries; i++ {
		if err := s.ParkForTransportResume(ctx, g.ID, now.Add(TransportBackoff(i)), "drop"); err != nil {
			t.Fatalf("park %d: %v", i, err)
		}
		got, err := s.Get(ctx, g.ID)
		if err != nil {
			t.Fatalf("get after park %d: %v", i, err)
		}
		if got.TransportRetryCount != i {
			t.Errorf("park %d: transport_retry_count = %d, want %d", i, got.TransportRetryCount, i)
		}
	}
}

// TestSetPendingRetry_ResetsTransportRetryCount confirms that a normal park
// (not a transport park) clears transport_retry_count back to 0.
func TestSetPendingRetry_ResetsTransportRetryCount(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC)

	g, err := s.Create(ctx, Grab{
		Mode: mode.Movies, Title: "Reset Transport", TMDBID: 202,
		RootFolderPath: "/movies", DownloadURL: "https://indexer.example/nzb?id=44",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := s.SetDownloadGID(ctx, g.ID, "nzb-reset-1"); err != nil {
		t.Fatalf("SetDownloadGID: %v", err)
	}
	if err := s.ParkForTransportResume(ctx, g.ID, now.Add(2*time.Minute), "drop"); err != nil {
		t.Fatalf("ParkForTransportResume: %v", err)
	}
	afterTransport, _ := s.Get(ctx, g.ID)
	if afterTransport.TransportRetryCount == 0 {
		t.Fatal("pre-condition: transport_retry_count should be >0 after transport park")
	}

	// Normal park (SetPendingRetry) resets transport_retry_count.
	if err := s.SetPendingRetry(ctx, g.ID, now.Add(24*time.Hour), "reset reason"); err != nil {
		t.Fatalf("SetPendingRetry: %v", err)
	}
	after, err := s.Get(ctx, g.ID)
	if err != nil {
		t.Fatalf("get after SetPendingRetry: %v", err)
	}
	if after.TransportRetryCount != 0 {
		t.Errorf("transport_retry_count = %d after SetPendingRetry, want 0", after.TransportRetryCount)
	}
	if after.DownloadGID != "" {
		t.Errorf("download_gid = %q after SetPendingRetry, want '' (cleared)", after.DownloadGID)
	}
}

// TestRelaunch_ResetsTransportRetryCount confirms Relaunch also clears
// transport_retry_count when re-arming a row as queued.
func TestRelaunch_ResetsTransportRetryCount(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC)

	g, err := s.Create(ctx, Grab{
		Mode: mode.Movies, Title: "Relaunch Reset", TMDBID: 203,
		RootFolderPath: "/movies", DownloadURL: "https://indexer.example/nzb?id=45",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := s.SetDownloadGID(ctx, g.ID, "nzb-relaunch-1"); err != nil {
		t.Fatalf("SetDownloadGID: %v", err)
	}
	if err := s.ParkForTransportResume(ctx, g.ID, now.Add(2*time.Minute), "drop"); err != nil {
		t.Fatalf("ParkForTransportResume: %v", err)
	}
	afterTransport, _ := s.Get(ctx, g.ID)
	if afterTransport.TransportRetryCount == 0 {
		t.Fatal("pre-condition: transport_retry_count should be >0 after transport park")
	}

	d := Dispatch{
		Indexer: "I", Protocol: "usenet", DownloadClient: "usenet",
		RootFolderPath: "/movies", DownloadURL: "https://indexer.example/nzb?id=45",
		GID: "nzb-relaunch-2",
	}
	if err := s.Relaunch(ctx, g.ID, d); err != nil {
		t.Fatalf("Relaunch: %v", err)
	}
	after, err := s.Get(ctx, g.ID)
	if err != nil {
		t.Fatalf("get after Relaunch: %v", err)
	}
	if after.TransportRetryCount != 0 {
		t.Errorf("transport_retry_count = %d after Relaunch, want 0", after.TransportRetryCount)
	}
	if after.Status != Queued {
		t.Errorf("status = %q after Relaunch, want queued", after.Status)
	}
	if after.RetryAfter != "" {
		t.Errorf("retry_after = %q after Relaunch, want '' (cleared)", after.RetryAfter)
	}
}

// TestDueForResume_VsDueForRetry_Disjointness proves the two due-sets are
// structurally disjoint on one fixture set: a transport-parked row (GID
// non-empty) appears in DueForResume and NOT DueForRetry; a normal-parked row
// (GID empty) appears in DueForRetry and NOT DueForResume.
func TestDueForResume_VsDueForRetry_Disjointness(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	past := time.Date(2026, 9, 17, 8, 0, 0, 0, time.UTC)  // overdue
	now := time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC)

	// Transport-parked row: has GID.
	gTransport, err := s.Create(ctx, Grab{
		Mode: mode.Movies, Title: "Transport Due", TMDBID: 300,
		RootFolderPath: "/movies", DownloadURL: "https://indexer.example/nzb?id=t1",
	})
	if err != nil {
		t.Fatalf("create transport grab: %v", err)
	}
	if err := s.SetDownloadGID(ctx, gTransport.ID, "nzb-due-t1"); err != nil {
		t.Fatalf("SetDownloadGID: %v", err)
	}
	if err := s.ParkForTransportResume(ctx, gTransport.ID, past, "transport drop"); err != nil {
		t.Fatalf("ParkForTransportResume: %v", err)
	}

	// Normal-parked row: no GID.
	gNormal, err := s.Create(ctx, Grab{
		Mode: mode.Movies, Title: "Normal Due", TMDBID: 301,
		RootFolderPath: "/movies",
		Status: PendingRetry, RetryAfter: FormatTime(past), RetryReason: "no match",
	})
	if err != nil {
		t.Fatalf("create normal grab: %v", err)
	}

	dueResume, err := s.DueForResume(ctx, now)
	if err != nil {
		t.Fatalf("DueForResume: %v", err)
	}
	dueRetry, err := s.DueForRetry(ctx, now)
	if err != nil {
		t.Fatalf("DueForRetry: %v", err)
	}

	// Transport row must be in DueForResume.
	foundInResume := false
	for _, g := range dueResume {
		if g.ID == gTransport.ID {
			foundInResume = true
		}
		if g.ID == gNormal.ID {
			t.Errorf("normal-parked row (no GID) appeared in DueForResume — sets must be disjoint")
		}
	}
	if !foundInResume {
		t.Errorf("transport-parked row not found in DueForResume")
	}

	// Normal row must be in DueForRetry.
	foundInRetry := false
	for _, g := range dueRetry {
		if g.ID == gNormal.ID {
			foundInRetry = true
		}
		if g.ID == gTransport.ID {
			t.Errorf("transport-parked row (GID non-empty) appeared in DueForRetry — sets must be disjoint")
		}
	}
	if !foundInRetry {
		t.Errorf("normal-parked row not found in DueForRetry")
	}
}

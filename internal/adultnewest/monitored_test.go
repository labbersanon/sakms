// Tests for the MonitoredStore — schema round-trips, backoff boundaries, and
// store-level logic. All tests run against a real migrated Postgres database
// (via dbtest.New) so they exercise actual SQL, not mocks.
package adultnewest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/labbersanon/sakms/internal/dbtest"
)

func newTestMonitoredStore(t *testing.T) *MonitoredStore {
	t.Helper()
	return NewMonitoredStore(dbtest.New(t))
}

// --- FormatMonitorKey / SplitMonitorKey ---

func TestFormatAndSplitMonitorKey_RoundTrips(t *testing.T) {
	cases := []struct {
		kind, source, id string
	}{
		{"performer", "tpdb", "abc123"},
		{"studio", "stashdb", "uuid-goes-here"},
		{"performer", "fansdb", "9876"},
	}
	for _, tc := range cases {
		key := FormatMonitorKey(tc.kind, tc.source, tc.id)
		k, s, id := SplitMonitorKey(key)
		if k != tc.kind || s != tc.source || id != tc.id {
			t.Errorf("SplitMonitorKey(%q) = %q, %q, %q; want %q, %q, %q", key, k, s, id, tc.kind, tc.source, tc.id)
		}
	}
}

func TestSplitMonitorKey_EmptyAndMalformed(t *testing.T) {
	for _, bad := range []string{"", "onlyone", "two\x1fparts"} {
		k, s, id := SplitMonitorKey(bad)
		if k != "" || s != "" || id != "" {
			t.Errorf("SplitMonitorKey(%q) = %q, %q, %q; want all empty", bad, k, s, id)
		}
	}
}

// --- monitorBackoff boundaries ---

func TestMonitorBackoff_Ladder(t *testing.T) {
	cases := []struct {
		emptyPolls int
		want       time.Duration
	}{
		{0, 24 * time.Hour},
		{1, 24 * time.Hour},
		{2, 24 * time.Hour},
		{3, 72 * time.Hour},
		{4, 72 * time.Hour},
		{5, 168 * time.Hour},
		{6, 168 * time.Hour},
		{7, 336 * time.Hour},
		{8, 336 * time.Hour},
		{9, 720 * time.Hour},
		{100, 720 * time.Hour},
	}
	for _, tc := range cases {
		got := monitorBackoff(tc.emptyPolls)
		if got != tc.want {
			t.Errorf("monitorBackoff(%d) = %v; want %v", tc.emptyPolls, got, tc.want)
		}
	}
}

// --- UpsertOnMonitor / GetByKindSourceID / ListMonitored ---

func TestUpsertOnMonitor_CreatesAndRetrieves(t *testing.T) {
	s := newTestMonitoredStore(t)
	ctx := context.Background()

	since := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	e, err := s.UpsertOnMonitor(ctx, "performer", "tpdb", "ent-001", "Jane Doe", "", since)
	if err != nil {
		t.Fatalf("UpsertOnMonitor: %v", err)
	}
	if e.ID == 0 || e.Kind != "performer" || e.EntitySource != "tpdb" || e.EntityID != "ent-001" || !e.Monitored {
		t.Errorf("unexpected upserted entity: %+v", e)
	}

	got, err := s.GetByKindSourceID(ctx, "performer", "tpdb", "ent-001")
	if err != nil {
		t.Fatalf("GetByKindSourceID: %v", err)
	}
	if got.ID != e.ID || got.EntityName != "Jane Doe" || !got.Monitored {
		t.Errorf("round-trip mismatch: got %+v want id=%d name=Jane Doe monitored=true", got, e.ID)
	}
}

func TestUpsertOnMonitor_ReenableDoesNotResetNextPollAt(t *testing.T) {
	s := newTestMonitoredStore(t)
	ctx := context.Background()
	now := time.Now()

	since := now.UTC().Format("2006-01-02T15:04:05.000Z")
	first, err := s.UpsertOnMonitor(ctx, "studio", "tpdb", "studio-99", "Big Studio", "", since)
	if err != nil {
		t.Fatalf("first upsert: %v", err)
	}

	// Record a poll so next_poll_at is non-empty.
	if err := s.RecordPoll(ctx, first.ID, 5, now); err != nil {
		t.Fatalf("RecordPoll: %v", err)
	}
	after, err := s.GetByKindSourceID(ctx, "studio", "tpdb", "studio-99")
	if err != nil {
		t.Fatalf("GetByKindSourceID after poll: %v", err)
	}
	if after.NextPollAt == "" {
		t.Fatal("expected NextPollAt to be set after RecordPoll")
	}
	savedNextPoll := after.NextPollAt

	// Disable then re-enable — next_poll_at must NOT be reset.
	if err := s.SetMonitored(ctx, "studio", "tpdb", "studio-99", false); err != nil {
		t.Fatalf("SetMonitored false: %v", err)
	}
	_, err = s.UpsertOnMonitor(ctx, "studio", "tpdb", "studio-99", "Big Studio", "", since)
	if err != nil {
		t.Fatalf("re-enable upsert: %v", err)
	}
	after2, err := s.GetByKindSourceID(ctx, "studio", "tpdb", "studio-99")
	if err != nil {
		t.Fatalf("GetByKindSourceID after re-enable: %v", err)
	}
	if after2.NextPollAt != savedNextPoll {
		t.Errorf("UpsertOnMonitor reset NextPollAt to %q; want unchanged %q", after2.NextPollAt, savedNextPoll)
	}
}

func TestListMonitored_OnlyReturnsMonitoredRows(t *testing.T) {
	s := newTestMonitoredStore(t)
	ctx := context.Background()
	since := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")

	_, _ = s.UpsertOnMonitor(ctx, "performer", "tpdb", "a1", "Alice", "", since)
	_, _ = s.UpsertOnMonitor(ctx, "performer", "tpdb", "b2", "Bob", "", since)
	// Disable one.
	_ = s.SetMonitored(ctx, "performer", "tpdb", "b2", false)

	list, err := s.ListMonitored(ctx)
	if err != nil {
		t.Fatalf("ListMonitored: %v", err)
	}
	if len(list) != 1 || list[0].EntityID != "a1" {
		t.Errorf("expected only a1 to be monitored; got %+v", list)
	}
}

// --- ListDue ---

func TestListDue_EmptyNextPollAtIsDue(t *testing.T) {
	s := newTestMonitoredStore(t)
	ctx := context.Background()
	since := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")

	_, _ = s.UpsertOnMonitor(ctx, "performer", "tpdb", "x1", "X One", "", since)

	due, err := s.ListDue(ctx, time.Now(), 10)
	if err != nil {
		t.Fatalf("ListDue: %v", err)
	}
	if len(due) != 1 || due[0].EntityID != "x1" {
		t.Errorf("expected x1 to be due immediately; got %+v", due)
	}
}

func TestListDue_FutureNextPollAtIsNotDue(t *testing.T) {
	s := newTestMonitoredStore(t)
	ctx := context.Background()
	now := time.Now()
	since := now.UTC().Format("2006-01-02T15:04:05.000Z")

	e, err := s.UpsertOnMonitor(ctx, "performer", "tpdb", "y1", "Y One", "", since)
	if err != nil {
		t.Fatalf("UpsertOnMonitor: %v", err)
	}
	// Record a poll with found=0 to push next_poll_at 24h into the future.
	if err := s.RecordPoll(ctx, e.ID, 0, now); err != nil {
		t.Fatalf("RecordPoll: %v", err)
	}

	// Querying with "now" (not +24h) should find nothing due.
	due, err := s.ListDue(ctx, now, 10)
	if err != nil {
		t.Fatalf("ListDue: %v", err)
	}
	for _, d := range due {
		if d.EntityID == "y1" {
			t.Errorf("y1 should not be due until 24h from now; got due: %+v", d)
		}
	}
}

func TestListDue_LimitIsHonoured(t *testing.T) {
	s := newTestMonitoredStore(t)
	ctx := context.Background()
	since := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")

	for i := 0; i < 5; i++ {
		_, _ = s.UpsertOnMonitor(ctx, "performer", "tpdb", "ent-"+string(rune('a'+i)), "Entity", "", since)
	}
	due, err := s.ListDue(ctx, time.Now(), 3)
	if err != nil {
		t.Fatalf("ListDue: %v", err)
	}
	if len(due) != 3 {
		t.Errorf("expected limit 3; got %d", len(due))
	}
}

// --- RecordPoll / backoff progression ---

func TestRecordPoll_IncrementsEmptyPollsAndBacksOff(t *testing.T) {
	s := newTestMonitoredStore(t)
	ctx := context.Background()
	now := time.Now()
	since := now.UTC().Format("2006-01-02T15:04:05.000Z")

	e, err := s.UpsertOnMonitor(ctx, "performer", "tpdb", "bp1", "Backoff Test", "", since)
	if err != nil {
		t.Fatalf("UpsertOnMonitor: %v", err)
	}

	// Three empty polls → emptyPolls=3 → backoff=72h.
	for i := 0; i < 3; i++ {
		if err := s.RecordPoll(ctx, e.ID, 0, now); err != nil {
			t.Fatalf("RecordPoll %d: %v", i, err)
		}
	}
	got, err := s.GetByKindSourceID(ctx, "performer", "tpdb", "bp1")
	if err != nil {
		t.Fatalf("GetByKindSourceID: %v", err)
	}
	if got.EmptyPolls != 3 {
		t.Errorf("expected EmptyPolls=3; got %d", got.EmptyPolls)
	}
	// NextPollAt should be approximately 72h from now.
	wantNext := now.Add(72 * time.Hour).UTC().Format("2006-01-02T15:04:05.000Z")
	if got.NextPollAt < wantNext[:10] {
		t.Errorf("NextPollAt %q is too early; expected ~72h from %v", got.NextPollAt, now)
	}
}

func TestRecordPoll_ResetsEmptyPollsOnMatch(t *testing.T) {
	s := newTestMonitoredStore(t)
	ctx := context.Background()
	now := time.Now()
	since := now.UTC().Format("2006-01-02T15:04:05.000Z")

	e, err := s.UpsertOnMonitor(ctx, "performer", "tpdb", "res1", "Reset Test", "", since)
	if err != nil {
		t.Fatalf("UpsertOnMonitor: %v", err)
	}
	// Build up 5 empty polls.
	for i := 0; i < 5; i++ {
		_ = s.RecordPoll(ctx, e.ID, 0, now)
	}
	// One poll with found>0 resets the counter.
	if err := s.RecordPoll(ctx, e.ID, 3, now); err != nil {
		t.Fatalf("RecordPoll with found=3: %v", err)
	}
	got, err := s.GetByKindSourceID(ctx, "performer", "tpdb", "res1")
	if err != nil {
		t.Fatalf("GetByKindSourceID: %v", err)
	}
	if got.EmptyPolls != 0 {
		t.Errorf("expected EmptyPolls reset to 0 after a match; got %d", got.EmptyPolls)
	}
}

// --- SetMonitored clears monitored_since on disable ---

func TestSetMonitored_ClearsMonitoredSinceOnDisable(t *testing.T) {
	s := newTestMonitoredStore(t)
	ctx := context.Background()
	since := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")

	_, _ = s.UpsertOnMonitor(ctx, "performer", "tpdb", "ms1", "Since Test", "", since)

	if err := s.SetMonitored(ctx, "performer", "tpdb", "ms1", false); err != nil {
		t.Fatalf("SetMonitored false: %v", err)
	}
	got, err := s.GetByKindSourceID(ctx, "performer", "tpdb", "ms1")
	if err != nil {
		t.Fatalf("GetByKindSourceID: %v", err)
	}
	if got.Monitored {
		t.Error("expected Monitored=false after SetMonitored(false)")
	}
	if got.MonitoredSince != "" {
		t.Errorf("expected MonitoredSince cleared; got %q", got.MonitoredSince)
	}
}

func TestSetMonitored_NotFound(t *testing.T) {
	s := newTestMonitoredStore(t)
	ctx := context.Background()

	err := s.SetMonitored(ctx, "performer", "tpdb", "does-not-exist", false)
	if !errors.Is(err, ErrMonitoredNotFound) {
		t.Errorf("expected ErrMonitoredNotFound; got %v", err)
	}
}

// --- GetByKindName ---

func TestGetByKindName_CaseInsensitive(t *testing.T) {
	s := newTestMonitoredStore(t)
	ctx := context.Background()
	since := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")

	_, _ = s.UpsertOnMonitor(ctx, "performer", "tpdb", "ci1", "Jane Doe", "", since)

	// Lookup with different casing should still find it.
	got, err := s.GetByKindName(ctx, "performer", "JANE DOE")
	if err != nil {
		t.Fatalf("GetByKindName (upper): %v", err)
	}
	if got.EntityID != "ci1" {
		t.Errorf("expected ci1; got %q", got.EntityID)
	}
}

func TestGetByKindName_NotFound(t *testing.T) {
	s := newTestMonitoredStore(t)
	ctx := context.Background()

	_, err := s.GetByKindName(ctx, "performer", "NoSuchPerson")
	if !errors.Is(err, ErrMonitoredNotFound) {
		t.Errorf("expected ErrMonitoredNotFound; got %v", err)
	}
}

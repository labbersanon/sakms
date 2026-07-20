package recheck

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/labbersanon/sakms/internal/connections"
	"github.com/labbersanon/sakms/internal/db"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/secrets"
	"github.com/labbersanon/sakms/internal/settings"
)

// newTestStores builds a WatchStore, a connections.Store, and a settings.Store
// all backed by the SAME freshly-migrated SQLite file — real SQL and real
// encryption, no mocks, matching the repo's store-test convention.
func newTestStores(t *testing.T) (*WatchStore, *connections.Store, *settings.Store) {
	t.Helper()
	sqlDB, err := db.Open(filepath.Join(t.TempDir(), "sakms.db"))
	if err != nil {
		t.Fatalf("opening db: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })

	secretStore, err := secrets.New(make([]byte, 32))
	if err != nil {
		t.Fatalf("building secret store: %v", err)
	}
	return NewWatchStore(sqlDB), connections.New(sqlDB, secretStore), settings.New(sqlDB)
}

// fakeProwlarr serves Prowlarr's /api/v1/search, returning body verbatim (a
// JSON array of releaseResource objects) for any query — enough to make an
// availability probe report "available" (non-empty) or "unavailable" ("[]").
func fakeProwlarr(t *testing.T, body string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/search" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(body))
	}))
}

// TestRunCycle_FlipsFlagWhenAvailable drives the extracted single-tick logic
// directly (no ticker, no sleep): an Adult watch entry — the one probe shape
// that needs only Prowlarr, no TMDB — starts unavailable, and one cycle
// against a Prowlarr returning a release flips last_available to true and
// stamps last_checked_at.
func TestRunCycle_FlipsFlagWhenAvailable(t *testing.T) {
	watchStore, connStore, _ := newTestStores(t)
	ctx := context.Background()

	prow := fakeProwlarr(t, `[{"guid":"abc","title":"Studio X - Some Scene","protocol":"torrent","seeders":10}]`)
	defer prow.Close()
	if err := connStore.Upsert(ctx, "prowlarr", prow.URL, "key"); err != nil {
		t.Fatalf("configuring prowlarr: %v", err)
	}

	w, err := watchStore.Add(ctx, Watch{Mode: mode.Adult, Studio: "Studio X", Title: "Some Scene"})
	if err != nil {
		t.Fatalf("add watch: %v", err)
	}

	runCycle(ctx, prow.Client(), connStore, watchStore, time.Now().Add(-time.Hour))

	got := reload(t, watchStore, w.ID)
	if !got.LastAvailable {
		t.Errorf("expected last_available to flip to true after an available probe")
	}
	if got.LastCheckedAt == "" {
		t.Errorf("expected last_checked_at to be stamped")
	}
}

// TestRunCycle_UnavailableRecordsCheck confirms an empty Prowlarr result still
// records the check (last_checked_at set) with last_available false.
func TestRunCycle_UnavailableRecordsCheck(t *testing.T) {
	watchStore, connStore, _ := newTestStores(t)
	ctx := context.Background()

	prow := fakeProwlarr(t, `[]`)
	defer prow.Close()
	if err := connStore.Upsert(ctx, "prowlarr", prow.URL, "key"); err != nil {
		t.Fatalf("configuring prowlarr: %v", err)
	}

	w, err := watchStore.Add(ctx, Watch{Mode: mode.Adult, Studio: "S", Title: "Nothing Here"})
	if err != nil {
		t.Fatalf("add watch: %v", err)
	}

	runCycle(ctx, prow.Client(), connStore, watchStore, time.Now().Add(-time.Hour))

	got := reload(t, watchStore, w.ID)
	if got.LastAvailable {
		t.Errorf("expected last_available false for an empty probe")
	}
	if got.LastCheckedAt == "" {
		t.Errorf("expected the check to be recorded even when unavailable")
	}
}

// TestRunCycle_NoProwlarrConnectionSkips confirms that with no Prowlarr
// configured the cycle is a clean no-op — the entry stays unchecked rather
// than erroring.
func TestRunCycle_NoProwlarrConnectionSkips(t *testing.T) {
	watchStore, connStore, _ := newTestStores(t)
	ctx := context.Background()

	w, err := watchStore.Add(ctx, Watch{Mode: mode.Adult, Studio: "S", Title: "T"})
	if err != nil {
		t.Fatalf("add watch: %v", err)
	}

	runCycle(ctx, &http.Client{Timeout: time.Second}, connStore, watchStore, time.Now().Add(-time.Hour))

	got := reload(t, watchStore, w.ID)
	if got.LastCheckedAt != "" {
		t.Errorf("expected the entry to stay unchecked with no prowlarr configured, got last_checked_at %q", got.LastCheckedAt)
	}
}

// TestRunCycle_OnlyChecksDueEntries confirms a recently-checked entry is left
// untouched (its stale stored result is not overwritten) while a never-checked
// one is probed.
func TestRunCycle_OnlyChecksDueEntries(t *testing.T) {
	watchStore, connStore, _ := newTestStores(t)
	ctx := context.Background()

	prow := fakeProwlarr(t, `[{"guid":"abc","title":"x","protocol":"torrent"}]`)
	defer prow.Close()
	if err := connStore.Upsert(ctx, "prowlarr", prow.URL, "key"); err != nil {
		t.Fatalf("configuring prowlarr: %v", err)
	}

	// One entry checked "now" with a stale false; one never checked.
	recent, err := watchStore.Add(ctx, Watch{Mode: mode.Adult, Studio: "R", Title: "Recent"})
	if err != nil {
		t.Fatalf("add recent: %v", err)
	}
	nowStamp := time.Now().UTC().Format(time.RFC3339Nano)
	if err := watchStore.UpdateResult(ctx, recent.ID, false, nowStamp); err != nil {
		t.Fatalf("seed recent: %v", err)
	}
	fresh, err := watchStore.Add(ctx, Watch{Mode: mode.Adult, Studio: "F", Title: "Fresh"})
	if err != nil {
		t.Fatalf("add fresh: %v", err)
	}

	// Interval 1h: the recent entry (checked "now") is NOT due; the fresh one is.
	runCycle(ctx, prow.Client(), connStore, watchStore, time.Now().Add(-time.Hour))

	gotRecent := reload(t, watchStore, recent.ID)
	if gotRecent.LastAvailable {
		t.Errorf("expected the recently-checked entry to be left untouched (still false), got true")
	}
	if gotRecent.LastCheckedAt != nowStamp {
		t.Errorf("expected the recently-checked entry's timestamp unchanged, got %q", gotRecent.LastCheckedAt)
	}
	gotFresh := reload(t, watchStore, fresh.ID)
	if !gotFresh.LastAvailable {
		t.Errorf("expected the never-checked entry to be probed and flip to available")
	}
}

func TestLoadInterval(t *testing.T) {
	_, _, settingsStore := newTestStores(t)
	ctx := context.Background()

	// Unset → off.
	if got := LoadInterval(ctx, settingsStore); got != 0 {
		t.Errorf("expected unset interval to be 0, got %s", got)
	}

	cases := []struct {
		stored string
		want   time.Duration
	}{
		{"0", 0},
		{"-5", 0},
		{"not-a-number", 0},
		{"", 0},
		{"  60  ", 60 * time.Second},
		{"900", 900 * time.Second},
	}
	for _, c := range cases {
		if err := settingsStore.Set(ctx, IntervalSettingKey, c.stored); err != nil {
			t.Fatalf("set %q: %v", c.stored, err)
		}
		if got := LoadInterval(ctx, settingsStore); got != c.want {
			t.Errorf("LoadInterval(%q) = %s, want %s", c.stored, got, c.want)
		}
	}
}

// reload fetches the single watch entry with id from the store, failing the
// test if it's gone.
func reload(t *testing.T, s *WatchStore, id int64) Watch {
	t.Helper()
	all, err := s.List(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, w := range all {
		if w.ID == id {
			return w
		}
	}
	t.Fatalf("watch entry %d not found", id)
	return Watch{}
}

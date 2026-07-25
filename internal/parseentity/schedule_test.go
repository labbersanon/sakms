package parseentity

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/labbersanon/sakms/internal/connections"
	"github.com/labbersanon/sakms/internal/db"
	"github.com/labbersanon/sakms/internal/secrets"
	"github.com/labbersanon/sakms/internal/settings"
)

// newTestSettingsStore builds a settings.Store backed by a freshly-migrated
// SQLite file — real SQL, no mocks, matching the repo's store-test
// convention (see internal/recheck/recheck_test.go's newTestStores).
func newTestSettingsStore(t *testing.T) *settings.Store {
	t.Helper()
	sqlDB, err := db.Open(filepath.Join(t.TempDir(), "sakms.db"))
	if err != nil {
		t.Fatalf("opening db: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	return settings.New(sqlDB)
}

func TestLoadInterval(t *testing.T) {
	settingsStore := newTestSettingsStore(t)
	ctx := context.Background()

	// Unset → off. Entity sync was purely manual before this job existed, so
	// an unset key must NOT read as active (unlike adultnewest's
	// default-active convention).
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
		{"21600", 21600 * time.Second},
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

// TestRun_ZeroIntervalStartsNothing confirms the top-of-function opt-in gate:
// calling Run with interval <= 0 returns immediately without blocking, since
// main calls this unconditionally at boot.
func TestRun_ZeroIntervalStartsNothing(t *testing.T) {
	settingsStore := newTestSettingsStore(t)
	done := make(chan struct{})
	go func() {
		Run(context.Background(), 0, nil, settingsStore, nil)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run with interval <= 0 did not return immediately")
	}
}

// TestRun_BootPollFiresBeforeInterval proves the immediate boot poll: with a
// long interval and a configured Stash source, the entity cache is synced at
// t≈0, before the first tick would ever elapse. Without the boot poll, a 24h
// ticker in this fast-redeploy deployment never actually fired between
// redeploys, leaving the entity DB empty. Mirrors adultnewest's boot-poll test.
func TestRun_BootPollFiresBeforeInterval(t *testing.T) {
	sqlDB, err := db.Open(filepath.Join(t.TempDir(), "sakms.db"))
	if err != nil {
		t.Fatalf("opening db: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	secretStore, err := secrets.New(make([]byte, 32))
	if err != nil {
		t.Fatalf("building secret store: %v", err)
	}
	connStore := connections.New(sqlDB, secretStore)
	settingsStore := settings.New(sqlDB)
	entityStore := NewSQLiteStore(sqlDB)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	hit := make(chan struct{}, 1)
	stash := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case hit <- struct{}{}:
		default:
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":{"findScenes":{"scenes":[]}}}`))
	}))
	t.Cleanup(stash.Close)
	if err := connStore.Upsert(ctx, "stash", stash.URL, ""); err != nil {
		t.Fatalf("configuring stash: %v", err)
	}

	// A one-hour interval: if the boot poll didn't fire, the Stash source would
	// not be synced for an hour — the test would time out at 2s instead.
	go Run(ctx, time.Hour, connStore, settingsStore, entityStore)

	select {
	case <-hit:
		// Boot poll fired — success.
	case <-time.After(2 * time.Second):
		t.Fatal("stash was not synced within 2s — the entity-sync boot poll did not fire before the interval")
	}
}

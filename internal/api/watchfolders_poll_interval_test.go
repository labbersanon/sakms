package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/labbersanon/sakms/internal/db"
	"github.com/labbersanon/sakms/internal/settings"
)

// testSettingsStore builds a bare *settings.Store backed by its own sqlite
// DB, for in-package unit tests (like TestPollInterval_Substitution below)
// that need a real settings store but don't need the full HTTP mux/testStores
// wiring.
func testSettingsStore(t *testing.T) *settings.Store {
	t.Helper()
	sqlDB, err := db.Open(filepath.Join(t.TempDir(), "sakms.db"))
	if err != nil {
		t.Fatalf("opening db: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	return settings.New(sqlDB)
}

// TestWatchFoldersPollInterval_RoundTrip drives the real mux: GET on a blank
// install returns 0 (unset — collapses to defaultWatchPollInterval at
// runtime, see pollInterval), a PUT stores and reads back.
func TestWatchFoldersPollInterval_RoundTrip(t *testing.T) {
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), nil, rssFeedsStore, nil, nil, nil, nil, nil, nil))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/settings/watch-folders-poll-interval")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	var got watchFoldersPollIntervalResponse
	json.NewDecoder(resp.Body).Decode(&got)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 on unset GET, got %d", resp.StatusCode)
	}
	if got.IntervalSeconds != 0 {
		t.Errorf("expected the unset interval to be 0, got %d", got.IntervalSeconds)
	}

	body, _ := json.Marshal(watchFoldersPollIntervalRequest{IntervalSeconds: 3600})
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/settings/watch-folders-poll-interval", bytes.NewReader(body))
	putResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT failed: %v", err)
	}
	putResp.Body.Close()
	if putResp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", putResp.StatusCode)
	}

	resp2, _ := http.Get(srv.URL + "/api/settings/watch-folders-poll-interval")
	var got2 watchFoldersPollIntervalResponse
	json.NewDecoder(resp2.Body).Decode(&got2)
	resp2.Body.Close()
	if got2.IntervalSeconds != 3600 {
		t.Errorf("expected round-tripped interval 3600, got %d", got2.IntervalSeconds)
	}
}

// TestWatchFoldersPollInterval_NoFloor proves the inverse of
// TestScanInterval_BelowFloorRejected — this endpoint has no floor, so a
// small positive value like 5 is accepted, not rejected. Negative and
// malformed bodies are still 400s.
func TestWatchFoldersPollInterval_NoFloor(t *testing.T) {
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), nil, rssFeedsStore, nil, nil, nil, nil, nil, nil))
	defer srv.Close()

	for _, tc := range []struct {
		name   string
		body   []byte
		wantOK bool
	}{
		{"small positive value accepted (no floor)", mustMarshal(t, watchFoldersPollIntervalRequest{IntervalSeconds: 5}), true},
		{"negative rejected", mustMarshal(t, watchFoldersPollIntervalRequest{IntervalSeconds: -1}), false},
		{"malformed JSON rejected", []byte("not json"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/settings/watch-folders-poll-interval", bytes.NewReader(tc.body))
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("PUT failed: %v", err)
			}
			resp.Body.Close()
			gotOK := resp.StatusCode == http.StatusNoContent
			if gotOK != tc.wantOK {
				t.Errorf("got status %d (ok=%v), want ok=%v", resp.StatusCode, gotOK, tc.wantOK)
			}
			if !tc.wantOK && resp.StatusCode != http.StatusBadRequest {
				t.Errorf("expected 400 on rejection, got %d", resp.StatusCode)
			}
		})
	}
}

func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

// TestPollInterval_Substitution is the critical proof that a stored
// 0/negative/garbage value can never produce a zero-duration timer in
// RunWatchFolders/runWatcher — pollInterval must always substitute
// defaultWatchPollInterval in those cases.
func TestPollInterval_Substitution(t *testing.T) {
	ctx := context.Background()

	for _, tc := range []struct {
		name  string
		store string // "" means leave the key unset
		want  time.Duration
	}{
		{"unset", "", 30 * time.Second},
		{"stored zero", "0", 30 * time.Second},
		{"stored negative", "-5", 30 * time.Second},
		{"stored small positive", "5", 5 * time.Second},
		{"stored large positive", "3600", 3600 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := testSettingsStore(t)
			if tc.store != "" {
				if err := s.Set(ctx, watchPollIntervalKey, tc.store); err != nil {
					t.Fatalf("seeding settings store: %v", err)
				}
			}

			got := pollInterval(ctx, s)
			if got != tc.want {
				t.Errorf("pollInterval() = %v, want %v", got, tc.want)
			}
			if got <= 0 {
				t.Fatalf("pollInterval() returned non-positive duration %v — would busy-loop a timer", got)
			}
		})
	}
}

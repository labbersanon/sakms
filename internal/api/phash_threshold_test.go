package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/phash"
)

// TestResolvePHashThreshold_VersionGate proves the stored-threshold scale gate:
// only a value tagged with the CURRENT phash.PerFrameBits is honored; a legacy
// bare int and a wrong-scale tag both fall back to the mode default rather than
// being reinterpreted on the PDQ scale.
func TestResolvePHashThreshold_VersionGate(t *testing.T) {
	_, _, _, settingsStore, _, _, _, _, _, _, _ := testStores(t)
	ctx := context.Background()
	key := phashThresholdKey(mode.Movies)
	def := phashModeDefault(mode.Movies)

	cases := []struct {
		name   string
		stored string
		want   int
	}{
		{"unset falls back to default", "", def},
		{"legacy bare int is stale-scale", "30", def},
		{"wrong scale (64-bit) is stale", "64:30", def},
		{"non-numeric scale token is stale", "phash:30", def},
		{"current scale is honored", fmtScale(phash.PerFrameBits, 100), 100},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := settingsStore.Set(ctx, key, tc.stored); err != nil {
				t.Fatalf("seeding %q: %v", tc.stored, err)
			}
			got, err := resolvePHashThreshold(ctx, settingsStore, mode.Movies)
			if err != nil {
				t.Fatalf("resolve: %v", err)
			}
			if got != tc.want {
				t.Errorf("stored %q: expected %d, got %d", tc.stored, tc.want, got)
			}
		})
	}
}

// TestSweepStalePHashThresholds proves the one-time boot reset: stale-scale and
// legacy values are cleared (so a later read yields the default), while a
// current-scale value is left untouched.
func TestSweepStalePHashThresholds(t *testing.T) {
	_, _, _, settingsStore, _, _, _, _, _, _, _ := testStores(t)
	ctx := context.Background()

	if err := settingsStore.Set(ctx, phashThresholdKey(mode.Movies), "64:30"); err != nil {
		t.Fatalf("seed movies: %v", err)
	}
	if err := settingsStore.Set(ctx, phashThresholdKey(mode.Series), "30"); err != nil {
		t.Fatalf("seed series: %v", err)
	}
	current := fmtScale(phash.PerFrameBits, 120)
	if err := settingsStore.Set(ctx, phashThresholdKey(mode.Adult), current); err != nil {
		t.Fatalf("seed adult: %v", err)
	}

	SweepStalePHashThresholds(ctx, settingsStore)

	for _, m := range []mode.Mode{mode.Movies, mode.Series} {
		raw, err := settingsStore.Get(ctx, phashThresholdKey(m))
		if err != nil {
			t.Fatalf("get %s: %v", m, err)
		}
		if raw != "" {
			t.Errorf("%s: expected stale value cleared to empty, got %q", m, raw)
		}
	}
	adultRaw, err := settingsStore.Get(ctx, phashThresholdKey(mode.Adult))
	if err != nil {
		t.Fatalf("get adult: %v", err)
	}
	if adultRaw != current {
		t.Errorf("adult: expected current-scale value %q left untouched, got %q", current, adultRaw)
	}
}

func fmtScale(scale, v int) string {
	return strconv.Itoa(scale) + ":" + strconv.Itoa(v)
}

func TestGetPHashThresholdHandler_DefaultsToDefault(t *testing.T) {
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), nil, rssFeedsStore, nil, nil, nil, nil, nil))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/modes/movies/phash-threshold")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var got phashThresholdResponse
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if got.Threshold != phash.DefaultMoviesThreshold {
		t.Errorf("expected movies default threshold %d, got %d", phash.DefaultMoviesThreshold, got.Threshold)
	}
}

// TestPutPHashThresholdHandler_RejectsOutOfRange exercises the width-derived
// bound: phash.PerFrameBits (256 under PDQ) is the top of the valid range, so
// PerFrameBits itself is accepted and PerFrameBits+1 is rejected. The bound is
// derived from the constant, not a fixed 64/65, so these cases track the active
// algorithm's width automatically.
func TestPutPHashThresholdHandler_RejectsOutOfRange(t *testing.T) {
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), nil, rssFeedsStore, nil, nil, nil, nil, nil))
	defer srv.Close()

	cases := []struct {
		name       string
		threshold  int
		wantStatus int
	}{
		{"zero accepted", 0, http.StatusNoContent},
		{"width bound accepted", phash.PerFrameBits, http.StatusNoContent},
		{"one over width rejected", phash.PerFrameBits + 1, http.StatusBadRequest},
		{"negative rejected", -1, http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body, _ := json.Marshal(phashThresholdRequest{Threshold: tc.threshold})
			req, err := http.NewRequest(http.MethodPut, srv.URL+"/api/modes/movies/phash-threshold", bytes.NewReader(body))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != tc.wantStatus {
				t.Fatalf("threshold %d: expected %d, got %d", tc.threshold, tc.wantStatus, resp.StatusCode)
			}
		})
	}
}

// TestPutThenGetPHashThreshold_PDQScaleRoundTrips proves a PDQ-scale value the
// old 0–64 bound would have rejected (100) now round-trips through the widened
// bound and the scale-tagged store unchanged.
func TestPutThenGetPHashThreshold_PDQScaleRoundTrips(t *testing.T) {
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), nil, rssFeedsStore, nil, nil, nil, nil, nil))
	defer srv.Close()

	body, _ := json.Marshal(phashThresholdRequest{Threshold: 100})
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/modes/movies/phash-threshold", bytes.NewReader(body))
	putResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT failed: %v", err)
	}
	putResp.Body.Close()
	if putResp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204 from PUT of a PDQ-scale value, got %d", putResp.StatusCode)
	}

	getResp, err := http.Get(srv.URL + "/api/modes/movies/phash-threshold")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer getResp.Body.Close()
	var got phashThresholdResponse
	if err := json.NewDecoder(getResp.Body).Decode(&got); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if got.Threshold != 100 {
		t.Errorf("expected the stored PDQ-scale threshold 100 to round-trip, got %d", got.Threshold)
	}
}

func TestPutThenGetPHashThreshold_RoundTrips(t *testing.T) {
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), nil, rssFeedsStore, nil, nil, nil, nil, nil))
	defer srv.Close()

	body, _ := json.Marshal(phashThresholdRequest{Threshold: 8})
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/modes/movies/phash-threshold", bytes.NewReader(body))
	putResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT failed: %v", err)
	}
	putResp.Body.Close()
	if putResp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204 from PUT, got %d", putResp.StatusCode)
	}

	getResp, err := http.Get(srv.URL + "/api/modes/movies/phash-threshold")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer getResp.Body.Close()
	var got phashThresholdResponse
	if err := json.NewDecoder(getResp.Body).Decode(&got); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if got.Threshold != 8 {
		t.Errorf("expected the stored threshold 8 to round-trip, got %d", got.Threshold)
	}
}

// The threshold endpoint is mode-generic ({mode} is resolved from the path), so
// these prove the series_phash_dedup_threshold key path works exactly as the
// movies one does — Series Dedup gained a phash threshold with zero new routing.
func TestPutThenGetPHashThreshold_Series_RoundTrips(t *testing.T) {
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), nil, rssFeedsStore, nil, nil, nil, nil, nil))
	defer srv.Close()

	body, _ := json.Marshal(phashThresholdRequest{Threshold: 8})
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/modes/series/phash-threshold", bytes.NewReader(body))
	putResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT failed: %v", err)
	}
	putResp.Body.Close()
	if putResp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204 from PUT, got %d", putResp.StatusCode)
	}

	getResp, err := http.Get(srv.URL + "/api/modes/series/phash-threshold")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer getResp.Body.Close()
	var got phashThresholdResponse
	if err := json.NewDecoder(getResp.Body).Decode(&got); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if got.Threshold != 8 {
		t.Errorf("expected the stored series threshold 8 to round-trip, got %d", got.Threshold)
	}
}

func TestPutPHashThresholdHandler_Series_RejectsOutOfRange(t *testing.T) {
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), nil, rssFeedsStore, nil, nil, nil, nil, nil))
	defer srv.Close()

	body, _ := json.Marshal(phashThresholdRequest{Threshold: phash.PerFrameBits + 1})
	req, err := http.NewRequest(http.MethodPut, srv.URL+"/api/modes/series/phash-threshold", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for an out-of-range series threshold, got %d", resp.StatusCode)
	}
}

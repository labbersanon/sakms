package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestAdultModeEnabled_UnsetDefaultsToFalseOnBlankInstall confirms a blank
// install (no Adult root folder set, key never written) reports disabled.
func TestAdultModeEnabled_UnsetDefaultsToFalseOnBlankInstall(t *testing.T) {
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, nil, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), rssFeedsStore, nil, nil, nil, nil, nil, nil))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/settings/adult-mode-enabled")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var got adultModeEnabledResponse
	json.NewDecoder(resp.Body).Decode(&got)
	if got.Enabled != false {
		t.Errorf("expected disabled on a blank install, got enabled=%v", got.Enabled)
	}
}

// TestAdultModeEnabled_UnsetDefaultsToTrueWhenRootFolderConfigured confirms
// that when the Adult library root folder IS set, and adult_mode_enabled
// was never explicitly written, the computed default is enabled.
func TestAdultModeEnabled_UnsetDefaultsToTrueWhenRootFolderConfigured(t *testing.T) {
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, nil, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), rssFeedsStore, nil, nil, nil, nil, nil, nil))
	defer srv.Close()

	// Seed the Adult root folder directly via the settings store, without
	// ever explicitly writing adult_mode_enabled.
	if err := settingsStore.Set(context.Background(), adultLibraryRootFolderKey, "/data/adult"); err != nil {
		t.Fatalf("seeding root folder: %v", err)
	}

	resp, err := http.Get(srv.URL + "/api/settings/adult-mode-enabled")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	var got adultModeEnabledResponse
	json.NewDecoder(resp.Body).Decode(&got)
	if got.Enabled != true {
		t.Errorf("expected enabled when the Adult root folder is configured, got enabled=%v", got.Enabled)
	}
}

// TestAdultModeEnabled_ExplicitFalseSurvivesConfiguredRootFolder confirms an
// explicit PUT{enabled:false} always wins over the computed default, even
// while the Adult root folder remains configured.
func TestAdultModeEnabled_ExplicitFalseSurvivesConfiguredRootFolder(t *testing.T) {
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, nil, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), rssFeedsStore, nil, nil, nil, nil, nil, nil))
	defer srv.Close()

	if err := settingsStore.Set(context.Background(), adultLibraryRootFolderKey, "/data/adult"); err != nil {
		t.Fatalf("seeding root folder: %v", err)
	}

	body, _ := json.Marshal(adultModeEnabledRequest{Enabled: false})
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/settings/adult-mode-enabled", bytes.NewReader(body))
	putResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT failed: %v", err)
	}
	putResp.Body.Close()
	if putResp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", putResp.StatusCode)
	}

	resp, err := http.Get(srv.URL + "/api/settings/adult-mode-enabled")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	var got adultModeEnabledResponse
	json.NewDecoder(resp.Body).Decode(&got)
	if got.Enabled != false {
		t.Errorf("expected explicit false to survive a still-configured root folder, got enabled=%v", got.Enabled)
	}
}

// TestAdultModeEnabled_MalformedStoredValueFallsThroughToComputedDefault
// confirms a garbage stored value doesn't error the GET — it falls through
// to the computed default, mirroring resolveAdultIdentifyEnabled's existing
// tolerance of a malformed stored value.
func TestAdultModeEnabled_MalformedStoredValueFallsThroughToComputedDefault(t *testing.T) {
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, nil, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), rssFeedsStore, nil, nil, nil, nil, nil, nil))
	defer srv.Close()

	if err := settingsStore.Set(context.Background(), adultLibraryRootFolderKey, "/data/adult"); err != nil {
		t.Fatalf("seeding root folder: %v", err)
	}
	if err := settingsStore.Set(context.Background(), AdultModeEnabledKey, "not-a-bool"); err != nil {
		t.Fatalf("seeding malformed value: %v", err)
	}

	resp, err := http.Get(srv.URL + "/api/settings/adult-mode-enabled")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 even with a malformed stored value, got %d", resp.StatusCode)
	}
	var got adultModeEnabledResponse
	json.NewDecoder(resp.Body).Decode(&got)
	if got.Enabled != true {
		t.Errorf("expected fallthrough to the computed default (root folder configured -> true), got enabled=%v", got.Enabled)
	}
}

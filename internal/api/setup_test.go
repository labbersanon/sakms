package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labbersanon/sakms/internal/mode"
)

// TestSetupStatus_ReflectsRealConfiguredState exercises the real read model
// a future wizard would poll: nothing configured, then Movies' library root
// folder setting and a Purge allowlist entry for Movies, confirming the
// endpoint's numbers track the actual settings/allowlist stores rather than
// a cached snapshot.
func TestSetupStatus_ReflectsRealConfiguredState(t *testing.T) {
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, nil, nil, nil, nil, nil))
	defer srv.Close()

	// Before anything is configured.
	resp, err := http.Get(srv.URL + "/api/setup/status")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	var status setupStatus
	json.NewDecoder(resp.Body).Decode(&status)
	resp.Body.Close()
	if status.AnyConfigured {
		t.Fatalf("expected AnyConfigured=false on a blank install, got %+v", status)
	}
	if len(status.Modes) != 3 {
		t.Fatalf("expected all 3 modes reported, got %+v", status.Modes)
	}
	var adult, movies modeStatus
	for _, m := range status.Modes {
		if m.Mode == mode.Adult {
			adult = m
		}
		if m.Mode == mode.Movies {
			movies = m
		}
	}
	if !adult.Available || adult.ArrConfigured {
		t.Errorf("expected Adult available but not yet configured, got %+v", adult)
	}
	if !movies.Available || movies.ArrConfigured {
		t.Errorf("expected Movies available but not yet configured, got %+v", movies)
	}

	// Configure Movies' library root folder and an allowlist entry.
	if err := settingsStore.Set(context.Background(), moviesLibraryRootFolderKey, "/media/Movies"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := allowStore.Add(context.Background(), mode.Movies, "BDSM"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	resp2, err := http.Get(srv.URL + "/api/setup/status")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp2.Body.Close()
	var status2 setupStatus
	json.NewDecoder(resp2.Body).Decode(&status2)
	if !status2.AnyConfigured {
		t.Fatal("expected AnyConfigured=true once Movies' library root folder is configured")
	}
	for _, m := range status2.Modes {
		if m.Mode == mode.Movies {
			if !m.ArrConfigured || m.AllowlistCount != 1 {
				t.Errorf("expected Movies to show arrConfigured=true, allowlistCount=1, got %+v", m)
			}
		}
	}
}

func TestSetupStatus_JellyfinAndOllamaConnectionPresence(t *testing.T) {
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	if err := connStore.Upsert(context.Background(), "jellyfin", "http://192.168.1.20:8096", ""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := connStore.Upsert(context.Background(), "ollama", "http://127.0.0.1:11434", ""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, nil, nil, nil, nil, nil))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/setup/status")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	var status setupStatus
	json.NewDecoder(resp.Body).Decode(&status)
	if !status.JellyfinConfigured || !status.OllamaConfigured {
		t.Errorf("expected both Jellyfin and Ollama reported configured, got %+v", status)
	}
}

func TestDismissSetup_PersistsAndReflectsInStatus(t *testing.T) {
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, nil, nil, nil, nil, nil))
	defer srv.Close()

	body, _ := json.Marshal(dismissSetupRequest{Dismissed: true})
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/setup/dismissed", bytes.NewReader(body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", resp.StatusCode)
	}

	statusResp, err := http.Get(srv.URL + "/api/setup/status")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer statusResp.Body.Close()
	var status setupStatus
	json.NewDecoder(statusResp.Body).Decode(&status)
	if !status.Dismissed {
		t.Fatal("expected Dismissed=true to persist and show up in status")
	}
}

func TestDismissSetup_InvalidBody(t *testing.T) {
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, nil, nil, nil, nil, nil))
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/setup/dismissed", bytes.NewReader([]byte("not json")))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for a malformed body, got %d", resp.StatusCode)
	}
}

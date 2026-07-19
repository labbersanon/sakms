package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// aiModelEndpoints is a single-entry table (kept as a table, not a bare
// constant, so each test case gets its own t.Run subtest name) for the one
// shared AI model setting — Adult identification and Movies/Series Rename's
// AI fallback both read mode.AIModelKey via this one endpoint.
var aiModelEndpoints = []struct {
	name string
	path string
}{
	{"shared", "/api/settings/ai-model"},
}

// TestAIModel_RoundTrip drives the real mux: GET on a blank install returns
// an empty model, PUT stores it, and a follow-up GET reads it back.
func TestAIModel_RoundTrip(t *testing.T) {
	for _, ep := range aiModelEndpoints {
		t.Run(ep.name, func(t *testing.T) {
			connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
			srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, nil, nil, nil, nil, nil))
			defer srv.Close()

			// Unset is a normal state: GET returns 200 with an empty model.
			resp, err := http.Get(srv.URL + ep.path)
			if err != nil {
				t.Fatalf("GET failed: %v", err)
			}
			var got aiModelResponse
			json.NewDecoder(resp.Body).Decode(&got)
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("expected 200 on unset GET, got %d", resp.StatusCode)
			}
			if got.Model != "" {
				t.Errorf("expected empty model before anything is set, got %q", got.Model)
			}

			// PUT stores it.
			body, _ := json.Marshal(aiModelRequest{Model: "qwen2.5vl:7b"})
			req, _ := http.NewRequest(http.MethodPut, srv.URL+ep.path, bytes.NewReader(body))
			putResp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("PUT failed: %v", err)
			}
			putResp.Body.Close()
			if putResp.StatusCode != http.StatusNoContent {
				t.Fatalf("expected 204 on PUT, got %d", putResp.StatusCode)
			}

			// GET reads it back.
			resp2, err := http.Get(srv.URL + ep.path)
			if err != nil {
				t.Fatalf("GET failed: %v", err)
			}
			defer resp2.Body.Close()
			var got2 aiModelResponse
			json.NewDecoder(resp2.Body).Decode(&got2)
			if got2.Model != "qwen2.5vl:7b" {
				t.Errorf("expected the stored model to round-trip, got %q", got2.Model)
			}
		})
	}
}

// TestAIModel_EmptyModelRejected confirms a PUT with an empty model is a
// 400 — the endpoint won't store a blank value.
func TestAIModel_EmptyModelRejected(t *testing.T) {
	for _, ep := range aiModelEndpoints {
		t.Run(ep.name, func(t *testing.T) {
			connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
			srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, nil, nil, nil, nil, nil))
			defer srv.Close()

			body, _ := json.Marshal(aiModelRequest{Model: ""})
			req, _ := http.NewRequest(http.MethodPut, srv.URL+ep.path, bytes.NewReader(body))
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("PUT failed: %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("expected 400 for an empty model, got %d", resp.StatusCode)
			}
		})
	}
}

// TestAIModel_InvalidBody confirms a malformed JSON body is a 400.
func TestAIModel_InvalidBody(t *testing.T) {
	for _, ep := range aiModelEndpoints {
		t.Run(ep.name, func(t *testing.T) {
			connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
			srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, nil, nil, nil, nil, nil))
			defer srv.Close()

			req, _ := http.NewRequest(http.MethodPut, srv.URL+ep.path, bytes.NewReader([]byte("not json")))
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("PUT failed: %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("expected 400 for a malformed body, got %d", resp.StatusCode)
			}
		})
	}
}

// TestAIProvider_RoundTrip confirms the provider setting defaults to
// "ollama" (matching mode.buildAIClient's own default) and round-trips a
// valid choice.
func TestAIProvider_RoundTrip(t *testing.T) {
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, nil, nil, nil, nil, nil))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/settings/ai-provider")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	var got aiProviderResponse
	json.NewDecoder(resp.Body).Decode(&got)
	resp.Body.Close()
	if got.Provider != "ollama" {
		t.Errorf("expected the default provider to be ollama, got %q", got.Provider)
	}

	body, _ := json.Marshal(aiProviderRequest{Provider: "anthropic"})
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/settings/ai-provider", bytes.NewReader(body))
	putResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT failed: %v", err)
	}
	putResp.Body.Close()
	if putResp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204 on PUT, got %d", putResp.StatusCode)
	}

	resp2, err := http.Get(srv.URL + "/api/settings/ai-provider")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp2.Body.Close()
	var got2 aiProviderResponse
	json.NewDecoder(resp2.Body).Decode(&got2)
	if got2.Provider != "anthropic" {
		t.Errorf("expected the stored provider to round-trip, got %q", got2.Provider)
	}
}

// TestAIProvider_RejectsUnknownProvider confirms a typo'd provider name
// fails fast at save time rather than surfacing later as an opaque Scan
// error.
func TestAIProvider_RejectsUnknownProvider(t *testing.T) {
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, nil, nil, nil, nil, nil))
	defer srv.Close()

	body, _ := json.Marshal(aiProviderRequest{Provider: "chatgpt"})
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/settings/ai-provider", bytes.NewReader(body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for an unrecognized provider, got %d", resp.StatusCode)
	}
}

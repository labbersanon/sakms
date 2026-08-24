package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labbersanon/sakms/internal/rename"
)

func newMatchConfigTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	connStore, propStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, nil, propStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), rssFeedsStore, nil, nil, nil, nil, nil, nil, nil, nil, nil))
	t.Cleanup(srv.Close)
	return srv
}

func TestMatchConfig_Default(t *testing.T) {
	srv := newMatchConfigTestServer(t)
	resp, err := http.Get(srv.URL + "/api/modes/movies/rename-match-config")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	var got matchConfigResponse
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	def := rename.DefaultMatchConfig()
	if got.CandidateN != def.CandidateN || got.DurationTolerancePct != def.DurationTolerancePct {
		t.Fatalf("got %+v want %+v", got, def)
	}
}

func TestMatchConfig_PutAndGet(t *testing.T) {
	srv := newMatchConfigTestServer(t)
	body, _ := json.Marshal(matchConfigRequest{CandidateN: 7, DurationTolerancePct: 8})
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/modes/movies/rename-match-config", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("put status %d", resp.StatusCode)
	}
	getResp, err := http.Get(srv.URL + "/api/modes/movies/rename-match-config")
	if err != nil {
		t.Fatal(err)
	}
	defer getResp.Body.Close()
	var got matchConfigResponse
	if err := json.NewDecoder(getResp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.CandidateN != 7 || got.DurationTolerancePct != 8 {
		t.Fatalf("got %+v", got)
	}
}

func TestMatchConfig_RejectsOutOfRange(t *testing.T) {
	srv := newMatchConfigTestServer(t)
	body, _ := json.Marshal(matchConfigRequest{CandidateN: 99, DurationTolerancePct: 5})
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/modes/movies/rename-match-config", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

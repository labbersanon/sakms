package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/labbersanon/sakms/internal/usenet"
)

func TestEtaPriorsHandler_DefaultsWhenNZBNil(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/downloads/eta-priors", etaPriorsHandler(nil))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/api/downloads/eta-priors")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d, want 200", resp.StatusCode)
	}
	var out etaPriorsResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.RepairBps != usenet.DefaultHardwareRepairBps || out.UnpackBps != usenet.DefaultHardwareUnpackBps {
		t.Fatalf("priors = %+v, want defaults %d/%d", out, usenet.DefaultHardwareRepairBps, usenet.DefaultHardwareUnpackBps)
	}
	if out.RepairSamples != 0 || out.UnpackSamples != 0 {
		t.Fatalf("samples = %d/%d, want 0/0", out.RepairSamples, out.UnpackSamples)
	}
}

func TestEtaPriorsHandler_ReturnsUpdatedEMA(t *testing.T) {
	nzb := usenet.New(usenet.Config{StagingDir: t.TempDir()})
	nzb.Observe("repairing", 50<<20, time.Second)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/downloads/eta-priors", etaPriorsHandler(nzb))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/api/downloads/eta-priors")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	var out etaPriorsResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.RepairBps != 30<<20 {
		t.Fatalf("repairBps = %d, want %d", out.RepairBps, int64(30<<20))
	}
	if out.RepairSamples != 1 {
		t.Fatalf("repairSamples = %d, want 1", out.RepairSamples)
	}
	if out.UnpackBps != usenet.DefaultHardwareUnpackBps || out.UnpackSamples != 0 {
		t.Fatalf("unpack unchanged, got %d n=%d", out.UnpackBps, out.UnpackSamples)
	}
}

func TestEtaAccuracyHandler_AcceptsBodyReturns204(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/downloads/eta-accuracy", etaAccuracyHandler())
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	body, _ := json.Marshal(etaAccuracySample{
		GID:          "nzb-abc",
		Protocol:     "usenet",
		Phase:        "repairing",
		ProjectedSec: 12.5,
		ActualSec:    10,
		TotalLength:  1000,
		At:           "2026-09-21T20:00:00Z",
	})
	resp, err := http.Post(srv.URL+"/api/downloads/eta-accuracy", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status %d, want 204", resp.StatusCode)
	}
}

func TestEtaAccuracyHandler_InvalidJSON400(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/downloads/eta-accuracy", etaAccuracyHandler())
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	resp, err := http.Post(srv.URL+"/api/downloads/eta-accuracy", "application/json", bytes.NewReader([]byte("{")))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", resp.StatusCode)
	}
}

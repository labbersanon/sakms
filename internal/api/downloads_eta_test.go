package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/labbersanon/sakms/internal/dbtest"
	"github.com/labbersanon/sakms/internal/settings"
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
	if out.Calibrated || out.CalibratedAt != "" {
		t.Fatalf("nil manager calibrated = %t %q, want false empty", out.Calibrated, out.CalibratedAt)
	}
}

func TestEtaPriorsHandler_FreshManagerUncalibrated(t *testing.T) {
	nzb := usenet.New(usenet.Config{StagingDir: t.TempDir()})
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
	if out.Calibrated {
		t.Fatal("fresh Manager must report calibrated=false")
	}
	if out.RepairBps != usenet.DefaultHardwareRepairBps {
		t.Fatalf("repairBps = %d, want compiled default", out.RepairBps)
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
	if out.Calibrated {
		t.Fatal("Observe-only EMA must not set calibrated")
	}
}

func TestCalibrateHardwareHandler_PersistsReplace(t *testing.T) {
	store := settings.New(dbtest.New(t))
	nzb := usenet.New(usenet.Config{StagingDir: t.TempDir()})
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/downloads/eta-priors", etaPriorsHandler(nzb))
	mux.HandleFunc("POST /api/downloads/calibrate-hardware", calibrateHardwareHandler(store, nzb))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	resp, err := http.Post(srv.URL+"/api/downloads/calibrate-hardware", "application/json", http.NoBody)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status %d, want 200 (%s)", resp.StatusCode, body)
	}
	var out etaPriorsResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !out.Calibrated || out.CalibratedAt == "" {
		t.Fatalf("POST calibrated = %+v", out)
	}
	if out.RepairBps <= 0 {
		t.Fatalf("repairBps = %d, want REPLACE sample > 0", out.RepairBps)
	}
	if out.RepairSamples != 0 || out.UnpackSamples != 0 {
		t.Fatalf("samples after REPLACE = %d/%d, want 0/0", out.RepairSamples, out.UnpackSamples)
	}

	got, err := http.Get(srv.URL + "/api/downloads/eta-priors")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer got.Body.Close()
	var again etaPriorsResponse
	if err := json.NewDecoder(got.Body).Decode(&again); err != nil {
		t.Fatalf("decode GET: %v", err)
	}
	if again.RepairBps != out.RepairBps || !again.Calibrated {
		t.Fatalf("GET after POST = %+v, want same REPLACE as POST %+v", again, out)
	}

	ctx := context.Background()
	flag, err := store.GetBool(ctx, UsenetHWPriorsCalibratedKey, false)
	if err != nil || !flag {
		t.Fatalf("persisted calibrated = %t err=%v", flag, err)
	}
	storedRepair, err := store.Get(ctx, UsenetHWRepairBpsKey)
	if err != nil {
		t.Fatal(err)
	}
	n, err := strconv.ParseInt(storedRepair, 10, 64)
	if err != nil || n != out.RepairBps {
		t.Fatalf("persisted repair_bps = %q, want %d", storedRepair, out.RepairBps)
	}
}

func TestCalibrateHardwareHandler_Overlap409(t *testing.T) {
	store := settings.New(dbtest.New(t))
	nzb := usenet.New(usenet.Config{StagingDir: t.TempDir()})
	hold := make(chan struct{})
	nzb.SetCalibrationHold(hold)
	var once sync.Once
	release := func() { once.Do(func() { close(hold) }) }
	t.Cleanup(release)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/downloads/calibrate-hardware", calibrateHardwareHandler(store, nzb))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	firstDone := make(chan int, 1)
	go func() {
		resp, err := http.Post(srv.URL+"/api/downloads/calibrate-hardware", "application/json", http.NoBody)
		if err != nil {
			firstDone <- -1
			return
		}
		defer resp.Body.Close()
		io.Copy(io.Discard, resp.Body)
		firstDone <- resp.StatusCode
	}()

	deadline := time.Now().Add(2 * time.Second)
	for !nzb.CalibrationInProgress() {
		if time.Now().After(deadline) {
			t.Fatal("first POST did not take the in-flight lock")
		}
		time.Sleep(5 * time.Millisecond)
	}

	resp, err := http.Post(srv.URL+"/api/downloads/calibrate-hardware", "application/json", http.NoBody)
	if err != nil {
		t.Fatalf("second POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("overlapping POST status %d, want 409", resp.StatusCode)
	}
	release()
	code := <-firstDone
	if code != http.StatusOK {
		t.Fatalf("first POST status %d, want 200", code)
	}
}

func TestCalibrateHardwareHandler_NilManager503(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/downloads/calibrate-hardware", calibrateHardwareHandler(nil, nil))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	resp, err := http.Post(srv.URL+"/api/downloads/calibrate-hardware", "application/json", http.NoBody)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status %d, want 503", resp.StatusCode)
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

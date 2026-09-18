package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labbersanon/sakms/internal/xferlimit"
)

func TestDownloadRateLimit_GetPut(t *testing.T) {
	store := newSettingsStore(t)
	cap := xferlimit.New(0)
	SetGlobalRateCap(cap)
	t.Cleanup(func() { SetGlobalRateCap(nil) })

	get := getDownloadRateLimitHandler(store)
	req := httptest.NewRequest(http.MethodGet, "/api/settings/download-rate-limit-mbps", nil)
	rr := httptest.NewRecorder()
	get.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET status=%d", rr.Code)
	}

	put := putDownloadRateLimitHandler(store, nil)
	body, _ := json.Marshal(downloadRateLimitRequest{Mbps: 50})
	req = httptest.NewRequest(http.MethodPut, "/api/settings/download-rate-limit-mbps", bytes.NewReader(body))
	rr = httptest.NewRecorder()
	put.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("PUT status=%d body=%s", rr.Code, rr.Body.String())
	}
	if cap.Mbps() != 50 {
		t.Fatalf("cap mbps=%d want 50", cap.Mbps())
	}
	if cap.BytesPerSec() != 50*125000 {
		t.Fatalf("bps=%d", cap.BytesPerSec())
	}
}

func TestDownloadRateLimit_RejectsNegative(t *testing.T) {
	store := newSettingsStore(t)
	put := putDownloadRateLimitHandler(store, nil)
	body, _ := json.Marshal(downloadRateLimitRequest{Mbps: -1})
	req := httptest.NewRequest(http.MethodPut, "/api/settings/download-rate-limit-mbps", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	put.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status=%d", rr.Code)
	}
}

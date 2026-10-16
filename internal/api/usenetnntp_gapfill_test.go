package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labbersanon/sakms/internal/dbtest"
	"github.com/labbersanon/sakms/internal/secrets"
	"github.com/labbersanon/sakms/internal/settings"
	"github.com/labbersanon/sakms/internal/usenetsearch"
)

func TestNNTPGapFill_DefaultsAreOnExceptIRC(t *testing.T) {
	store := newSettingsStore(t)
	r, err := loadNNTPNativeSettings(context.Background(), store, nil)
	if err != nil {
		t.Fatal(err)
	}
	cfg := configFromNNTPResponse(r)
	if !cfg.RepairEnabled || !cfg.NameFixEnabled || !cfg.BackfillEnabled {
		t.Fatalf("repair/namefix/backfill must default on (repair's Go zero value is false): %+v", cfg)
	}
	if cfg.MinCompletenessPct != 95 || cfg.IncompleteTTLDays != 3 || cfg.UnnamedTTLDays != 7 ||
		cfg.UnnamedMaxPct != 25 || cfg.NameFixBodyMBPerHour != 512 ||
		cfg.BackfillBudgetPct != 90 || cfg.BackfillDays != 0 {
		t.Fatalf("defaults: %+v", cfg)
	}
	g := r.GapFill
	if !g.PreDBHTTPEnabled || g.PreDBIRCEnabled || g.IRCPasswordSet || g.PreDBHTTPURL != "https://api.predb.net/" {
		t.Fatalf("predb defaults: %+v", g)
	}
}

func TestNNTPGapFill_KillSwitchesReachConfig(t *testing.T) {
	store := newSettingsStore(t)
	ctx := context.Background()
	for _, k := range []string{
		usenetsearch.KeyNameFixEnabled, usenetsearch.KeyRepairEnabled,
		usenetsearch.KeyBackfillEnabled, usenetsearch.KeyPreDBHTTPEnabled,
	} {
		if err := store.Set(ctx, k, "false"); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Set(ctx, usenetsearch.KeyBackfillDays, "30"); err != nil {
		t.Fatal(err)
	}
	r, err := loadNNTPNativeSettings(ctx, store, nil)
	if err != nil {
		t.Fatal(err)
	}
	cfg := configFromNNTPResponse(r)
	if cfg.RepairEnabled || cfg.NameFixEnabled || cfg.BackfillEnabled || r.GapFill.PreDBHTTPEnabled {
		t.Fatalf("kill switches ignored: %+v / %+v", cfg, r.GapFill)
	}
	if cfg.BackfillDays != 30 {
		t.Fatalf("BackfillDays=%d", cfg.BackfillDays)
	}
}

func TestNNTPGapFill_PutPreservesKnobs(t *testing.T) {
	store := newSettingsStore(t)
	ctx := context.Background()
	if err := store.Set(ctx, usenetsearch.KeyNameFixEnabled, "false"); err != nil {
		t.Fatal(err)
	}
	h := putUsenetNNTPNativeHandler(store, nil)
	req := httptest.NewRequest(http.MethodPut, "/api/settings/usenet-nntp-native", bytes.NewReader([]byte(`{"crawlIntervalSeconds":30}`)))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	v, err := store.GetBool(ctx, usenetsearch.KeyNameFixEnabled, true)
	if err != nil || v {
		t.Fatalf("namefix knob clobbered by PUT: v=%v err=%v", v, err)
	}
	var resp usenetNNTPNativeResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil || resp.GapFill.NameFixEnabled {
		t.Fatalf("resp=%+v err=%v", resp.GapFill, err)
	}
}

func TestNNTPGapFill_StatusEmptyWithoutIndex(t *testing.T) {
	store := newSettingsStore(t)
	svc := usenetsearch.NewService(nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/settings/usenet-nntp-native", nil)
	rr := httptest.NewRecorder()
	getUsenetNNTPNativeHandler(store, svc).ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var resp struct {
		GapFillStatus map[string]any `json:"gapFillStatus"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil || resp.GapFillStatus == nil {
		t.Fatalf("status block missing: %s err=%v", rr.Body.String(), err)
	}
	for _, k := range []string{"releasesSearchable", "releasesBelowGate", "pendingName", "namedBySource", "missedRanges", "backfill"} {
		if _, ok := resp.GapFillStatus[k]; !ok {
			t.Fatalf("status missing %q: %v", k, resp.GapFillStatus)
		}
	}
}

func TestNNTPPreDB_IRCPasswordStoredEncryptedNeverReturned(t *testing.T) {
	sqlDB := dbtest.New(t)
	store := settings.New(sqlDB)
	sec, err := secrets.New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	rt := StartNNTPPreDB(context.Background(), sqlDB, store, sec, usenetsearch.NewService(nil, sqlDB))
	SetNNTPPreDB(rt)
	t.Cleanup(func() { SetNNTPPreDB(nil) })

	const secret = "hunter2-not-a-real-password"
	req := httptest.NewRequest(http.MethodPut, "/x", strings.NewReader(`{"password":"`+secret+`"}`))
	rr := httptest.NewRecorder()
	putUsenetNNTPPreDBIRCPasswordHandler(store).ServeHTTP(rr, req)
	if rr.Code != http.StatusOK || strings.Contains(rr.Body.String(), secret) {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	enc, err := store.Get(context.Background(), usenetsearch.KeyPreDBIRCPasswordEnc)
	if err != nil || enc == "" || strings.Contains(enc, secret) {
		t.Fatalf("stored value must be ciphertext: %q err=%v", enc, err)
	}
	if got, err := sec.Decrypt(enc); err != nil || got != secret {
		t.Fatalf("round trip: %q err=%v", got, err)
	}
	r, _ := loadNNTPNativeSettings(context.Background(), store, nil)
	if !r.GapFill.IRCPasswordSet {
		t.Fatal("passwordSet not reported")
	}
	if b, _ := json.Marshal(r); strings.Contains(string(b), secret) || strings.Contains(string(b), enc) {
		t.Fatal("settings response leaks the password or its ciphertext")
	}

	rr = httptest.NewRecorder()
	putUsenetNNTPPreDBIRCPasswordHandler(store).ServeHTTP(rr, httptest.NewRequest(http.MethodPut, "/x", strings.NewReader(`{"password":""}`)))
	if enc, _ := store.Get(context.Background(), usenetsearch.KeyPreDBIRCPasswordEnc); rr.Code != http.StatusOK || enc != "" {
		t.Fatalf("clear failed: status=%d enc=%q", rr.Code, enc)
	}
}

func TestNNTPPreDB_PasswordEndpointWithoutRuntime(t *testing.T) {
	SetNNTPPreDB(nil)
	rr := httptest.NewRecorder()
	putUsenetNNTPPreDBIRCPasswordHandler(newSettingsStore(t)).ServeHTTP(rr, httptest.NewRequest(http.MethodPut, "/x", strings.NewReader(`{}`)))
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d", rr.Code)
	}
}

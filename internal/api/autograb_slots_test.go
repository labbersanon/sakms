package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labbersanon/sakms/internal/dbtest"
	"github.com/labbersanon/sakms/internal/settings"
)

func TestLoadAutoGrabSlots_DefaultsWhenUnset(t *testing.T) {
	store := settings.New(dbtest.New(t))
	ctx := t.Context()

	u, err := loadAutoGrabSlots(ctx, store, autoGrabSlotsProtocolUsenet)
	if err != nil {
		t.Fatal(err)
	}
	if u.PerCycle != defaultAutoGrabSlotsPerCycle || u.PerSeries != defaultAutoGrabSlotsPerSeries {
		t.Fatalf("usenet defaults: %+v", u)
	}

	tr, err := loadAutoGrabSlots(ctx, store, autoGrabSlotsProtocolTorrent)
	if err != nil {
		t.Fatal(err)
	}
	if tr.PerCycle != 0 || tr.PerSeries != defaultAutoGrabSlotsPerSeries {
		t.Fatalf("torrent defaults: %+v", tr)
	}
}

func TestAirDateDispatchCaps_TorrentZeroSharesUsenetBudget(t *testing.T) {
	store := settings.New(dbtest.New(t))
	limit, perSeries := airDateDispatchCaps(t.Context(), store)
	if limit != defaultAutoGrabSlotsPerCycle {
		t.Errorf("limit = %d, want %d (torrent 0 must not add slots)", limit, defaultAutoGrabSlotsPerCycle)
	}
	if perSeries != defaultAutoGrabSlotsPerSeries {
		t.Errorf("perSeries = %d, want %d", perSeries, defaultAutoGrabSlotsPerSeries)
	}
}

func TestAirDateDispatchCaps_TorrentAddsOnTop(t *testing.T) {
	store := settings.New(dbtest.New(t))
	ctx := t.Context()
	if err := store.Set(ctx, usenetAutoGrabSlotsPerCycleKey, "40"); err != nil {
		t.Fatal(err)
	}
	if err := store.Set(ctx, usenetAutoGrabSlotsPerSeriesKey, "8"); err != nil {
		t.Fatal(err)
	}
	if err := store.Set(ctx, torrentAutoGrabSlotsPerCycleKey, "10"); err != nil {
		t.Fatal(err)
	}
	if err := store.Set(ctx, torrentAutoGrabSlotsPerSeriesKey, "3"); err != nil {
		t.Fatal(err)
	}
	limit, perSeries := airDateDispatchCaps(ctx, store)
	if limit != 50 {
		t.Errorf("limit = %d, want 50", limit)
	}
	if perSeries != 11 {
		t.Errorf("perSeries = %d, want 11", perSeries)
	}
}

func TestPutGetAutoGrabSlots_RoundTrip(t *testing.T) {
	store := settings.New(dbtest.New(t))
	put := putAutoGrabSlotsHandler(store, autoGrabSlotsProtocolUsenet)
	get := getAutoGrabSlotsHandler(store, autoGrabSlotsProtocolUsenet)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/settings/usenet-autograb-slots",
		strings.NewReader(`{"perCycle":40,"perSeries":8}`))
	put(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("PUT status %d body %s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	get(rec, httptest.NewRequest(http.MethodGet, "/api/settings/usenet-autograb-slots", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET status %d", rec.Code)
	}
	var got autoGrabSlots
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.PerCycle != 40 || got.PerSeries != 8 {
		t.Fatalf("got %+v", got)
	}
}

func TestPutAutoGrabSlots_RejectsUsenetZeroCycle(t *testing.T) {
	store := settings.New(dbtest.New(t))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/settings/usenet-autograb-slots",
		strings.NewReader(`{"perCycle":0,"perSeries":5}`))
	putAutoGrabSlotsHandler(store, autoGrabSlotsProtocolUsenet)(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", rec.Code)
	}
}

func TestPutAutoGrabSlots_AllowsTorrentZeroCycle(t *testing.T) {
	store := settings.New(dbtest.New(t))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/settings/torrent-autograb-slots",
		strings.NewReader(`{"perCycle":0,"perSeries":5}`))
	putAutoGrabSlotsHandler(store, autoGrabSlotsProtocolTorrent)(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
}

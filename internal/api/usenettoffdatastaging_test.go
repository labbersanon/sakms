package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/labbersanon/sakms/internal/dbtest"
	"github.com/labbersanon/sakms/internal/settings"
	"github.com/labbersanon/sakms/internal/usenet"
)

func TestUsenetOffDataStaging_DefaultOff(t *testing.T) {
	store := settings.New(dbtest.New(t))
	h := getUsenetOffDataStagingHandler(store)
	rr := httptest.NewRecorder()
	h(rr, httptest.NewRequest(http.MethodGet, "/", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("GET status %d", rr.Code)
	}
	var got usenetOffDataStagingResponse
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Enabled {
		t.Fatalf("expected enabled=false by default, got %+v", got)
	}
}

func TestUsenetOffDataStaging_EnableRequiresAbsPath(t *testing.T) {
	store := settings.New(dbtest.New(t))
	h := putUsenetOffDataStagingHandler(store, nil)
	body, _ := json.Marshal(usenetOffDataStagingRequest{Enabled: true, Dir: "relative/path"})
	rr := httptest.NewRecorder()
	h(rr, httptest.NewRequest(http.MethodPut, "/", bytes.NewReader(body)))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("want 400 for relative path, got %d %s", rr.Code, rr.Body.String())
	}
}

func TestUsenetOffDataStaging_EnableAppliesLive(t *testing.T) {
	store := settings.New(dbtest.New(t))
	dir := t.TempDir()
	staging := filepath.Join(dir, "usenet-staging")
	nzb := usenet.New(usenet.Config{StagingDir: filepath.Join(dir, "default-downloads")})
	h := putUsenetOffDataStagingHandler(store, nzb)
	body, _ := json.Marshal(usenetOffDataStagingRequest{Enabled: true, Dir: staging})
	rr := httptest.NewRecorder()
	h(rr, httptest.NewRequest(http.MethodPut, "/", bytes.NewReader(body)))
	if rr.Code != http.StatusNoContent {
		t.Fatalf("want 204, got %d %s", rr.Code, rr.Body.String())
	}
	if nzb.StagingDir() != staging {
		t.Fatalf("live staging=%q want %q", nzb.StagingDir(), staging)
	}
	en, err := store.GetBool(context.Background(), UsenetOffDataStagingEnabledKey, false)
	if err != nil || !en {
		t.Fatalf("stored enabled=%v err=%v", en, err)
	}
}

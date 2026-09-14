package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/labbersanon/sakms/internal/apidto"
	"github.com/labbersanon/sakms/internal/grabs"
	"github.com/labbersanon/sakms/internal/mode"
)

func TestPromoteRequestHandler_BumpsRetryAfterToNow(t *testing.T) {
	grabsStore, _, _ := requestsTestStores(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	g, err := grabsStore.Create(ctx, grabs.Grab{
		Mode: mode.Movies, Title: "Some Movie", TMDBID: 42, RootFolderPath: "/movies",
		Status: grabs.PendingRetry, RetryAfter: grabs.FormatTime(now.Add(30 * 24 * time.Hour)),
		RetryReason: "no candidate cleared the quality floor",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	body, _ := json.Marshal(apidto.PromoteRequestRequest{GrabID: g.ID})
	req := httptest.NewRequest(http.MethodPost, "/api/requests/promote", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	promoteRequestHandler(grabsStore)(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("status = %d body %q, want 204", rr.Code, rr.Body.String())
	}
	got, err := grabsStore.Get(ctx, g.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.RetryAfter != grabs.FormatTime(now) && got.RetryAfter > grabs.FormatTime(now.Add(time.Minute)) {
		t.Fatalf("retry_after = %q, want near now (%q)", got.RetryAfter, grabs.FormatTime(now))
	}
	due, err := grabsStore.DueForRetry(ctx, now.Add(time.Second))
	if err != nil {
		t.Fatalf("DueForRetry: %v", err)
	}
	if len(due) != 1 || due[0].ID != g.ID {
		t.Fatalf("promoted row should be due, got %+v", due)
	}
}

func TestPromoteRequestHandler_RequiresGrabID(t *testing.T) {
	grabsStore, _, _ := requestsTestStores(t)
	body, _ := json.Marshal(apidto.PromoteRequestRequest{})
	req := httptest.NewRequest(http.MethodPost, "/api/requests/promote", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	promoteRequestHandler(grabsStore)(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

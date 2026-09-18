package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labbersanon/sakms/internal/usenetsearch"
)

func TestValidatePerModeGroups(t *testing.T) {
	cur := usenetNNTPNativeResponse{
		Enabled: true,
		Movies:  true,
	}
	if msg := validatePerModeGroups(cur); msg == "" {
		t.Fatal("expected error when movies enabled without groups")
	}
	cur.MoviesGroups = "alt.binaries.movies"
	if msg := validatePerModeGroups(cur); msg != "" {
		t.Fatalf("unexpected: %s", msg)
	}
}

func TestLoadNNTPNativeSettings_SoftMigratesLegacyGroups(t *testing.T) {
	store := newSettingsStore(t)
	ctx := context.Background()
	if err := store.Set(ctx, usenetsearch.KeyGroups, "alt.binaries.movies\nalt.binaries.tv"); err != nil {
		t.Fatal(err)
	}
	r, err := loadNNTPNativeSettings(ctx, store, nil)
	if err != nil {
		t.Fatal(err)
	}
	if r.MoviesGroups == "" || r.SeriesGroups == "" || r.AdultGroups == "" {
		t.Fatalf("soft-migrate failed: %+v", r)
	}
}

func TestGetUsenetNNTPGroups_BadMode(t *testing.T) {
	h := getUsenetNNTPGroupsHandler(nil)
	req := httptest.NewRequest(http.MethodGet, "/api/settings/usenet-nntp-native/groups?mode=bogus", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
}

func TestGetUsenetNNTPGroups_NoService(t *testing.T) {
	h := getUsenetNNTPGroupsHandler(nil)
	req := httptest.NewRequest(http.MethodGet, "/api/settings/usenet-nntp-native/groups?mode=movies", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d", rr.Code)
	}
	var got usenetNNTPGroupsResponse
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Mode != "movies" || got.Groups == nil {
		t.Fatalf("got %+v", got)
	}
}

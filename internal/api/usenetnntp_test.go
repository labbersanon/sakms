package api

import (
	"bytes"
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
	if got.Recommended == nil || got.Items == nil {
		t.Fatalf("recommended/items should be non-nil slices: %+v", got)
	}
	if got.Steps == nil || got.Packages == nil {
		t.Fatalf("steps/packages should be present: %+v", got)
	}
	if len(got.Steps) != usenetsearch.PackageStepMax {
		t.Fatalf("steps=%d want %d", len(got.Steps), usenetsearch.PackageStepMax)
	}
}

func TestPostUsenetNNTPResetGrade_Validation(t *testing.T) {
	h := postUsenetNNTPResetGradeHandler(nil)
	req := httptest.NewRequest(http.MethodPost, "/api/settings/usenet-nntp-native/grades/reset",
		bytes.NewBufferString(`{"mode":"bogus","group":"alt.binaries.moovee"}`))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("bad mode status=%d", rr.Code)
	}
	req = httptest.NewRequest(http.MethodPost, "/api/settings/usenet-nntp-native/grades/reset",
		bytes.NewBufferString(`{"mode":"movies","group":""}`))
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("empty group status=%d", rr.Code)
	}
	req = httptest.NewRequest(http.MethodPost, "/api/settings/usenet-nntp-native/grades/reset",
		bytes.NewBufferString(`{"mode":"movies","group":"alt.binaries.moovee"}`))
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("no service status=%d want 503", rr.Code)
	}
}

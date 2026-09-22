package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labbersanon/sakms/internal/dbtest"
	"github.com/labbersanon/sakms/internal/prowlarr"
	"github.com/labbersanon/sakms/internal/settings"
)

func TestFilterPreferredLanguages(t *testing.T) {
	in := []prowlarr.Release{
		{GUID: "1", Title: "Leverage.S04E16.720p.WEB-DL.x264-POD"},
		{GUID: "2", Title: "Leverage.S04E16.GERMAN.DUBBED.720p.WebHD.x264-TVP"},
		{GUID: "3", Title: "Leverage.S04E16.FRENCH.720p-GROUP"},
	}
	got := filterPreferredLanguages(in, nil)
	if len(got) != 1 || got[0].GUID != "1" {
		t.Fatalf("empty preferred kept %v", got)
	}
	got = filterPreferredLanguages(in, []string{"german"})
	if len(got) != 2 {
		t.Fatalf("preferred german kept %d want 2", len(got))
	}
	ids := map[string]bool{}
	for _, r := range got {
		ids[r.GUID] = true
	}
	if !ids["1"] || !ids["2"] || ids["3"] {
		t.Fatalf("preferred german unexpected set %v", ids)
	}
}

func TestGrabPreferredLanguagesHandlers(t *testing.T) {
	store := settings.New(dbtest.New(t))

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/settings/grab-preferred-languages", getGrabPreferredLanguagesHandler(store))
	mux.HandleFunc("PUT /api/settings/grab-preferred-languages", putGrabPreferredLanguagesHandler(store))

	putReq := httptest.NewRequest(http.MethodPut, "/api/settings/grab-preferred-languages", bytes.NewBufferString(`{"languages":["German"," german ","FRENCH",""]}`))
	putRec := httptest.NewRecorder()
	mux.ServeHTTP(putRec, putReq)
	if putRec.Code != http.StatusNoContent {
		t.Fatalf("PUT status=%d body=%s", putRec.Code, putRec.Body.String())
	}

	getReq := httptest.NewRequest(http.MethodGet, "/api/settings/grab-preferred-languages", nil)
	getRec := httptest.NewRecorder()
	mux.ServeHTTP(getRec, getReq)
	if getRec.Code != http.StatusOK {
		t.Fatalf("GET status=%d", getRec.Code)
	}
	var body grabPreferredLanguagesBody
	if err := json.NewDecoder(getRec.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Languages) != 2 || body.Languages[0] != "german" || body.Languages[1] != "french" {
		t.Fatalf("languages=%v", body.Languages)
	}

	loaded, err := loadPreferredLanguages(context.Background(), store)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 2 {
		t.Fatalf("loaded=%v", loaded)
	}
}

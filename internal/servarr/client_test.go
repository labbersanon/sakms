package servarr

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func newTestClient(t *testing.T, app App, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return New(Config{BaseURL: srv.URL, APIKey: "test-key", App: app}, srv.Client())
}

// Fixture below is trimmed from a real /api/v3/rootfolder response.
const rootFolderFixture = `[
  {"path":"/media/Media Library/Movies","accessible":true,"freeSpace":4382160977920,"unmappedFolders":[
    {"name":"9.11.Truth.Lies.and.Conspiracies.WEB.x264-spamTV","path":"/media/Media Library/Movies/9.11.Truth.Lies.and.Conspiracies.WEB.x264-spamTV","relativePath":"9.11.Truth.Lies.and.Conspiracies.WEB.x264-spamTV"},
    {"name":"Barbie (2023) 1080p.trickplay","path":"/media/Media Library/Movies/Barbie (2023) 1080p.trickplay","relativePath":"Barbie (2023) 1080p.trickplay"},
    {"name":"Lego Movies","path":"/media/Media Library/Movies/Lego Movies","relativePath":"Lego Movies"}
  ],"id":4},
  {"path":"/media/Media Library/Movies (Kids)","accessible":true,"freeSpace":4382160977920,"unmappedFolders":[],"id":5}
]`

func TestRootFolders_ParsesRealFixture(t *testing.T) {
	c := newTestClient(t, Radarr, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v3/rootfolder" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Header.Get("X-Api-Key") != "test-key" {
			t.Error("missing X-Api-Key header")
		}
		w.Write([]byte(rootFolderFixture))
	})

	folders, err := c.RootFolders(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(folders) != 2 {
		t.Fatalf("expected 2 root folders, got %d", len(folders))
	}
	if folders[0].ID != 4 || len(folders[0].UnmappedFolders) != 3 {
		t.Errorf("unexpected first folder: %+v", folders[0])
	}
	if folders[1].ID != 5 || len(folders[1].UnmappedFolders) != 0 {
		t.Errorf("expected Movies (Kids) empty of unmapped items, got %+v", folders[1])
	}
	found := false
	for _, uf := range folders[0].UnmappedFolders {
		if uf.Name == "Lego Movies" {
			found = true
		}
	}
	if !found {
		t.Error("expected Lego Movies in the unmapped list")
	}
}

func TestLookup_Radarr_IncludesCertification(t *testing.T) {
	c := newTestClient(t, Radarr, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("term") != "Shrek" {
			t.Errorf("unexpected term: %s", r.URL.Query().Get("term"))
		}
		w.Write([]byte(`[{"title":"Shrek","year":2001,"tmdbId":808,"certification":"PG","genres":["Animation","Comedy"]}]`))
	})

	results, err := c.Lookup(context.Background(), "Shrek")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 || results[0].Certification != "PG" || results[0].TMDBID != 808 {
		t.Errorf("unexpected result: %+v", results)
	}
}

func TestLookup_Sonarr_NoCertificationField(t *testing.T) {
	// Sonarr's series/lookup response has no certification field at all —
	// this fixture matches that real shape exactly.
	c := newTestClient(t, Sonarr, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[{"title":"Bluey","year":2018,"tvdbId":359710,"genres":["Comedy"]}]`))
	})

	results, err := c.Lookup(context.Background(), "Bluey")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if results[0].Certification != "" {
		t.Errorf("expected empty certification for Sonarr, got %q", results[0].Certification)
	}
	if results[0].TVDBID != 359710 {
		t.Errorf("unexpected tvdbId: %d", results[0].TVDBID)
	}
}

func TestTriggerCommand_SendsNameAndExtraFields(t *testing.T) {
	var gotBody map[string]any
	c := newTestClient(t, Radarr, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v3/command" || r.Method != http.MethodPost {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.Write([]byte(`{}`))
	})

	if err := c.TriggerCommand(context.Background(), "RescanMovie", map[string]any{"movieId": float64(5)}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotBody["name"] != "RescanMovie" || gotBody["movieId"] != float64(5) {
		t.Errorf("unexpected body: %+v", gotBody)
	}
}

func TestRescanTracked_UsesCorrectKeyPerApp(t *testing.T) {
	var gotBody map[string]any
	sonarrClient := newTestClient(t, Sonarr, func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.Write([]byte(`{}`))
	})
	if err := sonarrClient.RescanTracked(context.Background(), 7); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotBody["name"] != "RescanSeries" || gotBody["seriesId"] != float64(7) {
		t.Errorf("unexpected Sonarr rescan body: %+v", gotBody)
	}

	radarrClient := newTestClient(t, Radarr, func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.Write([]byte(`{}`))
	})
	if err := radarrClient.RescanTracked(context.Background(), 9); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotBody["name"] != "RescanMovie" || gotBody["movieId"] != float64(9) {
		t.Errorf("unexpected Radarr rescan body: %+v", gotBody)
	}
}

func TestDeleteTrackedFile_UsesCorrectResourcePerApp(t *testing.T) {
	var gotPath, gotMethod string
	sonarrClient := newTestClient(t, Sonarr, func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMethod = r.URL.Path, r.Method
		w.WriteHeader(http.StatusOK)
	})
	if err := sonarrClient.DeleteTrackedFile(context.Background(), 42); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotPath != "/api/v3/episodefile/42" || gotMethod != http.MethodDelete {
		t.Errorf("unexpected request: %s %s", gotMethod, gotPath)
	}

	radarrClient := newTestClient(t, Radarr, func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMethod = r.URL.Path, r.Method
		w.WriteHeader(http.StatusOK)
	})
	if err := radarrClient.DeleteTrackedFile(context.Background(), 42); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotPath != "/api/v3/moviefile/42" {
		t.Errorf("unexpected Radarr file path: %s", gotPath)
	}
}

func TestDeleteTrackedFile_EmptyBodyIsNotAnError(t *testing.T) {
	// Radarr/Sonarr's DELETE endpoints return no body on success — this
	// must not be mistaken for a decode failure.
	c := newTestClient(t, Radarr, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	if err := c.DeleteTrackedFile(context.Background(), 1); err != nil {
		t.Fatalf("empty response body should not error, got: %v", err)
	}
}

func TestDeleteTrackedFile_204NoContentIsNotAnError(t *testing.T) {
	// A 204 response must be treated as success, not just exactly 200 —
	// Sonarr/Radarr's own DELETE endpoints commonly return 204.
	c := newTestClient(t, Radarr, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	if err := c.DeleteTrackedFile(context.Background(), 1); err != nil {
		t.Fatalf("204 No Content should not error, got: %v", err)
	}
}

func TestQualityProfiles_ParsesRealFixture(t *testing.T) {
	// Fixture matches the actual live /api/v3/qualityprofile response.
	c := newTestClient(t, Radarr, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[{"id":1,"name":"Any"},{"id":4,"name":"HD-1080p"},{"id":5,"name":"Ultra-HD"}]`))
	})

	profiles, err := c.QualityProfiles(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(profiles) != 3 || profiles[1].Name != "HD-1080p" {
		t.Errorf("unexpected profiles: %+v", profiles)
	}
}

func TestAdd_Radarr_SendsTMDBIDAndSearchFlagFalse(t *testing.T) {
	var gotBody map[string]any
	c := newTestClient(t, Radarr, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v3/movie" || r.Method != http.MethodPost {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.Write([]byte(`{"id":123}`))
	})

	id, err := c.Add(context.Background(), AddRequest{
		Title: "Lego Movies", TMDBID: 808, QualityProfileID: 4,
		RootFolderPath: "/media/Media Library/Movies (Kids)", Monitored: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != 123 {
		t.Errorf("expected id 123, got %d", id)
	}
	if gotBody["tmdbId"] != float64(808) {
		t.Errorf("expected tmdbId in body, got %+v", gotBody)
	}
	addOptions, _ := gotBody["addOptions"].(map[string]any)
	if addOptions["searchForMovie"] != false {
		t.Errorf("expected searchForMovie=false, got %+v", gotBody["addOptions"])
	}
	if _, hasTVDB := gotBody["tvdbId"]; hasTVDB {
		t.Error("Radarr Add body should not include tvdbId")
	}
}

func TestAdd_Sonarr_SendsTVDBIDAndSeasonFolder(t *testing.T) {
	var gotBody map[string]any
	c := newTestClient(t, Sonarr, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v3/series" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.Write([]byte(`{"id":9}`))
	})

	id, err := c.Add(context.Background(), AddRequest{
		Title: "FathersLLDVD", TVDBID: 359710, QualityProfileID: 4,
		RootFolderPath: "/media/Media Library/Series (Kids)", Monitored: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != 9 {
		t.Errorf("expected id 9, got %d", id)
	}
	if gotBody["tvdbId"] != float64(359710) {
		t.Errorf("expected tvdbId in body, got %+v", gotBody)
	}
	if gotBody["seasonFolder"] != true {
		t.Error("expected seasonFolder=true for Sonarr")
	}
	if _, hasTMDB := gotBody["tmdbId"]; hasTMDB {
		t.Error("Sonarr Add body should not include tmdbId")
	}
}

func TestAllTracked_ParsesList(t *testing.T) {
	// Fixture matches the actual live /api/v3/movie response shape,
	// including certification/genres which ARE populated on tracked items
	// (unlike the pre-add lookup response).
	c := newTestClient(t, Radarr, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v3/movie" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.Write([]byte(`[{"id":1,"title":"Barnyard","path":"/media/Media Library/Movies/Barnyard (2006)","rootFolderPath":"/media/Media Library/Movies","monitored":true,"tmdbId":9907,"certification":"PG","genres":["Animation","Comedy","Family"]}]`))
	})

	items, err := c.AllTracked(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(items) != 1 || items[0].Title != "Barnyard" {
		t.Errorf("unexpected items: %+v", items)
	}
	if items[0].TMDBID != 9907 || items[0].Certification != "PG" || len(items[0].Genres) != 3 {
		t.Errorf("unexpected classification-relevant fields: %+v", items[0])
	}
}

func TestUpdateRootFolder_FetchesThenPutsWithMoveFilesTrue(t *testing.T) {
	var putPath string
	var putBody map[string]any
	c := newTestClient(t, Radarr, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			if r.URL.Path != "/api/v3/movie/1" {
				t.Errorf("unexpected GET path: %s", r.URL.Path)
			}
			w.Write([]byte(`{"id":1,"title":"Lego Movies","rootFolderPath":"/media/Media Library/Movies","monitored":true,"someOtherField":"must-survive"}`))
		case http.MethodPut:
			putPath = r.URL.RequestURI()
			json.NewDecoder(r.Body).Decode(&putBody)
			w.Write([]byte(`{}`))
		default:
			t.Errorf("unexpected method: %s", r.Method)
		}
	})

	if err := c.UpdateRootFolder(context.Background(), 1, "/media/Media Library/Movies (Kids)"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if putPath != "/api/v3/movie/1?moveFiles=true" {
		t.Errorf("expected moveFiles=true query param, got %s", putPath)
	}
	if putBody["rootFolderPath"] != "/media/Media Library/Movies (Kids)" {
		t.Errorf("expected rootFolderPath updated, got %+v", putBody)
	}
	if putBody["someOtherField"] != "must-survive" {
		t.Error("expected unrelated fields to round-trip unchanged, not be dropped")
	}
}

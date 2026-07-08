package rename

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/curtiswtaylorjr/tidyarr/internal/mode"
	"github.com/curtiswtaylorjr/tidyarr/internal/proposals"
	"github.com/curtiswtaylorjr/tidyarr/internal/servarr"
)

func newTestSession(t *testing.T, app servarr.App, handler http.HandlerFunc) *mode.Session {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	m := mode.Movies
	if app == servarr.Sonarr {
		m = mode.Series
	}
	return &mode.Session{
		Mode:    m,
		Servarr: servarr.New(servarr.Config{BaseURL: srv.URL, APIKey: "test-key", App: app}, srv.Client()),
	}
}

// fakeRadarr wires up the three read-only endpoints Scan needs, keyed by
// unmapped-folder name so each test case can control what Lookup returns.
type fakeRadarr struct {
	rootFolders string
	tracked     string
	profiles    string
	lookups     map[string]string // search term -> raw JSON response
}

func (f *fakeRadarr) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v3/rootfolder":
			w.Write([]byte(f.rootFolders))
		case r.URL.Path == "/api/v3/movie":
			w.Write([]byte(f.tracked))
		case r.URL.Path == "/api/v3/qualityprofile":
			w.Write([]byte(f.profiles))
		case r.URL.Path == "/api/v3/movie/lookup":
			term := r.URL.Query().Get("term")
			resp, ok := f.lookups[term]
			if !ok {
				t.Fatalf("unexpected lookup term %q", term)
			}
			w.Write([]byte(resp))
		case r.URL.Path == "/api/v3/command":
			w.Write([]byte(`{}`))
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}
}

func TestScan_ProducesPendingProposalForNewItem(t *testing.T) {
	f := &fakeRadarr{
		rootFolders: `[{"id":1,"path":"/media/Movies","accessible":true,"freeSpace":1,"unmappedFolders":[
			{"name":"A.Beautiful.Mind.2001.1080p.BluRay.x264-GROUP","path":"/media/Movies/A.Beautiful.Mind.2001.1080p.BluRay.x264-GROUP","relativePath":"A.Beautiful.Mind.2001.1080p.BluRay.x264-GROUP"}
		]}]`,
		tracked:  `[]`,
		profiles: `[{"id":4,"name":"HD-1080p"}]`,
		lookups: map[string]string{
			"A Beautiful Mind 2001": `[{"title":"A Beautiful Mind","year":2001,"tmdbId":453,"genres":["Drama"],"overview":"...","certification":"PG-13"}]`,
		},
	}
	sess := newTestSession(t, servarr.Radarr, f.handler(t))

	got, err := Scan(context.Background(), sess)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 proposal, got %d: %+v", len(got), got)
	}
	p := got[0]
	if p.Status != proposals.Pending {
		t.Fatalf("expected Pending, got %+v", p)
	}
	if p.Title != "A Beautiful Mind" || p.TMDBID != 453 {
		t.Errorf("unexpected identification: %+v", p)
	}
	if p.QualityProfileID != 4 {
		t.Errorf("expected fallback to the only available profile (4), got %d", p.QualityProfileID)
	}
	if p.RootFolderPath != "/media/Movies" || p.SourcePath != "/media/Movies/A.Beautiful.Mind.2001.1080p.BluRay.x264-GROUP" {
		t.Errorf("unexpected paths: %+v", p)
	}
}

func TestScan_SkipsSidecarFiles(t *testing.T) {
	f := &fakeRadarr{
		rootFolders: `[{"id":1,"path":"/media/Movies","accessible":true,"freeSpace":1,"unmappedFolders":[
			{"name":"Barbie (2023) 1080p.trickplay","path":"/media/Movies/Barbie (2023) 1080p.trickplay","relativePath":"Barbie (2023) 1080p.trickplay"}
		]}]`,
		tracked:  `[]`,
		profiles: `[]`,
		lookups:  map[string]string{},
	}
	sess := newTestSession(t, servarr.Radarr, f.handler(t))

	got, err := Scan(context.Background(), sess)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected sidecar file to be skipped entirely, got %+v", got)
	}
}

func TestScan_MarksUnmatchedWhenNoLookupResult(t *testing.T) {
	f := &fakeRadarr{
		rootFolders: `[{"id":1,"path":"/media/Movies","accessible":true,"freeSpace":1,"unmappedFolders":[
			{"name":"FathersLLDVD","path":"/media/Movies/FathersLLDVD","relativePath":"FathersLLDVD"}
		]}]`,
		tracked:  `[]`,
		profiles: `[]`,
		lookups:  map[string]string{"FathersLLDVD": `[]`},
	}
	sess := newTestSession(t, servarr.Radarr, f.handler(t))

	got, err := Scan(context.Background(), sess)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0].Status != proposals.Unmatched {
		t.Fatalf("expected 1 unmatched proposal, got %+v", got)
	}
	if got[0].Reason == "" {
		t.Error("expected a populated reason explaining why it's unmatched")
	}
}

func TestScan_MarksUnmatchedForAlreadyTrackedDuplicate(t *testing.T) {
	f := &fakeRadarr{
		rootFolders: `[{"id":1,"path":"/media/Movies","accessible":true,"freeSpace":1,"unmappedFolders":[
			{"name":"A.Beautiful.Mind.2001.1080p.BluRay.x264-GROUP","path":"/media/Movies/A.Beautiful.Mind.2001.1080p.BluRay.x264-GROUP","relativePath":"A.Beautiful.Mind.2001.1080p.BluRay.x264-GROUP"}
		]}]`,
		tracked:  `[{"id":9,"title":"A Beautiful Mind","path":"/media/Movies/A Beautiful Mind (2001)","rootFolderPath":"/media/Movies","tmdbId":453,"qualityProfileId":4}]`,
		profiles: `[{"id":4,"name":"HD-1080p"}]`,
		lookups: map[string]string{
			"A Beautiful Mind 2001": `[{"title":"A Beautiful Mind","year":2001,"tmdbId":453}]`,
		},
	}
	sess := newTestSession(t, servarr.Radarr, f.handler(t))

	got, err := Scan(context.Background(), sess)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0].Status != proposals.Unmatched {
		t.Fatalf("expected the duplicate to surface as unmatched rather than re-registering it, got %+v", got)
	}
}

func TestScan_QualityProfileLearnsFromExistingRootFolderConvention(t *testing.T) {
	f := &fakeRadarr{
		rootFolders: `[{"id":1,"path":"/media/Movies","accessible":true,"freeSpace":1,"unmappedFolders":[
			{"name":"New Movie (2020)","path":"/media/Movies/New Movie (2020)","relativePath":"New Movie (2020)"}
		]}]`,
		tracked: `[
			{"id":1,"title":"X","rootFolderPath":"/media/Movies","tmdbId":1,"qualityProfileId":7},
			{"id":2,"title":"Y","rootFolderPath":"/media/Movies","tmdbId":2,"qualityProfileId":7},
			{"id":3,"title":"Z","rootFolderPath":"/media/Movies","tmdbId":3,"qualityProfileId":9}
		]`,
		profiles: `[{"id":7,"name":"HD"},{"id":9,"name":"4K"}]`,
		lookups: map[string]string{
			"New Movie (2020)": `[{"title":"New Movie","year":2020,"tmdbId":999}]`,
		},
	}
	sess := newTestSession(t, servarr.Radarr, f.handler(t))

	got, err := Scan(context.Background(), sess)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0].QualityProfileID != 7 {
		t.Fatalf("expected the majority profile (7) already used in this root folder, got %+v", got)
	}
}

func TestApply_RegistersAndTriggersDownloadedScan(t *testing.T) {
	var addBody map[string]any
	var scanTriggered bool
	sess := newTestSession(t, servarr.Radarr, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v3/movie":
			json.NewDecoder(r.Body).Decode(&addBody)
			w.Write([]byte(`{"id":55}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v3/command":
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			if body["name"] != "DownloadedMoviesScan" {
				t.Errorf("expected a DownloadedMoviesScan trigger, got %+v", body)
			}
			scanTriggered = true
			w.Write([]byte(`{}`))
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	})

	p := proposals.Proposal{
		ID: 1, Status: proposals.Pending, Title: "A Beautiful Mind", TMDBID: 453,
		QualityProfileID: 4, RootFolderPath: "/media/Movies",
	}
	id, err := Apply(context.Background(), sess, p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != 55 {
		t.Errorf("expected the tracked id from Add's response (55), got %d", id)
	}
	if !scanTriggered {
		t.Error("expected Apply to trigger a downloaded-files scan after registering")
	}
	if addBody["tmdbId"] != float64(453) || addBody["title"] != "A Beautiful Mind" {
		t.Errorf("unexpected Add request body: %+v", addBody)
	}
}

func TestApply_RejectsNonPendingProposal(t *testing.T) {
	sess := newTestSession(t, servarr.Radarr, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("Apply must not make any HTTP call for a non-pending proposal")
	})

	for _, status := range []proposals.Status{proposals.Applied, proposals.Dismissed, proposals.Unmatched} {
		if _, err := Apply(context.Background(), sess, proposals.Proposal{Status: status}); err == nil {
			t.Errorf("expected Apply to refuse a %q proposal", status)
		}
	}
}

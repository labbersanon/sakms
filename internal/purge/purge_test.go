package purge

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/curtiswtaylorjr/sak/internal/mode"
	"github.com/curtiswtaylorjr/sak/internal/proposals"
	"github.com/curtiswtaylorjr/sak/internal/servarr"
)

// The full curated allowlist, mirroring stash-whisparr-sort's
// internal/config.PurgeTagAllowlist — kept as a local literal here so this
// test doesn't depend on any config package, matching purge's original
// leaf-level design.
var allowlist = []string{
	"BDSM", "Bondage", "Bondage Blowjob", "Bondage Collar", "Bondage Sex",
	"Dungeon", "Latina Trans", "Trans Fucked by Female", "Trans Fucked by Male",
	"Trans Fucks Female", "Trans Fucks Male", "Trans Fucks Trans", "Transgender",
	"Transgender (Female)", "Twosome (Trans)",
	"Bound", "Bound Wrists", "Bound Arms", "Bound Legs", "Chained", "Rope",
	"Crotch Rope", "Shibari", "Ribbon Bondage", "Breast Bondage", "Ball Gag",
	"Bit Gag", "Tape Gag", "Improvised Gag", "Whip", "Slave", "Dominatrix",
	"Spiked Collar", "Metal Collar", "Animal Collar",
	"Shemale", "She-male", "Chicks with Dicks", "Trannies", "Tgirls", "T-Girl",
	"Transmasculine", "Trans Women", "Trans Men", "Transgender Erotica",
	"FTM Gay Porn", "Queer Porn", "Feminist Porn", "Nonbinary", "Genderqueer",
	"Gender Variant Media",
	"Futanari", "Futa with Female", "Futa with Male", "Implied Futanari",
	"Crossdressing",
}

func TestMatchesAny_AllKnownLiveTagsStillMatch(t *testing.T) {
	known := []string{
		"BDSM", "Bondage", "Bondage Blowjob", "Bondage Collar", "Bondage Sex",
		"Dungeon", "Latina Trans", "Trans Fucked by Female", "Trans Fucked by Male",
		"Trans Fucks Female", "Trans Fucks Male", "Trans Fucks Trans", "Transgender",
		"Transgender (Female)", "Twosome (Trans)",
	}
	for _, tag := range known {
		t.Run(tag, func(t *testing.T) {
			if !MatchesAny(tag, allowlist) {
				t.Errorf("expected %q to match (regression against live data)", tag)
			}
		})
	}
}

// Transgender and Transformation are the case that breaks word-boundary
// regex matching (see the package doc comment) — exact matching has no such
// ambiguity.
func TestMatchesAny_TransgenderVsTransformation(t *testing.T) {
	if !MatchesAny("Transgender", allowlist) {
		t.Fatal("Transgender must match — it's an explicit allowlist entry")
	}
	if MatchesAny("Transformation", allowlist) {
		t.Fatal("Transformation must NOT match — not in the allowlist, and exact matching has no substring ambiguity")
	}
}

func TestMatchesAny_UnrelatedTagsNeverMatch(t *testing.T) {
	cases := []string{
		"Transformation", "Transatlantic", "Translator", "Transcript",
		"Bondage-Free", "Vanilla Romance", "Blonde", "Anal", "Threesome",
		"Chainsaw", "Collarbone", "Sailor Collar",
	}
	for _, tag := range cases {
		t.Run(tag, func(t *testing.T) {
			if MatchesAny(tag, allowlist) {
				t.Errorf("expected %q NOT to match — not an allowlist entry", tag)
			}
		})
	}
}

func TestMatchesAny_CaseInsensitive(t *testing.T) {
	if !MatchesAny("bdsm", allowlist) {
		t.Fatal("expected case-insensitive match for lowercase 'bdsm'")
	}
	if !MatchesAny("SHEMALE", allowlist) {
		t.Fatal("expected case-insensitive match for uppercase 'SHEMALE'")
	}
}

func TestMatchedEntries_ReportsWhichRuleFired(t *testing.T) {
	got := MatchedEntries("latina trans", allowlist) // case-insensitive input
	if len(got) != 1 || got[0] != "Latina Trans" {
		t.Fatalf("expected [\"Latina Trans\"], got %v", got)
	}
}

func TestMatchedEntries_NoMatch(t *testing.T) {
	got := MatchedEntries("Vanilla", allowlist)
	if len(got) != 0 {
		t.Fatalf("expected no matches, got %v", got)
	}
}

func newTestSession(t *testing.T, handler http.HandlerFunc) *mode.Session {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return &mode.Session{
		Mode:    mode.Movies,
		Servarr: servarr.New(servarr.Config{BaseURL: srv.URL, APIKey: "test-key", App: servarr.Radarr}, srv.Client()),
	}
}

func TestScan_ProposesOnlyItemsMatchingAllowlist(t *testing.T) {
	sess := newTestSession(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/movie":
			w.Write([]byte(`[
				{"id":1,"title":"Vanilla Movie","path":"/media/Movies/Vanilla Movie","rootFolderPath":"/media/Movies","tags":[9]},
				{"id":2,"title":"Flagged Movie","path":"/media/Movies/Flagged Movie","rootFolderPath":"/media/Movies","tags":[1,2]}
			]`))
		case "/api/v3/tag":
			w.Write([]byte(`[{"id":1,"label":"BDSM"},{"id":2,"label":"unrelated"},{"id":9,"label":"family-friendly"}]`))
		default:
			t.Fatalf("unexpected request: %s", r.URL.Path)
		}
	})

	got, err := Scan(context.Background(), sess, []string{"BDSM"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected exactly 1 matched proposal, got %d: %+v", len(got), got)
	}
	p := got[0]
	if p.TrackedID != 2 || p.Title != "Flagged Movie" || p.Status != proposals.Pending {
		t.Errorf("unexpected proposal: %+v", p)
	}
	if p.Reason == "" {
		t.Error("expected a populated reason naming the matched tag")
	}
}

func TestScan_EmptyAllowlistMatchesNothing(t *testing.T) {
	sess := newTestSession(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/movie":
			w.Write([]byte(`[{"id":1,"title":"X","path":"/x","rootFolderPath":"/media/Movies","tags":[1]}]`))
		case "/api/v3/tag":
			w.Write([]byte(`[{"id":1,"label":"BDSM"}]`))
		}
	})

	got, err := Scan(context.Background(), sess, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected no proposals against an empty allowlist, got %+v", got)
	}
}

func TestApply_DeletesTrackedItem(t *testing.T) {
	var gotPath string
	sess := newTestSession(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.String()
	})

	err := Apply(context.Background(), sess, proposals.Proposal{
		ID: 1, Status: proposals.Pending, Title: "Flagged Movie", TrackedID: 2,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotPath != "/api/v3/movie/2?deleteFiles=true" {
		t.Errorf("unexpected delete request: %s", gotPath)
	}
}

func TestApply_RejectsNonPendingProposal(t *testing.T) {
	sess := newTestSession(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("Apply must not make any HTTP call for a non-pending proposal")
	})

	for _, status := range []proposals.Status{proposals.Applied, proposals.Dismissed, proposals.Unmatched} {
		err := Apply(context.Background(), sess, proposals.Proposal{Status: status, TrackedID: 5})
		if err == nil {
			t.Errorf("expected Apply to refuse a %q proposal", status)
		}
	}
}

func TestApply_RejectsMissingTrackedID(t *testing.T) {
	sess := newTestSession(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("Apply must not make any HTTP call without a tracked id")
	})

	err := Apply(context.Background(), sess, proposals.Proposal{Status: proposals.Pending, TrackedID: 0})
	if err == nil {
		t.Fatal("expected Apply to refuse a proposal with no tracked id")
	}
}

func newTestWhisparrSession(t *testing.T, handler http.HandlerFunc) *mode.Session {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return &mode.Session{
		Mode:    mode.Adult,
		Servarr: servarr.New(servarr.Config{BaseURL: srv.URL, APIKey: "test-key", App: servarr.Whisparr}, srv.Client()),
	}
}

// TestScan_AdultWhisparrProposesOnlyItemsMatchingAllowlist is the explicit
// Whisparr counterpart to TestScan_ProposesOnlyItemsMatchingAllowlist. Purge
// needed no code changes to support Adult: Whisparr's itemResource() resolves
// to "movie" (the same default branch Radarr uses, client.go:66-73), so a
// Whisparr scene is fetched via the identical GET /api/v3/movie request a
// Radarr session already issues. This test locks that wire-path equivalence
// in as a regression guard rather than leaving it merely implied.
func TestScan_AdultWhisparrProposesOnlyItemsMatchingAllowlist(t *testing.T) {
	sess := newTestWhisparrSession(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/movie":
			w.Write([]byte(`[
				{"id":1,"title":"Vanilla Scene","path":"/media/Adult/Vanilla Scene","rootFolderPath":"/media/Adult","tags":[9]},
				{"id":2,"title":"Flagged Scene","path":"/media/Adult/Flagged Scene","rootFolderPath":"/media/Adult","tags":[1,2]}
			]`))
		case "/api/v3/tag":
			w.Write([]byte(`[{"id":1,"label":"BDSM"},{"id":2,"label":"unrelated"},{"id":9,"label":"family-friendly"}]`))
		default:
			t.Fatalf("unexpected request: %s", r.URL.Path)
		}
	})

	got, err := Scan(context.Background(), sess, []string{"BDSM"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected exactly 1 matched proposal, got %d: %+v", len(got), got)
	}
	p := got[0]
	if p.TrackedID != 2 || p.Title != "Flagged Scene" || p.Status != proposals.Pending {
		t.Errorf("unexpected proposal: %+v", p)
	}
}

// TestApply_AdultWhisparrDeletesTrackedScene confirms Apply's delete request
// against a Whisparr session hits the same movie-shaped path a Radarr session
// does — DeleteTracked routes through the same itemResource()="movie" branch
// (client.go:66-73), so there is no Adult-specific behavior to diverge.
func TestApply_AdultWhisparrDeletesTrackedScene(t *testing.T) {
	var gotPath string
	sess := newTestWhisparrSession(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.String()
	})

	err := Apply(context.Background(), sess, proposals.Proposal{
		ID: 1, Status: proposals.Pending, Title: "Flagged Scene", TrackedID: 2,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotPath != "/api/v3/movie/2?deleteFiles=true" {
		t.Errorf("unexpected delete request: %s", gotPath)
	}
}

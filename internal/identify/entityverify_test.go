package identify

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/labbersanon/sakms/internal/stashbox"
	"github.com/labbersanon/sakms/internal/throttle"
)

func newIdentifierWithFakes(t *testing.T, stashboxHandler, tpdbHandler http.HandlerFunc) *Identifier {
	t.Helper()
	return &Identifier{
		Boxes:    newBoxSearcherWithFakes(t, stashboxHandler, tpdbHandler),
		Throttle: throttle.New(0),
	}
}

func TestNormalizeForSearch(t *testing.T) {
	cases := map[string]string{
		"riley.reid":        "riley reid",
		"deep-desires":      "deep desires",
		"riley_reid":        "riley reid",
		"a.b-c_d":           "a b c d",
		"already clean":     "already clean",
		"  extra   spaces ": "extra spaces",
	}
	for in, want := range cases {
		if got := normalizeForSearch(in); got != want {
			t.Errorf("normalizeForSearch(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRejectNonStudioGuess(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"content-rating tag XXX", "XXX", ""},
		{"content-rating tag lowercase xxx", "xxx", ""},
		{"content-rating tag XXXX", "XXXX", ""},
		{"release-group-shaped tag", "WRB", ""},
		{"release-group-shaped tag 4 letters", "NBQ1", "NBQ1"}, // has a digit, not all-letters — passes through
		{"real studio name", "Tushy", "Tushy"},
		{"real multi-word studio name", "Wow Girls", "Wow Girls"},
		{"short all-caps acronym rejected by this bare function", "DDF", ""}, // rejectNonStudioGuess alone can't tell a real short-acronym studio from a release-group tag by shape — see TestVerifyStudio_ShortAcronymStudioStillSucceedsViaDBMatch, where verifyStudio's earlier DB-match step returns before this fallback is ever reached
		{"empty string", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := rejectNonStudioGuess(tc.in); got != tc.want {
				t.Errorf("rejectNonStudioGuess(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestBestMatch(t *testing.T) {
	names := []string{"Riley Reid", "Riley Star", "Someone Else"}
	got, ok := bestMatch("riley reid", names, 0.6)
	if !ok || got != "Riley Reid" {
		t.Fatalf("got (%q, %v), want (Riley Reid, true)", got, ok)
	}

	_, ok = bestMatch("totally unrelated name", names, 0.6)
	if ok {
		t.Fatal("expected no match above threshold for an unrelated name")
	}
}

func TestVerifyStudio_ConfidentMatchUsesCanonicalName(t *testing.T) {
	id := newIdentifierWithFakes(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"findStudio":{"id":"s1","name":"Tushy"}}}`))
	}, nil)

	got := id.verifyStudio(context.Background(), "tushy", "tushy.24.03.15.something")
	if got != "Tushy" {
		t.Fatalf("got %q, want the DB's canonical Tushy", got)
	}
}

func TestVerifyStudio_NoMatchFallsBackToCleanedGuess(t *testing.T) {
	id := newIdentifierWithFakes(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"findStudio":null}}`))
	}, nil)

	got := id.verifyStudio(context.Background(), "some.unknown-studio", "stem")
	if got != "some unknown studio" {
		t.Fatalf("got %q, want the deterministically-cleaned guess", got)
	}
}

func TestVerifyStudio_EmptyGuessReturnsEmpty(t *testing.T) {
	id := newIdentifierWithFakes(t, nil, nil)
	if got := id.verifyStudio(context.Background(), "", "stem"); got != "" {
		t.Fatalf("got %q, want empty string unchanged", got)
	}
}

// Confirmed real failure mode (see sakms_adult_ai_identification.md): on 2/8
// real test files, the AI returned a content-rating tag or release-group tag
// as "studio" instead of the real studio name. Neither can be fixed by
// fuzzy-matching against real studio names, so they must be rejected outright
// rather than passed through as Studio.
func TestVerifyStudio_RejectsContentRatingTag(t *testing.T) {
	id := newIdentifierWithFakes(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"findStudio":null}}`))
	}, nil)

	got := id.verifyStudio(context.Background(), "XXX", "ftvgirls.25.12.11.krystal.teen.orgasms.xxx.1080p")
	if got != "" {
		t.Fatalf("got %q, want empty (content-rating tag rejected, not passed through)", got)
	}
}

func TestVerifyStudio_RejectsReleaseGroupShapedTag(t *testing.T) {
	id := newIdentifierWithFakes(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"findStudio":null}}`))
	}, nil)

	got := id.verifyStudio(context.Background(), "WRB", "evilangel.25.11.11.rebel.rhyder.xxx.2160p.mp4-wrb")
	if got != "" {
		t.Fatalf("got %q, want empty (release-group-shaped tag rejected, not passed through)", got)
	}
}

func TestVerifyStudio_ShortAcronymStudioStillSucceedsViaDBMatch(t *testing.T) {
	// The denylist heuristic only gates the LAST-RESORT fallback — a real
	// short-acronym studio that a DB confirms should still return that DB's
	// canonical name, not get caught by looksLikeReleaseGroupTag.
	id := newIdentifierWithFakes(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"findStudio":{"id":"s1","name":"DDF"}}}`))
	}, nil)

	got := id.verifyStudio(context.Background(), "ddf", "ddf.24.01.01.some.scene")
	if got != "DDF" {
		t.Fatalf("got %q, want DDF's canonical DB name (not rejected)", got)
	}
}

func TestVerifyStudio_FansDBSkippedWithoutFansiteHint(t *testing.T) {
	fansdbCalls := 0
	boxes := map[string]*stashbox.Client{
		"stashdb": stashbox.New(stashbox.Config{Endpoint: newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":{"findStudio":null}}`))
		})}, &http.Client{Timeout: 5 * time.Second}),
		"fansdb": stashbox.New(stashbox.Config{Endpoint: newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
			fansdbCalls++
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":{"findStudio":null}}`))
		})}, &http.Client{Timeout: 5 * time.Second}),
	}
	id := &Identifier{Boxes: NewBoxSearcher(boxes, nil), Throttle: throttle.New(0)}

	id.verifyStudio(context.Background(), "Tushy", "tushy.24.03.15.some.scene")
	if fansdbCalls != 0 {
		t.Fatalf("expected fansdb to be skipped without a fansite hint, got %d calls", fansdbCalls)
	}

	id.verifyStudio(context.Background(), "Some Clip", "some.onlyfans.clip")
	if fansdbCalls != 1 {
		t.Fatalf("expected fansdb to be queried once a fansite hint is present, got %d calls", fansdbCalls)
	}
}

func newTestServer(t *testing.T, handler http.HandlerFunc) string {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestVerifyPerformers_ConfidentMatchUsesCanonicalName(t *testing.T) {
	id := newIdentifierWithFakes(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"searchPerformer":[{"id":"p1","name":"Riley Reid"}]}}`))
	}, nil)

	got := id.verifyPerformers(context.Background(), []string{"riley.reid"}, "stem", "")
	if len(got) != 1 || got[0] != "Riley Reid" {
		t.Fatalf("got %+v, want [Riley Reid]", got)
	}
}

func TestVerifyPerformers_NoMatchFallsBackToCleanedGuess(t *testing.T) {
	id := newIdentifierWithFakes(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"searchPerformer":[]}}`))
	}, nil)

	got := id.verifyPerformers(context.Background(), []string{"jane.doe"}, "stem", "")
	if len(got) != 1 || got[0] != "jane doe" {
		t.Fatalf("got %+v, want [jane doe] (cleaned, unmatched)", got)
	}
}

func TestVerifyPerformers_FallsBackToTPDBWhenStashboxUnconfigured(t *testing.T) {
	id := &Identifier{
		Boxes: newBoxSearcherWithFakes(t, nil, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[{"_id":"p1","name":"Riley Reid"}]}`))
		}),
		Throttle: throttle.New(0),
	}

	got := id.verifyPerformers(context.Background(), []string{"riley.reid"}, "stem", "")
	if len(got) != 1 || got[0] != "Riley Reid" {
		t.Fatalf("got %+v, want TPDB's canonical [Riley Reid]", got)
	}
}

func TestVerifyPerformers_EmptySliceReturnsEmptySlice(t *testing.T) {
	id := newIdentifierWithFakes(t, nil, nil)
	got := id.verifyPerformers(context.Background(), []string{}, "stem", "")
	if len(got) != 0 {
		t.Fatalf("got %+v, want empty", got)
	}
}

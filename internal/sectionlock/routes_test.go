package sectionlock

import (
	"reflect"
	"testing"
)

// SL-6: classification returns a SET, not a single id.
//
// The headline case is the one the plan names: /api/modes/adult/rename/scan
// is BOTH an Organize route and an Adult route, so it must be gated when
// either is locked. A classifier that returned one id would have to pick,
// and either choice silently drops half of AC6/AC7.
func TestClassifyReturnsSet(t *testing.T) {
	got := Classify("/api/modes/adult/rename/scan").Sorted()
	want := []string{SectionAdultContent, SectionOrganize}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Classify(/api/modes/adult/rename/scan) = %v, want %v", got, want)
	}
}

func TestClassify(t *testing.T) {
	cases := []struct {
		path string
		want []string
	}{
		// The Adult rule: the segment after /api/modes/ decides, and it
		// applies whether or not the rest of the path maps to a tab.
		{"/api/modes/adult/discover", []string{SectionAdultContent, SectionDiscover}},
		{"/api/modes/movies/discover", []string{SectionDiscover}},
		{"/api/modes/adult/dedup/scan/stream", []string{SectionAdultContent, SectionOrganize}},
		{"/api/modes/series/dedup/scan/status", []string{SectionOrganize}},
		{"/api/modes/adult/scenes/tags", []string{SectionAdultContent, SectionTag}},
		{"/api/modes/movies/items/12/tags/3", []string{SectionTag}},
		{"/api/modes/movies/collections", []string{SectionCollections}},
		{"/api/modes/movies/tracked", []string{SectionLibrary}},
		{"/api/modes/series/library/9/seasons", []string{SectionLibrary}},
		{"/api/modes/movies/grabs", []string{SectionQueue}},
		{"/api/modes/adult/search", []string{SectionAdultContent, SectionDiscover}},
		{"/api/modes/adult/performers/4/scenes", []string{SectionAdultContent, SectionDiscover}},
		{"/api/modes/adult/newest-rows", []string{SectionAdultContent, SectionDiscover}},

		// identify-enabled is only ever requested for Adult in practice
		// (other modes 400) and is caught by the Adult rule for free.
		{"/api/modes/adult/identify-enabled", []string{SectionAdultContent, SectionSettings}},

		// Mode-scoped settings controls belong to the Settings screen; the
		// Library screen's own data does not.
		{"/api/modes/movies/library/root-folder", []string{SectionSettings}},
		{"/api/modes/movies/naming-preset", []string{SectionSettings}},
		{"/api/modes/movies/quality-prefs", []string{SectionSettings}},

		// /api/settings/adult-* is the second Adult rule.
		{"/api/settings/adult-mode-enabled", []string{SectionAdultContent, SectionSettings}},
		{"/api/settings/adult-newest-scan-interval", []string{SectionAdultContent, SectionSettings}},
		{"/api/settings/dedup-scan-interval", []string{SectionSettings}},

		// The third Adult rule: {screen}=="adult" on the two discover
		// row-configuration routes.
		{"/api/discover/row-order/adult", []string{SectionAdultContent, SectionDiscover}},
		{"/api/discover/row-hidden/adult", []string{SectionAdultContent, SectionDiscover}},
		{"/api/discover/row-order/mainstream", []string{SectionDiscover}},
		{"/api/discover/row-hidden/mainstream", []string{SectionDiscover}},

		// Discover's own surface, including the RSS feeds AC6 names.
		{"/api/discover/rss-feeds", []string{SectionDiscover}},
		{"/api/discover/rss-feeds/7/resolve", []string{SectionDiscover}},
		{"/api/discover/sliders", []string{SectionDiscover}},
		{"/api/autograb-batch", []string{SectionDiscover}},
		{"/api/trakt/watchlist", []string{SectionDiscover}},
		{"/api/trakt/credentials", []string{SectionSettings}},

		// Queue.
		{"/api/downloads", []string{SectionQueue}},
		{"/api/downloads/abc/pause", []string{SectionQueue}},
		{"/api/requests/exclude-batch", []string{SectionQueue}},
		{"/api/calendar/upcoming/movies", []string{SectionQueue}},
		{"/api/grabs/4/check-import", []string{SectionQueue}},

		// Organize. Note apply-batch has NO {id} segment — a prefix rule
		// keyed on /api/proposals/{id}/ would miss it entirely.
		{"/api/proposals/12/apply", []string{SectionOrganize}},
		{"/api/proposals/apply-batch", []string{SectionOrganize}},
		{"/api/proposals/12/dismiss", []string{SectionOrganize}},

		// Dashboard widgets vs. genuinely administrative controls, which
		// share the /api/admin prefix.
		{"/api/admin/sysinfo/stream", []string{SectionDashboard}},
		{"/api/admin/storage-allocation", []string{SectionDashboard}},
		{"/api/admin/watch-folders", []string{SectionSettings}},

		// Settings.
		{"/api/connections", []string{SectionSettings}},
		{"/api/service-connections/3/modes", []string{SectionSettings}},
		{"/api/webhooks/2/test", []string{SectionSettings}},
		{"/api/nodes/stream", []string{SectionSettings}},
		{"/api/downloader/config", []string{SectionSettings}},

		// Deliberately ungated, each for a reason stated in routes.go.
		{"/api/images/proxy", nil},
		{"/api/modes/adult/poster", []string{SectionAdultContent}},
		{"/api/modes/movies/poster", nil},
		{"/api/notifications/stream", nil},
		{"/api/apikey/regenerate", nil},
		{"/api/auth/mode", nil},
		{"/api/section-lock/status", nil},
		{"/api/setup/status", nil},
		{"/healthz", nil},
		{"/api/openapi.yaml", nil},

		// Non-API paths: the SPA shell and its assets are never gated —
		// locked tabs stay visible and render a PIN overlay.
		{"/", nil},
		{"/organize", nil},
		{"/assets/index-abc123.js", nil},
	}

	for _, tc := range cases {
		want := tc.want
		if want == nil {
			want = []string{}
		}
		if got := Classify(tc.path).Sorted(); !reflect.DeepEqual(got, want) {
			t.Errorf("Classify(%q) = %v, want %v", tc.path, got, want)
		}
	}
}

// SL-7: classification runs path.Clean, so every traversal variant of a
// path classifies identically to its canonical form.
//
// This is not hypothetical tidiness. auth.Middleware sees the raw URL
// path; http.ServeMux routes the cleaned one. Without cleaning HERE,
// /api/modes/movies/../adult/rename/scan classifies on the movies branch —
// no adult-content — while the request is dispatched to the Adult handler.
// That is a bypass of the feature's primary mechanism.
func TestClassifyCleansPath(t *testing.T) {
	canonical := Classify("/api/modes/adult/rename/scan").Sorted()
	if len(canonical) != 2 {
		t.Fatalf("precondition failed: canonical classification = %v", canonical)
	}

	variants := []string{
		"/api/modes/adult/rename/scan/",
		"/api/modes/adult/./rename/scan",
		"/api/modes/adult//rename//scan",
		"/api/modes/movies/../adult/rename/scan",
		"/api/modes/adult/rename/purge/../scan",
		"//api/modes/adult/rename/scan",
		"/api/discover/../modes/adult/rename/scan",
	}
	for _, v := range variants {
		if got := Classify(v).Sorted(); !reflect.DeepEqual(got, canonical) {
			t.Errorf("Classify(%q) = %v, want %v (same as the canonical path)", v, got, canonical)
		}
	}
}

// The mirror image of the test above: cleaning must not INVENT Adult scope
// either. A path that traverses out of the adult segment is not an Adult
// route, and gating it would be a false positive that breaks Mainstream.
func TestClassifyDoesNotInventAdultScope(t *testing.T) {
	for _, p := range []string{
		"/api/modes/adult/../movies/rename/scan",
		"/api/modes/adultery/rename/scan",
		"/api/modes/movies/rename/scan",
	} {
		if Classify(p).Has(SectionAdultContent) {
			t.Errorf("Classify(%q) wrongly claimed adult-content", p)
		}
	}
}

// An unclassified path allows by default. This is safe only because the
// classification-completeness static test (SL-9, W2) proves every route
// pattern in the app is explicitly enumerated — recorded here so the
// default is not mistaken for an oversight.
func TestClassifyUnknownPathIsEmpty(t *testing.T) {
	if got := Classify("/api/some-future-route/that-nobody-classified"); got.Len() != 0 {
		t.Fatalf("expected an unknown API path to classify as nothing, got %v", got.Sorted())
	}
}

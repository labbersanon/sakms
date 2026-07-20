package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/proposals"
)

// TestSubmitDraftHandler_GivesUnmatchedProposalBackToTPDB proves the
// end-to-end wiring for give-back: a pre-existing Unmatched Adult Rename
// proposal (as Scan would leave a web-identified-only match) is submitted via
// POST /api/proposals/{id}/submit-draft, reaches the configured TPDB GraphQL
// fake, and the proposal's DraftID/DraftSubmittedAt persist afterward.
func TestSubmitDraftHandler_GivesUnmatchedProposalBackToTPDB(t *testing.T) {
	fakeWhisparr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("submit-draft must never call the *arr app, got %s %s", r.Method, r.URL.Path)
	}))
	defer fakeWhisparr.Close()

	var gotTitle, gotStudio string
	fakeTPDB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Variables struct {
				Input struct {
					Title  string `json:"title"`
					Studio struct {
						Name string `json:"name"`
					} `json:"studio"`
				} `json:"input"`
			} `json:"variables"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		gotTitle, gotStudio = req.Variables.Input.Title, req.Variables.Input.Studio.Name
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"submitSceneDraft": map[string]any{"id": "draft123"}}})
	}))
	defer fakeTPDB.Close()

	// TPDB's GraphQL endpoint is hardcoded in production (a single fixed
	// public service) — override it here to point at the fake.
	prevTPDBGraphQLURL := mode.TPDBGraphQLURL
	mode.TPDBGraphQLURL = fakeTPDB.URL
	t.Cleanup(func() { mode.TPDBGraphQLURL = prevTPDBGraphQLURL })

	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	ctx := context.Background()
	for _, c := range []struct{ service, url string }{
		{"whisparr", fakeWhisparr.URL},
		{"tpdb", fakeTPDB.URL},
	} {
		if err := connStore.Upsert(ctx, c.service, c.url, "test-key"); err != nil {
			t.Fatalf("seeding %s connection: %v", c.service, err)
		}
	}
	if err := settingsStore.Set(ctx, mode.AIModelKey, "test-model"); err != nil {
		t.Fatalf("seeding ollama model: %v", err)
	}
	// buildIdentifier requires an Ollama connection as its backbone, even
	// though this test's path never actually calls it.
	fakeOllama := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("Ollama must not be called by submit-draft")
	}))
	defer fakeOllama.Close()
	if err := connStore.Upsert(ctx, "ollama", fakeOllama.URL, "test-key"); err != nil {
		t.Fatalf("seeding ollama connection: %v", err)
	}

	saved, err := propStore.ReplacePending(ctx, mode.Adult, proposals.Rename, []proposals.Proposal{
		{
			Status: proposals.Unmatched, SourceName: "Some Scene", SourcePath: "/media/Adult/Some Scene",
			RootFolderPath: "/media/Adult", Title: "Some Scene", Studio: "Some Studio", Date: "2024",
			Reason: "web-identified only (no scene ID) — needs manual review",
		},
	})
	if err != nil {
		t.Fatalf("seeding proposal: %v", err)
	}

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, nil, nil, nil, nil, nil))
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/api/proposals/"+strconv.FormatInt(saved[0].ID, 10)+"/submit-draft", "application/json", nil)
	if err != nil {
		t.Fatalf("submit-draft POST failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var updated proposals.Proposal
	if err := json.NewDecoder(resp.Body).Decode(&updated); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if updated.DraftID != "draft123" || updated.DraftSubmittedAt == "" {
		t.Fatalf("expected the draft id/timestamp to persist, got %+v", updated)
	}
	if gotTitle != "Some Scene" || gotStudio != "Some Studio" {
		t.Fatalf("expected the proposal's title/studio to reach TPDB, got title=%q studio=%q", gotTitle, gotStudio)
	}
}

// A Pending proposal has already been matched — nothing to give back.
func TestSubmitDraftHandler_RejectsPendingProposal(t *testing.T) {
	fakeWhisparr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("must not call the *arr app")
	}))
	defer fakeWhisparr.Close()

	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	ctx := context.Background()
	if err := connStore.Upsert(ctx, "whisparr", fakeWhisparr.URL, "test-key"); err != nil {
		t.Fatalf("seeding whisparr connection: %v", err)
	}

	saved, err := propStore.ReplacePending(ctx, mode.Adult, proposals.Rename, []proposals.Proposal{
		{Status: proposals.Pending, SourceName: "X", Title: "X", ForeignID: "abc", ItemType: "scene"},
	})
	if err != nil {
		t.Fatalf("seeding proposal: %v", err)
	}

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, nil, nil, nil, nil, nil))
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/api/proposals/"+strconv.FormatInt(saved[0].ID, 10)+"/submit-draft", "application/json", nil)
	if err != nil {
		t.Fatalf("submit-draft POST failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		t.Fatal("expected submit-draft to reject a Pending proposal")
	}
}

package identify

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/labbersanon/sakms/internal/stashbox"
	"github.com/labbersanon/sakms/internal/throttle"
)

// genderBox serves the stash-box searchPerformer query: for a term in
// genders it returns one name-matched performer carrying that raw gender; for
// a term in errNames it returns a GraphQL error envelope (a reached-but-failed
// box); otherwise an empty result (a genuine reached-no-match). Distinguishes
// the exact three outcomes PerformerGender must tell apart.
func genderBox(t *testing.T, genders map[string]string, errNames map[string]bool) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Variables map[string]any `json:"variables"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		term, _ := req.Variables["term"].(string)
		w.Header().Set("Content-Type", "application/json")
		if errNames[term] {
			fmt.Fprint(w, `{"errors":[{"message":"boom"}]}`)
			return
		}
		if g, ok := genders[term]; ok {
			fmt.Fprintf(w, `{"data":{"searchPerformer":[{"id":"p1","name":%q,"gender":%q,"images":[{"url":""}]}]}}`, term, g)
			return
		}
		fmt.Fprint(w, `{"data":{"searchPerformer":[]}}`)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func genderIdentifier(boxURL string, thr *throttle.Throttle) *Identifier {
	return &Identifier{
		Boxes: NewBoxSearcher(map[string]*stashbox.Client{
			"stashdb": stashbox.New(stashbox.Config{Endpoint: boxURL, APIKey: "k"}, &http.Client{Timeout: 5 * time.Second}),
		}, nil),
		Throttle: thr,
	}
}

// TestPerformerGender_MatchReturnsNormalizedGender: a name-matched candidate
// yields (normalizeGender(p.Gender), nil).
func TestPerformerGender_MatchReturnsNormalizedGender(t *testing.T) {
	srv := genderBox(t, map[string]string{"Riley Reid": "FEMALE"}, nil)
	id := genderIdentifier(srv.URL, throttle.New(0))
	g, err := id.PerformerGender(context.Background(), "Riley Reid")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if g != "female" {
		t.Errorf("gender = %q, want %q", g, "female")
	}
}

// TestPerformerGender_ReachedNoMatchReturnsEmptyNoError: a box reached with no
// matching candidate is a genuine negative — ("", nil), NOT an error.
func TestPerformerGender_ReachedNoMatchReturnsEmptyNoError(t *testing.T) {
	srv := genderBox(t, nil, nil)
	id := genderIdentifier(srv.URL, throttle.New(0))
	g, err := id.PerformerGender(context.Background(), "Nobody Here")
	if err != nil {
		t.Fatalf("expected no error for a reached-but-no-match box, got %v", err)
	}
	if g != "" {
		t.Errorf("gender = %q, want empty", g)
	}
}

// TestPerformerGender_ReachedUnrecognizedGenderReturnsEmptyNoError: a matched
// performer whose raw gender normalizes to "" (e.g. TRANSGENDER_FEMALE) is
// still a reached answer — ("", nil), safe to persist as a real negative.
func TestPerformerGender_ReachedUnrecognizedGenderReturnsEmptyNoError(t *testing.T) {
	srv := genderBox(t, map[string]string{"Someone": "TRANSGENDER_FEMALE"}, nil)
	id := genderIdentifier(srv.URL, throttle.New(0))
	g, err := id.PerformerGender(context.Background(), "Someone")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if g != "" {
		t.Errorf("gender = %q, want empty (unrecognized token normalizes to '')", g)
	}
}

// TestPerformerGender_BoxErrorReturnsError: the only configured box erroring
// (all boxes errored/aborted) yields ("", lastErr) — NOT a negative answer, so
// the caller leaves the row NULL.
func TestPerformerGender_BoxErrorReturnsError(t *testing.T) {
	srv := genderBox(t, nil, map[string]bool{"Riley Reid": true})
	id := genderIdentifier(srv.URL, throttle.New(0))
	g, err := id.PerformerGender(context.Background(), "Riley Reid")
	if err == nil {
		t.Fatalf("expected an error when the only box errored, got (%q, nil)", g)
	}
	if g != "" {
		t.Errorf("gender = %q, want empty on error", g)
	}
}

// TestPerformerGender_ThrottleAbortReturnsError: a cancelled context that makes
// Throttle.Wait return an error must surface as ("", err) (a transient abort),
// never be mistaken for a reached-no-gender ("", nil). Primes the throttle with
// a long interval so the resolver's own Wait actually blocks and observes the
// cancelled context.
func TestPerformerGender_ThrottleAbortReturnsError(t *testing.T) {
	thr := throttle.New(time.Hour)
	// Prime "stashdb": the first Wait returns immediately (wait==0) but pushes
	// the next allowed time an hour out, so the resolver's Wait below blocks.
	if err := thr.Wait(context.Background(), "stashdb"); err != nil {
		t.Fatalf("priming throttle: %v", err)
	}
	srv := genderBox(t, map[string]string{"Riley Reid": "FEMALE"}, nil)
	id := genderIdentifier(srv.URL, thr)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled: the blocking Wait must abort
	g, err := id.PerformerGender(ctx, "Riley Reid")
	if err == nil {
		t.Fatalf("expected a throttle-abort error, got (%q, nil)", g)
	}
	if g != "" {
		t.Errorf("gender = %q, want empty on throttle abort", g)
	}
}

// TestPerformerGender_EmptyNameIsNoError: a blank name short-circuits to
// ("", nil) without touching any box.
func TestPerformerGender_EmptyNameIsNoError(t *testing.T) {
	id := genderIdentifier("http://127.0.0.1:0", throttle.New(0))
	if g, err := id.PerformerGender(context.Background(), ""); err != nil || g != "" {
		t.Errorf("PerformerGender(\"\") = (%q, %v), want (\"\", nil)", g, err)
	}
}

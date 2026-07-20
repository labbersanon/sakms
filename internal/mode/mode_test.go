package mode

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/labbersanon/sakms/internal/anthropic"
	"github.com/labbersanon/sakms/internal/bravesearch"
	"github.com/labbersanon/sakms/internal/connections"
	"github.com/labbersanon/sakms/internal/db"
	"github.com/labbersanon/sakms/internal/gemini"
	"github.com/labbersanon/sakms/internal/jellyfin"
	"github.com/labbersanon/sakms/internal/openai"
	"github.com/labbersanon/sakms/internal/secrets"
	"github.com/labbersanon/sakms/internal/settings"
	"github.com/labbersanon/sakms/internal/stashapi"
	"github.com/labbersanon/sakms/internal/stashbox"
)

// overrideAIProviderBaseURL points a cloud AI provider's hardcoded
// DefaultBaseURL package var at u for the duration of the test, restoring it
// on cleanup. buildAIClient/buildIdentifier now ignore Connection.URL for
// openai/gemini/anthropic/brave and read the package var instead, so a test
// that stands up a fake server must redirect the var, not just store the
// connection URL. No-op for ollama, which legitimately still uses
// Connection.URL.
func overrideAIProviderBaseURL(t *testing.T, provider, u string) {
	t.Helper()
	switch provider {
	case AIProviderOpenAI:
		prev := openai.DefaultBaseURL
		openai.DefaultBaseURL = u
		t.Cleanup(func() { openai.DefaultBaseURL = prev })
	case AIProviderGemini:
		prev := gemini.DefaultBaseURL
		gemini.DefaultBaseURL = u
		t.Cleanup(func() { gemini.DefaultBaseURL = prev })
	case AIProviderAnthropic:
		prev := anthropic.DefaultBaseURL
		anthropic.DefaultBaseURL = u
		t.Cleanup(func() { anthropic.DefaultBaseURL = prev })
	case "brave":
		prev := bravesearch.DefaultBaseURL
		bravesearch.DefaultBaseURL = u
		t.Cleanup(func() { bravesearch.DefaultBaseURL = prev })
	}
}

// newTestStores opens one fresh db and returns a connections store and a
// settings store backed by it, so a test can configure both the connections
// and the Ollama-model setting that Build reads.
func newTestStores(t *testing.T) (*connections.Store, *settings.Store) {
	t.Helper()
	sqlDB, err := db.Open(filepath.Join(t.TempDir(), "sakms.db"))
	if err != nil {
		t.Fatalf("opening db: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	secretStore, err := secrets.New(make([]byte, 32))
	if err != nil {
		t.Fatalf("building secret store: %v", err)
	}
	return connections.New(sqlDB, secretStore), settings.New(sqlDB)
}

// TestBuild_Movies_NoServarrConnectionRequired proves Movies builds a
// working session with ZERO connections configured — SAK owns its own
// Movies library instead of proxying Radarr, so there's no "radarr isn't
// configured yet" error to hit anymore (see Build's doc comment).
func TestBuild_Movies_NoServarrConnectionRequired(t *testing.T) {
	store, settingsStore := newTestStores(t)

	sess, err := Build(context.Background(), store, settingsStore, &http.Client{Timeout: time.Second}, nil, Movies)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sess.Mode != Movies {
		t.Errorf("expected Mode to be Movies, got %v", sess.Mode)
	}
	if sess.Servarr != nil {
		t.Errorf("expected a nil Servarr client for Movies, got %+v", sess.Servarr)
	}
	if sess.Identify != nil {
		t.Error("expected Identify to be nil for a Movies session")
	}
}

func TestBuild_Series_NoServarrConnectionRequired(t *testing.T) {
	store, settingsStore := newTestStores(t)

	sess, err := Build(context.Background(), store, settingsStore, &http.Client{Timeout: time.Second}, nil, Series)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sess.Mode != Series {
		t.Errorf("expected Mode to be Series, got %v", sess.Mode)
	}
	if sess.Servarr != nil {
		t.Errorf("expected a nil Servarr client for Series, got %+v", sess.Servarr)
	}
	if sess.Identify != nil {
		t.Error("expected Identify to be nil for a Series session")
	}
}

// TestBuild_Adult_NoServarrConnectionRequired proves Adult no longer needs a
// Whisparr connection (Whisparr eliminated, Stage 4) — Build succeeds with no
// connections configured at all and leaves sess.Servarr nil, exactly like
// Movies/Series now do.
func TestBuild_Adult_NoServarrConnectionRequired(t *testing.T) {
	store, settingsStore := newTestStores(t)

	sess, err := Build(context.Background(), store, settingsStore, &http.Client{Timeout: time.Second}, nil, Adult)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sess.Mode != Adult {
		t.Errorf("expected Mode to be Adult, got %v", sess.Mode)
	}
	if sess.Servarr != nil {
		t.Errorf("expected a nil Servarr client for Adult now, got %+v", sess.Servarr)
	}
}

// TestBuild_Adult_ServarrAlwaysNil proves that even with a "whisparr"
// connection still configured, Build constructs no Servarr client for Adult —
// the wiring that read that connection is gone (Stage 4), though the shared
// internal/servarr client package is retained.
func TestBuild_Adult_ServarrAlwaysNil(t *testing.T) {
	store, settingsStore := newTestStores(t)
	ctx := context.Background()
	if err := store.Upsert(ctx, "whisparr", "http://whisparr.local:6969", "whisparr-key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sess, err := Build(ctx, store, settingsStore, &http.Client{Timeout: time.Second}, nil, Adult)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sess.Servarr != nil {
		t.Errorf("expected sess.Servarr to stay nil for Adult even with whisparr configured, got %+v", sess.Servarr)
	}
}

func TestBuild_UnknownMode(t *testing.T) {
	store, settingsStore := newTestStores(t)
	_, err := Build(context.Background(), store, settingsStore, &http.Client{}, nil, Mode("bogus"))
	if err == nil {
		t.Fatal("expected an error for an unknown mode")
	}
	if errors.Is(err, connections.ErrNotFound) {
		t.Error("an unknown mode should fail before ever touching the connections store")
	}
}

// TestBuild_AdultOnlyWhisparr_IdentifyBuiltWithoutAI confirms that Adult
// Identify is always built (never nil — DB-first parsing needs no AI), and
// that the AI client remains nil when no AI provider connection exists. Tag,
// which never reads Identify, is unaffected either way.
func TestBuild_AdultOnlyWhisparr_IdentifyBuiltWithoutAI(t *testing.T) {
	store, settingsStore := newTestStores(t)
	ctx := context.Background()
	if err := store.Upsert(ctx, "whisparr", "http://whisparr.local:6969", "whisparr-key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sess, err := Build(ctx, store, settingsStore, &http.Client{Timeout: time.Second}, nil, Adult)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sess.Identify == nil {
		t.Error("expected Identify to be non-nil for Adult (DB-first parsing requires no AI)")
	}
	if sess.Identify != nil && sess.Identify.AI != nil {
		t.Error("expected Identify.AI to be nil when no AI provider is configured")
	}
}

// TestBuild_AdultNoStashConnection_SessionStashNil confirms Stash stays nil
// (not an error) when no "stash" connection is configured — phash-first
// identification is purely additive, so this must never block Adult from
// building otherwise.
func TestBuild_AdultNoStashConnection_SessionStashNil(t *testing.T) {
	store, settingsStore := newTestStores(t)
	ctx := context.Background()
	if err := store.Upsert(ctx, "whisparr", "http://whisparr.local:6969", "whisparr-key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sess, err := Build(ctx, store, settingsStore, &http.Client{Timeout: time.Second}, nil, Adult)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sess.Stash != nil {
		t.Error("expected Stash to be nil when no stash connection is configured")
	}
}

// TestBuild_AdultStashConnectionConfigured_PopulatesSessionStash confirms a
// configured "stash" connection populates sess.Stash.
func TestBuild_AdultStashConnectionConfigured_PopulatesSessionStash(t *testing.T) {
	store, settingsStore := newTestStores(t)
	ctx := context.Background()
	if err := store.Upsert(ctx, "whisparr", "http://whisparr.local:6969", "whisparr-key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := store.Upsert(ctx, "stash", "http://stash.local:9999/graphql", "stash-key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sess, err := Build(ctx, store, settingsStore, &http.Client{Timeout: time.Second}, nil, Adult)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sess.Stash == nil {
		t.Error("expected Stash to be populated when a stash connection is configured")
	}
}

// TestBuild_AdultSettingsStoreError_Propagates guards against buildIdentifier
// collapsing a REAL settings-store failure into the same "not configured, no
// error" outcome as an unset model. A real error must propagate (per
// buildIdentifier's own doc comment), not be silently swallowed as nil,nil —
// swallowing it would look identical to "identification not configured" from
// the caller's side, hiding an actual outage behind a misleading success.
func TestBuild_AdultSettingsStoreError_Propagates(t *testing.T) {
	sqlDB, err := db.Open(filepath.Join(t.TempDir(), "sakms.db"))
	if err != nil {
		t.Fatalf("opening db: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	secretStore, err := secrets.New(make([]byte, 32))
	if err != nil {
		t.Fatalf("building secret store: %v", err)
	}
	store := connections.New(sqlDB, secretStore)
	settingsStore := settings.New(sqlDB)
	ctx := context.Background()

	if err := store.Upsert(ctx, "whisparr", "http://whisparr.local:6969", "whisparr-key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := store.Upsert(ctx, "ollama", "http://ollama.local:11434", ""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Force a real (non-ErrNotFound) failure on the settings.Get call inside
	// buildIdentifier, without touching the connections table it already read.
	if _, err := sqlDB.Exec(`DROP TABLE settings`); err != nil {
		t.Fatalf("dropping settings table: %v", err)
	}

	_, err = Build(ctx, store, settingsStore, &http.Client{Timeout: time.Second}, nil, Adult)
	if err == nil {
		t.Fatal("expected a real settings-store error to propagate, got nil")
	}
	if strings.Contains(err.Error(), "not configured") {
		t.Errorf("a real store error must not be reported as merely unconfigured, got: %v", err)
	}
}

// TestBuild_AdultOllamaConnButNoModelSetting_IdentifyBuiltAINil pins that
// an Ollama connection with NO model setting leaves Identify.AI nil (no model
// guessed), and does not panic. Identify itself is now always non-nil for
// Adult (DB-first parsing runs without AI).
func TestBuild_AdultOllamaConnButNoModelSetting_IdentifyBuiltAINil(t *testing.T) {
	store, settingsStore := newTestStores(t)
	ctx := context.Background()
	if err := store.Upsert(ctx, "whisparr", "http://whisparr.local:6969", "whisparr-key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := store.Upsert(ctx, "ollama", "http://ollama.local:11434", ""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sess, err := Build(ctx, store, settingsStore, &http.Client{Timeout: time.Second}, nil, Adult)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sess.Identify == nil {
		t.Error("expected Identify to be non-nil for Adult (DB-first parsing requires no AI)")
	}
	if sess.Identify != nil && sess.Identify.AI != nil {
		t.Error("expected Identify.AI to be nil when the Ollama model setting is unset (no guessed fallback)")
	}
}

// TestBuild_AdultWithIdentificationConnections_PopulatesIdentify confirms that
// with whisparr + ollama + model + a stash-box connection configured, Build
// produces a non-nil Identifier with a non-nil Boxes searcher. Box-map
// internals are unexported, so behavior is proven in the functional test
// below; here we assert the pipeline is assembled at all.
func TestBuild_AdultWithIdentificationConnections_PopulatesIdentify(t *testing.T) {
	store, settingsStore := newTestStores(t)
	ctx := context.Background()
	for _, c := range []struct{ service, url, key string }{
		{"whisparr", "http://whisparr.local:6969", "whisparr-key"},
		{"stashdb", "http://stashdb.local/graphql", "stashdb-key"},
		{"tpdb", "http://tpdb.local", "tpdb-key"},
		{"ollama", "http://ollama.local:11434", ""},
	} {
		if err := store.Upsert(ctx, c.service, c.url, c.key); err != nil {
			t.Fatalf("unexpected error upserting %s: %v", c.service, err)
		}
	}
	if err := settingsStore.Set(ctx, AIModelKey, "qwen2.5vl:7b"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := settingsStore.Set(ctx, AIFallbackEnabledKey, "true"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sess, err := Build(ctx, store, settingsStore, &http.Client{Timeout: time.Second}, nil, Adult)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sess.Identify == nil {
		t.Fatal("expected a non-nil Identify with ollama+model+stashdb configured")
	}
	if sess.Identify.Boxes == nil {
		t.Error("expected the Identifier's Boxes searcher to be non-nil")
	}
	if sess.Identify.AI == nil {
		t.Error("expected the Identifier's AI client to be non-nil")
	}
}

// TestBuild_AdultIdentifierIsFunctional is the teeth of this slice: it proves
// stored connections + the model setting actually become a WORKING identifier,
// not just a non-nil struct. It stands up httptest fakes for Ollama (returns a
// parsed filename) and a stash-box (returns one matching scene), builds an
// Adult Session pointed at them, then drives sess.Identify.Identify end-to-end
// and asserts a real MatchResult comes back from the fakes. No proposal is
// persisted, no Apply runs, and Whisparr is never called (a placeholder URL is
// enough — Build constructs but never calls the servarr client before the
// Adult branch).
func TestBuild_AdultIdentifierIsFunctional(t *testing.T) {
	const (
		wantTitle  = "Some Scene"
		wantStudio = "Tushy"
		wantScene  = "scene-abc-123"
	)

	// Fake Ollama: parses the filename into a title/studio JSON payload.
	ollamaSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeTestJSON(t, w, map[string]any{
			"message": map[string]any{
				"content": `{"studio":"Tushy","title":"Some Scene","year":"2019","performers":null}`,
			},
		})
	}))
	defer ollamaSrv.Close()

	// Fake stash-box (StashDB): a searchScene GraphQL query returns one scene
	// whose title/studio match what Ollama parsed.
	stashSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Variables struct {
				Term string `json:"term"`
				ID   string `json:"id"`
			} `json:"variables"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Variables.ID != "" {
			writeTestJSON(t, w, map[string]any{"data": map[string]any{"findScene": nil}})
			return
		}
		writeTestJSON(t, w, map[string]any{"data": map[string]any{"searchScene": []map[string]any{
			{
				"id": wantScene, "title": wantTitle, "release_date": "2019-05-01",
				"studio": map[string]any{"name": wantStudio, "parent": nil},
			},
		}}})
	}))
	defer stashSrv.Close()
	// buildIdentifier now builds the stash-box client against the hardcoded
	// stashbox.StashDBURL, not Connection.URL — point it at the fake for this
	// test so the identification call lands on stashSrv.
	prevStashDBURL := stashbox.StashDBURL
	stashbox.StashDBURL = stashSrv.URL
	defer func() { stashbox.StashDBURL = prevStashDBURL }()

	store, settingsStore := newTestStores(t)
	ctx := context.Background()
	// Placeholder whisparr — Build constructs a servarr client but never calls
	// it before the Adult identifier branch.
	if err := store.Upsert(ctx, "whisparr", "http://whisparr.invalid", "k"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := store.Upsert(ctx, "ollama", ollamaSrv.URL, ""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := store.Upsert(ctx, "stashdb", stashSrv.URL, "k"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := settingsStore.Set(ctx, AIModelKey, "test-model"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := settingsStore.Set(ctx, AIFallbackEnabledKey, "true"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sess, err := Build(ctx, store, settingsStore, &http.Client{Timeout: 5 * time.Second}, nil, Adult)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sess.Identify == nil {
		t.Fatal("expected a functioning Identify from stored connections + model")
	}

	res, err := sess.Identify.Identify(ctx, "tushy-some-scene", "scenes")
	if err != nil {
		t.Fatalf("Identify returned an error: %v", err)
	}
	if res == nil {
		t.Fatal("expected a real MatchResult from the fake stash-box, got nil")
	}
	if res.SceneID != wantScene {
		t.Errorf("expected SceneID %q, got %q", wantScene, res.SceneID)
	}
	if res.Type != "scene" {
		t.Errorf("expected Type \"scene\", got %q", res.Type)
	}
	if res.Box != "stashdb" {
		t.Errorf("expected Box \"stashdb\", got %q", res.Box)
	}
}

// TestBuild_MainstreamAI_NilWithoutOllamaConnection confirms Movies/Series
// sessions get a nil MainstreamAI when no Ollama connection is configured at
// all — Rename's AI fallback is simply skipped, matching Identify's own
// "absent = not configured, never guess" rule.
func TestBuild_MainstreamAI_NilWithoutOllamaConnection(t *testing.T) {
	store, settingsStore := newTestStores(t)
	ctx := context.Background()
	if err := store.Upsert(ctx, "radarr", "http://radarr.local:7878", "radarr-key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sess, err := Build(ctx, store, settingsStore, &http.Client{Timeout: time.Second}, nil, Movies)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sess.MainstreamAI != nil {
		t.Error("expected MainstreamAI to be nil without an Ollama connection")
	}
}

// TestBuild_MainstreamAI_NilWithoutModelSetting confirms an Ollama connection
// alone isn't enough — the AIModelKey setting must also be set, same
// two-part gate as Adult's Identify.
func TestBuild_MainstreamAI_NilWithoutModelSetting(t *testing.T) {
	store, settingsStore := newTestStores(t)
	ctx := context.Background()
	if err := store.Upsert(ctx, "radarr", "http://radarr.local:7878", "radarr-key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := store.Upsert(ctx, "ollama", "http://ollama.local:11434", ""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sess, err := Build(ctx, store, settingsStore, &http.Client{Timeout: time.Second}, nil, Movies)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sess.MainstreamAI != nil {
		t.Error("expected MainstreamAI to be nil without the AIModelKey setting")
	}
}

// TestBuild_MainstreamAI_PopulatedWhenConfigured proves the happy path for
// every mode Rename actually applies this to.
func TestBuild_MainstreamAI_PopulatedWhenConfigured(t *testing.T) {
	for _, m := range []Mode{Movies, Series} {
		t.Run(string(m), func(t *testing.T) {
			store, settingsStore := newTestStores(t)
			ctx := context.Background()
			service := "radarr"
			if m == Series {
				service = "sonarr"
			}
			if err := store.Upsert(ctx, service, "http://app.local", "key"); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if err := store.Upsert(ctx, "ollama", "http://ollama.local:11434", ""); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if err := settingsStore.Set(ctx, AIModelKey, "qwen2.5:7b"); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if err := settingsStore.Set(ctx, AIFallbackEnabledKey, "true"); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			sess, err := Build(ctx, store, settingsStore, &http.Client{Timeout: time.Second}, nil, m)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if sess.MainstreamAI == nil {
				t.Fatal("expected a non-nil MainstreamAI with ollama+model configured")
			}
		})
	}
}

// TestBuild_AIClient_UsesConfiguredProvider proves buildAIClient actually
// dispatches to the right provider client for each of the four supported
// backends — not just that SOME client comes back non-nil, but that it's
// functional against that provider's real request/response shape.
func TestBuild_AIClient_UsesConfiguredProvider(t *testing.T) {
	cases := []struct {
		provider string
		fake     func(t *testing.T) *httptest.Server
	}{
		{AIProviderOllama, func(t *testing.T) *httptest.Server {
			return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				writeTestJSON(t, w, map[string]any{"message": map[string]any{"content": `{"title":"ok"}`}})
			}))
		}},
		{AIProviderOpenAI, func(t *testing.T) *httptest.Server {
			return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				writeTestJSON(t, w, map[string]any{"choices": []map[string]any{
					{"message": map[string]any{"content": `{"title":"ok"}`}},
				}})
			}))
		}},
		{AIProviderGemini, func(t *testing.T) *httptest.Server {
			return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				writeTestJSON(t, w, map[string]any{"candidates": []map[string]any{
					{"content": map[string]any{"parts": []map[string]any{{"text": `{"title":"ok"}`}}}},
				}})
			}))
		}},
		{AIProviderAnthropic, func(t *testing.T) *httptest.Server {
			return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				writeTestJSON(t, w, map[string]any{"content": []map[string]any{
					{"type": "text", "text": `{"title":"ok"}`},
				}})
			}))
		}},
	}

	for _, c := range cases {
		t.Run(c.provider, func(t *testing.T) {
			srv := c.fake(t)
			defer srv.Close()
			// openai/gemini/anthropic now target a hardcoded DefaultBaseURL,
			// not conn.URL — redirect the package var at the fake for cloud
			// providers (no-op for ollama, which still uses conn.URL).
			overrideAIProviderBaseURL(t, c.provider, srv.URL)

			store, settingsStore := newTestStores(t)
			ctx := context.Background()
			if err := store.Upsert(ctx, "radarr", "http://radarr.local:7878", "radarr-key"); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if err := store.Upsert(ctx, c.provider, srv.URL, "test-key"); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if err := settingsStore.Set(ctx, AIProviderKey, c.provider); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if err := settingsStore.Set(ctx, AIFallbackEnabledKey, "true"); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if err := settingsStore.Set(ctx, AIModelKey, "test-model"); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			sess, err := Build(ctx, store, settingsStore, &http.Client{Timeout: 5 * time.Second}, nil, Movies)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if sess.MainstreamAI == nil {
				t.Fatal("expected a non-nil MainstreamAI")
			}
			result, err := sess.MainstreamAI.ChatJSON(ctx, "prompt")
			if err != nil {
				t.Fatalf("expected the %s client to actually work against its fake, got: %v", c.provider, err)
			}
			if result["title"] != "ok" {
				t.Errorf("got %+v", result)
			}
		})
	}
}

// TestBuild_AIClient_CloudProvidersIgnoreStoredConnectionURL proves
// buildAIClient targets each cloud provider's DefaultBaseURL regardless of
// Connection.URL: the stored connection points at a bogus, unreachable host,
// yet ChatJSON still succeeds by reaching the fake the package var was
// redirected to. Acceptance criterion from the ai-connection-settings-simplify
// plan: "verified by a test that stores a differing URL and asserts the
// client still targets the constant."
func TestBuild_AIClient_CloudProvidersIgnoreStoredConnectionURL(t *testing.T) {
	cases := []struct {
		provider string
		fake     func(t *testing.T) *httptest.Server
	}{
		{AIProviderOpenAI, func(t *testing.T) *httptest.Server {
			return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				writeTestJSON(t, w, map[string]any{"choices": []map[string]any{
					{"message": map[string]any{"content": `{"title":"ok"}`}},
				}})
			}))
		}},
		{AIProviderGemini, func(t *testing.T) *httptest.Server {
			return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				writeTestJSON(t, w, map[string]any{"candidates": []map[string]any{
					{"content": map[string]any{"parts": []map[string]any{{"text": `{"title":"ok"}`}}}},
				}})
			}))
		}},
		{AIProviderAnthropic, func(t *testing.T) *httptest.Server {
			return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				writeTestJSON(t, w, map[string]any{"content": []map[string]any{
					{"type": "text", "text": `{"title":"ok"}`},
				}})
			}))
		}},
	}

	for _, c := range cases {
		t.Run(c.provider, func(t *testing.T) {
			srv := c.fake(t)
			defer srv.Close()
			overrideAIProviderBaseURL(t, c.provider, srv.URL)

			store, settingsStore := newTestStores(t)
			ctx := context.Background()
			if err := store.Upsert(ctx, "radarr", "http://radarr.local:7878", "radarr-key"); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			// Deliberately bogus stored URL — must be ignored entirely.
			if err := store.Upsert(ctx, c.provider, "http://wrong.invalid/nope", "test-key"); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if err := settingsStore.Set(ctx, AIProviderKey, c.provider); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if err := settingsStore.Set(ctx, AIFallbackEnabledKey, "true"); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if err := settingsStore.Set(ctx, AIModelKey, "test-model"); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			sess, err := Build(ctx, store, settingsStore, &http.Client{Timeout: 5 * time.Second}, nil, Movies)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if sess.MainstreamAI == nil {
				t.Fatal("expected a non-nil MainstreamAI")
			}
			result, err := sess.MainstreamAI.ChatJSON(ctx, "prompt")
			if err != nil {
				t.Fatalf("expected the %s client to reach the fixed URL (bogus stored URL ignored), got: %v", c.provider, err)
			}
			if result["title"] != "ok" {
				t.Errorf("got %+v", result)
			}
		})
	}
}

// TestBuild_Brave_IgnoresStoredConnectionURL is the buildIdentifier
// counterpart for Brave: a bogus stored "brave" connection URL is ignored in
// favor of bravesearch.DefaultBaseURL.
func TestBuild_Brave_IgnoresStoredConnectionURL(t *testing.T) {
	var hit bool
	braveSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = true
		writeTestJSON(t, w, map[string]any{"web": map[string]any{"results": []map[string]any{}}})
	}))
	defer braveSrv.Close()
	overrideAIProviderBaseURL(t, "brave", braveSrv.URL)

	store, settingsStore := newTestStores(t)
	ctx := context.Background()
	if err := store.Upsert(ctx, "whisparr", "http://whisparr.local:6969", "whisparr-key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Deliberately bogus stored URL — must be ignored entirely.
	if err := store.Upsert(ctx, "brave", "http://wrong.invalid/nope", "brave-key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sess, err := Build(ctx, store, settingsStore, &http.Client{Timeout: 5 * time.Second}, nil, Adult)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sess.Identify == nil || sess.Identify.Brave == nil {
		t.Fatal("expected a non-nil Identify.Brave with a brave connection configured")
	}

	if _, err := sess.Identify.Brave.Search(ctx, "test", 1); err != nil {
		t.Fatalf("expected Brave to reach the fixed URL (bogus stored URL ignored), got: %v", err)
	}
	if !hit {
		t.Error("expected the fixed-URL fake to be hit, not the stored Connection.URL")
	}
}

// TestBuild_AIClient_UnknownProviderErrors confirms an explicitly-set but
// unrecognized provider is a real configuration error, not silently
// tolerated — the user asked for something specific that can't be honored.
func TestBuild_AIClient_UnknownProviderErrors(t *testing.T) {
	store, settingsStore := newTestStores(t)
	ctx := context.Background()
	if err := store.Upsert(ctx, "radarr", "http://radarr.local:7878", "radarr-key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := settingsStore.Set(ctx, AIFallbackEnabledKey, "true"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := settingsStore.Set(ctx, AIProviderKey, "chatgpt"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := settingsStore.Set(ctx, AIModelKey, "test-model"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// The connection lookup for an unknown provider name would also miss, but
	// the provider-name validation must fire before that, with a clear message.
	if err := store.Upsert(ctx, "chatgpt", "http://example.invalid", "k"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	_, err := Build(ctx, store, settingsStore, &http.Client{Timeout: time.Second}, nil, Movies)
	if err == nil {
		t.Fatal("expected an error for an unrecognized ai_provider value")
	}
	if !strings.Contains(err.Error(), "chatgpt") {
		t.Errorf("expected the error to name the bad provider value, got: %v", err)
	}
}

// TestBuild_AIClient_ProviderStoreError_Propagates is a regression guard for
// the exact bug class this project has hit before (see buildIdentifier's own
// history): a real settings-store failure on the ai_provider lookup must
// propagate, not be silently swallowed as "just use the default provider"
// because the zero-value string also happens to equal "".
func TestBuild_AIClient_ProviderStoreError_Propagates(t *testing.T) {
	sqlDB, err := db.Open(filepath.Join(t.TempDir(), "sakms.db"))
	if err != nil {
		t.Fatalf("opening db: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	secretStore, err := secrets.New(make([]byte, 32))
	if err != nil {
		t.Fatalf("building secret store: %v", err)
	}
	store := connections.New(sqlDB, secretStore)
	settingsStore := settings.New(sqlDB)
	ctx := context.Background()

	if err := store.Upsert(ctx, "radarr", "http://radarr.local:7878", "radarr-key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, err := sqlDB.Exec(`DROP TABLE settings`); err != nil {
		t.Fatalf("dropping settings table: %v", err)
	}

	_, err = Build(ctx, store, settingsStore, &http.Client{Timeout: time.Second}, nil, Movies)
	if err == nil {
		t.Fatal("expected a real settings-store error to propagate, got nil")
	}
	if strings.Contains(err.Error(), "not configured") {
		t.Errorf("a real store error must not be reported as merely unconfigured, got: %v", err)
	}
}

// TestBuild_KidsRootPath_DefaultsEmpty confirms the feature is off by
// default for Movies/Series when nothing has been configured.
func TestBuild_KidsRootPath_DefaultsEmpty(t *testing.T) {
	for _, m := range []Mode{Movies, Series} {
		t.Run(string(m), func(t *testing.T) {
			store, settingsStore := newTestStores(t)
			ctx := context.Background()
			service := "radarr"
			if m == Series {
				service = "sonarr"
			}
			if err := store.Upsert(ctx, service, "http://app.local", "key"); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			sess, err := Build(ctx, store, settingsStore, &http.Client{Timeout: time.Second}, nil, m)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if sess.KidsRootPath != "" {
				t.Errorf("expected KidsRootPath to default empty, got %q", sess.KidsRootPath)
			}
		})
	}
}

// TestBuild_KidsRootPath_ReadsPerModeSetting proves Movies and Series each
// read their OWN key, not a shared one.
func TestBuild_KidsRootPath_ReadsPerModeSetting(t *testing.T) {
	store, settingsStore := newTestStores(t)
	ctx := context.Background()
	if err := store.Upsert(ctx, "radarr", "http://radarr.local", "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := store.Upsert(ctx, "sonarr", "http://sonarr.local", "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := settingsStore.Set(ctx, "movies_kids_root_path", "/media/Movies (Kids)"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := settingsStore.Set(ctx, "series_kids_root_path", "/media/Series (Kids)"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	moviesSess, err := Build(ctx, store, settingsStore, &http.Client{Timeout: time.Second}, nil, Movies)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if moviesSess.KidsRootPath != "/media/Movies (Kids)" {
		t.Errorf("got %q", moviesSess.KidsRootPath)
	}

	seriesSess, err := Build(ctx, store, settingsStore, &http.Client{Timeout: time.Second}, nil, Series)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if seriesSess.KidsRootPath != "/media/Series (Kids)" {
		t.Errorf("got %q", seriesSess.KidsRootPath)
	}
}

// TestBuild_KidsRootPath_NotApplicableToAdult confirms Adult never resolves
// a KidsRootPath — the concept doesn't apply there.
func TestBuild_KidsRootPath_NotApplicableToAdult(t *testing.T) {
	store, settingsStore := newTestStores(t)
	ctx := context.Background()
	if err := store.Upsert(ctx, "whisparr", "http://whisparr.local:6969", "whisparr-key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sess, err := Build(ctx, store, settingsStore, &http.Client{Timeout: time.Second}, nil, Adult)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sess.KidsRootPath != "" {
		t.Errorf("expected KidsRootPath to stay empty for Adult, got %q", sess.KidsRootPath)
	}
}

// TestBuild_DownloadPipeline_NilWhenUnconfigured confirms all four of
// Prowlarr/QBittorrent/NZBGet/TMDB stay nil when none of their connections
// are set up — search/grab/discover simply aren't possible yet, not an error.
func TestBuild_DownloadPipeline_NilWhenUnconfigured(t *testing.T) {
	store, settingsStore := newTestStores(t)
	ctx := context.Background()
	if err := store.Upsert(ctx, "radarr", "http://radarr.local:7878", "radarr-key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sess, err := Build(ctx, store, settingsStore, &http.Client{Timeout: time.Second}, nil, Movies)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sess.Prowlarr != nil || sess.QBittorrent != nil || sess.NZBGet != nil || sess.TMDB != nil {
		t.Errorf("expected all four search-pipeline clients to be nil, got Prowlarr=%v QBittorrent=%v NZBGet=%v TMDB=%v",
			sess.Prowlarr, sess.QBittorrent, sess.NZBGet, sess.TMDB)
	}
}

// TestBuild_SearchPipeline_PopulatedWhenConfigured confirms Prowlarr is
// populated when configured, and — post unified-downloader — that
// QBittorrent/NZBGet stay nil even when a qbittorrent connection exists,
// because Build no longer constructs them (the aria2c Downloader replaced
// them; the fields are retained only as generic capability). The injected
// Downloader pointer is passed straight through.
func TestBuild_SearchPipeline_PopulatedWhenConfigured(t *testing.T) {
	store, settingsStore := newTestStores(t)
	ctx := context.Background()
	if err := store.Upsert(ctx, "prowlarr", "http://prowlarr.local:9696", "prowlarr-key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := store.UpsertWithUsername(ctx, "qbittorrent", "http://qbt.local:8080", "wade", "hunter2"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sess, err := Build(ctx, store, settingsStore, &http.Client{Timeout: time.Second}, nil, Movies)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sess.Prowlarr == nil {
		t.Error("expected Prowlarr to be populated")
	}
	if sess.QBittorrent != nil {
		t.Error("expected QBittorrent to stay nil — Build no longer constructs it (aria2 replaced it)")
	}
	if sess.NZBGet != nil {
		t.Error("expected NZBGet to stay nil — Build no longer constructs it (aria2 replaced it)")
	}
	if sess.TMDB != nil {
		t.Error("expected TMDB to stay nil — not configured in this test")
	}
}

// TestBuild_TMDB_PopulatedWhenConfigured confirms TMDB is populated
// independently of the download-client connections — Discover works even
// before an indexer/download client is set up.
func TestBuild_TMDB_PopulatedWhenConfigured(t *testing.T) {
	store, settingsStore := newTestStores(t)
	ctx := context.Background()
	if err := store.Upsert(ctx, "radarr", "http://radarr.local:7878", "radarr-key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := store.Upsert(ctx, "tmdb", "https://api.themoviedb.org/3", "tmdb-key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sess, err := Build(ctx, store, settingsStore, &http.Client{Timeout: time.Second}, nil, Movies)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sess.TMDB == nil {
		t.Error("expected TMDB to be populated")
	}
	if sess.Prowlarr != nil || sess.QBittorrent != nil || sess.NZBGet != nil {
		t.Error("expected the download-side clients to stay nil — not configured in this test")
	}
}

// TestBuildDownloadPipeline_StoreError_Propagates guards against
// buildSearchPipeline collapsing a real connections-store failure into the
// same "not configured" outcome as an absent connection — a real error must
// propagate, not be silently swallowed as all-nil. Calls buildSearchPipeline
// directly (this test file is part of package mode) rather than through
// Build, since Build's earlier primary-service lookup shares the same
// connections table and would fail first, masking whether this function's
// own error handling is what's actually being exercised.
func TestBuildDownloadPipeline_StoreError_Propagates(t *testing.T) {
	sqlDB, err := db.Open(filepath.Join(t.TempDir(), "sakms.db"))
	if err != nil {
		t.Fatalf("opening db: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	secretStore, err := secrets.New(make([]byte, 32))
	if err != nil {
		t.Fatalf("building secret store: %v", err)
	}
	store := connections.New(sqlDB, secretStore)
	ctx := context.Background()

	if _, err := sqlDB.Exec(`DROP TABLE connections`); err != nil {
		t.Fatalf("dropping connections table: %v", err)
	}

	err = buildSearchPipeline(ctx, store, &http.Client{Timeout: time.Second}, &Session{})
	if err == nil {
		t.Fatal("expected a real connections-store error to propagate, got nil")
	}
}

func writeTestJSON(t *testing.T, w http.ResponseWriter, v any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Errorf("encoding test response: %v", err)
	}
}

// --- Session.NotifyPlayers (Slice 2 of player-rescan-notify) ---

// TestNotifyPlayers_JellyfinPOSTShape confirms a session with only
// sess.Jellyfin set sends exactly one POST to /Library/Media/Updated with
// the MediaBrowser auth header and the two updates translated verbatim
// (Acceptance #1).
func TestNotifyPlayers_JellyfinPOSTShape(t *testing.T) {
	var calls int
	var gotPath, gotAuth string
	var gotBody struct {
		Updates []jellyfin.MediaUpdate `json:"Updates"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	sess := &Session{Jellyfin: jellyfin.New(jellyfin.Config{URL: srv.URL, APIKey: "jf-key"}, &http.Client{Timeout: time.Second})}
	sess.NotifyPlayers(context.Background(), []PathChange{
		{Path: "/media/old.mkv", Kind: Deleted},
		{Path: "/media/new.mkv", Kind: Created},
	})

	if calls != 1 {
		t.Fatalf("expected exactly 1 POST, got %d", calls)
	}
	if gotPath != "/Library/Media/Updated" {
		t.Errorf("expected path /Library/Media/Updated, got %q", gotPath)
	}
	if gotAuth != `MediaBrowser Token="jf-key"` {
		t.Errorf("expected MediaBrowser auth header, got %q", gotAuth)
	}
	want := []jellyfin.MediaUpdate{
		{Path: "/media/old.mkv", UpdateType: "Deleted"},
		{Path: "/media/new.mkv", UpdateType: "Created"},
	}
	if len(gotBody.Updates) != len(want) || gotBody.Updates[0] != want[0] || gotBody.Updates[1] != want[1] {
		t.Errorf("expected updates %+v, got %+v", want, gotBody.Updates)
	}
}

// stashRecorder is a fake local-Stash GraphQL server that records which
// mutation (metadataScan vs metadataClean) each request invoked, along with
// its decoded input — used to prove NotifyPlayers routes Deleted paths to
// CleanMetadata and Created/Modified paths to the phash-free RescanPaths,
// never crossed.
type stashRecorder struct {
	scanCalls  []map[string]any
	cleanCalls []map[string]any
}

func newStashRecorderClient(t *testing.T, rec *stashRecorder) *stashapi.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query     string `json:"query"`
			Variables struct {
				Input map[string]any `json:"input"`
			} `json:"variables"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(req.Query, "metadataClean"):
			rec.cleanCalls = append(rec.cleanCalls, req.Variables.Input)
			_, _ = w.Write([]byte(`{"data":{"metadataClean":"clean-job"}}`))
		case strings.Contains(req.Query, "metadataScan"):
			rec.scanCalls = append(rec.scanCalls, req.Variables.Input)
			_, _ = w.Write([]byte(`{"data":{"metadataScan":"scan-job"}}`))
		default:
			t.Fatalf("unexpected stash mutation query: %s", req.Query)
		}
	}))
	t.Cleanup(srv.Close)
	return stashapi.New(stashapi.Config{URL: srv.URL, APIKey: "stash-key"}, &http.Client{Timeout: time.Second})
}

// TestNotifyPlayers_StashSplit_PurgeShapedBatchProducesCleanOnly is the single
// most important correctness guardrail in the whole feature (Recommendation
// #1 / Guardrail #4): a purge-shaped (Deleted-only) batch must produce ONLY
// a metadataClean call, and NEVER a metadataScan call.
func TestNotifyPlayers_StashSplit_PurgeShapedBatchProducesCleanOnly(t *testing.T) {
	rec := &stashRecorder{}
	sess := &Session{Stash: newStashRecorderClient(t, rec)}

	sess.NotifyPlayers(context.Background(), []PathChange{
		{Path: "/media/gone1.mkv", Kind: Deleted},
		{Path: "/media/gone2.mkv", Kind: Deleted},
	})

	if len(rec.scanCalls) != 0 {
		t.Fatalf("expected NO metadataScan calls for a purge-shaped batch, got %d: %+v", len(rec.scanCalls), rec.scanCalls)
	}
	if len(rec.cleanCalls) != 1 {
		t.Fatalf("expected exactly 1 metadataClean call, got %d", len(rec.cleanCalls))
	}
	gotPaths, _ := rec.cleanCalls[0]["paths"].([]any)
	if len(gotPaths) != 2 || gotPaths[0] != "/media/gone1.mkv" || gotPaths[1] != "/media/gone2.mkv" {
		t.Errorf("expected clean paths [gone1,gone2], got %+v", rec.cleanCalls[0]["paths"])
	}
	if rec.cleanCalls[0]["dryRun"] != false {
		t.Errorf("expected dryRun=false, got %v", rec.cleanCalls[0]["dryRun"])
	}
}

// TestNotifyPlayers_StashSplit_RenameShapedBatchScansNewAndCleansOld confirms
// a rename-shaped batch (one Created, one Deleted) produces a phash-free
// RescanPaths on the new path AND a CleanMetadata on the old path
// (Acceptance #3), asserting scanGeneratePhashes:false is exactly what
// proves the call went through RescanPaths and not ScanPaths.
func TestNotifyPlayers_StashSplit_RenameShapedBatchScansNewAndCleansOld(t *testing.T) {
	rec := &stashRecorder{}
	sess := &Session{Stash: newStashRecorderClient(t, rec)}

	sess.NotifyPlayers(context.Background(), []PathChange{
		{Path: "/media/old.mkv", Kind: Deleted},
		{Path: "/media/new.mkv", Kind: Created},
	})

	if len(rec.scanCalls) != 1 {
		t.Fatalf("expected exactly 1 metadataScan call, got %d", len(rec.scanCalls))
	}
	if len(rec.cleanCalls) != 1 {
		t.Fatalf("expected exactly 1 metadataClean call, got %d", len(rec.cleanCalls))
	}
	scanPaths, _ := rec.scanCalls[0]["paths"].([]any)
	if len(scanPaths) != 1 || scanPaths[0] != "/media/new.mkv" {
		t.Errorf("expected scan of [new.mkv], got %+v", rec.scanCalls[0]["paths"])
	}
	if rec.scanCalls[0]["scanGeneratePhashes"] != false {
		t.Errorf("expected phash-free scan (scanGeneratePhashes=false, proving RescanPaths not ScanPaths was used), got %v", rec.scanCalls[0]["scanGeneratePhashes"])
	}
	if rec.scanCalls[0]["rescan"] != false {
		t.Errorf("expected rescan=false, got %v", rec.scanCalls[0]["rescan"])
	}
	cleanPaths, _ := rec.cleanCalls[0]["paths"].([]any)
	if len(cleanPaths) != 1 || cleanPaths[0] != "/media/old.mkv" {
		t.Errorf("expected clean of [old.mkv], got %+v", rec.cleanCalls[0]["paths"])
	}
}

// TestNotifyPlayers_BothClientsNil_NoOp confirms a session with neither
// Jellyfin nor Stash configured is a safe no-op — no panic, no outbound
// calls possible since neither client exists (Acceptance #4, Edge #6).
func TestNotifyPlayers_BothClientsNil_NoOp(t *testing.T) {
	sess := &Session{}
	sess.NotifyPlayers(context.Background(), []PathChange{{Path: "/media/f.mkv", Kind: Created}})
}

// TestNotifyPlayers_EmptyChanges_NoOutboundCalls confirms an empty changes
// slice short-circuits before touching either client — fake servers here
// fail the test if they ever receive a request, proving the early return
// fires before any client is called.
func TestNotifyPlayers_EmptyChanges_NoOutboundCalls(t *testing.T) {
	jfSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("jellyfin should not be called for an empty changes slice")
	}))
	defer jfSrv.Close()
	stashSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("stash should not be called for an empty changes slice")
	}))
	defer stashSrv.Close()

	sess := &Session{
		Jellyfin: jellyfin.New(jellyfin.Config{URL: jfSrv.URL, APIKey: "k"}, &http.Client{Timeout: time.Second}),
		Stash:    stashapi.New(stashapi.Config{URL: stashSrv.URL, APIKey: "k"}, &http.Client{Timeout: time.Second}),
	}
	sess.NotifyPlayers(context.Background(), nil)
	sess.NotifyPlayers(context.Background(), []PathChange{})
}

// TestNotifyPlayers_ExactPath_NeverRootFolderPath confirms NotifyPlayers
// forwards only the exact file paths it was given — the fake server never
// sees a RootFolderPath-shaped key anywhere in the request (Guardrail #3,
// Acceptance #7).
func TestNotifyPlayers_ExactPath_NeverRootFolderPath(t *testing.T) {
	var rawBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		rawBody = string(body)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	sess := &Session{Jellyfin: jellyfin.New(jellyfin.Config{URL: srv.URL, APIKey: "k"}, &http.Client{Timeout: time.Second})}
	sess.NotifyPlayers(context.Background(), []PathChange{{Path: "/media/Movies/Some Movie (2020)/movie.mkv", Kind: Created}})

	if strings.Contains(rawBody, "RootFolderPath") {
		t.Errorf("expected the request to never mention RootFolderPath, got body: %s", rawBody)
	}
	if !strings.Contains(rawBody, "/media/Movies/Some Movie (2020)/movie.mkv") {
		t.Errorf("expected the exact file path in the request body, got: %s", rawBody)
	}
}

// TestNotifyPlayers_BestEffort_JellyfinFailureLogsAndReturns confirms a
// downstream player error never surfaces to the caller — NotifyPlayers
// still returns normally (void) when the fake player answers 500
// (Acceptance #5, Guardrail #1).
func TestNotifyPlayers_BestEffort_JellyfinFailureLogsAndReturns(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	sess := &Session{Jellyfin: jellyfin.New(jellyfin.Config{URL: srv.URL, APIKey: "k"}, &http.Client{Timeout: time.Second})}
	// Reaching the end of this call without panicking/blocking IS the
	// assertion: NotifyPlayers never returns an error and never propagates
	// the player's failure to the caller.
	sess.NotifyPlayers(context.Background(), []PathChange{{Path: "/media/f.mkv", Kind: Created}})
}

// TestNotifyPlayers_BestEffort_StashScanFailureStillCleans is the meaty
// best-effort test: a rename-shaped batch (Created+Deleted) where Stash's
// metadataScan fails must NOT skip metadataClean — the two arms are
// independent `if` blocks, so a scan failure on the new path never causes
// the old path's DB row to go un-cleaned (the phantom-scene bug the plan
// warns about).
func TestNotifyPlayers_BestEffort_StashScanFailureStillCleans(t *testing.T) {
	var cleanCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query string `json:"query"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		switch {
		case strings.Contains(req.Query, "metadataScan"):
			http.Error(w, "boom", http.StatusInternalServerError)
		case strings.Contains(req.Query, "metadataClean"):
			cleanCalls++
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":{"metadataClean":"clean-job"}}`))
		default:
			t.Fatalf("unexpected stash mutation query: %s", req.Query)
		}
	}))
	defer srv.Close()

	sess := &Session{Stash: stashapi.New(stashapi.Config{URL: srv.URL, APIKey: "k"}, &http.Client{Timeout: time.Second})}
	sess.NotifyPlayers(context.Background(), []PathChange{
		{Path: "/media/old.mkv", Kind: Deleted},
		{Path: "/media/new.mkv", Kind: Created},
	})

	if cleanCalls != 1 {
		t.Fatalf("expected metadataClean to still fire even though metadataScan failed, got %d clean calls", cleanCalls)
	}
}

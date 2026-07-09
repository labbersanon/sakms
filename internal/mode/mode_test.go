package mode

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/curtiswtaylorjr/tidyarr/internal/connections"
	"github.com/curtiswtaylorjr/tidyarr/internal/db"
	"github.com/curtiswtaylorjr/tidyarr/internal/secrets"
	"github.com/curtiswtaylorjr/tidyarr/internal/servarr"
	"github.com/curtiswtaylorjr/tidyarr/internal/settings"
)

// newTestStores opens one fresh db and returns a connections store and a
// settings store backed by it, so a test can configure both the connections
// and the Ollama-model setting that Build reads.
func newTestStores(t *testing.T) (*connections.Store, *settings.Store) {
	t.Helper()
	sqlDB, err := db.Open(filepath.Join(t.TempDir(), "tidyarr.db"))
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

func TestBuild_MoviesUsesRadarrConnection(t *testing.T) {
	store, settingsStore := newTestStores(t)
	ctx := context.Background()
	if err := store.Upsert(ctx, "radarr", "http://radarr.local:7878", "radarr-key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sess, err := Build(ctx, store, settingsStore, &http.Client{Timeout: time.Second}, Movies)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sess.Mode != Movies {
		t.Errorf("expected Mode to be Movies, got %v", sess.Mode)
	}
	if sess.Servarr.AppType() != servarr.Radarr {
		t.Errorf("expected the Radarr app type, got %v", sess.Servarr.AppType())
	}
	if sess.Identify != nil {
		t.Error("expected Identify to be nil for a Movies session")
	}
}

func TestBuild_SeriesUsesSonarrConnection(t *testing.T) {
	store, settingsStore := newTestStores(t)
	ctx := context.Background()
	if err := store.Upsert(ctx, "sonarr", "http://sonarr.local:8989", "sonarr-key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sess, err := Build(ctx, store, settingsStore, &http.Client{Timeout: time.Second}, Series)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sess.Servarr.AppType() != servarr.Sonarr {
		t.Errorf("expected the Sonarr app type, got %v", sess.Servarr.AppType())
	}
	if sess.Identify != nil {
		t.Error("expected Identify to be nil for a Series session")
	}
}

func TestBuild_MissingConnection(t *testing.T) {
	store, settingsStore := newTestStores(t)
	if _, err := Build(context.Background(), store, settingsStore, &http.Client{}, Movies); err == nil {
		t.Fatal("expected an error when radarr isn't configured yet")
	}
}

func TestBuild_AdultUsesWhisparrConnection(t *testing.T) {
	store, settingsStore := newTestStores(t)
	ctx := context.Background()
	if err := store.Upsert(ctx, "whisparr", "http://whisparr.local:6969", "whisparr-key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sess, err := Build(ctx, store, settingsStore, &http.Client{Timeout: time.Second}, Adult)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sess.Mode != Adult {
		t.Errorf("expected Mode to be Adult, got %v", sess.Mode)
	}
	if sess.Servarr.AppType() != servarr.Whisparr {
		t.Errorf("expected the Whisparr app type, got %v", sess.Servarr.AppType())
	}
}

func TestBuild_AdultMissingConnection(t *testing.T) {
	store, settingsStore := newTestStores(t)
	_, err := Build(context.Background(), store, settingsStore, &http.Client{}, Adult)
	if err == nil {
		t.Fatal("expected an error when whisparr isn't configured yet")
	}
	if !strings.Contains(err.Error(), "isn't configured yet") {
		t.Errorf("expected the 'not configured yet' error, got: %v", err)
	}
	if strings.Contains(err.Error(), "wired up") {
		t.Errorf("stale 'wired up' error still returned: %v", err)
	}
}

func TestBuild_UnknownMode(t *testing.T) {
	store, settingsStore := newTestStores(t)
	_, err := Build(context.Background(), store, settingsStore, &http.Client{}, Mode("bogus"))
	if err == nil {
		t.Fatal("expected an error for an unknown mode")
	}
	if errors.Is(err, connections.ErrNotFound) {
		t.Error("an unknown mode should fail before ever touching the connections store")
	}
}

// TestBuild_AdultOnlyWhisparr_IdentifyNil confirms the Tag-preservation
// constraint: an Adult session with only whisparr configured builds
// successfully with a nil Identify (no identification backbone), so Tag —
// which never reads Identify — is unaffected.
func TestBuild_AdultOnlyWhisparr_IdentifyNil(t *testing.T) {
	store, settingsStore := newTestStores(t)
	ctx := context.Background()
	if err := store.Upsert(ctx, "whisparr", "http://whisparr.local:6969", "whisparr-key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sess, err := Build(ctx, store, settingsStore, &http.Client{Timeout: time.Second}, Adult)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sess.Identify != nil {
		t.Error("expected Identify to be nil when only whisparr is configured")
	}
}

// TestBuild_AdultSettingsStoreError_Propagates guards against buildIdentifier
// collapsing a REAL settings-store failure into the same "not configured, no
// error" outcome as an unset model. A real error must propagate (per
// buildIdentifier's own doc comment), not be silently swallowed as nil,nil —
// swallowing it would look identical to "identification not configured" from
// the caller's side, hiding an actual outage behind a misleading success.
func TestBuild_AdultSettingsStoreError_Propagates(t *testing.T) {
	sqlDB, err := db.Open(filepath.Join(t.TempDir(), "tidyarr.db"))
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

	_, err = Build(ctx, store, settingsStore, &http.Client{Timeout: time.Second}, Adult)
	if err == nil {
		t.Fatal("expected a real settings-store error to propagate, got nil")
	}
	if strings.Contains(err.Error(), "not configured") {
		t.Errorf("a real store error must not be reported as merely unconfigured, got: %v", err)
	}
}

// TestBuild_AdultOllamaConnButNoModelSetting_IdentifyNil pins the §2
// anti-pattern guard: an Ollama connection with NO model setting must leave
// Identify nil (no guessed model) and must not panic.
func TestBuild_AdultOllamaConnButNoModelSetting_IdentifyNil(t *testing.T) {
	store, settingsStore := newTestStores(t)
	ctx := context.Background()
	if err := store.Upsert(ctx, "whisparr", "http://whisparr.local:6969", "whisparr-key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := store.Upsert(ctx, "ollama", "http://ollama.local:11434", ""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sess, err := Build(ctx, store, settingsStore, &http.Client{Timeout: time.Second}, Adult)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sess.Identify != nil {
		t.Error("expected Identify to be nil when the Ollama model setting is unset (no guessed fallback)")
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

	sess, err := Build(ctx, store, settingsStore, &http.Client{Timeout: time.Second}, Adult)
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

	sess, err := Build(ctx, store, settingsStore, &http.Client{Timeout: 5 * time.Second}, Adult)
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

	sess, err := Build(ctx, store, settingsStore, &http.Client{Timeout: time.Second}, Movies)
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

	sess, err := Build(ctx, store, settingsStore, &http.Client{Timeout: time.Second}, Movies)
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

			sess, err := Build(ctx, store, settingsStore, &http.Client{Timeout: time.Second}, m)
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
			if err := settingsStore.Set(ctx, AIModelKey, "test-model"); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			sess, err := Build(ctx, store, settingsStore, &http.Client{Timeout: 5 * time.Second}, Movies)
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

// TestBuild_AIClient_UnknownProviderErrors confirms an explicitly-set but
// unrecognized provider is a real configuration error, not silently
// tolerated — the user asked for something specific that can't be honored.
func TestBuild_AIClient_UnknownProviderErrors(t *testing.T) {
	store, settingsStore := newTestStores(t)
	ctx := context.Background()
	if err := store.Upsert(ctx, "radarr", "http://radarr.local:7878", "radarr-key"); err != nil {
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

	_, err := Build(ctx, store, settingsStore, &http.Client{Timeout: time.Second}, Movies)
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
	sqlDB, err := db.Open(filepath.Join(t.TempDir(), "tidyarr.db"))
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

	_, err = Build(ctx, store, settingsStore, &http.Client{Timeout: time.Second}, Movies)
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

			sess, err := Build(ctx, store, settingsStore, &http.Client{Timeout: time.Second}, m)
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

	moviesSess, err := Build(ctx, store, settingsStore, &http.Client{Timeout: time.Second}, Movies)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if moviesSess.KidsRootPath != "/media/Movies (Kids)" {
		t.Errorf("got %q", moviesSess.KidsRootPath)
	}

	seriesSess, err := Build(ctx, store, settingsStore, &http.Client{Timeout: time.Second}, Series)
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

	sess, err := Build(ctx, store, settingsStore, &http.Client{Timeout: time.Second}, Adult)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sess.KidsRootPath != "" {
		t.Errorf("expected KidsRootPath to stay empty for Adult, got %q", sess.KidsRootPath)
	}
}

func writeTestJSON(t *testing.T, w http.ResponseWriter, v any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Errorf("encoding test response: %v", err)
	}
}

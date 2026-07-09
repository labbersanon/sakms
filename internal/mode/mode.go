// Package mode builds the live client(s) for one of Tidyarr's three
// isolated modes — Movies, Series, or Adult — from whatever connection is
// currently configured in Settings. A Session is cheap to build (an HTTP
// client wrapper, nothing cached), so it's constructed fresh per request:
// a connection edited in Settings takes effect on the very next Scan/Apply,
// no restart required.
package mode

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/curtiswtaylorjr/tidyarr/internal/anthropic"
	"github.com/curtiswtaylorjr/tidyarr/internal/bravesearch"
	"github.com/curtiswtaylorjr/tidyarr/internal/connections"
	"github.com/curtiswtaylorjr/tidyarr/internal/gemini"
	"github.com/curtiswtaylorjr/tidyarr/internal/identify"
	"github.com/curtiswtaylorjr/tidyarr/internal/ollama"
	"github.com/curtiswtaylorjr/tidyarr/internal/openai"
	"github.com/curtiswtaylorjr/tidyarr/internal/servarr"
	"github.com/curtiswtaylorjr/tidyarr/internal/settings"
	"github.com/curtiswtaylorjr/tidyarr/internal/stashbox"
	"github.com/curtiswtaylorjr/tidyarr/internal/throttle"
	"github.com/curtiswtaylorjr/tidyarr/internal/tpdbrest"
)

// Mode is one of Tidyarr's three isolated library contexts. Never blended —
// see the design spec's "Mode replaces checkboxes" section for why.
type Mode string

const (
	Movies Mode = "movies"
	Series Mode = "series"
	Adult  Mode = "adult"
)

// AIProviderKey is the settings key holding which AI backend every AI-
// assisted feature talks to — Adult identification AND Movies/Series
// Rename's AI title-guess fallback share this ONE choice, since Tidyarr
// never asks which mode's provider to configure. One of the AIProvider*
// constants; empty/unset defaults to AIProviderOllama, preserving every
// existing install's behavior without requiring a migration.
const AIProviderKey = "ai_provider"

// AIProvider identifies which service backs AIClient (see buildAIClient).
const (
	AIProviderOllama    = "ollama"
	AIProviderOpenAI    = "openai"
	AIProviderGemini    = "gemini"
	AIProviderAnthropic = "anthropic"
)

// AIModelKey is the settings key holding the model name to request from
// whichever AIProviderKey backend is configured (e.g. "qwen2.5vl:7b" for
// Ollama, "gpt-4o-mini" for OpenAI, "gemini-2.5-flash" for Gemini,
// "claude-haiku-4-5" for Anthropic) — Adult identification (filename parsing
// + web-grounding, internal/identify's ParseFilename/ExtractFromSearch) AND
// Movies/Series Rename's AI title-guess fallback (internal/identify's
// GuessTitle) share this ONE setting. Stored in settings (not a connections
// column) because it's a non-secret scalar with no schema of its own.
// Empty/unset means "AI features not configured": Build leaves
// sess.Identify/sess.MainstreamAI nil rather than guessing a model. Exported
// so internal/api can read/write the same key without duplicating the
// string literal.
//
// Whatever model is configured here MUST return its response as a parseable
// JSON object matching the calling prompt's schema — every prompt in
// internal/identify asks for a specific JSON shape and parses the response
// accordingly (Ollama/OpenAI/Gemini enforce this via a structured-output
// mode; Anthropic's client relies on the prompt's own explicit "respond with
// ONLY valid JSON" instruction, since Claude's Messages API has no separate
// JSON-mode toggle). Swapping providers or models works as long as the
// response satisfies that shape; nothing here is tuned to one specific
// model's quirks.
const AIModelKey = "ai_model"

// adultThrottleInterval is the per-host minimum call spacing for the Adult
// identification pipeline's external services — technical call-spacing
// (politeness to StashDB/FansDB/TPDB/Brave), not a user-facing setting, so a
// constant is correct.
const adultThrottleInterval = 1 * time.Second

// TPDBGraphQLURL is TPDB's stash-box-protocol-compatible GraphQL endpoint,
// used ONLY for give-back (fingerprint/draft submission) — a completely
// different host from the REST search API at the "tpdb" connection's URL.
// Hardcoded rather than a connection field: TPDB is a single fixed public
// service, not a self-hostable app like Whisparr, so there's nothing for a
// user to point it at. The same API key configured for the "tpdb" connection
// authenticates here too (as a Bearer token). A var (not const) so tests can
// override it to point at a fake server.
var TPDBGraphQLURL = "https://theporndb.net/graphql"

// KidsRootPathKey returns the settings key holding m's paired Kids root
// folder path — Rename's destination for content classified kids-appropriate
// (see internal/classify), instead of the general root it was found under.
// ok is false for Adult, which has no kids/general split concept. An
// explicit path the user picks from the mode's own real root folders (see
// internal/api's kids-root-path handler), not an automatic naming-convention
// guess like the original CLI's "(Kids)"-suffix pairing — blank/unset means
// the feature is off for that mode.
func (m Mode) KidsRootPathKey() (key string, ok bool) {
	switch m {
	case Movies:
		return "movies_kids_root_path", true
	case Series:
		return "series_kids_root_path", true
	default:
		return "", false
	}
}

// service reports which connections.Store key and servarr.App back this
// mode's primary client.
func (m Mode) service() (service string, app servarr.App, err error) {
	switch m {
	case Movies:
		return "radarr", servarr.Radarr, nil
	case Series:
		return "sonarr", servarr.Sonarr, nil
	case Adult:
		// Adult's primary client is Whisparr V3 (a Radarr fork — see
		// internal/servarr), hard-required for every Adult workflow. The
		// identification pipeline (StashDB/FansDB/TPDB/Ollama, internal/identify)
		// is built separately and tolerantly — see buildIdentifier.
		return "whisparr", servarr.Whisparr, nil
	default:
		return "", 0, fmt.Errorf("mode %q: unknown mode", m)
	}
}

// Session holds the live client(s) for one mode.
type Session struct {
	Mode    Mode
	Servarr *servarr.Client

	// Identify is the AI-assisted content-identification pipeline, populated
	// ONLY for Adult mode and ONLY when its backbone (a connection for the
	// configured AIProviderKey backend AND the AIModelKey setting) is
	// configured; nil otherwise — including for every Movies/Series session.
	// Consumers must nil-check before use.
	Identify *identify.Identifier

	// MainstreamAI is Movies/Series Rename's AI title-guess fallback client —
	// populated for every mode (cheap, harmless if unused) from the SAME
	// AIProviderKey/AIModelKey settings Identify's AI client uses (one shared
	// provider+model, not a per-mode choice); nil otherwise. Adult mode
	// doesn't use this field (its own Identify covers all of its AI needs)
	// but nothing stops it from being populated too. Consumers must nil-check
	// before use.
	MainstreamAI identify.AIClient

	// KidsRootPath is m's paired Kids root folder path (see
	// KidsRootPathKey), or "" if unset/not applicable to m (Adult, or a
	// Movies/Series install that hasn't configured one) — Rename simply
	// skips kids classification/routing in that case.
	KidsRootPath string
}

// Build constructs a Session for m using the connection currently configured
// in store. Returns an error if m isn't supported yet, or if its service has
// no connection configured (Settings hasn't been filled in for it yet).
func Build(ctx context.Context, store *connections.Store, settingsStore *settings.Store, httpClient *http.Client, m Mode) (*Session, error) {
	service, app, err := m.service()
	if err != nil {
		return nil, err
	}
	conn, err := store.Get(ctx, service)
	if err != nil {
		if errors.Is(err, connections.ErrNotFound) {
			return nil, fmt.Errorf("mode %q: %s isn't configured yet — add it in Settings first", m, service)
		}
		return nil, fmt.Errorf("mode %q: loading %s connection: %w", m, service, err)
	}
	client := servarr.New(servarr.Config{BaseURL: conn.URL, APIKey: conn.APIKey, App: app}, httpClient)

	sess := &Session{Mode: m, Servarr: client}
	aiClient, err := buildAIClient(ctx, store, settingsStore, httpClient)
	if err != nil {
		return nil, fmt.Errorf("mode %q: building AI client: %w", m, err)
	}
	sess.MainstreamAI = aiClient
	if key, ok := m.KidsRootPathKey(); ok {
		path, err := settingsStore.Get(ctx, key)
		if err != nil && !errors.Is(err, settings.ErrNotFound) {
			return nil, fmt.Errorf("mode %q: loading kids root path: %w", m, err)
		}
		sess.KidsRootPath = path
	}
	if m == Adult {
		id, err := buildIdentifier(ctx, store, settingsStore, httpClient, aiClient)
		if err != nil {
			return nil, fmt.Errorf("mode %q: building identifier: %w", m, err)
		}
		sess.Identify = id
	}
	return sess, nil
}

// buildAIClient assembles the one AI client every AI-assisted feature shares
// (Adult identification's backbone AND Movies/Series Rename's title-guess
// fallback) from AIProviderKey/AIModelKey. Tolerant by design: without a
// connection for the configured provider AND the model setting, returns
// (nil, nil) rather than guessing — callers treat a nil client as "AI
// features not configured" and simply skip them. A real store error
// (anything other than "not configured") propagates. An explicitly-set but
// unrecognized provider value is a real configuration error, not silently
// tolerated — the user asked for something specific and it can't be honored.
func buildAIClient(ctx context.Context, store *connections.Store, settingsStore *settings.Store, httpClient *http.Client) (identify.AIClient, error) {
	provider, err := settingsStore.Get(ctx, AIProviderKey)
	if err != nil && !errors.Is(err, settings.ErrNotFound) {
		return nil, err // a real store error must propagate, not look like "unset"
	}
	if provider == "" {
		provider = AIProviderOllama // default: preserves every existing install's behavior
	}

	model, err := settingsStore.Get(ctx, AIModelKey)
	if errors.Is(err, settings.ErrNotFound) {
		return nil, nil // no model → do NOT guess one
	}
	if err != nil {
		return nil, err
	}
	if model == "" {
		return nil, nil // stored but blank → same as unconfigured
	}

	conn, err := optionalConn(ctx, store, provider)
	if err != nil {
		return nil, err
	}
	if conn == nil {
		return nil, nil // provider chosen but its connection isn't set up yet
	}

	switch provider {
	case AIProviderOllama:
		return ollama.New(conn.URL, model, httpClient), nil
	case AIProviderOpenAI:
		return openai.New(conn.URL, conn.APIKey, model, httpClient), nil
	case AIProviderGemini:
		return gemini.New(conn.URL, conn.APIKey, model, httpClient), nil
	case AIProviderAnthropic:
		return anthropic.New(conn.URL, conn.APIKey, model, httpClient), nil
	default:
		return nil, fmt.Errorf("%s %q: expected one of %s, %s, %s, %s",
			AIProviderKey, provider, AIProviderOllama, AIProviderOpenAI, AIProviderGemini, AIProviderAnthropic)
	}
}

// buildIdentifier assembles the Adult identification pipeline around aiClient
// (already resolved by buildAIClient — nil means AI features aren't
// configured, so there is no identifier at all: ParseFilename would nil-panic
// on a missing AI client). Every other client (stashdb/fansdb/tpdb/brave) is
// optional: a missing connection yields a nil client, which BoxSearcher and
// Identify already treat as "not configured" rather than erroring. A real
// store error (anything other than "not configured") propagates.
func buildIdentifier(ctx context.Context, store *connections.Store, settingsStore *settings.Store, httpClient *http.Client, aiClient identify.AIClient) (*identify.Identifier, error) {
	if aiClient == nil {
		return nil, nil
	}

	boxes := map[string]*stashbox.Client{}
	giveBackBoxes := map[string]*stashbox.Client{}
	for _, name := range []string{"stashdb", "fansdb"} {
		conn, err := optionalConn(ctx, store, name)
		if err != nil {
			return nil, err
		}
		if conn != nil {
			client := stashbox.New(stashbox.Config{
				Endpoint: conn.URL, APIKey: conn.APIKey, IsBearer: false, HasVoteField: true,
			}, httpClient)
			boxes[name] = client
			giveBackBoxes[name] = client
		}
	}

	var tpdb *tpdbrest.Client
	if conn, err := optionalConn(ctx, store, "tpdb"); err != nil {
		return nil, err
	} else if conn != nil {
		tpdb = tpdbrest.New(conn.URL, conn.APIKey, httpClient)
		// TPDB's GraphQL endpoint (give-back only) is a different host from its
		// REST search API, but shares the same API key — see TPDBGraphQLURL.
		giveBackBoxes["tpdb"] = stashbox.New(stashbox.Config{
			Endpoint: TPDBGraphQLURL, APIKey: conn.APIKey, IsBearer: true, HasVoteField: false,
		}, httpClient)
	}

	var brave *bravesearch.Client
	if conn, err := optionalConn(ctx, store, "brave"); err != nil {
		return nil, err
	} else if conn != nil {
		brave = bravesearch.New(conn.URL, conn.APIKey, httpClient)
	}

	return &identify.Identifier{
		Boxes:    identify.NewBoxSearcher(boxes, tpdb),
		AI:       aiClient,
		Brave:    brave,
		Throttle: throttle.New(adultThrottleInterval),
		GiveBack: identify.NewGiveBack(giveBackBoxes),
	}, nil
}

// optionalConn returns the connection for service, or (nil, nil) if it simply
// isn't configured — collapsing connections.ErrNotFound into "absent" so
// callers can treat optional services uniformly. Any other error propagates.
func optionalConn(ctx context.Context, store *connections.Store, service string) (*connections.Connection, error) {
	conn, err := store.Get(ctx, service)
	if errors.Is(err, connections.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return conn, nil
}

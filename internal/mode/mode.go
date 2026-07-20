// Package mode builds the live client(s) for one of SAK's three
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
	"log"
	"net/http"
	"time"

	"github.com/labbersanon/sakms/internal/anthropic"
	"github.com/labbersanon/sakms/internal/bravesearch"
	"github.com/labbersanon/sakms/internal/connections"
	"github.com/labbersanon/sakms/internal/downloader"
	"github.com/labbersanon/sakms/internal/gemini"
	"github.com/labbersanon/sakms/internal/identify"
	"github.com/labbersanon/sakms/internal/jellyfin"
	"github.com/labbersanon/sakms/internal/nzbget"
	"github.com/labbersanon/sakms/internal/ollama"
	"github.com/labbersanon/sakms/internal/openai"
	"github.com/labbersanon/sakms/internal/prowlarr"
	"github.com/labbersanon/sakms/internal/qbittorrent"
	"github.com/labbersanon/sakms/internal/servarr"
	"github.com/labbersanon/sakms/internal/settings"
	"github.com/labbersanon/sakms/internal/stashapi"
	"github.com/labbersanon/sakms/internal/stashbox"
	"github.com/labbersanon/sakms/internal/throttle"
	"github.com/labbersanon/sakms/internal/tmdb"
	"github.com/labbersanon/sakms/internal/tpdbrest"
	"github.com/labbersanon/sakms/internal/tvdb"
)

// Mode is one of SAK's three isolated library contexts. Never blended —
// see the design spec's "Mode replaces checkboxes" section for why.
type Mode string

const (
	Movies Mode = "movies"
	Series Mode = "series"
	Adult  Mode = "adult"
)

// AIProviderKey is the settings key holding which AI backend every AI-
// assisted feature talks to — Adult identification AND Movies/Series
// Rename's AI title-guess fallback share this ONE choice, since SAK
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

// AIFallbackEnabledKey is the settings key for the opt-in BYOAI fallback
// toggle. When unset or not "true", buildAIClient returns nil immediately —
// the DB-first parser runs alone and ParseFilename is never called.
const AIFallbackEnabledKey = "ai_fallback_enabled"

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

// ChangeKind classifies a PathChange (see PathChange).
type ChangeKind int

const (
	Created ChangeKind = iota
	Modified
	Deleted
)

// PathChange is one file-level change a workflow's Apply committed to disk
// — the exact path SAK just created, modified, or deleted, destined for
// Session.NotifyPlayers. Always an exact file path, never a root/library
// folder (see NotifyPlayers).
type PathChange struct {
	Path string
	Kind ChangeKind
}

// Session holds the live client(s) for one mode.
type Session struct {
	Mode Mode
	// Servarr is nil for every mode now — SAK owns its own library across all
	// three (Movies/Series since their Radarr/Sonarr eliminations, Adult since
	// Stage 4's Whisparr elimination), so Build never constructs one, and no
	// live code path (connection testing included — TestConnection has no
	// radarr/sonarr/whisparr case) reads this field. The field (and
	// internal/servarr itself) is retained anyway as generic, still-valid
	// capability per this project's "don't strip a shared library's capability
	// just because its last caller moved on" convention. Callers must not
	// assume this is non-nil.
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

	// Prowlarr backs the native indexer search. Like MainstreamAI, it's
	// global — one Prowlarr per install, not per-mode — populated the same
	// way regardless of which mode is building, and tolerantly nil if it
	// isn't configured. Consumers must nil-check before use, same as Identify.
	Prowlarr *prowlarr.Client

	// QBittorrent and NZBGet remain as generic, still-valid capability (same
	// precedent as internal/servarr keeping Radarr/Sonarr/Whisparr after
	// their eliminations) — but Build no longer constructs them: the unified
	// downloader (Downloader below) owns the actual download now, so these
	// stay nil for every session. Kept because callers must not assume they
	// disappeared, and a future usenet backend could reuse NZBGet.
	QBittorrent *qbittorrent.Client
	NZBGet      *nzbget.Client

	// Downloader is the process-lifetime unified download engine
	// (anacrolix/torrent in-process BitTorrent — internal/downloader),
	// injected as the same singleton into every session, not built per-request
	// like the tolerant clients above. nil only when the engine failed to start
	// at boot; consumers must nil-check before use.
	Downloader *downloader.Manager

	// TMDB backs the Discover browse view — same "global, tolerant" rule as
	// Prowlarr/QBittorrent/NZBGet above.
	TMDB *tmdb.Client

	// TVDB is TheTVDB's v4 client, used as a search fallback in Movies/Series
	// Rename when TMDB search returns no results or a below-threshold
	// confidence match. Nil when no "tvdb" connection is configured — callers
	// must nil-check before use. Adult Rename uses its own phash-first
	// identification pipeline, not TVDB, so this field is unused for Adult.
	TVDB *tvdb.Client

	// Stash is the LOCAL Stash instance's own GraphQL client (internal/stashapi
	// — distinct from the stash-box protocol used for StashDB/FansDB/TPDB),
	// populated ONLY for Adult mode and ONLY when a "stash" connection is
	// configured; nil otherwise. Used for phash-first identification (Stash
	// computes/holds the phash; SAK never computes one itself). Purely
	// additive: Rename's Adult Scan falls back to the AI/text pipeline
	// unchanged when this is nil. Consumers must nil-check before use.
	Stash *stashapi.Client

	// Jellyfin is a Jellyfin instance's targeted media-refresh client
	// (internal/jellyfin), populated ONLY for Movies/Series mode and ONLY
	// when a "jellyfin" connection is configured; nil otherwise — including
	// for every Adult session, which notifies Stash instead (see Stash
	// above). Used by NotifyPlayers to poke Jellyfin's library index after a
	// file op. Purely additive: nothing in Movies/Series' existing
	// workflows requires this to be non-nil. Consumers must nil-check
	// before use.
	Jellyfin *jellyfin.Client
}

// Build constructs a Session for m from whatever connections are currently
// configured in store. Returns an error only if m isn't one of the three
// known modes (the sole hard requirement) — no mode requires a *arr
// connection anymore.
//
// SAK owns its own library for all three modes now (internal/library) instead
// of proxying Radarr/Sonarr/Whisparr, so sess.Servarr stays nil for every
// mode. What Build still wires per-mode is the tolerant, optional clientry:
// Adult's identification pipeline (sess.Identify) and local Stash notify
// client (sess.Stash), Movies/Series' Jellyfin notify client (sess.Jellyfin),
// and the shared search/download pipeline — each left nil when unconfigured.
func Build(ctx context.Context, store *connections.Store, settingsStore *settings.Store, httpClient *http.Client, dl *downloader.Manager, m Mode) (*Session, error) {
	sess := &Session{Mode: m, Downloader: dl}

	// Every mode owns its own library now — Movies/Series since their Radarr/
	// Sonarr eliminations, Adult since Stage 4's Whisparr elimination — so no
	// mode constructs a Servarr client anymore; sess.Servarr stays nil for all
	// three. This switch is now purely the unknown-mode validator that
	// Mode.service() used to be: Build is the sole chokepoint, since handlers
	// pass mode.Mode(r.PathValue("mode")) through unvalidated.
	switch m {
	case Movies, Series, Adult:
		// SAK-owned library; no *arr construction.
	default:
		return nil, fmt.Errorf("mode %q: unknown mode", m)
	}

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

		stash, err := buildStashClient(ctx, store, httpClient)
		if err != nil {
			return nil, fmt.Errorf("mode %q: building stash client: %w", m, err)
		}
		sess.Stash = stash
	}

	if m != Adult {
		jf, err := buildJellyfinClient(ctx, store, httpClient)
		if err != nil {
			return nil, fmt.Errorf("mode %q: building jellyfin client: %w", m, err)
		}
		sess.Jellyfin = jf
	}

	if err := buildSearchPipeline(ctx, store, httpClient, sess); err != nil {
		return nil, fmt.Errorf("mode %q: building download pipeline: %w", m, err)
	}
	return sess, nil
}

// buildSearchPipeline populates sess.Prowlarr/QBittorrent/NZBGet/TMDB from
// whatever of those four connections exist — every one tolerant, since a
// usenet-only or torrent-only install won't have all three download-side
// connections, and TMDB's Discover browse view is a separate concern again
// (usable even before any indexer/download client is set up; search itself
// can still list results even before any download client is set up — grab
// just isn't possible yet). A real store error still propagates.
func buildSearchPipeline(ctx context.Context, store *connections.Store, httpClient *http.Client, sess *Session) error {
	if conn, err := optionalConn(ctx, store, "prowlarr"); err != nil {
		return err
	} else if conn != nil {
		sess.Prowlarr = prowlarr.New(prowlarr.Config{BaseURL: conn.URL, APIKey: conn.APIKey}, httpClient)
	}

	// qBittorrent/NZBGet are no longer constructed here — the unified aria2c
	// downloader (sess.Downloader, injected above) owns the actual download
	// now. The clients (and their packages) are retained as generic
	// capability but have no live caller, exactly the internal/servarr
	// precedent.

	if conn, err := optionalConn(ctx, store, "tmdb"); err != nil {
		return err
	} else if conn != nil {
		// TMDB is a fixed public service — its base URL is the hardcoded
		// tmdb.DefaultBaseURL, never conn.URL (which is not collected for it).
		sess.TMDB = tmdb.New(tmdb.Config{BaseURL: tmdb.DefaultBaseURL, APIKey: conn.APIKey}, httpClient)
	}

	if conn, err := optionalConn(ctx, store, "tvdb"); err != nil {
		return err
	} else if conn != nil {
		// TVDB is a fixed public service — same reasoning as TMDB above.
		sess.TVDB = tvdb.New(tvdb.Config{BaseURL: tvdb.DefaultBaseURL, APIKey: conn.APIKey}, httpClient)
	}

	return nil
}

// buildStashClient wires the local Stash instance's own GraphQL client from
// the "stash" connection key — already recognized and testable via
// internal/api/connections.go's testStash, just never read back into a live
// session before now. Tolerant: nil, nil if unconfigured, same as every
// other optional client in this file.
func buildStashClient(ctx context.Context, store *connections.Store, httpClient *http.Client) (*stashapi.Client, error) {
	conn, err := optionalConn(ctx, store, "stash")
	if err != nil {
		return nil, err
	}
	if conn == nil {
		return nil, nil
	}
	return stashapi.New(stashapi.Config{URL: conn.URL, APIKey: conn.APIKey}, httpClient), nil
}

// buildJellyfinClient wires the Jellyfin targeted-rescan client from the
// "jellyfin" connection. Tolerant: nil, nil if unconfigured. Built only for
// Movies/Series — Adult notifies Stash instead (see buildStashClient), a
// hardcoded per-mode scoping (CLAUDE.md Mission / the player-rescan-notify
// design note), not a user-facing toggle.
func buildJellyfinClient(ctx context.Context, store *connections.Store, httpClient *http.Client) (*jellyfin.Client, error) {
	conn, err := optionalConn(ctx, store, "jellyfin")
	if err != nil {
		return nil, err
	}
	if conn == nil {
		return nil, nil
	}
	return jellyfin.New(jellyfin.Config{URL: conn.URL, APIKey: conn.APIKey}, httpClient), nil
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
	// AI fallback is opt-in; return nil unless the operator explicitly enabled it.
	fallbackEnabled, err := settingsStore.Get(ctx, AIFallbackEnabledKey)
	if err != nil && !errors.Is(err, settings.ErrNotFound) {
		return nil, err
	}
	if fallbackEnabled != "true" {
		return nil, nil
	}

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
		// OpenAI's base URL is fixed and public — hardcoded, never conn.URL.
		return openai.New(openai.DefaultBaseURL, conn.APIKey, model, httpClient), nil
	case AIProviderGemini:
		// Gemini's base URL is fixed and public — hardcoded, never conn.URL.
		return gemini.New(gemini.DefaultBaseURL, conn.APIKey, model, httpClient), nil
	case AIProviderAnthropic:
		// Anthropic's base URL is fixed and public — hardcoded, never conn.URL.
		return anthropic.New(anthropic.DefaultBaseURL, conn.APIKey, model, httpClient), nil
	default:
		return nil, fmt.Errorf("%s %q: expected one of %s, %s, %s, %s",
			AIProviderKey, provider, AIProviderOllama, AIProviderOpenAI, AIProviderGemini, AIProviderAnthropic)
	}
}

// buildIdentifier assembles the Adult identification pipeline. The entity
// store (DB-first parsing) and the AI client (BYOAI fallback) are both
// optional — at least one must be non-nil for IdentifyDetailed to do useful
// work, but we always return a non-nil Identifier so callers can inject
// EntityStore after Build() without re-entering this constructor. AI features
// (ParseFilename BYOAI path) are available when aiClient is non-nil; the
// DB-first path (ParseFilenameDB) is available when EntityStore is non-nil
// (injected by api handlers that need it). Original rationale for the old
// nil guard:
// on a missing AI client). Every other client (stashdb/fansdb/tpdb/brave) is
// optional: a missing connection yields a nil client, which BoxSearcher and
// Identify already treat as "not configured" rather than erroring. A real
// store error (anything other than "not configured") propagates.
func buildIdentifier(ctx context.Context, store *connections.Store, settingsStore *settings.Store, httpClient *http.Client, aiClient identify.AIClient) (*identify.Identifier, error) {
	boxes := map[string]*stashbox.Client{}
	giveBackBoxes := map[string]*stashbox.Client{}
	for _, name := range []string{"stashdb", "fansdb"} {
		conn, err := optionalConn(ctx, store, name)
		if err != nil {
			return nil, err
		}
		if conn != nil {
			// StashDB/FansDB are fixed public stash-box instances — the endpoint
			// is the hardcoded per-name constant, never conn.URL (not collected).
			endpoint, _ := stashbox.URLForBox(name)
			client := stashbox.New(stashbox.Config{
				Endpoint: endpoint, APIKey: conn.APIKey, IsBearer: false, HasVoteField: true,
			}, httpClient)
			boxes[name] = client
			giveBackBoxes[name] = client
		}
	}

	var tpdb *tpdbrest.Client
	if conn, err := optionalConn(ctx, store, "tpdb"); err != nil {
		return nil, err
	} else if conn != nil {
		// TPDB's REST base is fixed and public — hardcoded, never conn.URL.
		tpdb = tpdbrest.New(tpdbrest.DefaultBaseURL, conn.APIKey, httpClient)
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
		// Brave's search endpoint is fixed and public — hardcoded, never conn.URL.
		brave = bravesearch.New(bravesearch.DefaultBaseURL, conn.APIKey, httpClient)
	}

	return &identify.Identifier{
		Boxes:    identify.NewBoxSearcher(boxes, tpdb),
		AI:       aiClient,
		Brave:    brave,
		Throttle: throttle.New(adultThrottleInterval),
		GiveBack: identify.NewGiveBack(giveBackBoxes),
	}, nil
}

// playerNotifyTimeout bounds how long NotifyPlayers waits on its downstream
// player POSTs — a hard ceiling on how much latency notify can add to an
// Apply request, not a correctness knob.
const playerNotifyTimeout = 8 * time.Second

// NotifyPlayers tells s's configured downstream player (exactly one of
// s.Jellyfin/s.Stash is ever non-nil for a given mode — see Build's
// hardcoded per-mode scoping) that changes just committed to disk. NEVER
// returns an error: a player being unreachable must not fail SAK's own
// Apply, which has already fully committed by the time this is called —
// every failure path here is log-only, best-effort.
//
// Deletes route to Stash's CleanMetadata (never ScanPaths/RescanPaths) and
// Created/Modified route to the phash-free RescanPaths — this split is the
// single most important correctness guardrail in the whole feature: mixing
// them up would make a purge look like a scan to Stash.
//
// Uses context.WithoutCancel so a committed change still gets notified even
// if the HTTP request that triggered the Apply disconnects before
// NotifyPlayers finishes — this is inside the best-effort envelope (updating
// a player's own index, not one of SAK's records), so decoupling from
// request cancellation is cheap insurance, not a correctness requirement.
//
// HONESTY NOTE (house "unverified assumptions" convention): a move is sent
// as RescanPaths(newPath) followed by CleanMetadata(oldPath) — this is
// modeled from Stash's own CleanMetadata doc, not confirmed against a live
// Stash instance, so it is not verified that this ordering never produces a
// transient duplicate scene between the two calls. A live-Stash check is a
// reasonable follow-up but is NOT a gate for this feature (see
// internal/jellyfin's parallel honesty note for the Jellyfin side of the
// same convention).
func (s *Session) NotifyPlayers(ctx context.Context, changes []PathChange) {
	if len(changes) == 0 {
		return
	}
	nctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), playerNotifyTimeout)
	defer cancel()

	if s.Jellyfin != nil {
		if err := s.Jellyfin.NotifyMediaUpdated(nctx, toJellyfinUpdates(changes)); err != nil {
			log.Printf("jellyfin rescan notify (best-effort) failed: %v", err)
		}
	}
	if s.Stash != nil {
		scan := pathsWhere(changes, Created, Modified)
		clean := pathsWhere(changes, Deleted)
		if len(scan) > 0 {
			if _, err := s.Stash.RescanPaths(nctx, scan); err != nil { // phash-free, no WaitJob
				log.Printf("stash rescan notify (best-effort) failed: %v", err)
			}
		}
		if len(clean) > 0 {
			if _, err := s.Stash.CleanMetadata(nctx, clean, false); err != nil { // dryRun=false, no WaitJob
				log.Printf("stash clean notify (best-effort) failed: %v", err)
			}
		}
	}
}

// toJellyfinUpdates maps PathChanges to Jellyfin's MediaUpdate request shape.
func toJellyfinUpdates(changes []PathChange) []jellyfin.MediaUpdate {
	updates := make([]jellyfin.MediaUpdate, len(changes))
	for i, c := range changes {
		updates[i] = jellyfin.MediaUpdate{Path: c.Path, UpdateType: changeKindString(c.Kind)}
	}
	return updates
}

// changeKindString maps a ChangeKind to Jellyfin's UpdateType string.
func changeKindString(k ChangeKind) string {
	switch k {
	case Created:
		return "Created"
	case Deleted:
		return "Deleted"
	default:
		return "Modified"
	}
}

// pathsWhere returns the Path of every change whose Kind is one of kinds.
func pathsWhere(changes []PathChange, kinds ...ChangeKind) []string {
	var paths []string
	for _, c := range changes {
		for _, k := range kinds {
			if c.Kind == k {
				paths = append(paths, c.Path)
				break
			}
		}
	}
	return paths
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

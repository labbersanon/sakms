// Command sakms runs the SAK server.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/labbersanon/sakms/internal/adultnewest"
	"github.com/labbersanon/sakms/internal/allowlist"
	"github.com/labbersanon/sakms/internal/api"
	"github.com/labbersanon/sakms/internal/auth"
	"github.com/labbersanon/sakms/internal/config"
	"github.com/labbersanon/sakms/internal/connections"
	"github.com/labbersanon/sakms/internal/db"
	"github.com/labbersanon/sakms/internal/dedupscan"
	"github.com/labbersanon/sakms/internal/discoverrefresh"
	"github.com/labbersanon/sakms/internal/discoversliders"
	"github.com/labbersanon/sakms/internal/downloader"
	"github.com/labbersanon/sakms/internal/excludes"
	"github.com/labbersanon/sakms/internal/grabs"
	"github.com/labbersanon/sakms/internal/imageproxy"
	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mediainfo"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/nodekeys"
	"github.com/labbersanon/sakms/internal/nodes"
	"github.com/labbersanon/sakms/internal/nodesettings"
	"github.com/labbersanon/sakms/internal/parseentity"
	"github.com/labbersanon/sakms/internal/phash"
	"github.com/labbersanon/sakms/internal/proposals"
	"github.com/labbersanon/sakms/internal/organizeevents"
	"github.com/labbersanon/sakms/internal/pruning"
	"github.com/labbersanon/sakms/internal/recheck"
	"github.com/labbersanon/sakms/internal/rssfeeds"
	"github.com/labbersanon/sakms/internal/scanschedule"
	"github.com/labbersanon/sakms/internal/secrets"
	"github.com/labbersanon/sakms/internal/sectionlock"
	"github.com/labbersanon/sakms/internal/serviceconn"
	"github.com/labbersanon/sakms/internal/settings"
	"github.com/labbersanon/sakms/internal/trakt"
	"github.com/labbersanon/sakms/internal/usenet"
	"github.com/labbersanon/sakms/internal/videophash"
	"github.com/labbersanon/sakms/internal/web"
	"github.com/labbersanon/sakms/internal/webhooks"
)

// sectionLockDisabledByEnv reports whether SAKMS_SECTION_LOCK_DISABLE asks
// for the section PIN lock's full disarm (see §2's threat model: restarting
// SAK with an env var set already requires host access, which is well past
// the household-member attacker this feature defends against). It is the
// documented — and only — way out of a corrupt stored PIN hash.
//
// # It is parsed UNLIKE every other env var in this program, deliberately
//
// There is no boolean env-var convention here to follow: config.FromEnv's
// four variables are all string-valued, and the one boolean-ish flag in the
// codebase (internal/tmdb/cache.go's SAKMS_TMDB_CACHE_DEBUG) is a bare
// `!= ""`. That convention is fine for a debug flag and wrong for this: it
// would make SAKMS_SECTION_LOCK_DISABLE=0 and =false DISARM the lock, which
// is the exact opposite of what an operator setting either of them means.
// Every doc, plan and runbook names this variable as =1; only =1 disarms.
func sectionLockDisabledByEnv() bool {
	return os.Getenv("SAKMS_SECTION_LOCK_DISABLE") == "1"
}

// outboundTimeout bounds every call SAK makes to a configured service
// (Radarr/Sonarr/Ollama/Stash/...) — a Test Connection click against a dead
// URL should fail in seconds, not hang the request indefinitely.
const outboundTimeout = 15 * time.Second

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	cfg := config.FromEnv()

	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		return err
	}
	dsn, err := cfg.ResolveDatabaseURL()
	if err != nil {
		return err
	}
	sqlDB, err := db.Open(context.Background(), dsn, db.PoolOptions{
		MaxOpen: cfg.DBMaxOpenConns,
		MaxIdle: cfg.DBMaxIdleConns,
	})
	if err != nil {
		return err
	}
	defer sqlDB.Close()

	secretKey, err := secrets.LoadOrCreateKey(filepath.Join(cfg.DataDir, "secret.key"))
	if err != nil {
		return err
	}
	secretStore, err := secrets.New(secretKey)
	if err != nil {
		return err
	}
	connStore := connections.New(sqlDB, secretStore)
	propStore := proposals.New(sqlDB)
	organizeEventsStore := organizeevents.New(sqlDB)
	organizeevents.SetDefault(organizeEventsStore)
	allowStore := allowlist.New(sqlDB)
	prober := mediainfo.New()
	hasher := phash.New()
	// videoHasher is SAK's StashDB-compatible video perceptual hasher for Adult
	// Rename's phash-first identification — a SEPARATE algorithm from `hasher`
	// (internal/phash, Movies/Series Dedup); the two are not interchangeable.
	videoHasher := videophash.New()
	nodeReg := nodes.New()
	pairingReg := nodes.NewPairingRegistry()
	nodeKeyStore := nodekeys.New(sqlDB)
	nodeSettingsStore := nodesettings.New(sqlDB)
	phashDispatcher := nodes.NewDispatcher(nodeReg, nodes.JobTypePhash, hasher, 3*time.Minute)
	videoDispatcher := nodes.NewDispatcher(nodeReg, nodes.JobTypeVideoPhash, videoHasher, 3*time.Minute)
	settingsStore := settings.New(sqlDB)
	// One-shot boot step (same shape as the API-key/ollama blocks below,
	// context.Background() for the same reason): reset any per-mode Dedup phash
	// threshold stored on a stale bit scale (a pre-PDQ 64-bit value) to its PDQ
	// default, logging one operator-visible notice per affected mode. Non-fatal.
	api.SweepStalePHashThresholds(context.Background(), settingsStore)
	// secretStore encrypts each grab's download URL at rest — an indexer/NZB
	// URL commonly embeds an API key, and the retry path is what needs the URL
	// persisted at all (see migration 0054).
	grabsStore := grabs.New(sqlDB, secretStore)
	libStore := library.New(sqlDB)
	slidersStore := discoversliders.New(sqlDB)
	// Excluded titles back the Requests "remove" feature (see api.NewRequestsMux).
	// A dependency NewMux doesn't carry, so — like recheck's watchStore — it's
	// threaded into its own mux mounted on `top` below, not through NewMux.
	excludesStore := excludes.New(sqlDB)

	// Unified downloader (internal/downloader): an anacrolix/torrent in-process
	// BitTorrent engine. Constructed ONCE here as a process-lifetime singleton
	// (it owns a torrent client + a poll goroutine — never per-request like
	// mode.Session's cheap clients) and injected as the same pointer into every
	// mode.Build call that needs it.
	dlManager, err := buildDownloader(context.Background(), cfg.DataDir, settingsStore, &http.Client{Timeout: outboundTimeout})
	if err != nil {
		log.Printf("downloader: not starting (%v) — torrent grabbing will be unavailable until fixed", err)
		dlManager = nil
	}
	// rssFeedsStore backs admin-defined raw RSS 2.0 feed rows (NZBGeek
	// saved-search style) — a per-row feed URL fetched and parsed server-side
	// at resolve time, a separate concept from slidersStore (TMDB-backed). The
	// feed URL is encrypted at rest (secretStore) — a feed URL commonly embeds
	// an indexer API key — via the trakt.Store pattern.
	rssFeedsStore := rssfeeds.NewStore(sqlDB, secretStore)
	// Real production migration: encrypt any pre-existing plaintext feed_url
	// rows. This MUST complete before the feed poller starts AND before the HTTP
	// server accepts requests, so neither reads/writes a half-migrated row; the
	// Store's plaintext read-fallback keeps unreached rows working if it errors,
	// so it is logged-and-non-fatal (same one-shot boot-step shape as
	// SweepStalePHashThresholds above; context.Background() for the same reason —
	// the signal-driven ctx doesn't exist yet here).
	if err := rssFeedsStore.BackfillEncryption(context.Background()); err != nil {
		log.Printf("rss feeds: encrypting plaintext feed urls: %v (unreached rows keep working via the plaintext read-fallback; retried next boot)", err)
	}
	// serviceConnStore backs the shared multi-connection registry (Usenet
	// subscriptions + media players) that migration 0053 moved the singleton
	// nntp/jellyfin rows into; internal/connections keeps the ~7 services that
	// really are one-per-install.
	serviceConnStore := serviceconn.NewStore(sqlDB, secretStore)
	// The Go half of migration 0053's data move: SQLite cannot parse the legacy
	// `nntp://host:port` URL, so the migration copies it verbatim and this
	// normalizes it into host/port/tls. Idempotent, so it runs unconditionally
	// at every boot — same one-shot boot-step shape and non-fatal handling as
	// the rss-feeds backfill above.
	if err := serviceConnStore.BackfillUsenetURL(context.Background()); err != nil {
		log.Printf("service connections: normalizing legacy usenet urls: %v (retried next boot)", err)
	}
	// Usenet/NNTP downloader (internal/usenet): built from every usenet-kind row
	// in serviceConnStore, not a single connection — see buildUsenetManager.
	// Constructed unconditionally (even with zero subscriptions configured),
	// so nzbManager is never nil; must run after BackfillUsenetURL above, since
	// a freshly migrated legacy row has no host/port until that normalizes it.
	nzbManager, err := buildUsenetManager(context.Background(), cfg.DataDir, serviceConnStore, settingsStore, &http.Client{Timeout: outboundTimeout})
	if err != nil {
		// buildUsenetManager always returns a non-nil Manager (see its doc
		// comment) — an error here means a subscription/settings read failed,
		// so nzbManager boots with an empty pool set rather than being nilled
		// out. Do NOT set nzbManager = nil: callers (e.g. search.go's dispatch
		// path) now use Manager.HasSubscriptions() instead of a nil check, and
		// HasSubscriptions() on a nil receiver panics.
		log.Printf("usenet: starting with no subscriptions loaded (%v) — NZB grabbing unavailable until fixed", err)
	}
	// traktStore persists Trakt's single application connection + linked
	// account tokens (its own table, not connections.Store — see
	// internal/trakt's package doc for why); secretStore encrypts the same
	// way it does for connStore.
	traktStore := trakt.NewStore(sqlDB, secretStore)
	// discoverCache backs the Discover row-content read-through cache
	// (internal/discoverrefresh, plan discover-scheduled-refresh §3.8) — its
	// own table (migration 0058), constructed the same way every other
	// sqlDB-backed store here is. Injected into api.NewMux below (the read
	// path + the lifecycle hooks) and into discoverrefresh.Run further down
	// (the write path).
	discoverCache := discoverrefresh.NewStore(sqlDB)
	// Claude 2026-08-03: pruningStore added for propose-only pruning rules
	// (plan .omc/plans/autopilot-impl-pruning-rules.md §3.3).
	// Reason: ONE store instance feeds both the request path (api.NewMux — the
	// CRUD/preview routes and purgeScanHandler's propose phase) and the
	// scheduled path (newScanAdapter's ScanPurge), so a manual and a scheduled
	// Purge scan evaluate the identical rule set.
	// Troubleshooting: nothing here ever deletes — rules are read-only inputs
	// to Purge's propose phase, and every match still needs a human Apply.
	pruningStore := pruning.New(sqlDB)
	// watchStore backs the opt-in background recheck job (internal/recheck) —
	// its own table, shared with nothing else. Constructed here only so the one
	// start-call below can be handed it; nothing else in the program reads it.
	watchStore := recheck.NewWatchStore(sqlDB)
	// adultNewestRowStore/adultNewestReleaseStore back the opt-in Adult
	// "newest" Discover rows (internal/adultnewest) — a background Prowlarr
	// scan job (gated off by default, same convention as recheck above) that
	// caches matched releases for Discover to read; see the scan.Run
	// start-call below.
	adultNewestRowStore := adultnewest.New(sqlDB)
	adultNewestReleaseStore := adultnewest.NewReleaseStore(sqlDB)
	// feedHealth is the concurrency-safe per-feed liveness holder shared by the
	// adultnewest feed poller (sole writer) and the three Adult Discover read
	// handlers (readers) so a feed-sourced row's request-time availability is a
	// cheap in-memory health check plus the row's persisted last_confirmed_seen —
	// no live probe. Constructed once here; injected into both.
	feedHealth := adultnewest.NewFeedHealth()
	// imageProxy is built ONCE here as a process-lifetime singleton and injected
	// into api.NewMux (the request read path, imageProxyHandler). It is an
	// in-memory LRU over the live upstream fetch — a poster requested during one
	// grid render is not re-fetched from the same upstream host on the next.
	// Reuses the same outboundTimeout-bounded client every other external client
	// in this program uses.
	imageProxy := imageproxy.New(&http.Client{Timeout: outboundTimeout})
	// entityStore is the DB-first entity cache for Adult filename parsing. It
	// wraps the same sqlDB as every other store — no second connection needed.
	entityStore := parseentity.NewSQLiteStore(sqlDB)
	// webhookStore persists outbound webhook subscriptions — uses the same
	// secretStore as connStore/traktStore to encrypt signing secrets at rest.
	webhookStore := webhooks.New(sqlDB, secretStore)
	// secretStore doubles as authStore's OIDC-client-secret decryptor, and
	// the outbound HTTP client is the same outboundTimeout-bounded one every
	// other external client in this program uses — it bounds OIDC discovery,
	// token exchange, and JWKS fetch (oidc mode). Middleware's own signature
	// is untouched.
	authStore := auth.New(settingsStore, secretStore, &http.Client{Timeout: outboundTimeout})

	// Section PIN lock — Layer 1, installed into auth.Middleware below.
	//
	// Constructed exactly ONCE and shared. sectionlock.Store caches both of
	// its settings keys and invalidates only its own copy; sectionlock.Gate
	// owns the process-memory brute-force counter and the bcrypt memo. A
	// second Store would keep honouring a changed PIN until restart, and a
	// second Gate would split the failure counter so the effective lockout
	// threshold doubled — neither visible to anything that builds one gate.
	//
	// secretStore satisfies sectionlock.AADEncryptor via EncryptWithAAD/
	// DecryptWithAAD: the same AES-256-GCM primitive the session cookie uses,
	// with the unlock ticket's own AAD bound in so the two ciphertexts are not
	// interchangeable.
	//
	// SAKMS_SECTION_LOCK_DISABLE=1 is the full-disarm path from the threat
	// model, and the way it disarms is that sectionGate stays EMPTY: no call
	// site below installs a gate, and Middleware behaves exactly as it did
	// before the lock existed. The gate itself is still built, because the
	// control mux needs it to read and write configuration — clearing a
	// corrupt PIN is the whole point of the disarm.
	//
	// See sectionLockDisabledByEnv for how the variable is parsed, and why not
	// the way every other env var in this program is.
	sectionLockDisabled := sectionLockDisabledByEnv()
	sectionLockGate := sectionlock.NewGate(sectionlock.NewStore(settingsStore), secretStore)
	var sectionGate []auth.MiddlewareOption
	if sectionLockDisabled {
		log.Printf("section lock: DISARMED by SAKMS_SECTION_LOCK_DISABLE=1 — no section is enforced")
	} else {
		sectionGate = append(sectionGate, auth.WithSectionGate(sectionLockGate))
	}

	// dedupHub is the process-lifetime live-progress hub for background Dedup
	// scans (internal/dedupscan) — a broadcaster + per-mode in-flight tracker,
	// injected into NewMux like webhookStore/dlManager. Its shutdown-aware base
	// context is handed over post-construction via dedupHub.Start(ctx) below,
	// AFTER signal.NotifyContext exists (that ctx doesn't exist yet here — the
	// two-step construction the Hub's Start is designed for).
	dedupHub := dedupscan.New()

	// Boot-time API key resolution: SAKMS_API_KEY (if set) always wins over
	// whatever's persisted, and is never itself persisted (see
	// auth.Store.UseEnvAPIKey). Otherwise reuse a previously generated key,
	// or auto-generate one and log it exactly once — the only sanctioned
	// full-key log line anywhere in this codebase (see auth/apikey.go).
	// context.Background() is used here rather than the signal-driven ctx
	// below, which doesn't exist yet at this point in run() — this is a
	// one-shot boot step, not a long-lived operation that needs cancellation.
	if cfg.APIKey != "" {
		authStore.UseEnvAPIKey(cfg.APIKey)
		log.Printf("API key: using SAKMS_API_KEY from environment")
	} else if raw, err := authStore.EnsureAPIKey(context.Background()); err != nil {
		return err
	} else if raw != "" {
		log.Printf("API key generated (shown once, store it now): %s", raw)
	}

	// Opt-in ai-variant image only (see Dockerfile's "ai" build stage and
	// docker-entrypoint-ai.sh) — SAKMS_BUNDLED_OLLAMA_MODEL is blank on the
	// default image, so this is a no-op for every existing install. Same
	// one-shot-boot-step reasoning as the API key block above: not fatal,
	// since a seeding failure just means the operator falls back to
	// configuring the ollama connection/model by hand in Settings, same as
	// any other install.
	if cfg.BundledOllamaModel != "" {
		if err := seedBundledOllamaDefaults(context.Background(), connStore, settingsStore, cfg.BundledOllamaModel); err != nil {
			log.Printf("bundled ollama: seeding defaults: %v", err)
		}
	}

	// Every review-workflow route requires a valid session OR a valid
	// X-Api-Key header; login/setup/logout/status live on their own
	// always-public mux instead of an exemption list on this one (see
	// internal/api.NewAuthMux's doc comment) — NewMux stays unaware auth
	// exists either way, so its own large test suite never had to change
	// for auth specifically.
	//
	// Claude 2026-08-03: trailing arg is the real discoverCache store, not a
	// scripted nil (BE-17, discover-scheduled-refresh plan §3.8).
	// Reason: BE-10 landed NewMux's trailing *discoverrefresh.Store param
	// with every call site (production and tests) passing nil so
	// `go build ./...` stayed green before the real store existed; this is
	// the one production call site, now wired to the store constructed
	// above. Every test call site in internal/api still passes nil
	// deliberately (see BE-10's own note there) — only this one needed to
	// change.
	// Troubleshooting: N/A — the cache is live from here on.
	apiMux := api.NewMux(&http.Client{Timeout: outboundTimeout}, connStore, serviceConnStore, propStore, allowStore, prober, phashDispatcher, videoDispatcher, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, feedHealth, rssFeedsStore, entityStore, webhookStore, dlManager, nzbManager, dedupHub, imageProxy, discoverCache, pruningStore)
	protectedAPI := auth.Middleware(secretStore, authStore, apiMux, sectionGate...)

	// Node mux: per-handler auth (bearer for node agents, master key/session
	// for operators). Mounted without a top-level auth.Middleware wrapper — so
	// unlike every other protected mux here, the section gate cannot be applied
	// at the mount and is passed INTO the constructor instead, which installs it
	// on the operator paths only (see NewNodesMux's doc comment). Without this
	// argument /api/nodes/* stays reachable while `settings` is locked, which is
	// exactly what SL-10 exists to catch.
	nodesMux := api.NewNodesMux(nodeReg, pairingReg, nodeKeyStore, secretStore, authStore, settingsStore, nodeSettingsStore, sectionGate...)

	// API-key management (status + regenerate) is session-protected like
	// the rest of /api/..., but deliberately NOT part of NewMux (see
	// api.NewAPIKeyMux's doc comment) — its own small mux, wrapped in the
	// same middleware so either a cookie or a key can reach it.
	apikeyMux := api.NewAPIKeyMux(authStore, sectionLockGate)
	protectedAPIKey := auth.Middleware(secretStore, authStore, apikeyMux, sectionGate...)

	// Auth-mode management (GET/PUT /api/auth/mode) mutates security state,
	// so — unlike NewAuthMux's setup/login/logout/status routes — it must be
	// session-protected. Wrapped in the same auth.Middleware as apikeyMux,
	// so either a session cookie or the universal API key can reach it. Its
	// exact-match pattern ("/api/auth/mode") beats NewAuthMux's subtree
	// pattern ("/api/auth/") regardless of registration order (Go ServeMux
	// picks the more specific match), so mode stays protected while
	// setup/login/logout/status stay public.
	// The gate is passed here as well as to auth.WithSectionGate above and to
	// NewSectionLockMux below — the SAME instance all three times, so all
	// three share one settings cache and one brute-force counter. It carries
	// §4.4's PIN requirement onto PUT /api/auth/mode, which is the section
	// lock's own disarm surface: switching to auth mode "none" makes the lock
	// inert, so without this one request would permanently disarm it.
	authModeMux := api.NewAuthModeMux(authStore, sectionLockGate, sectionLockDisabled)
	protectedAuthMode := auth.Middleware(secretStore, authStore, authModeMux, sectionGate...)

	// OIDC-mode config (GET status, PUT issuer/client id/client secret/
	// redirect URL) — the post-first-run Settings-switch path, not first-run
	// bootstrap (that's carried in the public /api/auth/setup body, see
	// api.authSetupHandler's "oidc" branch) and not the public login/callback
	// redirect legs (those are on NewAuthMux). Session-protected like the
	// other mode-specific muxes above.
	oidcMux := api.NewOIDCMux(authStore, secretStore)
	protectedOIDC := auth.Middleware(secretStore, authStore, oidcMux, sectionGate...)

	// Manual "Refresh now" trigger for the recheck feature (see
	// api.NewRecheckTriggerMux's doc comment) — its own small mux, same
	// precedent as apikeyMux/authModeMux/oidcMux above, since it needs
	// watchStore, a dependency NewMux doesn't otherwise carry.
	recheckTriggerMux := api.NewRecheckTriggerMux(connStore, watchStore)
	protectedRecheckTrigger := auth.Middleware(secretStore, authStore, recheckTriggerMux, sectionGate...)

	// Manual "Refresh now" trigger for the discover-refresh feature (see
	// api.NewDiscoverRefreshTriggerMux's doc comment) — same precedent as
	// recheckTriggerMux above, since it needs the full discoverrefresh.Deps
	// shape (built once, mirroring NewMux's own discoverRefreshDeps), a
	// dependency NewMux doesn't carry (plan §5.1).
	discoverRefreshTriggerMux := api.NewDiscoverRefreshTriggerMux(discoverrefresh.Deps{
		HTTPClient:    &http.Client{Timeout: outboundTimeout},
		ConnStore:     connStore,
		SettingsStore: settingsStore,
		SlidersStore:  slidersStore,
		TraktStore:    traktStore,
		TraktBaseURL:  trakt.DefaultBaseURL,
		Cache:         discoverCache,
	})
	protectedDiscoverRefreshTrigger := auth.Middleware(secretStore, authStore, discoverRefreshTriggerMux, sectionGate...)

	// Requests worklist + excluded-titles endpoints — its own mux because it
	// needs excludesStore, a dependency NewMux doesn't carry (same precedent as
	// recheckTriggerMux above). Mounted exact ("/api/requests", GET list) +
	// subtree ("/api/requests/", POST exclude/exclude-batch) on top below, both
	// beating the general "/api/" subtree.
	requestsMux := api.NewRequestsMux(grabsStore, libStore, excludesStore)
	protectedRequests := auth.Middleware(secretStore, authStore, requestsMux, sectionGate...)

	// The section lock's own control surface — its own mux for the same
	// reason as apikeyMux/authModeMux above (it needs sectionLockGate, which
	// NewMux does not carry), wrapped in the SAME auth.Middleware as every
	// other protected mux. Three of its routes are exempt from the SECTION
	// gate — sectionlock.Classify deliberately maps /api/section-lock/* to no
	// section, because the panel that manages the lock lives behind the
	// `settings` lock — but NONE of them is exempt from primary auth: a
	// session cookie or the universal API key is still required, so an
	// unauthenticated caller cannot read or clear the lock.
	sectionLockMux := api.NewSectionLockMux(sectionLockGate, authStore, sectionLockDisabled)
	protectedSectionLock := auth.Middleware(secretStore, authStore, sectionLockMux, sectionGate...)

	top := http.NewServeMux()
	// Claude 2026-08-04: /healthz pings Postgres — deploy gate must not pass on a dead DB.
	// Reason: Postgres migration; sakms-auto-update health_ok previously only checked HTTP 200.
	// Review if: readiness vs liveness split is introduced.
	top.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		if err := sqlDB.PingContext(r.Context()); err != nil {
			http.Error(w, "database unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Write([]byte("ok"))
	})
	organizeEventsMux := api.NewOrganizeEventsMux(organizeEventsStore)
	protectedOrganizeEvents := auth.Middleware(secretStore, authStore, organizeEventsMux, sectionGate...)

	top.Handle("/api/organize/events", protectedOrganizeEvents)
	top.Handle("/api/auth/mode", protectedAuthMode)
	top.Handle("/api/auth/oidc", protectedOIDC) // exact match: GET status, PUT config (session-protected)
	// Everything else under /api/auth/ — including the PUBLIC OIDC redirect
	// legs /api/auth/oidc/login and /api/auth/oidc/callback — goes to the
	// unwrapped NewAuthMux. The exact "/api/auth/oidc" match above beats this
	// subtree only for that exact path, so config stays protected while the
	// login/callback subpaths stay public (they must run before a session
	// exists).
	top.Handle("/api/auth/", api.NewAuthMux(authStore, secretStore))
	top.HandleFunc("GET /api/openapi.yaml", api.OpenapiHandler())
	top.Handle("/api/apikey", protectedAPIKey)                                         // exact match: GET status
	top.Handle("/api/apikey/", protectedAPIKey)                                        // subtree: POST .../regenerate
	top.Handle("/api/admin/recheck/trigger", protectedRecheckTrigger)                  // exact match: manual "Refresh now"
	top.Handle("/api/admin/discover-refresh/trigger", protectedDiscoverRefreshTrigger) // exact match: manual "Refresh now" (discover cache)
	top.Handle("/api/requests", protectedRequests)                                     // exact match: GET worklist (excluded-title-suppressed)
	top.Handle("/api/requests/", protectedRequests)                                    // subtree: POST exclude, exclude-batch
	// BOTH forms are required, same precedent as /api/apikey and /api/requests
	// above. The subtree pattern is what beats the general "/api/" one for
	// every real route here; the exact one exists because without it the bare
	// path falls through to "/api/" and 404s inside NewMux's own mux instead of
	// reaching this handler.
	top.Handle("/api/section-lock", protectedSectionLock)                // exact match
	top.Handle("/api/section-lock/", protectedSectionLock)               // subtree: status, unlock, lock, pin, sections
	top.Handle("GET /api/nodes/pair", api.PairStreamHandler(pairingReg)) // no auth: pre-pairing SSE
	top.Handle("/api/nodes", nodesMux)                                   // exact match: GET list (per-handler auth inside)
	top.Handle("/api/nodes/", nodesMux)                                  // subtree: {id}/approve, /pending, /settings, etc.
	top.Handle("/api/", protectedAPI)                                    // more general; still wins for everything else
	// The frontend is mounted last and matches only what no /api/... route
	// already claimed — Go's ServeMux picks the most specific pattern, so
	// this never shadows a real API route. It's deliberately NOT behind
	// auth.Middleware: it's static code with no data in it, and the login
	// screen itself has to load before any session exists to check.
	top.Handle("/", web.Handler())

	srv := &http.Server{Addr: cfg.Addr, Handler: top}

	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()
	log.Printf("sakms listening on %s (data dir %s)", cfg.Addr, cfg.DataDir)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Hand the Dedup progress hub the signal-driven shutdown context it could
	// not receive at construction time (it was built and injected into NewMux
	// before this ctx existed). Background Dedup scans derive their ctx from
	// dedupHub.BaseContext(), so a SIGTERM mid-scan cancels them cleanly —
	// matching recheck/adultnewest/parseentity/watchfolders below. There is no
	// goroutine to start: the Hub is passive until a scan publishes to it.
	dedupHub.Start(ctx)

	// Unified downloader: wire the completion callback (which needs stores
	// built above) and start the torrent client + poll loop. Same deliberate
	// "background loop is a documented exception, not a reversal of the
	// manual-workflow rule" as recheck/adultnewest below — a download engine
	// inherently observes its progress; there's no human-triggered equivalent
	// of "the download finished." Reuses the signal-driven ctx so shutdown
	// stops the torrent client too.
	if dlManager != nil {
		dlManager.SetOnComplete(api.DownloadCompleteImporter(&http.Client{Timeout: outboundTimeout}, connStore, serviceConnStore, settingsStore, grabsStore, libStore, prober, dlManager))
		// Stale-torrent callback, wired the same way and for the same reason as
		// the completion callback above: the engine detects the dead download,
		// internal/api owns what to do about it (cancel + park for re-search).
		// Both must be set BEFORE Start — the poll loop reads them unguarded.
		dlManager.SetOnStale(api.StaleTorrentHandler(settingsStore, grabsStore, dlManager))
		go func() {
			if err := dlManager.Start(ctx); err != nil && ctx.Err() == nil {
				log.Printf("downloader: manager stopped: %v", err)
			}
		}()
	}
	if nzbManager != nil {
		nzbManager.SetOnComplete(api.UsenetCompleteImporter(&http.Client{Timeout: outboundTimeout}, connStore, serviceConnStore, settingsStore, grabsStore, libStore, prober, dlManager, nzbManager))
		go nzbManager.Start(ctx)
	}

	// DELIBERATE, opt-in exception to this project's "manual by default, no
	// background pollers" rule (see internal/recheck's package doc + CLAUDE.md):
	// one background availability-recheck loop, gated OFF by default (interval
	// 0). Reuses the same signal-driven ctx as the HTTP server, so shutdown
	// cancels it too. To remove the feature entirely: delete internal/recheck,
	// this line, and watchStore's construction above.
	go recheck.Run(ctx, recheck.LoadInterval(ctx, settingsStore), connStore, settingsStore, watchStore)

	// Claude 2026-08-03: corrected "Gated OFF by default (interval 0)" below
	// (discover-scheduled-refresh plan §7.1).
	// Reason: adultnewest.LoadInterval (scan.go:141-154) returns
	// defaultIntervalHours (24h) when the interval key was never explicitly
	// set — this job is ON by default, same as internal/discoverrefresh
	// further down this file. The stale claim contradicted
	// internal/discoverrefresh's own comment ("the second scheduler that is
	// on by default") and CLAUDE.md's AMENDED 2026-08-03 note under
	// Automation. Pre-existing staleness, not caused by this feature, but
	// left uncorrected it would make that note look wrong to the next
	// reader. An explicit "0" saved via Settings still turns it off.
	//
	// Same deliberate, opt-in exception as recheck above (see
	// internal/adultnewest's package doc + CLAUDE.md's "Discover never
	// queries Prowlarr" note for why this is a safe exception, not a
	// reversal, of that rule): a background job that scans Prowlarr's
	// newest Adult releases and caches matched TPDB/StashDB/FansDB entities
	// for Adult Discover's newest-releases rows to read. ON by default (24h,
	// mirrored by internal/discoverrefresh below); an explicit "0" in
	// Settings turns it off. To remove entirely: delete internal/adultnewest,
	// this line, its NewMux params, and the two stores' construction above.
	go adultnewest.Run(ctx, adultnewest.LoadInterval(ctx, settingsStore), connStore, serviceConnStore, settingsStore, adultNewestReleaseStore, entityStore, rssFeedsStore, feedHealth)

	// Same deliberate, opt-in exception as recheck/adultnewest above: a
	// background job that syncs all four entity-cache sources (Stash/TPDB/
	// StashDB/FansDB) on one shared cadence, additive to the existing manual
	// per-source "Sync now" buttons. Gated OFF by default (interval 0). To
	// remove entirely: delete internal/parseentity/schedule.go and this line.
	go parseentity.Run(ctx, parseentity.LoadInterval(ctx, settingsStore), connStore, settingsStore, entityStore)

	// Claude 2026-08-03: corrected the CLAUDE.md note reference below from
	// "AMENDED 2026-08-02" to "AMENDED 2026-08-03" (discover-scheduled-refresh
	// plan §7.1). Reason: this line's own construction is what makes the
	// count seven in the first place — the 2026-08-02 note above still
	// documents six.
	//
	// Discover row-content background refresh — the SEVENTH interval-driven
	// scheduler (count the launch block, not any prose ordinal; see CLAUDE.md's
	// AMENDED 2026-08-03 note under Automation). Populates internal/discoverrefresh's
	// cache for Mainstream's six TMDB rows, every enabled custom slider, and
	// the Trakt watchlist (stash-box is NOT a fourth source — see RefreshAll's
	// own doc comment), so Discover renders with zero live external calls.
	// UNLIKE recheck/parseentity/scanschedule above, this one is ON BY DEFAULT
	// (24h) — the second scheduler in this codebase to be (after adultnewest's
	// browse pass just above) and the first Mainstream-affecting one — a fresh
	// install gets fast Discover out of the box, which is the entire point of
	// the feature; an explicit "0" in Settings turns it off. Read-only
	// content caching: it never proposes, applies or grabs anything, so no
	// Scan-only boundary test applies.
	// To remove entirely: delete internal/discoverrefresh, this line,
	// discoverCache's construction above, its NewMux param, and
	// discoverRefreshTriggerMux's construction + mount.
	go discoverrefresh.Run(ctx, discoverrefresh.LoadInterval(ctx, settingsStore), discoverrefresh.Deps{
		HTTPClient:    &http.Client{Timeout: outboundTimeout},
		ConnStore:     connStore,
		SettingsStore: settingsStore,
		SlidersStore:  slidersStore,
		TraktStore:    traktStore,
		TraktBaseURL:  trakt.DefaultBaseURL,
		Cache:         discoverCache,
	})

	// Watch-folders: monitors each mode's library root folder for new content
	// and triggers a Rename Scan automatically (never auto-Apply). Gated OFF
	// by default (WatchFoldersEnabledKey = false). To remove entirely: delete
	// internal/api/watchfolders.go and this line.
	go api.RunWatchFolders(ctx, &http.Client{Timeout: outboundTimeout}, connStore, serviceConnStore, settingsStore, propStore, libStore, videoDispatcher, prober, entityStore)

	// Usenet retry loop — the fifth deliberate, opt-in exception to "manual by
	// default" (see internal/api/usenetretry.go's file doc). Two jobs per cycle:
	// the AUTHORITATIVE sweep that converts an asynchronous usenet retrieval
	// failure into pending_retry (430) or failed (451, never retried), and the
	// re-search of every pending_retry row that is due. Gated OFF by default —
	// its interval is written by the auto-grab toggle (on -> 86400, off -> 0),
	// never set independently. To remove entirely: delete
	// internal/api/usenetretry.go, its four route registrations in handler.go,
	// and this line.
	go api.RunUsenetRetry(ctx, api.LoadUsenetRetryInterval(ctx, settingsStore), &http.Client{Timeout: outboundTimeout},
		connStore, serviceConnStore, settingsStore, grabsStore, excludesStore, webhookStore, libStore, dlManager, nzbManager)

	// General Rename/Purge/Dedup scan scheduler — the fourth deliberate, opt-in
	// exception to "manual by default" (see internal/scanschedule's package doc
	// + CLAUDE.md's AMENDED "no scheduler" note). Built as a compile-time
	// Scan-only safety boundary: it drives its workflows only through the narrow
	// scanschedule.Scanner interface (scanAdapter below), which cannot reach any
	// Apply-family call. Per-workflow interval + dedup eager-VMAF toggle, all
	// gated OFF by default (0/off). Run launches one goroutine per workflow and
	// returns; all are cancelled via ctx on shutdown. Dedup cycles share the
	// same dedupHub concurrency guard as manual Dedup scans. To remove entirely:
	// delete internal/scanschedule, scanadapter.go, and this block.
	scanScheduler := newScanAdapter(&http.Client{Timeout: outboundTimeout}, connStore, serviceConnStore, settingsStore, propStore, allowStore, libStore, pruningStore, prober, phashDispatcher, videoDispatcher, entityStore)
	scanschedule.Run(ctx, scanScheduler, settingsStore, dedupHub)

	// Claude 2026-08-03: corrected the ordinal below from "seventh" to
	// "eighth" (discover-scheduled-refresh plan §7.1).
	// Reason: internal/discoverrefresh.Run (further up this file) is now the
	// real seventh interval-driven scheduler, so this one-shot backfill
	// would be the eighth position if it were counted as a scheduler at all
	// — which, per the sentence below, it still is not. CLAUDE.md's own
	// "count the launch block, not any prose ordinal" instruction exists
	// precisely to stop ordinals like this one from rotting.
	//
	// One-shot backfill of the size/quality_tier columns added in migration
	// 0055, for library rows that predate them. This is NOT an eighth
	// scheduler and must not be counted as one in CLAUDE.md's enumeration:
	// it has no interval, no toggle and no re-trigger, it runs exactly once
	// per boot, and it is a cheap no-op once every tracked row is captured.
	//
	// Deliberately started in a goroutine AFTER ListenAndServe, unlike
	// rssfeeds.BackfillEncryption / serviceconn.BackfillUsenetURL above,
	// which are called synchronously before it. Those two touch only DB rows
	// and finish in microseconds; this one os.Stats every tracked file, so a
	// stale or disconnected CIFS/NFS mount blocks on the syscall. Running
	// synchronously, that would hang boot and the app would never serve;
	// backgrounded, the same stale mount degrades one Dashboard section
	// instead. Uses the signal-driven ctx so SIGTERM stops the sweep between
	// rows rather than waiting the whole run out — a cancelled run returns
	// its partial summary with a nil error and the next boot resumes.
	go func() {
		summary, err := libStore.BackfillSizeAndTier(ctx)
		if err != nil {
			log.Printf("library: size/tier backfill failed: %v", err)
			return
		}
		if summary.Scanned == 0 {
			return
		}
		// Per-bucket counts are the operator's only visibility into the real
		// tier distribution. Expect mostly "unknown" on a library SAK has
		// already Renamed — the naming schemes strip every quality token, so
		// there is nothing left on disk to infer from. That is the accepted
		// outcome, not a failure.
		log.Printf("library: size/tier backfill captured %d rows (%d sized, %d unstattable); tiers %v",
			summary.Scanned, summary.SizedOK, summary.SizeFailed, summary.ByTier)
	}()

	select {
	case err := <-errCh:
		if err != nil && err != http.ErrServerClosed {
			return err
		}
	case <-ctx.Done():
		log.Println("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			return err
		}
	}
	return nil
}

// seedBundledOllamaDefaults gives the opt-in ai-variant image (see
// Dockerfile's "ai" build stage) a working AI backend with zero Settings-page
// steps: an "ollama" connection pointed at the in-container server, and the
// model to request. Only fills in what's genuinely unset — a connection or
// model an operator already configured (even to point somewhere else
// entirely, e.g. an external Ollama, OpenAI, or a different local model) is
// never overwritten, so this only ever helps a blank install and can't fight
// a deliberate choice made later.
func seedBundledOllamaDefaults(ctx context.Context, connStore *connections.Store, settingsStore *settings.Store, model string) error {
	if _, err := connStore.Get(ctx, "ollama"); errors.Is(err, connections.ErrNotFound) {
		if err := connStore.Upsert(ctx, "ollama", "http://localhost:11434", ""); err != nil {
			return fmt.Errorf("seeding ollama connection: %w", err)
		}
	} else if err != nil {
		return fmt.Errorf("checking existing ollama connection: %w", err)
	}

	if _, err := settingsStore.Get(ctx, mode.AIModelKey); errors.Is(err, settings.ErrNotFound) {
		if err := settingsStore.Set(ctx, mode.AIModelKey, model); err != nil {
			return fmt.Errorf("seeding ai_model setting: %w", err)
		}
	} else if err != nil {
		return fmt.Errorf("checking existing ai_model setting: %w", err)
	}
	return nil
}

// buildDownloader below is the boot-time half of a two-reader contract; the
// downloader-config GET/PUT handlers in internal/api are the other half, and
// the PUT handler is what feeds Manager.Reconfigure while the process runs. The
// two MUST read the same keys with the same defaults. If they diverge, a saved
// setting applies while the process is up and silently reverts on the next
// restart — worse than never applying it, because nothing errors and the value
// is still sitting in the settings table.
//
// Claude 2026-08-01: this file's own torrentDefault* const block was deleted;
// both the keys (api.*Key) and the defaults (api.TorrentDefault* /
// api.DownloaderDefault*) are now read straight from internal/api below.
// Reason: the const block was a hand-maintained duplicate of internal/api's
// then-package-private set, and MaxConcurrent/MaxConnections weren't even in it
// — they were bare 3 and 4 literals inline. Nothing but review prevented the
// two halves drifting, which is the exact silent-revert failure the paragraph
// above describes. With one shared definition the contract is compiler-enforced,
// so no drift test is needed or possible. (Values live in
// internal/api/downloads.go, including the note that DHT/PEX must stay TRUE —
// reading them with anything that yields the zero value on an unset key turns
// peer discovery off on every fresh install.)
// Troubleshooting: a torrent setting that works until the container restarts.
// Review if: this file stops importing internal/api.
//
// buildDownloader reads the operator-tunable config from settings (staging dir
// defaulting to <dataDir>/downloads, concurrency and torrent-engine knobs to
// their documented defaults) and constructs the process-lifetime download
// Manager. It does NOT start the engine — the caller does that with
// `go m.Start(ctx)`.
func buildDownloader(ctx context.Context, dataDir string, settingsStore *settings.Store, httpClient *http.Client) (*downloader.Manager, error) {
	staging, err := settingsStore.Get(ctx, api.DownloaderStagingDirKey)
	if err != nil && !errors.Is(err, settings.ErrNotFound) {
		return nil, err
	}
	if staging == "" {
		staging = filepath.Join(dataDir, "downloads")
	}

	maxConc := settingInt(ctx, settingsStore, api.DownloaderMaxConcurrentKey, api.DownloaderDefaultMaxConcurrent)
	maxConn := settingInt(ctx, settingsStore, api.DownloaderMaxConnectionsKey, api.DownloaderDefaultMaxConnections)

	obfuscation, err := settingsStore.Get(ctx, api.TorrentObfuscationModeKey)
	if err != nil && !errors.Is(err, settings.ErrNotFound) {
		return nil, err
	}
	if obfuscation == "" {
		obfuscation = api.TorrentDefaultObfuscationMode
	}

	return downloader.New(downloader.Config{
		StagingDir: staging,
		MaxConc:    maxConc,
		MaxConn:    maxConn,

		DownloadRateLimit:     settingInt(ctx, settingsStore, api.TorrentDownloadRateLimitKey, api.TorrentDefaultDownloadRateLimit),
		DHTEnabled:            settingBool(ctx, settingsStore, api.TorrentDHTEnabledKey, api.TorrentDefaultDHTEnabled),
		PEXEnabled:            settingBool(ctx, settingsStore, api.TorrentPEXEnabledKey, api.TorrentDefaultPEXEnabled),
		ListenPort:            settingInt(ctx, settingsStore, api.TorrentListenPortKey, api.TorrentDefaultListenPort),
		ObfuscationMode:       obfuscation,
		SeedingEnabled:        settingBool(ctx, settingsStore, api.TorrentSeedingEnabledKey, api.TorrentDefaultSeedingEnabled),
		SeedRatioLimit:        settingFloat(ctx, settingsStore, api.TorrentSeedRatioLimitKey, api.TorrentDefaultSeedRatioLimit),
		SeedDurationMinutes:   settingInt(ctx, settingsStore, api.TorrentSeedDurationMinutesKey, api.TorrentDefaultSeedDurationMinutes),
		StaleThresholdMinutes: settingInt(ctx, settingsStore, api.TorrentStaleThresholdMinutesKey, api.TorrentDefaultStaleThresholdMinutes),
	}, httpClient), nil
}

// buildUsenetManager reads every usenet-kind row from serviceConnStore (a
// Usenet subscription may now be zero, one, or many — the registry replaced
// the old singleton "nntp" connection) and constructs a usenet.Manager built
// from all of them. Disabled subscriptions are excluded from the pool set,
// same "only enabled rows are live" convention as PlayersForMode.
//
// ALWAYS returns a non-nil *usenet.Manager, even when err != nil — the one
// invariant every caller may rely on. usenet.New is safe to call with zero
// servers (see its doc comment), so a fresh install with no subscription
// configured boots with a working, empty Manager, and a genuine infra
// failure (the registry or settings store couldn't be read) degrades to that
// same empty-Manager shape rather than nil — callers now branch on
// Manager.HasSubscriptions(), not a nil check, and a nil Manager would panic
// on that method. The caller should still log err.
//
// Note the global downloader_max_connections setting is deliberately NOT
// read here — see DownloaderMaxConnectionsKey's doc comment: it is
// torrent-only now. Each subscription carries its own MaxConns, with
// usenet.defaultMaxConnsPerServer covering an unset (<=0) value.
func buildUsenetManager(ctx context.Context, dataDir string, serviceConnStore *serviceconn.Store, settingsStore *settings.Store, httpClient *http.Client) (*usenet.Manager, error) {
	var servers []usenet.ServerConfig
	subs, err := serviceConnStore.ListByKind(ctx, serviceconn.KindUsenet)
	if err != nil {
		err = fmt.Errorf("usenet: reading usenet subscriptions: %w", err)
	} else {
		servers = make([]usenet.ServerConfig, 0, len(subs))
		for _, s := range subs {
			if !s.Enabled {
				continue
			}
			servers = append(servers, usenet.ServerConfig{
				Host:     s.Host,
				Port:     s.Port,
				TLS:      s.TLS,
				Username: s.Username,
				Password: s.Secret,
				MaxConns: s.MaxConns,
			})
		}
	}

	staging, stagingErr := settingsStore.Get(ctx, api.DownloaderStagingDirKey)
	if stagingErr != nil && !errors.Is(stagingErr, settings.ErrNotFound) {
		staging = ""
		if err == nil {
			err = fmt.Errorf("usenet: reading staging dir: %w", stagingErr)
		}
	}
	if staging == "" {
		staging = filepath.Join(dataDir, "downloads")
	}

	m := usenet.New(usenet.Config{
		Servers:    servers,
		StagingDir: staging,
		HTTPClient: httpClient,
	})
	return m, err
}

// settingInt reads an int settings scalar, returning def when unset/invalid.
func settingInt(ctx context.Context, settingsStore *settings.Store, key string, def int) int {
	v, err := settingsStore.Get(ctx, key)
	if err != nil || v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

// settingBool reads a bool setting, returning def when the key is unset or
// unreadable. def — not false — is what an unset key yields, which is what
// makes a true-by-default knob like torrent_dht_enabled survive a fresh
// install.
func settingBool(ctx context.Context, settingsStore *settings.Store, key string, def bool) bool {
	v, err := settingsStore.GetBool(ctx, key, def)
	if err != nil {
		return def
	}
	return v
}

// settingFloat reads a float setting stored as a string, returning def when the
// key is unset or unparseable. Same shape as settingInt.
func settingFloat(ctx context.Context, settingsStore *settings.Store, key string, def float64) float64 {
	v, err := settingsStore.Get(ctx, key)
	if err != nil || v == "" {
		return def
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return def
	}
	return f
}

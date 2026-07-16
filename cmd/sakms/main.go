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
	"syscall"
	"time"

	"github.com/curtiswtaylorjr/sakms/internal/adultnewest"
	"github.com/curtiswtaylorjr/sakms/internal/allowlist"
	"github.com/curtiswtaylorjr/sakms/internal/api"
	"github.com/curtiswtaylorjr/sakms/internal/auth"
	"github.com/curtiswtaylorjr/sakms/internal/config"
	"github.com/curtiswtaylorjr/sakms/internal/connections"
	"github.com/curtiswtaylorjr/sakms/internal/db"
	"github.com/curtiswtaylorjr/sakms/internal/discoversliders"
	"github.com/curtiswtaylorjr/sakms/internal/grabs"
	"github.com/curtiswtaylorjr/sakms/internal/library"
	"github.com/curtiswtaylorjr/sakms/internal/mediainfo"
	"github.com/curtiswtaylorjr/sakms/internal/mode"
	"github.com/curtiswtaylorjr/sakms/internal/parseentity"
	"github.com/curtiswtaylorjr/sakms/internal/phash"
	"github.com/curtiswtaylorjr/sakms/internal/proposals"
	"github.com/curtiswtaylorjr/sakms/internal/recheck"
	"github.com/curtiswtaylorjr/sakms/internal/rssfeeds"
	"github.com/curtiswtaylorjr/sakms/internal/secrets"
	"github.com/curtiswtaylorjr/sakms/internal/settings"
	"github.com/curtiswtaylorjr/sakms/internal/trakt"
	"github.com/curtiswtaylorjr/sakms/internal/videophash"
	"github.com/curtiswtaylorjr/sakms/internal/web"
)

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
	sqlDB, err := db.Open(filepath.Join(cfg.DataDir, "sakms.db"))
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
	allowStore := allowlist.New(sqlDB)
	prober := mediainfo.New()
	hasher := phash.New()
	// videoHasher is SAK's StashDB-compatible video perceptual hasher for Adult
	// Rename's phash-first identification — a SEPARATE algorithm from `hasher`
	// (internal/phash, Movies/Series Dedup); the two are not interchangeable.
	videoHasher := videophash.New()
	settingsStore := settings.New(sqlDB)
	grabsStore := grabs.New(sqlDB)
	libStore := library.New(sqlDB)
	slidersStore := discoversliders.New(sqlDB)
	// rssFeedsStore backs admin-defined raw RSS 2.0 feed rows (NZBGeek
	// saved-search style) — a per-row feed URL fetched and parsed server-side
	// at resolve time, a separate concept from slidersStore (TMDB-backed).
	rssFeedsStore := rssfeeds.New(sqlDB)
	// traktStore persists Trakt's single application connection + linked
	// account tokens (its own table, not connections.Store — see
	// internal/trakt's package doc for why); secretStore encrypts the same
	// way it does for connStore.
	traktStore := trakt.NewStore(sqlDB, secretStore)
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
	// entityStore is the DB-first entity cache for Adult filename parsing. It
	// wraps the same sqlDB as every other store — no second connection needed.
	entityStore := parseentity.NewSQLiteStore(sqlDB)
	// secretStore doubles as authStore's OIDC-client-secret decryptor, and
	// the outbound HTTP client is the same outboundTimeout-bounded one every
	// other external client in this program uses — it bounds OIDC discovery,
	// token exchange, and JWKS fetch (oidc mode). Middleware's own signature
	// is untouched.
	authStore := auth.New(settingsStore, secretStore, &http.Client{Timeout: outboundTimeout})

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
	apiMux := api.NewMux(&http.Client{Timeout: outboundTimeout}, connStore, propStore, allowStore, prober, hasher, videoHasher, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, entityStore)
	protectedAPI := auth.Middleware(secretStore, authStore, apiMux)

	// API-key management (status + regenerate) is session-protected like
	// the rest of /api/..., but deliberately NOT part of NewMux (see
	// api.NewAPIKeyMux's doc comment) — its own small mux, wrapped in the
	// same middleware so either a cookie or a key can reach it.
	apikeyMux := api.NewAPIKeyMux(authStore)
	protectedAPIKey := auth.Middleware(secretStore, authStore, apikeyMux)

	// Auth-mode management (GET/PUT /api/auth/mode) mutates security state,
	// so — unlike NewAuthMux's setup/login/logout/status routes — it must be
	// session-protected. Wrapped in the same auth.Middleware as apikeyMux,
	// so either a session cookie or the universal API key can reach it. Its
	// exact-match pattern ("/api/auth/mode") beats NewAuthMux's subtree
	// pattern ("/api/auth/") regardless of registration order (Go ServeMux
	// picks the more specific match), so mode stays protected while
	// setup/login/logout/status stay public.
	authModeMux := api.NewAuthModeMux(authStore)
	protectedAuthMode := auth.Middleware(secretStore, authStore, authModeMux)

	// OIDC-mode config (GET status, PUT issuer/client id/client secret/
	// redirect URL) — the post-first-run Settings-switch path, not first-run
	// bootstrap (that's carried in the public /api/auth/setup body, see
	// api.authSetupHandler's "oidc" branch) and not the public login/callback
	// redirect legs (those are on NewAuthMux). Session-protected like the
	// other mode-specific muxes above.
	oidcMux := api.NewOIDCMux(authStore, secretStore)
	protectedOIDC := auth.Middleware(secretStore, authStore, oidcMux)

	// Manual "Refresh now" trigger for the recheck feature (see
	// api.NewRecheckTriggerMux's doc comment) — its own small mux, same
	// precedent as apikeyMux/authModeMux/oidcMux above, since it needs
	// watchStore, a dependency NewMux doesn't otherwise carry.
	recheckTriggerMux := api.NewRecheckTriggerMux(connStore, watchStore)
	protectedRecheckTrigger := auth.Middleware(secretStore, authStore, recheckTriggerMux)

	top := http.NewServeMux()
	top.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	})
	top.Handle("/api/auth/mode", protectedAuthMode)
	top.Handle("/api/auth/oidc", protectedOIDC) // exact match: GET status, PUT config (session-protected)
	// Everything else under /api/auth/ — including the PUBLIC OIDC redirect
	// legs /api/auth/oidc/login and /api/auth/oidc/callback — goes to the
	// unwrapped NewAuthMux. The exact "/api/auth/oidc" match above beats this
	// subtree only for that exact path, so config stays protected while the
	// login/callback subpaths stay public (they must run before a session
	// exists).
	top.Handle("/api/auth/", api.NewAuthMux(authStore, secretStore))
	top.Handle("/api/apikey", protectedAPIKey)                        // exact match: GET status
	top.Handle("/api/apikey/", protectedAPIKey)                       // subtree: POST .../regenerate
	top.Handle("/api/admin/recheck/trigger", protectedRecheckTrigger) // exact match: manual "Refresh now"
	top.Handle("/api/", protectedAPI)                                 // more general; still wins for everything else
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

	// DELIBERATE, opt-in exception to this project's "manual by default, no
	// background pollers" rule (see internal/recheck's package doc + CLAUDE.md):
	// one background availability-recheck loop, gated OFF by default (interval
	// 0). Reuses the same signal-driven ctx as the HTTP server, so shutdown
	// cancels it too. To remove the feature entirely: delete internal/recheck,
	// this line, and watchStore's construction above.
	go recheck.Run(ctx, recheck.LoadInterval(ctx, settingsStore), connStore, settingsStore, watchStore)

	// Same deliberate, opt-in exception as recheck above (see
	// internal/adultnewest's package doc + CLAUDE.md's "Discover never
	// queries Prowlarr" note for why this is a safe exception, not a
	// reversal, of that rule): a background job that scans Prowlarr's
	// newest Adult releases and caches matched TPDB/StashDB/FansDB entities
	// for Adult Discover's newest-releases rows to read. Gated OFF by
	// default (interval 0). To remove entirely: delete internal/adultnewest,
	// this line, its NewMux params, and the two stores' construction above.
	go adultnewest.Run(ctx, adultnewest.LoadInterval(ctx, settingsStore), connStore, settingsStore, adultNewestReleaseStore, entityStore)

	// Same deliberate, opt-in exception as recheck/adultnewest above: a
	// background job that syncs all four entity-cache sources (Stash/TPDB/
	// StashDB/FansDB) on one shared cadence, additive to the existing manual
	// per-source "Sync now" buttons. Gated OFF by default (interval 0). To
	// remove entirely: delete internal/parseentity/schedule.go and this line.
	go parseentity.Run(ctx, parseentity.LoadInterval(ctx, settingsStore), connStore, settingsStore, entityStore)

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

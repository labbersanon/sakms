// Command sakms runs the SAK server.
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/curtiswtaylorjr/sakms/internal/allowlist"
	"github.com/curtiswtaylorjr/sakms/internal/api"
	"github.com/curtiswtaylorjr/sakms/internal/auth"
	"github.com/curtiswtaylorjr/sakms/internal/config"
	"github.com/curtiswtaylorjr/sakms/internal/connections"
	"github.com/curtiswtaylorjr/sakms/internal/db"
	"github.com/curtiswtaylorjr/sakms/internal/grabs"
	"github.com/curtiswtaylorjr/sakms/internal/mediainfo"
	"github.com/curtiswtaylorjr/sakms/internal/proposals"
	"github.com/curtiswtaylorjr/sakms/internal/secrets"
	"github.com/curtiswtaylorjr/sakms/internal/settings"
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
	settingsStore := settings.New(sqlDB)
	grabsStore := grabs.New(sqlDB)
	authStore := auth.New(settingsStore)

	// Every review-workflow route requires a valid session; login/setup/
	// logout/status live on their own always-public mux instead of an
	// exemption list on this one (see internal/api.NewAuthMux's doc
	// comment) — NewMux stays unaware auth exists either way, so its own
	// large test suite never had to change for auth specifically.
	apiMux := api.NewMux(&http.Client{Timeout: outboundTimeout}, connStore, propStore, allowStore, prober, settingsStore, grabsStore)
	protectedAPI := auth.Middleware(secretStore, apiMux)

	top := http.NewServeMux()
	top.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	})
	top.Handle("/api/auth/", api.NewAuthMux(authStore, secretStore))
	top.Handle("/api/", protectedAPI)
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

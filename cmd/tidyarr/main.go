// Command tidyarr runs the Tidyarr server.
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

	"github.com/curtiswtaylorjr/tidyarr/internal/allowlist"
	"github.com/curtiswtaylorjr/tidyarr/internal/api"
	"github.com/curtiswtaylorjr/tidyarr/internal/config"
	"github.com/curtiswtaylorjr/tidyarr/internal/connections"
	"github.com/curtiswtaylorjr/tidyarr/internal/db"
	"github.com/curtiswtaylorjr/tidyarr/internal/mediainfo"
	"github.com/curtiswtaylorjr/tidyarr/internal/proposals"
	"github.com/curtiswtaylorjr/tidyarr/internal/secrets"
	"github.com/curtiswtaylorjr/tidyarr/internal/settings"
)

// outboundTimeout bounds every call Tidyarr makes to a configured service
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
	sqlDB, err := db.Open(filepath.Join(cfg.DataDir, "tidyarr.db"))
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

	mux := api.NewMux(&http.Client{Timeout: outboundTimeout}, connStore, propStore, allowStore, prober, settingsStore)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	})

	srv := &http.Server{Addr: cfg.Addr, Handler: mux}

	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()
	log.Printf("tidyarr listening on %s (data dir %s)", cfg.Addr, cfg.DataDir)

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

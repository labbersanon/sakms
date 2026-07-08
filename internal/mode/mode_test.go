package mode

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/curtiswtaylorjr/tidyarr/internal/connections"
	"github.com/curtiswtaylorjr/tidyarr/internal/db"
	"github.com/curtiswtaylorjr/tidyarr/internal/secrets"
	"github.com/curtiswtaylorjr/tidyarr/internal/servarr"
)

func newTestConnStore(t *testing.T) *connections.Store {
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
	return connections.New(sqlDB, secretStore)
}

func TestBuild_MoviesUsesRadarrConnection(t *testing.T) {
	store := newTestConnStore(t)
	ctx := context.Background()
	if err := store.Upsert(ctx, "radarr", "http://radarr.local:7878", "radarr-key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sess, err := Build(ctx, store, &http.Client{Timeout: time.Second}, Movies)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sess.Mode != Movies {
		t.Errorf("expected Mode to be Movies, got %v", sess.Mode)
	}
	if sess.Servarr.AppType() != servarr.Radarr {
		t.Errorf("expected the Radarr app type, got %v", sess.Servarr.AppType())
	}
}

func TestBuild_SeriesUsesSonarrConnection(t *testing.T) {
	store := newTestConnStore(t)
	ctx := context.Background()
	if err := store.Upsert(ctx, "sonarr", "http://sonarr.local:8989", "sonarr-key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sess, err := Build(ctx, store, &http.Client{Timeout: time.Second}, Series)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sess.Servarr.AppType() != servarr.Sonarr {
		t.Errorf("expected the Sonarr app type, got %v", sess.Servarr.AppType())
	}
}

func TestBuild_MissingConnection(t *testing.T) {
	store := newTestConnStore(t)
	if _, err := Build(context.Background(), store, &http.Client{}, Movies); err == nil {
		t.Fatal("expected an error when radarr isn't configured yet")
	}
}

func TestBuild_AdultUsesWhisparrConnection(t *testing.T) {
	store := newTestConnStore(t)
	ctx := context.Background()
	if err := store.Upsert(ctx, "whisparr", "http://whisparr.local:6969", "whisparr-key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sess, err := Build(ctx, store, &http.Client{Timeout: time.Second}, Adult)
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
	store := newTestConnStore(t)
	_, err := Build(context.Background(), store, &http.Client{}, Adult)
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
	store := newTestConnStore(t)
	_, err := Build(context.Background(), store, &http.Client{}, Mode("bogus"))
	if err == nil {
		t.Fatal("expected an error for an unknown mode")
	}
	if errors.Is(err, connections.ErrNotFound) {
		t.Error("an unknown mode should fail before ever touching the connections store")
	}
}

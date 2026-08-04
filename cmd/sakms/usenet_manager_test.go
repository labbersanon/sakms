package main

import (
	"context"
	"net/http"
	"testing"

	"github.com/labbersanon/sakms/internal/dbtest"
	"github.com/labbersanon/sakms/internal/secrets"
	"github.com/labbersanon/sakms/internal/serviceconn"
	"github.com/labbersanon/sakms/internal/settings"
)

// newUsenetTestStores builds a serviceconn.Store and settings.Store against a
// real, freshly migrated SQLite file — same convention as newTestStores above.
func newUsenetTestStores(t *testing.T) (*serviceconn.Store, *settings.Store, func()) {
	t.Helper()
	sqlDB := dbtest.New(t)

	secretStore, err := secrets.New(make([]byte, 32))
	if err != nil {
		t.Fatalf("building secret store: %v", err)
	}

	return serviceconn.NewStore(sqlDB, secretStore), settings.New(sqlDB), func() { sqlDB.Close() }
}

// TestBuildUsenetManager_ZeroSubscriptions proves the boot-time invariant
// BE-4b's registry rewire depends on: a fresh install with no Usenet
// subscription configured still gets a working, non-nil Manager — the
// common case right after this ships, before any operator adds one.
func TestBuildUsenetManager_ZeroSubscriptions(t *testing.T) {
	serviceConnStore, settingsStore, closeDB := newUsenetTestStores(t)
	t.Cleanup(closeDB)

	m, err := buildUsenetManager(context.Background(), t.TempDir(), serviceConnStore, settingsStore, &http.Client{})
	if err != nil {
		t.Fatalf("unexpected error with zero subscriptions: %v", err)
	}
	if m == nil {
		t.Fatal("expected a non-nil Manager even with zero subscriptions configured")
	}
	if m.HasSubscriptions() {
		t.Fatal("expected HasSubscriptions() == false with zero subscriptions configured")
	}
}

// TestBuildUsenetManager_NeverNil proves buildUsenetManager never returns a
// nil Manager, even on a genuine infra error (here, a closed DB) — callers
// (e.g. search.go's dispatch path) branch on Manager.HasSubscriptions()
// instead of a nil check, and that call panics on a nil receiver.
func TestBuildUsenetManager_NeverNil(t *testing.T) {
	serviceConnStore, settingsStore, closeDB := newUsenetTestStores(t)
	closeDB() // force ListByKind to fail

	m, err := buildUsenetManager(context.Background(), t.TempDir(), serviceConnStore, settingsStore, &http.Client{})
	if err == nil {
		t.Fatal("expected an error from a closed DB")
	}
	if m == nil {
		t.Fatal("expected a non-nil Manager even when the registry read fails")
	}
	if m.HasSubscriptions() {
		t.Fatal("expected HasSubscriptions() == false when the registry read failed")
	}
}

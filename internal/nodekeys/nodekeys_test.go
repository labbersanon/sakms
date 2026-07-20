package nodekeys_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/curtiswtaylorjr/sakms/internal/db"
	"github.com/curtiswtaylorjr/sakms/internal/nodekeys"
)

func newTestStore(t *testing.T) *nodekeys.Store {
	t.Helper()
	sqlDB, err := db.Open(filepath.Join(t.TempDir(), "sakms.db"))
	if err != nil {
		t.Fatalf("opening db: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	return nodekeys.New(sqlDB)
}

func TestCreateAndValidate(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	id, rawKey, err := store.Create(ctx, "wade-pc")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if id == "" || rawKey == "" {
		t.Fatal("Create returned empty id or rawKey")
	}

	name, ok := store.Validate(ctx, rawKey)
	if !ok || name != "wade-pc" {
		t.Fatalf("Validate: got (%q, %v), want (wade-pc, true)", name, ok)
	}
}

func TestValidateWrongKey(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	if _, _, err := store.Create(ctx, "node1"); err != nil {
		t.Fatal(err)
	}
	_, ok := store.Validate(ctx, "notthekey")
	if ok {
		t.Fatal("Validate should return false for wrong key")
	}
}

func TestValidateEmpty(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	_, ok := store.Validate(ctx, "")
	if ok {
		t.Fatal("Validate should return false for empty key")
	}
}

func TestRevoke(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	id, rawKey, err := store.Create(ctx, "wade-pc")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Revoke(ctx, id); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	_, ok := store.Validate(ctx, rawKey)
	if ok {
		t.Fatal("Validate should return false after Revoke")
	}
}

func TestMultipleNodes(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	_, keyA, _ := store.Create(ctx, "nodeA")
	_, keyB, _ := store.Create(ctx, "nodeB")

	nameA, okA := store.Validate(ctx, keyA)
	nameB, okB := store.Validate(ctx, keyB)

	if !okA || nameA != "nodeA" {
		t.Errorf("keyA: got (%q,%v), want (nodeA,true)", nameA, okA)
	}
	if !okB || nameB != "nodeB" {
		t.Errorf("keyB: got (%q,%v), want (nodeB,true)", nameB, okB)
	}

	// keyA does not validate as keyB
	_, crossOk := store.Validate(ctx, keyB+"tampered")
	if crossOk {
		t.Error("tampered key should not validate")
	}
}

package nodekeys_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/labbersanon/sakms/internal/db"
	"github.com/labbersanon/sakms/internal/nodekeys"
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

	gotID, name, ok := store.Validate(ctx, rawKey)
	if !ok || name != "wade-pc" {
		t.Fatalf("Validate: got (%q, %q, %v), want (_, wade-pc, true)", gotID, name, ok)
	}
	if gotID != id {
		t.Fatalf("Validate id: got %q, want %q (the durable id from Create)", gotID, id)
	}
}

func TestValidateWrongKey(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	if _, _, err := store.Create(ctx, "node1"); err != nil {
		t.Fatal(err)
	}
	_, _, ok := store.Validate(ctx, "notthekey")
	if ok {
		t.Fatal("Validate should return false for wrong key")
	}
}

func TestValidateEmpty(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	_, _, ok := store.Validate(ctx, "")
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
	_, _, ok := store.Validate(ctx, rawKey)
	if ok {
		t.Fatal("Validate should return false after Revoke")
	}
}

func TestMultipleNodes(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	idA, keyA, _ := store.Create(ctx, "nodeA")
	idB, keyB, _ := store.Create(ctx, "nodeB")

	gotIDA, nameA, okA := store.Validate(ctx, keyA)
	gotIDB, nameB, okB := store.Validate(ctx, keyB)

	if !okA || nameA != "nodeA" || gotIDA != idA {
		t.Errorf("keyA: got (%q,%q,%v), want (%q,nodeA,true)", gotIDA, nameA, okA, idA)
	}
	if !okB || nameB != "nodeB" || gotIDB != idB {
		t.Errorf("keyB: got (%q,%q,%v), want (%q,nodeB,true)", gotIDB, nameB, okB, idB)
	}

	// keyA does not validate as keyB
	_, _, crossOk := store.Validate(ctx, keyB+"tampered")
	if crossOk {
		t.Error("tampered key should not validate")
	}
}

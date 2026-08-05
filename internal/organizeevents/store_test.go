package organizeevents_test

import (
	"context"
	"testing"

	"github.com/labbersanon/sakms/internal/dbtest"
	"github.com/labbersanon/sakms/internal/organizeevents"
)

func TestAppendListPrune(t *testing.T) {
	sqlDB := dbtest.New(t)
	store := organizeevents.New(sqlDB)
	ctx := context.Background()

	ok := true
	for i := 0; i < 3; i++ {
		if err := store.Append(ctx, organizeevents.Event{
			Workflow: "rename",
			Mode:     "movies",
			Kind:     organizeevents.KindScan,
			Message:  "scan",
			OK:       &ok,
		}); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	list, err := store.List(ctx, "rename", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 3 {
		t.Fatalf("got %d events, want 3", len(list))
	}
	if err := store.Prune(ctx, 30, 2); err != nil {
		t.Fatal(err)
	}
	list, err = store.List(ctx, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("after prune got %d, want 2", len(list))
	}
}

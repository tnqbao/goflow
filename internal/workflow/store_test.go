package workflow

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"github.com/tnqbao/kubereel/internal/database"
)

func TestWorkflowPersistsAndTransitions(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	db, err := database.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	store := NewStore(db)
	w, err := store.Create(ctx, "  a scene  ")
	if err != nil {
		t.Fatal(err)
	}
	if w.Prompt != "a scene" || w.Status != "queued" || w.ID == "" {
		t.Fatalf("unexpected workflow: %+v", w)
	}
	if err := store.Transition(ctx, w.ID, "queued", "running", "", ""); err != nil {
		t.Fatal(err)
	}
	if err := store.Transition(ctx, w.ID, "running", "succeeded", "artifact.json", ""); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = database.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	got, err := NewStore(db).Get(ctx, w.ID)
	if err != nil || got.Status != "succeeded" || got.Result != "artifact.json" {
		t.Fatalf("persisted workflow = %+v, %v", got, err)
	}
}

func TestInvalidAndConcurrentTransitions(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := NewStore(db)
	if _, err := store.Create(ctx, " "); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty prompt: %v", err)
	}
	if _, err := store.Get(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
	w, err := store.Create(ctx, "prompt")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Transition(ctx, w.ID, "queued", "succeeded", "fake", ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid transition: %v", err)
	}
	results := make(chan error, 2)
	var group sync.WaitGroup
	for i := 0; i < 2; i++ {
		group.Add(1)
		go func() { defer group.Done(); results <- store.Transition(ctx, w.ID, "queued", "running", "", "") }()
	}
	group.Wait()
	close(results)
	var success, conflict int
	for err := range results {
		if err == nil {
			success++
			continue
		}
		if errors.Is(err, ErrConflict) {
			conflict++
			continue
		}
		t.Fatalf("unexpected transition error: %v", err)
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("success=%d conflict=%d", success, conflict)
	}
}

package repository

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/tnqbao/kubereel/internal/database"
	"github.com/tnqbao/kubereel/internal/workflow"
)

func TestWorkflowRepositoryPersistenceAndOptimisticLocking(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "state.db")

	db, err := database.Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}

	repo := NewWorkflowRepository(db)
	now := time.Now().UTC().Truncate(time.Nanosecond)
	item := workflow.Workflow{
		ID:        "0123456789abcdef0123456789abcdef",
		Prompt:    "a scene",
		Status:    "queued",
		CreatedAt: now,
		UpdatedAt: now,
	}

	if err := repo.Insert(ctx, item); err != nil {
		t.Fatal(err)
	}

	active, err := repo.ListActive(ctx)
	if err != nil || len(active) != 1 || active[0].ID != item.ID {
		t.Fatalf("active = %+v, %v", active, err)
	}

	results := make(chan error, 2)
	var group sync.WaitGroup
	for i := 0; i < 2; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			results <- repo.UpdateStatus(ctx, item.ID, "queued", "running", "", "", time.Now().UTC())
		}()
	}
	group.Wait()
	close(results)

	var okCount, conflictCount int
	for updateErr := range results {
		if updateErr == nil {
			okCount++
			continue
		}
		if errors.Is(updateErr, workflow.ErrConflict) {
			conflictCount++
			continue
		}
		t.Fatalf("unexpected error: %v", updateErr)
	}
	if okCount != 1 || conflictCount != 1 {
		t.Fatalf("ok=%d conflict=%d", okCount, conflictCount)
	}

	if err := repo.UpdateStatus(ctx, item.ID, "running", "succeeded", "workflows/"+item.ID+"/final.mp4", "", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := database.Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()

	got, err := NewWorkflowRepository(reopened).FindByID(ctx, item.ID)
	if err != nil || got.Status != "succeeded" || got.Result != "workflows/"+item.ID+"/final.mp4" {
		t.Fatalf("reloaded = %+v, %v", got, err)
	}

	if _, err := NewWorkflowRepository(reopened).FindByID(ctx, "missing"); !errors.Is(err, workflow.ErrNotFound) {
		t.Fatalf("missing err = %v", err)
	}
}

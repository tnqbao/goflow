package service

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/tnqbao/kubereel/internal/database"
	"github.com/tnqbao/kubereel/internal/provider"
	"github.com/tnqbao/kubereel/internal/repository"
	"github.com/tnqbao/kubereel/internal/storage"
	"github.com/tnqbao/kubereel/internal/workflow"
)

func TestWorkflowServiceLifecycleAndArtifacts(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	db, err := database.Open(ctx, filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	artifacts, err := storage.NewFilesystem(dir, 1<<20, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer artifacts.Close()

	svc := NewWorkflowService(
		repository.NewWorkflowRepository(db),
		artifacts,
		NewProviderService(provider.NewManager()),
	)

	if _, err := svc.Create(ctx, " ", ""); !errors.Is(err, workflow.ErrInvalid) {
		t.Fatalf("empty prompt err = %v", err)
	}
	if _, err := svc.Create(ctx, "scene", "unknown"); !errors.Is(err, ErrProviderUnavailable) {
		t.Fatalf("unavailable provider err = %v", err)
	}
	if _, err := svc.Get(ctx, "not-a-valid-id"); !errors.Is(err, workflow.ErrNotFound) {
		t.Fatalf("invalid id err = %v", err)
	}

	created, err := svc.Create(ctx, "  mountain dawn  ", "")
	if err != nil {
		t.Fatal(err)
	}
	if created.Prompt != "mountain dawn" || created.Status != "queued" {
		t.Fatalf("created = %+v", created)
	}

	if _, err := svc.OpenArtifact(ctx, created.ID); !errors.Is(err, ErrArtifactUnavailable) {
		t.Fatalf("pending artifact err = %v", err)
	}

	if err := svc.Transition(ctx, created.ID, "queued", "succeeded", "bad", ""); !errors.Is(err, workflow.ErrInvalid) {
		t.Fatalf("invalid transition err = %v", err)
	}
	if err := svc.Transition(ctx, created.ID, "queued", "running", "", ""); err != nil {
		t.Fatal(err)
	}

	resultPath := filepath.Join("workflows", created.ID, "final.mp4")
	if err := artifacts.Put(ctx, filepath.ToSlash(resultPath), bytes.NewReader([]byte("mp4-bytes"))); err != nil {
		t.Fatal(err)
	}
	if err := svc.Transition(ctx, created.ID, "running", "succeeded", resultPath, ""); err != nil {
		t.Fatal(err)
	}

	download, err := svc.OpenArtifact(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer download.Object.Close()

	if download.ContentType != "video/mp4" || download.Filename != DownloadMP4Filename {
		t.Fatalf("download metadata = %+v", download)
	}
}

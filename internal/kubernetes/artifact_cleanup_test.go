package kubernetes

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tnqbao/kubereel/internal/storage"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestCleanupSuccessfulRunArtifacts(t *testing.T) {
	ctx := context.Background()
	controller, jobs := testStepController(t, mockRun("image"), 1)
	root := t.TempDir()
	artifacts, err := storage.NewFilesystem(root, 1<<20, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := artifacts.Close(); err != nil {
			t.Error(err)
		}
	})
	controller.Executor.Artifacts = artifacts
	reconcileTimes(t, controller, 2)
	completeMockJob(t, jobs, controller)
	reconcileTimes(t, controller, 1)
	run, err := controller.Runs.Get(ctx, "sample", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	keys, _, err := unstructured.NestedStringSlice(run.Object, "status", "artifacts")
	if err != nil || len(keys) != 1 {
		t.Fatalf("artifacts: %v, %v", keys, err)
	}
	completedAt := time.Now().UTC().Add(-2 * time.Hour)
	if err := os.Chtimes(filepath.Join(root, keys[0]), completedAt.Add(-time.Second), completedAt.Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := unstructured.SetNestedField(run.Object, completedAt.Format(time.RFC3339Nano), "status", "completedAt"); err != nil {
		t.Fatal(err)
	}
	if err := controller.Executor.cleanupArtifacts(ctx, run, completedAt.Add(30*time.Minute)); err != nil {
		t.Fatal(err)
	}
	object, err := artifacts.Open(ctx, keys[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := object.Close(); err != nil {
		t.Fatal(err)
	}
	if err := unstructured.SetNestedStringSlice(run.Object, []string{keys[0], "workflows/other/scene.svg"}, "status", "artifacts"); err != nil {
		t.Fatal(err)
	}
	if err := controller.Executor.cleanupArtifacts(ctx, run, time.Now()); err == nil {
		t.Fatal("accepted an unrelated artifact")
	}
	object, err = artifacts.Open(ctx, keys[0])
	if err != nil {
		t.Fatal("deleted artifact before validating all keys", err)
	}
	if err := object.Close(); err != nil {
		t.Fatal(err)
	}
	if err := unstructured.SetNestedStringSlice(run.Object, keys, "status", "artifacts"); err != nil {
		t.Fatal(err)
	}
	if err := controller.Executor.cleanupArtifacts(ctx, run, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := artifacts.Open(ctx, keys[0]); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("expired artifact: %v", err)
	}
	if err := controller.Executor.cleanupArtifacts(ctx, run, time.Now()); err != nil {
		t.Fatalf("repeat cleanup: %v", err)
	}
	if err := unstructured.SetNestedStringSlice(run.Object, []string{"workflows/other/scene.svg"}, "status", "artifacts"); err != nil {
		t.Fatal(err)
	}
	if err := controller.Executor.cleanupArtifacts(ctx, run, time.Now()); err == nil {
		t.Fatal("accepted an unrelated artifact")
	}
}

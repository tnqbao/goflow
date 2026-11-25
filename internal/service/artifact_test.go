package service

import (
	"context"
	"errors"
	"path"
	"strings"
	"testing"

	"github.com/tnqbao/kubereel/internal/repository"
	"github.com/tnqbao/kubereel/internal/storage"
	"github.com/tnqbao/kubereel/internal/workflow"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	corefake "k8s.io/client-go/kubernetes/fake"
)

func TestArtifactsExposeOnlyPublishedRunOutputs(t *testing.T) {
	ctx := context.Background()
	store, err := storage.NewFilesystem(t.TempDir(), 1<<20, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})

	resource := repository.VideoRunResource()
	run := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": resource.GroupVersion().String(), "kind": "VideoRun",
		"metadata": map[string]any{"name": "example", "namespace": "test", "uid": "run-uid"},
	}}
	steps := []any{
		map[string]any{"name": "image", "type": "image"},
		map[string]any{"name": "movie", "type": "video"},
	}
	statuses := []any{
		map[string]any{"name": "image", "phase": "Succeeded", "attempts": int64(1)},
		map[string]any{"name": "movie", "phase": "Succeeded", "attempts": int64(2)},
	}
	imageDir := path.Join("workflows", workflow.StepWorkID("run-uid", "image", 1))
	image := path.Join(imageDir, "scene.svg")
	movie := path.Join("workflows", workflow.StepWorkID("run-uid", "movie", 2), "clip.mp4")
	privateRequest := path.Join(imageDir, "request.json")
	foreign := path.Join("workflows", workflow.StepWorkID("other-uid", "image", 1), "scene.svg")
	run.Object["spec"] = map[string]any{"workflowSnapshot": map[string]any{"steps": steps}}
	run.Object["status"] = map[string]any{"phase": "Running", "steps": statuses, "artifacts": []any{image, foreign, privateRequest, movie}}

	for _, key := range []string{image, movie, foreign, privateRequest} {
		if err := store.Put(ctx, key, strings.NewReader("test object")); err != nil {
			t.Fatal(err)
		}
	}
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{resource: "VideoRunList"}, run)
	kubernetes := corefake.NewSimpleClientset()
	runs := client.Resource(resource).Namespace("test")
	svc := NewArtifacts(repository.NewExecutionRepository(runs, kubernetes.BatchV1().Jobs("test"), nil), store)

	first, err := svc.List(ctx, "example", 1, "")
	if err != nil || len(first.Items) != 1 || first.Items[0].Name != "scene.svg" || first.Continue == "" {
		t.Fatalf("first page = %+v, %v", first, err)
	}
	next, err := svc.List(ctx, "example", 1, first.Continue)
	if err != nil || len(next.Items) != 1 || next.Items[0].Name != "clip.mp4" || next.Continue != "" {
		t.Fatalf("next page = %+v, %v", next, err)
	}
	if _, err := svc.List(ctx, "", 1, first.Continue); !errors.Is(err, workflow.ErrInvalid) {
		t.Fatalf("cursor from another filter: %v", err)
	}
	for _, key := range []string{foreign, privateRequest} {
		if _, err := svc.Open(ctx, artifactID(run, key)); !errors.Is(err, ErrArtifactNotFound) {
			t.Fatalf("disallowed reference: %v", err)
		}
	}
	if err := svc.Delete(ctx, first.Items[0].ID); !errors.Is(err, ErrArtifactRunActive) {
		t.Fatalf("active run deletion: %v", err)
	}
	if err := store.Delete(ctx, movie); err != nil {
		t.Fatal(err)
	}
	page, err := svc.List(ctx, "", 50, "")
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("page with expired file = %+v, %v", page, err)
	}
}

func TestArtifactDeletionRequiresSettledRun(t *testing.T) {
	for _, tt := range []struct {
		phase, step string
		allowed     bool
	}{
		{"Succeeded", "Succeeded", true},
		{"Failed", "Running", false},
		{"Failed", "Failed", true},
		{"Cancelled", "RequiresReview", false},
		{"RequiresReview", "Succeeded", false},
	} {
		if got := canDeleteRunArtifact(Execution{Phase: tt.phase, Steps: []ExecutionStep{{Phase: tt.step}}}); got != tt.allowed {
			t.Fatalf("delete permission for %s/%s = %v", tt.phase, tt.step, got)
		}
	}
}

func TestRasterArtifactUsesActualEncoding(t *testing.T) {
	object := strings.NewReader("\xff\xd8\xff")
	name, kind, err := artifactMedia("workflows/id/image.png", object)
	if err != nil || name != "image.jpg" || kind != "image/jpeg" || object.Len() != 3 {
		t.Fatalf("raster metadata = %q, %q, %v, %d bytes remaining", name, kind, err, object.Len())
	}
}

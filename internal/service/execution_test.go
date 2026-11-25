package service

import (
	"context"
	"errors"
	"testing"

	"github.com/tnqbao/kubereel/internal/repository"
	"github.com/tnqbao/kubereel/internal/workflow"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	clienttesting "k8s.io/client-go/testing"
)

func TestExecutionApprovalAndRequiredInputs(t *testing.T) {
	snapshot := map[string]any{
		"steps":  []any{map[string]any{"providerRef": "external"}},
		"inputs": []any{map[string]any{"name": "prompt", "type": "string", "required": true}},
	}
	if err := checkPaidExecution(snapshot, false); !errors.Is(err, ErrPaidExecutionApproval) {
		t.Fatalf("approval = %v", err)
	}
	if err := checkPaidExecution(snapshot, true); err != nil {
		t.Fatal(err)
	}
	if err := validateExecutionInputs(snapshot, nil); !errors.Is(err, workflow.ErrInvalid) {
		t.Fatalf("missing input = %v", err)
	}
	if err := validateExecutionInputs(snapshot, map[string]any{"prompt": map[string]any{"integerValue": int64(1)}}); !errors.Is(err, workflow.ErrInvalid) {
		t.Fatalf("wrong input type = %v", err)
	}
	if err := validateExecutionInputs(snapshot, map[string]any{"prompt": map[string]any{"stringValue": "scene"}}); err != nil {
		t.Fatal(err)
	}
}

func TestRetryRejectsUnsettledExecutions(t *testing.T) {
	for _, tt := range []struct {
		phase, step string
		allowed     bool
	}{
		{"Failed", "Failed", true},
		{"Cancelled", "Cancelled", true},
		{"Failed", "Running", false},
		{"Failed", "Cancelling", false},
		{"Failed", "RequiresReview", false},
		{"RequiresReview", "Failed", false},
		{"Succeeded", "Succeeded", false},
	} {
		view := Execution{Phase: tt.phase, Steps: []ExecutionStep{{Phase: tt.step}}}
		if canRetryExecution(view) != tt.allowed {
			t.Fatalf("unexpected retry permission for %s/%s", tt.phase, tt.step)
		}
	}
}

func TestCancelRetriesConcurrentStatusUpdate(t *testing.T) {
	resource := repository.VideoRunResource()
	initial := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": resource.GroupVersion().String(),
		"kind":       "VideoRun",
		"metadata":   map[string]any{"name": "run", "namespace": "test"},
		"spec":       map[string]any{"workflowRef": "example"},
	}}
	client := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), initial)
	calls := 0
	client.PrependReactor("update", "videoruns", func(clienttesting.Action) (bool, runtime.Object, error) {
		calls++
		if calls == 1 {
			return true, nil, apierrors.NewConflict(resource.GroupResource(), "run", errors.New("concurrent status update"))
		}
		return false, nil, nil
	})
	svc := NewExecutions(repository.NewExecutionRepository(client.Resource(resource).Namespace("test"), nil, nil), nil)
	run, err := svc.Cancel(context.Background(), "run")
	if err != nil || calls != 2 || !run.CancelRequested {
		t.Fatalf("cancel = %+v, writes = %d, err = %v", run, calls, err)
	}
}

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/tnqbao/kubereel/internal/repository"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	clienttesting "k8s.io/client-go/testing"
)

func TestScheduleRecoversReservedRunAcrossRestart(t *testing.T) {
	ctx := context.Background()
	tick := time.Date(2026, 11, 26, 5, 10, 0, 0, time.UTC)
	workflows, runs := repository.VideoWorkflowResource(), repository.VideoRunResource()
	item := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": workflows.GroupVersion().String(), "kind": "VideoWorkflow",
		"metadata": map[string]any{"name": "daily", "namespace": "test", "uid": "workflow-uid"},
		"spec":     map[string]any{"steps": []any{map[string]any{"name": "scene", "type": "image", "providerRef": "mock"}}},
	}}
	opts := ScheduleOptions{Cron: "* * * * *", ExecutionOptions: ExecutionOptions{Inputs: map[string]any{"prompt": map[string]any{"stringValue": "scene"}}}}
	if err := setScheduleAnnotation(item, scheduleAnnotation, workflowSchedule{ScheduleOptions: opts, UpdatedAt: tick.Add(-time.Minute)}); err != nil {
		t.Fatal(err)
	}
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		workflows: "VideoWorkflowList", runs: "VideoRunList",
	}, item)
	workflowClient := client.Resource(workflows).Namespace("test")
	runClient := client.Resource(runs).Namespace("test")
	definitions := repository.NewWorkflowDefinitionRepository(workflowClient)
	executions := repository.NewExecutionRepository(runClient, nil, nil)
	updates := 0
	client.PrependReactor("update", "videoworkflows", func(clienttesting.Action) (bool, runtime.Object, error) {
		updates++
		if updates == 2 {
			return true, nil, apierrors.NewConflict(workflows.GroupResource(), "daily", errors.New("checkpoint write failed"))
		}
		return false, nil, nil
	})
	first := NewSchedules(definitions, executions)
	if err := first.reconcileSchedule(ctx, item, tick); !apierrors.IsConflict(err) {
		t.Fatalf("expected checkpoint failure, got %v", err)
	}
	page, err := runClient.List(ctx, metav1.ListOptions{})
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("initial executions = %d, %v", len(page.Items), err)
	}
	original := page.Items[0].GetName()

	restarted := NewSchedules(definitions, executions)
	if err := restarted.Reconcile(ctx, tick.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	schedule, err := restarted.Get(ctx, "daily")
	if err != nil || schedule.LastRunID != original || schedule.LastRunAt == nil || !schedule.LastRunAt.Equal(tick) {
		t.Fatalf("recovered schedule = %+v, %v", schedule, err)
	}
	for i := 0; i < 2; i++ {
		if err := restarted.Reconcile(ctx, tick.Add(2*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	page, err = runClient.List(ctx, metav1.ListOptions{})
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("active run was overlapped: %d, %v", len(page.Items), err)
	}
	run, err := runClient.Get(ctx, original, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := unstructured.SetNestedField(run.Object, "Succeeded", "status", "phase"); err != nil {
		t.Fatal(err)
	}
	if _, err := runClient.UpdateStatus(ctx, run, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := restarted.Reconcile(ctx, tick.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	page, err = runClient.List(ctx, metav1.ListOptions{})
	if err != nil || len(page.Items) != 2 {
		t.Fatalf("next occurrence was not submitted: %d, %v", len(page.Items), err)
	}
}

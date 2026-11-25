package kubernetes

import (
	"context"
	"errors"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic/fake"
	ktesting "k8s.io/client-go/testing"
)

func testVideoRun(steps []interface{}) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "kubereel.io/v1alpha1", "kind": "VideoRun",
		"metadata": map[string]interface{}{"name": "sample", "namespace": "test"},
		"spec":     map[string]interface{}{"workflowRef": "sample", "workflowSnapshot": map[string]interface{}{"steps": steps}},
	}}
}

func testRunController(run *unstructured.Unstructured) (*VideoRunController, *fake.FakeDynamicClient) {
	client := fake.NewSimpleDynamicClient(runtime.NewScheme(), run)
	return &VideoRunController{Runs: client.Resource(VideoRunResource()).Namespace("test")}, client
}

func TestVideoRunReconciliation(t *testing.T) {
	ctx := context.Background()
	run := testVideoRun([]interface{}{
		map[string]interface{}{"name": "first", "type": "input"},
		map[string]interface{}{"name": "second", "type": "output", "dependsOn": []interface{}{"first"}},
		map[string]interface{}{"name": "independent", "type": "image"},
	})
	controller, client := testRunController(run)
	if err := controller.Reconcile(ctx, "sample"); err != nil {
		t.Fatal(err)
	}
	got, err := controller.Runs.Get(ctx, "sample", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	phase, _, _ := unstructured.NestedString(got.Object, "status", "phase")
	steps, _, _ := unstructured.NestedSlice(got.Object, "status", "steps")
	if phase != "Pending" || len(steps) != 3 {
		t.Fatalf("unexpected status: %v", got.Object["status"])
	}
	if steps[0].(map[string]interface{})["ready"] != true || steps[1].(map[string]interface{})["ready"] != false || steps[2].(map[string]interface{})["ready"] != true {
		t.Fatalf("unexpected readiness: %v", steps)
	}
	client.ClearActions()
	if err := controller.Reconcile(ctx, "sample"); err != nil {
		t.Fatal(err)
	}
	for _, action := range client.Actions() {
		if action.GetVerb() == "update" {
			t.Fatal("unchanged reconciliation updated status")
		}
	}
	steps[0].(map[string]interface{})["phase"] = "Succeeded"
	if err := unstructured.SetNestedSlice(got.Object, steps, "status", "steps"); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.Runs.UpdateStatus(ctx, got, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := controller.Reconcile(ctx, "sample"); err != nil {
		t.Fatal(err)
	}
	got, err = controller.Runs.Get(ctx, "sample", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	steps, _, _ = unstructured.NestedSlice(got.Object, "status", "steps")
	if steps[0].(map[string]interface{})["ready"] != false || steps[1].(map[string]interface{})["ready"] != true {
		t.Fatalf("unexpected readiness after success: %v", steps)
	}
}

func TestVideoRunInvalidAndInactive(t *testing.T) {
	ctx := context.Background()
	cycle := []interface{}{
		map[string]interface{}{"name": "a", "dependsOn": []interface{}{"b"}},
		map[string]interface{}{"name": "b", "dependsOn": []interface{}{"a"}},
	}
	controller, _ := testRunController(testVideoRun(cycle))
	if err := controller.Reconcile(ctx, "sample"); err != nil {
		t.Fatal(err)
	}
	got, err := controller.Runs.Get(ctx, "sample", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	phase, _, _ := unstructured.NestedString(got.Object, "status", "phase")
	message, _, _ := unstructured.NestedString(got.Object, "status", "error")
	if phase != "Failed" || message == "" {
		t.Fatalf("invalid graph status: %v", got.Object["status"])
	}
	for _, state := range []string{"Succeeded", "Failed", "Cancelled", "Cancelling", "RequiresReview"} {
		run := testVideoRun(cycle)
		run.Object["status"] = map[string]interface{}{"phase": state}
		c, client := testRunController(run)
		if err := c.Reconcile(ctx, "sample"); err != nil {
			t.Fatal(err)
		}
		for _, action := range client.Actions() {
			if action.GetVerb() == "update" {
				t.Fatalf("%s run updated", state)
			}
		}
	}
	run := testVideoRun(cycle)
	run.Object["spec"].(map[string]interface{})["cancelRequested"] = true
	c, client := testRunController(run)
	if err := c.Reconcile(ctx, "sample"); err != nil {
		t.Fatal(err)
	}
	for _, action := range client.Actions() {
		if action.GetVerb() == "update" {
			t.Fatal("cancellation-requested run updated")
		}
	}
	if err := c.Reconcile(ctx, "missing"); err != nil {
		t.Fatalf("deleted run: %v", err)
	}
}

func TestVideoRunUpdateFailure(t *testing.T) {
	controller, client := testRunController(testVideoRun([]interface{}{map[string]interface{}{"name": "a"}}))
	want := errors.New("update failed")
	client.PrependReactor("update", "videoruns", func(action ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, want
	})
	if err := controller.Reconcile(context.Background(), "sample"); !errors.Is(err, want) {
		t.Fatalf("error = %v, want update failure", err)
	}
}

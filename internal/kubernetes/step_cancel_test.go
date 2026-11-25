package kubernetes

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/tnqbao/kubereel/internal/mock"
	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func requestCancellation(t *testing.T, controller *VideoRunController) {
	t.Helper()
	run, err := controller.Runs.Get(context.Background(), "sample", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := unstructured.SetNestedField(run.Object, true, "spec", "cancelRequested"); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.Runs.Update(context.Background(), run, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
}

func reconcileTimes(t *testing.T, controller *VideoRunController, count int) {
	t.Helper()
	for i := 0; i < count; i++ {
		if err := controller.Reconcile(context.Background(), "sample"); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCancelPendingRun(t *testing.T) {
	controller, jobs, _ := testStepController(t, mockRun("image"), 1)
	requestCancellation(t, controller)
	reconcileTimes(t, controller, 2)
	run, err := controller.Runs.Get(context.Background(), "sample", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	phase, _, _ := unstructured.NestedString(run.Object, "status", "phase")
	steps, _, _ := unstructured.NestedSlice(run.Object, "status", "steps")
	if phase != "Cancelled" || steps[0].(map[string]interface{})["phase"] != "Cancelled" || len(jobs.jobs) != 0 {
		t.Fatalf("status = %v, Jobs = %d", run.Object["status"], len(jobs.jobs))
	}
}

func TestCancelRunningJob(t *testing.T) {
	controller, jobs, _ := testStepController(t, mockRun("image"), 1)
	reconcileTimes(t, controller, 2)
	requestCancellation(t, controller)
	reconcileTimes(t, controller, 4)
	run, err := controller.Runs.Get(context.Background(), "sample", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	phase, _, _ := unstructured.NestedString(run.Object, "status", "phase")
	if phase != "Cancelled" || len(jobs.jobs) != 0 {
		t.Fatalf("status = %v, Jobs = %d", run.Object["status"], len(jobs.jobs))
	}
	if len(jobs.deletePolicies) != 1 || jobs.deletePolicies[0] != metav1.DeletePropagationForeground {
		t.Fatalf("deletion policies = %v", jobs.deletePolicies)
	}
}

func TestCancelMissingJobRequiresReview(t *testing.T) {
	controller, jobs, _ := testStepController(t, mockRun("image"), 1)
	reconcileTimes(t, controller, 2)
	for name := range jobs.jobs {
		delete(jobs.jobs, name)
	}
	requestCancellation(t, controller)
	reconcileTimes(t, controller, 3)
	run, err := controller.Runs.Get(context.Background(), "sample", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	phase, _, _ := unstructured.NestedString(run.Object, "status", "phase")
	if phase != "RequiresReview" {
		t.Fatalf("status = %v, want RequiresReview", run.Object["status"])
	}
}

func TestCancelDoesNotDeleteForeignJob(t *testing.T) {
	controller, jobs, _ := testStepController(t, mockRun("image"), 1)
	reconcileTimes(t, controller, 2)
	for _, job := range jobs.jobs {
		job.OwnerReferences[0].UID = "foreign"
	}
	requestCancellation(t, controller)
	reconcileTimes(t, controller, 3)
	run, err := controller.Runs.Get(context.Background(), "sample", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	phase, _, _ := unstructured.NestedString(run.Object, "status", "phase")
	if phase != "RequiresReview" || len(jobs.jobs) != 1 || len(jobs.deletePolicies) != 0 {
		t.Fatalf("status = %v, Jobs = %d, deletions = %v", run.Object["status"], len(jobs.jobs), jobs.deletePolicies)
	}
}

func TestCancelPreservesCompletedArtifact(t *testing.T) {
	controller, jobs, dataDir := testStepController(t, mockRun("image"), 1)
	reconcileTimes(t, controller, 2)
	for _, job := range jobs.jobs {
		id := job.Labels["kubereel.io/workflow"]
		dir := filepath.Join(dataDir, "workflows", id)
		if err := mock.Generate(context.Background(), filepath.Join(dir, "request.json"), filepath.Join(dir, "scene.svg")); err != nil {
			t.Fatal(err)
		}
		jobs.finish(id, "worker", batchv1.JobComplete)
	}
	requestCancellation(t, controller)
	reconcileTimes(t, controller, 3)
	run, err := controller.Runs.Get(context.Background(), "sample", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	phase, _, _ := unstructured.NestedString(run.Object, "status", "phase")
	artifacts, _, _ := unstructured.NestedStringSlice(run.Object, "status", "artifacts")
	if phase != "Cancelled" || len(artifacts) != 1 {
		t.Fatalf("status = %v", run.Object["status"])
	}
}

func TestFailedRunStopsOtherActiveJobs(t *testing.T) {
	controller, jobs, _ := testStepController(t, mockRun("first", "second"), 2)
	reconcileTimes(t, controller, 3)
	for _, job := range jobs.jobs {
		if job.Annotations["kubereel.io/step"] == "first" {
			jobs.finish(job.Labels["kubereel.io/workflow"], "worker", batchv1.JobFailed)
		}
	}
	reconcileTimes(t, controller, 5)
	run, err := controller.Runs.Get(context.Background(), "sample", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	phase, _, _ := unstructured.NestedString(run.Object, "status", "phase")
	if phase != "Failed" || len(jobs.jobs) != 1 {
		t.Fatalf("status = %v, Jobs = %d", run.Object["status"], len(jobs.jobs))
	}
	if len(jobs.deletePolicies) != 1 || jobs.deletePolicies[0] != metav1.DeletePropagationForeground {
		t.Fatalf("deletion policies = %v", jobs.deletePolicies)
	}
}

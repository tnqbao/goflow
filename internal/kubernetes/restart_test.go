package kubernetes

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

	"github.com/tnqbao/kubereel/internal/mock"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func restartedController(previous *VideoRunController, changedConfig bool) *VideoRunController {
	executor := *previous.Executor
	executor.JobLock = &sync.Mutex{}
	if changedConfig {
		ttl := int32(60)
		executor.Config.Image = "example/kubereel:new"
		executor.Config.PullPolicy = corev1.PullAlways
		executor.Config.TTLSecondsAfterFinished = &ttl
	}
	return &VideoRunController{Runs: previous.Runs, Executor: &executor}
}

func completeMockJob(t *testing.T, jobs *testJobs, dataDir string) {
	t.Helper()
	for _, job := range jobs.jobs {
		if finished(job, batchv1.JobFailed) || finished(job, batchv1.JobComplete) {
			continue
		}
		id := job.Labels["kubereel.io/workflow"]
		dir := filepath.Join(dataDir, "workflows", id)
		if err := mock.Generate(context.Background(), filepath.Join(dir, "request.json"), filepath.Join(dir, "scene.svg")); err != nil {
			t.Fatal(err)
		}
		jobs.finish(id, "worker", batchv1.JobComplete)
	}
}

func TestRestartObservesJobWithOriginalConfiguration(t *testing.T) {
	controller, jobs, dataDir := testStepController(t, mockRun("image"), 1)
	reconcileTimes(t, controller, 2)
	completeMockJob(t, jobs, dataDir)
	restarted := restartedController(controller, true)
	reconcileTimes(t, restarted, 1)
	run, err := restarted.Runs.Get(context.Background(), "sample", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	phase, _, _ := unstructured.NestedString(run.Object, "status", "phase")
	if phase != "Succeeded" || len(jobs.jobs) != 1 {
		t.Fatalf("status = %v, Jobs = %d", run.Object["status"], len(jobs.jobs))
	}
}

func TestRestartRetryUsesOriginalImage(t *testing.T) {
	controller, jobs, dataDir := testStepController(t, retryRun(), 1)
	reconcileTimes(t, controller, 2)
	for _, job := range jobs.jobs {
		jobs.finish(job.Labels["kubereel.io/workflow"], "worker", batchv1.JobFailed)
	}
	restarted := restartedController(controller, true)
	reconcileTimes(t, restarted, 2)
	if len(jobs.jobs) != 2 {
		t.Fatalf("Jobs = %d, want two attempts", len(jobs.jobs))
	}
	for _, job := range jobs.jobs {
		if job.Spec.Template.Spec.Containers[0].Image != controller.Executor.Config.Image {
			t.Fatalf("retry changed image: %s", job.Spec.Template.Spec.Containers[0].Image)
		}
	}
	completeMockJob(t, jobs, dataDir)
	reconcileTimes(t, restarted, 1)
	run, err := restarted.Runs.Get(context.Background(), "sample", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	phase, _, _ := unstructured.NestedString(run.Object, "status", "phase")
	if phase != "Succeeded" {
		t.Fatalf("status = %v", run.Object["status"])
	}
}

func TestRestartRecoversCancellation(t *testing.T) {
	controller, jobs, _ := testStepController(t, mockRun("image"), 1)
	reconcileTimes(t, controller, 2)
	requestCancellation(t, controller)
	reconcileTimes(t, controller, 2)
	if len(jobs.jobs) != 0 {
		t.Fatalf("Jobs = %d, want foreground deletion", len(jobs.jobs))
	}
	restarted := restartedController(controller, true)
	reconcileTimes(t, restarted, 2)
	run, err := restarted.Runs.Get(context.Background(), "sample", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	phase, _, _ := unstructured.NestedString(run.Object, "status", "phase")
	if phase != "Cancelled" {
		t.Fatalf("status = %v", run.Object["status"])
	}
}

func TestRestartMissingReservedJobRequiresReview(t *testing.T) {
	controller, jobs, _ := testStepController(t, mockRun("image"), 1)
	reconcileTimes(t, controller, 1)
	jobs.denied = true
	if err := controller.Reconcile(context.Background(), "sample"); err == nil {
		t.Fatal("expected submission error")
	}
	jobs.denied = false
	restarted := restartedController(controller, true)
	reconcileTimes(t, restarted, 2)
	run, err := restarted.Runs.Get(context.Background(), "sample", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	phase, _, _ := unstructured.NestedString(run.Object, "status", "phase")
	if phase != "RequiresReview" || len(jobs.jobs) != 0 {
		t.Fatalf("status = %v, Jobs = %d", run.Object["status"], len(jobs.jobs))
	}
}

func TestRestartRecoversPreviouslyCreatedJobWithoutConfigSnapshot(t *testing.T) {
	controller, jobs, dataDir := testStepController(t, mockRun("image"), 1)
	reconcileTimes(t, controller, 2)
	run, err := controller.Runs.Get(context.Background(), "sample", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	steps, _, err := unstructured.NestedSlice(run.Object, "status", "steps")
	if err != nil {
		t.Fatal(err)
	}
	delete(steps[0].(map[string]interface{}), "jobConfig")
	if err := unstructured.SetNestedSlice(run.Object, steps, "status", "steps"); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.Runs.UpdateStatus(context.Background(), run, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	completeMockJob(t, jobs, dataDir)
	restarted := restartedController(controller, false)
	reconcileTimes(t, restarted, 1)
	run, err = restarted.Runs.Get(context.Background(), "sample", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	phase, _, _ := unstructured.NestedString(run.Object, "status", "phase")
	if phase != "Succeeded" {
		t.Fatalf("status = %v", run.Object["status"])
	}
}

func TestRestartRejectsIncompleteRecordedConfiguration(t *testing.T) {
	controller, jobs, _ := testStepController(t, mockRun("image"), 1)
	reconcileTimes(t, controller, 2)
	run, err := controller.Runs.Get(context.Background(), "sample", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	steps, _, err := unstructured.NestedSlice(run.Object, "status", "steps")
	if err != nil {
		t.Fatal(err)
	}
	steps[0].(map[string]interface{})["jobConfig"] = map[string]interface{}{"image": "image:v1"}
	if err := unstructured.SetNestedSlice(run.Object, steps, "status", "steps"); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.Runs.UpdateStatus(context.Background(), run, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	restarted := restartedController(controller, true)
	reconcileTimes(t, restarted, 1)
	run, err = restarted.Runs.Get(context.Background(), "sample", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	phase, _, _ := unstructured.NestedString(run.Object, "status", "phase")
	if phase != "RequiresReview" || len(jobs.jobs) != 1 {
		t.Fatalf("status = %v, Jobs = %d", run.Object["status"], len(jobs.jobs))
	}
}

func TestRestartUnrecordedJobWithChangedConfigRequiresReview(t *testing.T) {
	controller, jobs, _ := testStepController(t, mockRun("image"), 1)
	reconcileTimes(t, controller, 2)
	run, err := controller.Runs.Get(context.Background(), "sample", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	steps, _, err := unstructured.NestedSlice(run.Object, "status", "steps")
	if err != nil {
		t.Fatal(err)
	}
	delete(steps[0].(map[string]interface{}), "jobConfig")
	if err := unstructured.SetNestedSlice(run.Object, steps, "status", "steps"); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.Runs.UpdateStatus(context.Background(), run, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	restarted := restartedController(controller, true)
	reconcileTimes(t, restarted, 1)
	run, err = restarted.Runs.Get(context.Background(), "sample", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	phase, _, _ := unstructured.NestedString(run.Object, "status", "phase")
	if phase != "RequiresReview" || len(jobs.jobs) != 1 {
		t.Fatalf("status = %v, Jobs = %d", run.Object["status"], len(jobs.jobs))
	}
}

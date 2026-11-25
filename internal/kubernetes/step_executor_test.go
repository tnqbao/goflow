package kubernetes

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tnqbao/kubereel/internal/mock"
	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
)

func mockRun(names ...string) *unstructured.Unstructured {
	steps := make([]interface{}, 0, len(names))
	for _, name := range names {
		steps = append(steps, map[string]interface{}{"name": name, "type": "image", "providerRef": "mock"})
	}
	run := testVideoRun(steps)
	run.SetUID(types.UID("12345678-1234-1234-1234-123456789abc"))
	run.Object["spec"].(map[string]interface{})["inputs"] = map[string]interface{}{"prompt": map[string]interface{}{"stringValue": "A quiet lake"}}
	return run
}

func testStepController(t *testing.T, run *unstructured.Unstructured, maximum int) (*VideoRunController, *testJobs) {
	t.Helper()
	legacy, jobs := testController(t)
	controller, _ := testRunController(run)
	controller.Executor = &StepExecutor{Runs: controller.Runs, Jobs: jobs, Store: legacy.Store, Config: legacy.Config, Artifacts: legacy.Artifacts, MaxConcurrent: maximum, JobLock: &sync.Mutex{}}
	return controller, jobs
}

func TestStepJobRunsMockImage(t *testing.T) {
	ctx := context.Background()
	controller, jobs := testStepController(t, mockRun("image"), 2)
	for i := 0; i < 2; i++ {
		if err := controller.Reconcile(ctx, "sample"); err != nil {
			t.Fatal(err)
		}
	}
	if len(jobs.jobs) != 1 {
		t.Fatalf("Jobs = %d, want 1", len(jobs.jobs))
	}
	var job *batchv1.Job
	for _, job = range jobs.jobs {
	}
	if len(job.OwnerReferences) != 1 || job.OwnerReferences[0].UID != "12345678-1234-1234-1234-123456789abc" {
		t.Fatalf("owner references: %v", job.OwnerReferences)
	}
	id := job.Labels["kubereel.io/workflow"]
	dir := filepath.Join("workflows", id)
	if err := mock.Generate(ctx, controller.Executor.Artifacts, filepath.Join(dir, "request.json"), filepath.Join(dir, "scene.svg")); err != nil {
		t.Fatal(err)
	}
	jobs.finish(id, "worker", batchv1.JobComplete)
	if err := controller.Reconcile(ctx, "sample"); err != nil {
		t.Fatal(err)
	}
	got, err := controller.Runs.Get(ctx, "sample", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	phase, _, _ := unstructured.NestedString(got.Object, "status", "phase")
	artifacts, _, _ := unstructured.NestedStringSlice(got.Object, "status", "artifacts")
	if phase != "Succeeded" || len(artifacts) != 1 || artifacts[0] != filepath.Join("workflows", id, "scene.svg") {
		t.Fatalf("status = %v", got.Object["status"])
	}
	completedAt, found, err := unstructured.NestedString(got.Object, "status", "completedAt")
	if err != nil || !found {
		t.Fatalf("completion time: %q, %v", completedAt, err)
	}
	if _, err := time.Parse(time.RFC3339Nano, completedAt); err != nil {
		t.Fatalf("invalid completion time %q: %v", completedAt, err)
	}
	if err := controller.Reconcile(ctx, "sample"); err != nil {
		t.Fatal(err)
	}
	if len(jobs.jobs) != 1 {
		t.Fatal("reconciliation created a duplicate Job")
	}
}

func TestStepExecutorParallelAndLimit(t *testing.T) {
	ctx := context.Background()
	controller, jobs := testStepController(t, mockRun("first", "second"), 2)
	for i := 0; i < 4; i++ {
		if err := controller.Reconcile(ctx, "sample"); err != nil {
			t.Fatal(err)
		}
	}
	if len(jobs.jobs) != 2 {
		t.Fatalf("Jobs = %d, want 2", len(jobs.jobs))
	}
	controller, jobs = testStepController(t, mockRun("first", "second"), 1)
	for i := 0; i < 4; i++ {
		if err := controller.Reconcile(ctx, "sample"); err != nil {
			t.Fatal(err)
		}
	}
	if len(jobs.jobs) != 1 {
		t.Fatalf("Jobs = %d, want 1 with limit", len(jobs.jobs))
	}
	run := mockRun("first", "second")
	run.Object["spec"].(map[string]interface{})["execution"] = map[string]interface{}{"maxConcurrentSteps": int64(1)}
	controller, jobs = testStepController(t, run, 2)
	for i := 0; i < 4; i++ {
		if err := controller.Reconcile(ctx, "sample"); err != nil {
			t.Fatal(err)
		}
	}
	if len(jobs.jobs) != 1 {
		t.Fatalf("Jobs = %d, want 1 with run limit", len(jobs.jobs))
	}
}

func TestStepExecutorAccountsForLegacyWork(t *testing.T) {
	ctx := context.Background()
	controller, jobs := testStepController(t, mockRun("image"), 1)
	legacy, err := controller.Executor.Store.Create(ctx, "legacy")
	if err != nil {
		t.Fatal(err)
	}
	if err := controller.Executor.Store.Transition(ctx, legacy.ID, "queued", "running", "", ""); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := controller.Reconcile(ctx, "sample"); err != nil {
			t.Fatal(err)
		}
	}
	if len(jobs.jobs) != 0 {
		t.Fatal("started a VideoRun Job beyond the shared limit")
	}
}

func TestStepJobConfigurationAndIdentity(t *testing.T) {
	cfg := JobConfig{Image: "example/kubereel:v1", Claim: "data", Instance: "test"}
	job, err := StepJob("run", types.UID("12345678-1234-1234-1234-123456789abc"), "step", 1, cfg)
	if err != nil {
		t.Fatal(err)
	}
	step := executableStep{TimeoutSeconds: 60}
	step.Resources.Requests = map[string]string{"cpu": "200m"}
	if err := configureStepJob(job, step); err != nil {
		t.Fatal(err)
	}
	if *job.Spec.ActiveDeadlineSeconds != 60 || job.Spec.Template.Spec.Containers[0].Resources.Requests.Cpu().String() != "200m" {
		t.Fatalf("unexpected job configuration: %v", job.Spec)
	}
	if err := validateStepJob(job.DeepCopy(), job); err != nil {
		t.Fatalf("matching Job: %v", err)
	}
	tampered := job.DeepCopy()
	tampered.Spec.Template.Spec.Containers[0].Command = []string{"sh"}
	if err := validateStepJob(tampered, job); err == nil {
		t.Fatal("accepted a Job with a modified command")
	}
	step.Resources.Requests["cpu"] = "2"
	if err := configureStepJob(job.DeepCopy(), step); err == nil {
		t.Fatal("accepted a request larger than the limit")
	}
}

func TestStepExecutorMissingJobRequiresReview(t *testing.T) {
	ctx := context.Background()
	controller, jobs := testStepController(t, mockRun("image"), 1)
	for i := 0; i < 2; i++ {
		if err := controller.Reconcile(ctx, "sample"); err != nil {
			t.Fatal(err)
		}
	}
	for name := range jobs.jobs {
		delete(jobs.jobs, name)
	}
	if err := controller.Reconcile(ctx, "sample"); err != nil {
		t.Fatal(err)
	}
	got, err := controller.Runs.Get(ctx, "sample", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	phase, _, _ := unstructured.NestedString(got.Object, "status", "phase")
	message, _, _ := unstructured.NestedString(got.Object, "status", "error")
	if phase != "RequiresReview" || !strings.Contains(message, "missing") || len(jobs.jobs) != 0 {
		t.Fatalf("status = %v", got.Object["status"])
	}
}

func TestStepExecutorUncertainSubmissionDoesNotRetry(t *testing.T) {
	ctx := context.Background()
	controller, jobs := testStepController(t, mockRun("image"), 1)
	if err := controller.Reconcile(ctx, "sample"); err != nil {
		t.Fatal(err)
	}
	jobs.denied = true
	if err := controller.Reconcile(ctx, "sample"); err == nil {
		t.Fatal("expected Job creation error")
	}
	jobs.denied = false
	if err := controller.Reconcile(ctx, "sample"); err != nil {
		t.Fatal(err)
	}
	got, err := controller.Runs.Get(ctx, "sample", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	phase, _, _ := unstructured.NestedString(got.Object, "status", "phase")
	if phase != "RequiresReview" || len(jobs.jobs) != 0 {
		t.Fatalf("status = %v, Jobs = %d", got.Object["status"], len(jobs.jobs))
	}
}

func TestStepExecutorRejectsMissingArtifact(t *testing.T) {
	ctx := context.Background()
	controller, jobs := testStepController(t, mockRun("image"), 1)
	for i := 0; i < 2; i++ {
		if err := controller.Reconcile(ctx, "sample"); err != nil {
			t.Fatal(err)
		}
	}
	for _, job := range jobs.jobs {
		jobs.finish(job.Labels["kubereel.io/workflow"], "worker", batchv1.JobComplete)
	}
	if err := controller.Reconcile(ctx, "sample"); err != nil {
		t.Fatal(err)
	}
	got, err := controller.Runs.Get(ctx, "sample", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	phase, _, _ := unstructured.NestedString(got.Object, "status", "phase")
	if phase != "Failed" {
		t.Fatalf("status = %v, want Failed", got.Object["status"])
	}
}

func TestStepExecutorRejectsMissingPrompt(t *testing.T) {
	ctx := context.Background()
	run := mockRun("image")
	delete(run.Object["spec"].(map[string]interface{}), "inputs")
	controller, jobs := testStepController(t, run, 1)
	for i := 0; i < 2; i++ {
		if err := controller.Reconcile(ctx, "sample"); err != nil {
			t.Fatal(err)
		}
	}
	got, err := controller.Runs.Get(ctx, "sample", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	phase, _, _ := unstructured.NestedString(got.Object, "status", "phase")
	if phase != "Failed" || len(jobs.jobs) != 0 {
		t.Fatalf("status = %v, Jobs = %d", got.Object["status"], len(jobs.jobs))
	}
}

func TestLegacyControllerAccountsForVideoRunJobs(t *testing.T) {
	ctx := context.Background()
	controller, jobs := testController(t)
	job, err := StepJob("run", types.UID("12345678-1234-1234-1234-123456789abc"), "image", 1, controller.Config)
	if err != nil {
		t.Fatal(err)
	}
	jobs.jobs[job.Name] = job
	legacy, err := controller.Store.Create(ctx, "legacy")
	if err != nil {
		t.Fatal(err)
	}
	if err := controller.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := controller.Store.Get(ctx, legacy.ID)
	if err != nil || got.Status != "queued" {
		t.Fatalf("legacy workflow = %+v, error = %v", got, err)
	}
}

func TestSharedJobLimitDuringConcurrentReconciliation(t *testing.T) {
	ctx := context.Background()
	legacy, jobs := testController(t)
	lock := &sync.Mutex{}
	legacy.JobLock = lock
	runs, _ := testRunController(mockRun("image"))
	runs.Executor = &StepExecutor{Runs: runs.Runs, Jobs: jobs, Store: legacy.Store, Config: legacy.Config, Artifacts: legacy.Artifacts, MaxConcurrent: 1, JobLock: lock}
	if _, err := legacy.Store.Create(ctx, "legacy"); err != nil {
		t.Fatal(err)
	}
	if err := runs.Reconcile(ctx, "sample"); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	go func() { <-start; results <- legacy.Reconcile(ctx) }()
	go func() { <-start; results <- runs.Reconcile(ctx, "sample") }()
	close(start)
	for i := 0; i < 2; i++ {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	if len(jobs.jobs) != 1 {
		t.Fatalf("Jobs = %d, want 1 with shared limit", len(jobs.jobs))
	}
}

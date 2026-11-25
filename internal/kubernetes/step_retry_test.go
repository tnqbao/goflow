package kubernetes

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"testing"

	"github.com/tnqbao/kubereel/internal/mock"
	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
)

func retryRun() *unstructured.Unstructured {
	run := mockRun("image")
	spec := run.Object["spec"].(map[string]interface{})
	snapshot := spec["workflowSnapshot"].(map[string]interface{})
	step := snapshot["steps"].([]interface{})[0].(map[string]interface{})
	step["retry"] = map[string]interface{}{"maxAttempts": int64(2)}
	return run
}

func TestRetryJobIdentityPreservesFirstAttempt(t *testing.T) {
	uid := types.UID("12345678-1234-1234-1234-123456789abc")
	cfg := JobConfig{Image: "image:v1", Claim: "data", Instance: "test"}
	first, err := StepJob("run", uid, "image", 1, cfg)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256([]byte(string(uid) + "\x00image"))
	if first.Name != "kubereel-"+hex.EncodeToString(hash[:16])+"-worker" {
		t.Fatalf("first Job name = %q", first.Name)
	}
	second, err := StepJob("run", uid, "image", 2, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if second.Name == first.Name {
		t.Fatal("retry reused the first Job name")
	}
	for _, attempt := range []int64{0, 101} {
		if _, err := StepJob("run", uid, "image", attempt, cfg); err == nil {
			t.Fatalf("accepted attempt %d", attempt)
		}
	}
}

func TestRetryKnownFailedJob(t *testing.T) {
	controller, jobs := testStepController(t, retryRun(), 1)
	reconcileTimes(t, controller, 2)
	var firstID string
	for _, job := range jobs.jobs {
		firstID = job.Labels["kubereel.io/workflow"]
		jobs.finish(firstID, "worker", batchv1.JobFailed)
	}
	reconcileTimes(t, controller, 2)
	if len(jobs.jobs) != 2 {
		t.Fatalf("Jobs = %d, want two attempts", len(jobs.jobs))
	}
	for _, job := range jobs.jobs {
		id := job.Labels["kubereel.io/workflow"]
		if id == firstID {
			continue
		}
		dir := filepath.Join("workflows", id)
		if err := mock.Generate(context.Background(), controller.Executor.Artifacts, filepath.Join(dir, "request.json"), filepath.Join(dir, "scene.svg")); err != nil {
			t.Fatal(err)
		}
		jobs.finish(id, "worker", batchv1.JobComplete)
	}
	reconcileTimes(t, controller, 1)
	run, err := controller.Runs.Get(context.Background(), "sample", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	phase, _, _ := unstructured.NestedString(run.Object, "status", "phase")
	steps, _, _ := unstructured.NestedSlice(run.Object, "status", "steps")
	if phase != "Succeeded" || steps[0].(map[string]interface{})["attempts"] != int64(2) {
		t.Fatalf("status = %v", run.Object["status"])
	}
}

func TestRetryStopsAtMaximum(t *testing.T) {
	controller, jobs := testStepController(t, retryRun(), 1)
	reconcileTimes(t, controller, 2)
	for _, job := range jobs.jobs {
		jobs.finish(job.Labels["kubereel.io/workflow"], "worker", batchv1.JobFailed)
	}
	reconcileTimes(t, controller, 2)
	for _, job := range jobs.jobs {
		jobs.finish(job.Labels["kubereel.io/workflow"], "worker", batchv1.JobFailed)
	}
	reconcileTimes(t, controller, 2)
	run, err := controller.Runs.Get(context.Background(), "sample", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	phase, _, _ := unstructured.NestedString(run.Object, "status", "phase")
	if phase != "Failed" || len(jobs.jobs) != 2 {
		t.Fatalf("status = %v, Jobs = %d", run.Object["status"], len(jobs.jobs))
	}
}

package kubernetes

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tnqbao/kubereel/internal/mock"
	"github.com/tnqbao/kubereel/internal/renderer"
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
	controller.Executor = &StepExecutor{Runs: controller.Runs, Jobs: jobs, Workflows: legacy.Workflows, Config: legacy.Config, Artifacts: legacy.Artifacts, MaxConcurrent: maximum, JobLock: &sync.Mutex{}}
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
	legacy, err := controller.Executor.Workflows.Create(ctx, "legacy", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := controller.Executor.Workflows.Transition(ctx, legacy.ID, "queued", "running", "", ""); err != nil {
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
	legacy, err := controller.Workflows.Create(ctx, "legacy", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := controller.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := controller.Workflows.Get(ctx, legacy.ID)
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
	runs.Executor = &StepExecutor{Runs: runs.Runs, Jobs: jobs, Workflows: legacy.Workflows, Config: legacy.Config, Artifacts: legacy.Artifacts, MaxConcurrent: 1, JobLock: lock}
	if _, err := legacy.Workflows.Create(ctx, "legacy", ""); err != nil {
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

func testValidMP4() []byte {
	var buf bytes.Buffer
	writeBox := func(boxType string, payload []byte) {
		var header [8]byte
		binary.BigEndian.PutUint32(header[:4], uint32(8+len(payload)))
		copy(header[4:8], boxType)
		buf.Write(header[:])
		buf.Write(payload)
	}
	writeBox("ftyp", []byte("isom\x00\x00\x02\x00isomiso2mp41"))
	writeBox("moov", []byte("movie-header"))
	writeBox("mdat", []byte("video-payload"))
	return buf.Bytes()
}

func TestStepExecutorRunsChainedFFmpegStepsAndCleanup(t *testing.T) {
	ctx := context.Background()
	steps := []interface{}{
		map[string]interface{}{
			"name": "trim",
			"type": "ffmpeg",
			"ffmpeg": map[string]interface{}{
				"operation":       renderer.OpTrim,
				"inputs":          []interface{}{"inputs/source.mp4"},
				"startSeconds":    float64(0),
				"durationSeconds": float64(1.0),
			},
		},
		map[string]interface{}{
			"name":      "resize",
			"type":      "ffmpeg",
			"dependsOn": []interface{}{"trim"},
			"ffmpeg": map[string]interface{}{
				"operation": renderer.OpResize,
				"width":     int64(640),
				"height":    int64(360),
			},
		},
	}
	run := testVideoRun(steps)
	run.SetUID(types.UID("12345678-1234-1234-1234-123456789abc"))
	run.Object["spec"].(map[string]interface{})["workflowSnapshot"].(map[string]interface{})["outputs"] = []interface{}{
		map[string]interface{}{"name": "video", "from": "resize"},
	}

	controller, jobs := testStepController(t, run, 2)
	reconcileTimes(t, controller, 2)
	if len(jobs.jobs) != 1 {
		t.Fatalf("Jobs = %d, want 1 for trim step", len(jobs.jobs))
	}

	var trimJob *batchv1.Job
	for _, trimJob = range jobs.jobs {
	}
	if trimJob.Labels["kubereel.io/stage"] != StageRender || trimJob.Spec.Template.Spec.Containers[0].Args[0] != StageRender {
		t.Fatalf("unexpected render Job: %+v", trimJob)
	}

	trimID := trimJob.Labels["kubereel.io/workflow"]
	trimOut := filepath.ToSlash(filepath.Join("workflows", trimID, "output.mp4"))
	if err := controller.Executor.Artifacts.Put(ctx, trimOut, bytes.NewReader(testValidMP4())); err != nil {
		t.Fatal(err)
	}
	jobs.finish(trimID, StageRender, batchv1.JobComplete)

	reconcileTimes(t, controller, 3)
	if len(jobs.jobs) != 2 {
		t.Fatalf("Jobs = %d, want 2 after resize submission", len(jobs.jobs))
	}

	var resizeJob *batchv1.Job
	for _, candidate := range jobs.jobs {
		if candidate.Name != trimJob.Name {
			resizeJob = candidate
		}
	}
	resizeID := resizeJob.Labels["kubereel.io/workflow"]
	renderSpecObj, err := controller.Executor.Artifacts.Open(ctx, filepath.ToSlash(filepath.Join("workflows", resizeID, "render.json")))
	if err != nil {
		t.Fatal(err)
	}
	renderSpecRaw, err := io.ReadAll(renderSpecObj)
	_ = renderSpecObj.Close()
	if err != nil {
		t.Fatal(err)
	}

	var resolved renderer.Request
	if err := json.Unmarshal(renderSpecRaw, &resolved); err != nil {
		t.Fatal(err)
	}
	if len(resolved.Inputs) != 1 || resolved.Inputs[0] != trimOut {
		t.Fatalf("resolved resize inputs = %v, want [%s]", resolved.Inputs, trimOut)
	}

	resizeOut := filepath.ToSlash(filepath.Join("workflows", resizeID, "output.mp4"))
	if err := controller.Executor.Artifacts.Put(ctx, resizeOut, bytes.NewReader(testValidMP4())); err != nil {
		t.Fatal(err)
	}
	jobs.finish(resizeID, StageRender, batchv1.JobComplete)
	reconcileTimes(t, controller, 1)

	got, err := controller.Runs.Get(ctx, "sample", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	phase, _, _ := unstructured.NestedString(got.Object, "status", "phase")
	artifacts, _, _ := unstructured.NestedStringSlice(got.Object, "status", "artifacts")
	if phase != "Succeeded" || len(artifacts) != 2 {
		t.Fatalf("status = %v", got.Object["status"])
	}
	if err := controller.Executor.cleanupArtifacts(ctx, got, time.Now().Add(time.Minute)); err != nil {
		t.Fatalf("cleanupArtifacts = %v", err)
	}
}

func TestStepExecutorRejectsInvalidFFmpegStep(t *testing.T) {
	cases := []map[string]interface{}{
		{"name": "missing-cfg", "type": "ffmpeg"},
		{
			"name":        "with-provider",
			"type":        "ffmpeg",
			"providerRef": "mock",
			"ffmpeg":      map[string]interface{}{"operation": renderer.OpNormalize, "inputs": []interface{}{"a.mp4"}},
		},
		{
			"name": "bad-db-input",
			"type": "ffmpeg",
			"ffmpeg": map[string]interface{}{
				"operation": renderer.OpNormalize,
				"inputs":    []interface{}{"kubereel.db"},
			},
		},
		{
			"name": "odd-width",
			"type": "ffmpeg",
			"ffmpeg": map[string]interface{}{
				"operation": renderer.OpResize,
				"inputs":    []interface{}{"a.mp4"},
				"width":     int64(641),
				"height":    int64(360),
			},
		},
	}

	for _, step := range cases {
		run := testVideoRun([]interface{}{step})
		run.SetUID(types.UID("12345678-1234-1234-1234-123456789abc"))
		controller, jobs := testStepController(t, run, 1)
		reconcileTimes(t, controller, 2)

		got, err := controller.Runs.Get(context.Background(), "sample", metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		phase, _, _ := unstructured.NestedString(got.Object, "status", "phase")
		if phase != "Failed" || len(jobs.jobs) != 0 {
			t.Fatalf("step %v: phase = %q, jobs = %d", step["name"], phase, len(jobs.jobs))
		}
	}
}

func TestCancelRunningFFmpegStep(t *testing.T) {
	step := map[string]interface{}{
		"name": "encode",
		"type": "ffmpeg",
		"ffmpeg": map[string]interface{}{
			"operation": renderer.OpNormalize,
			"inputs":    []interface{}{"inputs/clip.mp4"},
		},
	}
	run := testVideoRun([]interface{}{step})
	run.SetUID(types.UID("12345678-1234-1234-1234-123456789abc"))

	controller, jobs := testStepController(t, run, 1)
	reconcileTimes(t, controller, 2)
	if len(jobs.jobs) != 1 {
		t.Fatalf("Jobs = %d, want 1", len(jobs.jobs))
	}

	requestCancellation(t, controller)
	reconcileTimes(t, controller, 4)

	got, err := controller.Runs.Get(context.Background(), "sample", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	phase, _, _ := unstructured.NestedString(got.Object, "status", "phase")
	if phase != "Cancelled" || len(jobs.jobs) != 0 {
		t.Fatalf("status = %v, Jobs = %d", got.Object["status"], len(jobs.jobs))
	}
}

func TestStepExecutorEndToEndMockWorkflowDAG(t *testing.T) {
	ctx := context.Background()
	steps := []interface{}{
		map[string]interface{}{"name": "script", "type": "llm", "providerRef": "mock"},
		map[string]interface{}{"name": "image-1", "type": "image", "providerRef": "mock", "dependsOn": []interface{}{"script"}},
		map[string]interface{}{"name": "image-2", "type": "image", "providerRef": "mock", "dependsOn": []interface{}{"script"}},
		map[string]interface{}{"name": "video-1", "type": "video", "providerRef": "mock", "dependsOn": []interface{}{"image-1"}},
		map[string]interface{}{"name": "video-2", "type": "video", "providerRef": "mock", "dependsOn": []interface{}{"image-2"}},
		map[string]interface{}{
			"name":      "render",
			"type":      "ffmpeg",
			"dependsOn": []interface{}{"video-1", "video-2"},
			"ffmpeg": map[string]interface{}{
				"operation":   renderer.OpRenderMP4,
				"aspectRatio": "9:16",
			},
		},
	}

	run := testVideoRun(steps)
	run.SetUID(types.UID("abcdef01-2345-6789-abcd-ef0123456789"))
	spec := run.Object["spec"].(map[string]interface{})
	spec["inputs"] = map[string]interface{}{
		"topic": map[string]interface{}{"stringValue": "Unknown creatures in the forest"},
		"style": map[string]interface{}{"stringValue": "Found footage horror"},
	}
	spec["workflowSnapshot"].(map[string]interface{})["outputs"] = []interface{}{
		map[string]interface{}{"name": "final-video", "from": "render"},
	}

	controller, jobs := testStepController(t, run, 4)

	// 1. Submit and execute the Mock LLM script step.
	reconcileTimes(t, controller, 2)
	if len(jobs.jobs) != 1 {
		t.Fatalf("Jobs after script submit = %d, want 1", len(jobs.jobs))
	}
	for _, job := range jobs.jobs {
		id := job.Labels["kubereel.io/workflow"]
		dir := filepath.ToSlash(filepath.Join("workflows", id))
		if err := mock.Execute(ctx, controller.Executor.Artifacts, dir+"/request.json", dir+"/script.json"); err != nil {
			t.Fatal(err)
		}
		jobs.finish(id, StageWorker, batchv1.JobComplete)
	}

	// 2. Both independent Mock image steps become ready and submit concurrently.
	reconcileTimes(t, controller, 5)
	activeImageJobs := 0
	for _, job := range jobs.jobs {
		if !isJobTerminal(job) {
			activeImageJobs++
			id := job.Labels["kubereel.io/workflow"]
			dir := filepath.ToSlash(filepath.Join("workflows", id))
			if err := mock.Execute(ctx, controller.Executor.Artifacts, dir+"/request.json", dir+"/scene.svg"); err != nil {
				t.Fatal(err)
			}
			jobs.finish(id, StageWorker, batchv1.JobComplete)
		}
	}
	if activeImageJobs != 2 {
		t.Fatalf("concurrent image Jobs = %d, want 2", activeImageJobs)
	}

	// 3. Both independent Mock video steps become ready and submit concurrently.
	reconcileTimes(t, controller, 6)
	activeVideoJobs := 0
	for _, job := range jobs.jobs {
		if !isJobTerminal(job) {
			activeVideoJobs++
			id := job.Labels["kubereel.io/workflow"]
			dir := filepath.ToSlash(filepath.Join("workflows", id))
			if err := controller.Executor.Artifacts.Put(ctx, dir+"/clip.mp4", bytes.NewReader(testValidMP4())); err != nil {
				t.Fatal(err)
			}
			jobs.finish(id, StageWorker, batchv1.JobComplete)
		}
	}
	if activeVideoJobs != 2 {
		t.Fatalf("concurrent video Jobs = %d, want 2", activeVideoJobs)
	}

	// 4. Final FFmpeg render step resolves both upstream video clips and completes the run.
	reconcileTimes(t, controller, 5)
	var renderJob *batchv1.Job
	for _, job := range jobs.jobs {
		if !isJobTerminal(job) {
			renderJob = job
		}
	}
	if renderJob == nil || renderJob.Labels["kubereel.io/stage"] != StageRender {
		t.Fatalf("expected active render Job, got %+v", renderJob)
	}

	renderID := renderJob.Labels["kubereel.io/workflow"]
	renderDir := filepath.ToSlash(filepath.Join("workflows", renderID))
	specObj, err := controller.Executor.Artifacts.Open(ctx, renderDir+"/render.json")
	if err != nil {
		t.Fatal(err)
	}
	rawSpec, err := io.ReadAll(specObj)
	_ = specObj.Close()
	if err != nil {
		t.Fatal(err)
	}
	var resolved renderer.Request
	if err := json.Unmarshal(rawSpec, &resolved); err != nil {
		t.Fatal(err)
	}
	if len(resolved.Inputs) != 2 || resolved.AspectRatio != "9:16" {
		t.Fatalf("resolved FFmpeg request = %+v", resolved)
	}

	if err := controller.Executor.Artifacts.Put(ctx, renderDir+"/output.mp4", bytes.NewReader(testValidMP4())); err != nil {
		t.Fatal(err)
	}
	jobs.finish(renderID, StageRender, batchv1.JobComplete)
	reconcileTimes(t, controller, 1)

	got, err := controller.Runs.Get(ctx, "sample", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	phase, _, _ := unstructured.NestedString(got.Object, "status", "phase")
	artifacts, _, _ := unstructured.NestedStringSlice(got.Object, "status", "artifacts")
	if phase != "Succeeded" || len(artifacts) != 6 {
		t.Fatalf("final status = %v", got.Object["status"])
	}
	if err := controller.Executor.cleanupArtifacts(ctx, got, time.Now().Add(time.Minute)); err != nil {
		t.Fatalf("cleanupArtifacts = %v", err)
	}
}

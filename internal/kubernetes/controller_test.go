package kubernetes

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"testing"

	"github.com/tnqbao/kubereel/internal/database"
	"github.com/tnqbao/kubereel/internal/mock"
	"github.com/tnqbao/kubereel/internal/provider"
	"github.com/tnqbao/kubereel/internal/renderer"
	"github.com/tnqbao/kubereel/internal/repository"
	"github.com/tnqbao/kubereel/internal/service"
	"github.com/tnqbao/kubereel/internal/storage"
	"github.com/tnqbao/kubereel/internal/workflow"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
)

type testJobs struct {
	jobs           map[string]*batchv1.Job
	denied         bool
	deletePolicies []metav1.DeletionPropagation
}

func (j *testJobs) List(_ context.Context, options metav1.ListOptions) (*batchv1.JobList, error) {
	selector, err := labels.Parse(options.LabelSelector)
	if err != nil {
		return nil, err
	}
	result := &batchv1.JobList{}
	for _, job := range j.jobs {
		if selector.Matches(labels.Set(job.Labels)) {
			result.Items = append(result.Items, *job.DeepCopy())
		}
	}
	return result, nil
}

func (j *testJobs) Watch(_ context.Context, _ metav1.ListOptions) (watch.Interface, error) {
	return watch.NewFake(), nil
}

func (j *testJobs) Create(_ context.Context, job *batchv1.Job, _ metav1.CreateOptions) (*batchv1.Job, error) {
	if j.denied {
		return nil, fmt.Errorf("denied")
	}
	if _, ok := j.jobs[job.Name]; ok {
		return nil, apierrors.NewAlreadyExists(schema.GroupResource{Resource: "jobs"}, job.Name)
	}
	j.jobs[job.Name] = job.DeepCopy()
	return job.DeepCopy(), nil
}
func (j *testJobs) Get(_ context.Context, name string, _ metav1.GetOptions) (*batchv1.Job, error) {
	job, ok := j.jobs[name]
	if !ok {
		return nil, apierrors.NewNotFound(schema.GroupResource{Resource: "jobs"}, name)
	}
	return job.DeepCopy(), nil
}
func (j *testJobs) Delete(_ context.Context, name string, options metav1.DeleteOptions) error {
	if _, ok := j.jobs[name]; !ok {
		return apierrors.NewNotFound(schema.GroupResource{Resource: "jobs"}, name)
	}
	if options.PropagationPolicy != nil {
		j.deletePolicies = append(j.deletePolicies, *options.PropagationPolicy)
	}
	delete(j.jobs, name)
	return nil
}
func (j *testJobs) finish(id, stage string, condition batchv1.JobConditionType) {
	job := j.jobs["kubereel-"+id+"-"+stage]
	job.Status.Conditions = []batchv1.JobCondition{{Type: condition, Status: corev1.ConditionTrue}}
}
func testController(t *testing.T) (*Controller, *testJobs) {
	t.Helper()
	dir := t.TempDir()
	db, err := database.Open(context.Background(), filepath.Join(dir, "db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	artifacts, err := storage.NewFilesystem(dir, 1<<20, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := artifacts.Close(); err != nil {
			t.Error(err)
		}
	})
	jobs := &testJobs{jobs: make(map[string]*batchv1.Job)}
	workflows := service.NewWorkflowService(
		repository.NewWorkflowRepository(db),
		artifacts,
		service.NewProviderService(provider.NewManager()),
	)
	return &Controller{Workflows: workflows, Jobs: jobs, Config: JobConfig{Image: "test/image:v1", Claim: "data", Instance: "test"}, Artifacts: artifacts, MaxConcurrent: 1}, jobs
}

func TestReconcileMockWorkflow(t *testing.T) {
	ctx := context.Background()
	controller, jobs := testController(t)
	w, err := controller.Workflows.Create(ctx, "test scene", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := controller.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	root := filepath.ToSlash(filepath.Join("workflows", w.ID))
	reqObj, err := controller.Artifacts.Open(ctx, root+"/request.json")
	if err != nil {
		t.Fatal(err)
	}
	reqBytes, err := io.ReadAll(reqObj)
	_ = reqObj.Close()
	if err != nil {
		t.Fatal(err)
	}
	var workerReq mock.Request
	if err := json.Unmarshal(reqBytes, &workerReq); err != nil || workerReq.Type != mock.StepVideo || workerReq.Prompt != "test scene" {
		t.Fatalf("worker request = %+v, %v", workerReq, err)
	}

	renderObj, err := controller.Artifacts.Open(ctx, root+"/render.json")
	if err != nil {
		t.Fatal(err)
	}
	renderBytes, err := io.ReadAll(renderObj)
	_ = renderObj.Close()
	if err != nil {
		t.Fatal(err)
	}
	var renderReq renderer.Request
	if err := json.Unmarshal(renderBytes, &renderReq); err != nil || renderReq.Operation != renderer.OpRenderMP4 {
		t.Fatalf("render request = %+v, %v", renderReq, err)
	}

	if err := controller.Artifacts.Put(ctx, root+"/clip.mp4", bytes.NewReader(testValidMP4())); err != nil {
		t.Fatal(err)
	}
	jobs.finish(w.ID, "worker", batchv1.JobComplete)
	if err := controller.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if err := controller.Artifacts.Put(ctx, root+"/final.mp4", bytes.NewReader(testValidMP4())); err != nil {
		t.Fatal(err)
	}
	jobs.finish(w.ID, "render", batchv1.JobComplete)
	if err := controller.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := controller.Workflows.Get(ctx, w.ID)
	if err != nil || got.Status != "succeeded" || got.Result != filepath.Join("workflows", w.ID, "final.mp4") {
		t.Fatalf("workflow = %+v, %v", got, err)
	}
	if err := controller.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if len(jobs.jobs) != 0 {
		t.Fatal("completed workflow jobs were not removed")
	}

	bad, err := controller.Workflows.Create(ctx, "corrupt render", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := controller.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	jobs.finish(bad.ID, "worker", batchv1.JobComplete)
	if err := controller.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	badRoot := filepath.ToSlash(filepath.Join("workflows", bad.ID))
	if err := controller.Artifacts.Put(ctx, badRoot+"/final.mp4", bytes.NewReader([]byte("not-mp4"))); err != nil {
		t.Fatal(err)
	}
	jobs.finish(bad.ID, "render", batchv1.JobComplete)
	if err := controller.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	gotBad, err := controller.Workflows.Get(ctx, bad.ID)
	if err != nil || gotBad.Status != "failed" || gotBad.Error != "render output is invalid" {
		t.Fatalf("corrupt workflow = %+v, %v", gotBad, err)
	}
}

func TestReconcileErrorsAndConcurrency(t *testing.T) {
	ctx := context.Background()
	controller, jobs := testController(t)
	first, err := controller.Workflows.Create(ctx, "first", "")
	if err != nil {
		t.Fatal(err)
	}
	second, err := controller.Workflows.Create(ctx, "second", "")
	if err != nil {
		t.Fatal(err)
	}
	jobs.denied = true
	if err := controller.Reconcile(ctx); err == nil {
		t.Fatal("expected job creation failure")
	}
	got, err := controller.Workflows.Get(ctx, first.ID)
	if err != nil || got.Status != "queued" {
		t.Fatalf("workflow = %+v, %v", got, err)
	}
	jobs.denied = false
	if err := controller.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	got, err = controller.Workflows.Get(ctx, second.ID)
	if err != nil || got.Status != "queued" {
		t.Fatalf("concurrency limit not enforced: %+v, %v", got, err)
	}
	jobs.finish(first.ID, "worker", batchv1.JobFailed)
	if err := controller.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	got, err = controller.Workflows.Get(ctx, first.ID)
	if err != nil || got.Status != "failed" {
		t.Fatalf("failed job = %+v, %v", got, err)
	}
	if err := controller.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	got, err = controller.Workflows.Get(ctx, second.ID)
	if err != nil || got.Status != "running" {
		t.Fatalf("next job = %+v, %v", got, err)
	}
	if err := controller.Workflows.Transition(ctx, second.ID, "queued", "running", "", ""); !errors.Is(err, workflow.ErrConflict) {
		t.Fatalf("expected conflict: %v", err)
	}
}

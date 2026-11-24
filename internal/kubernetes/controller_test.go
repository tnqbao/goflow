package kubernetes

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/tnqbao/kubereel/internal/database"
	"github.com/tnqbao/kubereel/internal/mock"
	"github.com/tnqbao/kubereel/internal/workflow"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

type testJobs struct {
	jobs   map[string]*batchv1.Job
	denied bool
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
	jobs := &testJobs{jobs: make(map[string]*batchv1.Job)}
	return &Controller{Store: workflow.NewStore(db), Jobs: jobs, Config: JobConfig{Image: "test/image:v1", Claim: "data", Instance: "test"}, DataDir: dir, MaxConcurrent: 1}, jobs
}

func TestReconcileMockWorkflow(t *testing.T) {
	ctx := context.Background()
	controller, jobs := testController(t)
	w, err := controller.Store.Create(ctx, "test scene")
	if err != nil {
		t.Fatal(err)
	}
	if err := controller.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(controller.DataDir, "workflows", w.ID)
	if err := mock.Generate(ctx, filepath.Join(root, "request.json"), filepath.Join(root, "scene.svg")); err != nil {
		t.Fatal(err)
	}
	jobs.finish(w.ID, "worker", batchv1.JobComplete)
	if err := controller.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if err := mock.Render(ctx, filepath.Join(root, "scene.svg"), filepath.Join(root, "final.svg")); err != nil {
		t.Fatal(err)
	}
	jobs.finish(w.ID, "render", batchv1.JobComplete)
	if err := controller.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := controller.Store.Get(ctx, w.ID)
	if err != nil || got.Status != "succeeded" || got.Result == "" {
		t.Fatalf("workflow = %+v, %v", got, err)
	}
	if err := controller.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestReconcileErrorsAndConcurrency(t *testing.T) {
	ctx := context.Background()
	controller, jobs := testController(t)
	first, err := controller.Store.Create(ctx, "first")
	if err != nil {
		t.Fatal(err)
	}
	second, err := controller.Store.Create(ctx, "second")
	if err != nil {
		t.Fatal(err)
	}
	jobs.denied = true
	if err := controller.Reconcile(ctx); err == nil {
		t.Fatal("expected job creation failure")
	}
	got, err := controller.Store.Get(ctx, first.ID)
	if err != nil || got.Status != "queued" {
		t.Fatalf("workflow = %+v, %v", got, err)
	}
	jobs.denied = false
	if err := controller.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	got, err = controller.Store.Get(ctx, second.ID)
	if err != nil || got.Status != "queued" {
		t.Fatalf("concurrency limit not enforced: %+v, %v", got, err)
	}
	jobs.finish(first.ID, "worker", batchv1.JobFailed)
	if err := controller.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	got, err = controller.Store.Get(ctx, first.ID)
	if err != nil || got.Status != "failed" {
		t.Fatalf("failed job = %+v, %v", got, err)
	}
	if err := controller.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	got, err = controller.Store.Get(ctx, second.ID)
	if err != nil || got.Status != "running" {
		t.Fatalf("next job = %+v, %v", got, err)
	}
	if err := controller.Store.Transition(ctx, second.ID, "queued", "running", "", ""); !errors.Is(err, workflow.ErrConflict) {
		t.Fatalf("expected conflict: %v", err)
	}
}

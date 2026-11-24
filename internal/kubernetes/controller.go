package kubernetes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/tnqbao/kubereel/internal/mock"
	"github.com/tnqbao/kubereel/internal/workflow"
	batchv1 "k8s.io/api/batch/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type Jobs interface {
	Create(context.Context, *batchv1.Job, metav1.CreateOptions) (*batchv1.Job, error)
	Get(context.Context, string, metav1.GetOptions) (*batchv1.Job, error)
	Delete(context.Context, string, metav1.DeleteOptions) error
}

type Controller struct {
	Store         *workflow.Store
	Jobs          Jobs
	Config        JobConfig
	DataDir       string
	MaxConcurrent int
}

// Run reconciles durable workflow state until the control plane stops.
func (c *Controller) Run(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		if err := c.Reconcile(ctx); err != nil && !errors.Is(err, context.Canceled) {
			slog.Error("workflow reconciliation failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (c *Controller) Reconcile(ctx context.Context) error {
	if c.Store == nil || c.Jobs == nil || c.DataDir == "" || c.MaxConcurrent < 1 {
		return fmt.Errorf("invalid workflow controller configuration")
	}
	active, err := c.Store.Active(ctx)
	if err != nil {
		return err
	}
	running := 0
	for _, w := range active {
		if w.Status == "running" {
			running++
		}
	}
	var failures []error
	for _, w := range active {
		if w.Status == "queued" && running >= c.MaxConcurrent {
			continue
		}
		if err := c.reconcileOne(ctx, w); err != nil {
			failures = append(failures, fmt.Errorf("workflow %s: %w", w.ID, err))
			continue
		}
		if w.Status == "queued" {
			running++
		}
	}
	return errors.Join(failures...)
}

func (c *Controller) reconcileOne(ctx context.Context, w workflow.Workflow) error {
	if w.Status == "queued" {
		if err := c.writeRequest(w); err != nil {
			return err
		}
		if _, err := c.ensureJob(ctx, w.ID, "worker"); err != nil {
			return err
		}
		return c.Store.Transition(ctx, w.ID, "queued", "running", "", "")
	}
	worker, err := c.getJob(ctx, w.ID, "worker")
	if apierrors.IsNotFound(err) {
		return c.fail(ctx, w.ID, "worker job disappeared")
	}
	if err != nil {
		return err
	}
	if finished(worker, batchv1.JobFailed) {
		return c.fail(ctx, w.ID, "worker job failed")
	}
	if !finished(worker, batchv1.JobComplete) {
		return nil
	}
	render, err := c.ensureJob(ctx, w.ID, "render")
	if err != nil {
		return err
	}
	if finished(render, batchv1.JobFailed) {
		return c.fail(ctx, w.ID, "render job failed")
	}
	if !finished(render, batchv1.JobComplete) {
		return nil
	}
	output := filepath.Join(c.DataDir, "workflows", w.ID, "final.svg")
	info, err := os.Stat(output)
	if errors.Is(err, os.ErrNotExist) {
		return c.fail(ctx, w.ID, "render output is missing")
	}
	if err != nil {
		return fmt.Errorf("inspect render output: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() == 0 {
		return c.fail(ctx, w.ID, "render output is invalid")
	}
	if err := c.Store.Transition(ctx, w.ID, "running", "succeeded", filepath.Join("workflows", w.ID, "final.svg"), ""); err != nil {
		return err
	}
	return c.cleanup(ctx, w.ID)
}

func (c *Controller) fail(ctx context.Context, id, message string) error {
	if err := c.Store.Transition(ctx, id, "running", "failed", "", message); err != nil {
		return err
	}
	return c.cleanup(ctx, id)
}

func (c *Controller) cleanup(ctx context.Context, id string) error {
	policy := metav1.DeletePropagationBackground
	var failures []error
	for _, stage := range []string{"worker", "render"} {
		job, err := Job(id, stage, c.Config)
		if err != nil {
			return err
		}
		err = c.Jobs.Delete(ctx, job.Name, metav1.DeleteOptions{PropagationPolicy: &policy})
		if err != nil && !apierrors.IsNotFound(err) {
			failures = append(failures, fmt.Errorf("delete %s job: %w", stage, err))
		}
	}
	return errors.Join(failures...)
}

func finished(job *batchv1.Job, kind batchv1.JobConditionType) bool {
	for _, condition := range job.Status.Conditions {
		if condition.Type == kind && condition.Status == "True" {
			return true
		}
	}
	return false
}

func (c *Controller) getJob(ctx context.Context, id, stage string) (*batchv1.Job, error) {
	expected, err := Job(id, stage, c.Config)
	if err != nil {
		return nil, err
	}
	actual, err := c.Jobs.Get(ctx, expected.Name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	if err := validateJob(actual, expected); err != nil {
		return nil, err
	}
	return actual, nil
}

func (c *Controller) ensureJob(ctx context.Context, id, stage string) (*batchv1.Job, error) {
	job, err := c.getJob(ctx, id, stage)
	if err == nil {
		return job, nil
	}
	if !apierrors.IsNotFound(err) {
		return nil, err
	}
	desired, err := Job(id, stage, c.Config)
	if err != nil {
		return nil, err
	}
	created, err := c.Jobs.Create(ctx, desired, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		return c.getJob(ctx, id, stage)
	}
	if err != nil {
		return nil, fmt.Errorf("create %s job: %w", stage, err)
	}
	return created, nil
}

func validateJob(actual, expected *batchv1.Job) error {
	for key, value := range expected.Labels {
		if actual.Labels[key] != value {
			return fmt.Errorf("job identity mismatch")
		}
	}
	if len(actual.Spec.Template.Spec.Containers) != 1 {
		return fmt.Errorf("job container mismatch")
	}
	container := actual.Spec.Template.Spec.Containers[0]
	wanted := expected.Spec.Template.Spec.Containers[0]
	if container.Image != wanted.Image || len(container.Args) != len(wanted.Args) {
		return fmt.Errorf("job configuration mismatch")
	}
	for i, arg := range wanted.Args {
		if container.Args[i] != arg {
			return fmt.Errorf("job command mismatch")
		}
	}
	return nil
}

func (c *Controller) writeRequest(w workflow.Workflow) error {
	if !workflowID.MatchString(w.ID) {
		return fmt.Errorf("invalid workflow ID")
	}
	dir := filepath.Join(c.DataDir, "workflows", w.ID)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create workflow directory: %w", err)
	}
	data, err := json.Marshal(mock.Request{Prompt: w.Prompt})
	if err != nil {
		return fmt.Errorf("encode workflow request: %w", err)
	}
	file, err := os.CreateTemp(dir, ".request-*")
	if err != nil {
		return fmt.Errorf("create workflow request: %w", err)
	}
	defer os.Remove(file.Name())
	if _, err := file.Write(data); err != nil {
		return errors.Join(err, file.Close())
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close workflow request: %w", err)
	}
	if err := os.Rename(file.Name(), filepath.Join(dir, "request.json")); err != nil {
		return fmt.Errorf("publish workflow request: %w", err)
	}
	return nil
}

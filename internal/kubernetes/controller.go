package kubernetes

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"path"
	"path/filepath"
	"sync"
	"time"

	"github.com/tnqbao/kubereel/internal/mock"
	"github.com/tnqbao/kubereel/internal/renderer"
	"github.com/tnqbao/kubereel/internal/repository"
	"github.com/tnqbao/kubereel/internal/service"
	"github.com/tnqbao/kubereel/internal/storage"
	"github.com/tnqbao/kubereel/internal/workflow"
	batchv1 "k8s.io/api/batch/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
)

const reconcileInterval = 2 * time.Second

// Jobs abstracts the Kubernetes Job client operations used by controllers.
type Jobs interface {
	Create(context.Context, *batchv1.Job, metav1.CreateOptions) (*batchv1.Job, error)
	Get(context.Context, string, metav1.GetOptions) (*batchv1.Job, error)
	Delete(context.Context, string, metav1.DeleteOptions) error
	List(context.Context, metav1.ListOptions) (*batchv1.JobList, error)
	Watch(context.Context, metav1.ListOptions) (watch.Interface, error)
}

// Controller reconciles durable workflow executions and runs the VideoRun controller.
type Controller struct {
	ExecutionRepository *repository.ExecutionRepository
	WorkflowDefinitions *repository.WorkflowDefinitionRepository
	ProviderRepository  *repository.ProviderRepository
	Workflows           *service.WorkflowService
	Jobs                Jobs
	Config              JobConfig
	Artifacts           storage.Store
	MaxConcurrent       int
	VideoRuns           *VideoRunController
	JobLock             *sync.Mutex
}

// Run reconciles durable workflow state until ctx is cancelled.
func (c *Controller) Run(ctx context.Context) {
	if c.VideoRuns != nil {
		done := make(chan struct{})
		go func() {
			defer close(done)
			c.VideoRuns.Run(ctx)
		}()
		defer func() { <-done }()
	}

	ticker := time.NewTicker(reconcileInterval)
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

// Reconcile advances queued and running workflows while respecting MaxConcurrent.
func (c *Controller) Reconcile(ctx context.Context) error {
	if c.Workflows == nil || c.Jobs == nil || c.Artifacts == nil || c.MaxConcurrent < 1 {
		return fmt.Errorf("invalid workflow controller configuration")
	}

	if c.JobLock != nil {
		c.JobLock.Lock()
		defer c.JobLock.Unlock()
	}

	activeVideoRunJobs, err := countActiveVideoRunJobs(ctx, c.Jobs, c.Config.Instance)
	if err != nil {
		return err
	}

	activeWorkflows, err := c.Workflows.Active(ctx)
	if err != nil {
		return err
	}

	runningCount := activeVideoRunJobs + countRunningWorkflows(activeWorkflows)

	var failures []error
	for _, wf := range activeWorkflows {
		if wf.Status == "queued" && runningCount >= c.MaxConcurrent {
			continue
		}

		if err := c.reconcileWorkflow(ctx, wf); err != nil {
			failures = append(failures, fmt.Errorf("workflow %s: %w", wf.ID, err))
			continue
		}

		if wf.Status == "queued" {
			runningCount++
		}
	}

	return errors.Join(failures...)
}

func countRunningWorkflows(workflows []workflow.Workflow) int {
	count := 0
	for _, wf := range workflows {
		if wf.Status == "running" {
			count++
		}
	}

	return count
}

func (c *Controller) reconcileWorkflow(ctx context.Context, wf workflow.Workflow) error {
	if wf.Status == "queued" {
		return c.startWorkflow(ctx, wf)
	}

	workerJob, err := c.getVerifiedJob(ctx, wf.ID, StageWorker)
	if apierrors.IsNotFound(err) {
		return c.failWorkflow(ctx, wf.ID, "worker job disappeared")
	}
	if err != nil {
		return err
	}

	if hasJobCondition(workerJob, batchv1.JobFailed) {
		return c.failWorkflow(ctx, wf.ID, "worker job failed")
	}
	if !hasJobCondition(workerJob, batchv1.JobComplete) {
		return nil
	}

	renderJob, err := c.ensureJob(ctx, wf.ID, StageRender)
	if err != nil {
		return err
	}

	if hasJobCondition(renderJob, batchv1.JobFailed) {
		return c.failWorkflow(ctx, wf.ID, "render job failed")
	}
	if !hasJobCondition(renderJob, batchv1.JobComplete) {
		return nil
	}

	return c.completeWorkflow(ctx, wf.ID)
}

func (c *Controller) completeWorkflow(ctx context.Context, workflowID string) error {
	outputPath := filepath.Join("workflows", workflowID, "final.mp4")

	failureReason, err := c.checkRenderedArtifact(ctx, outputPath)
	if err != nil {
		return err
	}
	if failureReason != "" {
		return c.failWorkflow(ctx, workflowID, failureReason)
	}

	if err := c.Workflows.Transition(ctx, workflowID, "running", "succeeded", outputPath, ""); err != nil {
		return err
	}

	return c.cleanupWorkflowJobs(ctx, workflowID)
}

func (c *Controller) checkRenderedArtifact(ctx context.Context, outputPath string) (string, error) {
	key := filepath.ToSlash(outputPath)
	object, err := c.Artifacts.Open(ctx, key)
	if errors.Is(err, fs.ErrNotExist) {
		return "render output is missing", nil
	}
	if err != nil {
		return "", fmt.Errorf("inspect render output: %w", err)
	}
	if closeErr := object.Close(); closeErr != nil {
		return "", fmt.Errorf("inspect render output: %w", closeErr)
	}

	err = renderer.ValidateMP4(ctx, c.Artifacts, key)
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return "", err
	}
	if err != nil {
		return "render output is invalid", nil
	}

	return "", nil
}

func (c *Controller) startWorkflow(ctx context.Context, wf workflow.Workflow) error {
	if err := publishDemoWorkflowSpecs(ctx, c.Artifacts, wf.ID, wf.Prompt); err != nil {
		return err
	}

	if _, err := c.ensureJob(ctx, wf.ID, StageWorker); err != nil {
		return err
	}

	return c.Workflows.Transition(ctx, wf.ID, "queued", "running", "", "")
}

func (c *Controller) failWorkflow(ctx context.Context, workflowID, reason string) error {
	if err := c.Workflows.Transition(ctx, workflowID, "running", "failed", "", reason); err != nil {
		return err
	}

	return c.cleanupWorkflowJobs(ctx, workflowID)
}

func (c *Controller) cleanupWorkflowJobs(ctx context.Context, workflowID string) error {
	policy := metav1.DeletePropagationBackground

	var failures []error
	for _, stage := range []string{StageWorker, StageRender} {
		job, err := Job(workflowID, stage, c.Config)
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

func hasJobCondition(job *batchv1.Job, conditionType batchv1.JobConditionType) bool {
	for _, condition := range job.Status.Conditions {
		if condition.Type == conditionType && condition.Status == "True" {
			return true
		}
	}

	return false
}

func isJobTerminal(job *batchv1.Job) bool {
	return hasJobCondition(job, batchv1.JobComplete) || hasJobCondition(job, batchv1.JobFailed)
}

func (c *Controller) getVerifiedJob(ctx context.Context, workflowID, stage string) (*batchv1.Job, error) {
	expected, err := Job(workflowID, stage, c.Config)
	if err != nil {
		return nil, err
	}

	actual, err := c.Jobs.Get(ctx, expected.Name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}

	if err := validateJobIdentity(actual, expected); err != nil {
		return nil, err
	}

	return actual, nil
}

func (c *Controller) ensureJob(ctx context.Context, workflowID, stage string) (*batchv1.Job, error) {
	existing, err := c.getVerifiedJob(ctx, workflowID, stage)
	if err == nil {
		return existing, nil
	}
	if !apierrors.IsNotFound(err) {
		return nil, err
	}

	desired, err := Job(workflowID, stage, c.Config)
	if err != nil {
		return nil, err
	}

	created, err := c.Jobs.Create(ctx, desired, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		return c.getVerifiedJob(ctx, workflowID, stage)
	}
	if err != nil {
		return nil, fmt.Errorf("create %s job: %w", stage, err)
	}

	return created, nil
}

func validateJobIdentity(actual, expected *batchv1.Job) error {
	for key, expectedValue := range expected.Labels {
		if actual.Labels[key] != expectedValue {
			return fmt.Errorf("job identity mismatch")
		}
	}

	if len(actual.Spec.Template.Spec.Containers) != 1 {
		return fmt.Errorf("job container mismatch")
	}

	actualContainer := actual.Spec.Template.Spec.Containers[0]
	expectedContainer := expected.Spec.Template.Spec.Containers[0]

	if actualContainer.Image != expectedContainer.Image || len(actualContainer.Args) != len(expectedContainer.Args) {
		return fmt.Errorf("job configuration mismatch")
	}

	for index, expectedArg := range expectedContainer.Args {
		if actualContainer.Args[index] != expectedArg {
			return fmt.Errorf("job command mismatch")
		}
	}

	return nil
}

func publishDemoWorkflowSpecs(ctx context.Context, artifacts storage.Store, workflowID, prompt string) error {
	if !workflowIDPattern.MatchString(workflowID) {
		return fmt.Errorf("invalid workflow ID")
	}

	workerPayload, err := json.Marshal(mock.Request{Type: mock.StepVideo, Prompt: prompt})
	if err != nil {
		return fmt.Errorf("encode workflow request: %w", err)
	}

	requestKey := path.Join("workflows", workflowID, "request.json")
	if err := artifacts.Put(ctx, requestKey, bytes.NewReader(workerPayload)); err != nil {
		return fmt.Errorf("publish workflow request: %w", err)
	}

	renderSpec := renderer.Request{
		Operation: renderer.OpRenderMP4,
		Inputs:    []string{path.Join("workflows", workflowID, "clip.mp4")},
		Width:     1280,
		Height:    720,
		FPS:       24,
	}
	renderPayload, err := json.Marshal(renderSpec)
	if err != nil {
		return fmt.Errorf("encode render request: %w", err)
	}

	renderKey := path.Join("workflows", workflowID, "render.json")
	if err := artifacts.Put(ctx, renderKey, bytes.NewReader(renderPayload)); err != nil {
		return fmt.Errorf("publish render request: %w", err)
	}

	return nil
}

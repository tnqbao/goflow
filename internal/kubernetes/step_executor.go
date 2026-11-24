package kubernetes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	"github.com/tnqbao/kubereel/internal/mock"
	"github.com/tnqbao/kubereel/internal/workflow"
	batchv1 "k8s.io/api/batch/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
)

// StepExecutor executes supported VideoRun steps using Kubernetes Jobs.
type StepExecutor struct {
	Runs          dynamic.ResourceInterface
	Jobs          Jobs
	Store         *workflow.Store
	Config        JobConfig
	DataDir       string
	MaxConcurrent int
	JobLock       *sync.Mutex
}

type executableStep struct {
	Name           string `json:"name"`
	Type           string `json:"type"`
	ProviderRef    string `json:"providerRef"`
	TimeoutSeconds int64  `json:"timeoutSeconds"`
	Resources      struct {
		Requests map[string]string `json:"requests"`
		Limits   map[string]string `json:"limits"`
	} `json:"resources"`
}

// Reconcile submits or observes one supported step at a time.
func (e *StepExecutor) Reconcile(ctx context.Context, run *unstructured.Unstructured, status map[string]interface{}) error {
	if e.Runs == nil || e.Jobs == nil || e.Store == nil || e.JobLock == nil || e.DataDir == "" || e.MaxConcurrent < 1 {
		return fmt.Errorf("invalid VideoRun executor configuration")
	}
	e.JobLock.Lock()
	defer e.JobLock.Unlock()
	snapshotMap, found, err := unstructured.NestedMap(run.Object, "spec", "workflowSnapshot")
	if err != nil {
		return fmt.Errorf("read workflow snapshot: %w", err)
	}
	if !found {
		return fmt.Errorf("workflow snapshot is missing")
	}
	data, err := json.Marshal(snapshotMap)
	if err != nil {
		return fmt.Errorf("encode workflow snapshot: %w", err)
	}
	var snapshot struct {
		Steps []executableStep `json:"steps"`
	}
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return fmt.Errorf("decode workflow snapshot: %w", err)
	}
	statuses, _, err := unstructured.NestedSlice(status, "steps")
	if err != nil {
		return fmt.Errorf("read step statuses: %w", err)
	}
	byName := make(map[string]map[string]interface{}, len(statuses))
	for _, item := range statuses {
		step, ok := item.(map[string]interface{})
		if !ok {
			return fmt.Errorf("invalid step status")
		}
		name, ok := step["name"].(string)
		if !ok {
			return fmt.Errorf("invalid step status name")
		}
		byName[name] = step
	}
	limit, hasLimit, err := unstructured.NestedInt64(run.Object, "spec", "execution", "maxConcurrentSteps")
	if err != nil {
		return fmt.Errorf("read step concurrency limit: %w", err)
	}
	running := int64(0)
	for _, entry := range byName {
		if entry["phase"] == "Running" {
			running++
		}
	}
	for _, step := range snapshot.Steps {
		if step.Type != "image" || step.ProviderRef != "mock" {
			continue
		}
		entry := byName[step.Name]
		if entry == nil {
			return fmt.Errorf("missing status for step %q", step.Name)
		}
		switch entry["phase"] {
		case "Running":
			changed, err := e.observe(ctx, run, status, statuses, step, entry)
			if err != nil || changed {
				return err
			}
			continue
		case "Pending":
		default:
			continue
		}
		if entry["ready"] != true {
			continue
		}
		if hasLimit && running >= limit {
			continue
		}
		prompt, found, err := unstructured.NestedString(run.Object, "spec", "inputs", "prompt", "stringValue")
		if err != nil {
			return fmt.Errorf("read mock prompt: %w", err)
		}
		if !found || strings.TrimSpace(prompt) == "" || len(strings.TrimSpace(prompt)) > 8192 {
			return e.mark(ctx, run, status, statuses, entry, "Failed", "mock image requires a nonempty prompt of at most 8192 bytes")
		}
		return e.submit(ctx, run, status, statuses, step, entry, prompt)
	}
	return nil
}

func (e *StepExecutor) submit(ctx context.Context, run *unstructured.Unstructured, status map[string]interface{}, statuses []interface{}, step executableStep, entry map[string]interface{}, prompt string) error {
	active, err := e.Store.Active(ctx)
	if err != nil {
		return err
	}
	count, err := activeVideoJobs(ctx, e.Jobs, e.Config.Instance)
	if err != nil {
		return err
	}
	for _, item := range active {
		if item.Status == "running" {
			count++
		}
	}
	if count >= e.MaxConcurrent {
		return nil
	}
	job, err := StepJob(run.GetName(), run.GetUID(), step.Name, e.Config)
	if err != nil {
		return err
	}
	if err := configureStepJob(job, step); err != nil {
		return e.mark(ctx, run, status, statuses, entry, "Failed", "invalid step resource configuration")
	}
	_, err = e.Jobs.Get(ctx, job.Name, metav1.GetOptions{})
	if err == nil {
		return e.mark(ctx, run, status, statuses, entry, "RequiresReview", "a Job exists without a recorded submission")
	}
	if !apierrors.IsNotFound(err) {
		return fmt.Errorf("check step Job: %w", err)
	}
	id := job.Labels["kubereel.io/workflow"]
	if err := writeRequest(e.DataDir, workflow.Workflow{ID: id, Prompt: prompt}); err != nil {
		return fmt.Errorf("write step request: %w", err)
	}
	entry["phase"] = "Running"
	entry["ready"] = false
	entry["jobName"] = job.Name
	entry["attempts"] = int64(1)
	status["phase"] = "Running"
	status["steps"] = statuses
	if err := updateVideoRunStatus(ctx, e.Runs, run, status); err != nil {
		return err
	}
	if _, err := e.Jobs.Create(ctx, job, metav1.CreateOptions{}); err != nil {
		return fmt.Errorf("submit step Job: %w", err)
	}
	return nil
}

func (e *StepExecutor) observe(ctx context.Context, run *unstructured.Unstructured, status map[string]interface{}, statuses []interface{}, step executableStep, entry map[string]interface{}) (bool, error) {
	expected, err := StepJob(run.GetName(), run.GetUID(), step.Name, e.Config)
	if err != nil {
		return false, err
	}
	if err := configureStepJob(expected, step); err != nil {
		return true, e.mark(ctx, run, status, statuses, entry, "Failed", "invalid step resource configuration")
	}
	if entry["jobName"] != expected.Name {
		return true, e.mark(ctx, run, status, statuses, entry, "RequiresReview", "recorded Job identity is invalid")
	}
	job, err := e.Jobs.Get(ctx, expected.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return true, e.mark(ctx, run, status, statuses, entry, "RequiresReview", "recorded Job is missing")
	}
	if err != nil {
		return false, fmt.Errorf("get step Job: %w", err)
	}
	if err := validateStepJob(job, expected); err != nil {
		return true, e.mark(ctx, run, status, statuses, entry, "RequiresReview", err.Error())
	}
	if finished(job, batchv1.JobFailed) {
		return true, e.mark(ctx, run, status, statuses, entry, "Failed", "step Job failed")
	}
	if !finished(job, batchv1.JobComplete) {
		return false, nil
	}
	id := expected.Labels["kubereel.io/workflow"]
	artifact := filepath.Join("workflows", id, "scene.svg")
	err = mock.ValidateSVG(ctx, filepath.Join(e.DataDir, artifact))
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false, err
	}
	if err != nil {
		return true, e.mark(ctx, run, status, statuses, entry, "Failed", "step artifact is missing or invalid")
	}
	entry["phase"] = "Succeeded"
	entry["ready"] = false
	status["steps"] = statuses
	artifacts, _, err := unstructured.NestedStringSlice(status, "artifacts")
	if err != nil {
		return false, fmt.Errorf("read run artifacts: %w", err)
	}
	if !contains(artifacts, artifact) {
		artifacts = append(artifacts, artifact)
	}
	if err := unstructured.SetNestedStringSlice(status, artifacts, "artifacts"); err != nil {
		return false, err
	}
	if !allStepsSucceeded(statuses) {
		return true, updateVideoRunStatus(ctx, e.Runs, run, status)
	}
	status["phase"] = "Succeeded"
	if err := validateRunOutputs(run, statuses); err != nil {
		status["phase"] = "Failed"
		status["error"] = err.Error()
	}
	return true, updateVideoRunStatus(ctx, e.Runs, run, status)
}

func validateRunOutputs(run *unstructured.Unstructured, statuses []interface{}) error {
	outputs, _, err := unstructured.NestedSlice(run.Object, "spec", "workflowSnapshot", "outputs")
	if err != nil {
		return fmt.Errorf("read workflow outputs: %w", err)
	}
	completed := make(map[string]bool, len(statuses))
	for _, item := range statuses {
		step, ok := item.(map[string]interface{})
		if !ok {
			return fmt.Errorf("invalid step status")
		}
		name, ok := step["name"].(string)
		if !ok {
			return fmt.Errorf("invalid step status name")
		}
		completed[name] = step["phase"] == "Succeeded"
	}
	for _, item := range outputs {
		output, ok := item.(map[string]interface{})
		if !ok {
			return fmt.Errorf("invalid workflow output")
		}
		from, ok := output["from"].(string)
		if !ok || !completed[from] {
			return fmt.Errorf("workflow output references an incomplete or unknown step")
		}
	}
	return nil
}

func (e *StepExecutor) mark(ctx context.Context, run *unstructured.Unstructured, status map[string]interface{}, statuses []interface{}, entry map[string]interface{}, phase, message string) error {
	entry["phase"] = phase
	entry["ready"] = false
	entry["error"] = message
	status["steps"] = statuses
	status["phase"] = phase
	status["error"] = message
	return updateVideoRunStatus(ctx, e.Runs, run, status)
}

func contains(items []string, target string) bool {
	for _, item := range items {
		if item == target {
			return true
		}
	}
	return false
}

func allStepsSucceeded(statuses []interface{}) bool {
	for _, item := range statuses {
		step, ok := item.(map[string]interface{})
		if !ok || step["phase"] != "Succeeded" {
			return false
		}
	}
	return len(statuses) > 0
}

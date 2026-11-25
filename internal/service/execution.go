package service

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"time"

	"github.com/tnqbao/kubereel/internal/repository"
	"github.com/tnqbao/kubereel/internal/workflow"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/util/retry"
)

var ErrExecutionsUnavailable = errors.New("executions unavailable")
var ErrPaidExecutionApproval = errors.New("allowPaid must be true for runs that reference external providers")

const retrySource = "kubereel.io/retry-source"

type Executions struct {
	repo        *repository.ExecutionRepository
	definitions *repository.WorkflowDefinitionRepository
}

func NewExecutions(repo *repository.ExecutionRepository, definitions *repository.WorkflowDefinitionRepository) *Executions {
	return &Executions{repo: repo, definitions: definitions}
}

type Execution struct {
	ID              string          `json:"id"`
	WorkflowID      string          `json:"workflowId"`
	Phase           string          `json:"phase"`
	CancelRequested bool            `json:"cancelRequested"`
	CreatedAt       time.Time       `json:"createdAt"`
	StartedAt       string          `json:"startedAt,omitempty"`
	CompletedAt     string          `json:"completedAt,omitempty"`
	Error           string          `json:"error,omitempty"`
	Steps           []ExecutionStep `json:"steps"`
}

type ExecutionStep struct {
	Name        string `json:"name"`
	Phase       string `json:"phase"`
	Attempts    int64  `json:"attempts"`
	StartedAt   string `json:"startedAt,omitempty"`
	CompletedAt string `json:"completedAt,omitempty"`
	Error       string `json:"error,omitempty"`
}

type ExecutionPage struct {
	Items    []Execution `json:"items"`
	Continue string      `json:"continue,omitempty"`
}

type ExecutionOptions struct {
	Inputs             map[string]any `json:"inputs,omitempty"`
	MaxConcurrentSteps int64          `json:"maxConcurrentSteps,omitempty"`
	AllowPaid          bool           `json:"allowPaid,omitempty"`
}

func (s *Executions) Create(ctx context.Context, id string, opts ExecutionOptions) (Execution, error) {
	if s == nil || s.repo == nil || s.definitions == nil {
		return Execution{}, ErrExecutionsUnavailable
	}
	if err := validateWorkflowID(id); err != nil {
		return Execution{}, err
	}
	definition, err := s.definitions.Get(ctx, id)
	if err != nil {
		return Execution{}, err
	}
	spec, err := executionSpec(definition, opts)
	if err != nil {
		return Execution{}, err
	}
	run, err := s.repo.Create(ctx, "", nil, spec)
	if err != nil {
		return Execution{}, err
	}
	return executionView(run), nil
}

func executionSpec(definition *unstructured.Unstructured, opts ExecutionOptions) (map[string]any, error) {
	snapshot, _, err := unstructured.NestedMap(definition.Object, "spec")
	if err != nil {
		return nil, err
	}
	if err := validateWorkflowSpec(snapshot); err != nil {
		return nil, err
	}
	if err := validateExecutionInputs(snapshot, opts.Inputs); err != nil {
		return nil, err
	}
	if opts.MaxConcurrentSteps < 0 || opts.MaxConcurrentSteps > 100 {
		return nil, fmt.Errorf("%w: maxConcurrentSteps must be between 1 and 100 when set", workflow.ErrInvalid)
	}
	if err := checkPaidExecution(snapshot, opts.AllowPaid); err != nil {
		return nil, err
	}
	spec := map[string]any{"workflowRef": definition.GetName(), "workflowSnapshot": snapshot}
	if opts.Inputs != nil {
		spec["inputs"] = opts.Inputs
	}
	if opts.MaxConcurrentSteps > 0 {
		spec["execution"] = map[string]any{"maxConcurrentSteps": opts.MaxConcurrentSteps}
	}
	return spec, nil
}

func (s *Executions) List(ctx context.Context, limit int64, cursor string) (ExecutionPage, error) {
	if s == nil || s.repo == nil {
		return ExecutionPage{}, ErrExecutionsUnavailable
	}
	page, err := s.repo.List(ctx, limit, cursor)
	if err != nil {
		return ExecutionPage{}, err
	}
	result := ExecutionPage{Items: make([]Execution, 0, len(page.Items)), Continue: page.GetContinue()}
	for i := range page.Items {
		result.Items = append(result.Items, executionView(&page.Items[i]))
	}
	return result, nil
}

func (s *Executions) Get(ctx context.Context, id string) (Execution, error) {
	run, err := s.lookup(ctx, id)
	if err != nil {
		return Execution{}, err
	}
	return executionView(run), nil
}

func (s *Executions) Cancel(ctx context.Context, id string) (Execution, error) {
	var view Execution
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var err error
		view, err = s.requestCancel(ctx, id)
		return err
	})
	return view, err
}

func (s *Executions) requestCancel(ctx context.Context, id string) (Execution, error) {
	run, err := s.lookup(ctx, id)
	if err != nil {
		return Execution{}, err
	}
	view := executionView(run)
	switch view.Phase {
	case "Cancelled", "Cancelling":
		return view, nil
	case "Pending", "Running":
	default:
		return Execution{}, workflow.ErrConflict
	}
	if view.CancelRequested {
		return view, nil
	}
	updated, err := s.repo.RequestCancel(ctx, run)
	if err != nil {
		return Execution{}, err
	}
	return executionView(updated), nil
}

func (s *Executions) Retry(ctx context.Context, id string, allowPaid bool) (Execution, error) {
	run, err := s.lookup(ctx, id)
	if err != nil {
		return Execution{}, err
	}
	view := executionView(run)
	if !canRetryExecution(view) {
		return Execution{}, workflow.ErrConflict
	}
	spec, _, err := unstructured.NestedMap(run.Object, "spec")
	if err != nil {
		return Execution{}, err
	}
	snapshot, _, err := unstructured.NestedMap(spec, "workflowSnapshot")
	if err != nil {
		return Execution{}, err
	}
	if err := checkPaidExecution(snapshot, allowPaid); err != nil {
		return Execution{}, err
	}
	active, err := s.repo.HasActiveJobs(ctx, run)
	if err != nil {
		return Execution{}, err
	}
	if active {
		return Execution{}, workflow.ErrConflict
	}
	delete(spec, "cancelRequested")
	uid := string(run.GetUID())
	digest := sha256.Sum256([]byte(uid))
	name := fmt.Sprintf("retry-%x", digest[:16])
	created, err := s.repo.Create(ctx, name, map[string]string{retrySource: uid}, spec)
	if apierrors.IsAlreadyExists(err) {
		return s.existingRetry(ctx, name, uid)
	}
	if err != nil {
		return Execution{}, err
	}
	return executionView(created), nil
}

func (s *Executions) existingRetry(ctx context.Context, name, uid string) (Execution, error) {
	existing, err := s.repo.Get(ctx, name)
	if err != nil {
		return Execution{}, err
	}
	if existing.GetAnnotations()[retrySource] != uid {
		return Execution{}, workflow.ErrConflict
	}
	return executionView(existing), nil
}

func canRetryExecution(view Execution) bool {
	if view.Phase != "Failed" && view.Phase != "Cancelled" {
		return false
	}
	for _, step := range view.Steps {
		switch step.Phase {
		case "Running", "Cancelling", "RequiresReview":
			return false
		}
	}
	return true
}

func (s *Executions) lookup(ctx context.Context, id string) (*unstructured.Unstructured, error) {
	if s == nil || s.repo == nil {
		return nil, ErrExecutionsUnavailable
	}
	if err := validateWorkflowID(id); err != nil {
		return nil, err
	}
	return s.repo.Get(ctx, id)
}

func executionView(run *unstructured.Unstructured) Execution {
	status, _, _ := unstructured.NestedMap(run.Object, "status")
	phase := statusString(status, "phase")
	if phase == "" {
		phase = "Pending"
	}
	ref, _, _ := unstructured.NestedString(run.Object, "spec", "workflowRef")
	cancel, _, _ := unstructured.NestedBool(run.Object, "spec", "cancelRequested")
	view := Execution{ID: run.GetName(), WorkflowID: ref, Phase: phase, CancelRequested: cancel, CreatedAt: run.GetCreationTimestamp().Time,
		StartedAt: statusString(status, "startedAt"), CompletedAt: statusString(status, "completedAt"), Error: statusString(status, "error"), Steps: []ExecutionStep{}}
	for _, step := range executionSteps(run) {
		attempts, _, _ := unstructured.NestedInt64(step, "attempts")
		view.Steps = append(view.Steps, ExecutionStep{Name: statusString(step, "name"), Phase: statusString(step, "phase"), Attempts: attempts,
			StartedAt: statusString(step, "startedAt"), CompletedAt: statusString(step, "completedAt"), Error: statusString(step, "error")})
	}
	return view
}

func executionSteps(run *unstructured.Unstructured) []map[string]any {
	entries, _, _ := unstructured.NestedSlice(run.Object, "status", "steps")
	steps := make([]map[string]any, 0, len(entries))
	for _, entry := range entries {
		if step, ok := entry.(map[string]any); ok {
			steps = append(steps, step)
		}
	}
	return steps
}

func statusString(fields map[string]any, key string) string {
	value, _ := fields[key].(string)
	return value
}

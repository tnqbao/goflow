package service

import (
	"context"
	"fmt"
	"time"

	"github.com/tnqbao/kubereel/internal/repository"
	"github.com/tnqbao/kubereel/internal/workflow"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func checkPaidExecution(snapshot map[string]any, approved bool) error {
	if approved {
		return nil
	}
	steps, _, err := unstructured.NestedSlice(snapshot, "steps")
	if err != nil {
		return err
	}
	for _, entry := range steps {
		step, ok := entry.(map[string]any)
		if !ok {
			return workflow.ErrInvalid
		}
		ref := statusString(step, "providerRef")
		if ref != "" && ref != "mock" {
			return ErrPaidExecutionApproval
		}
	}
	return nil
}

func validateExecutionInputs(snapshot, inputs map[string]any) error {
	definitions, _, err := unstructured.NestedSlice(snapshot, "inputs")
	if err != nil {
		return err
	}
	if len(definitions) == 0 {
		return nil
	}
	allowed := make(map[string]bool, len(definitions))
	for _, entry := range definitions {
		input, ok := entry.(map[string]any)
		if !ok {
			return workflow.ErrInvalid
		}
		name := statusString(input, "name")
		allowed[name] = true
		value, exists := inputs[name]
		required, _ := input["required"].(bool)
		if !exists && required {
			return fmt.Errorf("%w: required input %q is missing", workflow.ErrInvalid, name)
		}
		if !exists {
			continue
		}
		if !matchesInputType(value, statusString(input, "type")) {
			return fmt.Errorf("%w: input %q has the wrong type", workflow.ErrInvalid, name)
		}
	}
	for key := range inputs {
		if !allowed[key] {
			return fmt.Errorf("%w: input %q is not declared", workflow.ErrInvalid, key)
		}
	}
	return nil
}

func matchesInputType(value any, kind string) bool {
	fields, ok := value.(map[string]any)
	if !ok || len(fields) != 1 {
		return false
	}
	raw, ok := fields[kind+"Value"]
	return ok && raw != nil
}

type ExecutionLog struct {
	Step string `json:"step"`
	Text string `json:"text"`
}

func (s *Executions) Logs(ctx context.Context, id, step string, tail int64) (ExecutionLog, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	run, err := s.lookup(ctx, id)
	if err != nil {
		return ExecutionLog{}, err
	}
	steps := executionSteps(run)
	for i := len(steps) - 1; i >= 0; i-- {
		name, job := statusString(steps[i], "name"), statusString(steps[i], "jobName")
		if job == "" || (step != "" && name != step) {
			continue
		}
		text, err := s.repo.Logs(ctx, run, name, job, tail)
		if err != nil {
			return ExecutionLog{}, err
		}
		return ExecutionLog{Step: name, Text: text}, nil
	}
	return ExecutionLog{}, repository.ErrExecutionLogsUnavailable
}

func (s *Executions) Artifacts(ctx context.Context, id string) ([]string, error) {
	run, err := s.lookup(ctx, id)
	if err != nil {
		return nil, err
	}
	refs, _, err := unstructured.NestedStringSlice(run.Object, "status", "artifacts")
	if refs == nil && err == nil {
		refs = []string{}
	}
	return refs, err
}

package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/tnqbao/kubereel/internal/repository"
	"github.com/tnqbao/kubereel/internal/workflow"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/validation"
)

// WorkflowDefinition is the REST representation of a reusable VideoWorkflow.
type WorkflowDefinition struct {
	ID              string         `json:"id"`
	ResourceVersion string         `json:"resourceVersion"`
	Spec            map[string]any `json:"spec"`
}

type WorkflowDefinitionPage struct {
	Items    []WorkflowDefinition `json:"items"`
	Continue string               `json:"continue,omitempty"`
}

var ErrWorkflowDefinitionsUnavailable = errors.New("workflow definitions unavailable")

type WorkflowDefinitions struct {
	repo *repository.WorkflowDefinitionRepository
}

func NewWorkflowDefinitions(repo *repository.WorkflowDefinitionRepository) *WorkflowDefinitions {
	return &WorkflowDefinitions{repo: repo}
}

func (s *WorkflowDefinitions) List(ctx context.Context, limit int64, cursor string) (WorkflowDefinitionPage, error) {
	if s == nil || s.repo == nil {
		return WorkflowDefinitionPage{}, ErrWorkflowDefinitionsUnavailable
	}
	list, err := s.repo.List(ctx, limit, cursor)
	if err != nil {
		return WorkflowDefinitionPage{}, err
	}
	page := WorkflowDefinitionPage{Items: make([]WorkflowDefinition, 0, len(list.Items)), Continue: list.GetContinue()}
	for i := range list.Items {
		page.Items = append(page.Items, workflowDefinition(&list.Items[i]))
	}
	return page, nil
}

func (s *WorkflowDefinitions) Get(ctx context.Context, id string) (WorkflowDefinition, error) {
	item, err := s.lookup(ctx, id)
	if err != nil {
		return WorkflowDefinition{}, err
	}
	return workflowDefinition(item), nil
}

func (s *WorkflowDefinitions) Create(ctx context.Context, id string, spec map[string]any) (WorkflowDefinition, error) {
	if s == nil || s.repo == nil {
		return WorkflowDefinition{}, ErrWorkflowDefinitionsUnavailable
	}
	if err := validateWorkflowID(id); err != nil {
		return WorkflowDefinition{}, err
	}
	if err := validateWorkflowSpec(spec); err != nil {
		return WorkflowDefinition{}, err
	}
	item, err := s.repo.Create(ctx, id, spec)
	if err != nil {
		return WorkflowDefinition{}, err
	}
	return workflowDefinition(item), nil
}

func (s *WorkflowDefinitions) Update(ctx context.Context, id, version string, changes map[string]any) (WorkflowDefinition, error) {
	if len(changes) == 0 {
		return WorkflowDefinition{}, fmt.Errorf("%w: spec changes are required", workflow.ErrInvalid)
	}
	item, err := s.lookup(ctx, id)
	if err != nil {
		return WorkflowDefinition{}, err
	}
	if version != "" && version != item.GetResourceVersion() {
		return WorkflowDefinition{}, workflow.ErrConflict
	}
	spec, _, err := unstructured.NestedMap(item.Object, "spec")
	if err != nil {
		return WorkflowDefinition{}, fmt.Errorf("read workflow spec: %w", err)
	}
	if spec == nil {
		spec = make(map[string]any)
	}
	for field, value := range changes {
		if !workflowSpecField(field) {
			return WorkflowDefinition{}, fmt.Errorf("%w: unknown spec field", workflow.ErrInvalid)
		}
		if value == nil {
			delete(spec, field)
			continue
		}
		spec[field] = value
	}
	if err := validateWorkflowSpec(spec); err != nil {
		return WorkflowDefinition{}, err
	}
	item.Object["spec"] = spec
	updated, err := s.repo.Update(ctx, item)
	if err != nil {
		return WorkflowDefinition{}, err
	}
	return workflowDefinition(updated), nil
}

func (s *WorkflowDefinitions) Delete(ctx context.Context, id string) error {
	item, err := s.lookup(ctx, id)
	if err != nil {
		return err
	}
	return s.repo.Delete(ctx, item)
}

func (s *WorkflowDefinitions) lookup(ctx context.Context, id string) (*unstructured.Unstructured, error) {
	if s == nil || s.repo == nil {
		return nil, ErrWorkflowDefinitionsUnavailable
	}
	if err := validateWorkflowID(id); err != nil {
		return nil, err
	}
	return s.repo.Get(ctx, id)
}

func workflowDefinition(item *unstructured.Unstructured) WorkflowDefinition {
	spec, _, _ := unstructured.NestedMap(item.Object, "spec")
	return WorkflowDefinition{ID: item.GetName(), ResourceVersion: item.GetResourceVersion(), Spec: spec}
}

func validateWorkflowID(id string) error {
	if len(id) > 253 || len(validation.IsDNS1123Subdomain(id)) != 0 {
		return fmt.Errorf("%w: id must be a Kubernetes DNS subdomain", workflow.ErrInvalid)
	}
	return nil
}

func workflowSpecField(field string) bool {
	switch field {
	case "description", "inputs", "steps", "outputs":
		return true
	default:
		return false
	}
}

// Kubernetes validates the full CRD schema. Check relationships the schema cannot express.
func validateWorkflowSpec(spec map[string]any) error {
	for field := range spec {
		if !workflowSpecField(field) {
			return fmt.Errorf("%w: unknown spec field", workflow.ErrInvalid)
		}
	}
	data, err := json.Marshal(spec)
	if err != nil {
		return fmt.Errorf("encode workflow spec: %w", err)
	}
	var graph struct {
		Steps   []workflow.DAGStep `json:"steps"`
		Outputs []struct {
			From string `json:"from"`
		} `json:"outputs"`
	}
	if err := json.Unmarshal(data, &graph); err != nil {
		return fmt.Errorf("%w: invalid steps or outputs", workflow.ErrInvalid)
	}
	if err := workflow.ValidateDAG(graph.Steps); err != nil {
		return err
	}
	names := make(map[string]bool, len(graph.Steps))
	for _, step := range graph.Steps {
		names[step.Name] = true
	}
	for _, output := range graph.Outputs {
		if !names[output.From] {
			return fmt.Errorf("%w: output references an unknown step", workflow.ErrInvalid)
		}
	}
	return nil
}

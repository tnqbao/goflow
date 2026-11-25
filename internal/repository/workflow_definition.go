package repository

import (
	"context"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

// WorkflowDefinitionRepository stores reusable workflow definitions in one namespace.
type WorkflowDefinitionRepository struct {
	resources dynamic.ResourceInterface
}

func NewWorkflowDefinitionRepository(resources dynamic.ResourceInterface) *WorkflowDefinitionRepository {
	return &WorkflowDefinitionRepository{resources: resources}
}

func VideoWorkflowResource() schema.GroupVersionResource {
	return schema.GroupVersionResource{Group: "kubereel.io", Version: "v1alpha1", Resource: "videoworkflows"}
}

func (r *WorkflowDefinitionRepository) List(ctx context.Context, limit int64, cursor string) (*unstructured.UnstructuredList, error) {
	return r.resources.List(ctx, metav1.ListOptions{Limit: limit, Continue: cursor})
}

func (r *WorkflowDefinitionRepository) Get(ctx context.Context, id string) (*unstructured.Unstructured, error) {
	return r.resources.Get(ctx, id, metav1.GetOptions{})
}

func (r *WorkflowDefinitionRepository) Create(ctx context.Context, id string, spec map[string]any) (*unstructured.Unstructured, error) {
	item := &unstructured.Unstructured{Object: map[string]any{"spec": spec}}
	item.SetGroupVersionKind(VideoWorkflowResource().GroupVersion().WithKind("VideoWorkflow"))
	item.SetName(id)
	return r.resources.Create(ctx, item, metav1.CreateOptions{FieldValidation: metav1.FieldValidationStrict})
}

func (r *WorkflowDefinitionRepository) Update(ctx context.Context, item *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	return r.resources.Update(ctx, item, metav1.UpdateOptions{FieldValidation: metav1.FieldValidationStrict})
}

func (r *WorkflowDefinitionRepository) Delete(ctx context.Context, item *unstructured.Unstructured) error {
	uid, version := item.GetUID(), item.GetResourceVersion()
	policy := metav1.DeletePropagationOrphan
	return r.resources.Delete(ctx, item.GetName(), metav1.DeleteOptions{
		Preconditions: &metav1.Preconditions{UID: &uid, ResourceVersion: &version}, PropagationPolicy: &policy,
	})
}

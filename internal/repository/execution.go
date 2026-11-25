package repository

import (
	"context"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	batchclient "k8s.io/client-go/kubernetes/typed/batch/v1"
	coreclient "k8s.io/client-go/kubernetes/typed/core/v1"
)

type ExecutionRepository struct {
	runs dynamic.ResourceInterface
	jobs batchclient.JobInterface
	pods coreclient.PodInterface
}

func NewExecutionRepository(runs dynamic.ResourceInterface, jobs batchclient.JobInterface, pods coreclient.PodInterface) *ExecutionRepository {
	return &ExecutionRepository{runs: runs, jobs: jobs, pods: pods}
}

func VideoRunResource() schema.GroupVersionResource {
	return schema.GroupVersionResource{Group: "kubereel.io", Version: "v1alpha1", Resource: "videoruns"}
}

func (r *ExecutionRepository) Get(ctx context.Context, id string) (*unstructured.Unstructured, error) {
	return r.runs.Get(ctx, id, metav1.GetOptions{})
}

func (r *ExecutionRepository) List(ctx context.Context, limit int64, cursor string) (*unstructured.UnstructuredList, error) {
	return r.runs.List(ctx, metav1.ListOptions{Limit: limit, Continue: cursor})
}

func (r *ExecutionRepository) Create(ctx context.Context, name string, annotations map[string]string, spec map[string]any) (*unstructured.Unstructured, error) {
	item := &unstructured.Unstructured{Object: map[string]any{"spec": spec}}
	item.SetGroupVersionKind(VideoRunResource().GroupVersion().WithKind("VideoRun"))
	item.SetAnnotations(annotations)
	item.SetName(name)
	if name == "" {
		item.SetGenerateName("run-")
	}
	return r.runs.Create(ctx, item, metav1.CreateOptions{FieldValidation: metav1.FieldValidationStrict})
}

func (r *ExecutionRepository) RequestCancel(ctx context.Context, item *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	if err := unstructured.SetNestedField(item.Object, true, "spec", "cancelRequested"); err != nil {
		return nil, err
	}
	return r.runs.Update(ctx, item, metav1.UpdateOptions{FieldValidation: metav1.FieldValidationStrict})
}

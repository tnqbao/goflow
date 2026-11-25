package repository

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
	coreclient "k8s.io/client-go/kubernetes/typed/core/v1"
)

// ProviderRepository persists provider configuration as namespace-scoped VideoProviders.
type ProviderRepository struct {
	resources dynamic.ResourceInterface
	secrets   coreclient.SecretInterface
}

func NewProviderRepository(resources dynamic.ResourceInterface, secrets coreclient.SecretInterface) *ProviderRepository {
	return &ProviderRepository{resources: resources, secrets: secrets}
}

func (r *ProviderRepository) List(ctx context.Context, limit int64, cursor string) (*unstructured.UnstructuredList, error) {
	if r == nil || r.resources == nil {
		return nil, fmt.Errorf("provider repository unavailable")
	}
	items, err := r.resources.List(ctx, metav1.ListOptions{Limit: limit, Continue: cursor})
	if err != nil {
		return nil, fmt.Errorf("list providers: %w", err)
	}
	return items, nil
}

func (r *ProviderRepository) Get(ctx context.Context, name string) (*unstructured.Unstructured, error) {
	if r == nil || r.resources == nil {
		return nil, fmt.Errorf("provider repository unavailable")
	}
	item, err := r.resources.Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("get provider: %w", err)
	}
	return item, nil
}

func (r *ProviderRepository) Create(ctx context.Context, item *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	return r.resources.Create(ctx, item, metav1.CreateOptions{})
}

func (r *ProviderRepository) Update(ctx context.Context, item *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	return r.resources.Update(ctx, item, metav1.UpdateOptions{})
}

func (r *ProviderRepository) Delete(ctx context.Context, item *unstructured.Unstructured) error {
	uid, version := item.GetUID(), item.GetResourceVersion()
	return r.resources.Delete(ctx, item.GetName(), metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid, ResourceVersion: &version}})
}

const ProviderAPIKey = "api-key"

// StoreCredential creates a new immutable credential owned by the provider.
func (r *ProviderRepository) StoreCredential(ctx context.Context, owner *unstructured.Unstructured, name, key string) error {
	if r.secrets == nil {
		return fmt.Errorf("provider credential storage unavailable")
	}
	immutable := true
	_, err := r.secrets.Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, OwnerReferences: []metav1.OwnerReference{{
			APIVersion: owner.GetAPIVersion(), Kind: owner.GetKind(), Name: owner.GetName(), UID: owner.GetUID(),
		}}},
		Immutable: &immutable, Data: map[string][]byte{ProviderAPIKey: []byte(key)},
	}, metav1.CreateOptions{})
	return err
}

func (r *ProviderRepository) Credential(ctx context.Context, name, key string) ([]byte, error) {
	if r.secrets == nil {
		return nil, fmt.Errorf("provider credential storage unavailable")
	}
	secret, err := r.secrets.Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	return secret.Data[key], nil
}

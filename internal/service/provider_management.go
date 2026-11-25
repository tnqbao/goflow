package service

import (
	"context"
	"fmt"

	"github.com/tnqbao/kubereel/internal/repository"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// ManagedProvider is the non-secret provider configuration returned to clients.
type ManagedProvider struct {
	ID                    string   `json:"id"`
	Enabled               bool     `json:"enabled"`
	Type                  string   `json:"type"`
	Capabilities          []string `json:"capabilities"`
	Models                []string `json:"models,omitempty"`
	BaseURL               string   `json:"baseURL,omitempty"`
	MaxConcurrent         int64    `json:"maxConcurrent,omitempty"`
	TimeoutSeconds        int64    `json:"timeoutSeconds,omitempty"`
	CredentialsReferenced bool     `json:"credentialsReferenced"`
}

type ProviderPage struct {
	Items    []ManagedProvider `json:"items"`
	Continue string            `json:"continue,omitempty"`
}

// ProviderManagement reads provider configurations from Kubernetes.
type ProviderManagement struct {
	repository *repository.ProviderRepository
}

func NewProviderManagement(repo *repository.ProviderRepository) *ProviderManagement {
	return &ProviderManagement{repository: repo}
}

func (s *ProviderManagement) List(ctx context.Context, limit int64, cursor string) (ProviderPage, error) {
	if s == nil || s.repository == nil {
		return ProviderPage{}, ErrProviderUnavailable
	}
	list, err := s.repository.List(ctx, limit, cursor)
	if err != nil {
		return ProviderPage{}, err
	}
	page := ProviderPage{Items: make([]ManagedProvider, 0, len(list.Items)), Continue: list.GetContinue()}
	for i := range list.Items {
		item, err := managedProvider(&list.Items[i])
		if err != nil {
			return ProviderPage{}, err
		}
		page.Items = append(page.Items, item)
	}
	return page, nil
}

func (s *ProviderManagement) Get(ctx context.Context, name string) (ManagedProvider, error) {
	if s == nil || s.repository == nil {
		return ManagedProvider{}, ErrProviderUnavailable
	}
	item, err := s.repository.Get(ctx, name)
	if err != nil {
		return ManagedProvider{}, err
	}
	return managedProvider(item)
}

func managedProvider(item *unstructured.Unstructured) (ManagedProvider, error) {
	spec, found, err := unstructured.NestedMap(item.Object, "spec")
	if err != nil || !found {
		return ManagedProvider{}, fmt.Errorf("invalid VideoProvider spec")
	}
	typ, _, err := unstructured.NestedString(spec, "type")
	if err != nil {
		return ManagedProvider{}, fmt.Errorf("invalid provider type: %w", err)
	}
	caps, _, err := unstructured.NestedStringSlice(spec, "capabilities")
	if err != nil {
		return ManagedProvider{}, fmt.Errorf("invalid provider capabilities: %w", err)
	}
	models, _, err := unstructured.NestedStringSlice(spec, "models")
	if err != nil {
		return ManagedProvider{}, fmt.Errorf("invalid provider models: %w", err)
	}
	url, _, err := unstructured.NestedString(spec, "baseURL")
	if err != nil {
		return ManagedProvider{}, fmt.Errorf("invalid provider URL: %w", err)
	}
	concurrent, _, err := unstructured.NestedInt64(spec, "maxConcurrent")
	if err != nil {
		return ManagedProvider{}, fmt.Errorf("invalid provider concurrency: %w", err)
	}
	timeout, _, err := unstructured.NestedInt64(spec, "timeoutSeconds")
	if err != nil {
		return ManagedProvider{}, fmt.Errorf("invalid provider timeout: %w", err)
	}
	secretName, _, err := unstructured.NestedString(spec, "secretRef", "name")
	if err != nil {
		return ManagedProvider{}, fmt.Errorf("invalid provider secret reference: %w", err)
	}
	secretKey, _, err := unstructured.NestedString(spec, "secretRef", "key")
	if err != nil {
		return ManagedProvider{}, fmt.Errorf("invalid provider secret key: %w", err)
	}
	enabled, found, err := unstructured.NestedBool(spec, "enabled")
	if err != nil {
		return ManagedProvider{}, fmt.Errorf("invalid provider enabled flag: %w", err)
	}
	return ManagedProvider{Enabled: !found || enabled, ID: item.GetName(), Type: typ, Capabilities: caps, Models: models, BaseURL: url, MaxConcurrent: concurrent, TimeoutSeconds: timeout, CredentialsReferenced: secretName != "" && secretKey != ""}, nil
}

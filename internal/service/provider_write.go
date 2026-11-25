package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/tnqbao/kubereel/internal/provider/fal"
	"github.com/tnqbao/kubereel/internal/provider/gemini"
	"github.com/tnqbao/kubereel/internal/repository"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/validation"
)

var ErrInvalidProvider = errors.New("invalid provider configuration")
var ErrProviderCredentials = errors.New("provider credentials could not be stored")

type ProviderChanges struct {
	Capabilities   *[]string `json:"capabilities"`
	Models         *[]string `json:"models"`
	BaseURL        *string   `json:"baseURL"`
	MaxConcurrent  *int64    `json:"maxConcurrent"`
	TimeoutSeconds *int64    `json:"timeoutSeconds"`
	Enabled        *bool     `json:"enabled"`
	APIKey         *string   `json:"apiKey"`
}

func invalidProvider(message string) error { return fmt.Errorf("%w: %s", ErrInvalidProvider, message) }

func (s *ProviderManagement) Create(ctx context.Context, id, kind string, changes ProviderChanges) (ManagedProvider, error) {
	if s == nil || s.repository == nil {
		return ManagedProvider{}, ErrProviderUnavailable
	}
	if id == "mock" || len(id) > 63 || len(validation.IsDNS1123Label(id)) != 0 {
		return ManagedProvider{}, invalidProvider("id must be a DNS label other than mock")
	}
	if kind != "mock" && changes.APIKey == nil {
		return ManagedProvider{}, invalidProvider("apiKey is required")
	}
	item := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "kubereel.io/v1alpha1", "kind": "VideoProvider", "metadata": map[string]any{"name": id}, "spec": map[string]any{"type": kind}}}
	return s.save(ctx, item, changes, true)
}

func (s *ProviderManagement) Update(ctx context.Context, id string, changes ProviderChanges) (ManagedProvider, error) {
	if s == nil || s.repository == nil {
		return ManagedProvider{}, ErrProviderUnavailable
	}
	if id == "mock" {
		return ManagedProvider{}, invalidProvider("built-in mock cannot be modified")
	}
	item, err := s.repository.Get(ctx, id)
	if err != nil {
		return ManagedProvider{}, err
	}
	return s.save(ctx, item, changes, false)
}

func (s *ProviderManagement) Delete(ctx context.Context, id string) error {
	if s == nil || s.repository == nil {
		return ErrProviderUnavailable
	}
	if id == "mock" {
		return invalidProvider("built-in mock cannot be deleted")
	}
	item, err := s.repository.Get(ctx, id)
	if err != nil {
		return err
	}
	return s.repository.Delete(ctx, item)
}

func (s *ProviderManagement) save(ctx context.Context, item *unstructured.Unstructured, changes ProviderChanges, creating bool) (ManagedProvider, error) {
	spec, found, err := unstructured.NestedMap(item.Object, "spec")
	if err != nil || !found {
		return ManagedProvider{}, invalidProvider("missing spec")
	}
	applyProviderChanges(spec, changes)
	if err := validateProvider(spec, changes.APIKey); err != nil {
		return ManagedProvider{}, err
	}
	name, err := prepareCredential(spec, changes.APIKey)
	if err != nil {
		return ManagedProvider{}, err
	}
	item.Object["spec"] = spec
	if creating {
		return s.createProvider(ctx, item, name, changes.APIKey)
	}
	if err := s.storeCredential(ctx, item, name, changes.APIKey); err != nil {
		return ManagedProvider{}, err
	}
	updated, err := s.repository.Update(ctx, item)
	if err != nil {
		return ManagedProvider{}, err
	}
	return managedProvider(updated)
}

func (s *ProviderManagement) createProvider(ctx context.Context, item *unstructured.Unstructured, name string, key *string) (ManagedProvider, error) {
	created, err := s.repository.Create(ctx, item)
	if err != nil {
		return ManagedProvider{}, err
	}
	if err := s.storeCredential(ctx, created, name, key); err != nil {
		// Keep a concurrently changed provider intact.
		return ManagedProvider{}, errors.Join(err, s.repository.Delete(ctx, created))
	}
	return managedProvider(created)
}

func (s *ProviderManagement) storeCredential(ctx context.Context, owner *unstructured.Unstructured, name string, key *string) error {
	if key == nil {
		return nil
	}
	if err := s.repository.StoreCredential(ctx, owner, name, *key); err != nil {
		return ErrProviderCredentials
	}
	return nil
}

func prepareCredential(spec map[string]any, key *string) (string, error) {
	if key == nil {
		return "", nil
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", fmt.Errorf("generate credential identifier: %w", err)
	}
	name := "kubereel-provider-" + hex.EncodeToString(random[:])
	spec["secretRef"] = map[string]any{"name": name, "key": repository.ProviderAPIKey}
	return name, nil
}

func applyProviderChanges(spec map[string]any, c ProviderChanges) {
	for field, value := range map[string]*[]string{"capabilities": c.Capabilities, "models": c.Models} {
		if value == nil {
			continue
		}
		items := make([]any, len(*value))
		for i, v := range *value {
			items[i] = v
		}
		spec[field] = items
	}
	for field, value := range map[string]*int64{"maxConcurrent": c.MaxConcurrent, "timeoutSeconds": c.TimeoutSeconds} {
		if value == nil {
			continue
		}
		if *value == 0 {
			delete(spec, field)
			continue
		}
		spec[field] = *value
	}
	if c.BaseURL != nil {
		delete(spec, "baseURL")
		if *c.BaseURL != "" {
			spec["baseURL"] = *c.BaseURL
		}
	}
	if c.Enabled != nil {
		spec["enabled"] = *c.Enabled
	}
}

func validateProvider(spec map[string]any, key *string) error {
	kind, _ := spec["type"].(string)
	allowed := []string{"text", "image", "video"}
	var validateModel func(string) error
	var endpoint string
	switch kind {
	case "mock":
		if key != nil {
			return invalidProvider("mock does not accept an API key")
		}
	case "gemini":
		allowed, validateModel, endpoint = []string{"text"}, gemini.ValidateModel, gemini.DefaultBaseURL
	case "fal":
		allowed, validateModel, endpoint = []string{"image", "video"}, fal.ValidateModel, fal.DefaultQueueBaseURL
	default:
		return invalidProvider("type must be mock, gemini, or fal")
	}
	if key != nil && (len(*key) > 4096 || *key == "" || strings.IndexFunc(*key, func(r rune) bool { return r < 33 || r > 126 }) >= 0) {
		return invalidProvider("apiKey must contain 1 to 4096 visible ASCII characters")
	}
	if base, _ := spec["baseURL"].(string); base != "" && base != endpoint {
		return invalidProvider("baseURL must be the provider's official endpoint")
	}
	if n, _ := spec["maxConcurrent"].(int64); n < 0 || n > 100 {
		return invalidProvider("maxConcurrent must be between 0 and 100")
	}
	if n, _ := spec["timeoutSeconds"].(int64); n < 0 || n > 86400 {
		return invalidProvider("timeoutSeconds must be between 0 and 86400")
	}
	caps, _, err := unstructured.NestedStringSlice(spec, "capabilities")
	if err != nil || len(caps) == 0 || len(caps) > len(allowed) {
		return invalidProvider("capabilities must be supported by the provider")
	}
	seen := make(map[string]bool)
	for _, capability := range caps {
		if !slices.Contains(allowed, capability) || seen[capability] {
			return invalidProvider("capabilities must be unique and supported by the provider")
		}
		seen[capability] = true
	}
	models, _, err := unstructured.NestedStringSlice(spec, "models")
	if err != nil || len(models) > 100 {
		return invalidProvider("at most 100 models are allowed")
	}
	clear(seen)
	for _, model := range models {
		if len(model) == 0 || len(model) > 253 || seen[model] || (validateModel != nil && validateModel(model) != nil) {
			return invalidProvider("models must be unique and valid for the provider")
		}
		seen[model] = true
	}
	return nil
}

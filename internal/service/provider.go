// Package service implements KubeReel application business logic.
package service

import (
	"errors"

	"github.com/tnqbao/kubereel/internal/provider"
)

var ErrProviderUnavailable = errors.New("provider is unavailable")

// ProviderService coordinates provider availability and metadata queries.
type ProviderService struct {
	manager *provider.Manager
}

// NewProviderService wraps manager in a ProviderService.
func NewProviderService(manager *provider.Manager) *ProviderService {
	return &ProviderService{manager: manager}
}

// List returns the registered providers and their execution availability.
func (s *ProviderService) List() ([]provider.Info, error) {
	if s == nil || s.manager == nil {
		return nil, ErrProviderUnavailable
	}

	return s.manager.List(), nil
}

// IsAvailable reports whether providerID can accept workflow executions.
func (s *ProviderService) IsAvailable(providerID string) bool {
	if s == nil || s.manager == nil {
		return false
	}

	return s.manager.Available(providerID)
}

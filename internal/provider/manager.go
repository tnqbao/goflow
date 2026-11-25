// Package provider manages the available execution providers.
package provider

// Info describes a provider's execution availability.
type Info struct {
	ID        string `json:"id"`
	Available bool   `json:"available"`
}

// Manager maintains the providers available to the control plane.
type Manager struct {
	providers []Info
}

// NewManager initializes the built-in provider registry.
func NewManager() *Manager {
	return &Manager{
		providers: []Info{
			{ID: "mock", Available: true},
		},
	}
}

// List returns a copy so callers cannot mutate the registry.
func (m *Manager) List() []Info {
	return append([]Info(nil), m.providers...)
}

// Available reports whether a provider can execute workflows.
func (m *Manager) Available(id string) bool {
	for _, info := range m.providers {
		if info.ID == id {
			return info.Available
		}
	}

	return false
}

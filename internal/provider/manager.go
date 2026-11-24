package provider

// Info describes a provider's execution availability.
type Info struct {
	ID        string `json:"id"`
	Available bool   `json:"available"`
}

// Manager maintains the providers available to the control plane.
type Manager struct{ providers []Info }

// NewManager initializes the built-in provider registry.
func NewManager() *Manager {
	return &Manager{providers: []Info{{ID: "mock", Available: true}, {ID: "google-flow", Available: false}}}
}

// List returns a copy so callers cannot change the registry.
func (m *Manager) List() []Info { return append([]Info(nil), m.providers...) }

// Available reports whether a provider can execute workflows.
func (m *Manager) Available(id string) bool {
	for _, p := range m.providers {
		if p.ID == id {
			return p.Available
		}
	}
	return false
}

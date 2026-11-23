// Package provider defines the connection boundary for external providers.
package provider

import (
	"context"
	"fmt"
)

// Provider describes connection operations. An implementation must return an
// UnsupportedError for operations it cannot perform.
// A connected state must represent a verified provider session.
type Provider interface {
	Connect(ctx context.Context) error
	Status(ctx context.Context) (ConnectionState, error)
	Disconnect(ctx context.Context) error
}

// ConnectionState describes the state of a provider session.
type ConnectionState string

const (
	// StateUnknown means the session has not been checked or cannot be determined.
	StateUnknown ConnectionState = "unknown"
	// StateDisconnected means there is no active session.
	StateDisconnected ConnectionState = "disconnected"
	// StateConnecting means a connection attempt is in progress.
	StateConnecting ConnectionState = "connecting"
	// StateConnected means an active session has been verified.
	StateConnected ConnectionState = "connected"
	// StateExpired means a previously active session needs authentication again.
	StateExpired ConnectionState = "expired"
)

// Valid reports whether the state is a defined connection state.
func (s ConnectionState) Valid() bool {
	switch s {
	case StateUnknown, StateDisconnected, StateConnecting, StateConnected, StateExpired:
		return true
	default:
		return false
	}
}

// UnsupportedError identifies an operation that a provider cannot perform.
type UnsupportedError struct {
	Operation string
}

func (e *UnsupportedError) Error() string {
	return fmt.Sprintf("provider operation %q is unsupported", e.Operation)
}

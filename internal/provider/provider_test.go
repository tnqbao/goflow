package provider

import (
	"errors"
	"fmt"
	"testing"
)

func TestConnectionStateValid(t *testing.T) {
	for _, tt := range []struct {
		state ConnectionState
		valid bool
	}{
		{StateUnknown, true},
		{StateDisconnected, true},
		{StateConnecting, true},
		{StateConnected, true},
		{StateExpired, true},
		{"", false},
		{"unsupported-state", false},
	} {
		t.Run(string(tt.state), func(t *testing.T) {
			if got := tt.state.Valid(); got != tt.valid {
				t.Errorf("%q.Valid() = %t, want %t", tt.state, got, tt.valid)
			}
		})
	}
}

func TestUnsupportedError(t *testing.T) {
	err := fmt.Errorf("connect: %w", &UnsupportedError{Operation: "connect"})
	var unsupported *UnsupportedError
	if !errors.As(err, &unsupported) {
		t.Fatalf("expected UnsupportedError, got %v", err)
	}
	if unsupported.Operation != "connect" {
		t.Errorf("operation = %q, want connect", unsupported.Operation)
	}
	if unsupported.Error() != `provider operation "connect" is unsupported` {
		t.Errorf("unexpected error message: %q", unsupported.Error())
	}
}

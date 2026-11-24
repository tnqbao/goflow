package provider

import "testing"

func TestManagerAvailability(t *testing.T) {
	manager := NewManager()
	if !manager.Available("mock") || manager.Available("unknown") || len(manager.List()) != 1 {
		t.Fatal("incorrect provider availability")
	}
	items := manager.List()
	items[0].Available = false
	if !manager.Available("mock") {
		t.Fatal("provider list changed the registry")
	}
}

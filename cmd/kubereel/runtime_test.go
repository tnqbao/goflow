package main

import (
	"context"
	"testing"
)

func TestControllerConfiguration(t *testing.T) {
	t.Setenv("KUBEREEL_EXECUTION_MODE", "")
	controller, err := controllerFromEnv(context.Background(), nil, t.TempDir())
	if err != nil || controller != nil {
		t.Fatalf("disabled controller = %v, %v", controller, err)
	}
	t.Setenv("KUBEREEL_EXECUTION_MODE", "unknown")
	if _, err := controllerFromEnv(context.Background(), nil, t.TempDir()); err == nil {
		t.Fatal("accepted invalid mode")
	}
	t.Setenv("KUBEREEL_EXECUTION_MODE", "kubernetes")
	t.Setenv("KUBEREEL_NAMESPACE", "")
	if _, err := controllerFromEnv(context.Background(), nil, t.TempDir()); err == nil {
		t.Fatal("accepted missing namespace")
	}
}

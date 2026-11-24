package main

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
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

func TestStorageAccessModes(t *testing.T) {
	for _, tt := range []struct {
		modes  []corev1.PersistentVolumeAccessMode
		shared bool
		want   bool
	}{
		{[]corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}, false, true},
		{[]corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}, true, false},
		{[]corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany}, true, true},
		{[]corev1.PersistentVolumeAccessMode{corev1.ReadWriteOncePod}, false, false},
	} {
		if got := supportsStorage(tt.modes, tt.shared); got != tt.want {
			t.Errorf("modes %v shared %v: got %v, want %v", tt.modes, tt.shared, got, tt.want)
		}
	}
}

func TestParseJobTTL(t *testing.T) {
	for _, value := range []string{"-1", "not-a-number", "2147483648"} {
		if _, err := parseJobTTL(value); err == nil {
			t.Fatalf("accepted retention %q", value)
		}
	}
	ttl, err := parseJobTTL("0")
	if err != nil || ttl == nil || *ttl != 0 {
		t.Fatalf("zero retention = %v, %v", ttl, err)
	}
	ttl, err = parseJobTTL("")
	if err != nil || ttl != nil {
		t.Fatalf("default retention = %v, %v", ttl, err)
	}
}

package kubernetes

import (
	"context"
	"testing"

	"github.com/tnqbao/kubereel/internal/config"
	corev1 "k8s.io/api/core/v1"
)

func TestNewControllerFromClusterDisabledAndValidation(t *testing.T) {
	cfg := config.Defaults()

	controller, err := NewControllerFromCluster(context.Background(), cfg, nil, nil)
	if err != nil || controller != nil {
		t.Fatalf("disabled controller = %v, %v", controller, err)
	}

	cfg.Kubernetes = config.KubernetesConfig{
		Enabled:           true,
		Namespace:         "kubereel",
		MaxConcurrentJobs: 4,
	}
	if _, err := NewControllerFromCluster(context.Background(), cfg, nil, nil); err == nil {
		t.Fatal("accepted incomplete Kubernetes job configuration")
	}
}

func TestSupportsStorageAccessModes(t *testing.T) {
	tests := []struct {
		modes  []corev1.PersistentVolumeAccessMode
		shared bool
		want   bool
	}{
		{[]corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}, false, true},
		{[]corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}, true, false},
		{[]corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany}, true, true},
		{[]corev1.PersistentVolumeAccessMode{corev1.ReadWriteOncePod}, false, false},
	}

	for _, tt := range tests {
		if got := SupportsStorageAccessModes(tt.modes, tt.shared); got != tt.want {
			t.Errorf("modes %v shared %v: got %v, want %v", tt.modes, tt.shared, got, tt.want)
		}
	}
}

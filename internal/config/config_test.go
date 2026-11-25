package config

import (
	"path/filepath"
	"testing"
)

func TestLoadDefaultsAndOverrides(t *testing.T) {
	clearConfigEnv(t)

	got, err := Load()
	if err != nil || got != Defaults() {
		t.Fatalf("Load() = %+v, %v, want %+v", got, err, Defaults())
	}

	dataDir := t.TempDir()
	t.Setenv(EnvDataDir, dataDir)

	got, err = Load()
	if err != nil || got.Address != ":8080" || got.Database != filepath.Join(dataDir, "kubereel.db") {
		t.Fatalf("Load() with override = %+v, %v", got, err)
	}
}

func TestInvalidArtifactConfiguration(t *testing.T) {
	clearConfigEnv(t)

	for _, envName := range []string{EnvArtifactMaxBytes, EnvArtifactRetentionSeconds} {
		t.Run(envName, func(t *testing.T) {
			t.Setenv(envName, "-1")
			if _, err := Load(); err == nil {
				t.Fatal("accepted negative value")
			}
		})
	}

	t.Setenv(EnvArtifactMaxBytes, "0")
	if _, err := Load(); err == nil {
		t.Fatal("accepted zero size limit")
	}
}

func TestKubernetesConfiguration(t *testing.T) {
	clearConfigEnv(t)

	t.Setenv(EnvExecutionMode, "unknown")
	if _, err := Load(); err == nil {
		t.Fatal("accepted invalid execution mode")
	}

	t.Setenv(EnvExecutionMode, "kubernetes")
	t.Setenv(EnvNamespace, "")
	if _, err := Load(); err == nil {
		t.Fatal("accepted missing namespace")
	}

	t.Setenv(EnvNamespace, "kubereel")
	t.Setenv(EnvJobImage, "kubereel:0.1.0")
	t.Setenv(EnvPVCName, "kubereel-data")
	t.Setenv(EnvInstanceName, "kubereel")
	t.Setenv(EnvSharedStorage, "true")
	t.Setenv(EnvMaxConcurrentJobs, "6")
	t.Setenv(EnvJobTTLSeconds, "3600")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() kubernetes config error = %v", err)
	}

	if !cfg.Kubernetes.Enabled || cfg.Kubernetes.Namespace != "kubereel" || !cfg.Kubernetes.SharedStorage {
		t.Fatalf("unexpected kubernetes config = %+v", cfg.Kubernetes)
	}

	if cfg.Kubernetes.MaxConcurrentJobs != 6 || cfg.Kubernetes.JobTTLSeconds == nil || *cfg.Kubernetes.JobTTLSeconds != 3600 {
		t.Fatalf("unexpected job limits = %+v", cfg.Kubernetes)
	}
}

func TestParseJobTTL(t *testing.T) {
	for _, rawValue := range []string{"-1", "not-a-number", "2147483648"} {
		if _, err := ParseJobTTL(rawValue); err == nil {
			t.Fatalf("accepted retention %q", rawValue)
		}
	}

	ttl, err := ParseJobTTL("0")
	if err != nil || ttl == nil || *ttl != 0 {
		t.Fatalf("zero retention = %v, %v", ttl, err)
	}

	ttl, err = ParseJobTTL("")
	if err != nil || ttl != nil {
		t.Fatalf("default retention = %v, %v", ttl, err)
	}
}

func clearConfigEnv(t *testing.T) {
	t.Helper()

	for _, envName := range []string{
		EnvDataDir,
		EnvArtifactMaxBytes,
		EnvArtifactRetentionSeconds,
		EnvExecutionMode,
		EnvNamespace,
		EnvJobImage,
		EnvImagePullPolicy,
		EnvPVCName,
		EnvInstanceName,
		EnvSharedStorage,
		EnvMaxConcurrentJobs,
		EnvJobTTLSeconds,
	} {
		t.Setenv(envName, "")
	}
}

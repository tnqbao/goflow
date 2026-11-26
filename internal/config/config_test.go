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

func TestAuthConfiguration(t *testing.T) {
	clearConfigEnv(t)

	t.Setenv(EnvAuthSessionTTLSeconds, "30")
	if _, err := Load(); err == nil {
		t.Fatal("accepted session TTL below 60 seconds")
	}

	t.Setenv(EnvAuthSessionTTLSeconds, "3600")
	t.Setenv(EnvAuthBootstrapPassword, "super-secret-bootstrap-pass")
	t.Setenv(EnvAuthSetupToken, "setup-tok-123")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() auth config error = %v", err)
	}
	if cfg.Auth.BootstrapUsername != "admin" || cfg.Auth.SetupToken != "setup-tok-123" {
		t.Fatalf("unexpected auth config = %+v", cfg.Auth)
	}
}

func TestStorageS3Configuration(t *testing.T) {
	clearConfigEnv(t)

	t.Setenv(EnvStorageType, "nfs")
	if _, err := Load(); err == nil {
		t.Fatal("accepted unsupported storage type")
	}

	t.Setenv(EnvStorageType, StorageTypeS3)
	if _, err := Load(); err == nil {
		t.Fatal("accepted S3 storage without bucket and region")
	}

	t.Setenv(EnvS3Bucket, "kubereel-prod-artifacts")
	t.Setenv(EnvS3Region, "us-west-2")
	t.Setenv(EnvS3Prefix, "/cluster-1/")
	t.Setenv(EnvS3Endpoint, "https://s3.example.com")
	t.Setenv(EnvS3ForcePathStyle, "true")
	t.Setenv(EnvS3CredentialsSecret, "s3-creds")
	t.Setenv(EnvExecutionMode, "kubernetes")
	t.Setenv(EnvNamespace, "kubereel")
	t.Setenv(EnvWorkerServiceAccount, "kubereel-worker")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() S3 config error = %v", err)
	}
	if cfg.Storage.Type != StorageTypeS3 || cfg.Storage.S3.Prefix != "cluster-1" || !cfg.Storage.S3.ForcePathStyle {
		t.Fatalf("unexpected S3 storage config = %+v", cfg.Storage)
	}
	if cfg.Storage.S3.AccessKeyIDKey != "accessKeyId" || cfg.Storage.S3.SecretAccessKeyKey != "secretAccessKey" {
		t.Fatalf("unexpected S3 credential key defaults = %+v", cfg.Storage.S3)
	}
	if cfg.Kubernetes.WorkerServiceAccount != "kubereel-worker" {
		t.Fatalf("unexpected worker service account = %q", cfg.Kubernetes.WorkerServiceAccount)
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
		EnvAuthSessionTTLSeconds,
		EnvAuthBootstrapUsername,
		EnvAuthBootstrapPassword,
		EnvAuthSetupToken,
		EnvStorageType,
		EnvS3Bucket,
		EnvS3Region,
		EnvS3Prefix,
		EnvS3Endpoint,
		EnvS3ForcePathStyle,
		EnvS3CredentialsSecret,
		EnvS3AccessKeyIDKey,
		EnvS3SecretAccessKeyKey,
		EnvWorkerServiceAccount,
	} {
		t.Setenv(envName, "")
	}
}

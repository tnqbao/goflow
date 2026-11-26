// Package config loads and validates KubeReel's runtime settings.
package config

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

const (
	EnvDataDir                  = "DATA_DIR"
	EnvArtifactMaxBytes         = "ARTIFACT_MAX_BYTES"
	EnvArtifactRetentionSeconds = "ARTIFACT_RETENTION_SECONDS"
	EnvExecutionMode            = "EXECUTION_MODE"
	EnvNamespace                = "NAMESPACE"
	EnvJobImage                 = "JOB_IMAGE"
	EnvImagePullPolicy          = "IMAGE_PULL_POLICY"
	EnvPVCName                  = "PVC_NAME"
	EnvInstanceName             = "INSTANCE_NAME"
	EnvSharedStorage            = "SHARED_STORAGE"
	EnvMaxConcurrentJobs        = "MAX_CONCURRENT_JOBS"
	EnvJobTTLSeconds            = "JOB_TTL_SECONDS"
	EnvAuthSessionTTLSeconds    = "AUTH_SESSION_TTL_SECONDS"
	EnvAuthBootstrapUsername    = "AUTH_BOOTSTRAP_USERNAME"
	EnvAuthBootstrapPassword    = "AUTH_BOOTSTRAP_PASSWORD"
	EnvAuthSetupToken           = "AUTH_SETUP_TOKEN"
	EnvStorageType              = "STORAGE_TYPE"
	EnvS3Bucket                 = "S3_BUCKET"
	EnvS3Region                 = "S3_REGION"
	EnvS3Prefix                 = "S3_PREFIX"
	EnvS3Endpoint               = "S3_ENDPOINT"
	EnvS3ForcePathStyle         = "S3_FORCE_PATH_STYLE"
	EnvS3CredentialsSecret      = "S3_CREDENTIALS_SECRET"
	EnvS3AccessKeyIDKey         = "S3_ACCESS_KEY_ID_KEY"
	EnvS3SecretAccessKeyKey     = "S3_SECRET_ACCESS_KEY_KEY"
	EnvWorkerServiceAccount     = "WORKER_SERVICE_ACCOUNT"
)

const (
	defaultAddress           = ":8080"
	defaultDatabasePath      = "/data/kubereel.db"
	defaultArtifactMaxBytes  = 1 << 30
	defaultSessionTTLSeconds = 86400
	maxSessionTTLSeconds     = 31536000

	// MaxArtifactBytesLimit caps artifact sizes to the largest exact IEEE-754 integer
	// so values survive unstructured Kubernetes JSON round-trips without precision loss.
	MaxArtifactBytesLimit int64 = 9007199254740991
)

// AuthConfig holds optional bootstrap credentials and session settings.
type AuthConfig struct {
	SessionTTL        time.Duration
	BootstrapUsername string
	BootstrapPassword string
	SetupToken        string
}

// Config holds the application settings across standalone and Kubernetes modes.
type Config struct {
	Address           string
	Database          string
	ArtifactMaxBytes  int64
	ArtifactRetention time.Duration
	Storage           StorageConfig
	Auth              AuthConfig
	Kubernetes        KubernetesConfig
}

// Defaults returns the baseline application settings.
func Defaults() Config {
	return Config{
		Address:          defaultAddress,
		Database:         defaultDatabasePath,
		ArtifactMaxBytes: defaultArtifactMaxBytes,
		Storage: StorageConfig{
			Type: StorageTypePVC,
		},
		Auth: AuthConfig{
			SessionTTL: defaultSessionTTLSeconds * time.Second,
		},
	}
}

// Load reads and validates all application settings from environment variables.
func Load() (Config, error) {
	cfg := Defaults()

	if dataDir := os.Getenv(EnvDataDir); dataDir != "" {
		cfg.Database = filepath.Join(dataDir, "kubereel.db")
	}

	maxBytes, err := loadArtifactMaxBytes(cfg.ArtifactMaxBytes)
	if err != nil {
		return Config{}, err
	}
	cfg.ArtifactMaxBytes = maxBytes

	retention, err := loadArtifactRetention()
	if err != nil {
		return Config{}, err
	}
	cfg.ArtifactRetention = retention

	storageCfg, err := loadStorageConfig()
	if err != nil {
		return Config{}, err
	}
	cfg.Storage = storageCfg

	k8sCfg, err := loadKubernetesConfig()
	if err != nil {
		return Config{}, err
	}
	cfg.Kubernetes = k8sCfg

	authCfg, err := loadAuthConfig(cfg.Auth)
	if err != nil {
		return Config{}, err
	}
	cfg.Auth = authCfg

	return cfg, nil
}

func loadAuthConfig(defaults AuthConfig) (AuthConfig, error) {
	ttlSeconds, err := parseNonNegativeInt64(EnvAuthSessionTTLSeconds, int64(defaults.SessionTTL/time.Second))
	if err != nil {
		return AuthConfig{}, err
	}
	if ttlSeconds < 60 || ttlSeconds > maxSessionTTLSeconds {
		return AuthConfig{}, fmt.Errorf("%s must be between 60 and %d", EnvAuthSessionTTLSeconds, maxSessionTTLSeconds)
	}

	username := os.Getenv(EnvAuthBootstrapUsername)
	password := os.Getenv(EnvAuthBootstrapPassword)
	if password != "" && username == "" {
		username = "admin"
	}

	return AuthConfig{
		SessionTTL:        time.Duration(ttlSeconds) * time.Second,
		BootstrapUsername: username,
		BootstrapPassword: password,
		SetupToken:        os.Getenv(EnvAuthSetupToken),
	}, nil
}

func loadArtifactMaxBytes(fallback int64) (int64, error) {
	maxBytes, err := parseNonNegativeInt64(EnvArtifactMaxBytes, fallback)
	if err != nil {
		return 0, err
	}

	if maxBytes == 0 || maxBytes > MaxArtifactBytesLimit {
		return 0, fmt.Errorf("%s must be between 1 and %d", EnvArtifactMaxBytes, MaxArtifactBytesLimit)
	}

	return maxBytes, nil
}

func loadArtifactRetention() (time.Duration, error) {
	seconds, err := parseNonNegativeInt64(EnvArtifactRetentionSeconds, 0)
	if err != nil {
		return 0, err
	}

	if seconds > math.MaxInt64/int64(time.Second) {
		return 0, fmt.Errorf("%s is too large", EnvArtifactRetentionSeconds)
	}

	return time.Duration(seconds) * time.Second, nil
}

func parseNonNegativeInt64(envName string, fallback int64) (int64, error) {
	rawValue := os.Getenv(envName)
	if rawValue == "" {
		return fallback, nil
	}

	parsed, err := strconv.ParseInt(rawValue, 10, 64)
	if err != nil || parsed < 0 {
		return 0, fmt.Errorf("%s must be a non-negative integer", envName)
	}

	return parsed, nil
}

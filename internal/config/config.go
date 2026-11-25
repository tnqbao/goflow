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
)

const (
	defaultAddress          = ":8080"
	defaultDatabasePath     = "/data/kubereel.db"
	defaultArtifactMaxBytes = 1 << 30

	// MaxArtifactBytesLimit caps artifact sizes to the largest exact IEEE-754 integer
	// so values survive unstructured Kubernetes JSON round-trips without precision loss.
	MaxArtifactBytesLimit int64 = 9007199254740991
)

// Config holds the application settings across standalone and Kubernetes modes.
type Config struct {
	Address           string
	Database          string
	ArtifactMaxBytes  int64
	ArtifactRetention time.Duration
	Kubernetes        KubernetesConfig
}

// Defaults returns the baseline application settings.
func Defaults() Config {
	return Config{
		Address:          defaultAddress,
		Database:         defaultDatabasePath,
		ArtifactMaxBytes: defaultArtifactMaxBytes,
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

	k8sCfg, err := loadKubernetesConfig()
	if err != nil {
		return Config{}, err
	}
	cfg.Kubernetes = k8sCfg

	return cfg, nil
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

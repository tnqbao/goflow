// Package config provides KubeReel's settings.
package config

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// Config holds the application settings.
type Config struct {
	Address           string
	Database          string
	ArtifactMaxBytes  int64
	ArtifactRetention time.Duration
}

// Defaults returns the default application settings.
func Defaults() Config {
	return Config{
		Address:          ":8080",
		Database:         "/data/kubereel.db",
		ArtifactMaxBytes: 1 << 30,
	}
}

// Load returns the default settings with an optional data directory override.
func Load() (Config, error) {
	cfg := Defaults()
	dir := os.Getenv("KUBEREEL_DATA_DIR")
	if dir != "" {
		cfg.Database = filepath.Join(dir, "kubereel.db")
	}
	maxBytes, err := parseNonnegative("KUBEREEL_ARTIFACT_MAX_BYTES", cfg.ArtifactMaxBytes)
	if err != nil {
		return Config{}, err
	}
	if maxBytes == 0 || maxBytes > 9007199254740991 {
		return Config{}, fmt.Errorf("KUBEREEL_ARTIFACT_MAX_BYTES must be between 1 and %d", int64(9007199254740991))
	}
	cfg.ArtifactMaxBytes = maxBytes
	seconds, err := parseNonnegative("KUBEREEL_ARTIFACT_RETENTION_SECONDS", 0)
	if err != nil {
		return Config{}, err
	}
	if seconds > math.MaxInt64/int64(time.Second) {
		return Config{}, fmt.Errorf("KUBEREEL_ARTIFACT_RETENTION_SECONDS is too large")
	}
	cfg.ArtifactRetention = time.Duration(seconds) * time.Second
	return cfg, nil
}

func parseNonnegative(name string, fallback int64) (int64, error) {
	value := os.Getenv(name)
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed < 0 {
		return 0, fmt.Errorf("%s must be a non-negative integer", name)
	}
	return parsed, nil
}

// Package config provides KubeReel's settings.
package config

import (
	"os"
	"path/filepath"
)

// Config holds the application settings.
type Config struct {
	Address  string
	Database string
}

// Defaults returns the default application settings.
func Defaults() Config {
	return Config{
		Address:  ":8080",
		Database: "/data/kubereel.db",
	}
}

// Load returns the default settings with an optional data directory override.
func Load() Config {
	cfg := Defaults()
	dir := os.Getenv("KUBEREEL_DATA_DIR")
	if dir == "" {
		return cfg
	}
	cfg.Database = filepath.Join(dir, "kubereel.db")
	return cfg
}

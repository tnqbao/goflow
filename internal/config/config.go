// Package config provides GoFlow's settings.
package config

import (
	"os"
	"path/filepath"
)

// Config holds the application settings.
type Config struct {
	Address     string
	Database    string
	BrowserData string
}

// Defaults returns the default application settings.
func Defaults() Config {
	return Config{
		Address:     ":8080",
		Database:    "/data/goflow.db",
		BrowserData: "/data/browser",
	}
}

// Load returns the default settings with an optional data directory override.
func Load() Config {
	cfg := Defaults()
	dir := os.Getenv("GOFLOW_DATA_DIR")
	if dir == "" {
		return cfg
	}
	cfg.Database = filepath.Join(dir, "goflow.db")
	cfg.BrowserData = filepath.Join(dir, "browser")
	return cfg
}

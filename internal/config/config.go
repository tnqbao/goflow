// Package config provides GoFlow's default settings.
package config

// Config holds the application's default settings.
type Config struct {
	Address     string
	Database    string
	BrowserData string
}

// Defaults returns the settings used when GoFlow starts.
func Defaults() Config {
	return Config{
		Address:     ":8080",
		Database:    "/data/goflow.db",
		BrowserData: "/data/browser",
	}
}

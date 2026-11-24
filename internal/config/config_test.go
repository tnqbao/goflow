package config

import (
	"path/filepath"
	"testing"
)

func TestLoad(t *testing.T) {
	t.Setenv("KUBEREEL_DATA_DIR", "")
	t.Setenv("GOFLOW_DATA_DIR", "")
	if got := Load(); got != Defaults() {
		t.Errorf("Load() = %+v, want %+v", got, Defaults())
	}
	dir := t.TempDir()
	t.Setenv("GOFLOW_DATA_DIR", dir)
	got := Load()
	if got.Address != ":8080" || got.Database != filepath.Join(dir, "goflow.db") {
		t.Errorf("Load() with override = %+v", got)
	}
}

func TestKubeReelDataDirectoryTakesPrecedence(t *testing.T) {
	t.Setenv("GOFLOW_DATA_DIR", t.TempDir())
	dir := t.TempDir()
	t.Setenv("KUBEREEL_DATA_DIR", dir)
	if got := Load().Database; got != filepath.Join(dir, "goflow.db") {
		t.Errorf("database = %q", got)
	}
}

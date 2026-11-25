package config

import (
	"path/filepath"
	"testing"
)

func TestLoad(t *testing.T) {
	t.Setenv("KUBEREEL_DATA_DIR", "")
	t.Setenv("KUBEREEL_ARTIFACT_MAX_BYTES", "")
	t.Setenv("KUBEREEL_ARTIFACT_RETENTION_SECONDS", "")
	got, err := Load()
	if err != nil || got != Defaults() {
		t.Errorf("Load() = %+v, %v, want %+v", got, err, Defaults())
	}
	dir := t.TempDir()
	t.Setenv("KUBEREEL_DATA_DIR", dir)
	got, err = Load()
	if err != nil || got.Address != ":8080" || got.Database != filepath.Join(dir, "kubereel.db") {
		t.Errorf("Load() with override = %+v, %v", got, err)
	}
}

func TestInvalidArtifactConfiguration(t *testing.T) {
	for _, name := range []string{"KUBEREEL_ARTIFACT_MAX_BYTES", "KUBEREEL_ARTIFACT_RETENTION_SECONDS"} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(name, "-1")
			if _, err := Load(); err == nil {
				t.Fatal("accepted negative value")
			}
		})
	}
	t.Setenv("KUBEREEL_ARTIFACT_MAX_BYTES", "0")
	if _, err := Load(); err == nil {
		t.Fatal("accepted zero size limit")
	}
}

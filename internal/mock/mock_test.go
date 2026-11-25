package mock

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tnqbao/kubereel/internal/renderer"
	"github.com/tnqbao/kubereel/internal/storage"
)

func TestExecuteLLMImageAndVideo(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	artifacts, err := storage.NewFilesystem(dir, 16<<20, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer artifacts.Close()

	if err := os.WriteFile(filepath.Join(dir, "llm.json"), []byte(`{"type":"llm","prompt":"Forest mystery"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Execute(ctx, artifacts, "llm.json", "script.json"); err != nil {
		t.Fatalf("Execute llm: %v", err)
	}
	if err := ValidateScript(ctx, artifacts, "script.json"); err != nil {
		t.Fatalf("ValidateScript: %v", err)
	}

	if err := os.WriteFile(filepath.Join(dir, "image.json"), []byte(`{"prompt":"Cat & <bird>"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Execute(ctx, artifacts, "image.json", "scene.svg"); err != nil {
		t.Fatalf("Execute image: %v", err)
	}
	if err := ValidateSVG(ctx, artifacts, "scene.svg"); err != nil {
		t.Fatalf("ValidateSVG: %v", err)
	}

	svgBytes, err := os.ReadFile(filepath.Join(dir, "scene.svg"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(svgBytes), "Cat &amp; &lt;bird&gt;") {
		t.Fatalf("unescaped or missing prompt: %s", svgBytes)
	}

	if _, err := exec.LookPath("ffmpeg"); err != nil {
		return
	}

	if err := os.WriteFile(filepath.Join(dir, "video.json"), []byte(`{"type":"video","prompt":"Forest clip"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Execute(ctx, artifacts, "video.json", "clip.mp4"); err != nil {
		t.Fatalf("Execute video: %v", err)
	}
	if err := renderer.ValidateMP4(ctx, artifacts, "clip.mp4"); err != nil {
		t.Fatalf("ValidateMP4 clip: %v", err)
	}
}

func TestRejectInvalidInputs(t *testing.T) {
	dir := t.TempDir()
	artifacts, err := storage.NewFilesystem(dir, 1<<20, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer artifacts.Close()

	badPayloads := map[string]string{
		"empty.json":   `{"prompt":""}`,
		"unknown.json": `{"type":"audio","prompt":"hello"}`,
		"extra.json":   `{"prompt":"hello","unexpected":true}`,
		"trail.json":   `{"prompt":"hello"}{}`,
	}
	for name, body := range badPayloads {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if err := Execute(context.Background(), artifacts, name, "out"); err == nil {
			t.Fatalf("accepted invalid payload %s", name)
		}
	}

	if _, err := os.Stat(filepath.Join(dir, "out")); !os.IsNotExist(err) {
		t.Fatalf("unexpected output: %v", err)
	}

	if err := ValidateSVG(context.Background(), artifacts, "empty.json"); err == nil {
		t.Fatal("invalid SVG accepted")
	}
	if err := ValidateScript(context.Background(), artifacts, "empty.json"); err == nil {
		t.Fatal("invalid script accepted")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Execute(ctx, artifacts, "empty.json", "out"); err == nil {
		t.Fatal("canceled request accepted")
	}
}

package mock

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tnqbao/kubereel/internal/storage"
)

func TestGenerateAndRender(t *testing.T) {
	dir := t.TempDir()
	artifacts, err := storage.NewFilesystem(dir, 1<<20, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer artifacts.Close()
	input := filepath.Join(dir, "request.json")
	final := filepath.Join(dir, "final.svg")
	if err := os.WriteFile(input, []byte(`{"prompt":"Cat & <bird>"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Generate(context.Background(), artifacts, "request.json", "scene.svg"); err != nil {
		t.Fatal(err)
	}
	if err := Render(context.Background(), artifacts, "scene.svg", "final.svg"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(final)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "Cat &amp; &lt;bird&gt;") {
		t.Fatalf("unescaped or missing prompt: %s", data)
	}
}

func TestRejectInvalidInputs(t *testing.T) {
	dir := t.TempDir()
	artifacts, err := storage.NewFilesystem(dir, 1<<20, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer artifacts.Close()
	input := filepath.Join(dir, "input")
	output := filepath.Join(dir, "output")
	if err := os.WriteFile(input, []byte(`{"prompt":""}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Generate(context.Background(), artifacts, "input", "output"); err == nil {
		t.Fatal("empty prompt accepted")
	}
	if err := Render(context.Background(), artifacts, "input", "output"); err == nil {
		t.Fatal("invalid SVG accepted")
	}
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Fatalf("unexpected output: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Generate(ctx, artifacts, "input", "output"); err == nil {
		t.Fatal("canceled request accepted")
	}
}

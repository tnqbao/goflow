package mock

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGenerateAndRender(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "request.json")
	scene := filepath.Join(dir, "scene.svg")
	final := filepath.Join(dir, "final.svg")
	if err := os.WriteFile(input, []byte(`{"prompt":"Cat & <bird>"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Generate(context.Background(), input, scene); err != nil {
		t.Fatal(err)
	}
	if err := Render(context.Background(), scene, final); err != nil {
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
	input := filepath.Join(dir, "input")
	output := filepath.Join(dir, "output")
	if err := os.WriteFile(input, []byte(`{"prompt":""}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Generate(context.Background(), input, output); err == nil {
		t.Fatal("empty prompt accepted")
	}
	if err := Render(context.Background(), input, output); err == nil {
		t.Fatal("invalid SVG accepted")
	}
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Fatalf("unexpected output: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Generate(ctx, input, output); err == nil {
		t.Fatal("canceled request accepted")
	}
}

// Package mock produces deterministic local artifacts without external services.
package mock

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const maxInput = 1 << 20

type Request struct {
	Prompt string `json:"prompt"`
}

// Generate creates an SVG placeholder for the requested scene.
func Generate(ctx context.Context, input, output string) error {
	data, err := readFile(ctx, input)
	if err != nil {
		return err
	}
	var req Request
	if err := json.Unmarshal(data, &req); err != nil {
		return fmt.Errorf("decode mock request: %w", err)
	}
	prompt := strings.TrimSpace(req.Prompt)
	if prompt == "" || len(prompt) > 8192 {
		return fmt.Errorf("invalid mock prompt")
	}
	var escaped bytes.Buffer
	if err := xml.EscapeText(&escaped, []byte(prompt)); err != nil {
		return fmt.Errorf("escape prompt: %w", err)
	}
	svg := `<svg xmlns="http://www.w3.org/2000/svg" width="1280" height="720" viewBox="0 0 1280 720"><rect width="1280" height="720" fill="#18202b"/><text x="64" y="90" fill="#fff" font-family="sans-serif" font-size="32">Mock scene</text><text x="64" y="150" fill="#fff" font-family="sans-serif" font-size="24">` + escaped.String() + `</text></svg>`
	return writeFile(ctx, output, []byte(svg))
}

// Render validates and copies a generated SVG as the final mock artifact.
func Render(ctx context.Context, input, output string) error {
	data, err := readFile(ctx, input)
	if err != nil {
		return err
	}
	decoder := xml.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil {
		return fmt.Errorf("read SVG: %w", err)
	}
	root, ok := token.(xml.StartElement)
	if !ok || root.Name.Local != "svg" || root.Name.Space != "http://www.w3.org/2000/svg" {
		return fmt.Errorf("invalid mock SVG")
	}
	for {
		_, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("validate SVG: %w", err)
		}
	}
	return writeFile(ctx, output, data)
}

func readFile(ctx context.Context, path string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open input: %w", err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxInput+1))
	if err != nil {
		return nil, fmt.Errorf("read input: %w", err)
	}
	if len(data) > maxInput {
		return nil, fmt.Errorf("input exceeds %d bytes", maxInput)
	}
	return data, nil
}

func writeFile(ctx context.Context, path string, data []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if path == "" {
		return fmt.Errorf("output path is required")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create output directory: %w", err)
	}
	file, err := os.CreateTemp(dir, ".kubereel-*")
	if err != nil {
		return fmt.Errorf("create output: %w", err)
	}
	defer os.Remove(file.Name())
	if _, err := file.Write(data); err != nil {
		return fmt.Errorf("write output: %w", errors.Join(err, file.Close()))
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close output: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Rename(file.Name(), path); err != nil {
		return fmt.Errorf("publish output: %w", err)
	}
	return nil
}

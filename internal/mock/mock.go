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

	providermock "github.com/tnqbao/kubereel/internal/provider/mock"
	"github.com/tnqbao/kubereel/internal/storage"
)

const maxInput = 1 << 20

type Request struct {
	Prompt string `json:"prompt"`
}

// Generate creates an SVG placeholder for the requested scene.
func Generate(ctx context.Context, artifacts storage.Store, input, output string) error {
	data, err := readFile(ctx, artifacts, input)
	if err != nil {
		return err
	}
	var req Request
	if err := json.Unmarshal(data, &req); err != nil {
		return fmt.Errorf("decode mock request: %w", err)
	}
	svg, err := providermock.SVG(req.Prompt)
	if err != nil {
		return err
	}
	return artifacts.Put(ctx, output, bytes.NewReader(svg))
}

// Render validates and copies a generated SVG as the final mock artifact.
func Render(ctx context.Context, artifacts storage.Store, input, output string) error {
	data, err := readFile(ctx, artifacts, input)
	if err != nil {
		return err
	}
	if err := validateSVG(data); err != nil {
		return err
	}
	return artifacts.Put(ctx, output, bytes.NewReader(data))
}

// ValidateSVG checks an existing Mock artifact without modifying it.
func ValidateSVG(ctx context.Context, artifacts storage.Store, input string) error {
	data, err := readFile(ctx, artifacts, input)
	if err != nil {
		return err
	}
	return validateSVG(data)
}

func validateSVG(data []byte) error {
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
	return nil
}

func readFile(ctx context.Context, artifacts storage.Store, key string) ([]byte, error) {
	file, err := artifacts.Open(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("open input: %w", err)
	}
	data, readErr := io.ReadAll(io.LimitReader(file, maxInput+1))
	if err := errors.Join(readErr, file.Close()); err != nil {
		return nil, fmt.Errorf("read input: %w", err)
	}
	if len(data) > maxInput {
		return nil, fmt.Errorf("input exceeds %d bytes", maxInput)
	}
	return data, nil
}

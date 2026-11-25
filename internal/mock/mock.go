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

const maxInputBytes = 1 << 20

// Request holds the input payload for a Mock worker generation step.
type Request struct {
	Prompt string `json:"prompt"`
}

// Generate creates an SVG placeholder for the requested scene.
func Generate(ctx context.Context, artifacts storage.Store, inputKey, outputKey string) error {
	data, err := readBoundedArtifact(ctx, artifacts, inputKey)
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

	return artifacts.Put(ctx, outputKey, bytes.NewReader(svg))
}

// Render validates and copies a generated SVG as the final mock artifact.
func Render(ctx context.Context, artifacts storage.Store, inputKey, outputKey string) error {
	data, err := readBoundedArtifact(ctx, artifacts, inputKey)
	if err != nil {
		return err
	}

	if err := validateSVGData(data); err != nil {
		return err
	}

	return artifacts.Put(ctx, outputKey, bytes.NewReader(data))
}

// ValidateSVG checks an existing Mock SVG artifact without modifying it.
func ValidateSVG(ctx context.Context, artifacts storage.Store, inputKey string) error {
	data, err := readBoundedArtifact(ctx, artifacts, inputKey)
	if err != nil {
		return err
	}

	return validateSVGData(data)
}

func validateSVGData(data []byte) error {
	decoder := xml.NewDecoder(bytes.NewReader(data))

	firstToken, err := decoder.Token()
	if err != nil {
		return fmt.Errorf("read SVG: %w", err)
	}

	rootElement, ok := firstToken.(xml.StartElement)
	if !ok || rootElement.Name.Local != "svg" || rootElement.Name.Space != "http://www.w3.org/2000/svg" {
		return fmt.Errorf("invalid mock SVG")
	}

	for {
		_, err := decoder.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("validate SVG: %w", err)
		}
	}
}

func readBoundedArtifact(ctx context.Context, artifacts storage.Store, key string) ([]byte, error) {
	object, err := artifacts.Open(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("open input: %w", err)
	}

	data, readErr := io.ReadAll(io.LimitReader(object, maxInputBytes+1))
	if err := errors.Join(readErr, object.Close()); err != nil {
		return nil, fmt.Errorf("read input: %w", err)
	}

	if len(data) > maxInputBytes {
		return nil, fmt.Errorf("input exceeds %d bytes", maxInputBytes)
	}

	return data, nil
}

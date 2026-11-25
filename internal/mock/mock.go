// Package mock executes deterministic local provider steps without external services.
package mock

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/tnqbao/kubereel/internal/provider"
	providermock "github.com/tnqbao/kubereel/internal/provider/mock"
	"github.com/tnqbao/kubereel/internal/storage"
)

const (
	StepLLM   = "llm"
	StepImage = "image"
	StepVideo = "video"

	maxInputBytes = 1 << 20
	pollInterval  = 10 * time.Millisecond
)

// Request holds the input payload for a Mock worker generation step.
type Request struct {
	Type   string `json:"type,omitempty"`
	Prompt string `json:"prompt"`
}

// ScriptOutput is the structured artifact written by a Mock LLM step.
type ScriptOutput struct {
	Prompt string   `json:"prompt"`
	Script string   `json:"script"`
	Scenes []string `json:"scenes"`
}

// Execute runs a single Mock provider step (llm, image, or video) and stores the resulting artifact.
func Execute(ctx context.Context, artifacts storage.Store, inputKey, outputKey string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if artifacts == nil {
		return fmt.Errorf("artifact storage is required")
	}

	data, err := readBoundedArtifact(ctx, artifacts, inputKey)
	if err != nil {
		return err
	}

	req, err := decodeRequest(data)
	if err != nil {
		return err
	}

	adapter, err := providermock.New(providermock.Options{})
	if err != nil {
		return err
	}

	switch req.Type {
	case StepLLM:
		return executeLLM(ctx, adapter, artifacts, req.Prompt, outputKey)
	case "", StepImage:
		return executeImage(ctx, adapter, artifacts, req.Prompt, outputKey)
	case StepVideo:
		return executeVideo(ctx, adapter, artifacts, req.Prompt, outputKey)
	default:
		return fmt.Errorf("unsupported mock step type %q", req.Type)
	}
}

// Generate creates an SVG placeholder or typed Mock step output for the requested input.
func Generate(ctx context.Context, artifacts storage.Store, inputKey, outputKey string) error {
	return Execute(ctx, artifacts, inputKey, outputKey)
}

func decodeRequest(data []byte) (Request, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()

	var req Request
	if err := decoder.Decode(&req); err != nil {
		return Request{}, fmt.Errorf("decode mock request: %w", err)
	}

	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		return Request{}, fmt.Errorf("decode mock request: unexpected trailing JSON data")
	}

	return req, nil
}

func executeLLM(
	ctx context.Context,
	adapter *providermock.Provider,
	artifacts storage.Store,
	prompt, outputKey string,
) error {
	result, err := adapter.Generate(ctx, provider.TextRequest{Prompt: prompt})
	if err != nil {
		return err
	}

	trimmed := strings.TrimSpace(prompt)
	output := ScriptOutput{
		Prompt: trimmed,
		Script: result.Text,
		Scenes: []string{
			trimmed + " — Scene 1",
			trimmed + " — Scene 2",
		},
	}

	encoded, err := json.Marshal(output)
	if err != nil {
		return fmt.Errorf("encode mock script: %w", err)
	}

	return artifacts.Put(ctx, outputKey, bytes.NewReader(encoded))
}

func executeImage(
	ctx context.Context,
	adapter *providermock.Provider,
	artifacts storage.Store,
	prompt, outputKey string,
) error {
	imageAdapter := adapter.Image()
	handle, err := imageAdapter.Submit(ctx, provider.ImageRequest{Prompt: prompt})
	if err != nil {
		return err
	}
	defer func() {
		_ = adapter.Forget(context.Background(), handle)
	}()

	if err := awaitGeneration(ctx, handle, imageAdapter.GetStatus); err != nil {
		return err
	}

	media, err := imageAdapter.GetResult(ctx, handle)
	if err != nil {
		return err
	}

	return storeMediaResult(ctx, artifacts, outputKey, media)
}

func executeVideo(
	ctx context.Context,
	adapter *providermock.Provider,
	artifacts storage.Store,
	prompt, outputKey string,
) error {
	videoAdapter := adapter.Video()
	handle, err := videoAdapter.Submit(ctx, provider.VideoRequest{Prompt: prompt})
	if err != nil {
		return err
	}
	defer func() {
		_ = adapter.Forget(context.Background(), handle)
	}()

	if err := awaitGeneration(ctx, handle, videoAdapter.GetStatus); err != nil {
		return err
	}

	media, err := videoAdapter.GetResult(ctx, handle)
	if err != nil {
		return err
	}

	return storeMediaResult(ctx, artifacts, outputKey, media)
}

func awaitGeneration(
	ctx context.Context,
	handle provider.GenerationHandle,
	statusFn func(context.Context, provider.GenerationHandle) (provider.GenerationStatus, error),
) error {
	for {
		status, err := statusFn(ctx, handle)
		if err != nil {
			return err
		}

		switch status.State {
		case provider.GenerationSucceeded:
			return nil
		case provider.GenerationFailed:
			return fmt.Errorf("mock generation failed: %s", status.Failure)
		case provider.GenerationCancelled:
			return context.Canceled
		}

		timer := time.NewTimer(pollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func storeMediaResult(
	ctx context.Context,
	artifacts storage.Store,
	outputKey string,
	media provider.MediaResult,
) error {
	putErr := artifacts.Put(ctx, outputKey, media.Body)
	closeErr := media.Body.Close()

	return errors.Join(putErr, closeErr)
}

// ValidateScript checks an existing Mock LLM script JSON artifact without modifying it.
func ValidateScript(ctx context.Context, artifacts storage.Store, inputKey string) error {
	data, err := readBoundedArtifact(ctx, artifacts, inputKey)
	if err != nil {
		return err
	}

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()

	var script ScriptOutput
	if err := decoder.Decode(&script); err != nil {
		return fmt.Errorf("decode mock script: %w", err)
	}

	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		return fmt.Errorf("decode mock script: unexpected trailing JSON data")
	}

	if strings.TrimSpace(script.Prompt) == "" || strings.TrimSpace(script.Script) == "" || len(script.Scenes) == 0 {
		return fmt.Errorf("invalid mock script output")
	}

	return nil
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

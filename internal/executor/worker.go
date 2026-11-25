// Package executor runs individual workflow steps inside worker containers.
package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"strings"
	"time"

	"github.com/tnqbao/kubereel/internal/mock"
	"github.com/tnqbao/kubereel/internal/provider"
	"github.com/tnqbao/kubereel/internal/provider/fal"
	"github.com/tnqbao/kubereel/internal/provider/gemini"
	"github.com/tnqbao/kubereel/internal/storage"
)

const (
	EnvProviderAPIKey    = "PROVIDER_API_KEY"
	UncertainOutcomeFile = "uncertain.json"

	ProviderMock   = "mock"
	ProviderGemini = "gemini"
	ProviderFal    = "fal"

	maxRequestBytes  = 1 << 20
	falPollInterval  = 500 * time.Millisecond
	cancelGraceLimit = 5 * time.Second
)

// StepRequest describes the persisted JSON input for a worker step Job.
type StepRequest struct {
	Provider       string `json:"provider,omitempty"`
	Type           string `json:"type,omitempty"`
	Model          string `json:"model,omitempty"`
	BaseURL        string `json:"baseURL,omitempty"`
	ImageInput     string `json:"imageInput,omitempty"`
	TimeoutSeconds int64  `json:"timeoutSeconds,omitempty"`
	Prompt         string `json:"prompt"`
}

type uncertainRecord struct {
	Reason string `json:"reason"`
}

// Execute runs the requested provider step and publishes its output artifact.
func Execute(ctx context.Context, artifacts storage.Store, inputKey, outputKey string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if artifacts == nil {
		return fmt.Errorf("artifact storage is required")
	}

	raw, err := readBoundedRequest(ctx, artifacts, inputKey)
	if err != nil {
		return err
	}

	req, err := decodeStepRequest(raw)
	if err != nil {
		return err
	}

	switch req.Provider {
	case "", ProviderMock:
		return mock.Execute(ctx, artifacts, inputKey, outputKey)
	case ProviderGemini:
		return executeGeminiTextStep(ctx, artifacts, req, outputKey, nil)
	case ProviderFal:
		return executeFalStep(ctx, artifacts, req, outputKey)
	default:
		return fmt.Errorf("unsupported step provider %q", req.Provider)
	}
}

// HasUncertainOutcome reports whether a failed step Job recorded an uncertain provider submission.
func HasUncertainOutcome(ctx context.Context, artifacts storage.Store, workflowID string) (bool, error) {
	key := path.Join("workflows", workflowID, UncertainOutcomeFile)
	object, err := artifacts.Open(ctx, key)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect uncertain marker: %w", err)
	}

	return true, object.Close()
}

// ValidateImageArtifact checks an SVG or raster (PNG/JPEG/WebP) image artifact in storage.
func ValidateImageArtifact(ctx context.Context, artifacts storage.Store, key string) error {
	if strings.HasSuffix(key, ".svg") {
		return mock.ValidateSVG(ctx, artifacts, key)
	}

	object, err := artifacts.Open(ctx, key)
	if err != nil {
		return fmt.Errorf("open image artifact: %w", err)
	}
	defer object.Close()

	var header [12]byte
	n, err := io.ReadFull(object, header[:])
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
		return fmt.Errorf("read image header: %w", err)
	}

	data := header[:n]
	if isValidRasterHeader(data) {
		return nil
	}

	return fmt.Errorf("invalid raster image artifact")
}

func isValidRasterHeader(data []byte) bool {
	if len(data) >= 8 && bytes.Equal(data[:8], []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}) {
		return true
	}
	if len(data) >= 3 && data[0] == 0xff && data[1] == 0xd8 && data[2] == 0xff {
		return true
	}
	if len(data) >= 12 && bytes.Equal(data[:4], []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WEBP")) {
		return true
	}

	return false
}

func executeGeminiTextStep(
	ctx context.Context,
	artifacts storage.Store,
	req StepRequest,
	outputKey string,
	customProvider provider.TextProvider,
) error {
	if req.Type != mock.StepLLM {
		return fmt.Errorf("gemini provider currently supports only %q steps", mock.StepLLM)
	}

	textProvider, err := resolveGeminiProvider(req, customProvider)
	if err != nil {
		return err
	}

	result, err := textProvider.Generate(ctx, provider.TextRequest{
		Model:  req.Model,
		Prompt: req.Prompt,
	})
	if err != nil {
		return handleSubmitError(artifacts, outputKey, err)
	}

	trimmedPrompt := strings.TrimSpace(req.Prompt)
	output := mock.ScriptOutput{
		Prompt: trimmedPrompt,
		Script: result.Text,
		Scenes: deriveScenes(result.Text, trimmedPrompt),
	}

	encoded, err := json.Marshal(output)
	if err != nil {
		return fmt.Errorf("encode gemini script output: %w", err)
	}

	return artifacts.Put(ctx, outputKey, bytes.NewReader(encoded))
}

func resolveGeminiProvider(req StepRequest, customProvider provider.TextProvider) (provider.TextProvider, error) {
	if customProvider != nil {
		return customProvider, nil
	}

	return gemini.New(gemini.Config{
		APIKey:       os.Getenv(EnvProviderAPIKey),
		BaseURL:      req.BaseURL,
		DefaultModel: req.Model,
		Timeout:      time.Duration(req.TimeoutSeconds) * time.Second,
	})
}

func executeFalStep(ctx context.Context, artifacts storage.Store, req StepRequest, outputKey string) error {
	adapter, err := fal.New(fal.Config{
		APIKey:       os.Getenv(EnvProviderAPIKey),
		BaseURL:      req.BaseURL,
		DefaultModel: req.Model,
		Timeout:      time.Duration(req.TimeoutSeconds) * time.Second,
	})
	if err != nil {
		return err
	}

	switch req.Type {
	case mock.StepImage:
		return runFalImageStep(ctx, artifacts, adapter.Image(), req, outputKey, falPollInterval)
	case mock.StepVideo:
		return runFalVideoStep(ctx, artifacts, adapter.Video(), req, outputKey, falPollInterval)
	default:
		return fmt.Errorf("fal provider supports only %q and %q steps", mock.StepImage, mock.StepVideo)
	}
}

func runFalImageStep(
	ctx context.Context,
	artifacts storage.Store,
	imgProvider provider.ImageProvider,
	req StepRequest,
	outputKey string,
	pollDelay time.Duration,
) error {
	handle, err := imgProvider.Submit(ctx, provider.ImageRequest{
		Model:  req.Model,
		Prompt: req.Prompt,
	})
	if err != nil {
		return handleSubmitError(artifacts, outputKey, err)
	}

	if err := pollAsyncGeneration(ctx, handle, imgProvider.GetStatus, imgProvider, pollDelay); err != nil {
		return err
	}

	media, err := imgProvider.GetResult(ctx, handle)
	if err != nil {
		return err
	}

	return publishMediaStream(ctx, artifacts, outputKey, media)
}

func runFalVideoStep(
	ctx context.Context,
	artifacts storage.Store,
	vidProvider provider.VideoProvider,
	req StepRequest,
	outputKey string,
	pollDelay time.Duration,
) error {
	var imageSource io.Reader
	if req.ImageInput != "" {
		imgObject, err := artifacts.Open(ctx, req.ImageInput)
		if err != nil {
			return fmt.Errorf("open reference image: %w", err)
		}
		defer imgObject.Close()
		imageSource = imgObject
	}

	handle, err := vidProvider.Submit(ctx, provider.VideoRequest{
		Model:  req.Model,
		Prompt: req.Prompt,
		Image:  imageSource,
	})
	if err != nil {
		return handleSubmitError(artifacts, outputKey, err)
	}

	if err := pollAsyncGeneration(ctx, handle, vidProvider.GetStatus, vidProvider, pollDelay); err != nil {
		return err
	}

	media, err := vidProvider.GetResult(ctx, handle)
	if err != nil {
		return err
	}

	return publishMediaStream(ctx, artifacts, outputKey, media)
}

func handleSubmitError(artifacts storage.Store, outputKey string, err error) error {
	if errors.Is(err, provider.ErrSubmissionUncertain) {
		markerErr := writeUncertainMarker(artifacts, outputKey)
		return errors.Join(err, markerErr)
	}

	return err
}

func pollAsyncGeneration(
	ctx context.Context,
	handle provider.GenerationHandle,
	statusFn func(context.Context, provider.GenerationHandle) (provider.GenerationStatus, error),
	providerImpl any,
	pollDelay time.Duration,
) error {
	for {
		status, err := statusFn(ctx, handle)
		if err != nil {
			return handlePollError(ctx, err, providerImpl, handle)
		}

		switch status.State {
		case provider.GenerationSucceeded:
			return nil
		case provider.GenerationFailed:
			return fmt.Errorf("provider generation failed: %s", status.Failure)
		case provider.GenerationCancelled:
			return context.Canceled
		}

		timer := time.NewTimer(pollDelay)
		select {
		case <-ctx.Done():
			timer.Stop()
			cancelAsyncBestEffort(providerImpl, handle)
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func handlePollError(ctx context.Context, err error, providerImpl any, handle provider.GenerationHandle) error {
	if ctx.Err() == nil {
		return err
	}

	cancelAsyncBestEffort(providerImpl, handle)
	return ctx.Err()
}

func cancelAsyncBestEffort(providerImpl any, handle provider.GenerationHandle) {
	canceller, ok := providerImpl.(provider.Canceller)
	if !ok {
		return
	}

	cancelCtx, cancel := context.WithTimeout(context.Background(), cancelGraceLimit)
	defer cancel()
	_ = canceller.Cancel(cancelCtx, handle)
}

func publishMediaStream(ctx context.Context, artifacts storage.Store, outputKey string, media provider.MediaResult) error {
	putErr := artifacts.Put(ctx, outputKey, media.Body)
	closeErr := media.Body.Close()
	return errors.Join(putErr, closeErr)
}

func writeUncertainMarker(artifacts storage.Store, outputKey string) error {
	payload, err := json.Marshal(uncertainRecord{Reason: provider.ErrSubmissionUncertain.Error()})
	if err != nil {
		return err
	}

	markerKey := path.Join(path.Dir(outputKey), UncertainOutcomeFile)
	markerCtx, cancel := context.WithTimeout(context.Background(), cancelGraceLimit)
	defer cancel()

	return artifacts.Put(markerCtx, markerKey, bytes.NewReader(payload))
}

func deriveScenes(scriptText, fallbackPrompt string) []string {
	var scenes []string
	for _, line := range strings.Split(scriptText, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" {
			scenes = append(scenes, trimmed)
		}
		if len(scenes) >= 8 {
			break
		}
	}

	if len(scenes) > 0 {
		return scenes
	}

	return []string{fallbackPrompt}
}

func decodeStepRequest(data []byte) (StepRequest, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()

	var req StepRequest
	if err := decoder.Decode(&req); err != nil {
		return StepRequest{}, fmt.Errorf("decode worker request: %w", err)
	}

	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		return StepRequest{}, fmt.Errorf("decode worker request: unexpected trailing JSON data")
	}

	return req, nil
}

func readBoundedRequest(ctx context.Context, artifacts storage.Store, key string) ([]byte, error) {
	object, err := artifacts.Open(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("open worker request: %w", err)
	}

	data, readErr := io.ReadAll(io.LimitReader(object, maxRequestBytes+1))
	if err := errors.Join(readErr, object.Close()); err != nil {
		return nil, fmt.Errorf("read worker request: %w", err)
	}

	if len(data) > maxRequestBytes {
		return nil, fmt.Errorf("worker request exceeds %d bytes", maxRequestBytes)
	}

	return data, nil
}

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
	"github.com/tnqbao/kubereel/internal/provider/gemini"
	"github.com/tnqbao/kubereel/internal/storage"
)

const (
	EnvProviderAPIKey    = "PROVIDER_API_KEY"
	UncertainOutcomeFile = "uncertain.json"

	ProviderMock   = "mock"
	ProviderGemini = "gemini"

	maxRequestBytes = 1 << 20
)

// StepRequest describes the persisted JSON input for a worker step Job.
type StepRequest struct {
	Provider       string `json:"provider,omitempty"`
	Type           string `json:"type,omitempty"`
	Model          string `json:"model,omitempty"`
	BaseURL        string `json:"baseURL,omitempty"`
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

	textProvider := customProvider
	if textProvider == nil {
		constructed, err := gemini.New(gemini.Config{
			APIKey:       os.Getenv(EnvProviderAPIKey),
			BaseURL:      req.BaseURL,
			DefaultModel: req.Model,
			Timeout:      time.Duration(req.TimeoutSeconds) * time.Second,
		})
		if err != nil {
			return err
		}
		textProvider = constructed
	}

	result, err := textProvider.Generate(ctx, provider.TextRequest{
		Model:  req.Model,
		Prompt: req.Prompt,
	})
	if err != nil {
		if errors.Is(err, provider.ErrSubmissionUncertain) {
			markerErr := writeUncertainMarker(artifacts, outputKey)
			return errors.Join(err, markerErr)
		}
		return err
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

func writeUncertainMarker(artifacts storage.Store, outputKey string) error {
	payload, err := json.Marshal(uncertainRecord{Reason: provider.ErrSubmissionUncertain.Error()})
	if err != nil {
		return err
	}

	markerKey := path.Join(path.Dir(outputKey), UncertainOutcomeFile)
	markerCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
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
